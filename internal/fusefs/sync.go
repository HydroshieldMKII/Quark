package fusefs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

// syncer runs the background work of the mount: uploading pending changes
// with retries, and keeping pinned files on this device. It is the only
// code that talks to Proton Drive without a program asking for it, and it
// never does anything a pending entry or a pin did not request.
type syncer struct {
	m      *Mounted
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// kicks wakes the upload loop, pinKicks the pin loop.
	kicks    chan struct{}
	pinKicks chan struct{}

	mu       sync.Mutex
	lastSync time.Time
	lastErr  string
	running  bool
}

func newSyncer(m *Mounted) *syncer {
	ctx, cancel := context.WithCancel(context.Background())
	return &syncer{m: m, ctx: ctx, cancel: cancel, kicks: make(chan struct{}, 1), pinKicks: make(chan struct{}, 1)}
}

func (s *syncer) start() {
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.uploadLoop() }()
	go func() { defer s.wg.Done(); s.pinLoop() }()
}

func (s *syncer) stop() {
	s.cancel()
	s.wg.Wait()
}

// uploadNow uploads the given generation synchronously if nothing is
// uploading it already, returning when the attempt is over or ctx ends.
// The upload itself runs under the syncer's context, so a program that
// gives up waiting does not abort it.
func (s *syncer) uploadNow(ctx context.Context, uid string, gen int) {
	if s.ctx.Err() != nil {
		return
	}
	e := s.m.pending.claim(uid, gen)
	if e == nil {
		return
	}
	done := make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(done)
		s.upload(e)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// kick asks both loops for a pass as soon as possible.
func (s *syncer) kick() {
	select {
	case s.kicks <- struct{}{}:
	default:
	}
	select {
	case s.pinKicks <- struct{}{}:
	default:
	}
}

// syncNow retries every pending change at once and checks the pins.
func (s *syncer) syncNow() {
	s.m.pending.retryNow()
	s.kick()
}

// ----- uploads -----

func (s *syncer) uploadLoop() {
	for {
		e, wake := s.m.pending.next(time.Now())
		if e != nil {
			s.upload(e)
			continue
		}
		var timer <-chan time.Time
		if !wake.IsZero() {
			timer = time.After(time.Until(wake))
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.kicks:
		case <-timer:
		}
	}
}

// retryable reports whether an error is transient (offline, busy server,
// signed out, killed process) rather than a refusal of the request.
func retryable(err error) bool {
	switch pdcli.KindOf(err) { //nolint:exhaustive // everything else is a refusal
	case pdcli.KindNetwork, pdcli.KindRateLimited, pdcli.KindServer, pdcli.KindTimeout,
		pdcli.KindNotLoggedIn, pdcli.KindCancelled, pdcli.KindCrash, pdcli.KindUnknown:
		return true
	default:
		return false
	}
}

// upload sends one pending generation. If the remote file changed since the
// local edit started, the local content goes up as a conflict copy next to
// it, so neither version is lost.
func (s *syncer) upload(e *pendingEntry) {
	m := s.m
	ctx := s.ctx
	content := e.content(m.pending.dir)
	fail := func(err error) {
		next := time.Now().Add(retryDelay(m.opts.RetryBase, e.Attempts+1))
		m.pending.failed(e.UID, err, next)
		m.log.Warn("upload failed; will retry", "uid", e.UID, "name", e.Name, "attempt", e.Attempts+1, "next", next.Format(time.Kitchen), "err", err)
		s.kick() // the loop may be waiting without a timer
	}
	if err := m.acquire(ctx, m.reads); err != nil {
		m.pending.failed(e.UID, err, time.Now())
		return
	}
	nodes, err := m.opts.Backend.List(ctx, e.Parent)
	m.release(m.reads)
	if err != nil {
		fail(err)
		return
	}
	var remote *drive.Node
	for i := range nodes {
		if nodes[i].UID == e.UID {
			remote = &nodes[i]
			break
		}
	}
	name := e.Name
	strategy := pdcli.UploadFileNewRevision
	conflict := remote != nil && e.BaseRevision != "" && remote.RevisionUID != e.BaseRevision
	if conflict {
		name = conflictName(e.Name, time.Now())
		strategy = pdcli.UploadFileRename
		m.log.Warn("remote file changed meanwhile; uploading a conflict copy", "uid", e.UID, "name", e.Name, "copy", name)
	}
	// The CLI names the remote file after the local one.
	dir, err := os.MkdirTemp(filepath.Join(m.opts.CacheDir, "stage"), "u-")
	if err != nil {
		fail(err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	local := filepath.Join(dir, name)
	if err := os.Link(content, local); err != nil {
		if err := copyFile(content, local); err != nil {
			fail(err)
			return
		}
	}
	if err := m.acquire(ctx, m.writes); err != nil {
		m.pending.failed(e.UID, err, time.Now())
		return
	}
	_, err = m.opts.Backend.Upload(ctx, pdcli.UploadSpec{
		LocalPaths: []string{local}, Parent: e.Parent,
		FileStrategy: strategy, FolderStrategy: pdcli.UploadFolderMerge,
	})
	m.release(m.writes)
	if err != nil {
		if !retryable(err) {
			m.log.Error("upload refused; keeping the local copy and retrying later", "uid", e.UID, "name", e.Name, "err", err)
		}
		fail(err)
		return
	}
	// Find the uploaded node to seed the cache and refresh the inode.
	if err := m.acquire(ctx, m.reads); err == nil {
		nodes, lerr := m.opts.Backend.List(ctx, e.Parent)
		m.release(m.reads)
		if lerr == nil {
			for _, c := range nodes {
				if c.UID == e.UID && !conflict || conflict && c.Name == name {
					m.cache.seed(c, content)
					if !conflict {
						m.refreshInode(c)
					}
				}
			}
		}
		m.invalidateParent(e.Parent)
	}
	if m.pending.done(e.UID, e.Gen) {
		m.log.Info("uploaded", "uid", e.UID, "name", name, "conflict", conflict)
	} else {
		m.log.Info("uploaded; newer local change still pending", "uid", e.UID, "name", name)
	}
}

// refreshInode updates the live inode of c, if the kernel knows it.
func (m *Mounted) refreshInode(c drive.Node) {
	m.inodesMu.Lock()
	n := m.inodes[c.UID]
	m.inodesMu.Unlock()
	if n == nil {
		return
	}
	n.mu.Lock()
	n.attr = c // an open write handle reports the staging size anyway
	n.mu.Unlock()
	// Keep the parent's cached listing in step, so that an offline fallback
	// to it does not bring the old revision back.
	if _, p := n.Parent(); p != nil {
		pn := p.Operations().(*node)
		pn.mu.Lock()
		// Copy on write: readers iterate the old slice without the lock.
		entries := make([]drive.Node, len(pn.entries))
		copy(entries, pn.entries)
		for i := range entries {
			if entries[i].UID == c.UID {
				entries[i] = c
			}
		}
		pn.entries = entries
		pn.mu.Unlock()
	}
}

// invalidateParent drops the cached listing of the folder at path.
func (m *Mounted) invalidateParent(path string) {
	m.inodesMu.Lock()
	defer m.inodesMu.Unlock()
	for _, n := range m.inodes {
		n.mu.Lock()
		if n.isDir() && n.path == path {
			n.listed = time.Time{}
		}
		n.mu.Unlock()
	}
}

// ----- pins -----

func (s *syncer) pinLoop() {
	ticker := time.NewTicker(s.m.opts.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		case <-s.pinKicks:
		}
		s.pass()
	}
}

// pass makes sure every pinned file has its current content on this
// device, walking pinned folders. Downloads go through the read semaphore
// like any open, so a program's reads are never starved.
func (s *syncer) pass() {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	var first error
	seen := map[string]bool{}
	for _, p := range s.m.pins.list() {
		if s.ctx.Err() != nil {
			break
		}
		if err := s.ensure(p, seen); err != nil && first == nil {
			first = err
		}
	}
	if s.ctx.Err() == nil {
		s.m.inodesMu.Lock()
		s.m.protected = seen
		s.m.inodesMu.Unlock()
	}
	s.mu.Lock()
	s.running = false
	s.lastSync = time.Now()
	s.lastErr = ""
	if first != nil {
		s.lastErr = first.Error()
	}
	s.mu.Unlock()
}

func (s *syncer) ensure(p pin, seen map[string]bool) error {
	m := s.m
	if !p.Folder {
		n, err := m.info(s.ctx, p.Path)
		if err != nil {
			return err
		}
		seen[safeSegment(n.UID)] = true
		return s.fetch(n, p.Path)
	}
	return s.walk(p.Path, 0, seen)
}

const maxPinDepth = 64

func (s *syncer) walk(path string, depth int, seen map[string]bool) error {
	if depth > maxPinDepth {
		return errors.New("folder too deep")
	}
	m := s.m
	if err := m.acquire(s.ctx, m.reads); err != nil {
		return err
	}
	nodes, err := m.opts.Backend.List(s.ctx, path)
	m.release(m.reads)
	if err != nil {
		return err
	}
	var first error
	for _, c := range nodes {
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		childPath := m.childPathOf(path, c)
		var cerr error
		if c.Type == drive.TypeFolder {
			cerr = s.walk(childPath, depth+1, seen)
		} else {
			seen[safeSegment(c.UID)] = true
			cerr = s.fetch(c, childPath)
		}
		if cerr != nil && first == nil {
			first = cerr
		}
	}
	return first
}

// fetch downloads c unless it is cached or has a pending local change.
func (s *syncer) fetch(c drive.Node, path string) error {
	m := s.m
	if m.pending.get(c.UID) != nil || m.cache.has(c) {
		return nil
	}
	m.refreshInode(c)
	if _, errno := m.cache.fetch(s.ctx, m, c, path); errno != 0 {
		return errors.New("download " + c.Name + ": " + errno.Error())
	}
	return nil
}

// info returns the metadata of a file at a CLI path.
func (m *Mounted) info(ctx context.Context, path string) (drive.Node, error) {
	if err := m.acquire(ctx, m.reads); err != nil {
		return drive.Node{}, err
	}
	defer m.release(m.reads)
	return m.opts.Backend.Info(ctx, path)
}

// childPathOf is childPath without an inode.
func (m *Mounted) childPathOf(parent string, c drive.Node) string {
	if m.opts.AddressByName {
		return parent + "/" + drive.EscapeSegment(c.Name)
	}
	return drive.ByUID(c.UID)
}
