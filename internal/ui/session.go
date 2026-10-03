package ui

import (
	"context"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"example.org/quark/internal/app"
)

// sessionPages are the full-window pages shown before the browser is ready.
type sessionPages struct {
	w     *window
	pages map[string]gtk.Widgetter

	nobinary, incompatible, loggedout, loggingin, failed *adw.StatusPage
	hints                                                *gtk.Label
	loginURL                                             string
	openBrowser, copyLink                                *gtk.Button
}

func page(child gtk.Widgetter) gtk.Widgetter {
	view := adw.NewToolbarView()
	header := adw.NewHeaderBar()
	header.SetShowTitle(false)
	view.AddTopBar(header)
	view.SetContent(child)
	return view
}

func pill(label string, suggested bool, onClick func()) *gtk.Button {
	b := gtk.NewButtonWithLabel(label)
	b.AddCSSClass("pill")
	if suggested {
		b.AddCSSClass("suggested-action")
	}
	b.SetHAlign(gtk.AlignCenter)
	b.ConnectClicked(onClick)
	return b
}

func buttons(bs ...*gtk.Button) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 12)
	box.SetHAlign(gtk.AlignCenter)
	for _, b := range bs {
		box.Append(b)
	}
	return box
}

func newSessionPages(w *window) *sessionPages {
	s := &sessionPages{w: w, pages: map[string]gtk.Widgetter{}}

	spinner := adw.NewSpinner()
	spinner.SetSizeRequest(48, 48)
	spinner.SetHAlign(gtk.AlignCenter)
	spinner.SetVAlign(gtk.AlignCenter)
	s.pages["starting"] = page(spinner)

	s.nobinary = adw.NewStatusPage()
	s.nobinary.SetIconName("dialog-question-symbolic")
	s.nobinary.SetTitle(T("Proton Drive CLI Needed"))
	s.nobinary.SetDescription(T("Quark works through the official Proton Drive command-line tool. Download it from proton.me, then choose the file here."))
	s.hints = gtk.NewLabel("")
	s.hints.AddCSSClass("dim-label")
	s.hints.SetWrap(true)
	s.hints.SetJustify(gtk.JustifyCenter)
	nbBox := buttons(
		pill(T("Choose the CLI…"), true, w.chooseCLI),
		pill(T("Get the CLI"), false, func() { w.openURI("https://proton.me/download/drive/cli/index.html") }),
	)
	nbBox.Append(s.hints)
	s.nobinary.SetChild(nbBox)
	s.pages["nobinary"] = page(s.nobinary)

	s.incompatible = adw.NewStatusPage()
	s.incompatible.SetIconName("software-update-available-symbolic")
	s.incompatible.SetTitle(T("Proton Drive CLI Update Needed"))
	s.incompatible.SetChild(buttons(
		pill(T("Get the Latest CLI"), true, func() { w.openURI("https://proton.me/download/drive/cli/index.html") }),
		pill(T("Choose Another File…"), false, w.chooseCLI),
	))
	s.pages["incompatible"] = page(s.incompatible)

	s.loggedout = adw.NewStatusPage()
	s.loggedout.SetIconName(AppID)
	s.loggedout.SetTitle(T("Welcome to Quark"))
	s.loggedout.SetDescription(T("Sign in with your Proton account to browse and transfer your files. Your password is entered on Proton's website, never in Quark."))
	s.loggedout.SetChild(buttons(pill(T("Sign In…"), true, w.a.Login)))
	s.pages["loggedout"] = page(s.loggedout)

	s.loggingin = adw.NewStatusPage()
	s.loggingin.SetTitle(T("Continue in Your Browser"))
	s.loggingin.SetDescription(T("Finish signing in on Proton's website. You can also open the link on another device."))
	sp := adw.NewSpinner()
	sp.SetSizeRequest(32, 32)
	sp.SetHAlign(gtk.AlignCenter)
	s.openBrowser = pill(T("Open Browser"), true, func() { w.openURI(s.loginURL) })
	s.copyLink = pill(T("Copy Link"), false, func() {
		w.win.Clipboard().SetText(s.loginURL)
		w.toastText(T("Sign-in link copied"))
	})
	lbox := buttons(s.openBrowser, s.copyLink, pill(T("Cancel"), false, w.a.CancelLogin))
	lbox.Prepend(sp)
	s.loggingin.SetChild(lbox)
	s.pages["loggingin"] = page(s.loggingin)

	s.failed = adw.NewStatusPage()
	s.failed.SetIconName("network-error-symbolic")
	s.failed.SetChild(buttons(pill(T("Try Again"), true, w.a.Start)))
	s.pages["failed"] = page(s.failed)
	return s
}

func (s *sessionPages) show(st app.Session) {
	name := "starting"
	switch st.Phase {
	case app.PhaseStarting:
	case app.PhaseNoBinary:
		name = "nobinary"
		if len(st.BinaryHints) > 0 {
			s.hints.SetText(Tf("Found a copy at %s. Choose it to use it; Quark will ask to make it executable.", strings.Join(st.BinaryHints, ", ")))
			s.hints.SetVisible(true)
		} else {
			s.hints.SetVisible(false)
		}
	case app.PhaseIncompatible:
		name = "incompatible"
		if st.Err != nil {
			s.incompatible.SetDescription(st.Err.Detail)
		}
	case app.PhaseLoggedOut:
		name = "loggedout"
		if st.Err != nil {
			s.w.onToast(app.Toast{Text: st.Err.Title, Err: st.Err})
		}
	case app.PhaseLoggingIn:
		name = "loggingin"
		s.loginURL = st.LoginURL
		s.openBrowser.SetSensitive(st.LoginURL != "")
		s.copyLink.SetSensitive(st.LoginURL != "")
	case app.PhaseFailed:
		name = "failed"
		if st.Err != nil {
			s.failed.SetTitle(st.Err.Title)
			s.failed.SetDescription(st.Err.Detail)
		}
	case app.PhaseReady:
		return
	}
	s.w.stack.SetVisibleChildName(name)
}

func (w *window) openURI(uri string) {
	if uri == "" {
		return
	}
	l := gtk.NewURILauncher(uri)
	l.Launch(context.Background(), &w.win.Window, func(res gio.AsyncResulter) {
		if err := l.LaunchFinish(res); err != nil {
			w.toastText(T("Could not open the browser"))
		}
	})
}

// chooseCLI lets the user pick the proton-drive binary.
func (w *window) chooseCLI() {
	d := gtk.NewFileDialog()
	d.SetTitle(T("Choose the Proton Drive CLI"))
	d.SetModal(true)
	d.Open(context.Background(), &w.win.Window, func(res gio.AsyncResulter) {
		f, err := d.OpenFinish(res)
		if err != nil || f == nil {
			return
		}
		path := f.Path()
		if path == "" {
			w.toastText(T("Choose a file on this computer"))
			return
		}
		w.useCLI(path)
	})
}
