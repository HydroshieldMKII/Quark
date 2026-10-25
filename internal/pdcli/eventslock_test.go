package pdcli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"example.org/quark/internal/testutil/fakecli"
)

// lockFake returns a fake CLI that takes its events.lock like CLI 0.8, with a
// fixed HOME so that every run sees the same lock.
func lockFake(t *testing.T, rule fakecli.Rule) (fakeEnv, string) {
	t.Helper()
	rule.EventsLock = true
	f := newFake(t, rule)
	home := t.TempDir()
	f.runner.Environ = func() []string { return []string{"HOME=" + home, "PATH=/usr/bin:/bin"} }
	return f, home
}

func writeLock(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Regression: a zero-filled events.lock left by a power loss made every CLI
// command crash, so the mount stayed offline and uncached files failed with EIO.
func TestRunRepairsCorruptEventsLock(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		env     map[string]string // Request.Env, relative to the fake's HOME
		lockDir string            // relative to HOME
	}{
		{name: "zero-filled", content: make([]byte, 15), lockDir: ".local/share/proton-drive-cli"},
		{name: "empty", content: []byte{}, lockDir: ".local/share/proton-drive-cli"},
		{name: "truncated", content: []byte(`{"pid":12`), lockDir: ".local/share/proton-drive-cli"},
		{name: "xdg data home", content: make([]byte, 4), env: map[string]string{"XDG_DATA_HOME": "xdg"}, lockDir: "xdg/proton-drive-cli"},
		{name: "cache dir", content: make([]byte, 4), env: map[string]string{"PROTON_DRIVE_CACHE_DIR": "pd"}, lockDir: "pd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, home := lockFake(t, fakecli.Rule{Match: []string{"filesystem", "list"}, Stdout: "[]\n"})
			env := map[string]string{}
			for k, v := range tc.env {
				env[k] = filepath.Join(home, v)
			}
			lock := filepath.Join(home, tc.lockDir, "events.lock")
			writeLock(t, lock, tc.content)

			res, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "list", "/my-files"}, Env: env})
			if err != nil {
				t.Fatalf("run with corrupt lock: %v", err)
			}
			if string(res.Stdout) != "[]\n" {
				t.Errorf("stdout = %q", res.Stdout)
			}
			if n := len(fakecli.Records(t, f.records)); n != 2 {
				t.Errorf("runs = %d, want 2 (crash, then retry)", n)
			}
			if kept, err := os.ReadFile(lock + ".corrupt"); err != nil || string(kept) != string(tc.content) {
				t.Errorf("corrupt lock not kept aside: %q, %v", kept, err)
			}
			if _, err := os.Stat(lock); !os.IsNotExist(err) {
				t.Errorf("lock not released by the retried run: %v", err)
			}
		})
	}
}

func TestRunLeavesUsableEventsLock(t *testing.T) {
	crash := fakecli.Rule{Match: []string{"filesystem", "list"}, Stderr: crashMarker + "\nTypeError: x\n", Exit: 1}
	for _, content := range []string{"", `{"pid":999999999}`, "null"} {
		f, home := lockFake(t, crash)
		lock := filepath.Join(home, ".local/share/proton-drive-cli/events.lock")
		if content != "" {
			writeLock(t, lock, []byte(content))
		}
		_, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "list", "/"}})
		wantKind(t, err, KindCrash)
		if n := len(fakecli.Records(t, f.records)); n != 1 {
			t.Errorf("lock %q: an unrelated crash was retried (%d runs)", content, n)
		}
		if _, err := os.Stat(lock + ".corrupt"); !os.IsNotExist(err) {
			t.Errorf("lock %q moved aside: %v", content, err)
		}
	}
}

func TestRunSurfacesUnrepairableEventsLock(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f, home := lockFake(t, fakecli.Rule{Match: []string{"filesystem", "list"}, Stdout: "[]\n"})
	dir := filepath.Join(home, ".local/share/proton-drive-cli")
	writeLock(t, filepath.Join(dir, "events.lock"), make([]byte, 15))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := f.runner.Run(context.Background(), Request{Args: []string{"filesystem", "list", "/"}})
	wantKind(t, err, KindCrash)
	if n := len(fakecli.Records(t, f.records)); n != 1 {
		t.Errorf("runs = %d, want 1 when the lock cannot be moved", n)
	}
}

func TestEventsLockPath(t *testing.T) {
	for _, tc := range []struct {
		env  []string
		want string
	}{
		{[]string{"HOME=/h"}, "/h/.local/share/proton-drive-cli/events.lock"},
		{[]string{"HOME=/h", "XDG_DATA_HOME=/x"}, "/x/proton-drive-cli/events.lock"},
		{[]string{"HOME=/h", "XDG_DATA_HOME=/x", "PROTON_DRIVE_CACHE_DIR=/c"}, "/c/events.lock"},
		{[]string{"HOME=/h", "XDG_DATA_HOME=rel"}, ""},
		{[]string{"HOME=/h", "PROTON_DRIVE_CACHE_DIR=rel"}, ""},
	} {
		if got := eventsLockPath(tc.env); got != tc.want {
			t.Errorf("eventsLockPath(%q) = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestRepairEventsLock(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	dir := t.TempDir()
	if repairEventsLock(ctx, log, "") || repairEventsLock(ctx, log, filepath.Join(dir, "missing")) {
		t.Error("nothing to repair reported as repaired")
	}
	// A lock larger than any pid file is not judged (nor read whole).
	big := filepath.Join(dir, "big.lock")
	writeLock(t, big, []byte(strings.Repeat("x", maxLockSize+1)))
	if repairEventsLock(ctx, log, big) {
		t.Error("oversized lock repaired")
	}
	// A corrupt lock that cannot be renamed (a directory holds the backup
	// name) is removed instead: staying offline is worse than losing a pid.
	stuck := filepath.Join(dir, "stuck.lock")
	writeLock(t, stuck, make([]byte, 15))
	if err := os.MkdirAll(filepath.Join(stuck+".corrupt", "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !repairEventsLock(ctx, log, stuck) {
		t.Error("unrenamable lock: want removed and retried")
	}
	if exists(stuck) {
		t.Error("unrenamable lock still there")
	}
	// A cancelled run does not wait or move anything.
	lock := filepath.Join(dir, "events.lock")
	writeLock(t, lock, nil)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if repairEventsLock(cancelled, log, lock) {
		t.Error("cancelled: want no retry")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("cancelled: lock moved: %v", err)
	}
}

// A lock still being written when first read is left in place.
func TestRepairEventsLockSettles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "events.lock")
		writeLock(t, lock, nil)
		go func() {
			time.Sleep(lockSettle / 2)
			_ = os.WriteFile(lock, []byte(`{"pid":1}`), 0o600)
		}()
		if !repairEventsLock(context.Background(), slog.New(slog.DiscardHandler), lock) {
			t.Error("lock written during the settle time: want a retry")
		}
		if raw, err := os.ReadFile(lock); err != nil || string(raw) != `{"pid":1}` {
			t.Errorf("lock in progress changed: %q, %v", raw, err)
		}
		if _, err := os.Stat(lock + ".corrupt"); !os.IsNotExist(err) {
			t.Errorf("lock in progress moved aside: %v", err)
		}
	})
}

// A lock whose CLI exits and removes it during the settle time was not the
// cause of the crash: no retry.
func TestRepairEventsLockVanished(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "events.lock")
		writeLock(t, lock, nil)
		go func() {
			time.Sleep(lockSettle / 2)
			_ = os.Remove(lock)
		}()
		if repairEventsLock(context.Background(), slog.New(slog.DiscardHandler), lock) {
			t.Error("vanished lock: want no retry")
		}
	})
}
