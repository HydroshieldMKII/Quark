package app

import (
	"context"
	"strings"
	"unicode/utf8"

	"example.org/quark/internal/drive"
)

// Toast is a transient notification, optionally with one action.
type Toast struct {
	Text        string
	ActionLabel string
	Action      func()
	// Err carries details for failures (the UI may offer "Details").
	Err *UserError
}

// OnToast registers a listener for notifications.
func (a *App) OnToast(fn func(Toast)) { a.onToast = append(a.onToast, fn) }

func (a *App) toast(t Toast) {
	for _, fn := range a.onToast {
		fn(t)
	}
}

// maxNameBytes is a conservative limit for node names.
const maxNameBytes = 255

// ValidateName checks a new file or folder name before anything is sent.
func ValidateName(name string) *UserError {
	bad := func(detail string) *UserError {
		return &UserError{Title: T("Invalid name"), Detail: detail, Kind: "invalid-name"}
	}
	switch {
	case strings.TrimSpace(name) == "":
		return bad(T("The name cannot be empty."))
	case name == "." || name == "..":
		return bad(T("This name is reserved."))
	case strings.Contains(name, "/"):
		return bad(T("Names cannot contain “/”."))
	case strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return bad(T("Names cannot contain control characters."))
	case !utf8.ValidString(name):
		return bad(T("The name is not valid text."))
	case len(name) > maxNameBytes:
		return bad(T("The name is too long."))
	}
	return nil
}

// mutate runs a write operation, then invalidates and refreshes the
// affected listings. done receives the (described) error on the UI thread.
func (a *App) mutate(name string, affected []string, work func(context.Context) error, done func(*UserError)) {
	if a.closed {
		return
	}
	if a.backend == nil {
		if done != nil {
			done(&UserError{Title: T("Not connected"), Detail: T("The Proton Drive CLI is not available."), Kind: "binary-missing"})
		}
		return
	}
	a.spawn(a.rootCtx, name, a.writes, work, func(err error) {
		a.handleAuthLoss(err)
		a.refreshPaths(affected)
		if done != nil {
			done(Describe(err))
		}
	})
}

func (a *App) refreshPaths(paths []string) {
	a.cache.invalidate(paths...)
	for b := range a.browsers {
		lp := b.loc.ListPath()
		for _, p := range paths {
			if lp == p {
				b.Refresh()
				break
			}
		}
	}
}

func (a *App) refreshAll() {
	a.cache.clear()
	for b := range a.browsers {
		b.Refresh()
	}
}

func (a *App) failToast(ue *UserError) {
	if ue != nil {
		a.toast(Toast{Text: ue.Title, Err: ue})
	}
}

// nameTaken reports whether name exists among the known children of loc.
func (a *App) nameTaken(loc Location, name string) bool {
	nodes, ok := a.cache.get(loc.ListPath())
	return ok && countName(nodes, name) > 0
}

// CreateFolder creates a folder named name at loc.
func (a *App) CreateFolder(loc Location, name string) {
	if ue := ValidateName(name); ue != nil {
		a.failToast(ue)
		return
	}
	if !loc.CanCreate() {
		a.failToast(&UserError{Title: T("Cannot create a folder here"), Kind: "not-allowed"})
		return
	}
	if a.nameTaken(loc, name) {
		a.failToast(&UserError{Title: T("Name already in use"), Detail: T("An item with this name already exists here."), Kind: "conflict"})
		return
	}
	be, parent := a.backend, loc.ListPath()
	a.mutate("create-folder", []string{parent}, func(ctx context.Context) error {
		_, err := be.CreateFolder(ctx, parent, name)
		return err
	}, func(ue *UserError) {
		if ue != nil {
			a.failToast(ue)
			return
		}
		a.toast(Toast{Text: Tf("Folder “%s” created", name)})
	})
}

// Rename renames node n listed at loc.
func (a *App) Rename(loc Location, n drive.Node, newName string) {
	if newName == n.Name {
		return
	}
	if ue := ValidateName(newName); ue != nil {
		a.failToast(ue)
		return
	}
	if loc.Section == drive.SectionTrash {
		a.failToast(&UserError{Title: T("Items in the trash cannot be renamed"), Kind: "not-allowed"})
		return
	}
	if a.nameTaken(loc, newName) {
		a.failToast(&UserError{Title: T("Name already in use"), Detail: T("An item with this name already exists here."), Kind: "conflict"})
		return
	}
	be, path := a.backend, loc.NodePath(n)
	a.mutate("rename", []string{loc.ListPath()}, func(ctx context.Context) error {
		_, err := be.Rename(ctx, path, newName)
		return err
	}, a.failToast)
}

func countOK(res []drive.ItemResult) (ok, failed int) {
	for _, r := range res {
		if r.OK {
			ok++
		} else {
			failed++
		}
	}
	return ok, failed
}

func (a *App) batchToast(res []drive.ItemResult, ue *UserError, done string, undo func()) {
	ok, failed := countOK(res)
	switch {
	case ue != nil && ok == 0:
		a.failToast(ue)
	case failed > 0:
		a.toast(Toast{Text: Tf(T("%d done, %d failed"), ok, failed), Err: ue})
	default:
		t := Toast{Text: done}
		if undo != nil {
			t.ActionLabel, t.Action = T("Undo"), undo
		}
		a.toast(t)
	}
}

// Trash moves nodes listed at loc to the trash, offering Undo.
func (a *App) Trash(loc Location, nodes []drive.Node) {
	if len(nodes) == 0 || loc.Section == drive.SectionTrash {
		return
	}
	paths := make([]string, len(nodes))
	names := make([]string, len(nodes))
	for i, n := range nodes {
		paths[i], names[i] = loc.NodePath(n), n.Name
	}
	be := a.backend
	var res []drive.ItemResult
	a.mutate("trash", []string{loc.ListPath(), drive.SectionTrash.Root()}, func(ctx context.Context) error {
		var err error
		res, err = be.Trash(ctx, paths...)
		return err
	}, func(ue *UserError) {
		msg := Tf(N("Moved “%[2]s” to the trash", "Moved %[1]d items to the trash", len(nodes)), len(nodes), nodes[0].Name)
		a.batchToast(res, ue, msg, func() { a.undoTrash(names) })
	})
}

// undoTrash restores items by name, but only names that are unique in the
// trash: the CLI addresses trashed items by name (first match), and
// restoring someone else's same-named item would be wrong.
func (a *App) undoTrash(names []string) {
	be := a.backend
	var (
		res       []drive.ItemResult
		ambiguous int
	)
	a.mutate("undo-trash", []string{drive.SectionTrash.Root()}, func(ctx context.Context) error {
		trash, err := be.List(ctx, drive.SectionTrash.Root())
		if err != nil {
			return err
		}
		var paths []string
		for _, n := range names {
			if countName(trash, n) == 1 {
				paths = append(paths, drive.TrashPath(n))
			} else {
				ambiguous++
			}
		}
		if len(paths) == 0 {
			return nil
		}
		res, err = be.Restore(ctx, paths...)
		return err
	}, func(ue *UserError) {
		a.refreshAll()
		if ambiguous > 0 && ue == nil {
			a.toast(Toast{Text: Tf(N("%d item could not be restored automatically; restore it from the trash",
				"%d items could not be restored automatically; restore them from the trash", ambiguous), ambiguous)})
			return
		}
		a.batchToast(res, ue, T("Restored"), nil)
	})
}

// ambiguousInTrash returns the selected names that are not unique in the trash listing.
func ambiguousInTrash(listing, selected []drive.Node) []string {
	var out []string
	for _, n := range selected {
		if countName(listing, n.Name) != 1 {
			out = append(out, n.Name)
		}
	}
	return out
}

func ambiguityError(names []string) *UserError {
	return &UserError{
		Title: T("Some items cannot be handled by name"),
		Detail: Tf(T("Several items in the trash are named “%s”. The Proton Drive CLI cannot tell them apart; use the Proton Drive website for these."),
			strings.Join(names, "”, “")),
		Kind: "ambiguous",
	}
}

// Restore restores selected items. listing is the full trash listing, used to
// refuse names that are not unique.
func (a *App) Restore(listing, selected []drive.Node) {
	if len(selected) == 0 {
		return
	}
	if amb := ambiguousInTrash(listing, selected); len(amb) > 0 {
		a.failToast(ambiguityError(amb))
		return
	}
	paths := make([]string, len(selected))
	for i, n := range selected {
		paths[i] = drive.TrashPath(n.Name)
	}
	be := a.backend
	var res []drive.ItemResult
	a.mutate("restore", []string{drive.SectionTrash.Root()}, func(ctx context.Context) error {
		var err error
		res, err = be.Restore(ctx, paths...)
		return err
	}, func(ue *UserError) {
		a.refreshAll() // restored items reappear in folders we cannot name
		a.batchToast(res, ue, Tf(N("Restored “%[2]s”", "Restored %[1]d items", len(selected)), len(selected), selected[0].Name), nil)
	})
}

// DeleteForever permanently deletes selected trash items (the UI confirms first).
func (a *App) DeleteForever(listing, selected []drive.Node) {
	if len(selected) == 0 {
		return
	}
	if amb := ambiguousInTrash(listing, selected); len(amb) > 0 {
		a.failToast(ambiguityError(amb))
		return
	}
	paths := make([]string, len(selected))
	for i, n := range selected {
		paths[i] = drive.TrashPath(n.Name)
	}
	be := a.backend
	var res []drive.ItemResult
	a.mutate("delete", []string{drive.SectionTrash.Root()}, func(ctx context.Context) error {
		var err error
		res, err = be.DeleteForever(ctx, paths...)
		return err
	}, func(ue *UserError) {
		a.batchToast(res, ue, Tf(N("Deleted “%[2]s” forever", "Deleted %[1]d items forever", len(selected)), len(selected), selected[0].Name), nil)
	})
}

// EmptyTrash permanently deletes everything in the trash (the UI confirms first).
func (a *App) EmptyTrash() {
	be := a.backend
	a.mutate("empty-trash", []string{drive.SectionTrash.Root()}, be.EmptyTrash, func(ue *UserError) {
		if ue != nil {
			a.failToast(ue)
			return
		}
		a.toast(Toast{Text: T("Emptying the trash; it may take a moment to finish")})
	})
}

// Move moves nodes listed at from into the folder at to.
func (a *App) Move(from Location, nodes []drive.Node, to Location) {
	a.moveOrCopy("move", from, nodes, to, "")
}

// Copy copies nodes listed at from into the folder at to. name renames a single copy.
func (a *App) Copy(from Location, nodes []drive.Node, to Location, name string) {
	a.moveOrCopy("copy", from, nodes, to, name)
}

func (a *App) moveOrCopy(op string, from Location, nodes []drive.Node, to Location, name string) {
	if len(nodes) == 0 {
		return
	}
	if !to.CanCreate() {
		a.failToast(&UserError{Title: T("Cannot put items here"), Kind: "not-allowed"})
		return
	}
	if from.Section == drive.SectionTrash {
		a.failToast(&UserError{Title: T("Restore items before moving them"), Kind: "not-allowed"})
		return
	}
	if name != "" {
		if ue := ValidateName(name); ue != nil {
			a.failToast(ue)
			return
		}
	}
	srcs := make([]string, len(nodes))
	for i, n := range nodes {
		srcs[i] = from.NodePath(n)
		for _, c := range to.Trail {
			if c.UID == n.UID {
				a.failToast(&UserError{Title: T("A folder cannot be moved into itself"), Kind: "not-allowed"})
				return
			}
		}
	}
	target := to.ListPath()
	be := a.backend
	var res []drive.ItemResult
	affected := []string{to.ListPath()}
	if op == "move" {
		affected = append(affected, from.ListPath())
	}
	a.mutate(op, affected, func(ctx context.Context) error {
		var err error
		if op == "move" {
			res, err = be.Move(ctx, srcs, target)
		} else {
			res, err = be.Copy(ctx, srcs, target, name)
		}
		return err
	}, func(ue *UserError) {
		var msg string
		if op == "move" {
			msg = Tf(N("Moved “%[2]s”", "Moved %[1]d items", len(nodes)), len(nodes), nodes[0].Name)
		} else {
			msg = Tf(N("Copied “%[2]s”", "Copied %[1]d items", len(nodes)), len(nodes), nodes[0].Name)
		}
		a.batchToast(res, ue, msg, nil)
	})
}
