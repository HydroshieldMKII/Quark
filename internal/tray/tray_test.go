package tray

import (
	"os"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestStatusPresentation(t *testing.T) {
	tests := []struct {
		s       Status
		icon    string
		tooltip string
	}{
		{Status{}, "idle", "Quark"},
		{Status{Active: 1}, "active", "Quark — 1 transfer in progress"},
		{Status{Active: 3, Paused: true}, "idle", "Quark — 3 transfers paused"},
		{Status{Active: 2, Failed: 1}, "attention", "Quark — 2 transfers in progress, 1 transfer failed"},
		{Status{Failed: 2}, "attention", "Quark — 2 transfers failed"},
	}
	for _, tt := range tests {
		if tt.s.Icon() != tt.icon || tt.s.Tooltip() != tt.tooltip {
			t.Errorf("%+v: icon=%s tooltip=%q", tt.s, tt.s.Icon(), tt.s.Tooltip())
		}
		if len(iconFor(tt.s)) == 0 {
			t.Errorf("%+v: no icon data", tt.s)
		}
	}
}

// queue is a Dispatcher that runs functions immediately.
type queue struct{}

func (q *queue) Dispatch(fn func()) { fn() }

// fakeWatcher plays the tray host (the GNOME extension) on a private bus.
type fakeWatcher struct{ registered chan string }

// RegisterStatusNotifierItem mimics the real host: when the argument is an
// object path, the item lives on the caller's bus name.
func (w *fakeWatcher) RegisterStatusNotifierItem(sender dbus.Sender, service string) *dbus.Error {
	if len(service) > 0 && service[0] == '/' {
		service = string(sender)
	}
	w.registered <- service
	return nil
}

// The bus test needs its own session bus: `make test` runs it under
// dbus-run-session with QUARK_PRIVATE_BUS=1, so the real panel is never touched.
func TestRegistersWithTrayHostAndDispatchesClicks(t *testing.T) {
	if os.Getenv("QUARK_PRIVATE_BUS") != "1" {
		t.Skip("needs a private session bus (run via make test)")
	}
	if Available() {
		t.Fatal("a tray host is already on this bus")
	}
	conn, err := dbus.ConnectSessionBus() // shared; closed at process exit
	if err != nil {
		t.Fatal(err)
	}
	w := &fakeWatcher{registered: make(chan string, 1)}
	if err := conn.Export(w, "/StatusNotifierWatcher", watcherName); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(watcherName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("cannot own the watcher name: %v", err)
	}
	if !Available() {
		t.Fatal("Available() false with a tray host present")
	}

	opened := make(chan struct{}, 1)
	tr, err := Start(&queue{}, Handlers{Open: func() { opened <- struct{}{} }, TogglePause: func() {}, Quit: func() {}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Stop()

	var service string
	select {
	case service = <-w.registered:
	case <-time.After(5 * time.Second):
		t.Fatal("tray never registered with the host")
	}
	tr.Update(Status{Active: 2, Lines: []string{"a.txt — 40%", "b.txt"}})
	tr.Update(Status{Active: 2, Lines: []string{"a.txt — 40%", "b.txt"}}) // unchanged: no-op

	// Find "Open Quark" in the exported menu and click it like the panel would.
	menu := conn.Object(service, "/StatusNotifierItem/menu")
	var (
		rev    uint32
		layout struct {
			ID       int32
			Props    map[string]dbus.Variant
			Children []dbus.Variant
		}
	)
	deadline := time.Now().Add(5 * time.Second)
	openID := int32(-1)
	for openID < 0 && time.Now().Before(deadline) {
		call := menu.Call("com.canonical.dbusmenu.GetLayout", 0, int32(0), int32(-1), []string{})
		if call.Err == nil && call.Store(&rev, &layout) == nil {
			for _, c := range layout.Children {
				child, ok := c.Value().([]any)
				if !ok || len(child) < 2 {
					continue
				}
				props, _ := child[1].(map[string]dbus.Variant)
				if label, ok := props["label"].Value().(string); ok && label == "Open Quark" {
					openID, _ = child[0].(int32)
				}
			}
		}
		if openID < 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if openID < 0 {
		call := menu.Call("com.canonical.dbusmenu.GetLayout", 0, int32(0), int32(-1), []string{})
		t.Fatalf("menu has no Open Quark item; reply err=%v body=%#v", call.Err, call.Body)
	}
	if err := menu.Call("com.canonical.dbusmenu.Event", 0, openID, "clicked", dbus.MakeVariant(""), uint32(0)).Err; err != nil {
		t.Fatal(err)
	}
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("click did not reach the Open handler")
	}

	// The menu reflects the status.
	labels := func() map[string]bool {
		out := map[string]bool{}
		call := menu.Call("com.canonical.dbusmenu.GetLayout", 0, int32(0), int32(-1), []string{})
		if call.Err != nil || call.Store(&rev, &layout) != nil {
			t.Fatalf("GetLayout: %v", call.Err)
		}
		for _, c := range layout.Children {
			child, _ := c.Value().([]any)
			props, _ := child[1].(map[string]dbus.Variant)
			label, _ := props["label"].Value().(string)
			visible, _ := props["visible"].Value().(bool)
			if visible && label != "" {
				out[label] = true
			}
		}
		return out
	}
	got := labels()
	for _, want := range []string{"2 transfers in progress", "    a.txt — 40%", "    b.txt", "Pause Transfers", "Quit"} {
		if !got[want] {
			t.Errorf("menu lacks %q: %v", want, got)
		}
	}
	if got["Upload Files…"] {
		t.Error("item without a handler is visible")
	}
	tr.Update(Status{Active: 2, Paused: true})
	if got := labels(); !got["Resume Transfers"] || got["    a.txt — 40%"] {
		t.Errorf("paused menu: %v", got)
	}

	// The other dbusmenu calls panels make.
	var groups []struct {
		ID    int32
		Props map[string]dbus.Variant
	}
	if err := menu.Call("com.canonical.dbusmenu.GetGroupProperties", 0, []int32{openID}, []string{}).Store(&groups); err != nil ||
		len(groups) != 1 || groups[0].Props["label"].Value() != "Open Quark" {
		t.Errorf("GetGroupProperties: %+v, %v", groups, err)
	}
	var v dbus.Variant
	if err := menu.Call("com.canonical.dbusmenu.GetProperty", 0, openID, "label").Store(&v); err != nil || v.Value() != "Open Quark" {
		t.Errorf("GetProperty: %v, %v", v, err)
	}
	if err := menu.Call("com.canonical.dbusmenu.GetProperty", 0, int32(999), "label").Err; err == nil {
		t.Error("GetProperty on a missing item succeeded")
	}
	var needUpdate bool
	if err := menu.Call("com.canonical.dbusmenu.AboutToShow", 0, int32(0)).Store(&needUpdate); err != nil {
		t.Errorf("AboutToShow: %v", err)
	}
	if err := menu.Call("com.canonical.dbusmenu.AboutToShowGroup", 0, []int32{0}).Err; err != nil {
		t.Errorf("AboutToShowGroup: %v", err)
	}
	var sub struct {
		ID       int32
		Props    map[string]dbus.Variant
		Children []dbus.Variant
	}
	if err := menu.Call("com.canonical.dbusmenu.GetLayout", 0, openID, int32(-1), []string{}).Store(&rev, &sub); err != nil || sub.ID != openID {
		t.Errorf("GetLayout(item): %+v, %v", sub, err)
	}
	if err := menu.Call("com.canonical.dbusmenu.GetLayout", 0, int32(999), int32(-1), []string{}).Err; err == nil {
		t.Error("GetLayout on a missing item succeeded")
	}
	type ev struct {
		ID        int32
		EventID   string
		Data      dbus.Variant
		Timestamp uint32
	}
	if err := menu.Call("com.canonical.dbusmenu.EventGroup", 0, []ev{{openID, "clicked", dbus.MakeVariant(""), 0}, {openID, "hovered", dbus.MakeVariant(""), 0}}).Err; err != nil {
		t.Errorf("EventGroup: %v", err)
	}
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("EventGroup click did not reach the Open handler")
	}
	// A left click on the icon opens Quark too.
	item := conn.Object(service, "/StatusNotifierItem")
	for _, m := range []string{"Activate", "SecondaryActivate"} {
		if err := item.Call("org.kde.StatusNotifierItem."+m, 0, int32(0), int32(0)).Err; err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		select {
		case <-opened:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not open Quark", m)
		}
	}
	if err := item.Call("org.kde.StatusNotifierItem.ContextMenu", 0, int32(0), int32(0)).Err; err != nil {
		t.Errorf("ContextMenu: %v", err)
	}
	if err := item.Call("org.kde.StatusNotifierItem.Scroll", 0, int32(1), "vertical").Err; err != nil {
		t.Errorf("Scroll: %v", err)
	}
	var title dbus.Variant
	if err := item.Call("org.freedesktop.DBus.Properties.Get", 0, "org.kde.StatusNotifierItem", "Title").Store(&title); err != nil || title.Value() != "Quark" {
		t.Errorf("Title property: %v, %v", title, err)
	}

	// The host restarts (GNOME Shell restart, extension toggled): Quark registers again.
	if _, err := conn.ReleaseName(watcherName); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(watcherName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("cannot re-own the watcher name: %v", err)
	}
	select {
	case <-w.registered:
	case <-time.After(5 * time.Second):
		t.Fatal("no re-registration after the host restarted")
	}
}
