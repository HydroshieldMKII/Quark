package fusefs

import (
	"encoding/json"
	"os"
	"sync"

	"example.org/quark/internal/fsutil"
)

// pin is a file or folder the user wants kept on this device.
type pin struct {
	UID    string `json:"uid"`
	Name   string `json:"name"`
	Folder bool   `json:"folder"`
	// Path addresses the node for the CLI, so pinned folders can be walked
	// without an inode (after a restart, for instance).
	Path string `json:"path"`
}

type pinFile struct {
	Version int   `json:"version"`
	Pins    []pin `json:"pins"`
}

// pinStore persists pins at <cache>/pins.json.
type pinStore struct {
	path string
	mu   sync.Mutex
	pins map[string]pin
}

func newPinStore(path string) *pinStore {
	s := &pinStore{path: path, pins: map[string]pin{}}
	data, err := os.ReadFile(path) //nolint:gosec // our own file
	if err != nil {
		return s
	}
	var f pinFile
	if json.Unmarshal(data, &f) != nil || f.Version != 1 {
		_ = os.Rename(path, path+".corrupt")
		return s
	}
	for _, p := range f.Pins {
		if p.UID != "" {
			s.pins[p.UID] = p
		}
	}
	return s
}

func (s *pinStore) save() error {
	f := pinFile{Version: 1}
	for _, p := range s.pins {
		f.Pins = append(f.Pins, p)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.path, data, fsutil.FilePrivate)
}

func (s *pinStore) set(p pin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pins[p.UID] = p
	return s.save()
}

func (s *pinStore) unset(uid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pins[uid]; !ok {
		return nil
	}
	delete(s.pins, uid)
	return s.save()
}

// has reports whether uid itself is pinned.
func (s *pinStore) has(uid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pins[uid]
	return ok
}

// hasSegment is has for a cache directory segment.
func (s *pinStore) hasSegment(seg string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for uid := range s.pins {
		if safeSegment(uid) == seg {
			return true
		}
	}
	return false
}

// update refreshes the name and path of a pinned node after a rename or move.
func (s *pinStore) update(uid, name, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pins[uid]
	if !ok || p.Name == name && p.Path == path {
		return
	}
	p.Name, p.Path = name, path
	s.pins[uid] = p
	_ = s.save()
}

func (s *pinStore) list() []pin {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]pin, 0, len(s.pins))
	for _, p := range s.pins {
		out = append(out, p)
	}
	return out
}

func (s *pinStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pins)
}
