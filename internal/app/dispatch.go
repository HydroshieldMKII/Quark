package app

import "sync"

// Dispatcher runs functions on the UI thread, in order. The GTK
// implementation uses glib.IdleAdd; tests use Loop.
type Dispatcher interface {
	Dispatch(fn func())
}

// Loop is a deterministic Dispatcher for tests: queued functions run only
// when the test calls Drain or RunUntil.
type Loop struct {
	mu    sync.Mutex
	queue []func()
	wake  chan struct{}
}

// NewLoop returns an empty loop.
func NewLoop() *Loop { return &Loop{wake: make(chan struct{}, 1)} }

// Dispatch implements Dispatcher. It is safe to call from any goroutine.
func (l *Loop) Dispatch(fn func()) {
	l.mu.Lock()
	l.queue = append(l.queue, fn)
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Drain runs queued functions (including ones queued while draining) and
// returns how many ran.
func (l *Loop) Drain() int {
	n := 0
	for {
		l.mu.Lock()
		if len(l.queue) == 0 {
			l.mu.Unlock()
			return n
		}
		fn := l.queue[0]
		l.queue = l.queue[1:]
		l.mu.Unlock()
		fn()
		n++
	}
}

// Wait blocks until something is queued or done is closed.
func (l *Loop) Wait(done <-chan struct{}) {
	select {
	case <-l.wake:
	case <-done:
	}
}
