package ui

import (
	"os"
	"path/filepath"

	"github.com/diamondburned/gotk4/pkg/gio/v2"

	"example.org/quark/internal/autostart"
	"example.org/quark/internal/jobs"
	"example.org/quark/internal/tray"
)

// startTray shows the panel icon when a tray host (the GNOME AppIndicator
// extension, KDE, ...) is running. Without one, Quark behaves like a normal
// app: closing the window quits.
func (w *window) startTray() {
	if !tray.Available() {
		w.log.Info("no tray host: background mode unavailable")
		return
	}
	t, err := tray.Start(MainLoop{}, tray.Handlers{
		Open:    w.present,
		MyFiles: func() { w.present(); w.showSection("my-files") },
		Upload: func() {
			w.present()
			w.showSection("my-files")
			if v := w.files["my-files"]; v != nil {
				v.chooseUpload(false)
			}
		},
		TogglePause: w.togglePause,
		Quit:        w.quit,
	}, w.log)
	if err != nil {
		w.log.Warn("tray unavailable", "err", err)
		return
	}
	w.tray = t
	w.updateTray()
}

func (w *window) present() {
	w.win.SetVisible(true)
	w.win.Present()
}

func (w *window) togglePause() {
	if w.q.Paused() {
		w.q.Resume()
	} else {
		w.q.Pause()
	}
}

// quit really exits, bypassing close-to-tray.
func (w *window) quit() {
	w.quitting = true
	w.gapp.Quit()
}

// onCloseRequest hides the window instead of closing it when running in the
// background is possible and wanted. It returns true to stop the close.
func (w *window) onCloseRequest() bool {
	if w.quitting || w.tray == nil || !w.a.Settings().RunInBackground {
		return false
	}
	w.win.SetVisible(false)
	st := w.a.Settings()
	if !st.BackgroundHintShown {
		st.BackgroundHintShown = true
		w.a.UpdateSettings(st)
		n := gio.NewNotification(T("Quark is still running"))
		n.SetBody(T("Transfers continue in the background. Open Quark or quit it from its icon in the top bar."))
		w.gapp.SendNotification("background", n)
	}
	return true
}

// updateTray mirrors the transfer queue in the tray icon and menu.
func (w *window) updateTray() {
	if w.tray == nil {
		return
	}
	active, failed := w.q.Counts()
	s := tray.Status{Active: active, Failed: failed, Paused: w.q.Paused()}
	for _, t := range w.q.Items() {
		if !t.Active() || len(s.Lines) == tray.MaxLines {
			continue
		}
		verb := T("Uploading")
		if t.Kind == jobs.Download {
			verb = T("Downloading")
		}
		line := verb + " " + t.Name
		if t.Status == jobs.Queued {
			line = t.Name + " — " + T("waiting")
		} else if f, ok := t.Fraction(); ok {
			line += Tf(" — %.0f%%", f*100)
		}
		s.Lines = append(s.Lines, line)
	}
	w.tray.Update(s)
}

// setStartAtLogin creates or removes the autostart entry.
func (w *window) setStartAtLogin(on bool) error {
	if !on {
		return autostart.Disable(AppID)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return autostart.Enable(AppID, "Quark", exe, "--background")
}
