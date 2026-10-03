package fusefs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

// handle is an open file. Read-only handles serve the cached copy; write
// handles work on a private staging copy that is uploaded on close.
type handle struct {
	n     *node
	f     *os.File
	write bool
	dirty bool
	// stage is the staging directory of a write handle (contains one file
	// named like the remote node).
	stage string
	// base is the revision the staging copy started from.
	base string
	// pinned is set on read handles that hold a cache pin.
	pinned bool
}

var (
	_ fs.FileReader    = (*handle)(nil)
	_ fs.FileWriter    = (*handle)(nil)
	_ fs.FileFlusher   = (*handle)(nil)
	_ fs.FileReleaser  = (*handle)(nil)
	_ fs.FileFsyncer   = (*handle)(nil)
	_ fs.FileGetattrer = (*handle)(nil)
)

const writeFlags = syscall.O_WRONLY | syscall.O_RDWR | syscall.O_APPEND | syscall.O_TRUNC

// Open implements fs.NodeOpener.
func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if n.isDir() {
		return nil, 0, syscall.EISDIR
	}
	wantWrite := flags&writeFlags != 0
	if wantWrite && n.m.opts.ReadOnly {
		return nil, 0, syscall.EROFS
	}
	// Lookups answer from a possibly stale listing; a program that opens
	// the file wants the current version, so check with the server first
	// (listings never wait behind downloads).
	n.freshen(ctx)
	n.mu.Lock()
	attr := n.attr
	// If the kernel still holds the size of an older revision, serve this
	// open with direct I/O so reads are not cut at that size.
	var readFlags uint32
	if n.seenRev != attr.RevisionUID {
		readFlags = fuse.FOPEN_DIRECT_IO
	}
	n.mu.Unlock()
	n.m.log.Debug("open", "uid", attr.UID, "rev", attr.RevisionUID, "write", wantWrite)
	if !wantWrite {
		// A change waiting for upload is the current content.
		if p := n.m.pending.contentPath(attr.UID); p != "" {
			f, err := os.Open(p) //nolint:gosec // our own journal
			if err == nil {
				return &handle{n: n, f: f}, fuse.FOPEN_DIRECT_IO, 0
			}
		}
		path, errno := n.m.cache.fetch(ctx, n.m, attr, n.path)
		if errno != 0 {
			return nil, 0, errno
		}
		f, err := os.Open(path) //nolint:gosec // our own cache path
		if err != nil {
			return nil, 0, syscall.EIO
		}
		n.m.cache.pin(attr.UID, 1)
		// No FOPEN_KEEP_CACHE: the kernel drops its page cache on each open,
		// so a new version uploaded elsewhere is never served from stale pages.
		return &handle{n: n, f: f, pinned: true}, readFlags, 0
	}
	if errno := n.writable(); errno != 0 {
		return nil, 0, errno
	}
	h, errno := n.openWrite(ctx, attr, flags&syscall.O_TRUNC != 0)
	if errno != 0 {
		return nil, 0, errno
	}
	return h, fuse.FOPEN_DIRECT_IO, 0
}

// openWrite prepares a staging copy: empty for O_TRUNC, else the current content.
func (n *node) openWrite(ctx context.Context, attr drive.Node, truncate bool) (*handle, syscall.Errno) {
	stage, err := os.MkdirTemp(filepath.Join(n.m.opts.CacheDir, "stage"), "w-")
	if err != nil {
		return nil, syscall.EIO
	}
	target := filepath.Join(stage, drive.LocalName(attr.Name))
	base := attr.RevisionUID
	src := ""
	if e := n.m.pending.get(attr.UID); e != nil {
		// Edits chain on the pending content; the base stays the revision
		// the first unsynced edit was made from.
		src, base = e.content(n.m.pending.dir), e.BaseRevision
	}
	if !truncate && (src != "" || attr.HasSize && attr.Size > 0 || !attr.HasSize) {
		if src == "" {
			var errno syscall.Errno
			if src, errno = n.m.cache.fetch(ctx, n.m, attr, n.path); errno != 0 {
				_ = os.RemoveAll(stage)
				return nil, errno
			}
		}
		if err := copyFile(src, target); err != nil {
			_ = os.RemoveAll(stage)
			return nil, syscall.EIO
		}
	}
	f, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // our staging dir
	if err != nil {
		_ = os.RemoveAll(stage)
		return nil, syscall.EIO
	}
	h := &handle{n: n, f: f, write: true, stage: stage, base: base}
	n.mu.Lock()
	n.writers++
	n.staging = target
	if n.open == nil {
		n.open = map[*handle]struct{}{}
	}
	n.open[h] = struct{}{}
	n.mu.Unlock()
	return h, 0
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // cache path
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // staging path
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Create implements fs.NodeCreater: the empty file is uploaded at once so
// it exists remotely (and for other clients), then content follows on close.
func (n *node) Create(ctx context.Context, name string, _ uint32, _ uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.m.opts.ReadOnly {
		return nil, nil, 0, syscall.EROFS
	}
	if !n.isDir() {
		return nil, nil, 0, syscall.ENOTDIR
	}
	if _, errno := n.find(ctx, name, listFresh); errno == 0 {
		return nil, nil, 0, syscall.EEXIST
	}
	stage, err := os.MkdirTemp(filepath.Join(n.m.opts.CacheDir, "stage"), "c-")
	if err != nil {
		return nil, nil, 0, syscall.EIO
	}
	target := filepath.Join(stage, name)
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		_ = os.RemoveAll(stage)
		return nil, nil, 0, syscall.EIO
	}
	if err := n.m.acquire(ctx, n.m.writes); err != nil {
		_ = os.RemoveAll(stage)
		return nil, nil, 0, syscall.EINTR
	}
	_, uerr := n.m.opts.Backend.Upload(ctx, pdcli.UploadSpec{
		LocalPaths: []string{target}, Parent: n.path,
		FileStrategy: pdcli.UploadFileNewRevision, FolderStrategy: pdcli.UploadFolderMerge, SkipThumbnails: true,
	})
	n.m.release(n.m.writes)
	if uerr != nil {
		_ = os.RemoveAll(stage)
		return nil, nil, 0, toErrno(uerr)
	}
	c, errno := n.refind(ctx, name)
	if errno != 0 {
		_ = os.RemoveAll(stage)
		return nil, nil, 0, errno
	}
	inode := n.child(ctx, c)
	cn := inode.Operations().(*node)
	f, err := os.OpenFile(target, os.O_RDWR, 0o600) //nolint:gosec // staging path
	if err != nil {
		_ = os.RemoveAll(stage)
		return nil, nil, 0, syscall.EIO
	}
	h := &handle{n: cn, f: f, write: true, stage: stage, base: c.RevisionUID}
	cn.mu.Lock()
	cn.writers++
	cn.staging = target
	if cn.open == nil {
		cn.open = map[*handle]struct{}{}
	}
	cn.open[h] = struct{}{}
	cn.mu.Unlock()
	cn.fillAttr(&out.Attr)
	return inode, h, fuse.FOPEN_DIRECT_IO, 0
}

// freshen refreshes the node's metadata from a fresh listing of its parent
// when the current one is stale. Offline it keeps what it has.
func (n *node) freshen(ctx context.Context) {
	_, p := n.Parent()
	if p == nil {
		return
	}
	pn := p.Operations().(*node)
	entries, errno := pn.list(ctx, listFresh)
	if errno != 0 {
		return
	}
	for _, c := range entries {
		if c.UID == n.uid {
			n.mu.Lock()
			if n.writers == 0 {
				n.attr = c
			}
			n.path = pn.childPath(c)
			n.mu.Unlock()
			return
		}
	}
}

// refind re-lists and finds a child by name.
func (n *node) refind(ctx context.Context, name string) (drive.Node, syscall.Errno) {
	n.invalidate()
	return n.find(ctx, name, listForce)
}

// Read implements fs.FileReader.
func (h *handle) Read(_ context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	return fuse.ReadResultFd(h.f.Fd(), off, len(dest)), 0
}

// Write implements fs.FileWriter.
func (h *handle) Write(_ context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if !h.write {
		return 0, syscall.EBADF
	}
	n, err := h.f.WriteAt(data, off)
	if err != nil {
		return uint32(n), syscall.EIO //nolint:gosec // n <= len(data)
	}
	h.dirty = true
	return uint32(n), 0 //nolint:gosec // n <= len(data)
}

// Getattr implements fs.FileGetattrer.
func (h *handle) Getattr(_ context.Context, out *fuse.AttrOut) syscall.Errno {
	h.n.fillAttr(&out.Attr)
	return 0
}

// Fsync implements fs.FileFsyncer: data is durable locally; upload happens on close.
func (h *handle) Fsync(_ context.Context, _ uint32) syscall.Errno {
	if h.write {
		if err := h.f.Sync(); err != nil {
			return syscall.EIO
		}
	}
	return 0
}

// Flush implements fs.FileFlusher: called on close(2); commits a dirty staging copy.
func (h *handle) Flush(ctx context.Context) syscall.Errno {
	if !h.write || !h.dirty {
		return 0
	}
	return h.commit(ctx)
}

// Release implements fs.FileReleaser.
func (h *handle) Release(ctx context.Context) syscall.Errno {
	var errno syscall.Errno
	if h.write && h.dirty {
		errno = h.commit(ctx) // Flush was not called (e.g. mmap-only access)
	}
	_ = h.f.Close()
	if h.write {
		h.n.mu.Lock()
		h.n.writers--
		delete(h.n.open, h)
		if h.n.writers == 0 {
			h.n.staging = ""
		}
		h.n.mu.Unlock()
		_ = os.RemoveAll(h.stage)
	} else if h.pinned {
		h.n.m.cache.pin(h.n.uid, -1)
	}
	return errno
}

// commit makes the staging copy the pending content of the node, durable
// in the journal, then tries to upload it at once. The close succeeds as
// soon as the change is journaled: a failed upload is retried in the
// background and never loses the data.
func (h *handle) commit(ctx context.Context) syscall.Errno {
	if err := h.f.Sync(); err != nil {
		return syscall.EIO
	}
	m := h.n.m
	h.n.mu.Lock()
	attr := h.n.attr
	h.n.mu.Unlock()
	_, parentNode := h.n.Parent()
	if parentNode == nil {
		return syscall.EIO
	}
	pn := parentNode.Operations().(*node)
	// The journal takes the file itself (same file system: a rename), so a
	// second close of a dup'ed descriptor works on a fresh copy.
	snapshot := filepath.Join(h.stage, "commit")
	if err := copyFile(h.f.Name(), snapshot); err != nil {
		return syscall.EIO
	}
	e, err := m.pending.put(attr.UID, attr.Name, pn.path, h.base, snapshot)
	if err != nil {
		m.log.Error("journaling the change failed; copy kept under unsynced", "name", attr.Name, "err", err)
		m.cache.keepUnsynced(snapshot, attr.Name)
		return syscall.EIO
	}
	h.dirty = false
	m.log.Debug("change journaled", "uid", attr.UID, "gen", e.Gen, "size", e.Size)
	m.syncer.uploadNow(ctx, e.UID, e.Gen)
	return 0
}
