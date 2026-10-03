package autostart

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
)

// InFlatpak reports whether Quark runs inside a Flatpak sandbox, where the
// autostart directory is not reachable and the Background portal is used.
func InFlatpak() bool {
	_, err := os.Stat("/.flatpak-info")
	return err == nil
}

// ErrDenied means the user (or policy) refused background permission.
var ErrDenied = errors.New("autostart: background permission denied")

// RequestBackground asks the desktop (org.freedesktop.portal.Background)
// to let Quark run in the background and, when autostart is true, to start
// it at login with commandline. GNOME may show a permission dialog; the call
// waits for the answer up to timeout.
func RequestBackground(reason string, autostart bool, commandline []string, timeout time.Duration) error {
	conn, err := dbus.SessionBusPrivate()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Auth(nil); err != nil {
		return err
	}
	if err := conn.Hello(); err != nil {
		return err
	}
	// Listen for the Response before calling, so it cannot be missed.
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
	); err != nil {
		return err
	}
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)

	opts := map[string]dbus.Variant{
		"reason":           dbus.MakeVariant(reason),
		"autostart":        dbus.MakeVariant(autostart),
		"dbus-activatable": dbus.MakeVariant(false),
	}
	if len(commandline) > 0 {
		opts["commandline"] = dbus.MakeVariant(commandline)
	}
	var handle dbus.ObjectPath
	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	if err := obj.Call("org.freedesktop.portal.Background.RequestBackground", 0, "", opts).Store(&handle); err != nil {
		return fmt.Errorf("autostart: background portal: %w", err)
	}
	deadline := time.After(timeout)
	for {
		select {
		case sig := <-signals:
			if sig.Path != handle || len(sig.Body) < 1 {
				continue
			}
			if code, _ := sig.Body[0].(uint32); code != 0 {
				return ErrDenied
			}
			return nil
		case <-deadline:
			return errors.New("autostart: no answer from the background portal")
		}
	}
}
