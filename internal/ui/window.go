package ui

import (
	"context"
	"errors"
	"log/slog"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"example.org/quark/internal/app"
	"example.org/quark/internal/drive"
	"example.org/quark/internal/i18n"
	"example.org/quark/internal/jobs"
	"example.org/quark/internal/tray"
)

// Translation helpers (see internal/i18n).
var (
	T  = i18n.T
	Tf = i18n.Tf
	N  = i18n.N

	errSaveFailed = errors.New("could not save the PNG")
)

// Sidebar entries, in display order.
type place struct {
	id      string
	title   string
	icon    string
	section drive.Section // empty for the transfers view
}

var places = []place{
	{"my-files", "My files", "user-home-symbolic", drive.SectionMyFiles},
	{"shared-with-me", "Shared with me", "folder-publicshare-symbolic", drive.SectionSharedWithMe},
	{"shared-by-me", "Shared by me", "send-to-symbolic", drive.SectionSharedByMe},
	{"devices", "Computers", "computer-symbolic", drive.SectionDevices},
	{"trash", "Trash", "user-trash-symbolic", drive.SectionTrash},
	{"transfers", "Transfers", "network-transmit-receive-symbolic", ""},
}

type window struct {
	gapp *adw.Application
	win  *adw.ApplicationWindow
	a    *app.App
	q    *jobs.Queue
	log  *slog.Logger
	opts Options

	toasts  *adw.ToastOverlay
	stack   *gtk.Stack
	session *sessionPages

	split        *adw.NavigationSplitView
	sidebarList  *gtk.ListBox
	sidebarRows  map[string]*gtk.ListBoxRow
	badge        *gtk.Label
	files        map[string]*filesView
	transfers    *transfersView
	current      string
	selectedOnce bool
	openOnFinish map[string]string // transfer id -> local file to open
	tray         *tray.Tray
	quitting     bool
}

func newWindow(gapp *adw.Application, o Options) *window {
	w := &window{
		gapp: gapp, a: o.App, q: o.Queue, log: o.Logger, opts: o,
		files: map[string]*filesView{}, sidebarRows: map[string]*gtk.ListBoxRow{},
		openOnFinish: map[string]string{},
	}
	w.win = adw.NewApplicationWindow(&gapp.Application)
	w.win.SetTitle("Quark")
	st := o.App.Settings()
	w.win.SetDefaultSize(st.Window.Width, st.Window.Height)
	if st.Window.Maximized {
		w.win.Maximize()
	}
	w.win.SetSizeRequest(360, 294)

	w.toasts = adw.NewToastOverlay()
	w.stack = gtk.NewStack()
	w.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	w.session = newSessionPages(w)
	for name, page := range w.session.pages {
		w.stack.AddNamed(page, name)
	}
	w.stack.AddNamed(w.buildMain(), "ready")
	w.toasts.SetChild(w.stack)
	w.win.SetContent(w.toasts)

	// Narrow windows collapse the sidebar (adaptive layout).
	bp := adw.NewBreakpoint(adw.BreakpointConditionParse("max-width: 600sp"))
	bp.AddSetter(w.split, "collapsed", true)
	w.win.AddBreakpoint(bp)

	w.win.ConnectCloseRequest(w.onCloseRequest)
	w.a.OnSession(w.onSession)
	w.a.OnToast(w.onToast)
	w.q.OnChange(w.onQueueChange)
	w.q.OnFinished(w.onTransferFinished)
	w.onSession(w.a.Session())
	return w
}

func (w *window) buildMain() gtk.Widgetter {
	w.split = adw.NewNavigationSplitView()
	w.split.SetMinSidebarWidth(200)
	w.split.SetMaxSidebarWidth(260)

	// Sidebar.
	w.sidebarList = gtk.NewListBox()
	w.sidebarList.AddCSSClass("navigation-sidebar")
	w.sidebarList.SetSelectionMode(gtk.SelectionBrowse)
	for _, p := range places {
		box := gtk.NewBox(gtk.OrientationHorizontal, 12)
		img := gtk.NewImageFromIconName(p.icon)
		label := gtk.NewLabel(T(p.title))
		label.SetXAlign(0)
		label.SetHExpand(true)
		box.Append(img)
		box.Append(label)
		if p.id == "transfers" {
			w.badge = gtk.NewLabel("")
			w.badge.AddCSSClass("quark-badge")
			w.badge.SetVisible(false)
			box.Append(w.badge)
		}
		row := gtk.NewListBoxRow()
		row.SetChild(box)
		row.SetName(p.id)
		w.sidebarList.Append(row)
		w.sidebarRows[p.id] = row
	}
	w.sidebarList.ConnectRowSelected(func(row *gtk.ListBoxRow) {
		if row != nil {
			w.showSection(row.Name())
		}
	})
	w.sidebarList.ConnectRowActivated(func(*gtk.ListBoxRow) { w.split.SetShowContent(true) })

	sideHeader := adw.NewHeaderBar()
	sideHeader.SetTitleWidget(adw.NewWindowTitle("Quark", ""))
	menu := gio.NewMenu()
	section := gio.NewMenu()
	section.Append(T("Preferences"), "app.preferences")
	section.Append(T("Keyboard Shortcuts"), "app.shortcuts")
	section.Append(T("About Quark"), "app.about")
	menu.AppendSection("", section)
	account := gio.NewMenu()
	account.Append(T("Sign Out…"), "app.logout")
	menu.AppendSection("", account)
	menuButton := gtk.NewMenuButton()
	menuButton.SetIconName("open-menu-symbolic")
	menuButton.SetMenuModel(menu)
	menuButton.SetTooltipText(T("Main Menu"))
	menuButton.SetPrimary(true)
	sideHeader.PackEnd(menuButton)

	scroll := gtk.NewScrolledWindow()
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scroll.SetChild(w.sidebarList)
	sideView := adw.NewToolbarView()
	sideView.AddTopBar(sideHeader)
	sideView.SetContent(scroll)
	if w.opts.Demo {
		banner := adw.NewBanner(T("Demo mode — sample data"))
		banner.SetRevealed(true)
		sideView.AddBottomBar(banner)
	}
	w.split.SetSidebar(adw.NewNavigationPage(sideView, "Quark"))
	w.split.SetContent(adw.NewNavigationPage(adw.NewBin(), "Quark"))
	return w.split
}

// showSection switches the content pane to a sidebar entry.
func (w *window) showSection(id string) {
	if id == w.current {
		return
	}
	var p *place
	for i := range places {
		if places[i].id == id {
			p = &places[i]
		}
	}
	if p == nil {
		return
	}
	w.current = id
	if row := w.sidebarRows[id]; row != nil && w.sidebarList.SelectedRow() != row {
		w.sidebarList.SelectRow(row)
	}
	if p.section == "" {
		if w.transfers == nil {
			w.transfers = newTransfersView(w)
		}
		w.split.SetContent(w.transfers.page)
		w.transfers.refresh()
		return
	}
	fv := w.files[id]
	if fv == nil {
		fv = newFilesView(w, p.section, T(p.title))
		w.files[id] = fv
		fv.b.Navigate(app.Root(p.section))
	} else if fv.b.State().Err != nil {
		fv.b.Refresh()
	}
	w.split.SetContent(fv.page)
}

func (w *window) visibleFiles() *filesView { return w.files[w.current] }

func (w *window) onSession(s app.Session) {
	if s.Phase == app.PhaseReady {
		w.stack.SetVisibleChildName("ready")
		if w.current == "" {
			w.showSection("my-files")
		}
	} else {
		w.session.show(s)
	}
	w.q.SetEnabled(s.Phase == app.PhaseReady)
}

func (w *window) onToast(t app.Toast) {
	toast := adw.NewToast(t.Text)
	toast.SetTimeout(5)
	if t.Action != nil {
		toast.SetButtonLabel(t.ActionLabel)
		toast.ConnectButtonClicked(t.Action)
	} else if t.Err != nil && (t.Err.Detail != "" || t.Err.Technical != "") {
		ue := t.Err
		toast.SetButtonLabel(T("Details"))
		toast.ConnectButtonClicked(func() { w.showError(ue) })
	}
	w.toasts.AddToast(toast)
}

func (w *window) toastText(text string) { w.onToast(app.Toast{Text: text}) }

// showError presents a UserError in a dialog.
func (w *window) showError(ue *app.UserError) {
	body := ue.Detail
	if ue.Technical != "" && ue.Technical != ue.Detail {
		body += "\n\n" + Tf("Details: %s", ue.Technical)
	}
	d := adw.NewAlertDialog(ue.Title, body)
	d.AddResponse("close", T("Close"))
	d.SetDefaultResponse("close")
	d.SetCloseResponse("close")
	d.Present(w.win)
}

func (w *window) onQueueChange() {
	w.updateTray()
	active, failed := w.q.Counts()
	switch {
	case active > 0:
		w.badge.SetText(Tf("%d", active))
		w.badge.RemoveCSSClass("error")
		w.badge.SetVisible(true)
	case failed > 0:
		w.badge.SetText("!")
		w.badge.AddCSSClass("error")
		w.badge.SetVisible(true)
	default:
		w.badge.SetVisible(false)
	}
	if w.transfers != nil {
		w.transfers.refresh()
	}
}

func (w *window) onTransferFinished(t jobs.Transfer) {
	// Files opened from the browser are launched once downloaded.
	if path, ok := w.openOnFinish[t.ID]; ok {
		delete(w.openOnFinish, t.ID)
		if t.Status == jobs.Done {
			w.launchFile(path)
			return
		}
	}
	// Refresh the destination folder of finished uploads.
	if t.Kind == jobs.Upload {
		for _, fv := range w.files {
			if fv.b.State().Location.ListPath() == t.Remote {
				fv.b.Refresh()
			}
		}
	}
	if w.win.IsActive() {
		return
	}
	var title string
	switch {
	case t.Status == jobs.Done && t.Kind == jobs.Upload:
		title = T("Upload finished")
	case t.Status == jobs.Done:
		title = T("Download finished")
	case t.Kind == jobs.Upload:
		title = T("Upload failed")
	default:
		title = T("Download failed")
	}
	n := gio.NewNotification(title)
	n.SetBody(t.Name)
	n.SetDefaultAction("app.activate")
	w.gapp.SendNotification("transfer-"+t.ID, n)
}

func (w *window) launchFile(path string) {
	l := gtk.NewFileLauncher(gio.NewFileForPath(path))
	l.Launch(context.Background(), &w.win.Window, func(res gio.AsyncResulter) {
		if err := l.LaunchFinish(res); err != nil {
			w.toastText(Tf("Could not open the file: %s", err.Error()))
		}
	})
}

func (w *window) saveGeometry() {
	st := w.a.Settings()
	st.Window.Maximized = w.win.IsMaximized()
	if !st.Window.Maximized {
		st.Window.Width, st.Window.Height = w.win.DefaultSize()
	}
	w.a.UpdateSettings(st)
}

func (w *window) confirmLogout() {
	if w.a.Session().Phase != app.PhaseReady {
		return
	}
	active, _ := w.q.Counts()
	body := T("You will need to sign in again to use Quark. Files on your computer are not affected.")
	if active > 0 {
		body += "\n\n" + Tf(N("%d transfer is still in progress and will be paused.", "%d transfers are still in progress and will be paused.", active), active)
	}
	d := adw.NewAlertDialog(T("Sign Out?"), body)
	d.AddResponse("cancel", T("Cancel"))
	d.AddResponse("logout", T("Sign Out"))
	d.SetResponseAppearance("logout", adw.ResponseDestructive)
	d.SetDefaultResponse("cancel")
	d.SetCloseResponse("cancel")
	d.ConnectResponse(func(r string) {
		if r == "logout" {
			w.a.Logout()
		}
	})
	d.Present(w.win)
}
