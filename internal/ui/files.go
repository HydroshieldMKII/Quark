package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"example.org/quark/internal/app"
	"example.org/quark/internal/drive"
	"example.org/quark/internal/jobs"
)

// filesView shows one section of the drive.
type filesView struct {
	w       *window
	b       *app.Browser
	section drive.Section
	label   string

	page      *adw.NavigationPage
	title     *adw.WindowTitle
	upButton  *gtk.Button
	newFolder *gtk.Button
	upload    *gtk.MenuButton
	emptyBtn  *gtk.Button
	search    *gtk.SearchBar
	entry     *gtk.SearchEntry
	stack     *gtk.Stack
	errPage   *adw.StatusPage
	emptyPage *adw.StatusPage

	model *gtk.StringList
	sel   *gtk.MultiSelection
	view  *gtk.ColumnView
	popup *gtk.PopoverMenu
	nodes map[string]drive.Node
	order []string
	index map[string]uint // uid -> position

	actions map[string]*gio.SimpleAction
	sortAct *gio.SimpleAction
	descAct *gio.SimpleAction
}

func newFilesView(w *window, section drive.Section, label string) *filesView {
	v := &filesView{w: w, section: section, label: label, nodes: map[string]drive.Node{}, actions: map[string]*gio.SimpleAction{}}
	v.b = w.a.NewBrowser(app.Root(section))

	header := adw.NewHeaderBar()
	v.title = adw.NewWindowTitle(label, "")
	header.SetTitleWidget(v.title)

	v.upButton = gtk.NewButtonFromIconName("go-up-symbolic")
	v.upButton.SetTooltipText(T("Parent Folder"))
	v.upButton.SetActionName("files.up")
	header.PackStart(v.upButton)

	v.newFolder = gtk.NewButtonFromIconName("folder-new-symbolic")
	v.newFolder.SetTooltipText(T("New Folder"))
	v.newFolder.SetActionName("files.new-folder")
	header.PackStart(v.newFolder)

	upMenu := gio.NewMenu()
	upMenu.Append(T("Upload Files…"), "files.upload-files")
	upMenu.Append(T("Upload Folder…"), "files.upload-folder")
	v.upload = gtk.NewMenuButton()
	v.upload.SetIconName("document-send-symbolic")
	v.upload.SetTooltipText(T("Upload"))
	v.upload.SetMenuModel(upMenu)
	header.PackStart(v.upload)

	viewMenu := gio.NewMenu()
	sortSection := gio.NewMenu()
	sortSection.Append(T("Name"), "files.sort::name")
	sortSection.Append(T("Size"), "files.sort::size")
	sortSection.Append(T("Modified"), "files.sort::modified")
	viewMenu.AppendSection(T("Sort By"), sortSection)
	order := gio.NewMenu()
	order.Append(T("Descending"), "files.descending")
	viewMenu.AppendSection("", order)
	other := gio.NewMenu()
	other.Append(T("Refresh"), "files.refresh")
	viewMenu.AppendSection("", other)
	viewButton := gtk.NewMenuButton()
	viewButton.SetIconName("view-more-symbolic")
	viewButton.SetTooltipText(T("View Options"))
	viewButton.SetMenuModel(viewMenu)
	header.PackEnd(viewButton)

	searchToggle := gtk.NewToggleButton()
	searchToggle.SetIconName("system-search-symbolic")
	searchToggle.SetTooltipText(T("Search This Folder"))
	header.PackEnd(searchToggle)

	if section == drive.SectionTrash {
		v.emptyBtn = gtk.NewButtonWithLabel(T("Empty Trash…"))
		v.emptyBtn.AddCSSClass("destructive-action")
		v.emptyBtn.SetActionName("files.empty-trash")
		header.PackEnd(v.emptyBtn)
	}

	v.entry = gtk.NewSearchEntry()
	v.entry.SetPlaceholderText(T("Filter by name"))
	v.entry.ConnectSearchChanged(func() { v.b.SetFilter(v.entry.Text()) })
	v.search = gtk.NewSearchBar()
	v.search.SetChild(v.entry)
	v.search.ConnectEntry(v.entry)
	// Keep the toggle button and the search bar in sync both ways.
	searchToggle.ConnectToggled(func() {
		if v.search.SearchMode() != searchToggle.Active() {
			v.search.SetSearchMode(searchToggle.Active())
		}
	})
	v.search.NotifyProperty("search-mode-enabled", func() {
		if searchToggle.Active() != v.search.SearchMode() {
			searchToggle.SetActive(v.search.SearchMode())
		}
		if !v.search.SearchMode() {
			v.entry.SetText("")
		}
	})

	v.buildList()
	v.stack = gtk.NewStack()
	v.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	spinner := adw.NewSpinner()
	spinner.SetSizeRequest(32, 32)
	spinner.SetHAlign(gtk.AlignCenter)
	spinner.SetVAlign(gtk.AlignCenter)
	v.stack.AddNamed(spinner, "loading")
	v.errPage = adw.NewStatusPage()
	v.errPage.SetIconName("dialog-warning-symbolic")
	retry := pill(T("Try Again"), true, func() { v.b.Refresh() })
	v.errPage.SetChild(buttons(retry))
	v.stack.AddNamed(v.errPage, "error")
	v.emptyPage = adw.NewStatusPage()
	v.stack.AddNamed(v.emptyPage, "empty")
	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(v.view)
	scroll.SetVExpand(true)
	v.stack.AddNamed(scroll, "list")

	content := gtk.NewBox(gtk.OrientationVertical, 0)
	content.Append(v.search)
	content.Append(v.stack)

	view := adw.NewToolbarView()
	view.AddTopBar(header)
	view.SetContent(content)
	v.page = adw.NewNavigationPage(view, label)
	v.search.SetKeyCaptureWidget(v.page)

	v.installActions()
	v.installDrop(content)
	v.b.OnChange(v.update)
	v.update(v.b.State())
	return v
}

// ----- list -----

func (v *filesView) buildList() {
	v.model = gtk.NewStringList(nil)
	v.sel = gtk.NewMultiSelection(v.model)
	v.sel.ConnectSelectionChanged(func(uint, uint) { v.updateActions() })
	v.view = gtk.NewColumnView(v.sel)
	v.view.AddCSSClass("quark-files")
	v.view.SetShowRowSeparators(false)
	v.view.ConnectActivate(func(pos uint) {
		if n, ok := v.nodeAt(pos); ok {
			v.activate(n)
		}
	})

	nameCol := gtk.NewColumnViewColumn(T("Name"), v.factory(func() gtk.Widgetter {
		box := gtk.NewBox(gtk.OrientationHorizontal, 10)
		box.Append(gtk.NewImage())
		l := gtk.NewLabel("")
		l.SetXAlign(0)
		l.SetEllipsize(pango.EllipsizeEnd)
		box.Append(l)
		shared := gtk.NewImageFromIconName("folder-publicshare-symbolic")
		shared.AddCSSClass("dim-label")
		shared.AddCSSClass("quark-emblem")
		shared.SetTooltipText(T("Shared"))
		box.Append(shared)
		return box
	}, func(child gtk.Widgetter, n drive.Node) {
		box := child.(*gtk.Box)
		img := box.FirstChild().(*gtk.Image)
		label := img.NextSibling().(*gtk.Label)
		shared := label.NextSibling().(*gtk.Image)
		setNodeIcon(img, n)
		label.SetText(n.Name)
		if n.NameOK {
			label.RemoveCSSClass("dim-label")
		} else {
			label.AddCSSClass("dim-label")
		}
		shared.SetVisible(n.IsShared)
	}))
	nameCol.SetExpand(true)
	nameCol.SetResizable(true)
	v.view.AppendColumn(nameCol)

	sizeCol := gtk.NewColumnViewColumn(T("Size"), v.factory(func() gtk.Widgetter {
		l := gtk.NewLabel("")
		l.SetXAlign(1)
		l.AddCSSClass("numeric")
		l.AddCSSClass("dim-label")
		return l
	}, func(child gtk.Widgetter, n drive.Node) {
		l := child.(*gtk.Label)
		switch {
		case n.IsFolder():
			l.SetText("—")
		case n.HasSize:
			l.SetText(formatSize(n.Size))
		default:
			l.SetText("—")
		}
	}))
	sizeCol.SetFixedWidth(110)
	sizeCol.SetResizable(true)
	v.view.AppendColumn(sizeCol)

	modCol := gtk.NewColumnViewColumn(T("Modified"), v.factory(func() gtk.Widgetter {
		l := gtk.NewLabel("")
		l.SetXAlign(0)
		l.AddCSSClass("dim-label")
		return l
	}, func(child gtk.Widgetter, n drive.Node) {
		t := n.Modified
		if !n.ContentModified.IsZero() {
			t = n.ContentModified
		}
		if v.section == drive.SectionTrash && !n.Trashed.IsZero() {
			t = n.Trashed
		}
		child.(*gtk.Label).SetText(app.FormatTime(t, time.Now()))
	}))
	if v.section == drive.SectionTrash {
		modCol.SetTitle(T("Trashed"))
	}
	modCol.SetFixedWidth(170)
	modCol.SetResizable(true)
	v.view.AppendColumn(modCol)

	// Context menu on right click (and long press on touch screens).
	v.popup = gtk.NewPopoverMenuFromModel(v.contextMenu())
	v.popup.SetParent(v.view)
	v.popup.SetHasArrow(false)
	v.popup.SetHAlign(gtk.AlignStart)
	click := gtk.NewGestureClick()
	click.SetButton(gdk.BUTTON_SECONDARY)
	click.ConnectPressed(func(_ int, x, y float64) { v.showPopup(x, y) })
	v.view.AddController(click)
	press := gtk.NewGestureLongPress()
	press.SetTouchOnly(true)
	press.ConnectPressed(func(x, y float64) { v.showPopup(x, y) })
	v.view.AddController(press)
}

// factory builds a column factory: setup creates the cell, bind fills it
// from the node whose UID is the row's string.
func (v *filesView) factory(setup func() gtk.Widgetter, bind func(gtk.Widgetter, drive.Node)) *gtk.ListItemFactory {
	f := gtk.NewSignalListItemFactory()
	f.ConnectSetup(func(obj *glib.Object) {
		if item := listItem(obj); item != nil {
			item.SetChild(setup())
		}
	})
	f.ConnectBind(func(obj *glib.Object) {
		item := listItem(obj)
		if item == nil || item.Item() == nil {
			return
		}
		so, ok := item.Item().Cast().(*gtk.StringObject)
		if !ok {
			return
		}
		if n, ok := v.nodes[so.String()]; ok {
			child := item.Child()
			// The UID identifies the row for right-click handling.
			gtk.BaseWidget(child).SetName(n.UID)
			bind(child, n)
		}
	})
	return &f.ListItemFactory
}

// listItem unwraps the object a list factory receives: a ColumnViewCell for
// column views (GTK 4.12+), a ListItem for list views.
func listItem(obj *glib.Object) *gtk.ListItem {
	switch it := obj.Cast().(type) {
	case *gtk.ColumnViewCell:
		return &it.ListItem
	case *gtk.ListItem:
		return it
	}
	return nil
}

func setNodeIcon(img *gtk.Image, n drive.Node) {
	switch {
	case n.IsFolder():
		img.SetFromIconName("folder-symbolic")
	case n.MediaType != "":
		ct := gio.ContentTypeFromMIMEType(n.MediaType)
		if icon := gio.ContentTypeGetSymbolicIcon(ct); icon != nil {
			img.SetFromGIcon(icon)
			return
		}
		img.SetFromIconName("text-x-generic-symbolic")
	default:
		img.SetFromIconName("text-x-generic-symbolic")
	}
}

func (v *filesView) nodeAt(pos uint) (drive.Node, bool) {
	if int(pos) >= len(v.order) {
		return drive.Node{}, false
	}
	n, ok := v.nodes[v.order[pos]]
	return n, ok
}

func (v *filesView) selected() []drive.Node {
	bs := v.sel.Selection()
	var out []drive.Node
	for i := uint64(0); i < bs.Size(); i++ {
		if n, ok := v.nodeAt(bs.Nth(uint(i))); ok {
			out = append(out, n)
		}
	}
	return out
}

func (v *filesView) showPopup(x, y float64) {
	// Select the row under the pointer when it is not part of the selection.
	if pick := v.view.Pick(x, y, gtk.PickDefault); pick != nil {
		if pos, ok := v.rowPosition(pick); ok && !v.sel.IsSelected(pos) {
			v.sel.SelectItem(pos, true)
		}
	}
	if len(v.selected()) == 0 {
		return
	}
	rect := gdk.NewRectangle(int(x), int(y), 1, 1)
	v.popup.SetPointingTo(&rect)
	v.popup.Popup()
}

// rowPosition finds the list position of the row containing w: every cell
// carries its node's UID as widget name.
func (v *filesView) rowPosition(w gtk.Widgetter) (uint, bool) {
	for cur := gtk.BaseWidget(w); cur != nil; {
		if pos, ok := v.index[cur.Name()]; ok {
			return pos, true
		}
		parent := cur.Parent()
		if parent == nil {
			break
		}
		cur = gtk.BaseWidget(parent)
	}
	return 0, false
}

// ----- state -----

func (v *filesView) update(st app.BrowserState) {
	// Title: the current folder; subtitle: where it is.
	if st.Location.IsRoot() {
		v.title.SetTitle(v.label)
		v.title.SetSubtitle("")
	} else {
		trail := st.Location.Trail
		v.title.SetTitle(trail[len(trail)-1].Name)
		parts := []string{v.label}
		for _, c := range trail[:len(trail)-1] {
			parts = append(parts, c.Name)
		}
		v.title.SetSubtitle(strings.Join(parts, " › "))
	}
	v.newFolder.SetVisible(st.Location.CanCreate() || st.Location.Section == drive.SectionMyFiles)
	v.upload.SetVisible(st.Location.CanCreate() || st.Location.Section == drive.SectionMyFiles)
	v.newFolder.SetSensitive(st.Location.CanCreate())
	v.upload.SetSensitive(st.Location.CanCreate())

	// Rows.
	uids := make([]string, len(st.Visible))
	nodes := make(map[string]drive.Node, len(st.Visible))
	for i, n := range st.Visible {
		uids[i] = n.UID
		nodes[n.UID] = n
	}
	same := len(uids) == len(v.order)
	for i := 0; same && i < len(uids); i++ {
		same = uids[i] == v.order[i] && nodes[uids[i]] == v.nodes[uids[i]]
	}
	if !same {
		v.nodes, v.order = nodes, uids
		v.index = make(map[string]uint, len(uids))
		for i, u := range uids {
			v.index[u] = uint(i)
		}
		v.model.Splice(0, v.model.NItems(), uids)
	}

	switch {
	case st.Err != nil:
		v.errPage.SetTitle(st.Err.Title)
		v.errPage.SetDescription(st.Err.Detail)
		v.stack.SetVisibleChildName("error")
	case st.Loading && len(st.Visible) == 0:
		v.stack.SetVisibleChildName("loading")
	case len(st.Visible) == 0:
		v.setEmpty(st)
		v.stack.SetVisibleChildName("empty")
	default:
		v.stack.SetVisibleChildName("list")
	}
	v.updateActions()
}

func (v *filesView) setEmpty(st app.BrowserState) {
	switch {
	case st.Filter != "" && st.Total > 0:
		v.emptyPage.SetIconName("system-search-symbolic")
		v.emptyPage.SetTitle(T("No Matches"))
		v.emptyPage.SetDescription(T("No item in this folder matches your filter."))
	case v.section == drive.SectionTrash:
		v.emptyPage.SetIconName("user-trash-symbolic")
		v.emptyPage.SetTitle(T("Trash Is Empty"))
		v.emptyPage.SetDescription("")
	case v.section == drive.SectionSharedWithMe && st.Location.IsRoot():
		v.emptyPage.SetIconName("folder-publicshare-symbolic")
		v.emptyPage.SetTitle(T("Nothing Shared With You"))
		v.emptyPage.SetDescription(T("Files and folders other people share with you appear here."))
	case v.section == drive.SectionSharedByMe:
		v.emptyPage.SetIconName("send-to-symbolic")
		v.emptyPage.SetTitle(T("Nothing Shared Yet"))
		v.emptyPage.SetDescription("")
	case v.section == drive.SectionDevices && st.Location.IsRoot():
		v.emptyPage.SetIconName("computer-symbolic")
		v.emptyPage.SetTitle(T("No Computers"))
		v.emptyPage.SetDescription(T("Computers synced with the Proton Drive desktop app appear here."))
	default:
		v.emptyPage.SetIconName("folder-symbolic")
		v.emptyPage.SetTitle(T("Folder Is Empty"))
		v.emptyPage.SetDescription(T("Drop files here or use the upload button."))
	}
}

// ----- actions -----

func (v *filesView) installActions() {
	group := gio.NewSimpleActionGroup()
	add := func(name string, fn func()) {
		a := gio.NewSimpleAction(name, nil)
		a.ConnectActivate(func(*glibv2.Variant) { fn() })
		group.AddAction(a)
		v.actions[name] = a
	}
	add("up", v.b.Up)
	add("refresh", v.b.Refresh)
	add("new-folder", v.promptNewFolder)
	add("upload-files", func() { v.chooseUpload(false) })
	add("upload-folder", func() { v.chooseUpload(true) })
	add("open", func() {
		if s := v.selected(); len(s) == 1 {
			v.activate(s[0])
		}
	})
	add("download", v.chooseDownload)
	add("rename", v.promptRename)
	add("trash", func() { v.w.a.Trash(v.b.State().Location, v.selected()) })
	add("restore", func() { v.w.a.Restore(v.allNodes(), v.selected()) })
	add("delete-forever", v.confirmDeleteForever)
	add("empty-trash", v.confirmEmptyTrash)
	add("copy-name", func() {
		var names []string
		for _, n := range v.selected() {
			names = append(names, n.Name)
		}
		v.w.win.Clipboard().SetText(strings.Join(names, "\n"))
	})
	add("search", func() { v.search.SetSearchMode(true) })

	st := v.w.a.Settings()
	v.sortAct = gio.NewSimpleActionStateful("sort", glibv2.NewVariantType("s"), glibv2.NewVariantString(st.SortColumn))
	v.sortAct.ConnectActivate(func(p *glibv2.Variant) {
		v.sortAct.SetState(p)
		s := v.b.State().Sort
		s.Column = p.String()
		v.b.SetSort(s)
	})
	group.AddAction(v.sortAct)
	v.descAct = gio.NewSimpleActionStateful("descending", nil, glibv2.NewVariantBoolean(st.SortDescending))
	v.descAct.ConnectActivate(func(*glibv2.Variant) {
		on := !v.descAct.State().Boolean()
		v.descAct.SetState(glibv2.NewVariantBoolean(on))
		s := v.b.State().Sort
		s.Descending = on
		v.b.SetSort(s)
	})
	group.AddAction(v.descAct)
	v.page.InsertActionGroup("files", group)

	keys := gtk.NewShortcutController()
	keys.SetScope(gtk.ShortcutScopeManaged)
	for trigger, action := range map[string]string{
		"F2": "files.rename", "Delete": "files.trash", "KP_Delete": "files.trash",
		"<Shift>Delete": "files.delete-forever", "F5": "files.refresh", "<Control>r": "files.refresh",
		"<Alt>Up": "files.up", "BackSpace": "files.up", "<Control><Shift>n": "files.new-folder",
		"<Control>f": "files.search", "<Control>u": "files.upload-files",
	} {
		keys.AddShortcut(gtk.NewShortcut(gtk.NewShortcutTriggerParseString(trigger), gtk.NewNamedAction(action)))
	}
	v.page.AddController(keys)
}

func (v *filesView) allNodes() []drive.Node {
	out := make([]drive.Node, 0, len(v.order))
	for _, uid := range v.order {
		out = append(out, v.nodes[uid])
	}
	return out
}

func (v *filesView) updateActions() {
	st := v.b.State()
	sel := v.selected()
	inTrash := v.section == drive.SectionTrash
	files := 0
	for _, n := range sel {
		if !n.IsFolder() {
			files++
		}
	}
	enable := func(name string, on bool) {
		if a := v.actions[name]; a != nil {
			a.SetEnabled(on)
		}
	}
	enable("up", !st.Location.IsRoot())
	v.upButton.SetVisible(!st.Location.IsRoot())
	enable("new-folder", st.Location.CanCreate())
	enable("upload-files", st.Location.CanCreate())
	enable("upload-folder", st.Location.CanCreate())
	enable("open", len(sel) == 1 && !inTrash)
	enable("download", len(sel) > 0 && !inTrash)
	sharedRoot := v.section == drive.SectionSharedWithMe && st.Location.IsRoot()
	enable("rename", len(sel) == 1 && !inTrash && !sharedRoot)
	// Shared roots and computers themselves cannot be trashed.
	topLevelForeign := st.Location.IsRoot() && (v.section == drive.SectionSharedWithMe || v.section == drive.SectionDevices)
	enable("trash", len(sel) > 0 && !inTrash && !topLevelForeign)
	enable("restore", len(sel) > 0 && inTrash)
	enable("delete-forever", len(sel) > 0 && inTrash)
	enable("empty-trash", inTrash && st.Total > 0)
	enable("copy-name", len(sel) > 0)
	if v.emptyBtn != nil {
		v.emptyBtn.SetSensitive(st.Total > 0)
	}
}

func (v *filesView) contextMenu() *gio.Menu {
	m := gio.NewMenu()
	if v.section == drive.SectionTrash {
		m.Append(T("Restore"), "files.restore")
		del := gio.NewMenu()
		del.Append(T("Delete Forever…"), "files.delete-forever")
		m.AppendSection("", del)
		return m
	}
	open := gio.NewMenu()
	open.Append(T("Open"), "files.open")
	open.Append(T("Download…"), "files.download")
	m.AppendSection("", open)
	edit := gio.NewMenu()
	edit.Append(T("Rename…"), "files.rename")
	edit.Append(T("Copy Name"), "files.copy-name")
	m.AppendSection("", edit)
	trash := gio.NewMenu()
	trash.Append(T("Move to Trash"), "files.trash")
	m.AppendSection("", trash)
	return m
}

// activate opens a folder or downloads and opens a file.
func (v *filesView) activate(n drive.Node) {
	if n.IsFolder() {
		v.b.Open(n)
		return
	}
	if v.section == drive.SectionTrash {
		return
	}
	v.openFile(n)
}

// openFile downloads a file into Quark's cache and opens it with the default app.
func (v *filesView) openFile(n drive.Node) {
	cache, err := os.UserCacheDir()
	if err != nil {
		v.w.toastText(T("No cache directory available"))
		return
	}
	dir := filepath.Join(cache, "quark", "open", strings.ReplaceAll(n.UID, "/", "_"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		v.w.toastText(Tf("Could not prepare the file: %s", err.Error()))
		return
	}
	target := filepath.Join(dir, n.Name)
	// A previous copy in our own cache is replaced: the remote may have changed.
	_ = os.Remove(target)
	id := v.w.q.Add(jobs.Transfer{
		Kind: jobs.Download, Name: n.Name, Local: dir, Remote: v.b.State().Location.NodePath(n),
		Size: n.Size, FileStrategy: "rename", FolderStrategy: "merge",
	})
	if id != "" {
		v.w.openOnFinish[id] = target
		v.w.toastText(Tf("Opening “%s”…", n.Name))
	}
}

// ----- dialogs and transfers -----

func (v *filesView) promptNewFolder() {
	loc := v.b.State().Location
	if !loc.CanCreate() {
		return
	}
	nameDialog(v.w, T("New Folder"), T("Create"), T("Untitled Folder"), func(name string) {
		v.w.a.CreateFolder(loc, name)
	})
}

func (v *filesView) promptRename() {
	sel := v.selected()
	if len(sel) != 1 {
		return
	}
	n, loc := sel[0], v.b.State().Location
	nameDialog(v.w, T("Rename"), T("Rename"), n.Name, func(name string) { v.w.a.Rename(loc, n, name) })
}

func (v *filesView) confirmDeleteForever() {
	sel := v.selected()
	if len(sel) == 0 {
		return
	}
	listing := v.allNodes()
	heading := Tf(N("Delete “%[2]s” Forever?", "Delete %[1]d Items Forever?", len(sel)), len(sel), sel[0].Name)
	confirm(v.w, heading, T("This cannot be undone."), T("Delete Forever"), func() { v.w.a.DeleteForever(listing, sel) })
}

func (v *filesView) confirmEmptyTrash() {
	n := v.b.State().Total
	if n == 0 {
		return
	}
	confirm(v.w, T("Empty Trash?"),
		Tf(N("%d item will be deleted forever. This cannot be undone.", "All %d items will be deleted forever. This cannot be undone.", n), n),
		T("Empty Trash"), v.w.a.EmptyTrash)
}

func (v *filesView) chooseUpload(folder bool) {
	loc := v.b.State().Location
	if !loc.CanCreate() {
		return
	}
	d := gtk.NewFileDialog()
	d.SetModal(true)
	if folder {
		d.SetTitle(T("Upload Folder"))
		d.SelectFolder(context.Background(), &v.w.win.Window, func(res gio.AsyncResulter) {
			f, err := d.SelectFolderFinish(res)
			if err == nil && f != nil && f.Path() != "" {
				v.enqueueUploads(loc, []string{f.Path()})
			}
		})
		return
	}
	d.SetTitle(T("Upload Files"))
	d.OpenMultiple(context.Background(), &v.w.win.Window, func(res gio.AsyncResulter) {
		list, err := d.OpenMultipleFinish(res)
		if err != nil || list == nil {
			return
		}
		var paths []string
		for i := uint(0); i < list.NItems(); i++ {
			if f, ok := list.Item(i).Cast().(*gio.File); ok && f.Path() != "" {
				paths = append(paths, f.Path())
			}
		}
		v.enqueueUploads(loc, paths)
	})
}

func (v *filesView) installDrop(target gtk.Widgetter) {
	drop := gtk.NewDropTarget(gdk.GTypeFileList, gdk.ActionCopy)
	drop.ConnectDrop(func(value *glib.Value, _, _ float64) bool {
		loc := v.b.State().Location
		if !loc.CanCreate() {
			v.w.toastText(T("Files cannot be uploaded here"))
			return false
		}
		fl, ok := value.GoValue().(*gdk.FileList)
		if !ok {
			return false
		}
		var paths []string
		for _, f := range fl.Files() {
			if p := f.Path(); p != "" {
				paths = append(paths, p)
			}
		}
		v.enqueueUploads(loc, paths)
		return len(paths) > 0
	})
	gtk.BaseWidget(target).AddController(drop)
}

func (v *filesView) enqueueUploads(loc app.Location, paths []string) {
	st := v.w.a.Settings()
	label := v.title.Title()
	n := 0
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			v.w.toastText(Tf("Cannot read “%s”", filepath.Base(p)))
			continue
		}
		size := int64(0)
		if info.Mode().IsRegular() {
			size = info.Size()
		}
		if v.w.q.Add(jobs.Transfer{
			Kind: jobs.Upload, Name: filepath.Base(p), Local: p, Remote: loc.ListPath(), RemoteLabel: label,
			IsFolder: info.IsDir(), Size: size, FileStrategy: st.UploadFileStrategy, FolderStrategy: st.UploadFolderStrategy,
			SkipThumbnails: st.SkipThumbnails,
		}) != "" {
			n++
		}
	}
	if n > 0 {
		v.w.onToast(app.Toast{
			Text:        Tf(N("Uploading %d item", "Uploading %d items", n), n),
			ActionLabel: T("Show"), Action: func() { v.w.showSection("transfers") },
		})
	}
}

func (v *filesView) chooseDownload() {
	sel := v.selected()
	if len(sel) == 0 {
		return
	}
	loc := v.b.State().Location
	d := gtk.NewFileDialog()
	d.SetModal(true)
	d.SetTitle(T("Download To"))
	dir := v.w.a.Settings().DownloadDir
	if dir == "" {
		dir = downloadsDir()
	}
	if dir != "" {
		d.SetInitialFolder(gio.NewFileForPath(dir))
	}
	d.SelectFolder(context.Background(), &v.w.win.Window, func(res gio.AsyncResulter) {
		f, err := d.SelectFolderFinish(res)
		if err != nil || f == nil || f.Path() == "" {
			return
		}
		st := v.w.a.Settings()
		n := 0
		for _, node := range sel {
			if v.w.q.Add(jobs.Transfer{
				Kind: jobs.Download, Name: node.Name, Local: f.Path(), Remote: loc.NodePath(node),
				IsFolder: node.IsFolder(), Size: node.Size,
				FileStrategy: st.DownloadFileStrategy, FolderStrategy: st.DownloadFolderStrategy,
			}) != "" {
				n++
			}
		}
		v.w.onToast(app.Toast{
			Text:        Tf(N("Downloading %d item", "Downloading %d items", n), n),
			ActionLabel: T("Show"), Action: func() { v.w.showSection("transfers") },
		})
	})
}
