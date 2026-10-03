// Package app is Quark's application layer: the session state machine, the
// browser view-models and the file operations. It has no GTK dependency.
//
// Threading rule: every exported method and every callback registered with
// On* must be called on the UI thread (the Dispatcher's thread). Background
// work runs through spawn and reports back through the Dispatcher.
package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"example.org/quark/internal/pdcli"
	"example.org/quark/internal/settings"
)

// Phase is the session state.
type Phase int

// Phases.
const (
	PhaseStarting Phase = iota
	PhaseNoBinary
	PhaseIncompatible
	PhaseLoggedOut
	PhaseLoggingIn
	PhaseReady
	PhaseFailed
)

func (p Phase) String() string {
	return [...]string{"starting", "no-binary", "incompatible", "logged-out", "logging-in", "ready", "failed"}[p]
}

// Session is a snapshot of the session state.
type Session struct {
	Phase       Phase
	CLIVersion  string
	CLIUntested bool
	NewerCLI    string
	// BinaryHints are places where a CLI was found but cannot be used as is.
	BinaryHints []string
	// LoginURL is set while signing in, once the CLI has printed it.
	LoginURL string
	Err      *UserError
}

// Config wires an App.
type Config struct {
	Dispatcher Dispatcher
	Logger     *slog.Logger
	Settings   settings.Settings
	// SaveSettings persists settings; nil disables saving.
	SaveSettings func(settings.Settings) error
	// FindBinary locates the CLI; default pdcli.FindBinary.
	FindBinary func(explicit string) (string, []string)
	// NewBackend builds a backend for a CLI binary.
	NewBackend func(binary string, s settings.Settings) Backend
	// Now is the clock; default time.Now.
	Now func() time.Time
}

// App is the application state. Create it with New and call Start.
type App struct {
	disp       Dispatcher
	log        *slog.Logger
	cfg        Config
	settings   settings.Settings
	backend    Backend
	binary     string
	session    Session
	onSession  []func(Session)
	onToast    []func(Toast)
	browsers   map[*Browser]struct{}
	cache      *listCache
	reads      limiter
	writes     limiter
	rootCtx    context.Context
	rootCancel context.CancelFunc
	loginStop  context.CancelFunc
	startGen   uint64
	wg         sync.WaitGroup
	closed     bool
}

// New creates an App. It does not start anything.
func New(cfg Config) *App {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.FindBinary == nil {
		cfg.FindBinary = pdcli.FindBinary
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &App{
		disp: cfg.Dispatcher, log: cfg.Logger, cfg: cfg, settings: cfg.Settings,
		browsers: map[*Browser]struct{}{}, cache: newListCache(cfg.Now, 30*time.Second),
		reads: newLimiter(maxReads), writes: newLimiter(maxWrites),
		rootCtx: ctx, rootCancel: cancel,
	}
}

// Session returns the current session snapshot.
func (a *App) Session() Session { return a.session }

// Settings returns the current settings.
func (a *App) Settings() settings.Settings { return a.settings }

// OnSession registers a listener for session changes.
func (a *App) OnSession(fn func(Session)) { a.onSession = append(a.onSession, fn) }

func (a *App) setSession(s Session) {
	a.session = s
	if s.Err != nil {
		a.log.Warn("session error", "phase", s.Phase.String(), "kind", s.Err.Kind, "err", s.Err.Technical)
	} else {
		a.log.Info("session", "phase", s.Phase.String())
	}
	for _, fn := range a.onSession {
		fn(s)
	}
}

// Start locates the CLI, checks its version and whether the user is signed
// in. It returns immediately; the result arrives through OnSession.
func (a *App) Start() {
	if a.closed {
		return
	}
	a.cache.clear()
	a.setSession(Session{Phase: PhaseStarting})
	bin, hints := a.cfg.FindBinary(a.settings.CLIPath)
	if bin == "" {
		a.setSession(Session{Phase: PhaseNoBinary, BinaryHints: hints})
		return
	}
	if bin != a.binary || a.backend == nil {
		a.binary = bin
		a.backend = a.cfg.NewBackend(bin, a.settings)
	}
	b := a.backend
	a.startGen++
	gen := a.startGen
	var (
		v     pdcli.Version
		roots []string
	)
	a.spawn(a.rootCtx, "start", a.reads, func(ctx context.Context) error {
		var err error
		if v, err = b.Version(ctx); err != nil {
			return err
		}
		roots, err = b.Roots(ctx)
		return err
	}, func(err error) {
		if gen != a.startGen {
			return // a newer Start superseded this one
		}
		s := Session{CLIVersion: v.String(), CLIUntested: v.Untested(), NewerCLI: v.Newer}
		if v.Raw == "" {
			s.CLIVersion = ""
		}
		switch {
		case err == nil:
			s.Phase = PhaseReady
			a.log.Info("signed in", "sections", len(roots))
		case pdcli.IsKind(err, pdcli.KindNotLoggedIn):
			s.Phase = PhaseLoggedOut
		case pdcli.IsKind(err, pdcli.KindIncompatibleVersion):
			s.Phase, s.Err = PhaseIncompatible, Describe(err)
		case pdcli.IsKind(err, pdcli.KindBinaryMissing):
			s.Phase, s.Err = PhaseNoBinary, Describe(err)
		default:
			s.Phase, s.Err = PhaseFailed, Describe(err)
			if s.Err == nil { // cancelled during shutdown
				return
			}
		}
		a.setSession(s)
	})
}

// UseBinary saves an explicit CLI path and restarts.
func (a *App) UseBinary(path string) {
	a.settings.CLIPath = path
	a.saveSettings()
	a.backend = nil
	a.Start()
}

// Login starts the browser sign-in. The sign-in URL is published through
// the session (LoginURL) as soon as the CLI prints it.
func (a *App) Login() {
	if a.closed || a.backend == nil || (a.session.Phase != PhaseLoggedOut && a.session.Phase != PhaseFailed) {
		return
	}
	ctx, cancel := context.WithCancel(a.rootCtx)
	a.loginStop = cancel
	a.setSession(Session{Phase: PhaseLoggingIn, CLIVersion: a.session.CLIVersion})
	b := a.backend
	a.spawn(ctx, "login", nil, func(ctx context.Context) error {
		return b.Login(ctx, func(u string) {
			a.disp.Dispatch(func() {
				if a.session.Phase == PhaseLoggingIn && !a.closed {
					s := a.session
					s.LoginURL = u
					a.setSession(s)
				}
			})
		})
	}, func(err error) {
		cancel()
		a.loginStop = nil
		if err == nil {
			a.Start()
			return
		}
		s := Session{Phase: PhaseLoggedOut, CLIVersion: a.session.CLIVersion, Err: Describe(err)}
		a.setSession(s)
	})
}

// CancelLogin stops a sign-in in progress.
func (a *App) CancelLogin() {
	if a.loginStop != nil {
		a.loginStop()
	}
}

// Logout signs out (the UI confirms first) and clears cached listings.
func (a *App) Logout() {
	if a.closed || a.backend == nil || a.session.Phase != PhaseReady {
		return
	}
	b := a.backend
	a.spawn(a.rootCtx, "logout", a.writes, b.Logout, func(err error) {
		a.cache.clear()
		for br := range a.browsers {
			br.reset()
		}
		if ue := Describe(err); ue != nil {
			a.toast(Toast{Text: ue.Title, Err: ue})
		}
		a.Start()
	})
}

// handleAuthLoss moves to LoggedOut when any operation reports it.
func (a *App) handleAuthLoss(err error) bool {
	if pdcli.IsKind(err, pdcli.KindNotLoggedIn) && a.session.Phase == PhaseReady {
		a.setSession(Session{Phase: PhaseLoggedOut, CLIVersion: a.session.CLIVersion, Err: Describe(err)})
		return true
	}
	return false
}

// UpdateSettings applies and saves new settings. A change of CLI path restarts the session.
func (a *App) UpdateSettings(s settings.Settings) {
	s.Normalize()
	restart := s.CLIPath != a.settings.CLIPath || s.CLILogLevel != a.settings.CLILogLevel
	a.settings = s
	a.saveSettings()
	if restart {
		a.backend = nil
		a.Start()
	}
}

func (a *App) saveSettings() {
	if a.cfg.SaveSettings == nil {
		return
	}
	if err := a.cfg.SaveSettings(a.settings); err != nil {
		a.log.Error("saving settings", "err", err)
	}
}

// Shutdown cancels all work, waits for background tasks (which kill their
// CLI processes) up to timeout, and stops delivering callbacks. It reports
// whether everything finished in time.
func (a *App) Shutdown(timeout time.Duration) bool {
	a.closed = true
	a.rootCancel()
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		a.log.Error("background tasks did not stop in time")
		return false
	}
}
