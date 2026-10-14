package fusefs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"example.org/quark/internal/fsutil"
)

// pendingEntry is a local change that has not reached Proton Drive yet. The
// content lives under <cache>/pending/<uid>/content-<gen>; meta.json makes
// it survive a restart. Nothing here is ever discarded until the upload
// succeeds (or a conflict copy of it does).
type pendingEntry struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	// Parent is the CLI path of the folder, stable across restarts.
	Parent string `json:"parent"`
	// BaseRevision is the revision the change was made from; a different
	// remote revision at upload time means someone else changed the file.
	BaseRevision string    `json:"baseRevision"`
	Gen          int       `json:"gen"`
	Size         int64     `json:"size"`
	Updated      time.Time `json:"updated"`
	Attempts     int       `json:"attempts"`
	LastError    string    `json:"lastError,omitempty"`
	// NextTry and uploading are runtime state.
	NextTry   time.Time `json:"-"`
	uploading bool
}

func (e *pendingEntry) dir(base string) string { return filepath.Join(base, safeSegment(e.UID)) }

func (e *pendingEntry) content(base string) string {
	return filepath.Join(e.dir(base), "content-"+strconv.Itoa(e.Gen))
}

// pendingStore is the journal of local changes awaiting upload.
type pendingStore struct {
	dir string
	mu  sync.Mutex
	// entries by uid.
	entries map[string]*pendingEntry
}

func newPendingStore(dir string) (*pendingStore, error) {
	if err := os.MkdirAll(dir, fsutil.DirPrivate); err != nil {
		return nil, err
	}
	p := &pendingStore{dir: dir, entries: map[string]*pendingEntry{}}
	return p, p.load()
}

// load restores the journal; entries without a readable meta or content
// are moved aside under <dir>/lost, never deleted.
func (p *pendingStore) load() error {
	dirs, err := os.ReadDir(p.dir)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "lost" {
			continue
		}
		full := filepath.Join(p.dir, d.Name())
		var e pendingEntry
		data, rerr := os.ReadFile(filepath.Join(full, "meta.json")) //nolint:gosec // our journal
		if rerr == nil {
			rerr = json.Unmarshal(data, &e)
		}
		if rerr == nil {
			if _, serr := os.Stat(e.content(p.dir)); serr != nil {
				rerr = serr
			}
		}
		if rerr != nil || e.UID == "" {
			lost := filepath.Join(p.dir, "lost")
			_ = os.MkdirAll(lost, fsutil.DirPrivate)
			_ = os.Rename(full, filepath.Join(lost, time.Now().Format("20060102-150405")+"-"+d.Name()))
			continue
		}
		p.entries[e.UID] = &e
	}
	return nil
}

func (p *pendingStore) save(e *pendingEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(e.dir(p.dir), "meta.json"), data, fsutil.FilePrivate)
}

// put records a new content file (moved from path) for uid. It replaces any
// earlier pending content that is not being uploaded right now; an upload in
// flight keeps its generation until it finishes.
func (p *pendingStore) put(uid, name, parent, baseRevision, path string) (*pendingEntry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[uid]
	if e == nil {
		e = &pendingEntry{UID: uid, Name: name, Parent: parent, BaseRevision: baseRevision}
		if err := os.MkdirAll(e.dir(p.dir), fsutil.DirPrivate); err != nil {
			return nil, err
		}
		p.entries[uid] = e
	}
	old := e.content(p.dir)
	e.Gen++
	e.Name, e.Parent = name, parent
	if err := os.Rename(path, e.content(p.dir)); err != nil {
		if cerr := copyFile(path, e.content(p.dir)); cerr != nil {
			e.Gen--
			return nil, cerr
		}
		_ = os.Remove(path)
	}
	if st, err := os.Stat(e.content(p.dir)); err == nil {
		e.Size = st.Size()
	}
	e.Updated = time.Now()
	e.Attempts, e.LastError, e.NextTry = 0, "", time.Time{}
	if err := p.save(e); err != nil {
		return nil, err
	}
	if e.Gen > 1 && !e.uploading {
		_ = os.Remove(old)
	}
	return e, nil
}

// get returns the entry for uid, if any.
func (p *pendingStore) get(uid string) *pendingEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.entries[uid]
}

// contentPath returns the path of the pending content for uid, or "".
func (p *pendingStore) contentPath(uid string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[uid]; e != nil {
		return e.content(p.dir)
	}
	return ""
}

// claim marks the entry as uploading if it is at gen and idle, returning a copy.
func (p *pendingStore) claim(uid string, gen int) *pendingEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[uid]
	if e == nil || e.uploading || e.Gen != gen {
		return nil
	}
	e.uploading = true
	cp := *e
	return &cp
}

// done removes the entry if gen is still its current generation; a newer
// generation written during the upload stays pending.
func (p *pendingStore) done(uid string, gen int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[uid]
	if e == nil {
		return true
	}
	e.uploading = false
	if e.Gen != gen {
		_ = os.Remove(filepath.Join(e.dir(p.dir), "content-"+strconv.Itoa(gen)))
		return false
	}
	delete(p.entries, uid)
	_ = os.RemoveAll(e.dir(p.dir))
	return true
}

// rename follows a rename or move of a node with a pending change.
func (p *pendingStore) rename(uid, name, parent string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[uid]
	if e == nil || e.Name == name && e.Parent == parent {
		return
	}
	e.Name, e.Parent = name, parent
	_ = p.save(e)
}

// failed records a failed attempt and schedules the next one.
func (p *pendingStore) failed(uid string, err error, next time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[uid]
	if e == nil {
		return
	}
	e.uploading = false
	e.Attempts++
	e.LastError = err.Error()
	e.NextTry = next
	_ = p.save(e)
}

// retryNow clears every scheduled delay.
func (p *pendingStore) retryNow() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		e.NextTry = time.Time{}
	}
}

// next returns the entry that should be uploaded now, marked as uploading,
// or nil and the time of the earliest scheduled retry (zero if none).
func (p *pendingStore) next(now time.Time) (*pendingEntry, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var (
		best     *pendingEntry
		earliest time.Time
	)
	for _, e := range p.entries {
		if e.uploading {
			continue
		}
		if e.NextTry.After(now) {
			if earliest.IsZero() || e.NextTry.Before(earliest) {
				earliest = e.NextTry
			}
			continue
		}
		if best == nil || e.Updated.Before(best.Updated) {
			best = e
		}
	}
	if best != nil {
		best.uploading = true
		cp := *best
		return &cp, time.Time{}
	}
	return nil, earliest
}

// stats summarises the journal.
func (p *pendingStore) stats() (count, errors int, bytes int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.entries {
		count++
		bytes += e.Size
		if e.LastError != "" {
			errors++
		}
	}
	return count, errors, bytes
}

// list returns the entries sorted by age (oldest first).
func (p *pendingStore) list() []pendingEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]pendingEntry, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.Before(out[j].Updated) })
	return out
}

// retryDelay grows with the number of failed attempts, capped.
func retryDelay(base time.Duration, attempts int) time.Duration {
	d := base
	for i := 1; i < attempts && d < 5*time.Minute; i++ {
		d *= 3
	}
	return min(d, 5*time.Minute)
}

// conflictName returns "<stem> (conflict 2026-10-03 14-05)<ext>".
func conflictName(name string, at time.Time) string {
	ext := filepath.Ext(name)
	stem := name[:len(name)-len(ext)]
	if stem == "" {
		stem, ext = name, ""
	}
	return fmt.Sprintf("%s (conflict %s)%s", stem, at.Format("2006-01-02 15-04"), ext)
}
