// Package ui is Quark's GTK 4 / libadwaita user interface. It is the only
// package (with cmd/quark) allowed to import the GTK bindings, and it talks
// to the rest of Quark only through internal/app.
package ui

import "github.com/diamondburned/gotk4/pkg/core/glib"

// MainLoop dispatches functions onto the GTK main loop. It implements
// app.Dispatcher and is safe to call from any goroutine.
type MainLoop struct{}

// Dispatch runs fn once on the main loop, in submission order.
func (MainLoop) Dispatch(fn func()) {
	glib.IdleAddPriority(glib.PriorityDefault, fn)
}
