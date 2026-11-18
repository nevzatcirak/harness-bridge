package execution

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

// strayHold is how long the stray call stays in flight in the test whose logic waits for it: the helper's
// waitFSIdle simply waits as long as the stray runs, so the length only costs time.
const strayHold = 300 * time.Millisecond

// startStray holds one NON-fresh evalsymlinks call on "/" in flight: it blocks inside a hook that is
// replaced right after the call started, so the hook of a later blockFS never counts it. It models a call
// that was already running when a test installs its hook (for example the monitor's last probe, abandoned
// at its deadline). release lets it finish.
func startStray(t *testing.T) (release func()) {
	t.Helper()
	entered, gate := make(chan struct{}), make(chan struct{})
	restore := SetFSHookForTesting(func(op, path string) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-gate
	})
	go func() { _, _ = evalSymlinks(context.Background(), "/") }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the stray call never started")
	}
	restore() // the stray stays blocked inside the old hook; the group still holds its in-flight call
	closed := false
	rel := func() {
		if !closed {
			closed = true
			close(gate)
		}
	}
	t.Cleanup(func() { rel(); waitFSIdle(t) })
	return rel
}

// releaseWhenInterruptWaits releases the stray as soon as Interrupt's goroutine is parked waiting inside checkSession (V6
// of Interrupt, joined to the stray's call), not after a fixed delay: a fixed delay is shorter than a
// starved scheduler can take to start Interrupt, and the stray would then be gone before V6 could join it.
// The fallback (1.5 s, under fsTimeout) only keeps a broken scenario from hanging.
func releaseWhenInterruptWaits(release func()) {
	go func() {
		deadline := time.Now().Add(1500 * time.Millisecond)
		buf := make([]byte, 1<<20)
		for time.Now().Before(deadline) {
			n := runtime.Stack(buf, true)
			for _, g := range strings.Split(string(buf[:n]), "\n\n") {
				// Parked in the group's select: V6 has joined the in-flight call and waits for it. Running
				// or runnable inside checkSession or Do is not enough (it may not have looked the call up yet).
				if strings.HasPrefix(g, "goroutine ") && strings.Contains(strings.SplitN(g, "\n", 2)[0], "[select") &&
					strings.Contains(g, "execution.(*Manager).Interrupt") && strings.Contains(g, "execution.checkSession") && strings.Contains(g, "bounded") {
					release()
					return
				}
			}
			time.Sleep(time.Millisecond)
		}
		release()
	}()
}

// The mechanism of the main-CI failure (ead39fd, run 44194335438: "interrupt: got <nil>, want retryable
// UPSTREAM_TIMEOUT"): selecting V3 by "the third executed call" is fooled by a stray in-flight call of the
// same key. V6 joins it (not counted), V7 and V3 are the only counted calls, nothing blocks, V3 runs to
// completion and Abort follows a COMPLETED identity check. That is a test-timing defect, not an Abort
// without a check: this test pins the mechanism so the reason for waitFSIdle in the real test stays visible.
func TestInterruptCountBasedBlockIsShiftedByAStrayCall(t *testing.T) {
	f, m, x := monitorEnded(t)
	release := startStray(t)
	g := blockFS(t, func(op, _ string, nth int32) bool { return op == "evalsymlinks" && nth == 3 }) // the old selection, no idle wait
	releaseWhenInterruptWaits(release)
	var err error
	within(t, 2*fsBound, "interrupt", func() { _, err = m.Interrupt(context.Background(), x.ID) })
	f.mu.Lock()
	aborts := f.aborts
	f.mu.Unlock()
	if err != nil || aborts != 1 {
		t.Fatalf("the shifted count did not reproduce the CI outcome: err=%v aborts=%d (want <nil>, 1)", err, aborts)
	}
	if n := g.n("evalsymlinks"); n != 2 {
		t.Fatalf("executed evalsymlinks calls = %d, want 2 (V7 and V3; V6 joined the stray)", n)
	}
	if fsInFlight() != 0 {
		t.Fatalf("%d bounded calls still in flight after Abort: V3 had not completed", fsInFlight())
	}
}

// The real test's logic with the same stray call: it waits for the stray, so V3 is the third executed call
// and blocks; the outcome is the fail-closed one.
func TestInterruptFSTimeoutWithAStrayCallStillBlocksTheIdentityCheck(t *testing.T) {
	f, m, x := monitorEnded(t)
	release := startStray(t)
	time.AfterFunc(strayHold, release)
	interruptBlocksTheIdentityCheck(t, f, m, x)
}

// waitFSIdle's failure names the operation of the call that is still running (review #401 P3-4).
func TestInFlightOpsNamesTheBlockedOperation(t *testing.T) {
	if got := inFlightOps(); !strings.Contains(got, "no evalsymlinks or stat goroutine") {
		t.Fatalf("with nothing in flight: %q", got)
	}
	release := startStray(t)
	defer release()
	// the stray's caller goroutine is parked inside the bounded group, in evalSymlinksKey
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := inFlightOps(); strings.Contains(got, "evalsymlinks:") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the blocked call is not named: %q", inFlightOps())
		}
		time.Sleep(time.Millisecond)
	}
}
