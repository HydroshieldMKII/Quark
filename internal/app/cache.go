package app

import (
	"time"

	"example.org/quark/internal/drive"
)

// listCache keeps recent folder listings so navigating back is instant.
// It is only touched on the UI thread.
type listCache struct {
	now     func() time.Time
	ttl     time.Duration
	entries map[string]cacheEntry
}

type cacheEntry struct {
	nodes []drive.Node
	at    time.Time
}

const maxCacheEntries = 64

func newListCache(now func() time.Time, ttl time.Duration) *listCache {
	return &listCache{now: now, ttl: ttl, entries: map[string]cacheEntry{}}
}

func (c *listCache) get(path string) ([]drive.Node, bool) {
	e, ok := c.entries[path]
	if !ok || c.now().Sub(e.at) > c.ttl {
		return nil, false
	}
	return e.nodes, true
}

func (c *listCache) put(path string, nodes []drive.Node) {
	if len(c.entries) >= maxCacheEntries {
		// Evict the oldest entry.
		var oldest string
		var at time.Time
		for p, e := range c.entries {
			if oldest == "" || e.at.Before(at) {
				oldest, at = p, e.at
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[path] = cacheEntry{nodes: nodes, at: c.now()}
}

func (c *listCache) invalidate(paths ...string) {
	for _, p := range paths {
		delete(c.entries, p)
	}
}

func (c *listCache) clear() { c.entries = map[string]cacheEntry{} }
