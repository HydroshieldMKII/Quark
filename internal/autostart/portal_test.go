package autostart

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakePortal plays org.freedesktop.portal.Background on a private bus.
type fakePortal struct {
	conn  *dbus.Conn
	mu    sync.Mutex
	code  uint32 // 0 granted, 1 denied
	reply bool   // send a Response at all
	got   map[string]dbus.Variant
}

func (p *fakePortal) set(code uint32, reply bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.code, p.reply = code, reply
}

func (p *fakePortal) options() map[string]dbus.Variant {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got
}

func (p *fakePortal) RequestBackground(_ string, opts map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	p.mu.Lock()
	p.got = opts
	code, reply := p.code, p.reply
	p.mu.Unlock()
	handle := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/1_1/quark")
	if reply {
		go func() {
			time.Sleep(20 * time.Millisecond)
			_ = p.conn.Emit(handle, "org.freedesktop.portal.Request.Response", code, map[string]dbus.Variant{})
		}()
	}
	return handle, nil
}

// Run via `make test` (dbus-run-session, QUARK_PRIVATE_BUS=1): never the desktop's portal.
func TestRequestBackground(t *testing.T) {
	if os.Getenv("QUARK_PRIVATE_BUS") != "1" {
		t.Skip("needs a private session bus (run via make test)")
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePortal{conn: conn, reply: true}
	if err := conn.Export(p, "/org/freedesktop/portal/desktop", "org.freedesktop.portal.Background"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName("org.freedesktop.portal.Desktop", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}

	if err := RequestBackground("why", true, []string{"quark", "--background"}, 5*time.Second); err != nil {
		t.Fatalf("granted: %v", err)
	}
	got := p.options()
	if got["autostart"].Value() != true || got["reason"].Value() != "why" {
		t.Errorf("options = %v", got)
	}
	if cl, _ := got["commandline"].Value().([]string); len(cl) != 2 || cl[1] != "--background" {
		t.Errorf("commandline = %v", got["commandline"])
	}

	p.set(1, true)
	if err := RequestBackground("why", false, nil, 5*time.Second); err != ErrDenied { //nolint:errorlint // sentinel
		t.Errorf("denied: %v", err)
	}

	p.set(0, false)
	if err := RequestBackground("why", false, nil, 200*time.Millisecond); err == nil {
		t.Error("no answer accepted")
	}
}
