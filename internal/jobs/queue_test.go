package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"example.org/quark/internal/drive"
	"example.org/quark/internal/pdcli"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// loop is a deterministic dispatcher (the jobs package cannot import app).
type loop struct {
	mu sync.Mutex
	q  []func()
}

func (l *loop) Dispatch(fn func()) {
	l.mu.Lock()
	l.q = append(l.q, fn)
	l.mu.Unlock()
}

func (l *loop) drain() {
	for {
		l.mu.Lock()
		if len(l.q) == 0 {
			l.mu.Unlock()
			return
		}
		fn := l.q[0]
		l.q = l.q[1:]
		l.mu.Unlock()
		fn()
	}
}

// call is one scripted runner invocation.
type call struct {
	ctx  context.Context
	kind Kind
	up   pdcli.UploadSpec
	down pdcli.DownloadSpec
	resp chan result
}

type result struct {
	sum drive.TransferSummary
	err error
	// before runs in the runner goroutine before returning (e.g. write a file).
	before func()
}

// fakeRunner hands every call to the test through a channel.
type fakeRunner struct {
	calls chan *call
	mu    sync.Mutex
	now   int
	peak  int
}

func newRunner() *fakeRunner { return &fakeRunner{calls: make(chan *call, 16)} }

func (f *fakeRunner) do(ctx context.Context, c *call) (drive.TransferSummary, error) {
	f.mu.Lock()
	f.now++
	f.peak = max(f.peak, f.now)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.now--
		f.mu.Unlock()
	}()
	c.ctx, c.resp = ctx, make(chan result, 1)
	f.calls <- c
	select {
	case r := <-c.resp:
		if r.before != nil {
			r.before()
		}
		return r.sum, r.err
	case <-ctx.Done():
		return drive.TransferSummary{}, &pdcli.Error{Kind: pdcli.KindCancelled}
	}
}

func (f *fakeRunner) Upload(ctx context.Context, s pdcli.UploadSpec) (drive.TransferSummary, error) {
	return f.do(ctx, &call{kind: Upload, up: s})
}

func (f *fakeRunner) Download(ctx context.Context, s pdcli.DownloadSpec) (drive.TransferSummary, error) {
	return f.do(ctx, &call{kind: Download, down: s})
}

func (f *fakeRunner) Peak() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

type fixture struct {
	t        *testing.T
	l        *loop
	q        *Queue
	r        *fakeRunner
	now      time.Time
	timers   []func()
	finished []Transfer
	state    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, l: &loop{}, r: newRunner(), now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	f.state = filepath.Join(t.TempDir(), "transfers.json")
	f.q = NewQueue(f.config())
	f.q.OnFinished(func(tr Transfer) { f.finished = append(f.finished, tr) })
	t.Cleanup(func() {
		f.q.Shutdown(5 * time.Second)
		f.l.drain()
	})
	return f
}

func (f *fixture) config() Config {
	return Config{
		Dispatcher: f.l, Runner: func() Runner { return f.r }, StatePath: f.state, Concurrency: 2,
		Now: func() time.Time { return f.now },
		After: func(_ time.Duration, fn func()) func() {
			f.timers = append(f.timers, fn)
			return func() {}
		},
		PollInterval: 5 * time.Millisecond,
	}
}

// next waits for the next runner call.
func (f *fixture) next() *call {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.l.drain() // finish() and retries start the next transfer on the UI loop
		select {
		case c := <-f.r.calls:
			return c
		case <-time.After(2 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			f.t.Fatal("runner was not called")
			return nil
		}
	}
}

// settle drains the loop until the item reaches status (or times out).
func (f *fixture) settle(id string, want Status) Transfer {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.l.drain()
		for _, it := range f.q.Items() {
			if it.ID == id && it.Status == want {
				return it
			}
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("item %s never reached %s: %+v", id, want, f.q.Items())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func upload(name string) Transfer {
	return Transfer{Kind: Upload, Name: name, Local: "/home/u/" + name, Remote: "/my-files", FileStrategy: "rename", FolderStrategy: "merge"}
}

func TestUploadSucceeds(t *testing.T) {
	f := newFixture(t)
	id := f.q.Add(upload("a.txt"))
	c := f.next()
	if c.kind != Upload || c.up.LocalPaths[0] != "/home/u/a.txt" || c.up.Parent != "/my-files" ||
		c.up.FileStrategy != pdcli.UploadFileRename || c.up.FolderStrategy != pdcli.UploadFolderMerge {
		t.Fatalf("spec = %+v", c.up)
	}
	c.resp <- result{sum: drive.TransferSummary{TransferredItems: 1, TransferredBytes: 9}}
	tr := f.settle(id, Done)
	if tr.Summary == nil || tr.Summary.TransferredBytes != 9 || tr.Attempts != 1 || tr.Finished.IsZero() {
		t.Fatalf("transfer = %+v", tr)
	}
	if fr, ok := tr.Fraction(); !ok || fr != 1 {
		t.Fatal("done fraction")
	}
	if len(f.finished) != 1 {
		t.Fatal("OnFinished not called")
	}
	data, _ := os.ReadFile(f.state)
	if !strings.Contains(string(data), `"status":"done"`) {
		t.Fatalf("state = %s", data)
	}
}

func TestConcurrencyCap(t *testing.T) {
	f := newFixture(t)
	var ids []string
	for _, n := range []string{"a", "b", "c", "d"} {
		ids = append(ids, f.q.Add(upload(n)))
	}
	c1, c2 := f.next(), f.next()
	select {
	case <-f.r.calls:
		t.Fatal("third transfer started above the cap")
	case <-time.After(30 * time.Millisecond):
	}
	if active, _ := f.q.Counts(); active != 4 {
		t.Fatalf("active = %d", active)
	}
	f.q.SetConcurrency(3)
	c3 := f.next()
	for _, c := range []*call{c1, c2, c3} {
		c.resp <- result{}
	}
	f.next().resp <- result{}
	for _, id := range ids {
		f.settle(id, Done)
	}
	if f.r.Peak() != 3 {
		t.Fatalf("peak = %d", f.r.Peak())
	}
	if clampConcurrency(0) != 2 || clampConcurrency(9) != 4 {
		t.Fatal("clamp")
	}
}

func TestCancel(t *testing.T) {
	f := newFixture(t)
	f.q.SetConcurrency(1)
	running := f.q.Add(upload("a"))
	queued := f.q.Add(upload("b"))
	c := f.next()
	f.q.Cancel(queued)
	if it := f.settle(queued, Cancelled); it.Attempts != 0 {
		t.Fatal("queued transfer ran")
	}
	f.q.Cancel(running)
	<-c.ctx.Done()
	f.settle(running, Cancelled)
	if len(f.finished) != 0 {
		t.Fatal("cancellations reported as finished")
	}
	f.q.Cancel("nope")
	f.q.Retry(running)
	f.next().resp <- result{}
	f.settle(running, Done)
}

func TestRetryPolicy(t *testing.T) {
	f := newFixture(t)
	id := f.q.Add(upload("a"))
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		f.next().resp <- result{err: &pdcli.Error{Kind: pdcli.KindNetwork, Message: "Connection failed"}}
		if attempt < MaxAttempts {
			tr := f.settle(id, Queued)
			if tr.NotBefore.IsZero() || tr.ErrorKind != "network" {
				t.Fatalf("attempt %d: %+v", attempt, tr)
			}
			// Before the delay elapses nothing starts.
			f.q.pump()
			select {
			case <-f.r.calls:
				t.Fatal("retried before the delay")
			case <-time.After(10 * time.Millisecond):
			}
			f.now = tr.NotBefore
			f.timers[len(f.timers)-1]()
		}
	}
	tr := f.settle(id, Failed)
	if tr.Attempts != MaxAttempts || tr.Error != "Connection failed" || len(f.finished) != 1 {
		t.Fatalf("final = %+v", tr)
	}
}

func TestNoRetryForPermanentErrorsAndFolderDownloads(t *testing.T) {
	f := newFixture(t)
	id := f.q.Add(upload("a"))
	f.next().resp <- result{err: &pdcli.Error{Kind: pdcli.KindNotFound}}
	if tr := f.settle(id, Failed); tr.Attempts != 1 {
		t.Fatalf("permanent error retried: %+v", tr)
	}
	dl := f.q.Add(Transfer{Kind: Download, Name: "Docs", IsFolder: true, Local: t.TempDir(), Remote: "/my-files/x", FileStrategy: "rename", FolderStrategy: "merge"})
	c := f.next()
	if c.down.Remotes[0] != "/my-files/x" || c.down.FileStrategy != pdcli.DownloadFileRename || c.down.FolderStrategy != pdcli.DownloadFolderMerge {
		t.Fatalf("spec = %+v", c.down)
	}
	c.resp <- result{err: &pdcli.Error{Kind: pdcli.KindNetwork}}
	if tr := f.settle(dl, Failed); tr.Attempts != 1 {
		t.Fatalf("folder download retried automatically: %+v", tr)
	}
}

func TestSingleFileDownloadProgressAndCleanup(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "movie.mkv")
	id := f.q.Add(Transfer{Kind: Download, Name: "movie.mkv", Size: 100, Local: dir, Remote: "/my-files/m", FileStrategy: "rename", FolderStrategy: "merge"})
	c := f.next()
	if err := os.WriteFile(target, make([]byte, 40), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.l.drain()
		it := f.q.Items()[0]
		if fr, ok := it.Fraction(); ok && fr == 0.4 && it.PartialFile == target {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no progress: %+v", it)
		}
		time.Sleep(2 * time.Millisecond)
	}
	persisted, _ := os.ReadFile(f.state)
	if !strings.Contains(string(persisted), "partialFile") {
		t.Fatal("partial file not recorded for crash recovery")
	}
	c.resp <- result{err: &pdcli.Error{Kind: pdcli.KindNetwork}}
	f.settle(id, Queued)
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial file kept before retry")
	}
	f.timers[len(f.timers)-1]()
	f.now = f.now.Add(time.Hour)
	f.q.pump()
	c = f.next()
	c.resp <- result{before: func() { _ = os.WriteFile(target, make([]byte, 100), 0o600) }}
	tr := f.settle(id, Done)
	if tr.PartialFile != "" {
		t.Fatal("completed file still marked partial")
	}
	if st, err := os.Stat(target); err != nil || st.Size() != 100 {
		t.Fatal("completed download removed")
	}
}

func TestExistingTargetIsNeverRemoved(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(target, []byte("the user's own file"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := f.q.Add(Transfer{Kind: Download, Name: "report.pdf", Local: dir, Remote: "/my-files/r", FileStrategy: "rename", FolderStrategy: "merge"})
	f.next().resp <- result{err: &pdcli.Error{Kind: pdcli.KindNotFound}}
	f.settle(id, Failed)
	if b, err := os.ReadFile(target); err != nil || string(b) != "the user's own file" {
		t.Fatal("pre-existing file was touched")
	}
}

func TestPartialAndPanic(t *testing.T) {
	f := newFixture(t)
	id := f.q.Add(upload("dir"))
	f.next().resp <- result{sum: drive.TransferSummary{TransferredItems: 2, FailedItems: 1}, err: &pdcli.Error{Kind: pdcli.KindPartial}}
	tr := f.settle(id, Partial)
	if tr.Summary == nil || tr.Summary.FailedItems != 1 {
		t.Fatalf("partial = %+v", tr)
	}
	if _, failed := f.q.Counts(); failed != 1 {
		t.Fatal("Counts")
	}

	p := NewQueue(Config{Dispatcher: f.l, Runner: func() Runner { return panicRunner{} }})
	pid := p.Add(upload("x"))
	deadline := time.Now().Add(5 * time.Second)
	for p.Items()[0].Status != Failed {
		f.l.drain()
		if time.Now().After(deadline) {
			t.Fatal("panic not contained")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if p.Items()[0].ID != pid {
		t.Fatal("id")
	}
	p.Shutdown(time.Second)
}

type panicRunner struct{}

func (panicRunner) Upload(context.Context, pdcli.UploadSpec) (drive.TransferSummary, error) {
	panic("boom")
}

func (panicRunner) Download(context.Context, pdcli.DownloadSpec) (drive.TransferSummary, error) {
	panic("boom")
}

func TestPersistenceAndResume(t *testing.T) {
	f := newFixture(t)
	f.q.SetConcurrency(1)
	a := f.q.Add(upload("a"))
	b := f.q.Add(upload("b"))
	c := f.next()
	_ = c
	// Simulate a crash: the state file says "a" is queued (it was running).
	data, _ := os.ReadFile(f.state)
	var st stateFile
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	for _, it := range st.Items {
		if it.Status != Queued {
			t.Fatalf("persisted %s as %s", it.Name, it.Status)
		}
	}
	f.q.Shutdown(5 * time.Second)

	r2 := newRunner()
	cfg := f.config()
	cfg.Concurrency = 1
	cfg.Runner = func() Runner { return r2 }
	q2 := NewQueue(cfg)
	if err := q2.Load(); err != nil {
		t.Fatal(err)
	}
	if len(q2.Items()) != 2 || q2.Items()[0].ID != a || q2.Items()[1].ID != b {
		t.Fatalf("restored = %+v", q2.Items())
	}
	first := <-r2.calls
	if first.up.LocalPaths[0] != "/home/u/a" {
		t.Fatal("resume order")
	}
	q2.Shutdown(5 * time.Second)
	f.l.drain()
}

func TestLoadCorruptAndMissing(t *testing.T) {
	f := newFixture(t)
	if err := f.q.Load(); err != nil { // missing file
		t.Fatal(err)
	}
	if err := os.WriteFile(f.state, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	q := NewQueue(f.config())
	if err := q.Load(); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("Load(corrupt) = %v", err)
	}
	matches, _ := filepath.Glob(f.state + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatal("corrupt file not kept aside")
	}
	q.Shutdown(time.Second)
}

func TestShutdownRemovesPartialFilesAndRequeues(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "big.iso")
	id := f.q.Add(Transfer{Kind: Download, Name: "big.iso", Size: 1000, Local: dir, Remote: "/my-files/b", FileStrategy: "rename", FolderStrategy: "merge"})
	f.next()
	if err := os.WriteFile(target, make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.q.Items()[0].PartialFile == "" {
		f.l.drain()
		if time.Now().After(deadline) {
			t.Fatal("partial file not detected")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !f.q.Shutdown(5 * time.Second) {
		t.Fatal("shutdown timed out")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial file left after shutdown")
	}
	data, _ := os.ReadFile(f.state)
	if !strings.Contains(string(data), `"status":"queued"`) || strings.Contains(string(data), "partialFile") {
		t.Fatalf("state = %s", data)
	}
	if f.q.Add(upload("late")) != "" {
		t.Fatal("Add after shutdown")
	}
	_ = id
}

func TestEnableRetryRemoveClear(t *testing.T) {
	f := newFixture(t)
	f.q.SetEnabled(false)
	id := f.q.Add(upload("a"))
	select {
	case <-f.r.calls:
		t.Fatal("started while disabled")
	case <-time.After(20 * time.Millisecond):
	}
	f.q.SetEnabled(true)
	f.next().resp <- result{err: &pdcli.Error{Kind: pdcli.KindConflict}}
	f.settle(id, Failed)
	f.q.Retry(id)
	f.next().resp <- result{}
	f.settle(id, Done)
	f.q.Retry(id) // done: no-op
	other := f.q.Add(upload("b"))
	f.q.Remove(other) // active: kept
	f.next().resp <- result{}
	f.settle(other, Done)
	f.q.Remove(id)
	if len(f.q.Items()) != 1 {
		t.Fatalf("items = %+v", f.q.Items())
	}
	f.q.ClearFinished()
	if len(f.q.Items()) != 0 {
		t.Fatal("ClearFinished")
	}
}

func TestHistoryIsBounded(t *testing.T) {
	f := newFixture(t)
	f.q.cfg.StatePath = "" // speed
	for i := 0; i < maxHistory+5; i++ {
		f.q.items = append(f.q.items, &entry{t: Transfer{ID: string(rune(i)), Kind: Upload, Status: Done}})
	}
	f.q.trimHistory()
	if len(f.q.items) != maxHistory {
		t.Fatalf("history = %d", len(f.q.items))
	}
}

func TestNoRunnerMeansNoStart(t *testing.T) {
	l := &loop{}
	q := NewQueue(Config{Dispatcher: l, Runner: func() Runner { return nil }})
	q.Add(upload("a"))
	if q.Items()[0].Status != Queued || q.running != 0 {
		t.Fatal("started without a runner")
	}
	q.Shutdown(time.Second)
	if fr, ok := (Transfer{Status: Running, Size: 0}).Fraction(); ok || fr != 0 {
		t.Fatal("unknown size fraction")
	}
}
