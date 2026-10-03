package tray

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// This file implements the two D-Bus interfaces a tray icon needs:
// org.kde.StatusNotifierItem (the icon) and com.canonical.dbusmenu (its
// menu, flat: no submenus). All state is guarded by Tray.mu; D-Bus handlers
// run on godbus goroutines and hand actions to the UI thread via Dispatcher.

const (
	itemPath  = dbus.ObjectPath("/StatusNotifierItem")
	menuPath  = dbus.ObjectPath("/StatusNotifierItem/menu")
	itemIface = "org.kde.StatusNotifierItem"
	menuIface = "com.canonical.dbusmenu"
)

// pixmap is the SNI icon format: width, height, ARGB32 in network byte order.
type pixmap struct {
	W, H int32
	Data []byte
}

type tooltip struct {
	IconName string
	Icons    []pixmap
	Title    string
	Text     string
}

func toPixmap(pngData []byte) ([]pixmap, error) {
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	nrgba := image.NewNRGBA(b)
	draw.Draw(nrgba, b, img, b.Min, draw.Src)
	out := make([]byte, 0, len(nrgba.Pix))
	for i := 0; i+3 < len(nrgba.Pix); i += 4 {
		r, g, bl, a := nrgba.Pix[i], nrgba.Pix[i+1], nrgba.Pix[i+2], nrgba.Pix[i+3]
		out = append(out, a, r, g, bl) // ARGB32, network byte order
	}
	return []pixmap{{W: int32(b.Dx()), H: int32(b.Dy()), Data: out}}, nil //nolint:gosec // small icons
}

// menuEntry is one row of the flat menu.
type menuEntry struct {
	id        int32
	label     string
	enabled   bool
	visible   bool
	separator bool
	action    func()
}

func (e menuEntry) props() map[string]dbus.Variant {
	if e.separator {
		return map[string]dbus.Variant{"type": dbus.MakeVariant("separator"), "visible": dbus.MakeVariant(e.visible)}
	}
	return map[string]dbus.Variant{
		"label":   dbus.MakeVariant(e.label),
		"enabled": dbus.MakeVariant(e.enabled),
		"visible": dbus.MakeVariant(e.visible),
	}
}

type layout struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

type groupProps struct {
	ID    int32
	Props map[string]dbus.Variant
}

type menuEvent struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

// ----- StatusNotifierItem methods -----

type itemObject struct{ t *Tray }

func (o itemObject) Activate(_, _ int32) *dbus.Error {
	o.t.run(o.t.h.Open)
	return nil
}

func (o itemObject) SecondaryActivate(_, _ int32) *dbus.Error {
	o.t.run(o.t.h.Open)
	return nil
}

func (o itemObject) ContextMenu(_, _ int32) *dbus.Error { return nil }

func (o itemObject) Scroll(_ int32, _ string) *dbus.Error { return nil }

// ----- dbusmenu methods -----

type menuObject struct{ t *Tray }

func (o menuObject) GetLayout(parentID, _ int32, _ []string) (uint32, layout, *dbus.Error) {
	t := o.t
	t.mu.Lock()
	defer t.mu.Unlock()
	if parentID != 0 {
		for _, e := range t.entries {
			if e.id == parentID {
				return t.revision, layout{ID: e.id, Props: e.props(), Children: []dbus.Variant{}}, nil
			}
		}
		return 0, layout{}, dbus.MakeFailedError(fmt.Errorf("no menu item %d", parentID))
	}
	children := make([]dbus.Variant, 0, len(t.entries))
	for _, e := range t.entries {
		children = append(children, dbus.MakeVariant(layout{ID: e.id, Props: e.props(), Children: []dbus.Variant{}}))
	}
	root := layout{ID: 0, Props: map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")}, Children: children}
	return t.revision, root, nil
}

func (o menuObject) GetGroupProperties(ids []int32, _ []string) ([]groupProps, *dbus.Error) {
	t := o.t
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []groupProps
	for _, e := range t.entries {
		for _, id := range ids {
			if e.id == id {
				out = append(out, groupProps{ID: e.id, Props: e.props()})
			}
		}
	}
	return out, nil
}

func (o menuObject) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	t := o.t
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.entries {
		if e.id == id {
			if v, ok := e.props()[name]; ok {
				return v, nil
			}
		}
	}
	return dbus.MakeVariant(""), dbus.MakeFailedError(fmt.Errorf("no property %q on %d", name, id))
}

func (o menuObject) Event(id int32, eventID string, _ dbus.Variant, _ uint32) *dbus.Error {
	if eventID != "clicked" {
		return nil
	}
	t := o.t
	t.mu.Lock()
	var action func()
	for _, e := range t.entries {
		if e.id == id && e.enabled && e.visible {
			action = e.action
		}
	}
	t.mu.Unlock()
	t.run(action)
	return nil
}

func (o menuObject) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	for _, ev := range events {
		_ = o.Event(ev.ID, ev.EventID, ev.Data, ev.Timestamp)
	}
	return []int32{}, nil
}

func (o menuObject) AboutToShow(int32) (bool, *dbus.Error) { return false, nil }

func (o menuObject) AboutToShowGroup([]int32) ([]int32, []int32, *dbus.Error) {
	return []int32{}, []int32{}, nil
}

// ----- export and registration -----

var introspection = introspect.Node{
	Interfaces: []introspect.Interface{
		introspect.IntrospectData,
		prop.IntrospectData,
		{
			Name: itemIface,
			Methods: []introspect.Method{
				{Name: "Activate", Args: []introspect.Arg{{Name: "x", Type: "i", Direction: "in"}, {Name: "y", Type: "i", Direction: "in"}}},
				{Name: "SecondaryActivate", Args: []introspect.Arg{{Name: "x", Type: "i", Direction: "in"}, {Name: "y", Type: "i", Direction: "in"}}},
				{Name: "ContextMenu", Args: []introspect.Arg{{Name: "x", Type: "i", Direction: "in"}, {Name: "y", Type: "i", Direction: "in"}}},
				{Name: "Scroll", Args: []introspect.Arg{{Name: "delta", Type: "i", Direction: "in"}, {Name: "orientation", Type: "s", Direction: "in"}}},
			},
			Signals: []introspect.Signal{{Name: "NewIcon"}, {Name: "NewToolTip"}, {Name: "NewStatus", Args: []introspect.Arg{{Name: "status", Type: "s"}}}},
		},
	},
}

var menuIntrospection = introspect.Node{
	Interfaces: []introspect.Interface{
		introspect.IntrospectData,
		prop.IntrospectData,
		{
			Name: menuIface,
			Methods: []introspect.Method{
				{Name: "GetLayout", Args: []introspect.Arg{
					{Name: "parentId", Type: "i", Direction: "in"},
					{Name: "recursionDepth", Type: "i", Direction: "in"},
					{Name: "propertyNames", Type: "as", Direction: "in"},
					{Name: "revision", Type: "u", Direction: "out"},
					{Name: "layout", Type: "(ia{sv}av)", Direction: "out"},
				}},
				{Name: "GetGroupProperties", Args: []introspect.Arg{
					{Name: "ids", Type: "ai", Direction: "in"},
					{Name: "propertyNames", Type: "as", Direction: "in"},
					{Name: "properties", Type: "a(ia{sv})", Direction: "out"},
				}},
				{Name: "GetProperty", Args: []introspect.Arg{
					{Name: "id", Type: "i", Direction: "in"},
					{Name: "name", Type: "s", Direction: "in"},
					{Name: "value", Type: "v", Direction: "out"},
				}},
				{Name: "Event", Args: []introspect.Arg{
					{Name: "id", Type: "i", Direction: "in"},
					{Name: "eventId", Type: "s", Direction: "in"},
					{Name: "data", Type: "v", Direction: "in"},
					{Name: "timestamp", Type: "u", Direction: "in"},
				}},
				{Name: "EventGroup", Args: []introspect.Arg{
					{Name: "events", Type: "a(isvu)", Direction: "in"}, {Name: "idErrors", Type: "ai", Direction: "out"},
				}},
				{Name: "AboutToShow", Args: []introspect.Arg{
					{Name: "id", Type: "i", Direction: "in"}, {Name: "needUpdate", Type: "b", Direction: "out"},
				}},
				{Name: "AboutToShowGroup", Args: []introspect.Arg{
					{Name: "ids", Type: "ai", Direction: "in"},
					{Name: "updatesNeeded", Type: "ai", Direction: "out"},
					{Name: "idErrors", Type: "ai", Direction: "out"},
				}},
			},
			Signals: []introspect.Signal{
				{Name: "LayoutUpdated", Args: []introspect.Arg{{Name: "revision", Type: "u"}, {Name: "parent", Type: "i"}}},
				{Name: "ItemsPropertiesUpdated", Args: []introspect.Arg{{Name: "updatedProps", Type: "a(ia{sv})"}, {Name: "removedProps", Type: "a(ias)"}}},
			},
		},
	},
}

// export publishes both objects on conn and claims the item's bus name.
func (t *Tray) export() error {
	icon, err := toPixmap(iconFor(t.status))
	if err != nil {
		return err
	}
	itemProps, err := prop.Export(t.conn, itemPath, prop.Map{itemIface: {
		"Category":      {Value: "ApplicationStatus"},
		"Id":            {Value: "quark"},
		"Title":         {Value: "Quark"},
		"Status":        {Value: "Active"},
		"WindowId":      {Value: int32(0)},
		"IconName":      {Value: ""},
		"IconPixmap":    {Value: icon},
		"IconThemePath": {Value: ""},
		"ToolTip":       {Value: tooltip{Title: t.status.Tooltip(), Icons: []pixmap{}}},
		"ItemIsMenu":    {Value: false},
		"Menu":          {Value: menuPath},
	}})
	if err != nil {
		return err
	}
	menuProps, err := prop.Export(t.conn, menuPath, prop.Map{menuIface: {
		"Version":       {Value: uint32(3)},
		"TextDirection": {Value: "ltr"},
		"Status":        {Value: "normal"},
		"IconThemePath": {Value: []string{}},
	}})
	if err != nil {
		return err
	}
	t.itemProps, t.menuProps = itemProps, menuProps
	if err := t.conn.Export(itemObject{t}, itemPath, itemIface); err != nil {
		return err
	}
	if err := t.conn.Export(menuObject{t}, menuPath, menuIface); err != nil {
		return err
	}
	if err := t.conn.Export(introspect.NewIntrospectable(&introspection), itemPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	if err := t.conn.Export(introspect.NewIntrospectable(&menuIntrospection), menuPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	t.busName = fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	if _, err := t.conn.RequestName(t.busName, dbus.NameFlagDoNotQueue); err != nil {
		return err
	}
	return nil
}

// register announces the item to the tray host.
func (t *Tray) register() error {
	return t.conn.Object(watcherName, "/StatusNotifierWatcher").
		Call(watcherName+".RegisterStatusNotifierItem", 0, t.busName).Err
}

// watchHost re-registers whenever the tray host (re)appears, e.g. after
// GNOME Shell restarts or the extension is toggled.
func (t *Tray) watchHost() {
	if err := t.conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, watcherName),
	); err != nil {
		t.log.Warn("tray: cannot watch the tray host", "err", err)
		return
	}
	signals := make(chan *dbus.Signal, 8)
	t.conn.Signal(signals)
	for sig := range signals {
		if sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) < 3 {
			continue
		}
		if owner, _ := sig.Body[2].(string); owner != "" {
			if err := t.register(); err != nil {
				t.log.Warn("tray: re-registration failed", "err", err)
			}
		}
	}
}

// emit sends a signal, logging failures (the host may be gone).
func (t *Tray) emit(path dbus.ObjectPath, name string, args ...any) {
	if err := t.conn.Emit(path, name, args...); err != nil {
		t.log.Debug("tray: emit failed", "signal", name, "err", err)
	}
}
