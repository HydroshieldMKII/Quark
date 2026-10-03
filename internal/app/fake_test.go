package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
	"example.org/quark/internal/settings"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// fakeBackend records calls and lets each test script the answers.
type fakeBackend struct {
	mu       sync.Mutex
	calls    []string
	inflight map[string]int
	peak     map[string]int

	version func() (pdcli.Version, error)
	roots   func() ([]string, error)
	list    func(ctx context.Context, path string) ([]drive.Node, error)
	devices func() ([]drive.Device, error)
	create  func(parent, name string) (drive.Node, error)
	rename  func(path, name string) (drive.Node, error)
	batch   func(ctx context.Context, op string, paths []string) ([]drive.ItemResult, error)
	empty   func() error
	login   func(ctx context.Context, onURL func(string)) error
	logout  func() error
	share   func(op, path string, arg any) (*drive.Sharing, error)
	invites func() ([]drive.Invitation, error)
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		inflight: map[string]int{}, peak: map[string]int{},
		version: func() (pdcli.Version, error) {
			return pdcli.Version{Major: 0, Minor: 8, Patch: 0, Raw: "cli-drive@0.8.0"}, nil
		},
		roots: func() ([]string, error) { return []string{"/my-files"}, nil },
		list:  func(context.Context, string) ([]drive.Node, error) { return nil, nil },
		batch: func(_ context.Context, _ string, paths []string) ([]drive.ItemResult, error) {
			res := make([]drive.ItemResult, len(paths))
			for i := range paths {
				res[i] = drive.ItemResult{UID: paths[i], OK: true}
			}
			return res, nil
		},
	}
}

// script reads a scripted function under the lock (tests may swap scripts
// while background work is running).
func (f *fakeBackend) script(get func() any) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return get()
}

func (f *fakeBackend) enter(kind, call string) func() {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.inflight[kind]++
	if f.inflight[kind] > f.peak[kind] {
		f.peak[kind] = f.inflight[kind]
	}
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.inflight[kind]--
		f.mu.Unlock()
	}
}

func (f *fakeBackend) batchFn() func(context.Context, string, []string) ([]drive.ItemResult, error) {
	return f.script(func() any { return f.batch }).(func(context.Context, string, []string) ([]drive.ItemResult, error))
}

// set swaps scripts under the lock.
func (f *fakeBackend) set(fn func(f *fakeBackend)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeBackend) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeBackend) InFlight(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inflight[kind]
}

func (f *fakeBackend) Peak(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak[kind]
}

func (f *fakeBackend) Version(context.Context) (pdcli.Version, error) {
	defer f.enter("read", "version")()
	return f.script(func() any { return f.version }).(func() (pdcli.Version, error))()
}

func (f *fakeBackend) Roots(context.Context) ([]string, error) {
	defer f.enter("read", "roots")()
	return f.script(func() any { return f.roots }).(func() ([]string, error))()
}

func (f *fakeBackend) List(ctx context.Context, path string) ([]drive.Node, error) {
	defer f.enter("read", "list "+path)()
	return f.script(func() any { return f.list }).(func(context.Context, string) ([]drive.Node, error))(ctx, path)
}

func (f *fakeBackend) Devices(context.Context) ([]drive.Device, error) {
	defer f.enter("read", "devices")()
	return f.script(func() any { return f.devices }).(func() ([]drive.Device, error))()
}

func (f *fakeBackend) Info(context.Context, string) (drive.Node, error) { return drive.Node{}, nil }

func (f *fakeBackend) CreateFolder(_ context.Context, parent, name string) (drive.Node, error) {
	defer f.enter("write", "create "+parent+" "+name)()
	if fn, _ := f.script(func() any { return f.create }).(func(string, string) (drive.Node, error)); fn != nil {
		return fn(parent, name)
	}
	return drive.Node{UID: "new", Name: name, Type: drive.TypeFolder}, nil
}

func (f *fakeBackend) Rename(_ context.Context, path, name string) (drive.Node, error) {
	defer f.enter("write", "rename "+path+" "+name)()
	if fn, _ := f.script(func() any { return f.rename }).(func(string, string) (drive.Node, error)); fn != nil {
		return fn(path, name)
	}
	return drive.Node{}, nil
}

func (f *fakeBackend) Copy(ctx context.Context, sources []string, target, name string) ([]drive.ItemResult, error) {
	defer f.enter("write", "copy "+strings.Join(sources, ",")+" -> "+target+" name="+name)()
	return f.batchFn()(ctx, "copy", sources)
}

func (f *fakeBackend) Move(ctx context.Context, sources []string, target string) ([]drive.ItemResult, error) {
	defer f.enter("write", "move "+strings.Join(sources, ",")+" -> "+target)()
	return f.batchFn()(ctx, "move", sources)
}

func (f *fakeBackend) Trash(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	defer f.enter("write", "trash "+strings.Join(paths, ","))()
	return f.batchFn()(ctx, "trash", paths)
}

func (f *fakeBackend) Restore(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	defer f.enter("write", "restore "+strings.Join(paths, ","))()
	return f.batchFn()(ctx, "restore", paths)
}

func (f *fakeBackend) DeleteForever(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	defer f.enter("write", "delete "+strings.Join(paths, ","))()
	return f.batchFn()(ctx, "delete", paths)
}

func (f *fakeBackend) EmptyTrash(context.Context) error {
	defer f.enter("write", "empty-trash")()
	if fn, _ := f.script(func() any { return f.empty }).(func() error); fn != nil {
		return fn()
	}
	return nil
}

func (f *fakeBackend) Login(ctx context.Context, onURL func(string)) error {
	defer f.enter("login", "login")()
	if fn, _ := f.script(func() any { return f.login }).(func(context.Context, func(string)) error); fn != nil {
		return fn(ctx, onURL)
	}
	return nil
}

func (f *fakeBackend) Logout(context.Context) error {
	defer f.enter("write", "logout")()
	if fn, _ := f.script(func() any { return f.logout }).(func() error); fn != nil {
		return fn()
	}
	return nil
}

func (f *fakeBackend) shareFn() func(string, string, any) (*drive.Sharing, error) {
	fn, _ := f.script(func() any { return f.share }).(func(string, string, any) (*drive.Sharing, error))
	if fn == nil {
		return func(string, string, any) (*drive.Sharing, error) { return nil, nil }
	}
	return fn
}

func (f *fakeBackend) Sharing(_ context.Context, path string) (*drive.Sharing, error) {
	defer f.enter("read", "sharing "+path)()
	return f.shareFn()("status", path, nil)
}

func (f *fakeBackend) Invite(_ context.Context, path string, s pdcli.InviteSpec) (*drive.Sharing, error) {
	defer f.enter("write", "invite "+path+" "+strings.Join(s.Emails, ",")+" "+string(s.Role))()
	return f.shareFn()("invite", path, s)
}

func (f *fakeBackend) RemoveMembers(_ context.Context, path string, emails []string) (*drive.Sharing, error) {
	defer f.enter("write", "unshare "+path+" "+strings.Join(emails, ","))()
	return f.shareFn()("remove", path, emails)
}

func (f *fakeBackend) SetLink(_ context.Context, path string, s pdcli.LinkSpec) (*drive.Sharing, error) {
	exp := ""
	if !s.Expires.IsZero() {
		exp = s.Expires.UTC().Format("2006-01-02")
	}
	defer f.enter("write", "set-url "+path+" "+string(s.Role)+" pw="+s.Password+" exp="+exp)()
	return f.shareFn()("set-url", path, s)
}

func (f *fakeBackend) RemoveLink(_ context.Context, path string) (*drive.Sharing, error) {
	defer f.enter("write", "remove-url "+path)()
	return f.shareFn()("remove-url", path, nil)
}

func (f *fakeBackend) Leave(_ context.Context, path string) error {
	defer f.enter("write", "leave "+path)()
	_, err := f.shareFn()("leave", path, nil)
	return err
}

func (f *fakeBackend) Invitations(context.Context) ([]drive.Invitation, error) {
	defer f.enter("read", "invitations")()
	if fn, _ := f.script(func() any { return f.invites }).(func() ([]drive.Invitation, error)); fn != nil {
		return fn()
	}
	return nil, nil
}

func (f *fakeBackend) AcceptInvitation(_ context.Context, uid string) error {
	defer f.enter("write", "accept "+uid)()
	return nil
}

func (f *fakeBackend) RejectInvitation(_ context.Context, uid string) error {
	defer f.enter("write", "reject "+uid)()
	return nil
}

// harness wires an App to a Loop and a fake backend.
type harness struct {
	t        *testing.T
	loop     *Loop
	app      *App
	be       *fakeBackend
	now      time.Time
	toasts   []Toast
	phases   []Phase
	saved    []settings.Settings
	binary   string
	backends int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, loop: NewLoop(), be: newFakeBackend(), now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), binary: "/usr/bin/proton-drive"}
	h.app = New(Config{
		Dispatcher:   h.loop,
		Settings:     settings.Defaults(),
		SaveSettings: func(s settings.Settings) error { h.saved = append(h.saved, s); return nil },
		FindBinary:   func(string) (string, []string) { return h.binary, []string{"/home/u/Downloads/proton-drive"} },
		NewBackend:   func(string, settings.Settings) Backend { h.backends++; return h.be },
		Now:          func() time.Time { return h.now },
	})
	h.app.OnToast(func(tt Toast) { h.toasts = append(h.toasts, tt) })
	h.app.OnSession(func(s Session) { h.phases = append(h.phases, s.Phase) })
	t.Cleanup(func() {
		if !h.app.Shutdown(5 * time.Second) {
			t.Error("shutdown timed out")
		}
		h.loop.Drain()
	})
	return h
}

// until drains the loop until cond holds or the deadline passes.
func (h *harness) until(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.loop.Drain()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s (calls: %q)", what, h.be.Calls())
		}
		done := make(chan struct{})
		go func() { time.Sleep(10 * time.Millisecond); close(done) }()
		h.loop.Wait(done)
	}
}

func (h *harness) ready() {
	h.t.Helper()
	h.app.Start()
	h.until("ready", func() bool { return h.app.Session().Phase == PhaseReady })
}

func (h *harness) lastToast() Toast {
	h.t.Helper()
	if len(h.toasts) == 0 {
		h.t.Fatal("no toast")
	}
	return h.toasts[len(h.toasts)-1]
}

func node(uid, name string, typ drive.NodeType) drive.Node {
	return drive.Node{UID: uid, Name: name, NameOK: true, Type: typ}
}
