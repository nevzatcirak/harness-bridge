package bounded

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitIdle(t *testing.T, g *Group[string, int]) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for g.InFlight() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d calls still in flight", g.InFlight())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDoReturnsAtCallerDeadline(t *testing.T) {
	g := NewGroup[string, int](4, 2*time.Second)
	gate := make(chan struct{})
	defer func() { close(gate); waitIdle(t, g) }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	v, e := g.Do(ctx, "k", func(context.Context) int { <-gate; return 1 })
	if !errors.Is(e, ErrTimeout) || v != 0 {
		t.Fatalf("got %v %v, want zero value and ErrTimeout", v, e)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Do held the caller %v past a 100ms deadline", elapsed)
	}
}

func TestDoOwnBoundWithoutCallerDeadline(t *testing.T) {
	g := NewGroup[string, int](4, 200*time.Millisecond)
	gate := make(chan struct{})
	defer func() { close(gate); waitIdle(t, g) }()
	start := time.Now()
	if _, e := g.Do(context.Background(), "k", func(context.Context) int { <-gate; return 1 }); !errors.Is(e, ErrTimeout) {
		t.Fatalf("got %v, want ErrTimeout", e)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Do held the caller %v past its 200ms bound", elapsed)
	}
}

func TestDoSharesOneCallPerKey(t *testing.T) {
	g := NewGroup[string, int](4, 2*time.Second)
	gate := make(chan struct{})
	var calls atomic.Int64
	var wg sync.WaitGroup
	results := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := g.Do(context.Background(), "k", func(context.Context) int { calls.Add(1); <-gate; return 7 })
			if e != nil {
				t.Error(e)
			}
			results <- v
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let the other callers join
	close(gate)
	wg.Wait()
	close(results)
	for v := range results {
		if v != 7 {
			t.Fatalf("joined caller got %d", v)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("fn ran %d times for one key, want 1", n)
	}
}

func TestDoLimitFailsWithoutCalling(t *testing.T) {
	g := NewGroup[string, int](2, 2*time.Second)
	gate := make(chan struct{})
	defer func() { close(gate); waitIdle(t, g) }()
	var calls atomic.Int64
	block := func(context.Context) int { calls.Add(1); <-gate; return 1 }
	for _, k := range []string{"a", "b"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		g.Do(ctx, k, block)
		cancel()
	}
	if _, e := g.Do(context.Background(), "c", block); !errors.Is(e, ErrBusy) {
		t.Fatalf("got %v, want ErrBusy at the limit", e)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("fn ran %d times, want 2 (none for the rejected key)", n)
	}
}

func TestDoWorkDetachedFromLeadingCaller(t *testing.T) {
	g := NewGroup[string, int](4, 2*time.Second)
	gate := make(chan struct{})
	started := make(chan struct{})
	fn := func(work context.Context) int {
		close(started)
		<-gate
		if work.Err() != nil {
			return -1 // work context was canceled with the leader
		}
		return 9
	}
	leader, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	leaderDone := make(chan error, 1)
	go func() { _, e := g.Do(leader, "k", fn); leaderDone <- e }()
	<-started
	joined := make(chan int, 1)
	go func() { v, _ := g.Do(context.Background(), "k", fn); joined <- v }()
	if e := <-leaderDone; !errors.Is(e, ErrTimeout) {
		t.Fatalf("leader got %v, want ErrTimeout", e)
	}
	close(gate)
	if v := <-joined; v != 9 {
		t.Fatalf("joined caller got %d after the leader cancelled, want 9", v)
	}
}

func TestDoDoesNotCacheResults(t *testing.T) {
	g := NewGroup[string, int](4, 2*time.Second)
	var calls atomic.Int64
	fn := func(context.Context) int { return int(calls.Add(1)) }
	a, _ := g.Do(context.Background(), "k", fn)
	waitIdle(t, g)
	b, _ := g.Do(context.Background(), "k", fn)
	if a != 1 || b != 2 {
		t.Fatalf("second call reused a result: %d then %d", a, b)
	}
}

// R3-PR55-REVIEW P3-3: a caller whose context is already cancelled always gets
// ErrTimeout, even when the call would finish immediately, and starts nothing.
func TestDoCancelledCallerAlwaysTimesOut(t *testing.T) {
	g := NewGroup[int, int](4, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	for i := 0; i < 2000; i++ {
		v, e := g.Do(ctx, i%3, func(context.Context) int { calls++; return 7 })
		if e != ErrTimeout || v != 0 {
			t.Fatalf("iteration %d: got (%d, %v), want (0, ErrTimeout)", i, v, e)
		}
	}
	if calls != 0 || g.InFlight() != 0 {
		t.Fatalf("cancelled callers started %d calls, %d in flight", calls, g.InFlight())
	}
}
