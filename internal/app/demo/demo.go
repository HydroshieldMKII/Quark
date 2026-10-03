// Package demo is an in-memory stand-in for the Proton Drive CLI with sample
// data. `quark --demo` and the UI tests use it; it never runs the real CLI
// and never touches an account.
package demo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

// Backend implements app.Backend and jobs.Runner over an in-memory tree.
type Backend struct {
	mu       sync.Mutex
	nodes    map[string]*drive.Node // by UID
	children map[string][]string    // parent UID -> child UIDs
	trash    []string
	seq      int
	// Latency is added to every call (default 150 ms) to show loading states.
	Latency  time.Duration
	LoggedIn bool
	now      time.Time
}

const rootUID = "demo~root"

// New returns a backend with sample content, already signed in.
func New(now time.Time) *Backend {
	b := &Backend{
		nodes: map[string]*drive.Node{}, children: map[string][]string{},
		Latency: 150 * time.Millisecond, LoggedIn: true, now: now,
	}
	b.nodes[rootUID] = &drive.Node{UID: rootUID, Name: "My files", NameOK: true, Type: drive.TypeFolder}
	docs := b.add(rootUID, "Documents", drive.TypeFolder, "", 0, 26*time.Hour)
	photos := b.add(rootUID, "Photos", drive.TypeFolder, "", 0, 72*time.Hour)
	b.add(rootUID, "Projects", drive.TypeFolder, "", 0, 400*time.Hour)
	shared := b.add(rootUID, "Shared plans", drive.TypeFolder, "", 0, 5*time.Hour)
	b.nodes[shared].IsShared, b.nodes[shared].IsSharedByURL = true, true
	b.add(rootUID, "Budget 2026.ods", drive.TypeFile, "application/vnd.oasis.opendocument.spreadsheet", 48_211, 3*time.Hour)
	b.add(rootUID, "Notes.md", drive.TypeFile, "text/markdown", 2_048, 20*time.Minute)
	b.add(rootUID, "Vacation.mp4", drive.TypeFile, "video/mp4", 734_003_200, 900*time.Hour)
	b.add(docs, "Tax return 2025.pdf", drive.TypeFile, "application/pdf", 1_204_224, 30*24*time.Hour)
	b.add(docs, "Lease agreement.pdf", drive.TypeFile, "application/pdf", 512_000, 200*24*time.Hour)
	b.add(docs, "CV.odt", drive.TypeFile, "application/vnd.oasis.opendocument.text", 33_000, 10*24*time.Hour)
	b.add(photos, "Beach.jpg", drive.TypeFile, "image/jpeg", 3_400_000, 90*24*time.Hour)
	b.add(photos, "Mountains.png", drive.TypeFile, "image/png", 8_900_000, 95*24*time.Hour)
	old := b.add(rootUID, "Old draft.txt", drive.TypeFile, "text/plain", 900, 50*24*time.Hour)
	b.trashNode(old)
	return b
}

func (b *Backend) add(parent, name string, typ drive.NodeType, mime string, size int64, age time.Duration) string {
	b.seq++
	uid := fmt.Sprintf("demo~%04d", b.seq)
	n := &drive.Node{
		UID: uid, ParentUID: parent, Name: name, NameOK: true, Type: typ, MediaType: mime,
		Created: b.now.Add(-age), Modified: b.now.Add(-age), Author: "you@proton.me", AuthorVerified: true,
		DirectRole: drive.RoleAdmin,
	}
	if typ == drive.TypeFile {
		n.Size, n.HasSize = size, true
	}
	b.nodes[uid] = n
	b.children[parent] = append(b.children[parent], uid)
	return uid
}

func (b *Backend) trashNode(uid string) {
	n := b.nodes[uid]
	b.children[n.ParentUID] = slices.DeleteFunc(b.children[n.ParentUID], func(s string) bool { return s == uid })
	n.Trashed = b.now
	b.trash = append(b.trash, uid)
}

func (b *Backend) wait(ctx context.Context) error {
	select {
	case <-time.After(b.Latency):
		return nil
	case <-ctx.Done():
		return &pdcli.Error{Kind: pdcli.KindCancelled, Err: ctx.Err()}
	}
}

func (b *Backend) authed() error {
	if !b.LoggedIn {
		return &pdcli.Error{Kind: pdcli.KindNotLoggedIn, Message: "You need to login first"}
	}
	return nil
}

func notFound(what string) error {
	return &pdcli.Error{Kind: pdcli.KindNotFound, Message: "Node not found: " + what}
}

// resolve maps a CLI path to a node UID.
func (b *Backend) resolve(path string) (string, error) {
	segs := drive.SplitSegments(path)
	if len(segs) < 2 || segs[0] != "" {
		return "", notFound(path)
	}
	switch segs[1] {
	case string(drive.SectionMyFiles):
		if len(segs) == 2 {
			return rootUID, nil
		}
		cur := rootUID
		for _, s := range segs[2:] {
			if _, ok := b.nodes[s]; ok {
				cur = s
				continue
			}
			found := ""
			for _, c := range b.children[cur] {
				if b.nodes[c].Name == s {
					found = c
					break
				}
			}
			if found == "" {
				return "", notFound(s)
			}
			cur = found
		}
		return cur, nil
	case string(drive.SectionTrash):
		if len(segs) == 3 {
			for _, uid := range b.trash {
				if b.nodes[uid].Name == segs[2] {
					return uid, nil
				}
			}
		}
		return "", &pdcli.Error{Kind: pdcli.KindNotFound, Message: "Trashed node not found"}
	}
	return "", notFound(path)
}

func (b *Backend) snapshot(uids []string) []drive.Node {
	out := make([]drive.Node, 0, len(uids))
	for _, u := range uids {
		out = append(out, *b.nodes[u])
	}
	return out
}

// Version implements app.Backend.
func (b *Backend) Version(context.Context) (pdcli.Version, error) {
	return pdcli.Version{Major: 0, Minor: 8, Patch: 0, Raw: "cli-drive@0.8.0+demo", SDK: "demo"}, nil
}

// Roots implements app.Backend.
func (b *Backend) Roots(ctx context.Context) ([]string, error) {
	if err := b.wait(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.authed(); err != nil {
		return nil, err
	}
	return []string{"/my-files", "/devices", "/shared-by-me", "/shared-with-me", "/trash"}, nil
}

// List implements app.Backend.
func (b *Backend) List(ctx context.Context, path string) ([]drive.Node, error) {
	if err := b.wait(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.authed(); err != nil {
		return nil, err
	}
	switch path {
	case drive.SectionTrash.Root():
		return b.snapshot(b.trash), nil
	case drive.SectionSharedWithMe.Root():
		return nil, nil
	case drive.SectionSharedByMe.Root():
		var out []drive.Node
		for _, n := range b.nodes {
			if n.IsShared && n.Trashed.IsZero() {
				out = append(out, *n)
			}
		}
		return out, nil
	}
	uid, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	return b.snapshot(b.children[uid]), nil
}

// Devices implements app.Backend.
func (b *Backend) Devices(ctx context.Context) ([]drive.Device, error) {
	if err := b.wait(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}

// Info implements app.Backend.
func (b *Backend) Info(ctx context.Context, path string) (drive.Node, error) {
	if err := b.wait(ctx); err != nil {
		return drive.Node{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	uid, err := b.resolve(path)
	if err != nil {
		return drive.Node{}, err
	}
	return *b.nodes[uid], nil
}

func (b *Backend) nameTaken(parent, name string) bool {
	for _, c := range b.children[parent] {
		if b.nodes[c].Name == name {
			return true
		}
	}
	return false
}

// CreateFolder implements app.Backend.
func (b *Backend) CreateFolder(ctx context.Context, parent, name string) (drive.Node, error) {
	if err := b.wait(ctx); err != nil {
		return drive.Node{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p, err := b.resolve(parent)
	if err != nil {
		return drive.Node{}, err
	}
	if b.nameTaken(p, name) {
		return drive.Node{}, &pdcli.Error{Kind: pdcli.KindConflict, Message: "A file or folder with that name already exists"}
	}
	uid := b.add(p, name, drive.TypeFolder, "", 0, 0)
	return *b.nodes[uid], nil
}

// Rename implements app.Backend.
func (b *Backend) Rename(ctx context.Context, path, newName string) (drive.Node, error) {
	if err := b.wait(ctx); err != nil {
		return drive.Node{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	uid, err := b.resolve(path)
	if err != nil {
		return drive.Node{}, err
	}
	n := b.nodes[uid]
	if b.nameTaken(n.ParentUID, newName) {
		return drive.Node{}, &pdcli.Error{Kind: pdcli.KindConflict, Message: "A file or folder with that name already exists"}
	}
	n.Name, n.Modified = newName, b.now
	return *n, nil
}

func (b *Backend) eachPath(ctx context.Context, paths []string, fn func(uid string) bool) ([]drive.ItemResult, error) {
	if err := b.wait(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var res []drive.ItemResult
	failed := 0
	for _, p := range paths {
		uid, err := b.resolve(p)
		ok := err == nil && fn(uid)
		if !ok {
			failed++
		}
		res = append(res, drive.ItemResult{UID: uid, OK: ok})
	}
	if failed > 0 {
		return res, &pdcli.Error{Kind: pdcli.KindPartial, Message: fmt.Sprintf("%d of %d item(s) failed", failed, len(paths))}
	}
	return res, nil
}

// Copy implements app.Backend.
func (b *Backend) Copy(ctx context.Context, sources []string, target, name string) ([]drive.ItemResult, error) {
	b.mu.Lock()
	t, err := b.resolve(target)
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return b.eachPath(ctx, sources, func(uid string) bool {
		n := *b.nodes[uid]
		if name != "" {
			n.Name = name
		}
		if b.nameTaken(t, n.Name) {
			return false
		}
		b.add(t, n.Name, n.Type, n.MediaType, n.Size, 0)
		return true
	})
}

// Move implements app.Backend.
func (b *Backend) Move(ctx context.Context, sources []string, target string) ([]drive.ItemResult, error) {
	b.mu.Lock()
	t, err := b.resolve(target)
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return b.eachPath(ctx, sources, func(uid string) bool {
		n := b.nodes[uid]
		if b.nameTaken(t, n.Name) || uid == t {
			return false
		}
		b.children[n.ParentUID] = slices.DeleteFunc(b.children[n.ParentUID], func(s string) bool { return s == uid })
		n.ParentUID = t
		b.children[t] = append(b.children[t], uid)
		return true
	})
}

// Trash implements app.Backend.
func (b *Backend) Trash(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	return b.eachPath(ctx, paths, func(uid string) bool {
		if !b.nodes[uid].Trashed.IsZero() || uid == rootUID {
			return false
		}
		b.trashNode(uid)
		return true
	})
}

// Restore implements app.Backend.
func (b *Backend) Restore(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	return b.eachPath(ctx, paths, func(uid string) bool {
		n := b.nodes[uid]
		b.trash = slices.DeleteFunc(b.trash, func(s string) bool { return s == uid })
		n.Trashed = time.Time{}
		if _, ok := b.nodes[n.ParentUID]; !ok {
			n.ParentUID = rootUID
		}
		b.children[n.ParentUID] = append(b.children[n.ParentUID], uid)
		return true
	})
}

// DeleteForever implements app.Backend.
func (b *Backend) DeleteForever(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	return b.eachPath(ctx, paths, func(uid string) bool {
		b.trash = slices.DeleteFunc(b.trash, func(s string) bool { return s == uid })
		delete(b.nodes, uid)
		return true
	})
}

// EmptyTrash implements app.Backend.
func (b *Backend) EmptyTrash(ctx context.Context) error {
	if err := b.wait(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, uid := range b.trash {
		delete(b.nodes, uid)
	}
	b.trash = nil
	return nil
}

// Login implements app.Backend: it "opens" a URL and signs in after a moment.
func (b *Backend) Login(ctx context.Context, onURL func(string)) error {
	onURL("https://account.proton.me/desktop/login?demo=1")
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return &pdcli.Error{Kind: pdcli.KindCancelled}
	}
	b.mu.Lock()
	b.LoggedIn = true
	b.mu.Unlock()
	return nil
}

// Logout implements app.Backend.
func (b *Backend) Logout(context.Context) error {
	b.mu.Lock()
	b.LoggedIn = false
	b.mu.Unlock()
	return nil
}

// Upload implements jobs.Runner with a short simulated transfer.
func (b *Backend) Upload(ctx context.Context, s pdcli.UploadSpec) (drive.TransferSummary, error) {
	if err := sleep(ctx, 3*time.Second); err != nil {
		return drive.TransferSummary{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	parent, err := b.resolve(s.Parent)
	if err != nil {
		return drive.TransferSummary{}, err
	}
	for _, p := range s.LocalPaths {
		name := p[strings.LastIndexByte(p, '/')+1:]
		if !b.nameTaken(parent, name) {
			b.add(parent, name, drive.TypeFile, "application/octet-stream", 1024, 0)
		}
	}
	return drive.TransferSummary{TransferredItems: len(s.LocalPaths), TransferredBytes: 1024}, nil
}

// Download implements jobs.Runner. Demo downloads always fail politely: the
// demo has no file contents.
func (b *Backend) Download(ctx context.Context, _ pdcli.DownloadSpec) (drive.TransferSummary, error) {
	if err := sleep(ctx, 2*time.Second); err != nil {
		return drive.TransferSummary{}, err
	}
	return drive.TransferSummary{}, &pdcli.Error{Kind: pdcli.KindUnknown, Message: "Demo mode has no file contents to download"}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return &pdcli.Error{Kind: pdcli.KindCancelled, Err: errors.Join(ctx.Err())}
	}
}
