package runner

import (
	"io"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// #233 P2-1/P3-1: a Wait that returns Exit{Unknown:true} is NOT a confirmed reap. The
// guard stays held (release is operator-only), the reaper still fires, the child slot
// is kept until it did, and Close is bounded by ReapAfter.
func TestUnknownWaitIsNotAConfirmedReap(t *testing.T) {
	for name, written := range map[string]int{"no byte accepted (row 1)": 0, "partial write (row 1s)": 5} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.ReapAfter = 800 * time.Millisecond })
			h.sp.tweak = func(c *fakeChild) { c.writeN, c.writeErr, c.ignoreIn = written, io.ErrClosedPipe, true }
			x := h.mustSend(1, "a longer prompt", "key-uw-0000001")
			c := h.next(1)
			want := "failed"
			if written > 0 {
				want = "unknown"
			}
			h.waitState(x.ID, want)
			c.finish(Exit{Unknown: true}) // Wait returned, but the reap is not confirmed
			// Promptly, while the reaper has not fired yet:
			if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
				t.Fatal("an unconfirmed reap freed the guard")
			}
			if _, _, err := h.send(1, "second", "key-uw-0000002"); errCode(err) != "SESSION_BUSY" {
				t.Fatalf("a second send after an unknown wait: %v", err)
			}
			if h.sp.count() != 1 {
				t.Fatalf("children started: %d", h.sp.count())
			}
			// The reaper is not cancelled: the group is still SIGKILLed.
			if !c.gotSignal(SigKill) {
				t.Fatal("the reaper never fired after an unknown wait")
			}
			h.wait(x.ID)
			if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
				t.Fatal("the guard must stay held after the run ended on an unknown wait (operator-only release)")
			}
			if _, _, err := h.send(1, "third", "key-uw-0000003"); errCode(err) != "SESSION_BUSY" {
				t.Fatalf("after the run ended: %v", err)
			}
			if h.sp.count() != 1 {
				t.Fatalf("children started: %d", h.sp.count())
			}
			// Close is bounded.
			done := make(chan struct{})
			go func() { h.ex.Close(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Close hung on a run whose wait was unknown")
			}
		})
	}
}

// A confirmed reap still frees a deferred guard (the P2-1 rule of #231 is unchanged).
func TestConfirmedReapStillFreesTheDeferredGuard(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.ReapAfter = time.Minute })
	h.sp.tweak = func(c *fakeChild) { c.writeN, c.writeErr, c.ignoreIn = 0, io.ErrClosedPipe, true }
	x := h.mustSend(1, "p", "key-uw-0000004")
	c := h.next(1)
	h.waitState(x.ID, "failed")
	c.finish(Exit{Code: 1})
	h.wait(x.ID)
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); held {
		t.Fatal("a confirmed reap must free the guard")
	}
}
