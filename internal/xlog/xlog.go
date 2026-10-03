// Package xlog sets up Quark's structured logging: JSON lines in a
// size-rotated file under $XDG_STATE_HOME/quark, optional text on stderr,
// and redaction of personal data before anything is written.
package xlog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Options configures New.
type Options struct {
	// Dir holds quark.log and its rotations; empty means the default state dir.
	Dir string
	// MaxSize is the size in bytes at which the file rotates (default 2 MiB).
	MaxSize int64
	// MaxFiles is the number of rotated files kept (default 5).
	MaxFiles int
	// Stderr also writes human-readable logs to stderr (the -v flag).
	Stderr bool
	Level  slog.Level
}

// DefaultDir returns $XDG_STATE_HOME/quark (or ~/.local/state/quark).
func DefaultDir() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, "quark"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "quark"), nil
}

// New returns a logger and a function that closes the log file.
// If the file cannot be opened, logging falls back to stderr only; it never fails startup.
func New(o Options) (*slog.Logger, func() error, error) {
	if o.MaxSize <= 0 {
		o.MaxSize = 2 << 20
	}
	if o.MaxFiles <= 0 {
		o.MaxFiles = 5
	}
	var handlers []slog.Handler
	closer := func() error { return nil }
	var openErr error
	dir := o.Dir
	if dir == "" {
		dir, openErr = DefaultDir()
	}
	if openErr == nil {
		w, err := NewRotatingWriter(filepath.Join(dir, "quark.log"), o.MaxSize, o.MaxFiles)
		if err == nil {
			handlers = append(handlers, slog.NewJSONHandler(w, &slog.HandlerOptions{Level: o.Level}))
			closer = w.Close
		} else {
			openErr = err
		}
	}
	if o.Stderr || len(handlers) == 0 {
		handlers = append(handlers, slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: o.Level}))
	}
	return slog.New(NewRedactor(fanout(handlers))), closer, openErr
}

// ----- redaction -----

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	urlRe   = regexp.MustCompile(`https?://[^\s"'<>]+`)
	// Messages from the CLI that embed a node name after a fixed prefix.
	nameAfterRe = regexp.MustCompile(`((?:Node|Photo|Album|Trashed node|Device) not found: ).+`)
	quotedRe    = regexp.MustCompile(`"[^"\n]{1,512}"`)
	homeRe      = regexp.MustCompile(`/home/[^/\s"']+/`)
)

// Keys whose values are user data and are always reduced to a shape.
var sensitiveKeys = map[string]bool{
	"name": true, "newName": true, "path": true, "paths": true, "local": true, "remote": true,
	"email": true, "emails": true, "url": true, "password": true, "message": true, "stdout": true,
}

// RedactString removes emails, URLs (keeping scheme and host), quoted text,
// names after "not found:" and home-directory user names from free text.
func RedactString(s string) string {
	s = urlRe.ReplaceAllStringFunc(s, func(u string) string {
		rest := u[strings.Index(u, "//")+2:]
		host := rest
		if i := strings.IndexAny(rest, "/?#"); i >= 0 {
			host = rest[:i]
		}
		return u[:strings.Index(u, "//")+2] + host + "/…"
	})
	s = emailRe.ReplaceAllString(s, "<email>")
	s = nameAfterRe.ReplaceAllString(s, "${1}<name>")
	s = quotedRe.ReplaceAllString(s, `"…"`)
	s = homeRe.ReplaceAllString(s, "/home/<user>/")
	return s
}

// shape describes a sensitive value without revealing it.
func shape(v string) string {
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "/") {
		// Remote or local path: keep the first segment and the depth.
		segs := strings.Split(strings.TrimPrefix(v, "/"), "/")
		return "/" + segs[0] + "/…(" + strconv.Itoa(len(segs)) + ")"
	}
	return "<" + strconv.Itoa(len([]rune(v))) + " chars>"
}

// Redactor is a slog.Handler that redacts attributes and messages.
type Redactor struct{ next slog.Handler }

// NewRedactor wraps next.
func NewRedactor(next slog.Handler) *Redactor { return &Redactor{next: next} }

// Enabled implements slog.Handler.
func (r *Redactor) Enabled(ctx context.Context, l slog.Level) bool { return r.next.Enabled(ctx, l) }

// Handle implements slog.Handler.
func (r *Redactor) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, RedactString(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return r.next.Handle(ctx, out)
}

// WithAttrs implements slog.Handler.
func (r *Redactor) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = redactAttr(a)
	}
	return &Redactor{next: r.next.WithAttrs(red)}
}

// WithGroup implements slog.Handler.
func (r *Redactor) WithGroup(name string) slog.Handler {
	return &Redactor{next: r.next.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if v.Kind() != slog.KindGroup && sensitiveKeys[a.Key] {
		if v.Kind() == slog.KindString && a.Key != "password" {
			return slog.String(a.Key, shape(v.String()))
		}
		return slog.String(a.Key, "<redacted>")
	}
	switch v.Kind() {
	case slog.KindGroup:
		g := v.Group()
		red := make([]slog.Attr, len(g))
		for i, x := range g {
			red[i] = redactAttr(x)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(red...)}
	case slog.KindString:
		return slog.String(a.Key, RedactString(v.String()))
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, RedactString(x.Error()))
		case fmt.Stringer:
			return slog.String(a.Key, RedactString(x.String()))
		case []string:
			return slog.String(a.Key, RedactString(strings.Join(x, " ")))
		default:
			return slog.String(a.Key, RedactString(fmt.Sprint(x)))
		}
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}

// ----- fan-out -----

type multi []slog.Handler

func fanout(hs []slog.Handler) slog.Handler {
	if len(hs) == 1 {
		return hs[0]
	}
	return multi(hs)
}

func (m multi) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (m multi) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range m {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (m multi) WithAttrs(a []slog.Attr) slog.Handler {
	out := make(multi, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(a)
	}
	return out
}

func (m multi) WithGroup(n string) slog.Handler {
	out := make(multi, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(n)
	}
	return out
}

// ----- rotation -----

// RotatingWriter appends to a file and rotates it at MaxSize, keeping
// path.1 … path.N. It is safe for concurrent use.
type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxSize  int64
	maxFiles int
	f        *os.File
	size     int64
}

var _ io.WriteCloser = (*RotatingWriter)(nil)

// NewRotatingWriter opens (or creates) path for appending.
func NewRotatingWriter(path string, maxSize int64, maxFiles int) (*RotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	w := &RotatingWriter{path: path, maxSize: maxSize, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

// Write implements io.Writer.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingWriter) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	w.f = nil
	_ = os.Remove(fmt.Sprintf("%s.%d", w.path, w.maxFiles))
	for i := w.maxFiles - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open()
}

// Close implements io.Closer.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
