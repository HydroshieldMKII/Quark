package app

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

func TestLocationPaths(t *testing.T) {
	file := node("F1", "a/b.txt", drive.TypeFile)
	tests := []struct {
		name     string
		loc      Location
		list     string
		nodePath string
		create   bool
	}{
		{"my-files root", Root(drive.SectionMyFiles), "/my-files", "/my-files/F1", true},
		{"my-files folder", Location{drive.SectionMyFiles, []Crumb{{"D1", "Docs"}, {"D2", "Sub"}}}, "/my-files/D2", "/my-files/F1", true},
		{"devices root", Root(drive.SectionDevices), "/devices", "/my-files/F1", false},
		{"device folder", Location{drive.SectionDevices, []Crumb{{"R1", "laptop"}}}, "/my-files/R1", "/my-files/F1", true},
		{"shared root", Root(drive.SectionSharedWithMe), "/shared-with-me", `/shared-with-me/a\/b.txt`, false},
		{"shared folder", Location{drive.SectionSharedWithMe, []Crumb{{"S1", "Team/x"}}}, `/shared-with-me/Team\/x`, `/shared-with-me/Team\/x/F1`, true},
		{"shared deep", Location{drive.SectionSharedWithMe, []Crumb{{"S1", "Team"}, {"S2", "Q3"}}}, "/shared-with-me/Team/S2", "/shared-with-me/Team/F1", true},
		{"shared by me", Root(drive.SectionSharedByMe), "/shared-by-me", "/my-files/F1", false},
		{"trash", Root(drive.SectionTrash), "/trash", `/trash/a\/b.txt`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.loc.ListPath(); got != tt.list {
				t.Errorf("ListPath = %q, want %q", got, tt.list)
			}
			if got := tt.loc.NodePath(file); got != tt.nodePath {
				t.Errorf("NodePath = %q, want %q", got, tt.nodePath)
			}
			if got := tt.loc.CanCreate(); got != tt.create {
				t.Errorf("CanCreate = %v", got)
			}
		})
	}
	l := Location{drive.SectionMyFiles, []Crumb{{"D1", "Docs"}}}
	if c := l.Child(node("D2", "Sub", drive.TypeFolder)); len(c.Trail) != 2 || len(l.Trail) != 1 {
		t.Fatal("Child aliased the trail")
	}
	if !l.Parent().IsRoot() || !Root(drive.SectionTrash).Parent().IsRoot() || l.Equal(l.Parent()) {
		t.Fatal("Parent")
	}
}

func TestSortNodes(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(uid, name string, folder bool, size int64, mod time.Time) drive.Node {
		n := node(uid, name, drive.TypeFile)
		if folder {
			n.Type = drive.TypeFolder
		} else {
			n.Size, n.HasSize = size, true
		}
		n.Modified = mod
		return n
	}
	nodes := []drive.Node{
		mk("1", "beta.txt", false, 30, t0.Add(3*time.Hour)),
		mk("2", "Alpha", true, 0, t0),
		mk("3", "alpha.txt", false, 10, t0.Add(time.Hour)),
		mk("4", "zeta", true, 0, t0.Add(5*time.Hour)),
		mk("5", "Beta.txt", false, 20, t0.Add(2*time.Hour)),
	}
	names := func(ns []drive.Node) string {
		var out []string
		for _, n := range ns {
			out = append(out, n.Name)
		}
		return strings.Join(out, ",")
	}
	SortNodes(nodes, SortSpec{Column: "name"})
	if got := names(nodes); got != "Alpha,zeta,alpha.txt,Beta.txt,beta.txt" {
		t.Errorf("by name: %s", got)
	}
	SortNodes(nodes, SortSpec{Column: "size", Descending: true})
	if got := names(nodes); got != "zeta,Alpha,beta.txt,Beta.txt,alpha.txt" {
		t.Errorf("by size desc: %s", got)
	}
	nodes[0].ContentModified = t0.Add(-time.Hour) // zeta's own mtime is older than Alpha
	SortNodes(nodes, SortSpec{Column: "modified"})
	if got := names(nodes); got != "zeta,Alpha,alpha.txt,Beta.txt,beta.txt" {
		t.Errorf("by modified: %s", got)
	}
}

func TestBrowserNavigateFilterSort(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.list = func(_ context.Context, path string) ([]drive.Node, error) {
			if path != "/my-files" {
				t.Errorf("listed %q", path)
			}
			small := node("f1", "Notes.txt", drive.TypeFile)
			small.Size, small.HasSize = 5, true
			big := node("f2", "movie.mkv", drive.TypeFile)
			big.Size, big.HasSize = 5000, true
			return []drive.Node{big, node("d1", "Projects", drive.TypeFolder), small}, nil
		}
	})
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	var states []BrowserState
	b.OnChange(func(s BrowserState) { states = append(states, s) })
	b.Navigate(Root(drive.SectionMyFiles))
	if !states[0].Loading {
		t.Fatal("no loading state first")
	}
	h.until("listed", func() bool { return b.State().Total == 3 })
	st := b.State()
	if st.Loading || st.Err != nil || st.Visible[0].Name != "Projects" || st.Visible[1].Name != "movie.mkv" {
		t.Fatalf("state = %+v", st)
	}
	b.SetFilter("  TXT ")
	if v := b.State().Visible; len(v) != 1 || v[0].Name != "Notes.txt" || b.State().Total != 3 {
		t.Fatalf("filtered = %+v", v)
	}
	b.SetFilter("")
	b.SetSort(SortSpec{Column: "size", Descending: true})
	if v := b.State().Visible; v[1].Name != "movie.mkv" {
		t.Fatalf("sorted = %+v", v)
	}
	if s := h.app.Settings(); s.SortColumn != "size" || !s.SortDescending || len(h.saved) == 0 {
		t.Fatal("sort not persisted")
	}
	b.SetSort(SortSpec{Column: "bogus"})
	if b.State().Sort.Column != "name" {
		t.Fatalf("invalid sort column kept: %+v", b.State().Sort)
	}
}

func TestBrowserIgnoresStaleResults(t *testing.T) {
	h := newHarness(t)
	h.ready()
	releaseA := make(chan struct{})
	h.be.set(func(f *fakeBackend) {
		f.list = func(_ context.Context, path string) ([]drive.Node, error) {
			if path == "/my-files/A" {
				<-releaseA
				return []drive.Node{node("a", "from-A", drive.TypeFile)}, nil
			}
			return []drive.Node{node("b", "from-B", drive.TypeFile)}, nil
		}
	})
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	locA := Location{drive.SectionMyFiles, []Crumb{{"A", "A"}}}
	locB := Location{drive.SectionMyFiles, []Crumb{{"B", "B"}}}
	b.Navigate(locA)
	h.until("A started", func() bool { return slices.Contains(h.be.Calls(), "list /my-files/A") })
	b.Navigate(locB)
	h.until("B listed", func() bool { return b.State().Total == 1 })
	close(releaseA)
	h.until("A finished", func() bool { return h.be.InFlight("read") == 0 })
	time.Sleep(30 * time.Millisecond)
	h.loop.Drain()
	if st := b.State(); !st.Location.Equal(locB) || st.Visible[0].Name != "from-B" {
		t.Fatalf("stale listing shown: %+v", st)
	}
}

func TestBrowserCache(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.list = func(context.Context, string) ([]drive.Node, error) {
			return []drive.Node{node("x", "x", drive.TypeFile)}, nil
		}
	})
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	count := func() int {
		n := 0
		for _, c := range h.be.Calls() {
			if c == "list /my-files" {
				n++
			}
		}
		return n
	}
	b.Navigate(Root(drive.SectionMyFiles))
	h.until("first list", func() bool { return b.State().Total == 1 })
	b.Navigate(Root(drive.SectionMyFiles))
	if b.State().Loading || count() != 1 {
		t.Fatalf("fresh cache not used: %d lists", count())
	}
	h.now = h.now.Add(31 * time.Second)
	b.Navigate(Root(drive.SectionMyFiles))
	h.until("expired", func() bool { return count() == 2 && !b.State().Loading })
	b.Refresh()
	h.until("forced", func() bool { return count() == 3 && !b.State().Loading })

	// Cache eviction keeps memory bounded.
	for i := range maxCacheEntries + 10 {
		h.app.cache.put("/p/"+string(rune(i)), nil)
	}
	if len(h.app.cache.entries) > maxCacheEntries {
		t.Fatalf("cache has %d entries", len(h.app.cache.entries))
	}
}

func TestBrowserErrorsAndBreadcrumbs(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.list = func(_ context.Context, path string) ([]drive.Node, error) {
			if path == "/my-files/D2" {
				return nil, &pdcli.Error{Kind: pdcli.KindNotFound}
			}
			return []drive.Node{node("D1", "Docs", drive.TypeFolder), node("f", "file", drive.TypeFile)}, nil
		}
	})
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	b.Navigate(Root(drive.SectionMyFiles))
	h.until("root", func() bool { return b.State().Total == 2 })
	if b.Open(node("f", "file", drive.TypeFile)) {
		t.Fatal("opened a file as a folder")
	}
	if !b.Open(b.State().Visible[0]) {
		t.Fatal("could not open folder")
	}
	h.until("docs", func() bool { return len(b.State().Location.Trail) == 1 && !b.State().Loading })
	b.Navigate(b.State().Location.Child(node("D2", "Gone", drive.TypeFolder)))
	h.until("error", func() bool { return b.State().Err != nil })
	if e := b.State().Err; e.Action != ActionRefresh || b.State().Total != 0 {
		t.Fatalf("err = %+v", e)
	}
	b.GoToDepth(1)
	if st := b.State(); len(st.Location.Trail) != 1 || st.Err != nil {
		t.Fatalf("breadcrumb: %+v", st)
	}
	b.GoToDepth(5) // out of range: ignored
	b.Up()
	if !b.State().Location.IsRoot() {
		t.Fatal("Up")
	}
	b.Up() // at root: no-op
	b.Close()
	if _, ok := h.app.browsers[b]; ok {
		t.Fatal("Close did not detach")
	}
}

func TestBrowserDevicesAndSpecialSections(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.devices = func() ([]drive.Device, error) {
			return []drive.Device{{UID: "dev", Name: "laptop", RootFolderUID: "R1"}}, nil
		}
	})
	b := h.app.NewBrowser(Root(drive.SectionDevices))
	b.Navigate(Root(drive.SectionDevices))
	h.until("devices", func() bool { return b.State().Total == 1 })
	dev := b.State().Visible[0]
	if dev.UID != "R1" || !dev.IsFolder() {
		t.Fatalf("device node = %+v", dev)
	}
	b.Open(dev)
	h.until("device folder", func() bool { return slices.Contains(h.be.Calls(), "list /my-files/R1") })

	// Trash folders cannot be opened.
	h.be.set(func(f *fakeBackend) {
		f.list = func(context.Context, string) ([]drive.Node, error) {
			return []drive.Node{node("T1", "Old", drive.TypeFolder)}, nil
		}
	})
	tb := h.app.NewBrowser(Root(drive.SectionTrash))
	tb.Navigate(Root(drive.SectionTrash))
	h.until("trash", func() bool { return tb.State().Total == 1 })
	if tb.Open(tb.State().Visible[0]) || len(h.toasts) == 0 {
		t.Fatal("opened a trashed folder")
	}

	// Shared roots with duplicate names are refused.
	h.be.set(func(f *fakeBackend) {
		f.list = func(context.Context, string) ([]drive.Node, error) {
			return []drive.Node{node("S1", "Team", drive.TypeFolder), node("S2", "Team", drive.TypeFolder), node("S3", "Solo", drive.TypeFolder)}, nil
		}
	})
	sb := h.app.NewBrowser(Root(drive.SectionSharedWithMe))
	sb.Navigate(Root(drive.SectionSharedWithMe))
	h.until("shared", func() bool { return sb.State().Total == 3 })
	if sb.Open(node("S1", "Team", drive.TypeFolder)) || sb.State().Err == nil || sb.State().Err.Kind != "ambiguous" {
		t.Fatalf("ambiguous shared root opened: %+v", sb.State())
	}
	if !sb.Open(node("S3", "Solo", drive.TypeFolder)) {
		t.Fatal("unique shared root refused")
	}
	h.until("solo", func() bool { return slices.Contains(h.be.Calls(), "list /shared-with-me/Solo") })
}

func TestBrowserWithoutBackend(t *testing.T) {
	h := newHarness(t)
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	b.Navigate(Root(drive.SectionMyFiles))
	if e := b.State().Err; e == nil || e.Action != ActionChooseCLI {
		t.Fatalf("err = %+v", e)
	}
}
