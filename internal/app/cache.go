package app

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/fsutil"
)

// listCache keeps folder listings so navigating back is instant and the
// last session's folders render at once on the next start. Entries past
// the TTL are "stale": shown immediately, then refreshed in the background.
//
// Safety: cached data is only ever used for display and for addressing
// nodes by their stable UID. Operations that must address by name (trash,
// shared roots) re-list from the server before acting (see ops.go), so a
// stale cache can never redirect an action to the wrong item.
//
// It is only touched on the UI thread except for the background save.
type listCache struct {
	now     func() time.Time
	ttl     time.Duration
	entries map[string]cacheEntry
	path    string // persisted file; empty disables persistence
	saveMu  sync.Mutex
	dirty   bool
}

type cacheEntry struct {
	Nodes []drive.Node `json:"nodes"`
	At    time.Time    `json:"at"`
}

type cacheFile struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

const (
	maxCacheEntries = 64
	// maxCacheAge bounds how old a persisted listing may be to be shown at all.
	maxCacheAge  = 7 * 24 * time.Hour
	cacheVersion = 1
)

func newListCache(now func() time.Time, ttl time.Duration) *listCache {
	return &listCache{now: now, ttl: ttl, entries: map[string]cacheEntry{}}
}

// get returns the cached nodes and whether they are fresh (within the TTL).
// Entries older than maxCacheAge are not returned.
func (c *listCache) get(path string) (nodes []drive.Node, fresh, ok bool) {
	e, found := c.entries[path]
	if !found {
		return nil, false, false
	}
	age := c.now().Sub(e.At)
	if age > maxCacheAge || age < 0 {
		delete(c.entries, path)
		return nil, false, false
	}
	return e.Nodes, age <= c.ttl, true
}

func (c *listCache) put(path string, nodes []drive.Node) {
	if len(c.entries) >= maxCacheEntries {
		var (
			oldest string
			at     time.Time
		)
		for p, e := range c.entries {
			if oldest == "" || e.At.Before(at) {
				oldest, at = p, e.At
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[path] = cacheEntry{Nodes: nodes, At: c.now()}
	c.dirty = true
}

func (c *listCache) invalidate(paths ...string) {
	for _, p := range paths {
		if e, ok := c.entries[p]; ok {
			// Keep the nodes for instant display but mark them stale.
			e.At = time.Time{}
			c.entries[p] = e
			c.dirty = true
		}
	}
}

func (c *listCache) clear() {
	c.entries = map[string]cacheEntry{}
	c.dirty = true
	if c.path != "" {
		_ = os.Remove(c.path)
	}
}

// load reads the persisted cache; a corrupt file is ignored (and replaced
// on the next save).
func (c *listCache) load(path string) {
	c.path = path
	data, err := os.ReadFile(path) //nolint:gosec // our own cache file
	if err != nil {
		return
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != cacheVersion {
		return
	}
	now := c.now()
	for p, e := range f.Entries {
		if age := now.Sub(e.At); age >= 0 && age <= maxCacheAge && len(c.entries) < maxCacheEntries {
			c.entries[p] = e
		}
	}
}

// snapshot returns the bytes to persist, or nil when nothing changed.
func (c *listCache) snapshot() []byte {
	if c.path == "" || !c.dirty {
		return nil
	}
	c.dirty = false
	data, err := json.Marshal(cacheFile{Version: cacheVersion, Entries: c.entries})
	if err != nil {
		return nil
	}
	return data
}

// save writes a snapshot atomically. It may run off the UI thread.
func (c *listCache) save(data []byte) error {
	if data == nil || c.path == "" {
		return nil
	}
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	return fsutil.WriteFileAtomic(c.path, data, fsutil.FilePrivate)
}
