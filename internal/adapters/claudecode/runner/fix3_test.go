package runner

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// onlySIGINT drains what the child was signalled with and fails on anything but SIGINT.
func onlySIGINT(t *testing.T, c *fakeChild) {
	t.Helper()
	for {
		select {
		case s := <-c.signals:
			if s != SigInt {
				t.Fatalf("the runner sent signal %d: ADR-021 §4 forbids any SIGKILL of the wrapper group", s)
			}
		default:
			return
		}
	}
}

// #236 P2-1 (option a), test (i): row 1 and a child that ignores SIGINT. No SIGKILL is
// ever sent, the guard stays held, a second send is SESSION_BUSY, spawns stays 1.
func TestRowOneNeverSendsSIGKILLAndHoldsTheGuard(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.CloseWait = 150 * time.Millisecond })
	h.sp.tweak = func(c *fakeChild) { c.writeN, c.writeErr, c.ignoreIn = 0, io.ErrClosedPipe, true }
	x := h.mustSend(1, "p", "key-f3-00000001")
	c := h.next(1)
	h.waitState(x.ID, "failed")
	if !c.gotSignal(SigInt) {
		t.Fatal("a child whose prompt was not delivered is interrupted")
	}
	time.Sleep(700 * time.Millisecond) // far past the old reaper
	onlySIGINT(t, c)
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
		t.Fatal("the guard must stay held while the child ignores SIGINT")
	}
	if _, _, err := h.send(1, "second", "key-f3-00000002"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("second send: %v", err)
	}
	if h.sp.count() != 1 {
		t.Fatalf("children started: %d", h.sp.count())
	}
	// The child ends by itself: a known wait frees the guard, still without any SIGKILL.
	c.finish(Exit{Code: 1})
	h.wait(x.ID)
	onlySIGINT(t, c)
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); held {
		t.Fatal("a known wait frees the deferred guard")
	}
}

// Test (ii): an unknown Wait. No SIGKILL, guard held for good, second send busy.
func TestUnknownWaitSendsNoSIGKILLAndHoldsTheGuard(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.CloseWait = 150 * time.Millisecond })
	h.sp.tweak = func(c *fakeChild) { c.writeN, c.writeErr, c.ignoreIn = 0, io.ErrClosedPipe, true }
	x := h.mustSend(1, "p", "key-f3-00000003")
	c := h.next(1)
	h.waitState(x.ID, "failed")
	c.finish(Exit{Unknown: true})
	h.wait(x.ID)
	time.Sleep(500 * time.Millisecond)
	onlySIGINT(t, c)
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
		t.Fatal("an unknown wait keeps the guard held")
	}
	if _, _, err := h.send(1, "second", "key-f3-00000004"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("second send: %v", err)
	}
	if h.sp.count() != 1 {
		t.Fatalf("children started: %d", h.sp.count())
	}
}

// #236 P3-3 (C-3): admission after the writer locks were released is refused.
func TestAdmissionAfterLocksClosedIsForbidden(t *testing.T) {
	h := newHarness(t, nil)
	h.locks.Close()
	if _, _, err := h.send(1, "p", "key-f3-00000006"); errCode(err) != "FORBIDDEN" {
		t.Fatalf("admission without the writer lock: %v", err)
	}
	if h.sp.count() != 0 {
		t.Fatalf("a child was started without the lock (%d)", h.sp.count())
	}
}

// #236 P3-3 (C-6): a child that keeps writing after a broken stream is drained, not left
// blocked on a full pipe.
func TestStdoutIsDrainedAfterABrokenStream(t *testing.T) {
	h := newHarness(t, nil)
	x := h.mustSend(1, "p", "key-f3-00000007")
	c := h.next(1)
	c.emit(h.initLine(1))
	emitted := make(chan struct{})
	go func() {
		c.emitRaw(strings.Repeat("a", 1<<20+10) + "\n") // breaks the stream
		for i := 0; i < 3; i++ {
			c.emitRaw(strings.Repeat("b", 1<<20) + "\n") // would block forever without the drain
		}
		close(emitted)
	}()
	select {
	case <-emitted:
	case <-time.After(5 * time.Second):
		t.Fatal("the runner stopped reading after the stream broke: the child is blocked on its pipe")
	}
	c.finish(Exit{Code: 0})
	h.wait(x.ID)
	if v := h.verdict(x.ID); v.Row != "6" && v.Row != "8" {
		t.Fatalf("verdict = %+v", v)
	}
}
