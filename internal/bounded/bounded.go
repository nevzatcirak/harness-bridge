// Package bounded runs calls that may block in the kernel (filesystem access on
// a hung NFS/FUSE mount) without holding a request past its deadline (H4-A F-3).
//
// A Group shares one in-flight call per key, caps the number of calls still
// running, and lets every caller stop waiting at its own deadline. A goroutine
// stuck in a blocking system call cannot be cancelled from Go; a Group bounds
// how many such goroutines exist instead of preventing them. Results are never
// cached: once a call finishes and its waiters have read it, the next caller
// starts a new call.
package bounded

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	// ErrTimeout means the caller's context or the call's own bound ended
	// before the call returned. The call may still be running.
	ErrTimeout = errors.New("bounded call did not complete in time")
	// ErrBusy means the Group's limit of running calls was reached; nothing
	// was started.
	ErrBusy = errors.New("too many bounded calls in flight")
)

type call[V any] struct {
	done  chan struct{}
	value V
}

// Group runs fn at most once per key at a time and at most Limit calls at once.
// The zero value is not usable; use NewGroup.
type Group[K comparable, V any] struct {
	limit   int
	timeout time.Duration
	mu      sync.Mutex
	calls   map[K]*call[V]
}

// NewGroup returns a Group allowing at most limit running calls, each bounded
// by timeout for its own context and for every caller's wait.
func NewGroup[K comparable, V any](limit int, timeout time.Duration) *Group[K, V] {
	return &Group[K, V]{limit: limit, timeout: timeout, calls: map[K]*call[V]{}}
}

// Do returns fn's value for key, sharing a call already in flight for the same
// key. fn receives a context detached from every caller (so one caller giving
// up does not fail the others) and bounded by the Group's timeout. Do returns
// ErrTimeout when ctx or the timeout ends first, and ErrBusy without calling fn
// when the limit is reached. Callers must treat both errors as "unknown" and
// fail closed.
func (g *Group[K, V]) Do(ctx context.Context, key K, fn func(context.Context) V) (V, error) {
	var zero V
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	g.mu.Lock()
	c, ok := g.calls[key]
	if !ok {
		if len(g.calls) >= g.limit {
			g.mu.Unlock()
			return zero, ErrBusy
		}
		c = &call[V]{done: make(chan struct{})}
		g.calls[key] = c
		go func() {
			work, stop := context.WithTimeout(context.Background(), g.timeout)
			c.value = fn(work)
			stop()
			g.mu.Lock()
			delete(g.calls, key)
			g.mu.Unlock()
			close(c.done)
		}()
	}
	g.mu.Unlock()
	select {
	case <-c.done:
		return c.value, nil
	case <-ctx.Done():
		return zero, ErrTimeout
	}
}

// InFlight reports how many calls are still running, for tests and diagnostics.
func (g *Group[K, V]) InFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}
