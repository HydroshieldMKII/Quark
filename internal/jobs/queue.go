// Package jobs runs uploads and downloads: a persistent FIFO queue with a
// concurrency cap, cancellation, safe automatic retries and progress for
// single-file downloads. Like internal/app it has no GTK dependency and does
// all state changes on the Dispatcher's thread.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/fsutil"
	"example.org/quark/internal/pdcli"
)

// Dispatcher runs functions on the UI thread (see app.Dispatcher).
type Dispatcher interface{ Dispatch(fn func()) }

// Runner performs transfers (implemented by *pdcli.Client).
type Runner interface {
	Upload(ctx context.Context, s pdcli.UploadSpec) (drive.TransferSummary, error)
	Download(ctx context.Context, s pdcli.DownloadSpec) (drive.TransferSummary, error)
}

// Kind is the direction of a transfer.
type Kind string

// Kinds.
const (
	Upload   Kind = "upload"
	Download Kind = "download"
)

// Status is the state of a transfer.
type Status string

// Statuses.
const (
	Queued    Status = "queued"
	Running   Status = "running"
	Done      Status = "done"
	Partial   Status = "partial" // finished, but some items failed
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

// Transfer is one upload or download.
type Transfer struct {
	ID   string `json:"id"`
	Kind Kind   `json:"kind"`
	// Name is the display name of the file or folder.
	Name string `json:"name"`
	// Local: for uploads the source path, for downloads the destination folder.
	Local string `json:"local"`
	// Remote: for uploads the destination folder path, for downloads the node path.
	Remote string `json:"remote"`
	// RemoteLabel is a human-readable remote location ("My files › Docs").
	RemoteLabel    string `json:"remoteLabel,omitempty"`
	IsFolder       bool   `json:"isFolder"`
	Size           int64  `json:"size,omitempty"`
	FileStrategy   string `json:"fileStrategy"`
	FolderStrategy string `json:"folderStrategy"`
	SkipThumbnails bool   `json:"skipThumbnails,omitempty"`

	Status    Status                 `json:"status"`
	Attempts  int                    `json:"attempts"`
	Summary   *drive.TransferSummary `json:"summary,omitempty"`
	Error     string                 `json:"error,omitempty"`
	ErrorKind string                 `json:"errorKind,omitempty"`
	Created   time.Time              `json:"created"`
	Started   time.Time              `json:"started,omitzero"`
	Finished  time.Time              `json:"finished,omitzero"`
	// NotBefore delays an automatic retry.
	NotBefore time.Time `json:"notBefore,omitzero"`
	// PartialFile is a file this transfer created and has not finished; it is
	// removed before a retry or resume, and on failure or cancellation.
	PartialFile string `json:"partialFile,omitempty"`
	// Progress is the number of bytes written so far (single-file downloads only).
	Progress int64 `json:"-"`
}

// Active reports whether the transfer is queued or running.
func (t Transfer) Active() bool { return t.Status == Queued || t.Status == Running }

// Fraction returns progress in [0,1] when it is known.
func (t Transfer) Fraction() (float64, bool) {
	if t.Status == Done {
		return 1, true
	}
	if t.Status != Running || t.Size <= 0 || t.Progress <= 0 {
		return 0, false
	}
	return min(1, float64(t.Progress)/float64(t.Size)), true
}

// MaxAttempts bounds automatic retries, which happen only for error kinds
// where a later attempt may succeed, with growing delays.
const MaxAttempts = 3

var backoff = []time.Duration{2 * time.Second, 8 * time.Second, 30 * time.Second}

// maxHistory bounds how many finished transfers are kept.
const maxHistory = 100

// Config wires a Queue.
type Config struct {
	Dispatcher Dispatcher
	// Runner returns the current runner (it changes when the CLI changes);
	// nil means transfers cannot start yet.
	Runner func() Runner
	Logger *slog.Logger
	// StatePath is where the queue is persisted; empty disables persistence.
	StatePath   string
	Concurrency int
	Now         func() time.Time
	// After schedules f after d and returns a stop function (default time.AfterFunc).
	After func(d time.Duration, f func()) (stop func())
	// PollInterval is the download progress poll period (default 500 ms).
	PollInterval time.Duration
}

type entry struct {
	t          Transfer
	cancel     context.CancelFunc
	userCancel bool
	stopRetry  func()
}

// Queue is the transfer queue. All methods must be called on the UI thread.
type Queue struct {
	cfg        Config
	log        *slog.Logger
	items      []*entry
	running    int
	limit      int
	enabled    bool
	closed     bool
	seq        int
	onChange   []func()
	onFinished []func(Transfer)
	wg         sync.WaitGroup
	// ctx is the parent of every transfer; Shutdown cancels it.
	ctx  context.Context
	stop context.CancelFunc
}

// NewQueue creates an empty queue. Call Load to restore persisted transfers.
func NewQueue(cfg Config) *Queue {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.After == nil {
		cfg.After = func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		}
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	ctx, stop := context.WithCancel(context.Background())
	q := &Queue{cfg: cfg, log: cfg.Logger, enabled: true, ctx: ctx, stop: stop}
	q.limit = clampConcurrency(cfg.Concurrency)
	return q
}

func clampConcurrency(n int) int {
	if n < 1 {
		return 2
	}
	return min(n, 4)
}

// OnChange registers a listener for any change (including progress).
func (q *Queue) OnChange(fn func()) { q.onChange = append(q.onChange, fn) }

// OnFinished registers a listener for transfers that reach done, partial or failed.
func (q *Queue) OnFinished(fn func(Transfer)) { q.onFinished = append(q.onFinished, fn) }

func (q *Queue) changed() {
	for _, fn := range q.onChange {
		fn()
	}
}

// Items returns a snapshot, oldest first.
func (q *Queue) Items() []Transfer {
	out := make([]Transfer, len(q.items))
	for i, e := range q.items {
		out[i] = e.t
	}
	return out
}

// Counts returns the number of active and failed transfers (for badges).
func (q *Queue) Counts() (active, failed int) {
	for _, e := range q.items {
		switch {
		case e.t.Active():
			active++
		case e.t.Status == Failed || e.t.Status == Partial:
			failed++
		}
	}
	return active, failed
}

func (q *Queue) find(id string) *entry {
	for _, e := range q.items {
		if e.t.ID == id {
			return e
		}
	}
	return nil
}

// Add enqueues a transfer and returns its id.
func (q *Queue) Add(t Transfer) string {
	if q.closed {
		return ""
	}
	q.seq++
	t.ID = strconv.FormatInt(q.cfg.Now().UnixNano(), 36) + "-" + strconv.Itoa(q.seq)
	t.Status, t.Attempts, t.Created = Queued, 0, q.cfg.Now()
	t.Summary, t.Error, t.ErrorKind, t.Progress = nil, "", "", 0
	q.items = append(q.items, &entry{t: t})
	q.log.Info("transfer queued", "kind", string(t.Kind), "folder", t.IsFolder)
	q.persist()
	q.changed()
	q.pump()
	return t.ID
}

// Cancel stops a queued or running transfer.
func (q *Queue) Cancel(id string) {
	e := q.find(id)
	if e == nil || !e.t.Active() {
		return
	}
	if e.stopRetry != nil {
		e.stopRetry()
		e.stopRetry = nil
	}
	if e.t.Status == Running {
		e.userCancel = true
		e.cancel() // finish() records the result
		return
	}
	e.t.Status, e.t.Finished = Cancelled, q.cfg.Now()
	q.persist()
	q.changed()
}

// Retry re-queues a failed, partial or cancelled transfer.
func (q *Queue) Retry(id string) {
	e := q.find(id)
	if e == nil || e.t.Active() || e.t.Status == Done {
		return
	}
	e.t.Status, e.t.Attempts, e.t.NotBefore = Queued, 0, time.Time{}
	e.t.Error, e.t.ErrorKind, e.t.Summary, e.t.Finished = "", "", nil, time.Time{}
	q.persist()
	q.changed()
	q.pump()
}

// Remove forgets a finished transfer.
func (q *Queue) Remove(id string) {
	for i, e := range q.items {
		if e.t.ID == id && !e.t.Active() {
			q.items = append(q.items[:i], q.items[i+1:]...)
			q.persist()
			q.changed()
			return
		}
	}
}

// ClearFinished forgets all finished transfers.
func (q *Queue) ClearFinished() {
	kept := q.items[:0]
	for _, e := range q.items {
		if e.t.Active() {
			kept = append(kept, e)
		}
	}
	q.items = kept
	q.persist()
	q.changed()
}

// SetConcurrency changes the number of simultaneous transfers (1–4).
func (q *Queue) SetConcurrency(n int) {
	q.limit = clampConcurrency(n)
	q.pump()
}

// SetEnabled pauses (false) or resumes (true) starting new transfers, e.g.
// while signed out. Running transfers are not affected.
func (q *Queue) SetEnabled(on bool) {
	q.enabled = on
	q.pump()
}

func (q *Queue) pump() {
	if q.closed || !q.enabled || q.cfg.Runner == nil {
		return
	}
	now := q.cfg.Now()
	for q.running < q.limit {
		var next *entry
		for _, e := range q.items {
			if e.t.Status == Queued && !e.t.NotBefore.After(now) {
				next = e
				break
			}
		}
		if next == nil {
			return
		}
		r := q.cfg.Runner()
		if r == nil {
			return
		}
		q.start(next, r)
	}
}

func (q *Queue) start(e *entry, r Runner) {
	q.running++
	e.t.Status, e.t.Started, e.t.Progress, e.t.NotBefore = Running, q.cfg.Now(), 0, time.Time{}
	e.t.Attempts++
	e.userCancel = false
	ctx, cancel := context.WithCancel(q.ctx)
	e.cancel = cancel

	// A leftover partial file from an interrupted attempt is ours: remove it
	// so the CLI does not keep it or create a "(1)" copy next to it.
	q.removeCreated(e)
	t := e.t
	// Progress and cleanup are only possible for a single-file download whose
	// target did not exist before: then the file is certainly ours.
	var watch string
	if t.Kind == Download && !t.IsFolder && t.Name != "" {
		target := filepath.Join(t.Local, drive.LocalName(t.Name))
		if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
			watch = target
		}
	}

	q.persist()
	q.changed()
	q.log.Info("transfer started", "kind", string(t.Kind), "attempt", t.Attempts)

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		pollDone := make(chan struct{})
		var pollWG sync.WaitGroup
		if watch != "" {
			pollWG.Add(1)
			go q.poll(watch, e, pollDone, &pollWG)
		}
		sum, err := q.run(ctx, r, t)
		close(pollDone)
		pollWG.Wait()
		//nolint:contextcheck // UI-thread callback; transfers derive their context from q.ctx
		q.cfg.Dispatcher.Dispatch(func() { q.finish(e, watch, sum, err) })
	}()
}

func (q *Queue) run(ctx context.Context, r Runner, t Transfer) (sum drive.TransferSummary, err error) {
	defer func() {
		if p := recover(); p != nil {
			q.log.Error("panic in transfer", "panic", fmt.Sprint(p))
			err = &pdcli.Error{Kind: pdcli.KindUnknown, Message: "internal error"}
		}
	}()
	if t.Kind == Upload {
		return r.Upload(ctx, pdcli.UploadSpec{
			LocalPaths: []string{t.Local}, Parent: t.Remote,
			FileStrategy:   pdcli.UploadFileStrategy(t.FileStrategy),
			FolderStrategy: pdcli.UploadFolderStrategy(t.FolderStrategy),
			SkipThumbnails: t.SkipThumbnails,
		})
	}
	return r.Download(ctx, pdcli.DownloadSpec{
		Remotes: []string{t.Remote}, LocalDir: t.Local,
		FileStrategy:   pdcli.DownloadFileStrategy(t.FileStrategy),
		FolderStrategy: pdcli.DownloadFolderStrategy(t.FolderStrategy),
	})
}

// poll reports the size of a file being downloaded.
func (q *Queue) poll(path string, e *entry, done <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	tick := time.NewTicker(q.cfg.PollInterval)
	defer tick.Stop()
	var last int64 = -1
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			st, err := os.Lstat(path)
			if err != nil || !st.Mode().IsRegular() || st.Size() == last {
				continue
			}
			last = st.Size()
			size := last
			q.cfg.Dispatcher.Dispatch(func() {
				if q.closed || e.t.Status != Running {
					return
				}
				if e.t.PartialFile == "" {
					e.t.PartialFile = path
					q.persist() // remember it in case Quark crashes now
				}
				e.t.Progress = size
				q.changed()
			})
		}
	}
}

func (q *Queue) finish(e *entry, watch string, sum drive.TransferSummary, err error) {
	if q.closed {
		return
	}
	q.running--
	e.cancel()
	if watch != "" && e.t.PartialFile == "" {
		// The poller may not have seen it yet; it is ours if it exists now.
		if _, serr := os.Lstat(watch); serr == nil {
			e.t.PartialFile = watch
		}
	}
	kind := pdcli.KindOf(err)
	now := q.cfg.Now()
	switch {
	case err == nil:
		e.t.Status, e.t.Summary, e.t.PartialFile = Done, &sum, ""
	case kind == pdcli.KindPartial:
		e.t.Status, e.t.Summary, e.t.PartialFile = Partial, &sum, ""
	case kind == pdcli.KindCancelled:
		e.t.Status = Cancelled
		q.removeCreated(e)
	case kind.Retryable() && e.t.Attempts < MaxAttempts && q.retrySafe(e):
		q.removeCreated(e)
		delay := backoff[min(e.t.Attempts-1, len(backoff)-1)]
		e.t.Status, e.t.NotBefore = Queued, now.Add(delay)
		e.t.Error, e.t.ErrorKind = errMessage(err), kind.String()
		q.log.Warn("transfer will be retried", "kind", kind.String(), "attempt", e.t.Attempts, "delay", delay.String())
		e.stopRetry = q.cfg.After(delay, func() {
			q.cfg.Dispatcher.Dispatch(func() {
				e.stopRetry = nil
				q.pump()
			})
		})
	default:
		e.t.Status = Failed
		q.removeCreated(e)
	}
	if e.t.Status != Queued {
		e.t.Finished = now
		if err != nil && kind != pdcli.KindCancelled {
			e.t.Error, e.t.ErrorKind = errMessage(err), kind.String()
		}
	}
	q.log.Info("transfer finished", "status", string(e.t.Status), "kind", kind.String())
	q.trimHistory()
	q.persist()
	q.changed()
	if e.t.Status == Done || e.t.Status == Partial || e.t.Status == Failed {
		for _, fn := range q.onFinished {
			fn(e.t)
		}
	}
	q.pump()
}

// retrySafe reports whether re-running the transfer cannot duplicate or skip
// data: uploads skip identical content, single-file downloads remove their
// own partial file first. Folder downloads are never retried automatically.
func (q *Queue) retrySafe(e *entry) bool {
	if e.t.Kind == Upload {
		return true
	}
	return !e.t.IsFolder
}

func (q *Queue) removeCreated(e *entry) {
	if e.t.PartialFile == "" {
		return
	}
	if err := os.Remove(e.t.PartialFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		q.log.Warn("could not remove partial download", "err", err)
	}
	e.t.PartialFile = ""
	e.t.Progress = 0
}

func errMessage(err error) string {
	var pe *pdcli.Error
	if errors.As(err, &pe) {
		return pe.Message
	}
	return err.Error()
}

func (q *Queue) trimHistory() {
	finished := 0
	for _, e := range q.items {
		if !e.t.Active() {
			finished++
		}
	}
	for i := 0; finished > maxHistory && i < len(q.items); {
		if !q.items[i].t.Active() {
			q.items = append(q.items[:i], q.items[i+1:]...)
			finished--
			continue
		}
		i++
	}
}

// ----- persistence -----

type stateFile struct {
	Version int        `json:"version"`
	Items   []Transfer `json:"items"`
}

func (q *Queue) persist() {
	if q.cfg.StatePath == "" {
		return
	}
	st := stateFile{Version: 1, Items: q.Items()}
	for i := range st.Items {
		if st.Items[i].Status == Running {
			st.Items[i].Status = Queued // resumes after a crash
		}
	}
	data, err := json.Marshal(st)
	if err != nil {
		q.log.Error("encoding transfer queue", "err", err)
		return
	}
	if err := fsutil.WriteFileAtomic(q.cfg.StatePath, data, 0o600); err != nil {
		q.log.Error("saving transfer queue", "err", err)
	}
}

// Load restores persisted transfers. Transfers that were running when Quark
// stopped are queued again. A corrupt file is moved aside and reported.
func (q *Queue) Load() error {
	if q.cfg.StatePath == "" {
		return nil
	}
	data, err := os.ReadFile(q.cfg.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var st stateFile
	if err := json.Unmarshal(data, &st); err != nil || st.Version != 1 {
		aside := q.cfg.StatePath + ".corrupt-" + q.cfg.Now().UTC().Format("20060102T150405")
		_ = os.Rename(q.cfg.StatePath, aside)
		return fmt.Errorf("transfer queue was unreadable and has been reset (kept as %s)", aside)
	}
	for _, t := range st.Items {
		if t.ID == "" || (t.Kind != Upload && t.Kind != Download) {
			continue
		}
		if t.Status == Running {
			t.Status = Queued
		}
		t.Progress, t.NotBefore = 0, time.Time{}
		q.items = append(q.items, &entry{t: t})
	}
	q.changed()
	q.pump()
	return nil
}

// Shutdown stops all transfers, records the interrupted ones as queued (so
// they resume next time) and waits up to timeout for their processes to end.
func (q *Queue) Shutdown(timeout time.Duration) bool {
	if q.closed {
		return true
	}
	q.closed = true
	for _, e := range q.items {
		if e.stopRetry != nil {
			e.stopRetry()
		}
	}
	q.stop() // cancels every running transfer
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	ok := true
	select {
	case <-done:
		// The CLI processes are gone: their partial files can be removed now.
		for _, e := range q.items {
			if e.t.Status == Running {
				q.removeCreated(e)
			}
		}
	case <-time.After(timeout):
		ok = false // keep PartialFile recorded; it is removed before the resume
	}
	// persist() maps Running to Queued, so interrupted transfers resume.
	q.persist()
	return ok
}
