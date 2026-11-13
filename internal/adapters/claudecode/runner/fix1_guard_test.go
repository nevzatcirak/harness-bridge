package runner

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

func (h *harness) waitState(id, want string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if x, err := h.ex.Get(id); err == nil && x.State == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	x, _ := h.ex.Get(id)
	h.t.Fatalf("execution never reached %q (state %q)", want, x.State)
}

// #229 P2-1: a row 1 failed verdict decided after a child was spawned must not free
// the session guard while that child is alive (one child per session, ADR-021 §2).
func TestRowOneFailureHoldsTheGuardUntilTheChildIsReaped(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.ReapAfter = 3 * time.Second })
	h.sp.tweak = func(c *fakeChild) { c.writeN, c.writeErr, c.ignoreIn = 0, io.ErrClosedPipe, true }
	x := h.mustSend(1, "p", "key-r1-0000001")
	c := h.next(1)
	h.waitState(x.ID, "failed") // the verdict is decided early: nothing was delivered
	if v := h.verdict(x.ID); v.Row != "1" || !v.GuardReleased {
		t.Fatalf("verdict = %+v", v)
	}
	// ...but the child is alive, so the session is still guarded.
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
		t.Fatal("the guard was freed while the child was still running")
	}
	if _, _, err := h.send(1, "second", "key-r1-0000002"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("a second send while the first child is alive: %v", err)
	}
	if h.sp.count() != 1 {
		t.Fatalf("children started: %d (want 1 while the first is alive)", h.sp.count())
	}
	if !c.gotSignal(SigInt) {
		t.Fatal("a child whose prompt was not delivered must be interrupted")
	}
	// Reaped: the guard is released and a new execution is admitted.
	c.finish(Exit{Code: 1})
	h.wait(x.ID)
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); held {
		t.Fatal("the guard must be released once the child was reaped")
	}
	h.sp.tweak = nil
	y := h.mustSend(1, "third", "key-r1-0000003")
	deadline := time.Now().Add(5 * time.Second)
	for h.sp.count() < 2 && time.Now().Before(deadline) { // the second child starts only now
		time.Sleep(2 * time.Millisecond)
	}
	if h.sp.count() != 2 {
		t.Fatalf("children started: %d", h.sp.count())
	}
	h.ex.Close()
	_ = y
}

// The partial-write variant: unknown, guard held before and after the reap.
func TestPartialStdinWriteKeepsTheGuardAfterTheReap(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.ReapAfter = 3 * time.Second })
	h.sp.tweak = func(c *fakeChild) { c.writeN, c.writeErr, c.ignoreIn = 5, io.ErrClosedPipe, true }
	x := h.mustSend(1, "a longer prompt", "key-r1-0000004")
	c := h.next(1)
	h.waitState(x.ID, "unknown")
	if _, _, err := h.send(1, "second", "key-r1-0000005"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("while alive: %v", err)
	}
	c.finish(Exit{Code: 1})
	h.wait(x.ID)
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
		t.Fatal("an uncertain submission keeps the guard after the reap")
	}
	if _, _, err := h.send(1, "third", "key-r1-0000006"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("after the reap: %v", err)
	}
	if h.sp.count() != 1 {
		t.Fatalf("children started: %d", h.sp.count())
	}
}

// A spawn that failed has no child, so its guard is freed at once (unchanged).
func TestFailedStartStillFreesTheGuardImmediately(t *testing.T) {
	h := newHarness(t, nil)
	h.sp.failure = errors.New("fork/exec: no such file")
	x := h.mustSend(1, "p", "key-r1-0000007")
	h.wait(x.ID)
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); held {
		t.Fatal("no child exists: the guard is freed immediately")
	}
}

// #229 P3-1: the child-cap refusal comes after the allowlist, lock, root and
// wrapper checks, so a refused session is told so rather than "busy".
func TestPreflightRefusalsComeBeforeTheChildCap(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.MaxChildren = 1 })
	h.mustSend(1, "p", "key-cap1-000001")
	h.next(1)
	// At the cap: a session that is not allowlisted is NOT_FOUND, not busy.
	if _, _, err := h.ex.Send(ctxBG(), Request{Session: "claudecode:" + uuidN(9), Prompt: "p", Key: "key-cap1-000002"}); errCode(err) != "NOT_FOUND" {
		t.Fatalf("unlisted session at the cap: %v", err)
	}
	// A listed session at the cap is genuinely capacity: busy and retryable.
	if _, _, err := h.send(2, "p", "key-cap1-000003"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("listed session at the cap: %v", err)
	}
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(2)); held {
		t.Fatal("a capped request must not leave its guard held")
	}
	// A wrapper that no longer validates is FORBIDDEN even at the cap.
	h.fsys.breakIt()
	if _, _, err := h.send(3, "p", "key-cap1-000004"); errCode(err) != "FORBIDDEN" {
		t.Fatalf("invalid wrapper at the cap: %v", err)
	}
	if h.sp.count() != 1 {
		t.Fatalf("children started: %d", h.sp.count())
	}
}

// A rejected admission gives back the child slot it took (history full, after the
// preflight): at MaxChildren 1 another session is still admitted afterwards.
func TestRejectedAdmissionGivesTheChildSlotBack(t *testing.T) {
	h := newHarness(t, func(c *Config, s *claudecode.StoreConfig) {
		c.MaxChildren = 1
		s.MaxSessionBytes = 400
		s.MaxRecordBytes = 400
	})
	x := h.mustSend(1, strings.Repeat("a", 300), "key-slot-000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Code: 0})
	h.wait(x.ID)
	if _, _, err := h.send(1, "next", "key-slot-000002"); errCode(err) != "INTERNAL" {
		t.Fatalf("history full: %v", err)
	}
	// Session 2 must still be admitted: the slot taken by the refused request was returned.
	y := h.mustSend(2, "p", "key-slot-000003")
	h.next(2).finish(Exit{Code: 1})
	h.wait(y.ID)
}
