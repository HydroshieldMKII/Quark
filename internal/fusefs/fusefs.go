// Package fusefs mounts Proton Drive as a folder with FUSE, backed by the
// Proton Drive CLI through the same typed client the app uses.
//
// Model: directory listings are cached for a short time; a file's content is
// downloaded in full into a local cache on first open and served from
// there; writes go to a local staging copy that becomes a pending change
// when the file is closed and is uploaded as a new version, at once when
// possible and otherwise retried in the background (a remote change made
// meanwhile turns the upload into a conflict copy). Pinned files and
// folders are kept on this device by a background pass. Deleting moves to
// the trash. Files stay end-to-end encrypted on Proton's side: only the CLI
// decrypts them, into this user's cache directory. Extended attributes
// (see xattr.go) expose state and controls to other programs.
package fusefs

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/fsutil"
	"example.org/quark/internal/pdcli"
)

// Backend is the subset of *pdcli.Client the mount needs.
type Backend interface {
	List(ctx context.Context, path string) ([]drive.Node, error)
	Info(ctx context.Context, path string) (drive.Node, error)
	CreateFolder(ctx context.Context, parent, name string) (drive.Node, error)
	Rename(ctx context.Context, path, newName string) (drive.Node, error)
	Move(ctx context.Context, sources []string, target string) ([]drive.ItemResult, error)
	Trash(ctx context.Context, paths ...string) ([]drive.ItemResult, error)
	Upload(ctx context.Context, s pdcli.UploadSpec) (drive.TransferSummary, error)
	Download(ctx context.Context, s pdcli.DownloadSpec) (drive.TransferSummary, error)
}

var _ Backend = (*pdcli.Client)(nil)

// Options configures a mount.
type Options struct {
	Backend Backend
	// CacheDir holds downloaded content and staged writes; required.
	CacheDir string
	// Root is the remote folder shown at the mount point (default /my-files).
	Root string
	// ReadOnly refuses every change.
	ReadOnly bool
	// ListTTL is how long a directory listing is trusted (default 10 s).
	ListTTL time.Duration
	// CacheLimit bounds the content cache in bytes (default 1 GiB).
	CacheLimit int64
	// MaxReads and MaxWrites cap concurrent CLI processes (default 2 and 1).
	MaxReads, MaxWrites int
	// SyncInterval is how often pinned content is checked (default 1 min).
	SyncInterval time.Duration
	// RetryBase is the first retry delay of a failed upload (default 5 s);
	// later retries back off up to 5 minutes.
	RetryBase time.Duration
	// AddressByName addresses nodes by path names instead of UIDs. Only for
	// tests through the sandbox guard, which refuses UID segments.
	AddressByName bool
	Logger        *slog.Logger
	Debug         bool
}

func (o *Options) defaults() {
	if o.Root == "" {
		o.Root = drive.SectionMyFiles.Root()
	}
	if o.ListTTL <= 0 {
		o.ListTTL = 10 * time.Second
	}
	if o.CacheLimit <= 0 {
		o.CacheLimit = 1 << 30
	}
	if o.MaxReads <= 0 {
		o.MaxReads = 2
	}
	if o.MaxWrites <= 0 {
		o.MaxWrites = 1
	}
	if o.SyncInterval <= 0 {
		o.SyncInterval = time.Minute
	}
	if o.RetryBase <= 0 {
		o.RetryBase = 5 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
}

// Mounted is a mounted file system.
type Mounted struct {
	opts    Options
	server  *fuse.Server
	cache   *contentCache
	pins    *pinStore
	pending *pendingStore
	syncer  *syncer
	reads   chan struct{}
	writes  chan struct{}
	log     *slog.Logger
	root    *node
	uid     uint32
	gid     uint32
	// inodes are the live nodes by UID, for background refreshes.
	inodesMu sync.Mutex
	inodes   map[string]*node
	// protected holds the cache segments of files found under pinned
	// folders by the last sync pass; they are never evicted.
	protected map[string]bool
}

// keepCached reports whether the cache segment seg belongs to a pinned file:
// pinned itself, found under a pinned folder, or a live inode with a pinned
// ancestor.
func (m *Mounted) keepCached(seg string) bool {
	if m.pins.hasSegment(seg) {
		return true
	}
	m.inodesMu.Lock()
	protected := m.protected[seg]
	var live *node
	for uid, n := range m.inodes {
		if safeSegment(uid) == seg {
			live = n
			break
		}
	}
	m.inodesMu.Unlock()
	if protected {
		return true
	}
	return live != nil && live.pinned()
}

// Mount mounts the drive at mountpoint, which must be an existing empty
// directory, and returns once the mount is usable.
func Mount(mountpoint string, o Options) (*Mounted, error) {
	o.defaults()
	if o.Backend == nil {
		return nil, errors.New("fusefs: no backend")
	}
	if !filepath.IsAbs(o.CacheDir) {
		return nil, errors.New("fusefs: CacheDir must be absolute")
	}
	pins := newPinStore(filepath.Join(o.CacheDir, "pins.json"))
	pending, err := newPendingStore(filepath.Join(o.CacheDir, "pending"))
	if err != nil {
		return nil, err
	}
	m := &Mounted{
		opts: o, pins: pins, pending: pending, log: o.Logger,
		reads: make(chan struct{}, o.MaxReads), writes: make(chan struct{}, o.MaxWrites),
		uid: uint32(os.Getuid()), gid: uint32(os.Getgid()), //nolint:gosec // ids are small
		inodes: map[string]*node{}, protected: map[string]bool{},
	}
	m.cache, err = newContentCache(o.CacheDir, o.CacheLimit, o.Logger, m.keepCached)
	if err != nil {
		return nil, err
	}
	m.syncer = newSyncer(m)
	m.root = m.newNode(drive.Node{UID: "root", Name: "/", NameOK: true, Type: drive.TypeFolder}, o.Root)
	m.inodes["root"] = m.root
	if o.ReadOnly && len(pending.entries) > 0 {
		m.log.Warn("read-only mount: pending changes from an earlier run are kept but not uploaded", "pending", len(pending.entries))
	}
	ttl := o.ListTTL
	fsOpts := &fs.Options{
		EntryTimeout: &ttl, AttrTimeout: &ttl, NegativeTimeout: &ttl,
		UID: m.uid, GID: m.gid,
		MountOptions: fuse.MountOptions{
			FsName: "quark", Name: "quark", Debug: o.Debug,
			MaxWrite: 1 << 20,
			// With atomic O_TRUNC the kernel passes O_TRUNC to Open instead
			// of truncating afterwards, so opening a file to overwrite it
			// never downloads the old content first.
			ExtraCapabilities: fuse.CAP_ATOMIC_O_TRUNC,
			// Without this the kernel would let any local user read the
			// decrypted files through the mount.
			Options: []string{"default_permissions"},
		},
		RootStableAttr: &fs.StableAttr{Mode: syscall.S_IFDIR, Ino: 1},
	}
	server, err := fs.Mount(mountpoint, m.root, fsOpts)
	if err != nil {
		return nil, err
	}
	m.server = server
	if err := server.WaitMount(); err != nil {
		_ = server.Unmount()
		return nil, err
	}
	if !o.ReadOnly {
		m.syncer.start()
		m.syncer.kick()
	}
	m.log.Info("mounted", "root", o.Root, "readOnly", o.ReadOnly, "pins", pins.count(), "pending", len(pending.entries))
	return m, nil
}

// Unmount detaches the file system. Open files are flushed by the kernel
// first; background work is then stopped. Pending changes stay in the
// cache directory and resume on the next mount.
func (m *Mounted) Unmount() error {
	if err := m.server.Unmount(); err != nil {
		return err
	}
	m.syncer.stop()
	if n, _, _ := m.pending.stats(); n > 0 {
		m.log.Warn("unmounted with changes still pending; they resume on the next mount", "pending", n)
	}
	return nil
}

// saveListing keeps a folder listing on disk for offline use after a restart.
func (m *Mounted) saveListing(uid string, nodes []drive.Node) {
	data, err := json.Marshal(nodes)
	if err != nil {
		return
	}
	path := filepath.Join(m.opts.CacheDir, "listings", safeSegment(uid)+".json")
	if err := fsutil.WriteFileAtomic(path, data, 0o600); err != nil {
		m.log.Warn("saving listing", "err", err)
	}
}

// loadListing returns the listing saved for uid, or nil.
func (m *Mounted) loadListing(uid string) []drive.Node {
	data, err := os.ReadFile(filepath.Join(m.opts.CacheDir, "listings", safeSegment(uid)+".json")) //nolint:gosec // our own file
	if err != nil {
		return nil
	}
	var nodes []drive.Node
	if json.Unmarshal(data, &nodes) != nil {
		return nil
	}
	if nodes == nil {
		nodes = []drive.Node{}
	}
	m.log.Info("offline: using the saved listing", "uid", uid, "entries", len(nodes))
	return nodes
}

// Pending returns the local changes not yet uploaded, oldest first.
func (m *Mounted) Pending() []PendingChange {
	var out []PendingChange
	for _, e := range m.pending.list() {
		out = append(out, PendingChange{Name: e.Name, Size: e.Size, Updated: e.Updated, Attempts: e.Attempts, LastError: e.LastError})
	}
	return out
}

// PendingChange describes one entry of Pending.
type PendingChange struct {
	Name      string
	Size      int64
	Updated   time.Time
	Attempts  int
	LastError string
}

// Wait blocks until the file system is unmounted.
func (m *Mounted) Wait() { m.server.Wait() }

func (m *Mounted) acquire(ctx context.Context, sem chan struct{}) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Mounted) release(sem chan struct{}) { <-sem }

// toErrno maps backend errors to errno values.
func toErrno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	switch pdcli.KindOf(err) {
	case pdcli.KindNotFound:
		return syscall.ENOENT
	case pdcli.KindConflict:
		return syscall.EEXIST
	case pdcli.KindNotLoggedIn:
		return syscall.EACCES
	case pdcli.KindNetwork, pdcli.KindRateLimited, pdcli.KindServer, pdcli.KindTimeout:
		return syscall.EAGAIN
	case pdcli.KindCancelled:
		return syscall.EINTR
	case pdcli.KindUsage, pdcli.KindRefused:
		return syscall.EINVAL
	case pdcli.KindOutputTooLarge:
		return syscall.EFBIG
	default:
		return syscall.EIO
	}
}

// ino derives a stable inode number from a UID.
func ino(uid string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(uid))
	n := h.Sum64()
	if n < 2 {
		n += 2
	}
	return n
}

// ----- nodes -----

// node is a file or folder. attr is the last metadata seen; for directories
// entries is the cached listing.
type node struct {
	fs.Inode
	m *Mounted
	// uid and dir never change and may be read without the lock.
	uid  string
	dir  bool
	mu   sync.Mutex
	attr drive.Node
	// path addresses this node for the CLI.
	path    string
	entries []drive.Node
	listed  time.Time
	// writers counts open handles that may change the content; while > 0,
	// size comes from the staging file.
	writers int
	staging string
	// open holds the write handles, for truncation requests that arrive
	// without a handle (the kernel emulates O_TRUNC that way).
	open map[*handle]struct{}
}

var (
	_ fs.NodeLookuper  = (*node)(nil)
	_ fs.NodeReaddirer = (*node)(nil)
	_ fs.NodeGetattrer = (*node)(nil)
	_ fs.NodeSetattrer = (*node)(nil)
	_ fs.NodeOpener    = (*node)(nil)
	_ fs.NodeCreater   = (*node)(nil)
	_ fs.NodeMkdirer   = (*node)(nil)
	_ fs.NodeUnlinker  = (*node)(nil)
	_ fs.NodeRmdirer   = (*node)(nil)
	_ fs.NodeRenamer   = (*node)(nil)
	_ fs.NodeStatfser  = (*node)(nil)
)

func (n *node) isDir() bool { return n.dir }

// newNode makes a node for c (its UID and type are fixed for life).
func (m *Mounted) newNode(c drive.Node, path string) *node {
	return &node{m: m, uid: c.UID, dir: c.Type == drive.TypeFolder, attr: c, path: path}
}

// childPath is the CLI path of a child listed under this node.
func (n *node) childPath(c drive.Node) string {
	if n.m.opts.AddressByName {
		return n.path + "/" + drive.EscapeSegment(c.Name)
	}
	return drive.ByUID(c.UID)
}

// list returns the children, refreshing the listing after the TTL.
func (n *node) list(ctx context.Context, force bool) ([]drive.Node, syscall.Errno) {
	n.mu.Lock()
	fresh := !force && !n.listed.IsZero() && time.Since(n.listed) < n.m.opts.ListTTL
	entries := n.entries
	n.mu.Unlock()
	if fresh {
		return entries, 0
	}
	if err := n.m.acquire(ctx, n.m.reads); err != nil {
		return nil, syscall.EINTR
	}
	defer n.m.release(n.m.reads)
	nodes, err := n.m.opts.Backend.List(ctx, n.path)
	if err != nil {
		n.m.log.Warn("list failed", "err", err)
		// Offline, the last listing is better than an error: it is what
		// the cache and the journal can still serve.
		if entries == nil && retryable(err) {
			entries = n.m.loadListing(n.uid)
		}
		if entries != nil && retryable(err) {
			n.mu.Lock()
			if n.entries == nil {
				n.entries = entries
			}
			n.mu.Unlock()
			return entries, 0
		}
		return nil, toErrno(err)
	}
	n.mu.Lock()
	n.entries, n.listed = nodes, time.Now()
	n.mu.Unlock()
	n.m.saveListing(n.uid, nodes)
	n.m.log.Debug("listed", "uid", n.uid, "entries", len(nodes))
	return nodes, 0
}

func (n *node) invalidate() {
	n.mu.Lock()
	n.listed = time.Time{}
	n.mu.Unlock()
}

// find returns the child whose local name is name (first match, as the CLI does).
func (n *node) find(ctx context.Context, name string) (drive.Node, syscall.Errno) {
	entries, errno := n.list(ctx, false)
	if errno != 0 {
		return drive.Node{}, errno
	}
	for _, e := range entries {
		if drive.LocalName(e.Name) == name {
			return e, 0
		}
	}
	return drive.Node{}, syscall.ENOENT
}

func (n *node) stable(c drive.Node) fs.StableAttr {
	mode := uint32(syscall.S_IFREG)
	if c.Type == drive.TypeFolder {
		mode = syscall.S_IFDIR
	}
	return fs.StableAttr{Mode: mode, Ino: ino(c.UID)}
}

// child returns the inode for a listed child with its metadata refreshed.
// go-fuse only merges a new inode with an existing one after Lookup
// returns, so the live child must be found and updated here.
func (n *node) child(ctx context.Context, c drive.Node) *fs.Inode {
	path := n.childPath(c)
	if existing := n.GetChild(drive.LocalName(c.Name)); existing != nil && existing.StableAttr().Ino == ino(c.UID) {
		en := existing.Operations().(*node)
		en.mu.Lock()
		if en.writers == 0 {
			en.attr = c
		}
		en.path = path
		en.mu.Unlock()
		return existing
	}
	nn := n.m.newNode(c, path)
	inode := n.NewInode(ctx, nn, n.stable(c))
	if live, ok := inode.Operations().(*node); ok {
		n.m.inodesMu.Lock()
		n.m.inodes[c.UID] = live
		n.m.inodesMu.Unlock()
	}
	return inode
}

func (n *node) fillAttr(out *fuse.Attr) {
	n.mu.Lock()
	defer n.mu.Unlock()
	a := n.attr
	out.Uid, out.Gid = n.m.uid, n.m.gid
	if n.isDir() {
		out.Mode = syscall.S_IFDIR | 0o700
		out.Nlink = 2
	} else {
		out.Mode = syscall.S_IFREG | 0o600
		out.Nlink = 1
		out.Size = uint64(max(a.Size, 0)) //nolint:gosec // non-negative
		if p := n.m.pending.contentPath(a.UID); p != "" {
			if st, err := os.Stat(p); err == nil {
				out.Size = uint64(st.Size()) //nolint:gosec // non-negative
				mt := st.ModTime()
				a.Modified, a.ContentModified = mt, mt
			}
		}
		if n.writers > 0 && n.staging != "" {
			if st, err := os.Stat(n.staging); err == nil {
				out.Size = uint64(st.Size()) //nolint:gosec // non-negative
			}
		}
		out.Blocks = (out.Size + 511) / 512
	}
	mt := a.Modified
	if !a.ContentModified.IsZero() {
		mt = a.ContentModified
	}
	if mt.IsZero() {
		mt = time.Now()
	}
	ct := a.Created
	if ct.IsZero() {
		ct = mt
	}
	out.SetTimes(&mt, &mt, &ct)
}

// Lookup implements fs.NodeLookuper.
func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if !n.isDir() {
		return nil, syscall.ENOTDIR
	}
	c, errno := n.find(ctx, name)
	if errno != 0 {
		return nil, errno
	}
	inode := n.child(ctx, c)
	inode.Operations().(*node).fillAttr(&out.Attr)
	return inode, 0
}

// Readdir implements fs.NodeReaddirer.
func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, errno := n.list(ctx, false)
	if errno != 0 {
		return nil, errno
	}
	seen := map[string]bool{}
	list := make([]fuse.DirEntry, 0, len(entries))
	for _, e := range entries {
		name := drive.LocalName(e.Name)
		if seen[name] {
			continue // two remote names map to one local name: first wins
		}
		seen[name] = true
		list = append(list, fuse.DirEntry{Mode: n.stable(e).Mode, Name: name, Ino: ino(e.UID)})
	}
	return fs.NewListDirStream(list), 0
}

// Getattr implements fs.NodeGetattrer.
func (n *node) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	n.fillAttr(&out.Attr)
	return 0
}

// Setattr implements fs.NodeSetattrer: truncation is honoured, modes and
// times are accepted and ignored (the CLI does not store them).
func (n *node) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if n.m.opts.ReadOnly {
		return syscall.EROFS
	}
	if sz, ok := in.GetSize(); ok {
		if n.isDir() {
			return syscall.EISDIR
		}
		if errno := n.writable(); errno != 0 {
			return errno
		}
		if errno := n.truncate(ctx, f, int64(sz)); errno != 0 { //nolint:gosec // size from the kernel
			return errno
		}
	}
	n.fillAttr(&out.Attr)
	return 0
}

// truncate applies a new size: to the given write handle, else to every
// open write handle (the kernel sends O_TRUNC as a separate request without
// a handle), else as an immediate new version.
func (n *node) truncate(ctx context.Context, f fs.FileHandle, size int64) syscall.Errno {
	if h, ok := f.(*handle); ok && h.write {
		if err := h.f.Truncate(size); err != nil {
			return syscall.EIO
		}
		h.dirty = true
		return 0
	}
	n.mu.Lock()
	handles := make([]*handle, 0, len(n.open))
	for h := range n.open {
		handles = append(handles, h)
	}
	n.mu.Unlock()
	if len(handles) > 0 {
		for _, h := range handles {
			if err := h.f.Truncate(size); err != nil {
				return syscall.EIO
			}
			h.dirty = true
		}
		return 0
	}
	n.mu.Lock()
	attr := n.attr
	n.mu.Unlock()
	h, errno := n.openWrite(ctx, attr, size == 0)
	if errno != 0 {
		return errno
	}
	if err := h.f.Truncate(size); err != nil {
		_ = h.Release(ctx)
		return syscall.EIO
	}
	h.dirty = true
	errno = h.commit(ctx)
	_ = h.Release(ctx)
	return errno
}

// writable reports whether a file's content may be changed through the
// mount. A name the CLI would sanitize on download cannot be uploaded back
// under the same name, so such files are read-only here.
func (n *node) writable() syscall.Errno {
	n.mu.Lock()
	name := n.attr.Name
	n.mu.Unlock()
	if drive.LocalName(name) != name {
		return syscall.EPERM
	}
	return 0
}

// Statfs implements fs.NodeStatfser with generous constants: the CLI does
// not report quota.
func (n *node) Statfs(_ context.Context, out *fuse.StatfsOut) syscall.Errno {
	const tb = 1 << 40
	out.Bsize, out.Frsize = 4096, 4096
	out.Blocks, out.Bfree, out.Bavail = tb/4096, tb/8192, tb/8192
	out.Files, out.Ffree = 1<<20, 1<<19
	out.NameLen = 255
	return 0
}

// Mkdir implements fs.NodeMkdirer.
func (n *node) Mkdir(ctx context.Context, name string, _ uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.m.opts.ReadOnly {
		return nil, syscall.EROFS
	}
	if err := n.m.acquire(ctx, n.m.writes); err != nil {
		return nil, syscall.EINTR
	}
	defer n.m.release(n.m.writes)
	created, err := n.m.opts.Backend.CreateFolder(ctx, n.path, name)
	if err != nil {
		return nil, toErrno(err)
	}
	n.invalidate()
	inode := n.child(ctx, created)
	inode.Operations().(*node).fillAttr(&out.Attr)
	return inode, 0
}

// Unlink implements fs.NodeUnlinker: the file goes to the trash.
func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.trashChild(ctx, name, false)
}

// Rmdir implements fs.NodeRmdirer: an empty folder goes to the trash.
func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.trashChild(ctx, name, true)
}

func (n *node) trashChild(ctx context.Context, name string, dir bool) syscall.Errno {
	if n.m.opts.ReadOnly {
		return syscall.EROFS
	}
	c, errno := n.find(ctx, name)
	if errno != 0 {
		return errno
	}
	if dir != (c.Type == drive.TypeFolder) {
		if dir {
			return syscall.ENOTDIR
		}
		return syscall.EISDIR
	}
	if dir {
		// POSIX: only empty directories are removed this way.
		if ch := n.GetChild(name); ch != nil {
			if entries, e := ch.Operations().(*node).list(ctx, true); e != 0 || len(entries) > 0 {
				if e != 0 {
					return e
				}
				return syscall.ENOTEMPTY
			}
		} else if entries, err := n.m.opts.Backend.List(ctx, n.childPath(c)); err != nil || len(entries) > 0 {
			if err != nil {
				return toErrno(err)
			}
			return syscall.ENOTEMPTY
		}
	}
	if err := n.m.acquire(ctx, n.m.writes); err != nil {
		return syscall.EINTR
	}
	defer n.m.release(n.m.writes)
	if _, err := n.m.opts.Backend.Trash(ctx, n.childPath(c)); err != nil {
		return toErrno(err)
	}
	n.invalidate()
	n.m.dropLocal(c.UID)
	return 0
}

// dropLocal forgets a node that left the tree: its cached content goes,
// a pending change of it is kept under unsynced (the user may want it
// back), and its pin is released.
func (m *Mounted) dropLocal(uid string) {
	if p := m.pending.contentPath(uid); p != "" {
		if e := m.pending.get(uid); e != nil {
			kept := m.cache.keepUnsynced(p, e.Name)
			m.log.Warn("removed a file with a change not yet uploaded; copy kept", "name", e.Name, "kept", kept)
			m.pending.done(uid, e.Gen)
		}
	}
	m.cache.forget(uid)
	_ = m.pins.unset(uid)
}

// Rename implements fs.NodeRenamer. An existing target is moved to the trash
// first, so that "save to temp file, then rename over" works like on a local
// disk. Across folders this is a move followed by a rename when the name
// changes too.
func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, _ uint32) syscall.Errno {
	if n.m.opts.ReadOnly {
		return syscall.EROFS
	}
	target, ok := newParent.(*node)
	if !ok || !target.isDir() {
		return syscall.ENOTDIR
	}
	c, errno := n.find(ctx, name)
	if errno != 0 {
		return errno
	}
	if n.m.opts.AddressByName && c.Type == drive.TypeFolder && target != n {
		// By-name addressing cannot follow a folder that moved; not needed by tests.
		return syscall.ENOTSUP
	}
	if err := n.m.acquire(ctx, n.m.writes); err != nil {
		return syscall.EINTR
	}
	defer n.m.release(n.m.writes)
	be := n.m.opts.Backend
	if existing, e := target.find(ctx, newName); e == 0 && existing.UID != c.UID {
		if _, err := be.Trash(ctx, target.childPath(existing)); err != nil {
			return toErrno(err)
		}
		target.invalidate()
		n.m.dropLocal(existing.UID)
	}
	path := n.childPath(c)
	if target != n {
		if _, err := be.Move(ctx, []string{path}, target.path); err != nil {
			return toErrno(err)
		}
		n.invalidate()
		target.invalidate()
		c.ParentUID = target.uid
		path = target.childPath(c)
	}
	if newName != drive.LocalName(c.Name) {
		if _, err := be.Rename(ctx, path, newName); err != nil {
			return toErrno(err)
		}
		c.Name = newName
	}
	n.invalidate()
	target.invalidate()
	newPath := target.childPath(c)
	if ch := n.GetChild(name); ch != nil {
		cn := ch.Operations().(*node)
		cn.mu.Lock()
		cn.attr.Name, cn.attr.ParentUID = c.Name, c.ParentUID
		cn.path = newPath
		cn.mu.Unlock()
	}
	n.m.pins.update(c.UID, c.Name, newPath)
	n.m.pending.rename(c.UID, c.Name, target.path)
	return 0
}
