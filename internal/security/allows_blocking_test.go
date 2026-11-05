package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// H4-A F-3 (second half): the workspace check on the request path must not
// block past the caller's deadline on a hung NFS/FUSE mount, must fail closed
// (never allowed by default), must keep goroutines bounded, and must recover.

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
		waitChecksIdle(t)
	})
	return release, hits
}

func waitChecksIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for AllowsInFlightForTesting() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d workspace checks still in flight", AllowsInFlightForTesting())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// goroutinesSettle polls until the goroutine count is at most limit or a
// deadline passes; blocked checks never exit, so a real leak still fails.
func goroutinesSettle(limit int) int {
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= limit || time.Now().After(deadline) {
			return n
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func policyFor(t *testing.T) (string, *Policy) {
	t.Helper()
	root := t.TempDir()
	p, e := New([]string{root}, []string{"ses_test"})
	if e != nil {
		t.Fatal(e)
	}
	return p.Roots[0], p
}

func TestAllowsContextFailsClosedWhenStorageBlocks(t *testing.T) {
	root, p := policyFor(t)
	release, hits := blockUnder(t, root)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	ok, e := p.AllowsContext(ctx, "ses_test", root)
	if ok || !errors.Is(e, ErrCheckUnavailable) {
		t.Fatalf("blocked check returned allowed=%v err=%v, want false and ErrCheckUnavailable", ok, e)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("AllowsContext held the caller %v past a 200ms deadline", elapsed)
	}
	if hits.Load() == 0 {
		t.Fatal("hook never blocked: test does not exercise a blocking call")
	}

	// Without a caller deadline the check's own 2 s bound applies.
	start = time.Now()
	if ok, e := p.AllowsContext(context.Background(), "ses_test", root); ok || !errors.Is(e, ErrCheckUnavailable) {
		t.Fatalf("blocked check without deadline: allowed=%v err=%v", ok, e)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("AllowsContext held the caller %v past its 2 s bound", elapsed)
	}

	release()
	waitChecksIdle(t)
	if ok, e := p.AllowsContext(context.Background(), "ses_test", root); !ok || e != nil {
		t.Fatalf("check after recovery: allowed=%v err=%v", ok, e)
	}
}

func TestAllowsContextSkipsStorageForUnknownSession(t *testing.T) {
	root, p := policyFor(t)
	_, hits := blockUnder(t, root)
	ok, e := p.AllowsContext(context.Background(), "ses_other", root)
	if ok || e != nil {
		t.Fatalf("unknown session: allowed=%v err=%v, want false without error", ok, e)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d filesystem calls for a session outside the ACL", n)
	}
}

func TestAllowsContextBoundsBlockedChecks(t *testing.T) {
	root, p := policyFor(t)
	dirs := make([]string, 48)
	for i := range dirs {
		dirs[i] = filepath.Join(root, "d", string(rune('a'+i%26))+strings.Repeat("x", i/26))
		if e := os.MkdirAll(dirs[i], 0o755); e != nil {
			t.Fatal(e)
		}
	}
	_, hits := blockUnder(t, root)
	before := runtime.NumGoroutine()
	for round := 0; round < 3; round++ {
		for _, dir := range dirs {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			if ok, e := p.AllowsContext(ctx, "ses_test", dir); ok || !errors.Is(e, ErrCheckUnavailable) {
				t.Fatalf("blocked check for %s: allowed=%v err=%v", dir, ok, e)
			}
			cancel()
		}
	}
	if n := hits.Load(); n > 16 {
		t.Fatalf("%d blocked checks started, want at most 16", n)
	}
	if grown := goroutinesSettle(before+16+2) - before; grown > 16+2 {
		t.Fatalf("goroutines grew by %d with %d blocked directories", grown, len(dirs))
	}
}

func TestAllowsContextMatchesAllowsWhenStorageWorks(t *testing.T) {
	root, p := policyFor(t)
	inside := filepath.Join(root, "inside")
	if e := os.Mkdir(inside, 0o755); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(root, "link")
	if e := os.Symlink(inside, link); e != nil {
		t.Fatal(e)
	}
	outside := t.TempDir()
	for _, dir := range []string{root, inside, link, outside, root + "/../" + filepath.Base(root), filepath.Join(root, "missing")} {
		want := p.Allows("ses_test", dir)
		got, e := p.AllowsContext(context.Background(), "ses_test", dir)
		if e != nil || got != want {
			t.Errorf("%s: AllowsContext=%v,%v, Allows=%v", dir, got, e, want)
		}
	}
}
