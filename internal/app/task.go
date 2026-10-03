package app

import (
	"context"
	"fmt"
	"runtime/debug"
)

// limiter is a counting semaphore bounded by a context.
type limiter chan struct{}

func newLimiter(n int) limiter { return make(limiter, n) }

func (l limiter) acquire(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l limiter) release() { <-l }

// Concurrency caps for CLI processes (constitution, Principle II). Each CLI
// process uses about 125 MB, and processes share the CLI's SQLite caches.
const (
	maxReads  = 3
	maxWrites = 1
)

// spawn runs work on a goroutine, bounded by lim, and calls done with its
// result on the UI thread. A panic in work is recovered and reported as an
// internal error; it never crashes the app. done is skipped after Shutdown.
func (a *App) spawn(ctx context.Context, name string, lim limiter, work func(context.Context) error, done func(error)) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					a.log.Error("panic in background task", "task", name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
					err = fmt.Errorf("%w in %s: %v", errInternal, name, r)
				}
			}()
			if lim != nil {
				if err := lim.acquire(ctx); err != nil {
					return err
				}
				defer lim.release()
			}
			return work(ctx)
		}()
		a.disp.Dispatch(func() {
			if a.closed || done == nil {
				return
			}
			done(err)
		})
	}()
}
