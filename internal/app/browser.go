package app

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

// Crumb is one folder in a location's trail.
type Crumb struct {
	UID  string
	Name string
}

// Location is a folder the user is looking at: a section and the chain of
// folders below its root.
type Location struct {
	Section drive.Section
	Trail   []Crumb
}

// Root returns the root location of a section.
func Root(s drive.Section) Location { return Location{Section: s} }

// IsRoot reports whether l is a section root.
func (l Location) IsRoot() bool { return len(l.Trail) == 0 }

// Child returns the location of a sub-folder.
func (l Location) Child(n drive.Node) Location {
	t := append(slices.Clone(l.Trail), Crumb{UID: n.UID, Name: n.Name})
	return Location{Section: l.Section, Trail: t}
}

// Parent returns the parent location (the root's parent is itself).
func (l Location) Parent() Location {
	if l.IsRoot() {
		return l
	}
	return Location{Section: l.Section, Trail: slices.Clone(l.Trail[:len(l.Trail)-1])}
}

// Equal compares two locations.
func (l Location) Equal(o Location) bool {
	return l.Section == o.Section && slices.Equal(l.Trail, o.Trail)
}

// ListPath is the CLI path used to list l.
func (l Location) ListPath() string {
	if l.IsRoot() {
		return l.Section.Root()
	}
	last := l.Trail[len(l.Trail)-1]
	if l.Section == drive.SectionSharedWithMe {
		// The first segment below /shared-with-me is matched by name; deeper
		// segments may be UIDs.
		p := l.Section.Root() + "/" + drive.EscapeSegment(l.Trail[0].Name)
		if len(l.Trail) > 1 {
			p += "/" + last.UID
		}
		return p
	}
	return drive.ByUID(last.UID)
}

// NodePath is the CLI path used to act on node n listed at l.
func (l Location) NodePath(n drive.Node) string {
	switch {
	case l.Section == drive.SectionTrash:
		return drive.TrashPath(n.Name)
	case l.Section == drive.SectionSharedWithMe && l.IsRoot():
		return l.Section.Root() + "/" + drive.EscapeSegment(n.Name)
	case l.Section == drive.SectionSharedWithMe:
		return l.Section.Root() + "/" + drive.EscapeSegment(l.Trail[0].Name) + "/" + n.UID
	default:
		return drive.ByUID(n.UID)
	}
}

// CanCreate reports whether new items can be created or uploaded at l.
func (l Location) CanCreate() bool {
	switch l.Section {
	case drive.SectionMyFiles:
		return true
	case drive.SectionDevices, drive.SectionSharedWithMe:
		return !l.IsRoot()
	default:
		return false
	}
}

// SortSpec orders the visible list. Folders always come first.
type SortSpec struct {
	Column     string // "name", "size" or "modified"
	Descending bool
}

// BrowserState is a snapshot for the view.
type BrowserState struct {
	Location Location
	Loading  bool
	Err      *UserError
	// Visible is the filtered and sorted listing.
	Visible []drive.Node
	// Total is the number of items before filtering.
	Total  int
	Filter string
	Sort   SortSpec
}

// Browser is the view-model of one file list.
type Browser struct {
	a         *App
	loc       Location
	nodes     []drive.Node
	loading   bool
	err       *UserError
	filter    string
	sort      SortSpec
	gen       uint64
	cancel    context.CancelFunc
	listeners []func(BrowserState)
}

// NewBrowser creates a browser at loc. Call Close when the view goes away.
func (a *App) NewBrowser(loc Location) *Browser {
	b := &Browser{a: a, loc: loc, sort: SortSpec{Column: a.settings.SortColumn, Descending: a.settings.SortDescending}}
	a.browsers[b] = struct{}{}
	return b
}

// OnChange registers a listener called after every state change.
func (b *Browser) OnChange(fn func(BrowserState)) { b.listeners = append(b.listeners, fn) }

// Close stops the browser's work and detaches it.
func (b *Browser) Close() {
	if b.cancel != nil {
		b.cancel()
	}
	delete(b.a.browsers, b)
	b.listeners = nil
}

// State returns the current snapshot.
func (b *Browser) State() BrowserState {
	vis := b.visible()
	return BrowserState{Location: b.loc, Loading: b.loading, Err: b.err, Visible: vis, Total: len(b.nodes), Filter: b.filter, Sort: b.sort}
}

func (b *Browser) notify() {
	st := b.State()
	for _, fn := range b.listeners {
		fn(st)
	}
}

func (b *Browser) reset() {
	if b.cancel != nil {
		b.cancel()
	}
	b.gen++
	b.nodes, b.err, b.loading = nil, nil, false
	b.notify()
}

// Navigate shows loc, from the cache when fresh, otherwise by listing it.
func (b *Browser) Navigate(loc Location) { b.load(loc, false) }

// Refresh reloads the current location, bypassing the cache.
func (b *Browser) Refresh() { b.load(b.loc, true) }

// Up goes to the parent folder.
func (b *Browser) Up() {
	if !b.loc.IsRoot() {
		b.Navigate(b.loc.Parent())
	}
}

// GoToDepth navigates to a breadcrumb: 0 is the section root.
func (b *Browser) GoToDepth(depth int) {
	if depth < 0 || depth > len(b.loc.Trail) {
		return
	}
	b.Navigate(Location{Section: b.loc.Section, Trail: slices.Clone(b.loc.Trail[:depth])})
}

// Open navigates into a folder. It reports false (and sets an error) for
// items that cannot be opened as folders.
func (b *Browser) Open(n drive.Node) bool {
	if !n.IsFolder() {
		return false
	}
	switch {
	case b.loc.Section == drive.SectionTrash:
		b.a.toast(Toast{Text: T("Restore the folder to browse its contents")})
		return false
	case b.loc.Section == drive.SectionSharedWithMe && b.loc.IsRoot() && countName(b.nodes, n.Name) > 1:
		b.err = &UserError{
			Title:  T("Cannot open this folder"),
			Detail: T("Several items shared with you have this name, and the Proton Drive CLI cannot tell them apart."),
			Kind:   "ambiguous",
		}
		b.notify()
		return false
	}
	b.Navigate(b.loc.Child(n))
	return true
}

func (b *Browser) load(loc Location, force bool) {
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	b.gen++
	gen := b.gen
	changed := !loc.Equal(b.loc)
	b.loc = loc
	b.err = nil
	path := loc.ListPath()
	if !force {
		if nodes, ok := b.a.cache.get(path); ok {
			b.nodes, b.loading = nodes, false
			b.notify()
			return
		}
	}
	if changed {
		b.nodes = nil
	}
	b.loading = true
	b.notify()
	if b.a.backend == nil || b.a.closed {
		b.loading = false
		b.err = Describe(&pdcli.Error{Kind: pdcli.KindBinaryMissing})
		b.notify()
		return
	}
	ctx, cancel := context.WithCancel(b.a.rootCtx)
	b.cancel = cancel
	be := b.a.backend
	var nodes []drive.Node
	b.a.spawn(ctx, "list", b.a.reads, func(ctx context.Context) error {
		var err error
		if loc.Section == drive.SectionDevices && loc.IsRoot() {
			var devs []drive.Device
			devs, err = be.Devices(ctx)
			nodes = devicesAsNodes(devs)
		} else {
			nodes, err = be.List(ctx, path)
		}
		return err
	}, func(err error) {
		if gen != b.gen {
			return // superseded by a newer navigation
		}
		cancel()
		b.cancel = nil
		b.loading = false
		if b.a.handleAuthLoss(err) {
			b.notify()
			return
		}
		if err != nil {
			b.err = Describe(err)
			b.notify()
			return
		}
		b.nodes = nodes
		b.a.cache.put(path, nodes)
		b.notify()
	})
}

func devicesAsNodes(devs []drive.Device) []drive.Node {
	out := make([]drive.Node, 0, len(devs))
	for _, d := range devs {
		out = append(out, drive.Node{
			UID: d.RootFolderUID, Name: d.Name, NameOK: d.Name != "", Type: drive.TypeFolder,
			Created: d.Created, Modified: d.LastSync,
		})
	}
	return out
}

// SetFilter filters the visible list by a case-insensitive substring.
func (b *Browser) SetFilter(f string) {
	if f == b.filter {
		return
	}
	b.filter = f
	b.notify()
}

// SetSort changes the sort order and remembers it in the settings.
func (b *Browser) SetSort(s SortSpec) {
	if s == b.sort {
		return
	}
	b.sort = s
	st := b.a.settings
	st.SortColumn, st.SortDescending = s.Column, s.Descending
	if st.Normalize() {
		b.sort.Column = st.SortColumn
	}
	b.a.settings = st
	b.a.saveSettings()
	b.notify()
}

func (b *Browser) visible() []drive.Node {
	q := strings.ToLower(strings.TrimSpace(b.filter))
	out := make([]drive.Node, 0, len(b.nodes))
	for _, n := range b.nodes {
		if q == "" || strings.Contains(strings.ToLower(n.Name), q) {
			out = append(out, n)
		}
	}
	SortNodes(out, b.sort)
	return out
}

// SortNodes sorts in place: folders first, then by the column; ties are
// broken by name and UID so the order is stable and deterministic.
func SortNodes(nodes []drive.Node, s SortSpec) {
	slices.SortStableFunc(nodes, func(x, y drive.Node) int {
		if fx, fy := x.IsFolder(), y.IsFolder(); fx != fy {
			if fx {
				return -1
			}
			return 1
		}
		c := 0
		switch s.Column {
		case "size":
			c = cmp.Compare(sizeKey(x), sizeKey(y))
		case "modified":
			c = modTime(x).Compare(modTime(y))
		}
		if c == 0 {
			c = compareNames(x.Name, y.Name)
		}
		if c == 0 {
			c = strings.Compare(x.UID, y.UID)
		}
		if s.Descending {
			return -c
		}
		return c
	})
}

func sizeKey(n drive.Node) int64 {
	if !n.HasSize {
		return -1
	}
	return n.Size
}

// modTime is the file's own modification time when known, else the server's.
func modTime(n drive.Node) time.Time {
	if !n.ContentModified.IsZero() {
		return n.ContentModified
	}
	return n.Modified
}

func compareNames(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func countName(nodes []drive.Node, name string) int {
	n := 0
	for _, x := range nodes {
		if x.Name == name {
			n++
		}
	}
	return n
}
