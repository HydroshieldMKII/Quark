package fusefs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

// contentCache holds downloaded file contents under
// <dir>/files/<uid>/<revision>/<local name>. A file is valid for exactly one
// revision; a new version downloads again. Least recently used files are
// evicted when the total exceeds the limit, except files that are open.
type contentCache struct {
	dir   string
	limit int64
	log   *slog.Logger
	// keep reports whether a UID is pinned (never evicted).
	keep func(uid string) bool

	mu       sync.Mutex
	pins     map[string]int         // safe uid -> open handles
	locks    map[string]*sync.Mutex // uid -> single-flight download lock
	fetching map[string]bool        // uid -> download in progress
}

func newContentCache(dir string, limit int64, log *slog.Logger, keep func(uid string) bool) (*contentCache, error) {
	for _, sub := range []string{"files", "stage", "unsynced", "tmp", "listings"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	// Staging and temp files from a previous run are leftovers.
	_ = os.RemoveAll(filepath.Join(dir, "stage"))
	_ = os.RemoveAll(filepath.Join(dir, "tmp"))
	_ = os.MkdirAll(filepath.Join(dir, "stage"), 0o700)
	_ = os.MkdirAll(filepath.Join(dir, "tmp"), 0o700)
	if keep == nil {
		keep = func(string) bool { return false }
	}
	return &contentCache{dir: dir, limit: limit, log: log, keep: keep, pins: map[string]int{}, locks: map[string]*sync.Mutex{}, fetching: map[string]bool{}}, nil
}

// has reports whether n's current revision is cached.
func (c *contentCache) has(n drive.Node) bool {
	_, err := os.Stat(c.pathFor(n))
	return err == nil
}

// inflight reports whether uid is being downloaded.
func (c *contentCache) inflight(uid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetching[uid]
}

func (c *contentCache) inflightCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.fetching)
}

// size returns the bytes held under files.
func (c *contentCache) size() int64 {
	var total int64
	_ = filepath.WalkDir(filepath.Join(c.dir, "files"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func (c *contentCache) pathFor(n drive.Node) string {
	rev := n.RevisionUID
	if rev == "" {
		rev = "r-" + n.SHA1
	}
	return filepath.Join(c.dir, "files", safeSegment(n.UID), safeSegment(rev), drive.LocalName(n.Name))
}

// safeSegment makes an identifier usable as a directory name. Proton's
// node and revision identifiers can exceed a file name's 255-byte limit,
// so long ones are replaced by a digest.
func safeSegment(s string) string {
	if s == "" {
		return "_"
	}
	if len(s) > 64 {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:24])
	}
	out := []byte(s)
	for i, b := range out {
		if b == '/' || b == 0 {
			out[i] = '_'
		}
	}
	if string(out) == "." || string(out) == ".." {
		return "_" + string(out)
	}
	return string(out)
}

func (c *contentCache) lock(uid string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.locks[uid]
	if l == nil {
		l = &sync.Mutex{}
		c.locks[uid] = l
	}
	return l
}

// fetch returns the local path of n's content, downloading it if needed.
func (c *contentCache) fetch(ctx context.Context, m *Mounted, n drive.Node, remotePath string) (string, syscall.Errno) {
	l := c.lock(n.UID)
	l.Lock()
	defer l.Unlock()
	path := c.pathFor(n)
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		_ = os.Chtimes(path, now, now) // LRU touch
		return path, 0
	}
	c.mu.Lock()
	c.fetching[n.UID] = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.fetching, n.UID)
		c.mu.Unlock()
	}()
	if err := m.acquire(ctx, m.xfers); err != nil {
		return "", syscall.EINTR
	}
	defer m.release(m.xfers)
	tmp, err := os.MkdirTemp(filepath.Join(c.dir, "tmp"), "dl-")
	if err != nil {
		return "", syscall.EIO
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	sum, err := m.opts.Backend.Download(ctx, pdcli.DownloadSpec{
		Remotes: []string{remotePath}, LocalDir: tmp,
		FileStrategy: pdcli.DownloadFileRename, FolderStrategy: pdcli.DownloadFolderMerge,
	})
	if err != nil {
		c.log.Warn("download failed", "uid", n.UID, "err", err)
		return "", toErrno(err)
	}
	got := filepath.Join(tmp, drive.LocalName(n.Name))
	if _, err := os.Stat(got); err != nil {
		var found []string
		if entries, rerr := os.ReadDir(tmp); rerr == nil {
			for _, e := range entries {
				found = append(found, e.Name())
			}
		}
		c.log.Error("downloaded file not where expected", "uid", n.UID, "transferred", sum.TransferredItems,
			"skipped", sum.SkippedItems, "failed", sum.FailedItems, "found", len(found), "err", err)
		return "", syscall.EIO
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		c.log.Error("cache directory", "err", err)
		return "", syscall.EIO
	}
	if err := os.Rename(got, path); err != nil {
		c.log.Error("moving download into the cache", "err", err)
		return "", syscall.EIO
	}
	// Older revisions of the same file are now useless.
	c.dropOtherRevisions(n, filepath.Dir(path))
	c.evict()
	return path, 0
}

// seed stores a freshly uploaded staging copy as the content of n.
func (c *contentCache) seed(n drive.Node, from string) {
	l := c.lock(n.UID)
	l.Lock()
	defer l.Unlock()
	path := c.pathFor(n)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		c.log.Error("cache directory", "err", err)
		return
	}
	if err := copyFile(from, path); err != nil {
		c.log.Error("seeding the cache", "err", err)
		_ = os.Remove(path)
		return
	}
	c.dropOtherRevisions(n, filepath.Dir(path))
}

func (c *contentCache) dropOtherRevisions(n drive.Node, keep string) {
	base := filepath.Join(c.dir, "files", safeSegment(n.UID))
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if p := filepath.Join(base, e.Name()); p != keep {
			_ = os.RemoveAll(p)
		}
	}
}

// forget drops everything cached for a UID (after trash or eviction). It
// reports false when the content is open.
func (c *contentCache) forget(uid string) bool {
	c.mu.Lock()
	open := c.pins[safeSegment(uid)] > 0
	c.mu.Unlock()
	if open {
		return false
	}
	_ = os.RemoveAll(filepath.Join(c.dir, "files", safeSegment(uid)))
	return true
}

func (c *contentCache) pin(uid string, delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := safeSegment(uid)
	c.pins[key] += delta
	if c.pins[key] <= 0 {
		delete(c.pins, key)
	}
}

// keepUnsynced moves a local copy that cannot be uploaded to <dir>/unsynced.
func (c *contentCache) keepUnsynced(path, name string) string {
	dst := filepath.Join(c.dir, "unsynced", time.Now().Format("20060102-150405")+"-"+drive.LocalName(name))
	if err := os.Rename(path, dst); err != nil {
		if err := copyFile(path, dst); err != nil {
			return ""
		}
	}
	return dst
}

type cached struct {
	path string
	uid  string
	size int64
	used time.Time
}

// evict removes least recently used files until the cache fits the limit.
func (c *contentCache) evict() {
	base := filepath.Join(c.dir, "files")
	var (
		files []cached
		total int64
	)
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // skip unreadable entries, keep walking
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil //nolint:nilerr // same
		}
		rel, _ := filepath.Rel(base, path)
		uid := filepath.SplitList(rel)[0]
		if i := indexSep(rel); i > 0 {
			uid = rel[:i]
		}
		files = append(files, cached{path: path, uid: uid, size: info.Size(), used: info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= c.limit {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].used.Before(files[j].used) })
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range files {
		if total <= c.limit {
			break
		}
		if c.pins[f.uid] > 0 || c.keep(f.uid) {
			continue
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
			_ = os.Remove(filepath.Dir(f.path))
			_ = os.Remove(filepath.Dir(filepath.Dir(f.path)))
		}
	}
}

func indexSep(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == filepath.Separator {
			return i
		}
	}
	return -1
}
