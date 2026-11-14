package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// ADR-021 slice 4 (ADR-020 extension, D14 Q8): the Claude guard release. All with fake
// children; nothing live.

const settle = 80 * time.Millisecond

func (h *harness) sid(n int) string { return h.sessions[n-1].ID }

// unknownRun is a finished run with verdict unknown (row 6: no result, known exit): the guard
// is held and the child is confirmed reaped.
func (h *harness) unknownRun(n int, key string) (id string, c *fakeChild) {
	h.t.Helper()
	x := h.mustSend(n, "p", key)
	c = h.next(n)
	c.emit(h.initLine(n))
	c.finish(Exit{Code: 143, Signal: "terminated"})
	h.wait(x.ID)
	if v := h.verdict(x.ID); v.State != "unknown" || v.GuardReleased || !h.guardHeld(n) {
		h.t.Fatalf("setup: %+v", v)
	}
	return x.ID, c
}

func (h *harness) release(n int, id string) (ReleaseEvidence, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.ex.ReleaseGuard(ctx, h.sid(n), id, ReleaseOptions{Settle: settle})
}

func TestReleaseAfterAConfirmedExitFreesTheGuard(t *testing.T) {
	h := newHarness(t, s3)
	id, c := h.unknownRun(1, "key-rel-00000001")
	before := len(c.received())
	ev, err := h.release(1, id)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Harness != "claudecode" || ev.NativeRead || !ev.ChildExited || !ev.GroupEmpty || !ev.Rechecked || ev.GuardReleasedAt == nil || ev.SettleMS != settle.Milliseconds() || ev.Row != "6" {
		t.Fatalf("evidence %+v", ev)
	}
	if h.guardHeld(1) {
		t.Fatal("the guard is still held")
	}
	x, _ := h.ex.Get(id)
	if x.State != "unknown" || x.Unresolved == nil || x.GuardReleasedAt == nil {
		t.Fatalf("the release changed the execution: %+v", x)
	}
	if v := h.verdict(id); v.State != "unknown" || v.Row != "6" {
		t.Fatalf("the verdict changed: %+v", v)
	}
	if n := len(c.received()); n != before {
		t.Fatalf("the release sent %d signals", n-before)
	}
	// the session is free again: a new execution is admitted and starts a child
	b := h.mustSend(1, "again", "key-rel-00000002")
	until(t, "the second child", func() bool { return h.sp.count() == 2 })
	// The released execution is still unknown, ended and reaped, but the guard is B's now: a
	// second release of the OLD execution must not free B's guard (compare-and-swap).
	if _, err := h.release(1, id); errCode(err) != "INVALID_INPUT" || !h.guardHeld(1) {
		t.Fatalf("a repeated release of the old execution: %v (guard held %v)", err, h.guardHeld(1))
	}
	if held, _ := h.ex.SessionGuard(h.sid(1)); !held {
		t.Fatal("the new execution lost its guard")
	}
	_ = b
}

// Refused while the child is live (row 9, SIGINT ignored): SESSION_BUSY, guard kept.
func TestReleaseRefusedWhileTheChildIsLive(t *testing.T) {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.RunMax = 50 * time.Millisecond })
	h.sp.tweak = func(c *fakeChild) { c.ignoreIn, c.ignoreTerm = true, true }
	x := h.mustSend(1, "p", "key-rel-00000003")
	c := h.next(1)
	c.emit(h.initLine(1))
	h.decided(x.ID)
	ev, err := h.release(1, x.ID)
	if errCode(err) != "SESSION_BUSY" || ev.ChildExited || ev.Rechecked || !h.guardHeld(1) {
		t.Fatalf("err %v evidence %+v", err, ev)
	}
	// still refused after the escalation was exhausted (child_unconfirmed, §4 final escalation)
	until(t, "child_unconfirmed", func() bool { return h.verdict(x.ID).ChildUnconfirmed })
	if _, err := h.release(1, x.ID); errCode(err) != "SESSION_BUSY" || !h.guardHeld(1) {
		t.Fatalf("with child_unconfirmed: %v", err)
	}
	// The operator removes the child: the Wait returns a known status and the group is empty.
	// Only then are the preconditions facts, and the verdict's child_unconfirmed stays as audit.
	c.finish(Exit{Code: 137, Signal: "killed"})
	h.wait(x.ID)
	if _, err := h.release(1, x.ID); err != nil {
		t.Fatalf("after the operator removed the child: %v", err)
	}
	if h.guardHeld(1) {
		t.Fatal("the guard is still held")
	}
}

func TestReleaseRefusedWhenTheExitIsNotConfirmed(t *testing.T) {
	h := newHarness(t, s3)
	x := h.mustSend(1, "p", "key-rel-00000004")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.finish(Exit{Unknown: true}) // an unknown Wait confirms nothing
	h.wait(x.ID)
	if v := h.verdict(x.ID); v.Row != "10" || v.GuardReleased {
		t.Fatalf("setup: %+v", v)
	}
	ev, err := h.release(1, x.ID)
	if errCode(err) != "SESSION_BUSY" || ev.ChildExited || !h.guardHeld(1) {
		t.Fatalf("err %v evidence %+v", err, ev)
	}
}

func TestReleaseRefusedWhenTheProcessGroupIsNotEmpty(t *testing.T) {
	h := newHarness(t, s3)
	h.sp.tweak = func(c *fakeChild) { c.groupBusy = true }
	id, _ := h.unknownRun(1, "key-rel-00000005")
	ev, err := h.release(1, id)
	if errCode(err) != "SESSION_BUSY" || !ev.ChildExited || ev.GroupEmpty || ev.Rechecked || !h.guardHeld(1) {
		t.Fatalf("err %v evidence %+v", err, ev)
	}
}

func TestReleaseFailsClosedWhenTheGroupCheckFails(t *testing.T) {
	h := newHarness(t, s3)
	h.sp.tweak = func(c *fakeChild) { c.groupErr = errors.New("kill: boom") }
	id, _ := h.unknownRun(1, "key-rel-00000006")
	if _, err := h.release(1, id); errCode(err) != "UPSTREAM_FAILURE" || !h.guardHeld(1) {
		t.Fatalf("err %v", err)
	}
}

// The re-check is real: a group member that appears during the settle interval stops it.
func TestReleaseRecheckCatchesAChangeDuringTheSettle(t *testing.T) {
	h := newHarness(t, s3)
	id, c := h.unknownRun(1, "key-rel-00000007")
	go func() {
		time.Sleep(settle / 3)
		c.mu.Lock()
		c.groupBusy = true
		c.mu.Unlock()
	}()
	ev, err := h.release(1, id)
	if errCode(err) != "SESSION_BUSY" || !h.guardHeld(1) || ev.GuardReleasedAt != nil {
		t.Fatalf("err %v evidence %+v", err, ev)
	}
	if ev.Rechecked != true {
		t.Fatalf("the re-check did not run: %+v", ev)
	}
}

func TestReleaseRefusedForAnotherExecutionOrSession(t *testing.T) {
	h := newHarness(t, s3)
	// execution A completes and frees the guard; execution B (same session) holds it now
	a := h.mustSend(1, "p", "key-rel-0000000a")
	ca := h.next(1)
	ca.emit(h.initLine(1))
	ca.emit(h.resultLine(1, nil))
	ca.finish(Exit{Code: 0})
	h.wait(a.ID)
	until(t, "the guard release", func() bool { return !h.guardHeld(1) })
	bID, _ := func() (string, *fakeChild) {
		x := h.mustSend(1, "q", "key-rel-0000000b")
		c := h.sp.child(h.t, h.root(1))
		_ = c
		return x.ID, c
	}()
	// B is running: not unknown
	if _, err := h.release(1, bID); errCode(err) != "INVALID_INPUT" || !h.guardHeld(1) {
		t.Fatalf("a running execution: %v", err)
	}
	// A does not hold the guard
	if _, err := h.release(1, a.ID); errCode(err) != "INVALID_INPUT" || !h.guardHeld(1) {
		t.Fatalf("another execution of the same session: %v", err)
	}
	// B's ID with another session's ID
	if _, err := h.release(2, bID); errCode(err) != "INVALID_INPUT" || !h.guardHeld(1) {
		t.Fatalf("another session: %v", err)
	}
	// an unknown execution
	if _, err := h.release(1, strings.Repeat("0", 32)); errCode(err) != "INVALID_INPUT" {
		t.Fatalf("unknown execution: %v", err)
	}
}

func TestReleaseRefusedWhileTheExecutionIsNotUnknown(t *testing.T) {
	h := newHarness(t, s3)
	// a failed row-1 execution whose Wait was unknown keeps its guard, but only an `unknown`
	// execution can be released
	h.sp.tweak = func(c *fakeChild) { c.writeN, c.writeErr, c.ignoreIn, c.ignoreTerm = 0, io_ErrClosedPipe, true, true }
	x := h.mustSend(1, "p", "key-rel-00000008")
	c := h.next(1)
	h.waitState(x.ID, "failed")
	c.finish(Exit{Unknown: true})
	h.wait(x.ID)
	if _, err := h.release(1, x.ID); errCode(err) != "INVALID_INPUT" || !h.guardHeld(1) {
		t.Fatalf("a failed execution: %v", err)
	}
}

func TestReleaseInputsAndLifecycle(t *testing.T) {
	h := newHarness(t, s3)
	id, _ := h.unknownRun(1, "key-rel-00000009")
	ctx := context.Background()
	for name, tc := range map[string][2]string{
		"opencode id":    {"opencode:ses_abc", id},
		"bare uuid":      {uuidN(1), id},
		"uppercase uuid": {"claudecode:4E59C1BD-4333-4A71-875D-CBA9301BD4A9", id},
		"short exec":     {h.sid(1), "abc"},
		"upper exec":     {h.sid(1), strings.ToUpper(id)},
	} {
		if _, err := h.ex.ReleaseGuard(ctx, tc[0], tc[1], ReleaseOptions{Settle: settle}); errCode(err) != "INVALID_INPUT" || !h.guardHeld(1) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a cancelled context during the settle keeps the guard
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(settle / 4); cancel() }()
	if _, err := h.ex.ReleaseGuard(cctx, h.sid(1), id, ReleaseOptions{Settle: 2 * time.Second}); errCode(err) != "UPSTREAM_TIMEOUT" || !h.guardHeld(1) {
		t.Fatalf("cancelled settle: %v", err)
	}
	// Close during the settle: UNKNOWN_OUTCOME, guard kept; and after Close nothing is released
	done := make(chan error, 1)
	go func() {
		_, err := h.ex.ReleaseGuard(ctx, h.sid(1), id, ReleaseOptions{Settle: 2 * time.Second})
		done <- err
	}()
	time.Sleep(settle / 2)
	h.ex.Close()
	select {
	case err := <-done:
		if errCode(err) != "UNKNOWN_OUTCOME" || !h.guardHeld(1) {
			t.Fatalf("close during the settle: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the settle ignored Close")
	}
	if _, err := h.ex.ReleaseGuard(ctx, h.sid(1), id, ReleaseOptions{Settle: settle}); errCode(err) != "UNKNOWN_OUTCOME" {
		t.Fatalf("after Close: %v", err)
	}
}
