// Package tray shows Quark in the panel through the StatusNotifierItem
// protocol (the GNOME AppIndicator extension, KDE, Xfce, ...). It is pure
// D-Bus: no GTK 3 / libappindicator, which cannot coexist with GTK 4.
//
// The underlying library keeps package-level state, so there is at most one
// Tray per process. Handlers always run on the Dispatcher's (UI) thread.
package tray

import (
	_ "embed"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"example.org/quark/internal/i18n"
)

var (
	//go:embed icons/idle.png
	iconIdle []byte
	//go:embed icons/active.png
	iconActive []byte
	//go:embed icons/attention.png
	iconAttention []byte
)

// watcherName is the D-Bus name of the tray host.
const watcherName = "org.kde.StatusNotifierWatcher"

// Available reports whether a tray host is running on the session bus.
func Available() bool {
	// A private connection: ConnectSessionBus returns the process-wide shared
	// one, which the tray itself uses and must not be closed here.
	conn, err := dbus.SessionBusPrivate()
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Auth(nil); err != nil {
		return false
	}
	if err := conn.Hello(); err != nil {
		return false
	}
	var hasOwner bool
	err = conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, watcherName).Store(&hasOwner)
	return err == nil && hasOwner
}

// Dispatcher runs functions on the UI thread.
type Dispatcher interface{ Dispatch(fn func()) }

// Handlers are the menu actions. Nil handlers hide their item.
type Handlers struct {
	Open        func()
	MyFiles     func()
	Upload      func()
	TogglePause func()
	Quit        func()
}

// MaxLines is how many transfers the menu lists.
const MaxLines = 5

// Status is what the tray shows.
type Status struct {
	Active int
	Failed int
	Paused bool
	// Lines describe running or queued transfers, at most MaxLines are shown.
	Lines []string
}

// Icon names the icon a status uses (for tests and logs).
func (s Status) Icon() string {
	switch {
	case s.Failed > 0:
		return "attention"
	case s.Active > 0 && !s.Paused:
		return "active"
	default:
		return "idle"
	}
}

// Summary describes the transfers in words ("" when there is nothing to say).
func (s Status) Summary() string {
	var parts []string
	switch {
	case s.Active > 0 && s.Paused:
		parts = append(parts, i18n.Tf(i18n.N("%d transfer paused", "%d transfers paused", s.Active), s.Active))
	case s.Active > 0:
		parts = append(parts, i18n.Tf(i18n.N("%d transfer in progress", "%d transfers in progress", s.Active), s.Active))
	}
	if s.Failed > 0 {
		parts = append(parts, i18n.Tf(i18n.N("%d transfer failed", "%d transfers failed", s.Failed), s.Failed))
	}
	return strings.Join(parts, ", ")
}

// Tooltip is the hover text for a status.
func (s Status) Tooltip() string {
	if sum := s.Summary(); sum != "" {
		return "Quark — " + sum
	}
	return "Quark"
}

func iconFor(s Status) []byte {
	switch s.Icon() {
	case "attention":
		return iconAttention
	case "active":
		return iconActive
	default:
		return iconIdle
	}
}

// Tray is the running tray icon.
type Tray struct {
	disp      Dispatcher
	h         Handlers
	log       *slog.Logger
	conn      *dbus.Conn
	itemProps *prop.Properties
	menuProps *prop.Properties
	busName   string

	mu       sync.Mutex
	status   Status
	entries  []menuEntry
	revision uint32
}

// Menu entry ids.
const (
	idOpen int32 = iota + 1
	idMyFiles
	idUpload
	idSep1
	idHeader
	idLine0 // idLine0 … idLine0+MaxLines-1
)

const (
	idPause = idLine0 + MaxLines + iota
	idSep2
	idQuit
)

// Start connects to the session bus with a private connection, publishes
// the icon and its menu, and registers with the tray host. Call Available
// first: without a host the icon would not be shown anywhere.
func Start(d Dispatcher, h Handlers, log *slog.Logger) (*Tray, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	conn, err := dbus.SessionBusPrivate()
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	t := &Tray{disp: d, h: h, log: log, conn: conn, revision: 1}
	t.entries = t.buildEntries(Status{})
	if err := t.export(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tray: %w", err)
	}
	if err := t.register(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tray: registering with the tray host: %w", err)
	}
	go t.watchHost()
	return t, nil
}

func (t *Tray) buildEntries(s Status) []menuEntry {
	summary := s.Summary()
	if summary == "" {
		summary = i18n.T("No active transfers")
	}
	pause := i18n.T("Pause Transfers")
	if s.Paused {
		pause = i18n.T("Resume Transfers")
	}
	e := []menuEntry{
		{id: idOpen, label: i18n.T("Open Quark"), action: t.h.Open},
		{id: idMyFiles, label: i18n.T("My Files"), action: t.h.MyFiles},
		{id: idUpload, label: i18n.T("Upload Files…"), action: t.h.Upload},
		{id: idSep1, separator: true},
		{id: idHeader, label: summary},
	}
	for i := range MaxLines {
		line := menuEntry{id: idLine0 + int32(i)}
		if i < len(s.Lines) {
			line.label, line.visible = "    "+s.Lines[i], true
		}
		e = append(e, line)
	}
	e = append(e,
		menuEntry{id: idPause, label: pause, action: t.h.TogglePause},
		menuEntry{id: idSep2, separator: true},
		menuEntry{id: idQuit, label: i18n.T("Quit"), action: t.h.Quit},
	)
	for i := range e {
		switch {
		case e[i].separator:
			e[i].visible = true
		case e[i].id >= idLine0 && e[i].id < idLine0+MaxLines:
			// visibility set above; informational rows are not clickable
		case e[i].id == idHeader:
			e[i].visible = true
		default:
			e[i].visible = e[i].action != nil
			e[i].enabled = e[i].action != nil
		}
	}
	return e
}

// run hands an action to the UI thread.
func (t *Tray) run(fn func()) {
	if fn != nil {
		t.disp.Dispatch(fn)
	}
}

// Update shows a new status: icon, tooltip and menu.
func (t *Tray) Update(s Status) {
	t.mu.Lock()
	if statusEqual(s, t.status) {
		t.mu.Unlock()
		return
	}
	iconChanged := s.Icon() != t.status.Icon()
	t.status = s
	t.entries = t.buildEntries(s)
	t.revision++
	rev := t.revision
	t.mu.Unlock()

	if iconChanged {
		if px, err := toPixmap(iconFor(s)); err == nil {
			t.itemProps.SetMust(itemIface, "IconPixmap", px)
			t.emit(itemPath, itemIface+".NewIcon")
		}
	}
	t.itemProps.SetMust(itemIface, "ToolTip", tooltip{Title: s.Tooltip(), Icons: []pixmap{}})
	t.emit(itemPath, itemIface+".NewToolTip")
	t.emit(menuPath, menuIface+".LayoutUpdated", rev, int32(0))
}

func statusEqual(a, b Status) bool {
	if a.Active != b.Active || a.Failed != b.Failed || a.Paused != b.Paused || len(a.Lines) != len(b.Lines) {
		return false
	}
	for i := range a.Lines {
		if a.Lines[i] != b.Lines[i] {
			return false
		}
	}
	return true
}

// Stop removes the icon from the panel.
func (t *Tray) Stop() {
	if t.conn != nil {
		_, _ = t.conn.ReleaseName(t.busName)
		_ = t.conn.Close()
		t.conn = nil
	}
}
