package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
	"example.org/quark/internal/settings"
)

func TestStartNoBinary(t *testing.T) {
	h := newHarness(t)
	h.binary = ""
	h.app.Start()
	s := h.app.Session()
	if s.Phase != PhaseNoBinary || len(s.BinaryHints) != 1 {
		t.Fatalf("session = %+v", s)
	}
	if len(h.be.Calls()) != 0 {
		t.Fatal("backend used without a binary")
	}
}

func TestStartReadyAndUntestedVersion(t *testing.T) {
	h := newHarness(t)
	h.be.set(func(f *fakeBackend) {
		f.version = func() (pdcli.Version, error) {
			return pdcli.Version{Major: 0, Minor: 9, Patch: 1, Raw: "cli-drive@0.9.1", Newer: "0.9.2"}, nil
		}
	})
	h.ready()
	s := h.app.Session()
	if s.CLIVersion != "0.9.1" || !s.CLIUntested || s.NewerCLI != "0.9.2" || s.Err != nil {
		t.Fatalf("session = %+v", s)
	}
	if !slices.Equal(h.phases, []Phase{PhaseStarting, PhaseReady}) {
		t.Fatalf("phases = %v", h.phases)
	}
}

func TestStartFailures(t *testing.T) {
	tests := []struct {
		name    string
		version error
		roots   error
		want    Phase
		action  Action
	}{
		{"logged out", nil, &pdcli.Error{Kind: pdcli.KindNotLoggedIn}, PhaseLoggedOut, ActionNone},
		{"too old", &pdcli.Error{Kind: pdcli.KindIncompatibleVersion, Message: "too old"}, nil, PhaseIncompatible, ActionChooseCLI},
		{"cannot start", &pdcli.Error{Kind: pdcli.KindBinaryMissing}, nil, PhaseNoBinary, ActionChooseCLI},
		{"offline", nil, &pdcli.Error{Kind: pdcli.KindNetwork}, PhaseFailed, ActionRetry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.be.set(func(f *fakeBackend) {
				f.version = func() (pdcli.Version, error) { return pdcli.Version{Raw: "x"}, tt.version }
			})
			h.be.set(func(f *fakeBackend) { f.roots = func() ([]string, error) { return nil, tt.roots } })
			h.app.Start()
			h.until("result", func() bool { return h.app.Session().Phase != PhaseStarting })
			s := h.app.Session()
			if s.Phase != tt.want {
				t.Fatalf("phase = %s", s.Phase)
			}
			if tt.action != ActionNone && (s.Err == nil || s.Err.Action != tt.action) {
				t.Fatalf("err = %+v", s.Err)
			}
		})
	}
}

func TestStaleStartIsIgnored(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	entered := make(chan struct{})
	var block atomic.Bool
	block.Store(true)
	h.be.set(func(f *fakeBackend) {
		f.roots = func() ([]string, error) {
			if block.Load() {
				close(entered)
				<-release
				return nil, &pdcli.Error{Kind: pdcli.KindNetwork}
			}
			return []string{"/my-files"}, nil
		}
	})
	h.app.Start()
	<-entered // the first Start is now blocked inside the backend
	block.Store(false)
	h.app.Start()
	h.until("second start", func() bool { return h.app.Session().Phase == PhaseReady })
	close(release)
	// Wait until the first Start's result has been delivered to the UI thread.
	h.until("first start returns", func() bool { return h.be.InFlight("read") == 0 })
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		h.loop.Drain()
		time.Sleep(5 * time.Millisecond)
	}
	if h.app.Session().Phase != PhaseReady {
		t.Fatalf("stale failure overwrote session: %+v", h.app.Session())
	}
}

func TestLoginFlow(t *testing.T) {
	h := newHarness(t)
	loggedIn := atomic.Bool{}
	h.be.set(func(f *fakeBackend) {
		f.roots = func() ([]string, error) {
			if !loggedIn.Load() {
				return nil, &pdcli.Error{Kind: pdcli.KindNotLoggedIn}
			}
			return []string{"/my-files"}, nil
		}
	})
	proceed := make(chan struct{})
	h.be.set(func(f *fakeBackend) {
		f.login = func(_ context.Context, onURL func(string)) error {
			onURL("https://account.proton.me/desktop/login?x")
			<-proceed
			loggedIn.Store(true)
			return nil
		}
	})
	h.app.Start()
	h.until("logged out", func() bool { return h.app.Session().Phase == PhaseLoggedOut })
	h.app.Login()
	h.until("url", func() bool { return h.app.Session().LoginURL != "" })
	if s := h.app.Session(); s.Phase != PhaseLoggingIn || s.LoginURL != "https://account.proton.me/desktop/login?x" {
		t.Fatalf("session = %+v", s)
	}
	h.app.Login() // ignored while logging in
	close(proceed)
	h.until("ready", func() bool { return h.app.Session().Phase == PhaseReady })
	if c := strings.Count(strings.Join(h.be.Calls(), " "), "login"); c != 1 {
		t.Fatalf("login ran %d times", c)
	}
}

func TestLoginCancelAndFailure(t *testing.T) {
	h := newHarness(t)
	h.be.set(func(f *fakeBackend) {
		f.roots = func() ([]string, error) { return nil, &pdcli.Error{Kind: pdcli.KindNotLoggedIn} }
	})
	h.be.set(func(f *fakeBackend) {
		f.login = func(ctx context.Context, _ func(string)) error {
			<-ctx.Done()
			return &pdcli.Error{Kind: pdcli.KindCancelled}
		}
	})
	h.app.Start()
	h.until("logged out", func() bool { return h.app.Session().Phase == PhaseLoggedOut })
	h.app.Login()
	h.until("logging in", func() bool { return h.app.Session().Phase == PhaseLoggingIn })
	h.app.CancelLogin()
	h.until("cancelled", func() bool { return h.app.Session().Phase == PhaseLoggedOut })
	if h.app.Session().Err != nil {
		t.Fatalf("cancel shown as error: %+v", h.app.Session().Err)
	}

	h.be.set(func(f *fakeBackend) {
		f.login = func(context.Context, func(string)) error { return &pdcli.Error{Kind: pdcli.KindTimeout} }
	})
	h.app.Login()
	h.until("failed login", func() bool { s := h.app.Session(); return s.Phase == PhaseLoggedOut && s.Err != nil })
	if h.app.Session().Err.Action != ActionRetry {
		t.Fatalf("err = %+v", h.app.Session().Err)
	}
}

func TestLogout(t *testing.T) {
	h := newHarness(t)
	h.ready()
	out := atomic.Bool{}
	h.be.set(func(f *fakeBackend) { f.logout = func() error { out.Store(true); return nil } })
	h.be.set(func(f *fakeBackend) {
		f.roots = func() ([]string, error) {
			if out.Load() {
				return nil, &pdcli.Error{Kind: pdcli.KindNotLoggedIn}
			}
			return []string{"/my-files"}, nil
		}
	})
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	h.be.set(func(f *fakeBackend) {
		f.list = func(context.Context, string) ([]drive.Node, error) {
			return []drive.Node{node("u1", "a", drive.TypeFile)}, nil
		}
	})
	b.Navigate(b.State().Location)
	h.until("listed", func() bool { return b.State().Total == 1 })
	h.app.Logout()
	h.until("logged out", func() bool { return h.app.Session().Phase == PhaseLoggedOut })
	if b.State().Total != 0 {
		t.Fatal("listing kept after logout")
	}
}

func TestAuthLossDuringListing(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.list = func(context.Context, string) ([]drive.Node, error) {
			return nil, &pdcli.Error{Kind: pdcli.KindNotLoggedIn}
		}
	})
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	b.Refresh()
	h.until("logged out", func() bool { return h.app.Session().Phase == PhaseLoggedOut })
}

func TestPanicInBackendIsContained(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.set(func(f *fakeBackend) { f.list = func(context.Context, string) ([]drive.Node, error) { panic("boom") } })
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	b.Refresh()
	h.until("error", func() bool { return b.State().Err != nil })
	if e := b.State().Err; e.Kind != "internal" || b.State().Loading {
		t.Fatalf("state = %+v", b.State())
	}
}

func TestReadAndWriteConcurrencyCaps(t *testing.T) {
	h := newHarness(t)
	h.ready()
	release := make(chan struct{})
	h.be.set(func(f *fakeBackend) {
		f.list = func(ctx context.Context, _ string) ([]drive.Node, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, nil
		}
	})
	var browsers []*Browser
	for i := range 6 {
		b := h.app.NewBrowser(Root(drive.SectionMyFiles))
		browsers = append(browsers, b)
		b.Navigate(Location{Section: drive.SectionMyFiles, Trail: []Crumb{{UID: string(rune('a' + i)), Name: "f"}}})
	}
	h.until("3 reads in flight", func() bool { return h.be.Peak("read") == 3 })
	time.Sleep(50 * time.Millisecond)
	if p := h.be.Peak("read"); p != maxReads {
		t.Fatalf("peak reads = %d", p)
	}
	close(release)
	h.until("all listed", func() bool {
		for _, b := range browsers {
			if b.State().Loading {
				return false
			}
		}
		return true
	})

	// Writes are serialized.
	gate := make(chan struct{})
	h.be.set(func(f *fakeBackend) {
		f.create = func(_, name string) (drive.Node, error) {
			<-gate
			return drive.Node{Name: name}, nil
		}
	})
	for i := range 4 {
		h.app.CreateFolder(Root(drive.SectionMyFiles), "f"+string(rune('0'+i)))
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	h.until("creates done", func() bool { return len(h.toasts) >= 4 })
	if p := h.be.Peak("write"); p != maxWrites {
		t.Fatalf("peak writes = %d", p)
	}
}

func TestShutdownStopsWorkAndCallbacks(t *testing.T) {
	h := newHarness(t)
	h.ready()
	started := make(chan struct{})
	h.be.set(func(f *fakeBackend) {
		f.list = func(ctx context.Context, _ string) ([]drive.Node, error) {
			close(started)
			<-ctx.Done()
			return nil, &pdcli.Error{Kind: pdcli.KindCancelled}
		}
	})
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	called := false
	b.OnChange(func(BrowserState) { called = true })
	b.Refresh()
	<-started
	called = false
	if !h.app.Shutdown(2 * time.Second) {
		t.Fatal("shutdown timed out")
	}
	h.loop.Drain()
	if called {
		t.Fatal("callback after shutdown")
	}
	// Everything is a no-op after shutdown.
	h.app.Start()
	h.app.CreateFolder(Root(drive.SectionMyFiles), "x")
	h.loop.Drain()
}

func TestUpdateSettingsRestartsOnCLIChange(t *testing.T) {
	h := newHarness(t)
	h.ready()
	s := h.app.Settings()
	s.MaxTransfers = 3
	h.app.UpdateSettings(s)
	if h.backends != 1 || len(h.saved) != 1 {
		t.Fatalf("backends=%d saved=%d", h.backends, len(h.saved))
	}
	s.CLIPath = "/opt/proton-drive"
	h.app.UpdateSettings(s)
	h.until("restarted", func() bool { return h.app.Session().Phase == PhaseReady && h.backends == 2 })
	h.app.UseBinary("/opt/other")
	h.until("restarted again", func() bool { return h.backends == 3 && h.app.Session().Phase == PhaseReady })
	if h.app.Settings().CLIPath != "/opt/other" {
		t.Fatal("UseBinary did not save the path")
	}
}

func TestDescribe(t *testing.T) {
	if Describe(nil) != nil || Describe(context.Canceled) != nil || Describe(&pdcli.Error{Kind: pdcli.KindCancelled}) != nil {
		t.Fatal("cancellations must not be errors")
	}
	for k := pdcli.KindUnknown; k <= pdcli.KindRefused; k++ {
		if k == pdcli.KindCancelled {
			continue
		}
		u := Describe(&pdcli.Error{Kind: k, Message: "m"})
		if u == nil || u.Title == "" || u.Detail == "" || u.Kind != k.String() {
			t.Errorf("Describe(%s) = %+v", k, u)
		}
	}
	u := Describe(errors.New("plain"))
	if u.Kind != "unknown" || u.Detail != "plain" {
		t.Fatalf("plain = %+v", u)
	}
	inner := &UserError{Title: "x"}
	if Describe(inner) != inner {
		t.Fatal("UserError not passed through")
	}
	if (&UserError{Title: "a", Detail: "b"}).Error() != "a: b" {
		t.Fatal("Error()")
	}
	if !errors.Is(Describe(&pdcli.Error{Kind: pdcli.KindNetwork, Err: context.DeadlineExceeded}), context.DeadlineExceeded) {
		t.Fatal("Unwrap")
	}
}

func TestSettingsAccessor(t *testing.T) {
	h := newHarness(t)
	if h.app.Settings().MaxTransfers != settings.Defaults().MaxTransfers {
		t.Fatal("settings")
	}
	if PhaseReady.String() != "ready" {
		t.Fatal("phase name")
	}
}
