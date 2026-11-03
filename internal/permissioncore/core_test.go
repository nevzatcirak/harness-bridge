package permissioncore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureAuthority struct {
	mu       sync.Mutex
	receipts map[string]HumanEvidence
	denied   bool
	others   map[Principal]bool // independently authorized principals besides alice
	deny     func(Binding) bool // binding-specific revocation
	gate     func(Binding)      // called outside a.mu before each decision
}

func (a *fixtureAuthority) Authorize(ctx context.Context, p Principal, b Binding) error {
	a.mu.Lock()
	gate := a.gate
	a.mu.Unlock()
	if gate != nil {
		gate(b)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil || a.denied || (p != "alice" && !a.others[p]) || b.Principal != p || a.deny != nil && a.deny(b) {
		return ErrAuthority
	}
	return nil
}
func (a *fixtureAuthority) VerifyHuman(_ context.Context, ref string) (HumanEvidence, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.receipts[ref]
	if !ok {
		return HumanEvidence{}, ErrHuman
	}
	return e, nil
}
func (a *fixtureAuthority) issue(d Decision) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.receipts[d.ReceiptID] = HumanEvidence{d, "independent-human", IndependentHumanChannel, true, true}
}
func (a *fixtureAuthority) revoke(ref string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.receipts, ref)
}

// No HTTP client or OpenCode adapter is used. These counters belong to the fake
// native side, independently of the Core's attempted marker and snapshots.
type fixtureNative struct {
	mu                       sync.Mutex
	requests                 map[requestKey]Request
	resolved                 map[requestKey]bool
	checks, calls, effects   int
	rejectCalls              int
	missing, lost            bool
	checkEnter, checkRelease chan struct{}
	replyEnter, replyRelease chan struct{}
}

func (n *fixtureNative) Check(ctx context.Context, b Binding) (NativeEvidence, error) {
	if n.checkEnter != nil {
		close(n.checkEnter)
		select {
		case <-n.checkRelease:
		case <-ctx.Done():
			return NativeEvidence{}, ctx.Err()
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.checks++
	if n.missing {
		return NativeEvidence{Status: NativeUnknown}, nil
	}
	r, ok := n.requests[key(b)]
	if !ok {
		return NativeEvidence{}, errors.New("NOT_FOUND: private native details")
	}
	status := NativePending
	if n.resolved[key(b)] {
		status = NativeResolved
	}
	return NativeEvidence{r, status}, nil
}
func (n *fixtureNative) ReplyOnce(ctx context.Context, b Binding) error {
	n.mu.Lock()
	n.calls++
	_, ok := n.requests[key(b)]
	// Match upstream ID-only reply semantics: do NOT give the fake an invented
	// scope-fingerprint CAS that could mask a missing core freshness check.
	if !ok || n.resolved[key(b)] {
		n.mu.Unlock()
		return errors.New("NOT_FOUND or changed native subject")
	}
	n.effects++
	n.resolved[key(b)] = true
	lost := n.lost
	n.mu.Unlock()
	if n.replyEnter != nil {
		close(n.replyEnter)
		select {
		case <-n.replyRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if lost {
		return errors.New("sensitive native response lost")
	}
	return nil
}
func (n *fixtureNative) counts() (int, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls, n.effects
}

func request(id string) Request {
	return Request{Binding: Binding{Authority: "native-A", Lineage: "process-A", NativeVersion: "opencode-1.19.4", ProjectRoot: "root-A", Workspace: "workspace-A", PolicyRevision: 1, Generation: 1, RequestID: id, SessionID: "ses_A", ExecutionID: "execution-A", UserMessageID: "msg_user", ToolMessageID: "msg_assistant", ToolCallID: "call-A", Principal: "alice", Tool: "bash", Action: "execute", ResourceCount: 1}, ScopeMaterial: []byte(`exact-native-patterns-and-metadata: SECRET_CANARY do-not-expose`)}
}
func fixture(t *testing.T, public bool) (*Core, *fixtureAuthority, *fixtureNative, *atomic.Int64) {
	t.Helper()
	a := &fixtureAuthority{receipts: make(map[string]HumanEvidence)}
	c, err := New(Config{}, a)
	if err != nil {
		t.Fatal(err)
	}
	n := &fixtureNative{requests: make(map[requestKey]Request), resolved: make(map[requestKey]bool)}
	clock := &atomic.Int64{}
	c.clock = func() time.Duration { return time.Duration(clock.Load()) }
	if !public {
		c.native = n // ONLY this package's fixture seam; never an exported opt-in
	}
	t.Cleanup(c.Close)
	return c, a, n, clock
}
func observe(t *testing.T, c *Core, n *fixtureNative, r Request) View {
	t.Helper()
	n.mu.Lock()
	n.requests[key(r.Binding)] = r
	n.mu.Unlock()
	v, err := c.Observe(t.Context(), "alice", r)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func decide(t *testing.T, c *Core, a *fixtureAuthority, v View, choice Choice) Decision {
	t.Helper()
	d := Decision{v.Challenge, choice, fmt.Sprintf("receipt-%d", v.Challenge.ID)}
	a.issue(d)
	if _, err := c.Decide(t.Context(), "alice", d); err != nil {
		t.Fatal(err)
	}
	return d
}
func claimOnce(t *testing.T, c *Core, v View) Claim {
	t.Helper()
	x, err := c.Claim(t.Context(), "alice", v.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	return x
}
func requireError(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture barrier not reached")
	}
}

func TestPermissionCoreTrustedOnce(t *testing.T) {
	c, a, n, _ := fixture(t, false)
	v := observe(t, c, n, request("per_A"))
	d := decide(t, c, a, v, AllowOnce)
	x := claimOnce(t, c, v)
	result, err := c.Consume(t.Context(), "alice", x)
	if err != nil || result.State != ReplyObserved || !result.Attempted || result.Native != NativeUnknown {
		t.Fatalf("ACK is not resolution: %+v %v", result, err)
	}
	_, err = c.Consume(t.Context(), "alice", x)
	requireError(t, err, ErrUsed)
	_, err = c.Claim(t.Context(), "alice", v.Challenge)
	requireError(t, err, ErrUsed)
	duplicate, err := c.Decide(t.Context(), "alice", d)
	if err != nil || duplicate != result {
		t.Fatal("duplicate changed stored disposition", err)
	}
	if calls, effects := n.counts(); calls != 1 || effects != 1 {
		t.Fatal(calls, effects)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "SECRET_CANARY") || strings.Contains(string(encoded), "do-not-expose") {
		t.Fatal("raw scope leaked")
	}
}

func TestPermissionCoreWrongBindings(t *testing.T) {
	for _, field := range []string{"principal", "session", "execution", "request", "root", "workspace", "lineage", "generation", "policy", "toolcall", "message", "fingerprint", "epoch", "challenge", "deadline"} {
		t.Run(field, func(t *testing.T) {
			c, a, n, _ := fixture(t, false)
			v := observe(t, c, n, request("per_A"))
			d := decide(t, c, a, v, AllowOnce)
			bad := d
			switch field {
			case "principal":
				bad.Challenge.Binding.Principal = "mallory"
			case "session":
				bad.Challenge.Binding.SessionID = "ses_B"
			case "execution":
				bad.Challenge.Binding.ExecutionID = "execution-B"
			case "request":
				bad.Challenge.Binding.RequestID = "per_B"
			case "root":
				bad.Challenge.Binding.ProjectRoot = "root-B"
			case "workspace":
				bad.Challenge.Binding.Workspace = "workspace-B"
			case "lineage":
				bad.Challenge.Binding.Lineage = "process-B"
			case "generation":
				bad.Challenge.Binding.Generation++
			case "policy":
				bad.Challenge.Binding.PolicyRevision++
			case "toolcall":
				bad.Challenge.Binding.ToolCallID = "call-B"
			case "message":
				bad.Challenge.Binding.ToolMessageID = "msg_other"
			case "fingerprint":
				bad.Challenge.Binding.ScopeFingerprint[0] ^= 1
			case "epoch":
				bad.Challenge.Epoch = "another-epoch"
			case "challenge":
				bad.Challenge.ID++
			case "deadline":
				bad.Challenge.Deadline++
			}
			if _, err := c.Decide(t.Context(), "alice", bad); err == nil {
				t.Fatal("mismatched binding accepted")
			}
			if _, err := c.Claim(t.Context(), "alice", bad.Challenge); err == nil {
				t.Fatal("mismatched claim accepted")
			}
			if _, err := c.Snapshot(t.Context(), "mallory", v.Challenge); err == nil {
				t.Fatal("unauthorized snapshot")
			}
			x := claimOnce(t, c, v)
			_, err := c.Consume(t.Context(), "mallory", x)
			requireError(t, err, ErrAuthority)
			if calls, _ := n.counts(); calls != 0 {
				t.Fatal("wrong binding dispatched")
			}
			// Only the targeted relationship changed; the original still works.
			if _, err := c.Consume(t.Context(), "alice", x); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPermissionCoreConcurrentConsumers(t *testing.T) {
	for _, count := range []int{2, 32} {
		for _, sameClaim := range []bool{false, true} {
			t.Run(fmt.Sprintf("n%d-shared%v", count, sameClaim), func(t *testing.T) {
				c, a, n, _ := fixture(t, false)
				v := observe(t, c, n, request("per_A"))
				decide(t, c, a, v, AllowOnce)
				n.checkEnter, n.checkRelease = make(chan struct{}), make(chan struct{})
				var shared Claim
				if sameClaim {
					shared = claimOnce(t, c, v)
				}
				start := make(chan struct{})
				out := make(chan error, count)
				var ready sync.WaitGroup
				ready.Add(count)
				for i := 0; i < count; i++ {
					go func() {
						ready.Done()
						<-start
						x := shared
						var err error
						if !sameClaim {
							x, err = c.Claim(t.Context(), "alice", v.Challenge)
						}
						if err == nil {
							_, err = c.Consume(t.Context(), "alice", x)
						}
						out <- err
					}()
				}
				ready.Wait()
				close(start)
				wait(t, n.checkEnter)
				for i := 0; i < count-1; i++ {
					requireError(t, <-out, ErrUsed)
				}
				close(n.checkRelease)
				if err := <-out; err != nil {
					t.Fatal(err)
				}
				if calls, effects := n.counts(); calls != 1 || effects != 1 {
					t.Fatal(calls, effects)
				}
			})
		}
	}
}

func TestPermissionCoreExpiryAndStale(t *testing.T) {
	for _, where := range []string{"pending", "decided", "reserved", "during-read"} {
		t.Run(where, func(t *testing.T) {
			c, a, n, clock := fixture(t, false)
			v := observe(t, c, n, request("per_A"))
			var x Claim
			if where != "pending" {
				decide(t, c, a, v, AllowOnce)
			}
			if where == "reserved" || where == "during-read" {
				x = claimOnce(t, c, v)
			}
			if where == "during-read" {
				n.checkEnter, n.checkRelease = make(chan struct{}), make(chan struct{})
				out := make(chan error, 1)
				go func() { _, err := c.Consume(t.Context(), "alice", x); out <- err }()
				wait(t, n.checkEnter)
				clock.Store(int64(v.Challenge.Deadline))
				close(n.checkRelease)
				requireError(t, <-out, ErrExpired)
			} else {
				clock.Store(int64(v.Challenge.Deadline))
				if where == "reserved" {
					_, err := c.Consume(t.Context(), "alice", x)
					requireError(t, err, ErrExpired)
				} else {
					_, err := c.Claim(t.Context(), "alice", v.Challenge)
					requireError(t, err, ErrExpired)
				}
			}
			old, err := c.Observe(t.Context(), "alice", request("per_A"))
			if err != nil || old.State != Expired || old.Challenge != v.Challenge {
				t.Fatal("observation refreshed TTL", err)
			}
			if calls, _ := n.counts(); calls != 0 {
				t.Fatal(calls)
			}
		})
	}
	for _, changed := range []string{"scope", "session", "lineage", "generation", "workspace", "toolcall"} {
		t.Run(changed, func(t *testing.T) {
			c, a, n, _ := fixture(t, false)
			r := request("per_A")
			v := observe(t, c, n, r)
			decide(t, c, a, v, AllowOnce)
			x := claimOnce(t, c, v)
			switch changed {
			case "scope":
				r.ScopeMaterial = []byte("different native scope")
			case "session":
				r.Binding.SessionID = "ses_other"
			case "lineage":
				r.Binding.Lineage = "process_other"
			case "generation":
				r.Binding.Generation++
			case "workspace":
				r.Binding.Workspace = "workspace_other"
			case "toolcall":
				r.Binding.ToolCallID = "call_other"
			}
			n.mu.Lock()
			n.requests[key(v.Challenge.Binding)] = r
			n.mu.Unlock()
			got, err := c.Consume(t.Context(), "alice", x)
			requireError(t, err, ErrStale)
			if got.State != Stale || got.Attempted {
				t.Fatal(got)
			}
			if calls, _ := n.counts(); calls != 0 {
				t.Fatal(calls)
			}
		})
	}
}

func TestPermissionCoreNativeResolutionAndLoss(t *testing.T) {
	for _, mode := range []string{"TUI-first", "missing", "read-error", "lost-reply", "TUI-between-read-and-reply"} {
		t.Run(mode, func(t *testing.T) {
			c, a, n, _ := fixture(t, false)
			v := observe(t, c, n, request("per_A"))
			decide(t, c, a, v, AllowOnce)
			x := claimOnce(t, c, v)
			switch mode {
			case "TUI-first":
				n.resolved[key(v.Challenge.Binding)] = true
			case "missing":
				n.missing = true
			case "read-error":
				delete(n.requests, key(v.Challenge.Binding))
			case "lost-reply":
				n.lost = true
			case "TUI-between-read-and-reply":
				// A separate fake demonstrates an invocation may race a native
				// writer after a successful read. Public dispatch stays disabled.
				c.native = &tuiRace{fixtureNative: n}
			}
			got, err := c.Consume(t.Context(), "alice", x)
			wantCalls, wantEffects := 0, 0
			if mode == "TUI-first" {
				requireError(t, err, ErrResolved)
				if got.State != ExternallyResolved {
					t.Fatal(got)
				}
			} else {
				requireError(t, err, ErrUncertain)
				if got.State != Uncertain {
					t.Fatal(got)
				}
				if mode == "lost-reply" {
					wantCalls, wantEffects = 1, 1
				} else if mode == "TUI-between-read-and-reply" {
					wantCalls = 1
				}
			}
			_, err = c.Consume(t.Context(), "alice", x)
			requireError(t, err, ErrUsed)
			if calls, effects := n.counts(); calls != wantCalls || effects != wantEffects {
				t.Fatal(calls, effects)
			}
			_, err = c.Reconcile(t.Context(), "alice", v.Challenge, NativeEvidence{Status: NativeUnknown})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Claim(t.Context(), "alice", v.Challenge); err == nil {
				t.Fatal("absence resurrected a claim")
			}
		})
	}
}

type tuiRace struct{ *fixtureNative }

func (n *tuiRace) ReplyOnce(ctx context.Context, b Binding) error {
	n.mu.Lock()
	n.resolved[key(b)] = true
	n.mu.Unlock()
	return n.fixtureNative.ReplyOnce(ctx, b)
}

func TestPermissionCoreCancellationAndClose(t *testing.T) {
	for _, mode := range []string{"cancel-before", "cancel-read", "close-read", "cancel-after-attempt", "close-after-attempt"} {
		t.Run(mode, func(t *testing.T) {
			c, a, n, _ := fixture(t, false)
			v := observe(t, c, n, request("per_A"))
			decide(t, c, a, v, AllowOnce)
			x := claimOnce(t, c, v)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			barrier := make(chan struct{})
			if strings.HasSuffix(mode, "read") {
				n.checkEnter, n.checkRelease = barrier, make(chan struct{})
			} else if strings.HasSuffix(mode, "attempt") {
				n.replyEnter, n.replyRelease = barrier, make(chan struct{})
			} else {
				cancel()
				close(barrier)
			}
			out := make(chan struct{})
			var result View
			var err error
			go func() { result, err = c.Consume(ctx, "alice", x); close(out) }()
			wait(t, barrier)
			if strings.HasPrefix(mode, "close") {
				c.Close()
			} else {
				cancel()
			}
			wait(t, out)
			wantCount := 0
			if strings.HasSuffix(mode, "attempt") {
				wantCount = 1
				requireError(t, err, ErrUncertain)
				if result.State != Uncertain || !result.Attempted {
					t.Fatal(result)
				}
			} else if err == nil || result.Attempted {
				t.Fatal(result, err)
			}
			if calls, effects := n.counts(); calls != wantCount || effects != wantCount {
				t.Fatal(calls, effects)
			}
			if _, err := c.Consume(t.Context(), "alice", x); err == nil {
				t.Fatal("canceled claim refunded")
			}
		})
	}
}

func TestPermissionCorePublicDispatchAndHumanBoundary(t *testing.T) {
	for _, mode := range []string{"no-authority", "no-receipt", "model-text", "same-UID", "socket-only", "not-explicit", "incomplete-presentation", "wrong-human-binding", "verified-but-no-native-channel"} {
		t.Run(mode, func(t *testing.T) {
			c, a, n, _ := fixture(t, true)
			if c.native != nil {
				t.Fatal("public constructor installed dispatch")
			}
			if mode == "no-authority" {
				c.authority = nil
				_, err := c.Observe(t.Context(), "alice", request("per_A"))
				requireError(t, err, ErrAuthority)
				return
			}
			v := observe(t, c, n, request("per_A"))
			d := Decision{v.Challenge, AllowOnce, "receipt-A"}
			a.issue(d)
			e := a.receipts[d.ReceiptID]
			switch mode {
			case "no-receipt":
				a.revoke(d.ReceiptID)
			case "model-text":
				e.Channel = "model_says_approved"
			case "same-UID":
				e.Channel = "same_uid_peer"
			case "socket-only":
				e.Channel = "unix_socket"
			case "not-explicit":
				e.ExplicitConfirmation = false
			case "incomplete-presentation":
				e.CompletePresentation = false
			case "wrong-human-binding":
				e.Decision.Challenge.Binding.ExecutionID = "execution-B"
			}
			if mode != "no-receipt" {
				a.receipts[d.ReceiptID] = e
			}
			_, err := c.Decide(t.Context(), "alice", d)
			if mode == "verified-but-no-native-channel" {
				if err != nil {
					t.Fatal(err)
				}
				x := claimOnce(t, c, v)
				result, err := c.Consume(t.Context(), "alice", x)
				requireError(t, err, ErrDisabled)
				if result.State != Canceled || result.Attempted || result.Native != NativePending {
					t.Fatal(result)
				}
			} else {
				requireError(t, err, ErrHuman)
			}
			if calls, effects := n.counts(); calls != 0 || effects != 0 {
				t.Fatal(calls, effects)
			}
		})
	}
}

func TestPermissionCoreRestart(t *testing.T) {
	var previous Claim
	var old Decision
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		c, a, n, _ := fixture(t, false)
		v := observe(t, c, n, request("per_A"))
		if seen[v.Challenge.Epoch] {
			t.Fatal("epoch reused")
		}
		seen[v.Challenge.Epoch] = true
		if i > 0 {
			_, err := c.Consume(t.Context(), "alice", previous)
			requireError(t, err, ErrUsed)
			a.issue(old)
			_, err = c.Decide(t.Context(), "alice", old)
			requireError(t, err, ErrBinding)
			forged := old
			forged.Challenge = v.Challenge
			_, err = c.Decide(t.Context(), "alice", forged)
			requireError(t, err, ErrHuman)
		}
		old = decide(t, c, a, v, AllowOnce)
		previous = claimOnce(t, c, v)
		c.Close()
		if calls, effects := n.counts(); calls != 0 || effects != 0 {
			t.Fatal("restart replay", calls, effects)
		}
	}
	c, _, n, _ := fixture(t, false)
	r := request("per_old_mapping_lost")
	r.Binding.ExecutionID = ""
	_, err := c.Observe(t.Context(), "alice", r)
	requireError(t, err, ErrInput)
	if calls, _ := n.counts(); calls != 0 {
		t.Fatal(calls)
	}
}
