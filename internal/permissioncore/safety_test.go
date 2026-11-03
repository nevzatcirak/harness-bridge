package permissioncore

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestPermissionCoreConcurrentDuplicateDecisions(t *testing.T) {
	c, a, n, _ := fixture(t, false)
	v := observe(t, c, n, request("per_A"))
	d := Decision{v.Challenge, AllowOnce, "one-human-receipt"}
	a.issue(d)
	start := make(chan struct{})
	out := make(chan error, 32)
	var ready sync.WaitGroup
	ready.Add(32)
	for i := 0; i < 32; i++ {
		go func() {
			ready.Done()
			<-start
			_, err := c.Decide(t.Context(), "alice", d)
			out <- err
		}()
	}
	ready.Wait()
	close(start)
	for i := 0; i < 32; i++ {
		if err := <-out; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Consume(t.Context(), "alice", claimOnce(t, c, v)); err != nil {
		t.Fatal(err)
	}
	if calls, effects := n.counts(); calls != 1 || effects != 1 {
		t.Fatal(calls, effects)
	}
}

type abandonedReply struct {
	*fixtureNative
	goexit bool
}

func (n *abandonedReply) ReplyOnce(ctx context.Context, b Binding) error {
	_ = n.fixtureNative.ReplyOnce(ctx, b)
	if n.goexit {
		runtime.Goexit()
	}
	panic("synthetic fixture abandonment")
}

func TestPermissionCoreAbandonedAttempt(t *testing.T) {
	for _, goexit := range []bool{false, true} {
		t.Run(fmt.Sprint(goexit), func(t *testing.T) {
			c, a, n, _ := fixture(t, false)
			v := observe(t, c, n, request("per_A"))
			decide(t, c, a, v, AllowOnce)
			x := claimOnce(t, c, v)
			c.native = &abandonedReply{n, goexit}
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() { _ = recover() }()
				_, _ = c.Consume(t.Context(), "alice", x)
			}()
			wait(t, done)
			got, err := c.Snapshot(t.Context(), "alice", v.Challenge)
			if err != nil || got.State != Uncertain || !got.Attempted {
				t.Fatal(got, err)
			}
			_, err = c.Consume(t.Context(), "alice", x)
			requireError(t, err, ErrUsed)
			if calls, effects := n.counts(); calls != 1 || effects != 1 {
				t.Fatal(calls, effects)
			}
		})
	}
}

// Calibrates the fake to the pinned OpenCode1.19.4 session-wide reject semantics.
// This method is intentionally absent from the Core's nativeProvider interface.
func (n *fixtureNative) sessionWideReject(r Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.rejectCalls++
	for k, v := range n.requests {
		if v.Binding.Authority == r.Binding.Authority && v.Binding.Lineage == r.Binding.Lineage && v.Binding.SessionID == r.Binding.SessionID {
			n.resolved[k] = true
		}
	}
}

func TestPermissionCoreDenialBlastRadius(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c, a, n, _ := fixture(t, false)
			aRequest := request("per_A")
			v := observe(t, c, n, aRequest)
			if count == 2 {
				observe(t, c, n, request("per_B"))
			}
			other := request("per_C")
			other.Binding.SessionID = "ses_other"
			observe(t, c, n, other)
			decide(t, c, a, v, Deny)
			got, err := c.Snapshot(t.Context(), "alice", v.Challenge)
			if err != nil || got.State != Denied || got.Native != NativePending || got.Attempted || got.Reason != ErrUnsafeDeny || got.RejectScope != "same_native_session" {
				t.Fatal(got, err)
			}
			_, err = c.Claim(t.Context(), "alice", v.Challenge)
			requireError(t, err, ErrUnsafeDeny)
			// Disappearance is not native rejection evidence or permission refund.
			got, err = c.Reconcile(t.Context(), "alice", v.Challenge, NativeEvidence{Status: NativeUnknown})
			if err != nil || got.State != Denied || got.Native != NativeUnknown {
				t.Fatal(got, err)
			}
			if calls, effects := n.counts(); calls != 0 || effects != 0 || n.rejectCalls != 0 || len(n.resolved) != 0 {
				t.Fatal("local denial dispatched or resolved an unrelated request")
			}
			// Separate fake-only calibration: a supposedly exact reject would fan out.
			n.sessionWideReject(aRequest)
			if len(n.resolved) != count || n.resolved[key(other.Binding)] {
				t.Fatal("fake did not model actual reject blast radius")
			}
		})
	}
}

func TestPermissionCoreOneAllowDoesNotResolveOtherRequests(t *testing.T) {
	c, a, n, _ := fixture(t, false)
	v := observe(t, c, n, request("per_A"))
	other := observe(t, c, n, request("per_B"))
	decide(t, c, a, v, AllowOnce)
	if _, err := c.Consume(t.Context(), "alice", claimOnce(t, c, v)); err != nil {
		t.Fatal(err)
	}
	got, err := c.Snapshot(t.Context(), "alice", other.Challenge)
	if err != nil || got.State != Pending || n.resolved[key(other.Challenge.Binding)] {
		t.Fatal("unrelated request altered", got, err)
	}
}

func TestPermissionCoreConflictsAndReceiptReuse(t *testing.T) {
	c, a, n, _ := fixture(t, false)
	v := observe(t, c, n, request("per_A"))
	d := decide(t, c, a, v, AllowOnce)
	opposite := Decision{v.Challenge, Deny, "opposite-receipt"}
	a.issue(opposite)
	_, err := c.Decide(t.Context(), "alice", opposite)
	requireError(t, err, ErrConflict)
	other := observe(t, c, n, request("per_B"))
	reused := Decision{other.Challenge, AllowOnce, d.ReceiptID}
	a.issue(reused) // even a faulty authority cannot reuse an already spent receipt
	_, err = c.Decide(t.Context(), "alice", reused)
	requireError(t, err, ErrUsed)
	_, err = c.Decide(t.Context(), "alice", Decision{v.Challenge, Choice("always"), "another"})
	requireError(t, err, ErrInput)
	if calls, _ := n.counts(); calls != 0 {
		t.Fatal(calls)
	}
}

func TestPermissionCoreRevocationDuringFreshnessRead(t *testing.T) {
	for _, policy := range []bool{false, true} {
		t.Run(fmt.Sprint(policy), func(t *testing.T) {
			c, a, n, _ := fixture(t, false)
			v := observe(t, c, n, request("per_A"))
			d := decide(t, c, a, v, AllowOnce)
			x := claimOnce(t, c, v)
			n.checkEnter, n.checkRelease = make(chan struct{}), make(chan struct{})
			out := make(chan error, 1)
			go func() { _, err := c.Consume(t.Context(), "alice", x); out <- err }()
			wait(t, n.checkEnter)
			want := ErrHuman
			if policy {
				a.mu.Lock()
				a.denied = true
				a.mu.Unlock()
				want = ErrAuthority
			} else {
				a.revoke(d.ReceiptID)
			}
			close(n.checkRelease)
			requireError(t, <-out, want)
			if calls, _ := n.counts(); calls != 0 {
				t.Fatal(calls)
			}
		})
	}
}

func TestPermissionCoreBoundsAndMissingCorrelation(t *testing.T) {
	for _, cfg := range []Config{{DecisionTTL: -1}, {DecisionTTL: time.Nanosecond}, {DecisionTTL: 121 * time.Second}, {Capacity: -1}, {Capacity: 4097}} {
		_, err := New(cfg, nil)
		requireError(t, err, ErrInput)
	}
	c, a, n, clock := fixture(t, false)
	c.config.Capacity = 1
	v := observe(t, c, n, request("per_A"))
	clock.Store(int64(v.Challenge.Deadline - 1))
	decide(t, c, a, v, AllowOnce)
	_, err := c.Observe(t.Context(), "alice", request("per_B"))
	requireError(t, err, ErrCapacity)
	clock.Store(int64(v.Challenge.Deadline))
	got, err := c.Snapshot(t.Context(), "alice", v.Challenge)
	if err != nil || got.State != Expired {
		t.Fatal(got, err)
	}
	// No tombstone eviction permits a consumed/expired identity to be reused.
	_, err = c.Observe(t.Context(), "alice", request("per_B"))
	requireError(t, err, ErrCapacity)
	for _, mode := range []string{"missing-message", "missing-call", "empty-scope", "oversize", "utf8", "scope-override", "unknown-tool", "unknown-version"} {
		t.Run(mode, func(t *testing.T) {
			r := request("per_C")
			switch mode {
			case "missing-message":
				r.Binding.ToolMessageID = ""
			case "missing-call":
				r.Binding.ToolCallID = ""
			case "empty-scope":
				r.ScopeMaterial = nil
			case "oversize":
				r.ScopeMaterial = make([]byte, 16385)
			case "utf8":
				r.ScopeMaterial = []byte{0xff}
			case "scope-override":
				r.Binding.ScopeFingerprint[0] = 1
			case "unknown-tool":
				r.Binding.Tool = "model-approved-plugin"
			case "unknown-version":
				r.Binding.NativeVersion = "unverified-native"
			}
			_, err := c.Observe(t.Context(), "alice", r)
			requireError(t, err, ErrInput)
		})
	}
}

func TestPermissionCoreExternalResolutionAndChangedObservation(t *testing.T) {
	c, a, n, _ := fixture(t, false)
	r := request("per_A")
	v := observe(t, c, n, r)
	decide(t, c, a, v, AllowOnce)
	got, err := c.Reconcile(t.Context(), "alice", v.Challenge, NativeEvidence{r, NativeResolved})
	if err != nil || got.State != ExternallyResolved || got.Attempted {
		t.Fatal(got, err)
	}
	_, err = c.Claim(t.Context(), "alice", v.Challenge)
	requireError(t, err, ErrResolved)
	r.ScopeMaterial = []byte("changed request")
	_, err = c.Observe(t.Context(), "alice", r)
	requireError(t, err, ErrStale)
	got, err = c.Observe(t.Context(), "alice", request("per_A"))
	if err != nil || got.State != Stale {
		t.Fatal("old observation restored a decision", got, err)
	}
}
