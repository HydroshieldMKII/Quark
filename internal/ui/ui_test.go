//go:build uitest

// UI smoke tests. They run the real window against the demo backend inside a
// headless compositor (see `make test-ui`), with GLib criticals made fatal
// by G_DEBUG=fatal-criticals, so any GTK misuse fails the run.
package ui

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/godbus/dbus/v5"

	"example.org/quark/internal/app"
	"example.org/quark/internal/app/demo"
	"example.org/quark/internal/drive"
	"example.org/quark/internal/fsutil"
	"example.org/quark/internal/jobs"
	"example.org/quark/internal/pdcli"
	"example.org/quark/internal/settings"
)

var (
	testWin *window
	backend *demo.Backend
)

func init() { runtime.LockOSThread() } // GTK must run on the main thread

// fakeWatcher plays the GNOME AppIndicator extension on the test's private bus.
type fakeWatcher struct{}

func (fakeWatcher) RegisterStatusNotifierItem(dbus.Sender, string) *dbus.Error { return nil }

func startFakeTrayHost() {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		panic(err)
	}
	if err := conn.Export(fakeWatcher{}, "/StatusNotifierWatcher", "org.kde.StatusNotifierWatcher"); err != nil {
		panic(err)
	}
	if _, err := conn.RequestName("org.kde.StatusNotifierWatcher", dbus.NameFlagDoNotQueue); err != nil {
		panic(err)
	}
}

func TestMain(m *testing.M) {
	startFakeTrayHost()
	backend = demo.New(time.Now())
	backend.Latency = 20 * time.Millisecond
	loop := MainLoop{}
	a := app.New(app.Config{
		Dispatcher: loop, Settings: settings.Defaults(),
		FindBinary: func(string) (string, []string) { return "demo", nil },
		NewBackend: func(string, settings.Settings) app.Backend { return backend },
	})
	q := jobs.NewQueue(jobs.Config{Dispatcher: loop, Runner: func() jobs.Runner { return backend }})
	gapp := adw.NewApplication(AppID+".Test", gio.ApplicationNonUnique)
	code := 0
	gapp.ConnectStartup(func() {
		installCSS()
		installIcons()
	})
	gapp.ConnectActivate(func() {
		testWin = newWindow(gapp, Options{App: a, Queue: q, Demo: true, Version: "test"})
		testWin.startTray()
		testWin.win.Present()
		a.Start()
		go func() {
			code = m.Run()
			glib.IdleAdd(func() {
				q.Shutdown(5 * time.Second)
				a.Shutdown(5 * time.Second)
				gapp.Quit()
			})
		}()
	})
	if rc := gapp.Run([]string{os.Args[0]}); rc != 0 && code == 0 {
		code = rc
	}
	os.Exit(code)
}

// onMain runs fn on the GTK main loop and waits for it.
func onMain(fn func()) {
	done := make(chan struct{})
	glib.IdleAdd(func() {
		defer close(done)
		fn()
	})
	<-done
}

// eventually polls cond on the main loop until it holds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ok := false
		onMain(func() { ok = cond() })
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func files(t *testing.T, id string) *filesView {
	t.Helper()
	var v *filesView
	onMain(func() {
		testWin.showSection(id)
		v = testWin.files[id]
	})
	if v == nil {
		t.Fatalf("no files view for %s", id)
	}
	eventually(t, id+" listed", func() bool { return !v.b.State().Loading })
	return v
}

func find(v *filesView, name string) (drive.Node, bool) {
	for _, n := range v.b.State().Visible {
		if n.Name == name {
			return n, true
		}
	}
	return drive.Node{}, false
}

func TestReadyAndListing(t *testing.T) {
	eventually(t, "ready", func() bool { return testWin.a.Session().Phase == app.PhaseReady })
	v := files(t, "my-files")
	onMain(func() {
		if got := v.model.NItems(); got != 7 {
			t.Errorf("rows = %d", got)
		}
		if v.stack.VisibleChildName() != "list" {
			t.Errorf("stack = %q", v.stack.VisibleChildName())
		}
		if v.upButton.Visible() {
			t.Error("up button visible at the root")
		}
	})
}

func TestNavigateFilterAndUp(t *testing.T) {
	v := files(t, "my-files")
	onMain(func() {
		n, ok := find(v, "Documents")
		if !ok {
			t.Fatal("Documents missing")
		}
		v.activate(n)
	})
	eventually(t, "documents", func() bool { return pathLabels(v) == "My files › Documents" && !v.b.State().Loading })
	onMain(func() {
		if v.titleStack.VisibleChildName() != "path" || v.model.NItems() != 3 || !v.upButton.Visible() {
			t.Errorf("path=%q rows=%d", pathLabels(v), v.model.NItems())
		}
		v.search.SetSearchMode(true)
		v.entry.SetText("pdf")
	})
	eventually(t, "filtered", func() bool { return v.model.NItems() == 2 })
	onMain(func() {
		v.entry.SetText("zzz")
	})
	eventually(t, "no matches", func() bool { return v.stack.VisibleChildName() == "empty" })
	onMain(func() {
		v.search.SetSearchMode(false) // clears the filter
		v.b.Up()
	})
	eventually(t, "back at root", func() bool {
		return v.titleStack.VisibleChildName() == "title" && v.title.Title() == "My files" && v.model.NItems() == 7
	})
}

func TestCreateRenameTrashRestore(t *testing.T) {
	v := files(t, "my-files")
	onMain(func() { testWin.a.CreateFolder(app.Root(drive.SectionMyFiles), "Quark test folder") })
	eventually(t, "created", func() bool { _, ok := find(v, "Quark test folder"); return ok })

	onMain(func() {
		n, _ := find(v, "Quark test folder")
		testWin.a.Rename(v.b.State().Location, n, "Renamed folder")
	})
	eventually(t, "renamed", func() bool { _, ok := find(v, "Renamed folder"); return ok })

	onMain(func() {
		n, _ := find(v, "Renamed folder")
		testWin.a.Trash(v.b.State().Location, []drive.Node{n})
	})
	eventually(t, "trashed", func() bool { _, ok := find(v, "Renamed folder"); return !ok })

	tv := files(t, "trash")
	eventually(t, "in trash", func() bool { _, ok := find(tv, "Renamed folder"); return ok })
	onMain(func() {
		if tv.emptyBtn == nil || !tv.emptyBtn.Sensitive() {
			t.Error("empty trash button not available")
		}
		n, _ := find(tv, "Renamed folder")
		testWin.a.Restore(tv.allNodes(), []drive.Node{n})
	})
	eventually(t, "restored", func() bool { _, ok := find(tv, "Renamed folder"); return !ok })
}

func TestEmptyFolderState(t *testing.T) {
	v := files(t, "my-files")
	onMain(func() { testWin.a.CreateFolder(app.Root(drive.SectionMyFiles), "Empty one") })
	eventually(t, "created", func() bool { _, ok := find(v, "Empty one"); return ok })
	onMain(func() {
		n, _ := find(v, "Empty one")
		v.activate(n)
	})
	eventually(t, "empty state", func() bool { return v.stack.VisibleChildName() == "empty" })
	onMain(func() {
		if v.emptyPage.Title() != "Folder Is Empty" {
			t.Errorf("empty title = %q", v.emptyPage.Title())
		}
		v.b.Up()
	})
}

func TestAllSectionsAndTransfers(t *testing.T) {
	for _, p := range places {
		id := p.id
		onMain(func() { testWin.showSection(id) })
		if p.section != "" {
			v := files(t, id)
			eventually(t, id+" settled", func() bool {
				name := v.stack.VisibleChildName()
				return name == "list" || name == "empty"
			})
		}
	}
	onMain(func() {
		if testWin.transfers == nil || testWin.transfers.stack.VisibleChildName() == "" {
			t.Error("transfers view not built")
		}
	})
}

func TestUploadShowsInTransfers(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/upload-me.txt"
	if err := os.WriteFile(path, []byte("hello"), fsutil.FilePrivate); err != nil {
		t.Fatal(err)
	}
	v := files(t, "my-files")
	onMain(func() { v.enqueueUploads(v.b.State().Location, []string{path}) })
	eventually(t, "transfer row", func() bool {
		testWin.showSection("transfers")
		return len(testWin.transfers.rows) == 1
	})
	eventually(t, "upload done", func() bool {
		items := testWin.q.Items()
		return len(items) == 1 && items[0].Status == jobs.Done
	})
	onMain(func() {
		for _, r := range testWin.transfers.rows {
			if r.cancel.Visible() || !r.remove.Visible() {
				t.Error("finished row shows the wrong buttons")
			}
		}
		testWin.q.ClearFinished()
	})
	eventually(t, "cleared", func() bool { return len(testWin.transfers.rows) == 0 })
}

func TestDialogsOpenAndClose(t *testing.T) {
	onMain(func() {
		testWin.showPreferences()
		testWin.showAbout()
		testWin.showShortcuts()
	})
	eventually(t, "dialogs shown", func() bool { return testWin.win.VisibleDialog() != nil })
	for i := 0; i < 3; i++ {
		onMain(func() {
			if d := testWin.win.VisibleDialog(); d != nil {
				d.ForceClose()
			}
		})
	}
	eventually(t, "dialogs closed", func() bool { return testWin.win.VisibleDialog() == nil })
}

func TestNameDialogValidation(t *testing.T) {
	var got []string
	onMain(func() {
		nameDialog(testWin, "New Folder", "Create", "Untitled", func(s string) { got = append(got, s) })
	})
	var d *adw.AlertDialog
	eventually(t, "name dialog", func() bool {
		vd := testWin.win.VisibleDialog()
		if vd == nil {
			return false
		}
		d, _ = vd.Cast().(*adw.AlertDialog)
		return d != nil
	})
	onMain(func() {
		entry := findEntry(d.ExtraChild())
		if entry == nil {
			t.Fatal("no entry")
		}
		if d.ResponseEnabled("ok") {
			t.Error("unchanged name accepted")
		}
		entry.SetText("a/b")
		if d.ResponseEnabled("ok") {
			t.Error("invalid name accepted")
		}
		entry.SetText("Fine name")
		if !d.ResponseEnabled("ok") {
			t.Error("valid name refused")
		}
		// AdwAlertDialog has no public "respond" call: emit the signal as a
		// click would, then close.
		d.Emit("response", "ok")
		d.ForceClose()
	})
	eventually(t, "dialog closed", func() bool { return testWin.win.VisibleDialog() == nil })
	if fmt.Sprint(got) != "[Fine name]" {
		t.Fatalf("got %q", got)
	}
}

func findEntry(w gtk.Widgetter) *gtk.Entry {
	if w == nil {
		return nil
	}
	if e, ok := w.(*gtk.Entry); ok {
		return e
	}
	for c := gtk.BaseWidget(w).FirstChild(); c != nil; c = gtk.BaseWidget(c).NextSibling() {
		if e := findEntry(c); e != nil {
			return e
		}
	}
	return nil
}

func TestAdaptiveLayout(t *testing.T) {
	v := files(t, "my-files")
	onMain(func() { testWin.win.SetDefaultSize(400, 700) })
	eventually(t, "collapsed", func() bool { return testWin.split.Collapsed() })
	onMain(func() {
		if v.sizeCol.Visible() || v.modCol.Visible() {
			t.Error("Size/Modified columns visible at phone width")
		}
	})
	onMain(func() { testWin.win.SetDefaultSize(1100, 720) })
	eventually(t, "expanded", func() bool { return !testWin.split.Collapsed() && v.sizeCol.Visible() })
}

func TestCloseHidesToTrayAndReopens(t *testing.T) {
	onMain(func() {
		if testWin.tray == nil {
			t.Fatal("tray not started although a tray host is present")
		}
		testWin.win.Close()
	})
	eventually(t, "hidden", func() bool { return !testWin.win.Visible() })
	onMain(func() {
		if !testWin.a.Settings().BackgroundHintShown {
			t.Error("background notice not recorded")
		}
		testWin.present()
	})
	eventually(t, "shown again", func() bool { return testWin.win.Visible() })
}

func TestPauseFromWindow(t *testing.T) {
	onMain(func() {
		testWin.togglePause()
		if !testWin.q.Paused() {
			t.Error("not paused")
		}
		testWin.togglePause()
		if testWin.q.Paused() {
			t.Error("not resumed")
		}
	})
}

// pathLabels reads the breadcrumb bar as text.
func pathLabels(v *filesView) string {
	var parts []string
	for c := v.path.FirstChild(); c != nil; c = gtk.BaseWidget(c).NextSibling() {
		switch w := c.(type) {
		case *gtk.Label:
			parts = append(parts, w.Text())
		case *gtk.Button:
			parts = append(parts, w.Label())
		}
	}
	return strings.Join(parts, " ")
}

func TestBreadcrumbNavigation(t *testing.T) {
	v := files(t, "my-files")
	onMain(func() {
		n, _ := find(v, "Photos")
		v.activate(n)
	})
	eventually(t, "photos", func() bool { return pathLabels(v) == "My files › Photos" })
	onMain(func() {
		// Click the "My files" crumb.
		b, ok := v.path.FirstChild().(*gtk.Button)
		if !ok {
			t.Fatal("first crumb is not a button")
		}
		b.Emit("clicked")
	})
	eventually(t, "back at root", func() bool { return v.titleStack.VisibleChildName() == "title" })
	onMain(func() {
		if !v.addButton.Visible() {
			t.Error("+ menu hidden in My files")
		}
	})
	tv := files(t, "trash")
	onMain(func() {
		if tv.addButton.Visible() {
			t.Error("+ menu shown in the trash")
		}
	})
}

func TestShareDialog(t *testing.T) {
	v := files(t, "my-files")
	var sd *shareDialog
	onMain(func() {
		n, _ := find(v, "Shared plans")
		sd = &shareDialog{w: testWin, v: testWin.a.NewShare(v.b.State().Location, n)}
		sd.d = adw.NewDialog()
		sd.stack = gtk.NewStack()
		sd.errPage = adw.NewStatusPage()
		sd.banner = adw.NewBanner("")
		sd.spinner = adw.NewSpinner()
		sd.stack.AddNamed(sd.errPage, "error")
		sd.stack.AddNamed(sd.buildPage(), "page")
		sd.d.SetChild(sd.stack)
		sd.v.OnChange(sd.update)
		sd.d.Present(testWin.win)
	})
	eventually(t, "share loaded", func() bool { return !sd.v.State().Loading && sd.v.State().Sharing != nil })
	onMain(func() {
		if !sd.linkSwitch.Active() || sd.url == "" || len(sd.members) != 1 {
			t.Errorf("switch=%v url=%q members=%d", sd.linkSwitch.Active(), sd.url, len(sd.members))
		}
		sd.inviteRow.SetText("new@example.com")
		sd.invite()
	})
	eventually(t, "invited", func() bool { return !sd.v.State().Busy && len(sd.members) == 2 })
	onMain(func() {
		sd.accessRow.SetSelected(1) // can edit
	})
	eventually(t, "role changed", func() bool {
		st := sd.v.State()
		return !st.Busy && st.Sharing != nil && st.Sharing.Link != nil && st.Sharing.Link.Role == drive.RoleEditor
	})
	// Late notifications must not undo the change.
	time.Sleep(300 * time.Millisecond)
	onMain(func() {
		if st := sd.v.State(); st.Sharing.Link.Role != drive.RoleEditor || sd.accessRow.Selected() != 1 {
			t.Errorf("role reverted: %v, selected %d", st.Sharing.Link.Role, sd.accessRow.Selected())
		}
	})
	onMain(func() {
		sd.v.RemoveLink()
	})
	eventually(t, "link removed", func() bool { return !sd.v.State().Busy && !sd.linkSwitch.Active() && !sd.urlRow.Visible() })
	onMain(func() { sd.d.ForceClose() })
}

func TestInvitationsBannerAndDialog(t *testing.T) {
	v := files(t, "shared-with-me")
	eventually(t, "banner", func() bool { return v.invBanner.Revealed() })
	onMain(v.showInvitations)
	eventually(t, "dialog", func() bool { return testWin.win.VisibleDialog() != nil })
	onMain(func() { v.inv.Accept(v.inv.State().Items[0]) })
	eventually(t, "accepted", func() bool {
		return !v.invBanner.Revealed() && testWin.win.VisibleDialog() == nil
	})
}

func TestTransfersSummary(t *testing.T) {
	items := []jobs.Transfer{{Status: jobs.Running}, {Status: jobs.Queued}, {Status: jobs.Queued}, {Status: jobs.Failed}, {Status: jobs.Done}}
	if got := transfersSummary(items, false); got != "1 in progress · 2 waiting · 1 failed · 1 done" {
		t.Errorf("summary = %q", got)
	}
	if got := transfersSummary(items, true); got != "3 paused · 1 failed · 1 done" {
		t.Errorf("paused summary = %q", got)
	}
	if transfersSummary(nil, false) != "" {
		t.Error("empty summary")
	}
}

func TestToastsShowInsideOpenDialog(t *testing.T) {
	v := files(t, "my-files")
	var overlay *adw.ToastOverlay
	onMain(func() {
		d := adw.NewDialog()
		d.SetTitle("Test")
		testWin.presentDialog(d, gtk.NewLabel("content"))
		overlay, _ = testWin.toastSinks[len(testWin.toastSinks)-1].(*adw.ToastOverlay)
		if overlay == nil {
			t.Fatal("dialog has no toast overlay")
		}
		testWin.toastText("hello from a dialog")
	})
	eventually(t, "toast in dialog", func() bool {
		// A ToastOverlay with a toast has more than its child in the tree.
		return overlay.FirstChild() != nil && gtk.BaseWidget(overlay.FirstChild()).NextSibling() != nil
	})
	onMain(func() {
		testWin.win.VisibleDialog().ForceClose()
	})
	eventually(t, "sink popped", func() bool { return len(testWin.toastSinks) == 0 })
	_ = v
}

func TestLinkActionsAndProperties(t *testing.T) {
	v := files(t, "my-files")
	// Earlier tests may have removed the demo link: make sure one exists.
	n, _ := find(v, "Shared plans")
	if _, err := backend.SetLink(context.Background(), drive.ByUID(n.UID), pdcli.LinkSpec{Role: drive.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	onMain(func() { v.b.Refresh() })
	eventually(t, "link visible", func() bool { n, _ = find(v, "Shared plans"); return n.IsSharedByURL })
	onMain(func() {
		v.sel.SelectItem(v.index[n.UID], true)
		v.updateActions()
		if !v.actions["copy-link"].Enabled() || !v.actions["remove-link"].Enabled() || !v.actions["properties"].Enabled() {
			t.Errorf("link actions disabled for a linked item: %+v", n)
		}
		v.actions["copy-link"].Activate(nil)
	})
	eventually(t, "link copied", func() bool { return strings.HasPrefix(testWin.lastCopied, "https://drive.proton.me/urls/") })
	onMain(func() {
		n, _ := find(v, "Notes.md")
		v.sel.SelectItem(v.index[n.UID], true)
		v.updateActions()
		if v.actions["copy-link"].Enabled() {
			t.Error("copy-link enabled for an unshared item")
		}
		v.showProperties(n)
	})
	eventually(t, "properties", func() bool { return testWin.win.VisibleDialog() != nil })
	onMain(func() { testWin.win.VisibleDialog().ForceClose() })
	eventually(t, "closed", func() bool { return testWin.win.VisibleDialog() == nil })
}
