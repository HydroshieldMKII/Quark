package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

func TestValidateName(t *testing.T) {
	ok := []string{"a", "Report 2026.pdf", "été", "-dash", strings.Repeat("x", 255)}
	bad := []string{"", "   ", ".", "..", "a/b", "a\nb", "a\x00b", "\x7f", string([]byte{0xff}), strings.Repeat("x", 256)}
	for _, n := range ok {
		if ue := ValidateName(n); ue != nil {
			t.Errorf("ValidateName(%q) = %v", n, ue)
		}
	}
	for _, n := range bad {
		if ue := ValidateName(n); ue == nil {
			t.Errorf("ValidateName(%q) accepted", n)
		}
	}
}

// listed prepares a browser showing nodes at loc.
func listed(t *testing.T, h *harness, loc Location, nodes ...drive.Node) *Browser {
	t.Helper()
	h.be.set(func(f *fakeBackend) {
		f.list = func(context.Context, string) ([]drive.Node, error) { return nodes, nil }
	})
	b := h.app.NewBrowser(loc)
	b.Navigate(loc)
	h.until("listed", func() bool { return !b.State().Loading && b.State().Err == nil })
	return b
}

func writes(h *harness) []string {
	var out []string
	for _, c := range h.be.Calls() {
		if !strings.HasPrefix(c, "list") && !strings.HasPrefix(c, "sharing ") && c != "version" && c != "roots" && c != "devices" && c != "invitations" {
			out = append(out, c)
		}
	}
	return out
}

func TestCreateFolder(t *testing.T) {
	h := newHarness(t)
	h.ready()
	root := Root(drive.SectionMyFiles)
	b := listed(t, h, root, node("d", "Existing", drive.TypeFolder))

	h.app.CreateFolder(root, "a/b")
	h.app.CreateFolder(root, "Existing")
	h.app.CreateFolder(Root(drive.SectionTrash), "x")
	h.loop.Drain()
	if w := writes(h); len(w) != 0 {
		t.Fatalf("invalid creates reached the CLI: %q", w)
	}
	if len(h.toasts) != 3 || h.toasts[1].Err.Kind != "conflict" {
		t.Fatalf("toasts = %+v", h.toasts)
	}

	before := len(h.be.Calls())
	h.app.CreateFolder(root, "New")
	h.until("created", func() bool { return len(h.toasts) == 4 })
	if w := writes(h); !slices.Equal(w, []string{"create /my-files New"}) {
		t.Fatalf("writes = %q", w)
	}
	if h.lastToast().Err != nil {
		t.Fatalf("toast = %+v", h.lastToast())
	}
	h.until("refreshed", func() bool {
		return slices.Contains(h.be.Calls()[before:], "list /my-files") && !b.State().Loading
	})
}

func TestCreateFolderFailureAndAuthLoss(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.create = func(string, string) (drive.Node, error) {
			return drive.Node{}, &pdcli.Error{Kind: pdcli.KindNotLoggedIn}
		}
	})
	h.app.CreateFolder(Root(drive.SectionMyFiles), "x")
	h.until("toast", func() bool { return len(h.toasts) == 1 })
	if h.app.Session().Phase != PhaseLoggedOut || h.lastToast().Err.Action != ActionLogin {
		t.Fatalf("session=%s toast=%+v", h.app.Session().Phase, h.lastToast())
	}
}

func TestRename(t *testing.T) {
	h := newHarness(t)
	h.ready()
	loc := Location{drive.SectionMyFiles, []Crumb{{"D1", "Docs"}}}
	f := node("F1", "a.txt", drive.TypeFile)
	listed(t, h, loc, f, node("F2", "b.txt", drive.TypeFile))
	h.app.Rename(loc, f, "a.txt") // unchanged: nothing
	h.app.Rename(loc, f, "b.txt") // taken
	h.app.Rename(Root(drive.SectionTrash), f, "c.txt")
	h.loop.Drain()
	if len(writes(h)) != 0 || len(h.toasts) != 2 {
		t.Fatalf("writes=%q toasts=%+v", writes(h), h.toasts)
	}
	h.app.Rename(loc, f, "c.txt")
	h.until("renamed", func() bool { return len(writes(h)) == 1 && h.be.InFlight("write") == 0 })
	if w := writes(h)[0]; w != "rename /my-files/F1 c.txt" {
		t.Fatalf("write = %q", w)
	}
}

func TestTrashAndUndo(t *testing.T) {
	h := newHarness(t)
	h.ready()
	loc := Root(drive.SectionMyFiles)
	a, b := node("A", "a.txt", drive.TypeFile), node("B", "dup.txt", drive.TypeFile)
	listed(t, h, loc, a, b)
	h.app.Trash(loc, []drive.Node{a, b})
	h.until("trashed", func() bool { return len(h.toasts) == 1 })
	if w := writes(h); !slices.Equal(w, []string{"trash /my-files/A,/my-files/B"}) {
		t.Fatalf("writes = %q", w)
	}
	tt := h.lastToast()
	if tt.Text != "Moved 2 items to the trash" || tt.Action == nil || tt.ActionLabel != "Undo" {
		t.Fatalf("toast = %+v", tt)
	}
	// In the trash, "dup.txt" now exists twice (another item had that name).
	h.be.set(func(f *fakeBackend) {
		f.list = func(_ context.Context, path string) ([]drive.Node, error) {
			if path == "/trash" {
				return []drive.Node{node("A", "a.txt", drive.TypeFile), node("B", "dup.txt", drive.TypeFile), node("X", "dup.txt", drive.TypeFile)}, nil
			}
			return nil, nil
		}
	})
	tt.Action()
	h.until("undo", func() bool { return len(h.toasts) == 2 })
	if w := writes(h); !slices.Equal(w[1:], []string{"restore /trash/a.txt"}) {
		t.Fatalf("undo writes = %q (must skip the ambiguous name)", w)
	}
	if !strings.Contains(h.lastToast().Text, "could not be restored automatically") {
		t.Fatalf("toast = %+v", h.lastToast())
	}
	h.app.Trash(Root(drive.SectionTrash), []drive.Node{a}) // no-op
	h.app.Trash(loc, nil)
	h.loop.Drain()
	if len(writes(h)) != 2 {
		t.Fatal("no-op trash calls reached the CLI")
	}
}

func TestTrashPartialFailure(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.be.set(func(f *fakeBackend) {
		f.batch = func(context.Context, string, []string) ([]drive.ItemResult, error) {
			return []drive.ItemResult{{UID: "A", OK: true}, {UID: "B", OK: false}}, &pdcli.Error{Kind: pdcli.KindPartial}
		}
	})
	h.app.Trash(Root(drive.SectionMyFiles), []drive.Node{node("A", "a", drive.TypeFile), node("B", "b", drive.TypeFile)})
	h.until("toast", func() bool { return len(h.toasts) == 1 })
	if tt := h.lastToast(); tt.Text != "1 done, 1 failed" || tt.Action != nil {
		t.Fatalf("toast = %+v", tt)
	}
}

func TestRestoreAndDeleteRefuseAmbiguousNames(t *testing.T) {
	h := newHarness(t)
	h.ready()
	listing := []drive.Node{node("1", "a.txt", drive.TypeFile), node("2", "a.txt", drive.TypeFile), node("3", "b.txt", drive.TypeFile)}
	h.app.Restore(listing, listing[:1])
	h.app.DeleteForever(listing, []drive.Node{listing[1], listing[2]})
	h.loop.Drain()
	if len(writes(h)) != 0 {
		t.Fatalf("ambiguous names reached the CLI: %q", writes(h))
	}
	if len(h.toasts) != 2 || h.toasts[0].Err.Kind != "ambiguous" || !strings.Contains(h.toasts[1].Err.Detail, "a.txt") {
		t.Fatalf("toasts = %+v", h.toasts)
	}
	h.app.Restore(listing, listing[2:])
	h.until("restored", func() bool { return len(h.toasts) == 3 })
	h.app.DeleteForever(listing, listing[2:])
	h.until("deleted", func() bool { return len(h.toasts) == 4 })
	if w := writes(h); !slices.Equal(w, []string{"restore /trash/b.txt", "delete /trash/b.txt"}) {
		t.Fatalf("writes = %q", w)
	}
	if h.toasts[3].Text != "Deleted “b.txt” forever" {
		t.Fatalf("toast = %q", h.toasts[3].Text)
	}
	h.app.Restore(listing, nil)
	h.app.DeleteForever(listing, nil)
	h.loop.Drain()
}

func TestEmptyTrash(t *testing.T) {
	h := newHarness(t)
	h.ready()
	h.app.EmptyTrash()
	h.until("emptied", func() bool { return len(h.toasts) == 1 })
	if w := writes(h); !slices.Equal(w, []string{"empty-trash"}) || h.lastToast().Err != nil {
		t.Fatalf("writes=%q toast=%+v", w, h.lastToast())
	}
	h.be.set(func(f *fakeBackend) { f.empty = func() error { return &pdcli.Error{Kind: pdcli.KindNetwork} } })
	h.app.EmptyTrash()
	h.until("failed", func() bool { return len(h.toasts) == 2 })
	if h.lastToast().Err == nil {
		t.Fatal("failure not reported")
	}
}

func TestMoveAndCopy(t *testing.T) {
	h := newHarness(t)
	h.ready()
	from := Location{drive.SectionMyFiles, []Crumb{{"D1", "Docs"}}}
	to := Location{drive.SectionMyFiles, []Crumb{{"D2", "Archive"}}}
	folder := node("D1", "Docs", drive.TypeFolder)
	file := node("F1", "a.txt", drive.TypeFile)

	h.app.Move(Root(drive.SectionMyFiles), []drive.Node{folder}, from.Child(node("D9", "inner", drive.TypeFolder)))
	h.app.Move(from, []drive.Node{file}, Root(drive.SectionTrash))
	h.app.Move(Root(drive.SectionTrash), []drive.Node{file}, to)
	h.app.Copy(from, []drive.Node{file}, to, "bad/name")
	h.app.Move(from, nil, to)
	h.loop.Drain()
	if len(writes(h)) != 0 || len(h.toasts) != 4 {
		t.Fatalf("writes=%q toasts=%d", writes(h), len(h.toasts))
	}
	h.app.Move(from, []drive.Node{file}, to)
	h.until("moved", func() bool { return len(h.toasts) == 5 })
	h.app.Copy(from, []drive.Node{file}, to, "copy.txt")
	h.until("copied", func() bool { return len(h.toasts) == 6 })
	if w := writes(h); !slices.Equal(w, []string{"move /my-files/F1 -> /my-files/D2", "copy /my-files/F1 -> /my-files/D2 name=copy.txt"}) {
		t.Fatalf("writes = %q", w)
	}
	if h.toasts[4].Text != "Moved “a.txt”" || h.toasts[5].Text != "Copied “a.txt”" {
		t.Fatalf("toasts = %q / %q", h.toasts[4].Text, h.toasts[5].Text)
	}
}

func TestOperationsWithoutBackend(t *testing.T) {
	h := newHarness(t)
	h.app.CreateFolder(Root(drive.SectionMyFiles), "x")
	h.loop.Drain()
	if len(h.toasts) != 1 || h.toasts[0].Err.Kind != "binary-missing" {
		t.Fatalf("toasts = %+v", h.toasts)
	}
}
