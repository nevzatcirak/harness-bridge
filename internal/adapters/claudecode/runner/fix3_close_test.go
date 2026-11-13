package runner

import (
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// Test (iii): Close is bounded by abandoning the wait, never by killing: it returns, the
// abandoned run is reported, the guard stays held and nothing but SIGINT was sent.
func TestCloseAbandonsAStuckChildWithoutKilling(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.CloseWait = 200 * time.Millisecond })
	h.sp.tweak = func(c *fakeChild) { c.ignoreIn = true }
	x := h.mustSend(1, "p", "key-f3-00000005")
	c := h.next(1)
	c.emit(h.initLine(1))
	done := make(chan []string, 1)
	go func() { done <- h.ex.Close() }()
	select {
	case abandoned := <-done:
		if len(abandoned) != 1 || abandoned[0] != x.ID {
			t.Fatalf("abandoned = %v, want the stuck execution %s", abandoned, x.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on a child that ignores SIGINT")
	}
	if !c.gotSignal(SigInt) {
		t.Fatal("Close interrupts the child")
	}
	onlySIGINT(t, c)
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
		t.Fatal("the guard of an abandoned run stays held")
	}
	if v := h.verdict(x.ID); v.Row != "9s" || v.GuardReleased {
		t.Fatalf("verdict = %+v", v)
	}
	c.finish(Exit{Code: 130}) // let the goroutine end
}
