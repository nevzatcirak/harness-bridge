package execution

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// EXEC-FS-BOUND-IMPL: every workspace filesystem check in this package is
// bounded (docs/design/EXEC_FS_BOUNDING.md). Each test blocks a chosen check
// through the test hook and asserts the design table's outcome within a bound,
// so a regression shows as a failure, not as a hung test.

// fsGate blocks hooked calls for which block returns true until release.
type fsGate struct {
	gate    chan struct{}
	once    sync.Once
	entered chan string
	count   map[string]*atomic.Int32
	mu      sync.Mutex
}

func (g *fsGate) release() { g.once.Do(func() { close(g.gate) }) }

// n reports how many hooked calls of op have been seen.
func (g *fsGate) n(op string) int32 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c := g.count[op]; c != nil {
		return c.Load()
	}
	return 0
}

// blockFS installs a hook that blocks a call when block(op, path, nth) is true,
// where nth counts calls of op since installation (1-based). Cleanup releases
// the gate, removes the hook and waits until no bounded call is in flight.
func blockFS(t *testing.T, block func(op, path string, nth int32) bool) *fsGate {
	t.Helper()
	g := &fsGate{gate: make(chan struct{}), entered: make(chan string, 64), count: map[string]*atomic.Int32{}}
	restore := SetFSHookForTesting(func(op, path string) {
		g.mu.Lock()
		c := g.count[op]
		if c == nil {
			c = &atomic.Int32{}
			g.count[op] = c
		}
		g.mu.Unlock()
		if block(op, path, c.Add(1)) {
			select {
			case g.entered <- op + " " + path:
			default:
			}
			<-g.gate
		}
	})
	t.Cleanup(func() {
		g.release()
		restore()
		waitFSIdle(t)
	})
	return g
}

func waitFSIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for fsInFlight() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d bounded filesystem calls still in flight", fsInFlight())
		}
		time.Sleep(time.Millisecond)
	}
}

// within runs fn and fails if it does not return within d.
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v (blocked filesystem call)", what, d)
	}
}

func wantTimeout(t *testing.T, err error, what string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != "UPSTREAM_TIMEOUT" || !e.Retryable {
		t.Fatalf("%s: got %v, want retryable UPSTREAM_TIMEOUT", what, err)
	}
}

func guardHeld(m *Manager) bool {
	held, _ := m.SessionGuard("ses_test")
	return held
}

// fsBound is the longest a bounded call may hold a caller, plus slack.
const fsBound = fsTimeout + 1500*time.Millisecond

func TestAdmissionFSTimeoutReleasesAndSendsNothing(t *testing.T) {
	for _, op := range []string{"evalsymlinks", "stat"} {
		t.Run(op, func(t *testing.T) {
			f := &fake{status: "idle"}
			m := setup(t, f)
			g := blockFS(t, func(o, _ string, _ int32) bool { return o == op })
			var err error
			within(t, fsBound, "admission", func() {
				_, _, err = m.SendWithRunnerOutcome(context.Background(), f, "ses_test", "prompt", "admission-key")
			})
			wantTimeout(t, err, "admission")
			if guardHeld(m) {
				t.Fatal("guard reservation kept after an admission timeout")
			}
			f.mu.Lock()
			sends := f.sends
			f.mu.Unlock()
			if sends != 0 {
				t.Fatalf("Prompt invoked %d times after an admission timeout", sends)
			}
			g.release()
			waitFSIdle(t)
			// The key was never accepted: the same key is a new admission.
			x, replay, err := m.SendWithRunnerOutcome(context.Background(), f, "ses_test", "prompt", "admission-key")
			if err != nil || replay || x.ID == "" {
				t.Fatalf("retry after recovery: replay=%v id=%q err=%v", replay, x.ID, err)
			}
		})
	}
}

func TestReplayFSTimeoutReturnsNoSnapshot(t *testing.T) {
	f := &fake{status: "idle"}
	m := setup(t, f)
	first, err := m.Send(context.Background(), "ses_test", "prompt", "replay-key")
	if err != nil {
		t.Fatal(err)
	}
	g := blockFS(t, func(op, _ string, _ int32) bool { return op == "evalsymlinks" })
	var x Execution
	var replay bool
	within(t, fsBound, "replay", func() {
		x, replay, err = m.SendWithRunnerOutcome(context.Background(), f, "ses_test", "prompt", "replay-key")
	})
	wantTimeout(t, err, "replay")
	if x.ID != "" || replay {
		t.Fatalf("replay exposed a snapshot without a completed identity check: %+v replay=%v", x, replay)
	}
	g.release()
	waitFSIdle(t)
	x, replay, err = m.SendWithRunnerOutcome(context.Background(), f, "ses_test", "prompt", "replay-key")
	if err != nil || !replay || x.ID != first.ID {
		t.Fatalf("replay after recovery: %+v replay=%v err=%v", x, replay, err)
	}
}

func TestPreSubmitFSTimeoutFinalizesUnsubmitted(t *testing.T) {
	f := &fake{status: "idle"}
	m := setup(t, f)
	// Admission performs the first EvalSymlinks; the work goroutine's recheck,
	// immediately before Prompt, performs the second.
	g := blockFS(t, func(op, _ string, nth int32) bool { return op == "evalsymlinks" && nth >= 2 })
	x, err := m.Send(context.Background(), "ses_test", "prompt", "presubmit-key")
	if err != nil {
		t.Fatal(err)
	}
	var v Execution
	deadline := time.Now().Add(fsBound)
	for {
		v, err = m.Snapshot(context.Background(), x.ID)
		if err == nil && v.State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pre-submit timeout did not finalize: %+v %v", v, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if v.LocalFailure == nil || v.LocalFailure.Code != "PRE_SUBMISSION_UNAVAILABLE" {
		t.Fatalf("want PRE_SUBMISSION_UNAVAILABLE, got %+v", v)
	}
	if guardHeld(m) {
		t.Fatal("guard kept for a provably unsubmitted run")
	}
	f.mu.Lock()
	sends := f.sends
	f.mu.Unlock()
	if sends != 0 {
		t.Fatalf("Prompt invoked %d times", sends)
	}
	// The key stays a tombstone: a replay returns the failed run and sends nothing.
	g.release()
	waitFSIdle(t)
	again, replay, err := m.SendWithRunnerOutcome(context.Background(), f, "ses_test", "prompt", "presubmit-key")
	if err != nil || !replay || again.ID != x.ID || again.State != "failed" {
		t.Fatalf("replay of the tombstoned key: %+v replay=%v err=%v", again, replay, err)
	}
	f.mu.Lock()
	sends = f.sends
	f.mu.Unlock()
	if sends != 0 {
		t.Fatalf("replay resubmitted: %d Prompt calls", sends)
	}
}

// monitorEnded returns a submitted run whose background monitor has ended
// (MONITOR_EXPIRED, unknown) so that only the test's own calls touch the
// filesystem afterwards.
func monitorEnded(t *testing.T) (*fake, *Manager, Execution) {
	t.Helper()
	f := &fake{status: "idle"}
	m := NewWithTiming(f, 50*time.Millisecond, 2*time.Millisecond)
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "prompt", "monitor-key")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		v, err := m.Snapshot(context.Background(), x.ID)
		if err == nil && v.MonitorEndedAt != nil && v.State == "unknown" {
			return f, m, v
		}
		if time.Now().After(deadline) {
			t.Fatalf("monitor did not end: %+v %v", v, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestInterruptFSTimeoutSendsNoAbort(t *testing.T) {
	f, m, x := monitorEnded(t)
	// Interrupt first probes (V6 and V7: two EvalSymlinks), then re-checks the
	// identity immediately before Abort (V3: the third EvalSymlinks).
	blockFS(t, func(op, _ string, nth int32) bool { return op == "evalsymlinks" && nth == 3 })
	var err error
	within(t, 2*fsBound, "interrupt", func() { _, err = m.Interrupt(context.Background(), x.ID) })
	wantTimeout(t, err, "interrupt")
	f.mu.Lock()
	aborts := f.aborts
	f.mu.Unlock()
	if aborts != 0 {
		t.Fatalf("Abort sent %d times without a completed identity check", aborts)
	}
	v, _ := m.Snapshot(context.Background(), x.ID)
	if terminal(v.State) || !guardHeld(m) {
		t.Fatalf("interrupt timeout changed state or released the guard: %+v held=%v", v, guardHeld(m))
	}
}

func TestReadPathFSTimeout(t *testing.T) {
	f, m, x := monitorEnded(t)
	_ = f
	blockFS(t, func(op, _ string, _ int32) bool { return op == "evalsymlinks" })
	var err error
	within(t, fsBound, "ValidateSession", func() {
		err = m.ValidateSession(context.Background(), x.ID, opencode.Session{ID: "ses_test", Directory: "/"})
	})
	wantTimeout(t, err, "ValidateSession (V4)")
	within(t, fsBound, "Get", func() { _, err = m.Get(context.Background(), x.ID) })
	wantTimeout(t, err, "Get/refresh (V5)")
	within(t, fsBound, "Result", func() { _, _, err = m.Result(context.Background(), x.ID) })
	wantTimeout(t, err, "Result/refresh (V5)")
	if !guardHeld(m) {
		t.Fatal("read-path timeout released the guard")
	}
}

// runningWithMonitor returns a running execution whose background monitor keeps
// probing every few milliseconds.
func runningWithMonitor(t *testing.T) (*fake, *Manager, Execution) {
	t.Helper()
	f := &fake{status: "idle"}
	m := NewWithTiming(f, 30*time.Second, 5*time.Millisecond)
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "prompt", "probe-key")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		v, err := m.Snapshot(context.Background(), x.ID)
		if err == nil && v.State == "running" {
			return f, m, v
		}
		if time.Now().After(deadline) {
			t.Fatalf("not running: %+v %v", v, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func proofWithCwd(f *fake, mid, cwd string) {
	complete(f, mid, "stop", "")
	f.mu.Lock()
	last := &f.messages[len(f.messages)-1]
	last.Info.Time.Completed = time.Now().UnixMilli()
	last.Info.Path.Cwd = cwd
	f.status = "idle"
	f.mu.Unlock()
}

func waitReason(t *testing.T, m *Manager, id, reason string, d time.Duration) Execution {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		v, err := m.Snapshot(context.Background(), id)
		if err == nil && v.Unresolved != nil && v.Unresolved.Reason == reason {
			return v
		}
		if err == nil && terminal(v.State) {
			t.Fatalf("promoted to %s during a filesystem timeout: %+v", v.State, v)
		}
		if time.Now().After(deadline) {
			t.Fatalf("want unresolved %s, got %+v %v", reason, v, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestProbeIdentityFSTimeoutNeverPromotes(t *testing.T) {
	for _, tc := range []struct {
		name string
		nth  int32 // which EvalSymlinks of the next probe blocks: 1 = V6, 2 = V7
	}{{"V6", 1}, {"V7", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, x := runningWithMonitor(t)
			g := blockFS(t, func(op, path string, nth int32) bool { return op == "evalsymlinks" && path == "/" && nth == tc.nth })
			proofWithCwd(f, x.MessageID, "/")
			v := waitReason(t, m, x.ID, "OBSERVATION_UNAVAILABLE", 2*fsBound)
			if v.State != "unknown" || !guardHeld(m) {
				t.Fatalf("probe timeout: %+v held=%v", v, guardHeld(m))
			}
			// Pin the identity re-check itself, not a later check that joins the
			// same blocked call.
			if !strings.Contains(v.Error, "workspace identity check did not complete") {
				t.Fatalf("probe timeout not reported by the identity check: %q", v.Error)
			}
			g.release()
			waitFSIdle(t)
			waitState(t, m, x.ID, "completed")
		})
	}
}

// P5: the correlated assistant cwd check must not run under run.mu.
func TestProbeCwdFSTimeoutDoesNotHoldRunLock(t *testing.T) {
	f, m, x := runningWithMonitor(t)
	// "/." resolves to "/", the accepted canonical directory, so only the cwd
	// check (not V6/V7 on "/") blocks.
	g := blockFS(t, func(op, path string, _ int32) bool { return op == "evalsymlinks" && path == "/." })
	proofWithCwd(f, x.MessageID, "/.")
	select {
	case <-g.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("probe never reached the cwd check")
	}
	// While the probe is blocked in the cwd check, the run lock is free.
	within(t, 200*time.Millisecond, "Snapshot during blocked cwd check", func() {
		if _, err := m.Snapshot(context.Background(), x.ID); err != nil {
			t.Error(err)
		}
	})
	within(t, 200*time.Millisecond, "SessionGuard during blocked cwd check", func() {
		if held, _ := m.SessionGuard("ses_test"); !held {
			t.Error("guard not held")
		}
	})
	v := waitReason(t, m, x.ID, "OBSERVATION_UNAVAILABLE", 2*fsBound)
	if v.State != "unknown" || !guardHeld(m) {
		t.Fatalf("cwd timeout: %+v held=%v", v, guardHeld(m))
	}
	if !strings.Contains(v.Error, "correlated assistant workspace check did not complete") {
		t.Fatalf("cwd timeout not reported by the cwd check: %q", v.Error)
	}
	g.release()
	waitFSIdle(t)
	waitState(t, m, x.ID, "completed")
}

// A completed cwd resolution that differs is still a mismatch (unchanged).
func TestProbeCwdMismatchStillIdentityMismatch(t *testing.T) {
	f, m, x := runningWithMonitor(t)
	proofWithCwd(f, x.MessageID, t.TempDir())
	waitReason(t, m, x.ID, "IDENTITY_MISMATCH", 3*time.Second)
}

func TestCloseNotHeldByBlockedFS(t *testing.T) {
	f := &fake{status: "idle"}
	m := NewWithTiming(f, time.Second, 2*time.Millisecond)
	g := blockFS(t, func(op, _ string, _ int32) bool { return op == "evalsymlinks" })
	started := make(chan struct{})
	go func() {
		close(started)
		m.Send(context.Background(), "ses_test", "prompt", "close-key")
	}()
	<-started
	select {
	case <-g.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("admission never reached the filesystem check")
	}
	within(t, fsBound, "Close", m.Close)
}

func TestFSLimitFailsClosedAndBoundsGoroutines(t *testing.T) {
	blockFS(t, func(string, string, int32) bool { return true })
	base := runtime.NumGoroutine()
	var wg sync.WaitGroup
	for i := 0; i < fsLimit; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			evalSymlinks(context.Background(), fmt.Sprintf("/limit-%d", i))
		}(i)
	}
	deadline := time.Now().Add(2 * time.Second)
	for fsInFlight() < fsLimit {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d calls started", fsInFlight(), fsLimit)
		}
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	if _, err := evalSymlinks(context.Background(), "/limit-extra"); !errors.Is(err, errFSTimeout) {
		t.Fatalf("call beyond the limit: %v, want errFSTimeout", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("call beyond the limit waited instead of failing closed")
	}
	for i := 0; i < 50; i++ {
		evalSymlinks(context.Background(), fmt.Sprintf("/limit-more-%d", i))
	}
	if n := runtime.NumGoroutine() - base; n > 2*fsLimit+8 {
		t.Fatalf("goroutines grew by %d with %d calls stuck", n, fsLimit)
	}
	if fsInFlight() > fsLimit {
		t.Fatalf("%d calls in flight, limit %d", fsInFlight(), fsLimit)
	}
	wg.Wait()
}
