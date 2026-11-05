package security

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TEST-HARDENING-02 (R3-PR55-REVIEW P3 item 2): AllowsContext shares one
// in-flight filesystem check per (policy, directory). Two policies on the same
// directory must never share a check: one policy's result never answers the
// other, and a blocked check of one never decides the other. Mutant C3 (drop
// the policy from the key) must fail this test.
func TestAllowsContextKeyIsolatesPolicies(t *testing.T) {
	inside := t.TempDir()
	dir := filepath.Join(inside, "ws")
	if e := os.Mkdir(dir, 0o755); e != nil {
		t.Fatal(e)
	}
	// allowing has dir under its root; denying does not.
	allowing, e := New([]string{inside}, []string{"ses_x"})
	if e != nil {
		t.Fatal(e)
	}
	denying, e := New([]string{t.TempDir()}, []string{"ses_x"})
	if e != nil {
		t.Fatal(e)
	}
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	var calls, blocked atomic.Int64
	restore := SetFSHookForTesting(func(path string) {
		// Block only the very first check of dir: the allowing policy's.
		if path == dir && calls.Add(1) == 1 {
			blocked.Add(1)
			<-gate
		}
	})
	t.Cleanup(func() {
		release()
		restore()
		deadline := time.Now().Add(5 * time.Second)
		for AllowsInFlightForTesting() != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})

	type result struct {
		ok  bool
		err error
	}
	first := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		ok, err := allowing.AllowsContext(ctx, "ses_x", dir)
		first <- result{ok, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for blocked.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the allowing policy's check never started")
		}
		time.Sleep(time.Millisecond)
	}

	// While the allowing policy's check is blocked, the denying policy runs
	// its own check for the same directory and answers at once.
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	ok, err := denying.AllowsContext(ctx, "ses_x", dir)
	cancel()
	if err != nil || ok {
		t.Fatalf("denying policy while the other is blocked: ok=%v err=%v, want its own denial", ok, err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("denying policy waited %v on the other policy's check", elapsed)
	}

	// Once released, the allowing policy gets its own (allowed) result.
	release()
	if got := <-first; got.err != nil || !got.ok {
		t.Fatalf("allowing policy after release: %+v", got)
	}

	// Concurrently, results never cross.
	var wg sync.WaitGroup
	errs := make(chan string, 400)
	for i := 0; i < 200; i++ {
		for _, tc := range []struct {
			p    *Policy
			want bool
		}{{allowing, true}, {denying, false}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if ok, err := tc.p.AllowsContext(ctx, "ses_x", dir); err != nil || ok != tc.want {
					errs <- "crossed or failed result"
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	if n := len(errs); n != 0 {
		t.Fatalf("%d of 400 concurrent checks returned the other policy's result or failed", n)
	}
}
