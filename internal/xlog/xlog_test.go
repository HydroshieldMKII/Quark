package xlog

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"example.org/quark/internal/fsutil"
)

func TestRedactString(t *testing.T) {
	tests := map[string]string{
		"invite alice@proton.me failed":                                    "invite <email> failed",
		"open https://account.proton.me/desktop/login?selector=abc&code=x": "open https://account.proton.me/…",
		"link https://drive.proton.me/urls/ID#KEY ok":                      "link https://drive.proton.me/… ok",
		"Node not found: Tax return 2025.pdf":                              "Node not found: <name>",
		"Trashed node not found: secret":                                   "Trashed node not found: <name>",
		`Name conflict on "Medical.pdf" (file) already exists`:             `Name conflict on "…" (file) already exists`,
		"cannot read /home/user/Documents/x":                               "cannot read /home/<user>/Documents/x",
		"plain message":                                                    "plain message",
	}
	for in, want := range tests {
		if got := RedactString(in); got != want {
			t.Errorf("RedactString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactorAttributes(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewRedactor(slog.NewJSONHandler(&buf, nil)))
	log.With("email", "bob@proton.me").Info("opened https://drive.proton.me/urls/X#Y for carol@proton.me",
		"path", "/my-files/Taxes/2025/return.pdf",
		"name", "return.pdf",
		"local", "/home/user/secret.txt",
		"err", errors.New("Node not found: return.pdf"),
		"args", []string{"filesystem", "info", "dave@proton.me"},
		"count", 3,
		"kind", "not-found",
		slog.Group("req", "url", "https://account.proton.me/x?code=1", "password", "hunter2"),
		"stringer", stringer("eve@proton.me"),
		"other", struct{ A string }{"frank@proton.me"},
		"password", 42,
	)
	out := buf.String()
	for _, secret := range []string{
		"bob@", "carol@", "dave@", "eve@", "frank@", "Taxes", "return.pdf", "user/secret", "secret.txt", "X#Y", "code=1", "hunter2",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("log leaks %q:\n%s", secret, out)
		}
	}
	for _, kept := range []string{`"count":3`, `"kind":"not-found"`, `/my-files/…(4)`, `<10 chars>`, `"password":"<redacted>"`} {
		if !strings.Contains(out, kept) {
			t.Errorf("log lost %q:\n%s", kept, out)
		}
	}
}

type stringer string

func (s stringer) String() string { return string(s) }

func TestShape(t *testing.T) {
	if shape("") != "" || shape("/a") != "/a/…(1)" || shape("héllo") != "<5 chars>" {
		t.Fatal("shape")
	}
}

func TestRotatingWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quark.log")
	w, err := NewRotatingWriter(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for i := 0; i < 20; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"quark.log", "quark.log.1", "quark.log.2", "quark.log.3"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Size() > 100 {
			t.Errorf("%s is %d bytes", name, st.Size())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "quark.log.4")); !os.IsNotExist(err) {
		t.Error("kept more than MaxFiles rotations")
	}
	if _, err := w.Write(line); !errors.Is(err, os.ErrClosed) {
		t.Errorf("write after close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Error("double close")
	}
}

func TestRotatingWriterConcurrent(t *testing.T) {
	w, err := NewRotatingWriter(filepath.Join(t.TempDir(), "q.log"), 1<<10, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_, _ = fmt.Fprintf(w, "goroutine %d line %d\n", g, i)
			}
		}()
	}
	wg.Wait()
}

func TestNewWritesRedactedJSON(t *testing.T) {
	dir := t.TempDir()
	log, closeFn, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	log.Info("hello", "email", "a@b.co")
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "quark.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"msg":"hello"`) || strings.Contains(string(b), "a@b.co") {
		t.Fatalf("log file = %s", b)
	}
}

func TestNewFallsBackToStderr(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, fsutil.FilePrivate); err != nil {
		t.Fatal(err)
	}
	// Dir is a regular file: opening the log fails, but a usable logger is returned.
	log, closeFn, err := New(Options{Dir: file})
	if err == nil || log == nil {
		t.Fatalf("New = %v, %v", log, err)
	}
	_ = closeFn()
}

func TestDefaultDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if d, _ := DefaultDir(); d != "/state/quark" {
		t.Fatalf("DefaultDir = %q", d)
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	t.Setenv("HOME", "/h")
	if d, _ := DefaultDir(); d != "/h/.local/state/quark" {
		t.Fatalf("DefaultDir = %q", d)
	}
}

func TestFanout(t *testing.T) {
	var a, b bytes.Buffer
	h := fanout([]slog.Handler{
		slog.NewTextHandler(&a, &slog.HandlerOptions{Level: slog.LevelInfo}),
		slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelError}),
	})
	log := slog.New(h).With("k", "v").WithGroup("g")
	log.Info("info")
	log.Error("error")
	if !strings.Contains(a.String(), "info") || !strings.Contains(a.String(), "error") {
		t.Errorf("a = %q", a.String())
	}
	if strings.Contains(b.String(), "info") || !strings.Contains(b.String(), "error") {
		t.Errorf("b = %q", b.String())
	}
}
