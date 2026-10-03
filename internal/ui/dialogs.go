package ui

import (
	"errors"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"example.org/quark/internal/app"
)

// nameDialog asks for a file or folder name, validating as the user types.
func nameDialog(w *window, heading, okLabel, initial string, done func(string)) {
	d := adw.NewAlertDialog(heading, "")
	entry := gtk.NewEntry()
	entry.SetText(initial)
	entry.SetActivatesDefault(true)
	hint := gtk.NewLabel("")
	hint.AddCSSClass("error")
	hint.AddCSSClass("caption")
	hint.SetXAlign(0)
	hint.SetWrap(true)
	box := gtk.NewBox(gtk.OrientationVertical, 6)
	box.Append(entry)
	box.Append(hint)
	d.SetExtraChild(box)
	d.AddResponse("cancel", T("Cancel"))
	d.AddResponse("ok", okLabel)
	d.SetResponseAppearance("ok", adw.ResponseSuggested)
	d.SetDefaultResponse("ok")
	d.SetCloseResponse("cancel")
	validate := func() {
		ue := app.ValidateName(entry.Text())
		d.SetResponseEnabled("ok", ue == nil && entry.Text() != initial)
		if ue != nil && entry.Text() != "" {
			hint.SetText(ue.Detail)
			hint.SetVisible(true)
			entry.AddCSSClass("error")
		} else {
			hint.SetVisible(false)
			entry.RemoveCSSClass("error")
		}
	}
	entry.ConnectChanged(validate)
	validate()
	d.ConnectResponse(func(r string) {
		if r == "ok" && app.ValidateName(entry.Text()) == nil {
			done(entry.Text())
		}
	})
	d.Present(w.win)
	entry.GrabFocus()
	// Select the name without its extension, like Files does.
	end := utf8.RuneCountInString(initial)
	if i := strings.LastIndexByte(initial, '.'); i > 0 {
		end = utf8.RuneCountInString(initial[:i])
	}
	entry.SelectRegion(0, end)
}

// confirm asks before a destructive action. Cancel is the default.
func confirm(w *window, heading, body, action string, onConfirm func()) {
	d := adw.NewAlertDialog(heading, body)
	d.AddResponse("cancel", T("Cancel"))
	d.AddResponse("confirm", action)
	d.SetResponseAppearance("confirm", adw.ResponseDestructive)
	d.SetDefaultResponse("cancel")
	d.SetCloseResponse("cancel")
	d.ConnectResponse(func(r string) {
		if r == "confirm" {
			onConfirm()
		}
	})
	d.Present(w.win)
}

// useCLI checks a user-chosen CLI binary, offers to make it executable, and
// switches to it.
func (w *window) useCLI(path string) {
	err := app.CheckCLI(path)
	switch {
	case err == nil:
		w.a.UseBinary(path)
	case os.IsNotExist(err):
		w.toastText(T("That file does not exist"))
	case errors.Is(err, app.ErrCLINotExecutable):
		confirm(w, T("Make the File Executable?"),
			Tf("“%s” is not marked as a program yet. Quark can allow it to run. Only do this for the official Proton Drive CLI downloaded from proton.me.", path),
			T("Allow to Run"), func() {
				st, serr := os.Stat(path)
				if serr != nil {
					w.toastText(serr.Error())
					return
				}
				if cerr := os.Chmod(path, st.Mode().Perm()|0o100); cerr != nil {
					w.toastText(Tf("Could not change the file: %s", cerr.Error()))
					return
				}
				w.a.UseBinary(path)
			})
	default:
		w.toastText(Tf("Cannot use this file: %s", err.Error()))
	}
}
