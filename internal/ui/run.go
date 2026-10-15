package ui

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"

	"example.org/quark/internal/app"
	"example.org/quark/internal/jobs"
)

// AppID is the application identifier.
const AppID = "org.example.Quark"

// Options configures Run.
type Options struct {
	App     *app.App
	Queue   *jobs.Queue
	Logger  *slog.Logger
	Version string
	// Demo marks the window as showing sample data.
	Demo bool
	// Background starts hidden in the tray (used by the autostart entry);
	// ignored when no tray host is available.
	Background bool
	// Screenshot renders the window to this PNG after startup and quits (debug).
	Screenshot string
	// ScreenshotSection selects the sidebar entry to show before the screenshot.
	ScreenshotSection string
	// ScreenshotWidth and ScreenshotHeight size the window for screenshots.
	ScreenshotWidth, ScreenshotHeight int
	// OnShutdown runs when the application exits.
	OnShutdown func()
}

// Run creates the GTK application and runs its main loop. It returns the exit status.
func Run(o Options, args []string) int {
	gapp := adw.NewApplication(AppID, gio.ApplicationDefaultFlags)
	var w *window
	gapp.ConnectStartup(func() {
		installCSS()
		installIcons()
		installAppActions(gapp, func() *window { return w })
	})
	gapp.ConnectActivate(func() {
		if w != nil {
			w.present()
			return
		}
		w = newWindow(gapp, o)
		w.startTray()
		if !o.Background || w.tray == nil {
			w.win.Present()
		}
		o.App.Start()
		if err := o.Queue.Load(); err != nil {
			w.toastText(err.Error())
		}
		if o.Screenshot != "" {
			w.scheduleScreenshot(o)
		}
	})
	gapp.ConnectShutdown(func() {
		if w != nil {
			w.saveGeometry()
			if w.tray != nil {
				w.tray.Stop()
			}
		}
		if o.OnShutdown != nil {
			o.OnShutdown()
		}
	})
	return gapp.Run(args)
}

func installAppActions(gapp *adw.Application, cur func() *window) {
	add := func(name string, accels []string, fn func()) {
		a := gio.NewSimpleAction(name, nil)
		a.ConnectActivate(func(*glibv2.Variant) { fn() })
		gapp.AddAction(a)
		if len(accels) > 0 {
			gapp.SetAccelsForAction("app."+name, accels)
		}
	}
	add("quit", []string{"<Control>q"}, func() {
		if w := cur(); w != nil {
			w.quit()
			return
		}
		gapp.Quit()
	})
	add("preferences", []string{"<Control>comma"}, func() {
		if w := cur(); w != nil {
			w.showPreferences()
		}
	})
	add("shortcuts", []string{"<Control>question"}, func() {
		if w := cur(); w != nil {
			w.showShortcuts()
		}
	})
	add("about", nil, func() {
		if w := cur(); w != nil {
			w.showAbout()
		}
	})
	add("logout", nil, func() {
		if w := cur(); w != nil {
			w.confirmLogout()
		}
	})
}

// scheduleScreenshot renders the window after it has settled, then quits.
func (w *window) scheduleScreenshot(o Options) {
	if o.ScreenshotWidth > 0 && o.ScreenshotHeight > 0 {
		w.win.SetDefaultSize(o.ScreenshotWidth, o.ScreenshotHeight)
	}
	start := time.Now()
	glib.TimeoutAdd(200, func() bool {
		ready := w.a.Session().Phase != app.PhaseStarting
		if !ready && time.Since(start) < 10*time.Second {
			return true
		}
		if o.ScreenshotSection != "" && !w.selectedOnce {
			w.selectedOnce = true
			w.screenshotScenario(o.ScreenshotSection)
			return true
		}
		if v := w.visibleFiles(); v != nil && v.b.State().Loading && time.Since(start) < 10*time.Second {
			return true
		}
		glib.TimeoutAdd(600, func() {
			if err := renderToPNG(w.win, o.Screenshot); err != nil {
				w.log.Error("screenshot", "err", err)
				os.Exit(3) //nolint:forbidigo // debug path only
			}
			w.gapp.Quit()
		})
		return false
	})
}

// screenshotScenario prepares a debug screenshot. Syntax: a sidebar id,
// optionally followed by ":open=<folder>", ":upload", ":prefs" or ":signin".
func (w *window) screenshotScenario(spec string) {
	id, action, _ := strings.Cut(spec, ":")
	if w.a.Session().Phase == app.PhaseReady {
		w.showSection(id)
	}
	switch {
	case strings.HasPrefix(action, "open="):
		name := strings.TrimPrefix(action, "open=")
		if v := w.visibleFiles(); v != nil {
			glib.TimeoutAdd(400, func() {
				for _, n := range v.b.State().Visible {
					if n.Name == name {
						v.activate(n)
					}
				}
			})
		}
	case action == "upload":
		if v := w.files["my-files"]; v != nil {
			v.enqueueUploads(v.b.State().Location, []string{"/etc/hostname"})
		}
		w.showSection("transfers")
	case action == "prefs":
		w.showPreferences()
	case strings.HasPrefix(action, "share="):
		name := strings.TrimPrefix(action, "share=")
		if v := w.visibleFiles(); v != nil {
			glib.TimeoutAdd(400, func() {
				for _, n := range v.b.State().Visible {
					if n.Name == name {
						w.showShare(v.b.State().Location, n)
					}
				}
			})
		}
	case action == "invitations":
		if v := w.visibleFiles(); v != nil && v.inv != nil {
			glib.TimeoutAdd(400, v.showInvitations)
		}
	case action == "signin":
		w.a.Login()
	}
}
