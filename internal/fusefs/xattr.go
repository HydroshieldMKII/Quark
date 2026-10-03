package fusefs

import (
	"context"
	"encoding/json"
	"strings"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
)

// Extended attributes are the control surface of the mount, usable from any
// program (the GNOME Files extension, setfattr, the quark-mount command):
//
//	user.quark.state   read: cloud | cached | pinned | downloading | pending | uploading | error
//	user.quark.pin     read "1"/"0"; write "1" keeps the file or folder on this device, "0" releases it
//	user.quark.evict   write "1" drops the local copy (refused while open or pending)
//	user.quark.error   read: last upload error of a pending change, if any
//	user.quark.status  read on the mount root: JSON summary (see Status)
//	user.quark.sync    write "1" on the mount root: run a sync pass now
const (
	xaState  = "user.quark.state"
	xaPin    = "user.quark.pin"
	xaEvict  = "user.quark.evict"
	xaError  = "user.quark.error"
	xaStatus = "user.quark.status"
	xaSync   = "user.quark.sync"
)

// State values of user.quark.state.
const (
	StateCloud       = "cloud"
	StateCached      = "cached"
	StatePinned      = "pinned"
	StateDownloading = "downloading"
	StatePending     = "pending"
	StateUploading   = "uploading"
	StateError       = "error"
)

var (
	_ fs.NodeGetxattrer    = (*node)(nil)
	_ fs.NodeSetxattrer    = (*node)(nil)
	_ fs.NodeListxattrer   = (*node)(nil)
	_ fs.NodeRemovexattrer = (*node)(nil)
)

// pinned reports whether the node or one of its ancestors is pinned.
func (n *node) pinned() bool {
	if n.m.pins.has(n.uid) {
		return true
	}
	for _, p := n.Parent(); p != nil; _, p = p.Parent() {
		if n.m.pins.has(p.Operations().(*node).uid) {
			return true
		}
	}
	return false
}

// state computes user.quark.state.
func (n *node) state() string {
	n.mu.Lock()
	attr := n.attr
	n.mu.Unlock()
	if n.isDir() {
		if n.pinned() {
			return StatePinned
		}
		return StateCloud
	}
	if e := n.m.pending.get(attr.UID); e != nil {
		switch {
		case e.uploading:
			return StateUploading
		case e.LastError != "":
			return StateError
		}
		return StatePending
	}
	if n.m.cache.inflight(attr.UID) {
		return StateDownloading
	}
	if n.m.cache.has(attr) {
		if n.pinned() {
			return StatePinned
		}
		return StateCached
	}
	return StateCloud
}

func xattrReply(dest []byte, value string) (uint32, syscall.Errno) {
	if len(dest) == 0 {
		return uint32(len(value)), 0 //nolint:gosec // short strings
	}
	if len(dest) < len(value) {
		return 0, syscall.ERANGE
	}
	copy(dest, value)
	return uint32(len(value)), 0 //nolint:gosec // short strings
}

// Getxattr implements fs.NodeGetxattrer.
func (n *node) Getxattr(_ context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	switch attr {
	case xaState:
		return xattrReply(dest, n.state())
	case xaPin:
		if n.m.pins.has(n.uid) {
			return xattrReply(dest, "1")
		}
		return xattrReply(dest, "0")
	case xaError:
		if e := n.m.pending.get(n.uid); e != nil && e.LastError != "" {
			return xattrReply(dest, e.LastError)
		}
		return 0, syscall.ENODATA
	case xaStatus:
		if n != n.m.root {
			return 0, syscall.ENODATA
		}
		data, err := json.Marshal(n.m.Status())
		if err != nil {
			return 0, syscall.EIO
		}
		return xattrReply(dest, string(data))
	}
	return 0, syscall.ENODATA
}

// Setxattr implements fs.NodeSetxattrer.
func (n *node) Setxattr(ctx context.Context, attr string, data []byte, _ uint32) syscall.Errno {
	value := strings.TrimSpace(string(data))
	switch attr {
	case xaPin:
		switch value {
		case "1", "true", "yes":
			return n.setPin(ctx, true)
		case "0", "false", "no":
			return n.setPin(ctx, false)
		}
		return syscall.EINVAL
	case xaEvict:
		if value != "1" {
			return syscall.EINVAL
		}
		return n.evict()
	case xaSync:
		if n != n.m.root {
			return syscall.ENODATA
		}
		n.m.syncer.syncNow()
		return 0
	case xaState, xaError, xaStatus:
		return syscall.EACCES
	}
	// Other attributes are not stored: Proton Drive has nowhere to put them.
	return syscall.ENOTSUP
}

func (n *node) setPin(_ context.Context, on bool) syscall.Errno {
	n.mu.Lock()
	attr, path := n.attr, n.path
	n.mu.Unlock()
	if n == n.m.root {
		path = n.m.opts.Root
	}
	var err error
	if on {
		err = n.m.pins.set(pin{UID: attr.UID, Name: attr.Name, Folder: n.isDir(), Path: path})
	} else {
		err = n.m.pins.unset(attr.UID)
	}
	if err != nil {
		n.m.log.Error("saving pins", "err", err)
		return syscall.EIO
	}
	n.m.log.Info("pin changed", "uid", attr.UID, "name", attr.Name, "pinned", on)
	if on {
		n.m.syncer.kick()
	}
	return 0
}

// evict drops the local copy of a file that is neither open nor pending.
func (n *node) evict() syscall.Errno {
	if n.isDir() {
		return syscall.EISDIR
	}
	n.mu.Lock()
	attr := n.attr
	writers := n.writers
	n.mu.Unlock()
	if writers > 0 || n.m.pending.get(attr.UID) != nil {
		return syscall.EBUSY
	}
	if !n.m.cache.forget(attr.UID) {
		return syscall.EBUSY
	}
	return 0
}

// Listxattr implements fs.NodeListxattrer.
func (n *node) Listxattr(_ context.Context, dest []byte) (uint32, syscall.Errno) {
	names := []string{xaState, xaPin}
	if n == n.m.root {
		names = append(names, xaStatus)
	}
	var b strings.Builder
	for _, s := range names {
		b.WriteString(s)
		b.WriteByte(0)
	}
	return xattrReply(dest, b.String())
}

// Removexattr implements fs.NodeRemovexattrer: removing the pin attribute unpins.
func (n *node) Removexattr(ctx context.Context, attr string) syscall.Errno {
	if attr == xaPin {
		return n.setPin(ctx, false)
	}
	return syscall.ENODATA
}

// Status is the JSON summary exposed as user.quark.status on the mount root.
type Status struct {
	Root         string `json:"root"`
	ReadOnly     bool   `json:"readOnly"`
	Pinned       int    `json:"pinned"`
	Pending      int    `json:"pending"`
	PendingBytes int64  `json:"pendingBytes"`
	Errors       int    `json:"errors"`
	Downloading  int    `json:"downloading"`
	CachedBytes  int64  `json:"cachedBytes"`
	CacheLimit   int64  `json:"cacheLimit"`
	LastSync     string `json:"lastSync,omitempty"`
	LastSyncErr  string `json:"lastSyncError,omitempty"`
	Syncing      bool   `json:"syncing"`
}

// Status returns the current summary.
func (m *Mounted) Status() Status {
	s := Status{Root: m.opts.Root, ReadOnly: m.opts.ReadOnly, CacheLimit: m.opts.CacheLimit}
	s.Pinned = m.pins.count()
	s.Pending, s.Errors, s.PendingBytes = m.pending.stats()
	s.Downloading = m.cache.inflightCount()
	s.CachedBytes = m.cache.size()
	m.syncer.mu.Lock()
	if !m.syncer.lastSync.IsZero() {
		s.LastSync = m.syncer.lastSync.UTC().Format("2006-01-02T15:04:05Z")
	}
	s.LastSyncErr = m.syncer.lastErr
	s.Syncing = m.syncer.running
	m.syncer.mu.Unlock()
	return s
}
