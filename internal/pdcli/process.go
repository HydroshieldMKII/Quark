package pdcli

import (
	"context"
	"sync"
	"time"
)

// ProcessTable tracks every running CLI process so none outlives the app.
// The zero value is not usable; use NewProcessTable.
type ProcessTable struct {
	mu       sync.Mutex
	next     uint64
	running  map[uint64]*tracked
	shutdown bool
}

type tracked struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewProcessTable returns an empty table.
func NewProcessTable() *ProcessTable {
	return &ProcessTable{running: make(map[uint64]*tracked)}
}

// add registers a process. It returns false if the table is shutting down,
// in which case the caller must not start the process.
func (t *ProcessTable) add(cancel context.CancelFunc) (id uint64, done chan struct{}, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.shutdown {
		return 0, nil, false
	}
	t.next++
	done = make(chan struct{})
	t.running[t.next] = &tracked{cancel: cancel, done: done}
	return t.next, done, true
}

func (t *ProcessTable) remove(id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.running[id]; ok {
		delete(t.running, id)
		close(p.done)
	}
}

// Len returns the number of running processes.
func (t *ProcessTable) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.running)
}

// Shutdown refuses new processes, cancels all running ones (SIGTERM to the
// process group, SIGKILL after the runner's grace period) and waits until they
// are reaped or timeout elapses. It returns the number still running.
func (t *ProcessTable) Shutdown(timeout time.Duration) int {
	t.mu.Lock()
	t.shutdown = true
	procs := make([]*tracked, 0, len(t.running))
	for _, p := range t.running {
		procs = append(procs, p)
	}
	t.mu.Unlock()

	for _, p := range procs {
		p.cancel()
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for _, p := range procs {
		select {
		case <-p.done:
		case <-deadline.C:
			return t.Len()
		}
	}
	return t.Len()
}
