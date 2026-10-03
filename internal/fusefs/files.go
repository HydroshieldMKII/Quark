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
	n.mu.Lock()
	attr := n.attr
	n.mu.Unlock()
	n.m.log.Debug("open", "uid", attr.UID, "rev", attr.RevisionUID, "write", wantWrite)
	if !wantWrite {
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
		return &handle{n: n, f: f}, 0, 0
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
	if !truncate && attr.HasSize && attr.Size > 0 || !truncate && !attr.HasSize {
		src, errno := n.m.cache.fetch(ctx, n.m, attr, n.path)
		if errno != 0 {
			_ = os.RemoveAll(stage)
			return nil, errno
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
	h := &handle{n: n, f: f, write: true, stage: stage}
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
	if _, errno := n.find(ctx, name); errno == 0 {
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
	h := &handle{n: cn, f: f, write: true, stage: stage}
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

// refind re-lists and finds a child by name.
func (n *node) refind(ctx context.Context, name string) (drive.Node, syscall.Errno) {
	n.invalidate()
	return n.find(ctx, name)
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

// Flush implements fs.FileFlusher: called on close(2); uploads a dirty staging copy.
func (h *handle) Flush(ctx context.Context) syscall.Errno {
	if !h.write || !h.dirty {
		return 0
	}
	return h.upload(ctx)
}

// Release implements fs.FileReleaser.
func (h *handle) Release(ctx context.Context) syscall.Errno {
	var errno syscall.Errno
	if h.write && h.dirty {
		errno = h.upload(ctx) // Flush was not called (e.g. mmap-only access)
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
		if errno == 0 {
			_ = os.RemoveAll(h.stage)
		}
	} else {
		h.n.m.cache.pin(h.n.attr.UID, -1)
	}
	return errno
}

// upload sends the staging copy as a new version of the node. The data is
// never discarded on failure: it is kept under <cache>/unsynced so the user
// can recover it, and the error is reported to the program that closed the
// file.
func (h *handle) upload(ctx context.Context) syscall.Errno {
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
	if err := m.acquire(ctx, m.writes); err != nil {
		return syscall.EINTR
	}
	_, err := m.opts.Backend.Upload(ctx, pdcli.UploadSpec{
		LocalPaths: []string{h.f.Name()}, Parent: pn.path,
		FileStrategy: pdcli.UploadFileNewRevision, FolderStrategy: pdcli.UploadFolderMerge,
	})
	m.release(m.writes)
	if err != nil {
		kept := m.cache.keepUnsynced(h.f.Name())
		m.log.Error("upload failed; local copy kept", "uid", attr.UID, "kept", kept != "", "err", err)
		return toErrno(err)
	}
	h.dirty = false
	pn.invalidate()
	// Refresh the node's metadata (size, revision) and seed the content cache
	// with what was just written, so the next open does not download it again.
	if c, e := pn.find(ctx, drive.LocalName(attr.Name)); e == 0 {
		h.n.mu.Lock()
		h.n.attr = c
		h.n.mu.Unlock()
		m.cache.seed(c, h.f.Name())
	}
	return 0
}
