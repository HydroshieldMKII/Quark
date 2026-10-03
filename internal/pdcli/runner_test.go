package pdcli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"example.org/quark/internal/testutil/fakecli"
)

func TestMain(m *testing.M) {
	fakecli.MaybeRun()
	goleak.VerifyTestMain(m)
}

type fakeEnv struct {
	runner  *ExecRunner
	records string
}

func newFake(t *testing.T, rules ...fakecli.Rule) fakeEnv {
	t.Helper()
	bin, env := fakecli.Setup(t, fakecli.Scenario{Rules: rules})
	return fakeEnv{
		runner: &ExecRunner{
			Binary:   bin,
			Table:    NewProcessTable(),
			Grace:    300 * time.Millisecond,
			ExtraEnv: env,
			Environ:  func() []string { return []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"} },
		},
		records: env["QUARK_FAKE_RECORDS"],
	}
}

func (f fakeEnv) lastRecord(t *testing.T) fakecli.Record {
	t.Helper()
	recs := fakecli.Records(t, f.records)
	if len(recs) == 0 {
		t.Fatal("fake CLI was not run")
	}
	return recs[len(recs)-1]
}

// waitDead fails the test if pid is still alive after d.
func waitDead(t *testing.T, pid int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for fakecli.Alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still alive after %s", pid, d)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func wantKind(t *testing.T, err error, k Kind) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v (%T), want *Error of kind %s", err, err, k)
	}
	if e.Kind != k {
		t.Fatalf("kind = %s (%v), want %s", e.Kind, e, k)
	}
	return e
}

func TestRunSuccess(t *testing.T) {
	f := newFake(t, fakecli.Rule{Match: []string{"filesystem", "info"}, Stdout: `{"uid":"a"}` + "\n"})
	res, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "info", "--json", "/my-files"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Stdout); got != `{"uid":"a"}`+"\n" {
		t.Fatalf("stdout = %q", got)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	rec := f.lastRecord(t)
	if !slices.Equal(rec.Args, []string{"filesystem", "info", "--json", "/my-files"}) {
		t.Fatalf("args = %q", rec.Args)
	}
}

func TestRunNoTTYAndEmptyStdin(t *testing.T) {
	f := newFake(t, fakecli.Rule{})
	if _, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "list", "/"}}); err != nil {
		t.Fatal(err)
	}
	rec := f.lastRecord(t)
	if rec.HasTTY {
		t.Error("CLI has a controlling terminal; prompts could block")
	}
	if rec.Stdin != "devnull" {
		t.Errorf("stdin = %q, want /dev/null", rec.Stdin)
	}
}

func TestRunEnvironmentIsFiltered(t *testing.T) {
	f := newFake(t, fakecli.Rule{})
	f.runner.Environ = func() []string {
		return []string{
			"HOME=/home/u", "PATH=/usr/bin", "LC_ALL=C", "XDG_RUNTIME_DIR=/run/user/1",
			"PROTON_DRIVE_BASE_URL=evil.example", "PROTON_DRIVE_UNSAFE_CACHE=1",
			"NODE_OPTIONS=--inspect", "AWS_SECRET_ACCESS_KEY=x", "DBUS_SESSION_BUS_ADDRESS=unix:path=/x",
		}
	}
	_, err := f.runner.Run(context.Background(), Request{
		Args: []string{"filesystem", "list", "/"},
		Env:  map[string]string{"PROTON_DRIVE_UNSAFE_CACHE": "1", "PROTON_DRIVE_LOG_LEVEL": "ERROR"},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := f.lastRecord(t).Env
	for _, want := range []string{"HOME=/home/u", "LC_ALL=C", "XDG_RUNTIME_DIR=/run/user/1", "NO_COLOR=1", "PROTON_DRIVE_LOG_LEVEL=ERROR", "DBUS_SESSION_BUS_ADDRESS=unix:path=/x"} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
	for _, bad := range []string{"PROTON_DRIVE_BASE_URL", "PROTON_DRIVE_UNSAFE_CACHE", "NODE_OPTIONS", "AWS_SECRET_ACCESS_KEY"} {
		for _, kv := range env {
			if strings.HasPrefix(kv, bad+"=") {
				t.Errorf("env leaks %q", kv)
			}
		}
	}
}

func TestRunClassifiesFailures(t *testing.T) {
	f := newFake(t,
		fakecli.Rule{Match: []string{"filesystem", "list"}, Stderr: "You need to login first\n", Exit: 1},
		fakecli.Rule{Match: []string{"filesystem", "info"}, Stderr: "Expected 1 arguments, got 0\n", Exit: 2},
		fakecli.Rule{Match: []string{"filesystem", "rename"}, Stderr: crashMarker + "\nTrace: boom\n    at x\n", Exit: 1},
	)
	ctx := context.Background()
	_, err := f.runner.Run(ctx, Request{Args: []string{"filesystem", "list", "/"}})
	e := wantKind(t, err, KindNotLoggedIn)
	if e.Command != "filesystem list" || e.ExitCode != 1 {
		t.Errorf("error = %+v", e)
	}
	_, err = f.runner.Run(ctx, Request{Args: []string{"filesystem", "info"}})
	wantKind(t, err, KindUsage)
	_, err = f.runner.Run(ctx, Request{Args: []string{"filesystem", "rename", "a", "b"}})
	wantKind(t, err, KindCrash)
}

func TestRunPartialFailureKeepsStdout(t *testing.T) {
	summary := `{"transferredItems":1,"transferredBytes":5,"skippedItems":0,"failedItems":1,"failures":[]}` + "\n"
	f := newFake(t, fakecli.Rule{Stdout: summary, Stderr: "1 item(s) failed to upload\n", Exit: 1})
	res, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "upload", "a", "/my-files"}})
	wantKind(t, err, KindPartial)
	if string(res.Stdout) != summary {
		t.Fatalf("stdout lost on failure: %q", res.Stdout)
	}
}

func TestRunTimeoutKillsProcess(t *testing.T) {
	f := newFake(t, fakecli.Rule{Hang: true})
	start := time.Now()
	_, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "list", "/"}, Timeout: 200 * time.Millisecond})
	wantKind(t, err, KindTimeout)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("timeout took %s", d)
	}
	waitDead(t, f.lastRecord(t).Pid, time.Second)
	if n := f.runner.Table.Len(); n != 0 {
		t.Fatalf("table still tracks %d processes", n)
	}
}

func TestRunCancelKillsProcessGroup(t *testing.T) {
	f := newFake(t, fakecli.Rule{Hang: true, SpawnChild: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := f.runner.Run(ctx, Request{Args: []string{"filesystem", "download", "/x", "/tmp"}})
		done <- err
	}()
	rec := waitForRecord(t, f)
	cancel()
	wantKind(t, <-done, KindCancelled)
	waitDead(t, rec.Pid, time.Second)
	if rec.ChildPid == 0 {
		t.Fatal("fake did not spawn its child")
	}
	waitDead(t, rec.ChildPid, 2*time.Second)
}

func TestRunSIGTERMIgnoredEscalatesToSIGKILL(t *testing.T) {
	f := newFake(t, fakecli.Rule{Hang: true, IgnoreTerm: true})
	start := time.Now()
	_, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "list", "/"}, Timeout: 100 * time.Millisecond})
	wantKind(t, err, KindTimeout)
	// Timeout (100 ms) + grace (300 ms) + slack.
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("escalation took %s", d)
	}
	waitDead(t, f.lastRecord(t).Pid, time.Second)
}

func TestRunOutputTooLarge(t *testing.T) {
	f := newFake(t, fakecli.Rule{StdoutBytes: 8 << 20, Hang: true})
	res, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "list", "/my-files"}, MaxStdout: 1 << 20})
	wantKind(t, err, KindOutputTooLarge)
	if len(res.Stdout) != 1<<20 {
		t.Fatalf("kept %d bytes, want exactly the cap", len(res.Stdout))
	}
	waitDead(t, f.lastRecord(t).Pid, time.Second)
}

func TestRunMissingBinary(t *testing.T) {
	r := &ExecRunner{Binary: "/nonexistent/proton-drive", Table: NewProcessTable()}
	_, err := r.Run(context.Background(), Request{Args: []string{"filesystem", "list", "/"}})
	wantKind(t, err, KindBinaryMissing)
	_, err = (&ExecRunner{Table: NewProcessTable()}).Run(context.Background(), Request{Args: []string{"x", "y"}})
	wantKind(t, err, KindBinaryMissing)
}

func TestRunAlreadyCancelledContext(t *testing.T) {
	f := newFake(t, fakecli.Rule{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.runner.Run(ctx, Request{Args: []string{"filesystem", "list", "/"}})
	wantKind(t, err, KindCancelled)
}

func TestRunStreamsStdoutLines(t *testing.T) {
	f := newFake(t, fakecli.Rule{Chunks: []fakecli.Chunk{
		{Out: `{"signInUrl":"https://account.proton.me/x"}` + "\n"},
		{Out: "par", DelayMs: 50},
		{Out: "tial\nlast\n", DelayMs: 50},
	}})
	var (
		mu    sync.Mutex
		lines []string
	)
	_, err := f.runner.Run(context.Background(), Request{
		Args: []string{"auth", "login", "--json"},
		OnStdoutLine: func(l string) {
			mu.Lock()
			lines = append(lines, l)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`{"signInUrl":"https://account.proton.me/x"}`, "partial", "last"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %q, want %q", lines, want)
	}
}

func TestShutdownKillsEverythingAndRefusesNewRuns(t *testing.T) {
	f := newFake(t, fakecli.Rule{Hang: true})
	const n = 3
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "list", "/"}})
			errs <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.runner.Table.Len() < n || len(fakecli.Records(t, f.records)) < n {
		if time.Now().After(deadline) {
			t.Fatal("processes did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if left := f.runner.Table.Shutdown(5 * time.Second); left != 0 {
		t.Fatalf("%d processes survived shutdown", left)
	}
	for range n {
		wantKind(t, <-errs, KindCancelled)
	}
	for _, rec := range fakecli.Records(t, f.records) {
		waitDead(t, rec.Pid, time.Second)
	}
	_, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "list", "/"}})
	wantKind(t, err, KindCancelled)
}

func waitForRecord(t *testing.T, f fakeEnv) fakecli.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if recs := fakecli.Records(t, f.records); len(recs) > 0 {
			return recs[len(recs)-1]
		}
		if time.Now().After(deadline) {
			t.Fatal("fake CLI did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCommandName(t *testing.T) {
	tests := map[string][]string{
		"filesystem list": {"filesystem", "list", "--json", "/my-files/secret"},
		"auth login":      {"auth", "--json", "login"},
		"":                {},
		"sharing":         {"sharing"},
	}
	for want, args := range tests {
		if got := CommandName(args); got != want {
			t.Errorf("CommandName(%q) = %q, want %q", args, got, want)
		}
	}
}
