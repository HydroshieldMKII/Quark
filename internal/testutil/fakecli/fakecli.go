// Package fakecli is a scriptable stand-in for the proton-drive binary.
//
// A test package enables it with the helper-process pattern: its TestMain calls
// fakecli.MaybeRun() first; when the test binary is re-executed with
// QUARK_FAKE_CLI=1 it behaves as the CLI described by the scenario file and
// exits, without running any tests.
package fakecli

import (
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"example.org/quark/internal/fsutil"
)

// Environment variables understood by the fake.
const (
	EnvActive   = "QUARK_FAKE_CLI"
	EnvScenario = "QUARK_FAKE_SCENARIO"
)

// Rule describes how the fake responds to argv starting with Match.
type Rule struct {
	// Match is compared element-wise to the start of argv; empty matches anything.
	Match []string `json:"match"`
	// Contains requires every listed string to appear somewhere in argv.
	Contains []string `json:"contains,omitempty"`
	Stdout   string   `json:"stdout,omitempty"`
	Stderr   string   `json:"stderr,omitempty"`
	Exit     int      `json:"exit"`
	// Chunks are written to stdout one by one with a delay before each.
	Chunks []Chunk `json:"chunks,omitempty"`
	// StdoutBytes writes this many filler bytes to stdout.
	StdoutBytes int `json:"stdoutBytes,omitempty"`
	// DelayMs sleeps before producing any output.
	DelayMs int `json:"delayMs,omitempty"`
	// Hang blocks forever after producing output (until killed).
	Hang bool `json:"hang,omitempty"`
	// IgnoreTerm ignores SIGTERM, forcing the SIGKILL path.
	IgnoreTerm bool `json:"ignoreTerm,omitempty"`
	// SpawnChild starts a long-lived child in the same process group.
	SpawnChild bool `json:"spawnChild,omitempty"`
	// WriteFile creates this file with WriteFileBytes bytes, slowly (downloads).
	WriteFile      string `json:"writeFile,omitempty"`
	WriteFileBytes int    `json:"writeFileBytes,omitempty"`
	// EventsLock takes the CLI's events.lock first, as CLI 0.8 does: content
	// that is not JSON crashes it before the rule runs, any other content is
	// replaced by {"pid":N}, which is removed again on exit.
	EventsLock bool `json:"eventsLock,omitempty"`
}

// Chunk is a piece of stdout written after DelayMs.
type Chunk struct {
	Out     string `json:"out"`
	DelayMs int    `json:"delayMs,omitempty"`
}

// Scenario is the fake's script. RecordDir, if set, receives one JSON file per run.
type Scenario struct {
	Rules     []Rule `json:"rules"`
	RecordDir string `json:"recordDir,omitempty"`
}

// Record is what the fake saw and did in one run.
type Record struct {
	Args     []string `json:"args"`
	Env      []string `json:"env"`
	Pid      int      `json:"pid"`
	ChildPid int      `json:"childPid,omitempty"`
	Matched  int      `json:"matched"`
	Stdin    string   `json:"stdin"`
	HasTTY   bool     `json:"hasTTY"`
}

// MaybeRun turns the current process into the fake CLI when EnvActive is set.
// It never returns in that case.
func MaybeRun() {
	if os.Getenv(EnvActive) != "1" {
		return
	}
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	data, err := os.ReadFile(os.Getenv(EnvScenario))
	if err != nil {
		os.Stderr.WriteString("fakecli: cannot read scenario: " + err.Error() + "\n")
		return 99
	}
	var sc Scenario
	if err := json.Unmarshal(data, &sc); err != nil {
		os.Stderr.WriteString("fakecli: bad scenario: " + err.Error() + "\n")
		return 99
	}
	idx := -1
	for i, r := range sc.Rules {
		if matches(r, args) {
			idx = i
			break
		}
	}
	rec := Record{Args: args, Env: os.Environ(), Pid: os.Getpid(), Matched: idx, HasTTY: hasTTY()}
	rec.Stdin = readStdin()
	if idx < 0 {
		writeRecord(sc.RecordDir, rec)
		os.Stderr.WriteString("Command not found: fakecli has no rule\n")
		return 1
	}
	r := sc.Rules[idx]
	if r.IgnoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	if r.SpawnChild {
		c := exec.Command("sleep", "300")
		if err := c.Start(); err == nil {
			rec.ChildPid = c.Process.Pid
		}
	}
	writeRecord(sc.RecordDir, rec)

	if r.EventsLock {
		lock, ok := takeEventsLock()
		if !ok {
			os.Stdout.WriteString(lockCrashStdout)
			os.Stderr.WriteString(lockCrashStderr)
			return 1
		}
		defer os.Remove(lock)
	}
	sleepMs(r.DelayMs)
	if r.WriteFile != "" {
		writeSlowly(r.WriteFile, r.WriteFileBytes)
	}
	os.Stdout.WriteString(r.Stdout)
	for _, c := range r.Chunks {
		sleepMs(c.DelayMs)
		os.Stdout.WriteString(c.Out)
	}
	if r.StdoutBytes > 0 {
		filler := []byte(strings.Repeat("x", 64<<10))
		for left := r.StdoutBytes; left > 0; {
			n := min(left, len(filler))
			if _, err := os.Stdout.Write(filler[:n]); err != nil {
				return 98
			}
			left -= n
		}
	}
	os.Stderr.WriteString(r.Stderr)
	if r.Hang {
		select {}
	}
	return r.Exit
}

// The output of CLI 0.8 on a zero-filled events.lock.
const (
	lockCrashStdout = `19 |     const lockPath = eventsLockPath(cacheDir);
20 |     await fs.mkdir(cacheDir, { recursive: true });
21 |
22 |     try {
23 |         const raw = await fs.readFile(lockPath, 'utf8');
24 |         const parsed = JSON.parse(raw) as unknown;
                                 ^
SyntaxError: JSON Parse error: Unrecognized token '\x00'
      at Xp (src/events/lock.ts:24:29)
      at async open (src/events/providerPersisted.ts:20:37)
      at async KO (src/init.ts:51:41)
`
	lockCrashStderr = "===============================================\n"
)

// takeEventsLock resolves the lock like the CLI (PROTON_DRIVE_CACHE_DIR, else
// $XDG_DATA_HOME/proton-drive-cli, else ~/.local/share/proton-drive-cli) and
// writes this process's pid into it. It fails on content that is not JSON.
func takeEventsLock() (string, bool) {
	dir := os.Getenv("PROTON_DRIVE_CACHE_DIR")
	if dir == "" {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(os.Getenv("HOME"), ".local", "share")
		}
		dir = filepath.Join(data, "proton-drive-cli")
	}
	lock := filepath.Join(dir, "events.lock")
	if err := os.MkdirAll(dir, fsutil.DirPrivate); err != nil {
		return lock, false
	}
	if raw, err := os.ReadFile(lock); err == nil {
		if !json.Valid(raw) {
			return lock, false
		}
		_ = os.Remove(lock)
	}
	b, _ := json.Marshal(map[string]int{"pid": os.Getpid()})
	return lock, os.WriteFile(lock, b, fsutil.FilePrivate) == nil
}

func matches(r Rule, args []string) bool {
	if len(r.Match) > len(args) || !slices.Equal(r.Match, args[:len(r.Match)]) {
		return false
	}
	for _, want := range r.Contains {
		if !slices.Contains(args, want) {
			return false
		}
	}
	return true
}

func sleepMs(ms int) {
	if ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

func writeSlowly(path string, n int) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	block := make([]byte, max(1, n/10))
	for written := 0; written < n; written += len(block) {
		_, _ = f.Write(block[:min(len(block), n-written)])
		time.Sleep(20 * time.Millisecond)
	}
}

func readStdin() string {
	st, err := os.Stdin.Stat()
	if err != nil {
		return "error: " + err.Error()
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(st, null) {
		return "devnull"
	}
	if st.Mode()&os.ModeCharDevice != 0 {
		return "chardev" // a terminal or other device: unexpected for the CLI
	}
	buf := make([]byte, 64)
	n, _ := os.Stdin.Read(buf)
	return string(buf[:n])
}

func hasTTY() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func writeRecord(dir string, rec Record) {
	if dir == "" {
		return
	}
	b, _ := json.Marshal(rec)
	name := filepath.Join(dir, time.Now().Format("150405.000000000")+"-"+itoa(rec.Pid)+".json")
	_ = os.WriteFile(name, b, fsutil.FilePrivate)
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// Setup writes the scenario into a temp dir and returns the binary path (the
// current test executable) and the extra environment that activates the fake.
func Setup(t testing.TB, sc Scenario) (binary string, env map[string]string) {
	t.Helper()
	dir := t.TempDir()
	if sc.RecordDir == "" {
		sc.RecordDir = filepath.Join(dir, "records")
	}
	if err := os.MkdirAll(sc.RecordDir, fsutil.DirPrivate); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scenario.json")
	if err := os.WriteFile(path, data, fsutil.FilePrivate); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A coverage-instrumented test binary warns on stderr at exit unless GOCOVERDIR is set.
	cover := filepath.Join(dir, "cover")
	if err := os.MkdirAll(cover, fsutil.DirPrivate); err != nil {
		t.Fatal(err)
	}
	return exe, map[string]string{EnvActive: "1", EnvScenario: path, "QUARK_FAKE_RECORDS": sc.RecordDir, "GOCOVERDIR": cover}
}

// Records returns every run recorded in dir, oldest first.
func Records(t testing.TB, dir string) []Record {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var r Record
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// Alive reports whether a process with pid exists and is not a zombie.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// Field 3 is the state; "Z" is a zombie, "X" is dead.
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}
