// Package memdrive is an in-memory Proton Drive with real file contents,
// for tests of components that transfer data (the FUSE mount, the jobs
// queue). It implements the fusefs.Backend subset of pdcli.Client and
// enforces the same rules the CLI does: UID or name addressing, trash by
// name, conflict strategies, local glob escaping.
package memdrive

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/fsutil"
	"example.org/quark/internal/pdcli"
)

// Drive is the in-memory store. All methods are safe for concurrent use.
type Drive struct {
	mu       sync.Mutex
	nodes    map[string]*entry
	children map[string][]string
	trash    []string
	seq      int
	now      func() time.Time
	latency  time.Duration
	cmdLat   map[string]time.Duration
	fail     error
	calls    map[string]int
}

// SetLatency adds a delay to every call.
func (d *Drive) SetLatency(l time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.latency = l
}

// SetCommandLatency adds a delay to one command only ("list", "download", ...).
func (d *Drive) SetCommandLatency(cmd string, l time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cmdLat == nil {
		d.cmdLat = map[string]time.Duration{}
	}
	d.cmdLat[cmd] = l
}

// SetFail makes every call return err (nil restores normal behaviour).
func (d *Drive) SetFail(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fail = err
}

// Calls returns how many times the named command ran.
func (d *Drive) Calls(cmd string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[cmd]
}

type entry struct {
	node    drive.Node
	content []byte
	rev     int
}

const rootUID = "mem~root"

// New returns an empty drive whose root folder is /my-files.
func New(now func() time.Time) *Drive {
	d := &Drive{nodes: map[string]*entry{}, children: map[string][]string{}, now: now, calls: map[string]int{}}
	d.nodes[rootUID] = &entry{node: drive.Node{UID: rootUID, Name: "My files", NameOK: true, Type: drive.TypeFolder}}
	return d
}

func (d *Drive) begin(ctx context.Context, cmd string) error {
	d.mu.Lock()
	latency := d.latency + d.cmdLat[cmd]
	d.mu.Unlock()
	if latency > 0 {
		select {
		case <-time.After(latency):
		case <-ctx.Done():
			return &pdcli.Error{Kind: pdcli.KindCancelled, Err: ctx.Err()}
		}
	}
	d.mu.Lock()
	d.calls[cmd]++
	fail := d.fail
	d.mu.Unlock()
	return fail
}

func (d *Drive) newUID() string {
	d.seq++
	return fmt.Sprintf("mem~%06d", d.seq)
}

// AddFile seeds a file under parentUID ("" for the root). It returns the UID.
func (d *Drive) AddFile(parentUID, name string, content []byte) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if parentUID == "" {
		parentUID = rootUID
	}
	return d.add(parentUID, name, drive.TypeFile, content)
}

// AddFolder seeds a folder under parentUID ("" for the root).
func (d *Drive) AddFolder(parentUID, name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if parentUID == "" {
		parentUID = rootUID
	}
	return d.add(parentUID, name, drive.TypeFolder, nil)
}

func (d *Drive) add(parent, name string, typ drive.NodeType, content []byte) string {
	uid := d.newUID()
	now := d.now()
	n := drive.Node{UID: uid, ParentUID: parent, Name: name, NameOK: true, Type: typ, Created: now, Modified: now, ContentModified: now, DirectRole: drive.RoleAdmin}
	if typ == drive.TypeFile {
		n.Size, n.HasSize = int64(len(content)), true
		sum := sha1.Sum(content) //nolint:gosec // the CLI reports SHA-1 digests
		n.SHA1 = hex.EncodeToString(sum[:])
		n.RevisionUID = uid + "~rev1"
	}
	d.nodes[uid] = &entry{node: n, content: content, rev: 1}
	d.children[parent] = append(d.children[parent], uid)
	return uid
}

// Content returns a file's bytes (for assertions).
func (d *Drive) Content(uid string) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.nodes[uid]
	if !ok || e.node.Type != drive.TypeFile {
		return nil, false
	}
	return slices.Clone(e.content), true
}

// Find returns the node named name under parentUID ("" for the root).
func (d *Drive) Find(parentUID, name string) (drive.Node, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if parentUID == "" {
		parentUID = rootUID
	}
	for _, c := range d.children[parentUID] {
		if d.nodes[c].node.Name == name {
			return d.nodes[c].node, true
		}
	}
	return drive.Node{}, false
}

// Trashed returns the names in the trash.
func (d *Drive) Trashed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, uid := range d.trash {
		out = append(out, d.nodes[uid].node.Name)
	}
	sort.Strings(out)
	return out
}

func notFound(what string) error {
	return &pdcli.Error{Kind: pdcli.KindNotFound, Message: "Node not found: " + what}
}

// resolve follows the CLI's rules: a UID segment resolves anywhere, a name
// segment to the first child with that name.
func (d *Drive) resolve(path string) (string, error) {
	segs := drive.SplitSegments(path)
	if len(segs) < 2 || segs[0] != "" {
		return "", notFound(path)
	}
	switch segs[1] {
	case string(drive.SectionMyFiles):
		cur := rootUID
		for _, s := range segs[2:] {
			if drive.IsNodeUID(s) || strings.HasPrefix(s, "mem~") {
				e, ok := d.nodes[s]
				if !ok || !e.node.Trashed.IsZero() {
					return "", notFound(s)
				}
				cur = s
				continue
			}
			found := ""
			for _, c := range d.children[cur] {
				if d.nodes[c].node.Name == s {
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
			for _, uid := range d.trash {
				if d.nodes[uid].node.Name == segs[2] {
					return uid, nil
				}
			}
		}
		return "", &pdcli.Error{Kind: pdcli.KindNotFound, Message: "Trashed node not found"}
	}
	return "", &pdcli.Error{Kind: pdcli.KindUsage, Message: fmt.Sprintf("Path %q is not supported", path)}
}

// List implements the backend.
func (d *Drive) List(ctx context.Context, path string) ([]drive.Node, error) {
	if err := d.begin(ctx, "list"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if path == drive.SectionTrash.Root() {
		var out []drive.Node
		for _, uid := range d.trash {
			out = append(out, d.nodes[uid].node)
		}
		return out, nil
	}
	uid, err := d.resolve(path)
	if err != nil {
		return nil, err
	}
	if d.nodes[uid].node.Type != drive.TypeFolder {
		return nil, &pdcli.Error{Kind: pdcli.KindUsage, Message: "Not a folder"}
	}
	out := make([]drive.Node, 0, len(d.children[uid]))
	for _, c := range d.children[uid] {
		out = append(out, d.nodes[c].node)
	}
	return out, nil
}

// Info implements the backend.
func (d *Drive) Info(ctx context.Context, path string) (drive.Node, error) {
	if err := d.begin(ctx, "info"); err != nil {
		return drive.Node{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	uid, err := d.resolve(path)
	if err != nil {
		return drive.Node{}, err
	}
	return d.nodes[uid].node, nil
}

func (d *Drive) nameTaken(parent, name string) bool {
	for _, c := range d.children[parent] {
		if d.nodes[c].node.Name == name {
			return true
		}
	}
	return false
}

var errConflict = &pdcli.Error{Kind: pdcli.KindConflict, Message: "A file or folder with that name already exists"}

// CreateFolder implements the backend.
func (d *Drive) CreateFolder(ctx context.Context, parent, name string) (drive.Node, error) {
	if err := d.begin(ctx, "create-folder"); err != nil {
		return drive.Node{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p, err := d.resolve(parent)
	if err != nil {
		return drive.Node{}, err
	}
	if d.nameTaken(p, name) {
		return drive.Node{}, errConflict
	}
	return d.nodes[d.add(p, name, drive.TypeFolder, nil)].node, nil
}

// Rename implements the backend.
func (d *Drive) Rename(ctx context.Context, path, newName string) (drive.Node, error) {
	if err := d.begin(ctx, "rename"); err != nil {
		return drive.Node{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	uid, err := d.resolve(path)
	if err != nil {
		return drive.Node{}, err
	}
	e := d.nodes[uid]
	if d.nameTaken(e.node.ParentUID, newName) {
		return drive.Node{}, errConflict
	}
	e.node.Name, e.node.Modified = newName, d.now()
	return e.node, nil
}

func (d *Drive) batch(ctx context.Context, cmd string, paths []string, fn func(uid string) bool) ([]drive.ItemResult, error) {
	if err := d.begin(ctx, cmd); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var res []drive.ItemResult
	failed := 0
	for _, p := range paths {
		uid, err := d.resolve(p)
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

// Move implements the backend.
func (d *Drive) Move(ctx context.Context, sources []string, target string) ([]drive.ItemResult, error) {
	d.mu.Lock()
	t, err := d.resolve(target)
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return d.batch(ctx, "move", sources, func(uid string) bool {
		e := d.nodes[uid]
		if uid == t || d.nameTaken(t, e.node.Name) || d.isAncestor(uid, t) {
			return false
		}
		d.children[e.node.ParentUID] = slices.DeleteFunc(d.children[e.node.ParentUID], func(s string) bool { return s == uid })
		e.node.ParentUID = t
		d.children[t] = append(d.children[t], uid)
		return true
	})
}

func (d *Drive) isAncestor(uid, of string) bool {
	for cur := of; cur != "" && cur != rootUID; cur = d.nodes[cur].node.ParentUID {
		if cur == uid {
			return true
		}
	}
	return false
}

// Copy implements the backend (files only, content duplicated).
func (d *Drive) Copy(ctx context.Context, sources []string, target, name string) ([]drive.ItemResult, error) {
	d.mu.Lock()
	t, err := d.resolve(target)
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return d.batch(ctx, "copy", sources, func(uid string) bool {
		e := d.nodes[uid]
		n := e.node.Name
		if name != "" {
			n = name
		}
		if d.nameTaken(t, n) {
			return false
		}
		d.add(t, n, e.node.Type, slices.Clone(e.content))
		return true
	})
}

// Trash implements the backend.
func (d *Drive) Trash(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	return d.batch(ctx, "trash", paths, func(uid string) bool {
		e := d.nodes[uid]
		if uid == rootUID || !e.node.Trashed.IsZero() {
			return false
		}
		d.children[e.node.ParentUID] = slices.DeleteFunc(d.children[e.node.ParentUID], func(s string) bool { return s == uid })
		e.node.Trashed = d.now()
		d.trash = append(d.trash, uid)
		return true
	})
}

// Restore implements the backend.
func (d *Drive) Restore(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	return d.batch(ctx, "restore", paths, func(uid string) bool {
		e := d.nodes[uid]
		d.trash = slices.DeleteFunc(d.trash, func(s string) bool { return s == uid })
		e.node.Trashed = time.Time{}
		if _, ok := d.nodes[e.node.ParentUID]; !ok {
			e.node.ParentUID = rootUID
		}
		d.children[e.node.ParentUID] = append(d.children[e.node.ParentUID], uid)
		return true
	})
}

// DeleteForever implements the backend.
func (d *Drive) DeleteForever(ctx context.Context, paths ...string) ([]drive.ItemResult, error) {
	return d.batch(ctx, "delete", paths, func(uid string) bool {
		d.trash = slices.DeleteFunc(d.trash, func(s string) bool { return s == uid })
		delete(d.nodes, uid)
		return true
	})
}

// Upload implements the backend. Local paths arrive escaped for the CLI's
// glob syntax, exactly as the real client sends them.
func (d *Drive) Upload(ctx context.Context, s pdcli.UploadSpec) (drive.TransferSummary, error) {
	if err := d.begin(ctx, "upload"); err != nil {
		return drive.TransferSummary{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	parent, err := d.resolve(s.Parent)
	if err != nil {
		return drive.TransferSummary{}, err
	}
	var sum drive.TransferSummary
	for _, lp := range s.LocalPaths {
		lp = pdcli.UnescapeLocal(lp)
		content, rerr := os.ReadFile(lp) //nolint:gosec // test backend reads what it is told
		if rerr != nil {
			sum.FailedItems++
			sum.Failures = append(sum.Failures, drive.TransferFailure{Name: filepath.Base(lp), Error: rerr.Error()})
			continue
		}
		name := filepath.Base(lp)
		if existing := d.childNamed(parent, name); existing != "" {
			e := d.nodes[existing]
			if e.node.SHA1 == sha1hex(content) {
				sum.SkippedItems++
				continue
			}
			switch s.FileStrategy {
			case pdcli.UploadFileNewRevision:
				e.content = content
				e.node.Size, e.node.Modified, e.node.ContentModified = int64(len(content)), d.now(), d.now()
				e.node.SHA1 = sha1hex(content)
				e.rev++
				e.node.RevisionUID = fmt.Sprintf("%s~rev%d", existing, e.rev)
				sum.TransferredItems++
				sum.TransferredBytes += int64(len(content))
				continue
			case pdcli.UploadFileRename:
				name = available(name, func(n string) bool { return d.nameTaken(parent, n) })
			case pdcli.UploadFileSkip:
				sum.SkippedItems++
				continue
			default:
				sum.FailedItems++
				sum.Failures = append(sum.Failures, drive.TransferFailure{Name: name, Error: fmt.Sprintf("ValidationError: Name conflict on %q (file) already exists", name)})
				continue
			}
		}
		d.add(parent, name, drive.TypeFile, content)
		sum.TransferredItems++
		sum.TransferredBytes += int64(len(content))
	}
	if sum.FailedItems > 0 {
		return sum, &pdcli.Error{Kind: pdcli.KindPartial, Message: fmt.Sprintf("%d item(s) failed to upload", sum.FailedItems)}
	}
	return sum, nil
}

func sha1hex(b []byte) string {
	s := sha1.Sum(b) //nolint:gosec // mirrors the CLI
	return hex.EncodeToString(s[:])
}

func (d *Drive) childNamed(parent, name string) string {
	for _, c := range d.children[parent] {
		if d.nodes[c].node.Name == name {
			return c
		}
	}
	return ""
}

func available(name string, taken func(string) bool) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if !taken(cand) {
			return cand
		}
	}
}

// Download implements the backend: writes each remote file into LocalDir
// under the CLI's local name, honouring the file conflict strategy.
func (d *Drive) Download(ctx context.Context, s pdcli.DownloadSpec) (drive.TransferSummary, error) {
	if err := d.begin(ctx, "download"); err != nil {
		return drive.TransferSummary{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	dir := pdcli.UnescapeLocal(s.LocalDir)
	var sum drive.TransferSummary
	for _, r := range s.Remotes {
		uid, err := d.resolve(r)
		if err != nil {
			sum.FailedItems++
			sum.Failures = append(sum.Failures, drive.TransferFailure{Name: r, Error: err.Error()})
			continue
		}
		e := d.nodes[uid]
		if e.node.Type != drive.TypeFile {
			sum.FailedItems++
			sum.Failures = append(sum.Failures, drive.TransferFailure{Name: e.node.Name, Error: "folders not supported by memdrive"})
			continue
		}
		target := filepath.Join(dir, drive.LocalName(e.node.Name))
		if _, serr := os.Stat(target); serr == nil {
			switch s.FileStrategy {
			case pdcli.DownloadFileSkip:
				sum.SkippedItems++
				continue
			case pdcli.DownloadFileRename:
				target = filepath.Join(dir, available(drive.LocalName(e.node.Name), func(n string) bool {
					_, err := os.Stat(filepath.Join(dir, n))
					return err == nil
				}))
			case pdcli.DownloadFileRemove:
				_ = os.Remove(target)
			}
		}
		if werr := os.WriteFile(target, e.content, fsutil.FilePrivate); werr != nil {
			sum.FailedItems++
			sum.Failures = append(sum.Failures, drive.TransferFailure{Name: e.node.Name, Error: werr.Error()})
			continue
		}
		sum.TransferredItems++
		sum.TransferredBytes += int64(len(e.content))
	}
	if sum.FailedItems > 0 {
		return sum, &pdcli.Error{Kind: pdcli.KindPartial, Message: fmt.Sprintf("%d item(s) failed to download", sum.FailedItems)}
	}
	return sum, nil
}
