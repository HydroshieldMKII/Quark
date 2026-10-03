//go:build uitest

// UI smoke tests. They run the real window against the demo backend inside a
// headless compositor (see `make test-ui`), with GLib criticals made fatal
// by G_DEBUG=fatal-criticals, so any GTK misuse fails the run.
package ui

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"example.org/quark/internal/app"
	"example.org/quark/internal/app/demo"
	"example.org/quark/internal/drive"
	"example.org/quark/internal/jobs"
	"example.org/quark/internal/settings"
)

var (
	testWin *window
	backend *demo.Backend
)

func init() { runtime.LockOSThread() } // GTK must run on the main thread

func TestMain(m *testing.M) {
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
	eventually(t, "documents", func() bool { return v.title.Title() == "Documents" && !v.b.State().Loading })
	onMain(func() {
		if v.title.Subtitle() != "My files" || v.model.NItems() != 3 || !v.upButton.Visible() {
			t.Errorf("title=%q subtitle=%q rows=%d", v.title.Title(), v.title.Subtitle(), v.model.NItems())
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
	eventually(t, "back at root", func() bool { return v.title.Title() == "My files" && v.model.NItems() == 7 })
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
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
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
	onMain(func() { testWin.win.SetDefaultSize(400, 700) })
	eventually(t, "collapsed", func() bool { return testWin.split.Collapsed() })
	onMain(func() { testWin.win.SetDefaultSize(1100, 720) })
	eventually(t, "expanded", func() bool { return !testWin.split.Collapsed() })
}
