package repository

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// H4-A F-3: a filesystem call that blocks (hung NFS/FUSE) must not hold Inspect
// past its deadline, must not grow goroutines without bound, and the next
// inspection after the filesystem recovers must succeed.

// blockUnder makes every confined stat/read below root block until release is
// called. It counts the calls that blocked.
func blockUnder(t *testing.T, root string) (release func(), hits *atomic.Int64) {
	t.Helper()
	gate := make(chan struct{})
	hits = &atomic.Int64{}
	restore := SetFSHookForTesting(func(path string) {
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			hits.Add(1)
			<-gate
		}
	})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		release()
		restore()
		waitIdle(t)
	})
	return release, hits
}

// waitIdle waits until no inspection is still in flight.
func waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		inflight.Lock()
		n := len(inflight.calls)
		inflight.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d inspections still in flight", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func gitRepo(t *testing.T) (string, *security.Policy) {
	t.Helper()
	root := t.TempDir()
	gitTest(t, root, "init")
	gitTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "seed")
	policy, e := security.New([]string{root}, []string{"ses_test"})
	if e != nil {
		t.Fatal(e)
	}
	return root, policy
}

func TestInspectReturnsWithinDeadlineWhenStorageBlocks(t *testing.T) {
	root, policy := gitRepo(t)
	release, hits := blockUnder(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, w, e := Inspect(ctx, root, policy, "ses_test")
	elapsed := time.Since(start)
	if e == nil || r != (Repository{}) || w != (Workspace{}) {
		t.Fatalf("blocked inspection returned metadata: %+v %+v %v", r, w, e)
	}
	if elapsed > time.Second {
		t.Fatalf("Inspect held the caller %v past a 200ms deadline", elapsed)
	}
	if hits.Load() == 0 {
		t.Fatal("hook never blocked: test does not exercise a blocking call")
	}

	// Without a caller deadline, Inspect still returns after its own bound.
	start = time.Now()
	if _, _, e := Inspect(context.Background(), root, policy, "ses_test"); e == nil {
		t.Fatal("blocked inspection succeeded")
	}
	if elapsed := time.Since(start); elapsed > inspectTimeout+time.Second {
		t.Fatalf("Inspect held the caller %v past its %v bound", elapsed, inspectTimeout)
	}

	// Once storage recovers, the next inspection succeeds.
	release()
	waitIdle(t)
	r, w, e = Inspect(context.Background(), root, policy, "ses_test")
	if e != nil || r.ID == "" || w.Root != root {
		t.Fatalf("inspection after recovery: %+v %+v %v", r, w, e)
	}
}

func TestInspectSharesOneBlockedCallPerDirectory(t *testing.T) {
	root, policy := gitRepo(t)
	_, hits := blockUnder(t, root)
	before := runtime.NumGoroutine()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if _, _, e := Inspect(ctx, root, policy, "ses_test"); e == nil {
				t.Error("blocked inspection succeeded")
			}
		}()
	}
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d blocking filesystem calls for one directory, want 1 shared inspection", n)
	}
	if grown := runtime.NumGoroutine() - before; grown > 2 {
		t.Fatalf("goroutines grew by %d after 50 inspections of one blocked directory", grown)
	}
}

func TestInspectBoundsBlockedInspectionsAcrossDirectories(t *testing.T) {
	root, policy := gitRepo(t)
	dirs := make([]string, 3*inspectLimit)
	for i := range dirs {
		dirs[i] = filepath.Join(root, "sub", string(rune('a'+i%26))+strings.Repeat("x", i/26))
		if e := os.MkdirAll(dirs[i], 0o755); e != nil {
			t.Fatal(e)
		}
	}
	_, hits := blockUnder(t, root)
	before := runtime.NumGoroutine()
	for round := 0; round < 3; round++ {
		for _, dir := range dirs {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			if _, _, e := Inspect(ctx, dir, policy, "ses_test"); e == nil {
				t.Fatal("blocked inspection succeeded")
			}
			cancel()
		}
	}
	if n := hits.Load(); n > inspectLimit {
		t.Fatalf("%d blocked inspections started, want at most %d", n, inspectLimit)
	}
	if grown := runtime.NumGoroutine() - before; grown > inspectLimit+2 {
		t.Fatalf("goroutines grew by %d with %d blocked directories, bound %d", grown, len(dirs), inspectLimit)
	}
}
