package pdcli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Request describes one CLI invocation.
type Request struct {
	// Args are passed verbatim (no shell). They must start with "<group> <command>".
	Args []string
	// Timeout bounds the whole run; zero means only ctx bounds it.
	Timeout time.Duration
	// MaxStdout caps captured stdout; zero means DefaultMaxStdout.
	MaxStdout int
	// Env adds or overrides environment variables (after the allow-list).
	Env map[string]string
	// OnStdoutLine, if set, is called for each stdout line as it arrives.
	// It runs on the output-copying goroutine and must not block.
	OnStdoutLine func(line string)
}

// Result is what a finished process produced.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration
}

// Runner executes CLI invocations. ExecRunner is the real one; tests use fakes.
type Runner interface {
	// Run starts the process, waits for it and returns its output. A non-nil
	// error is always a *Error; Result is populated as far as it is known,
	// including on non-zero exit (transfers print a summary before failing).
	Run(ctx context.Context, req Request) (Result, error)
}

// Output caps.
const (
	DefaultMaxStdout = 1 << 20  // 1 MiB: single objects and batch results
	ListMaxStdout    = 64 << 20 // 64 MiB: very large folders
	maxStderr        = 256 << 10
	defaultGrace     = 5 * time.Second
)

var (
	errTimeout  = errors.New("timed out")
	errOverflow = errors.New("output too large")
	errShutdown = errors.New("application is shutting down")
)

// ExecRunner runs the real binary.
type ExecRunner struct {
	// Binary is the absolute path to proton-drive.
	Binary string
	// Table tracks running processes; required.
	Table *ProcessTable
	// Grace is the SIGTERM→SIGKILL delay; zero means 5 s.
	Grace time.Duration
	// Environ returns the parent environment; nil means os.Environ.
	Environ func() []string
	// Dir is the working directory; empty means the user's home directory.
	Dir string
	// ExtraEnv is applied after the allow-list on every run (e.g. the log level).
	// Request.Env overrides it.
	ExtraEnv map[string]string
	// Logger receives the events.lock repairs; nil means slog.Default().
	Logger *slog.Logger
}

// Run implements Runner. A crash caused by a corrupt events.lock is
// repaired and the command started once more: the CLI crashes on it at
// startup, before doing any work. Kills by signal are never retried.
func (r *ExecRunner) Run(ctx context.Context, req Request) (Result, error) {
	environ := os.Environ
	if r.Environ != nil {
		environ = r.Environ
	}
	env := buildEnv(environ(), mergeEnv(r.ExtraEnv, req.Env))
	res, err := r.run(ctx, req, env)
	var e *Error
	if errors.As(err, &e) && e.Kind == KindCrash && e.ExitCode > 0 {
		log := r.Logger
		if log == nil {
			log = slog.Default()
		}
		if repairEventsLock(ctx, log, eventsLockPath(env)) {
			first := res.Duration
			res, err = r.run(ctx, req, env)
			res.Duration += first
		}
	}
	return res, err
}

func (r *ExecRunner) run(ctx context.Context, req Request, env []string) (Result, error) {
	command := CommandName(req.Args)
	if r.Binary == "" {
		return Result{}, &Error{Kind: KindBinaryMissing, Command: command, Message: "Proton Drive CLI not configured"}
	}
	if r.Table == nil {
		return Result{}, &Error{Kind: KindUnknown, Command: command, Message: "pdcli: ExecRunner.Table is nil"}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, &Error{Kind: KindCancelled, Command: command, Err: err}
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if req.Timeout > 0 {
		var cancelT context.CancelFunc
		runCtx, cancelT = context.WithTimeoutCause(runCtx, req.Timeout, errTimeout)
		defer cancelT()
	}

	id, _, ok := r.Table.add(func() { cancel(errShutdown) })
	if !ok {
		return Result{}, &Error{Kind: KindCancelled, Command: command, Err: errShutdown}
	}
	defer r.Table.remove(id)

	maxOut := req.MaxStdout
	if maxOut <= 0 {
		maxOut = DefaultMaxStdout
	}
	stdout := &limitedBuffer{max: maxOut, onOverflow: func() { cancel(errOverflow) }, onLine: req.OnStdoutLine}
	stderr := &limitedBuffer{max: maxStderr}

	grace := r.Grace
	if grace <= 0 {
		grace = defaultGrace
	}

	cmd := exec.CommandContext(runCtx, r.Binary, req.Args...)
	cmd.Env = env
	cmd.Dir = r.Dir
	if cmd.Dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cmd.Dir = home
		}
	}
	cmd.Stdin = nil // /dev/null: the CLI can never read an answer to a prompt
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Own session: no controlling terminal, and its own process group so that
	// cancellation reaches everything it spawned. Pdeathsig kills it if Quark dies.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}

	var (
		killMu    sync.Mutex
		killTimer *time.Timer
	)
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		err := syscall.Kill(-pid, syscall.SIGTERM)
		killMu.Lock()
		if killTimer == nil {
			killTimer = time.AfterFunc(grace, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
		}
		killMu.Unlock()
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	// Backstop: if something still holds the pipes after the kill, stop waiting.
	cmd.WaitDelay = grace + 2*time.Second

	start := time.Now()
	runErr := cmd.Run()
	killMu.Lock()
	if killTimer != nil {
		killTimer.Stop()
	}
	killMu.Unlock()

	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: -1, Duration: time.Since(start)}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	// A clean exit with complete output is a success even if ctx was cancelled
	// a moment later: the command did take effect.
	if runErr == nil && !stdout.Overflowed() {
		return res, nil
	}
	// Otherwise our own cancellation reasons take precedence over the exit status.
	if runCtx.Err() != nil {
		cause := context.Cause(runCtx)
		switch {
		case errors.Is(cause, errOverflow) || stdout.Overflowed():
			return res, &Error{Kind: KindOutputTooLarge, Command: command, Message: fmt.Sprintf("output exceeded %d bytes", maxOut)}
		case errors.Is(cause, errTimeout):
			return res, &Error{Kind: KindTimeout, Command: command, Message: fmt.Sprintf("no result after %s", req.Timeout)}
		default:
			return res, &Error{Kind: KindCancelled, Command: command, Err: cause}
		}
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			// Start failed: missing binary, permission denied, bad interpreter.
			return res, &Error{Kind: KindBinaryMissing, Command: command, Message: "cannot start the Proton Drive CLI", Err: runErr}
		}
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return res, &Error{
				Kind: KindCrash, Command: command, ExitCode: -1, Stderr: string(res.Stderr),
				Message: fmt.Sprintf("the Proton Drive CLI was killed by signal %s", ws.Signal()),
			}
		}
		e := classify(command, res.ExitCode, res.Stderr)
		if e.Kind == KindCrash {
			// The CLI logs to stdout: the reason of a crash is usually there.
			e.Stderr += "\n[stdout tail] " + tailOf(res.Stdout, 1500)
		}
		return res, e
	}
	return res, nil
}

func tailOf(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(stripANSI(string(b)))
}

func mergeEnv(base, over map[string]string) map[string]string {
	if len(base) == 0 {
		return over
	}
	out := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// CommandName returns "<group> <command>" from args, for logs and errors.
// It never includes paths or other user data.
func CommandName(args []string) string {
	var parts []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		parts = append(parts, a)
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, " ")
}
