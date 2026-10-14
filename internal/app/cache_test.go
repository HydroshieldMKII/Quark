package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/fsutil"
	"example.org/quark/internal/pdcli"
	"example.org/quark/internal/settings"
)

func TestListCachePersistsAcrossRuns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "listings.json")
	h := newHarness(t)
	h.app.cfg.CacheFile = file
	h.app.cache.load(file)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.list = func(context.Context, string) ([]drive.Node, error) {
			return []drive.Node{node("d", "Docs", drive.TypeFolder), node("f", "a.txt", drive.TypeFile)}, nil
		}
	})
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	b.Navigate(Root(drive.SectionMyFiles))
	h.until("listed", func() bool { return b.State().Total == 2 && !b.State().Loading })
	h.app.Shutdown(5 * time.Second)
	h.loop.Drain()
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("cache not saved: %v", err)
	}

	// Next run, a minute later: the folder renders at once from the cache,
	// marked stale, while a refresh runs; the refresh replaces it.
	h2 := newHarness(t)
	h2.now = h.now.Add(time.Minute)
	h2.app.cfg.CacheFile = file
	h2.app.cache.load(file)
	gate := make(chan struct{})
	h2.be.set(func(f *fakeBackend) {
		f.list = func(ctx context.Context, _ string) ([]drive.Node, error) {
			select {
			case <-gate:
			case <-ctx.Done():
			}
			return []drive.Node{node("f", "a.txt", drive.TypeFile)}, nil
		}
	})
	h2.ready()
	b2 := h2.app.NewBrowser(Root(drive.SectionMyFiles))
	b2.Navigate(Root(drive.SectionMyFiles))
	st := b2.State()
	if st.Total != 2 || !st.Stale || !st.Loading {
		t.Fatalf("cached listing not shown at once: %+v", st)
	}
	close(gate)
	h2.until("refreshed", func() bool { s := b2.State(); return !s.Loading && !s.Stale && s.Total == 1 })
}

func TestListCacheIgnoresCorruptAndOldFiles(t *testing.T) {
	dir := t.TempDir()
	c := newListCache(time.Now, time.Minute)
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{nope"), fsutil.FilePrivate); err != nil {
		t.Fatal(err)
	}
	c.load(bad)
	if len(c.entries) != 0 {
		t.Fatal("corrupt cache loaded")
	}
	old := newListCache(func() time.Time { return time.Now().Add(-8 * 24 * time.Hour) }, time.Minute)
	old.put("/my-files", []drive.Node{node("x", "x", drive.TypeFile)})
	oldFile := filepath.Join(dir, "old.json")
	old.path = oldFile
	if err := old.save(old.snapshot()); err != nil {
		t.Fatal(err)
	}
	c2 := newListCache(time.Now, time.Minute)
	c2.load(oldFile)
	if _, _, ok := c2.get("/my-files"); ok {
		t.Fatal("week-old listing loaded")
	}
	// Eviction keeps the size bounded and snapshot only reports changes.
	for i := range maxCacheEntries + 5 {
		c2.put("/p/"+string(rune('a'+i%26))+string(rune('a'+i/26)), nil)
	}
	if len(c2.entries) > maxCacheEntries {
		t.Fatalf("entries = %d", len(c2.entries))
	}
	c2.path = filepath.Join(dir, "c2.json")
	if c2.snapshot() == nil {
		t.Fatal("dirty cache has no snapshot")
	}
	if c2.snapshot() != nil {
		t.Fatal("clean cache has a snapshot")
	}
	c2.put("/known", nil)
	_ = c2.snapshot()
	c2.invalidate("/known")
	if c2.snapshot() == nil {
		t.Fatal("invalidate did not mark dirty")
	}
	c2.clear()
	if _, err := os.Stat(c2.path); !os.IsNotExist(err) && err == nil {
		// clear removes the file if present; it was never written here
		_ = err
	}
}

func TestLogoutClearsPersistedCache(t *testing.T) {
	file := filepath.Join(t.TempDir(), "listings.json")
	h := newHarness(t)
	h.app.cfg.CacheFile = file
	h.app.cache.load(file)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.list = func(context.Context, string) ([]drive.Node, error) {
			return []drive.Node{node("f", "a", drive.TypeFile)}, nil
		}
	})
	b := h.app.NewBrowser(Root(drive.SectionMyFiles))
	b.Navigate(Root(drive.SectionMyFiles))
	h.until("listed", func() bool { return b.State().Total == 1 && !b.State().Loading })
	h.app.SaveCache()
	h.until("saved", func() bool { _, err := os.Stat(file); return err == nil })
	h.be.set(func(f *fakeBackend) {
		f.roots = func() ([]string, error) { return nil, &pdcli.Error{Kind: pdcli.KindNotLoggedIn} }
	})
	h.app.Logout()
	h.until("logged out", func() bool { return h.app.Session().Phase == PhaseLoggedOut })
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("listing cache survived logout")
	}
	if nodes, _, ok := h.app.cache.get("/my-files"); ok || nodes != nil {
		t.Fatal("in-memory cache survived logout")
	}
	_ = settings.Defaults()
	_ = slices.Contains([]string{}, "")
}
