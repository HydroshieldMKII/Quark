package main

import (
	"bytes"
	"strings"
	"testing"

	"example.org/quark/internal/fusefs"
)

func TestControlDispatch(t *testing.T) {
	var out, errb bytes.Buffer
	if _, handled := control([]string{"-v"}, &out, &errb); handled {
		t.Fatal("flags are not sub-commands")
	}
	if code, handled := control([]string{"pin"}, &out, &errb); !handled || code != 2 {
		t.Fatalf("pin without path: %d, %v", code, handled)
	}
	dir := t.TempDir()
	for _, cmd := range []string{"status", "state", "sync", "pin", "unpin", "evict"} {
		errb.Reset()
		if code, handled := control([]string{cmd, dir}, &out, &errb); !handled || code != 1 {
			t.Fatalf("%s on a plain directory: code %d, handled %v", cmd, code, handled)
		}
		if !strings.Contains(errb.String(), "Quark mount") {
			t.Fatalf("%s error = %q", cmd, errb.String())
		}
	}
}

func TestPrintStatusAndHumanBytes(t *testing.T) {
	var b bytes.Buffer
	printStatus(&b, fusefs.Status{Root: "/my-files", Pending: 2, PendingBytes: 1536, Errors: 1, Pinned: 3, CachedBytes: 5 << 20, CacheLimit: 1 << 30, LastSync: "2026-10-03T12:00:00Z", LastSyncErr: "boom"})
	s := b.String()
	for _, want := range []string{"read-write", "2 change(s), 1.5 KiB, 1 waiting to retry", "3 item(s)", "5.0 MiB of 1.0 GiB", "last error: boom"} {
		if !strings.Contains(s, want) {
			t.Errorf("status output lacks %q:\n%s", want, s)
		}
	}
	b.Reset()
	printStatus(&b, fusefs.Status{ReadOnly: true, Syncing: true})
	if !strings.Contains(b.String(), "read-only") || !strings.Contains(b.String(), "running") {
		t.Errorf("status output:\n%s", b.String())
	}
	if humanBytes(999) != "999 B" || humanBytes(1<<40) != "1.0 TiB" {
		t.Errorf("humanBytes: %s %s", humanBytes(999), humanBytes(1<<40))
	}
}

func TestRunUsage(t *testing.T) {
	devnull, err := openDevNull()
	if err != nil {
		t.Skip(err)
	}
	defer devnull.Close()
	if code := run(nil, devnull, devnull); code != 2 {
		t.Fatalf("no arguments: %d", code)
	}
	if code := run([]string{"-version"}, devnull, devnull); code != 0 {
		t.Fatalf("-version: %d", code)
	}
	if code := run([]string{"-bogus"}, devnull, devnull); code != 2 {
		t.Fatalf("bad flag: %d", code)
	}
}
