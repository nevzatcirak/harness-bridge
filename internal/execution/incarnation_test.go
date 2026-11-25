package execution

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
)

// ADR-024 A1 S7 (D21): the epoch is an ADDED verification condition. The run records the epoch armed at
// admission; the scope supplies an epoch only when it is armed and equal; the view reads the epoch before
// and after its snapshot. Real manager, real observer, fake native; no live call.

// epochVar is a settable incarnation source.
type epochVar struct {
	mu    sync.Mutex
	epoch string
	errs  []error // consumed one per call before the value is returned
	// calls counts reads; from the flipAt-th read (when flipAt > 0) the value returned is flipTo.
	calls, flipAt int
	flipTo        string
}

func (e *epochVar) Set(v string) { e.mu.Lock(); e.epoch = v; e.mu.Unlock() }
func (e *epochVar) NativeEpoch(context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.errs) > 0 {
		err := e.errs[0]
		e.errs = e.errs[1:]
		return "", err
	}
	e.calls++
	if e.flipAt > 0 && e.calls >= e.flipAt {
		return e.flipTo, nil
	}
	return e.epoch, nil
}

func epochCfg(e *epochVar) PermissionSourceConfig { return PermissionSourceConfig{Incarnation: e} }

func attributedTo(t *testing.T, fx *sourceFixture) opencode.ObservedPermission {
	t.Helper()
	batch, err := fx.src.PermissionView(context.Background(), srcSession)
	if err != nil || len(batch.Permissions) != 1 {
		t.Fatalf("view = %+v %v", batch, err)
	}
	return batch.Permissions[0]
}

// Control: a run admitted under an armed epoch that is still current is verified, and a reply goes out.
func TestRunAdmittedUnderTheCurrentEpochIsVerified(t *testing.T) {
	fx := newSourceFixture(t, epochCfg(&epochVar{epoch: "e1"}))
	if p := attributedTo(t, fx); p.Attribution != "verified" || p.ExecutionID != fx.x.ID {
		t.Fatalf("control: %+v", p)
	}
	res, err := fx.m.ReplyPermission(context.Background(), fx.in())
	if err != nil || res.Outcome != "replied" {
		t.Fatalf("control reply: %+v %v", res, err)
	}
}

// An execution submitted before a replacement can never be verified afterwards: not_verified, zero native
// writes, no attempt line; pending_permission is count-only.
func TestRunSubmittedBeforeAReplacementIsUnverifiable(t *testing.T) {
	ev := &epochVar{epoch: "e1"}
	fx := newSourceFixture(t, epochCfg(ev))
	if p := attributedTo(t, fx); p.Attribution != "verified" {
		t.Fatalf("precondition: %+v", p)
	}
	ev.Set("e2") // the instance was disposed, reloaded or the stream re-connected: a new epoch
	if p := attributedTo(t, fx); p.Attribution == "verified" {
		t.Fatalf("a run of the old epoch was verified under the new one: %+v", p)
	}
	_, err := fx.m.ReplyPermission(context.Background(), fx.in())
	wantErr(t, err, "FORBIDDEN", ReasonNotVerified)
	fx.noWrites("run submitted before a replacement")
	// the old epoch coming back does not help either way: the run is verifiable only under the epoch it
	// was admitted under, and that value is the Bridge's own memory of one armed period
	ev.Set("e1")
	if p := attributedTo(t, fx); p.Attribution != "verified" {
		t.Logf("note: a source that returns the very same value again verifies again (%+v); the tracker never reuses a token", p)
	}
}

// A run admitted while no epoch was armed is never verifiable, even once an epoch is armed afterwards.
func TestRunAdmittedWhileUnarmedIsNeverVerified(t *testing.T) {
	ev := &epochVar{epoch: ""} // not armed at admission
	fx := newSourceFixture(t, epochCfg(ev))
	ev.Set("e1") // armed later
	if p := attributedTo(t, fx); p.Attribution == "verified" {
		t.Fatalf("a run admitted while unarmed was verified: %+v", p)
	}
	_, err := fx.m.ReplyPermission(context.Background(), fx.in())
	wantErr(t, err, "FORBIDDEN", ReasonNotVerified)
	fx.noWrites("run admitted while unarmed")
}

// A source that never arms (the tracker has not seen its first heartbeat, or the stream is down) verifies
// nothing: the run has no submit epoch, the scope has no epoch, the request stays unattributed.
func TestSourceThatStaysUnarmedVerifiesNothing(t *testing.T) {
	fx := newSourceFixture(t, epochCfg(&epochVar{epoch: ""}))
	if p := attributedTo(t, fx); p.Attribution == "verified" || p.Reason != "identity_unverified" {
		t.Fatalf("an unarmed source verified a request: %+v", p)
	}
	_, err := fx.m.ReplyPermission(context.Background(), fx.in())
	wantErr(t, err, "FORBIDDEN", ReasonNotVerified)
	fx.noWrites("source never armed")
}

// A failed read of the source at admission leaves the run without a submit epoch (fail closed).
func TestAdmissionReadErrorLeavesTheRunUnverifiable(t *testing.T) {
	ev := &epochVar{epoch: "e1", errs: []error{errors.New("down")}} // the admission read fails
	fx := newSourceFixture(t, epochCfg(ev))
	if p := attributedTo(t, fx); p.Attribution == "verified" {
		t.Fatalf("a run whose admission read failed was verified: %+v", p)
	}
	_, err := fx.m.ReplyPermission(context.Background(), fx.in())
	wantErr(t, err, "FORBIDDEN", ReasonNotVerified)
	fx.noWrites("admission read error")
}

// The source has an epoch but the manager was never given the source: no run has a submit epoch, so
// nothing is verified (the wiring must install the same source in both places).
func TestSourceEpochWithoutManagerIncarnationVerifiesNothing(t *testing.T) {
	fx := newSourceFixture(t, PermissionSourceConfig{})
	src, err := NewOpenCodePermissionSource(fx.cli, fx.m, epochCfg(&epochVar{epoch: "e1"}))
	if err != nil {
		t.Fatal(err)
	}
	batch, err := src.PermissionView(context.Background(), srcSession)
	if err != nil || len(batch.Permissions) != 1 || batch.Permissions[0].Attribution == "verified" {
		t.Fatalf("view = %+v %v", batch, err)
	}
}

// The view checks the epoch once more AFTER its snapshot (viewAcrossEpoch). The observer re-checks its own
// scope while it reads, so a change during the native reads already fails the view; this layer catches a
// change after the observer's last check. The observer's reads interleave non-deterministically, so the
// layer is tested on its own, with a snapshot that changes the epoch at a controlled moment.
func verifiedBatch() opencode.PermissionObservationBatch {
	return opencode.PermissionObservationBatch{Status: "available", Permissions: []opencode.ObservedPermission{
		{RequestID: srcPerm, SessionID: srcSession, State: "pending", Attribution: "verified", ExecutionID: "x1", UserMessageID: "msg_1", Reason: "exact_native_parent_and_tool"},
		{RequestID: "per_0002", SessionID: srcSession, State: "pending", Attribution: "conflicting", Reason: "native_binding_conflict"},
	}}
}

func viewWith(t *testing.T, start, during string) opencode.PermissionObservationBatch {
	t.Helper()
	ev := &epochVar{epoch: start}
	src := &OpenCodePermissionSource{cfg: epochCfg(ev)}
	b, err := src.viewAcrossEpoch(context.Background(), func() (opencode.PermissionObservationBatch, error) {
		ev.Set(during)
		return verifiedBatch(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestViewKeepsAttributionWhenTheEpochIsUnchanged(t *testing.T) {
	b := viewWith(t, "e1", "e1")
	if b.Permissions[0].Attribution != "verified" || b.Permissions[0].ExecutionID != "x1" {
		t.Fatalf("an unchanged epoch downgraded the view: %+v", b.Permissions[0])
	}
}

func TestViewDowngradesWhenTheEpochChangesAfterTheSnapshotStarted(t *testing.T) {
	for name, to := range map[string]string{"another epoch": "e2", "no longer armed": ""} {
		b := viewWith(t, "e1", to)
		p := b.Permissions[0]
		if p.Attribution != "unattributed" || p.Reason != "epoch_changed" || p.ExecutionID != "" || p.UserMessageID != "" {
			t.Errorf("%s: %+v", name, p)
		}
		if c := b.Permissions[1]; c.Attribution != "conflicting" || c.Reason != "native_binding_conflict" {
			t.Errorf("%s: a non-verified entry was touched: %+v", name, c)
		}
	}
	// the batch of the observer is not modified in place
	orig := verifiedBatch()
	_ = unattributeAll(orig, "epoch_changed")
	if orig.Permissions[0].Attribution != "verified" {
		t.Error("unattributeAll modified its input")
	}
}

func TestViewWithoutASourceIsUnchanged(t *testing.T) {
	src := &OpenCodePermissionSource{}
	b, err := src.viewAcrossEpoch(context.Background(), func() (opencode.PermissionObservationBatch, error) { return verifiedBatch(), nil })
	if err != nil || b.Permissions[0].Attribution != "verified" {
		t.Fatalf("no source: %+v %v", b, err)
	}
}

func TestViewErrorIsPassedThroughUnchanged(t *testing.T) {
	ev := &epochVar{epoch: "e1"}
	src := &OpenCodePermissionSource{cfg: epochCfg(ev)}
	want := errors.New("snapshot failed")
	if _, err := src.viewAcrossEpoch(context.Background(), func() (opencode.PermissionObservationBatch, error) {
		return opencode.PermissionObservationBatch{}, want
	}); err != want {
		t.Fatalf("err = %v", err)
	}
}

// A change DURING the native reads never yields a verified request either: the observer's own scope
// re-check refuses the view (an error) or it is downgraded; both are fail closed.
func TestEpochChangingDuringTheNativeReadsNeverVerifies(t *testing.T) {
	ev := &epochVar{epoch: "e1"}
	fx := newSourceFixture(t, epochCfg(ev))
	fx.nat.mu.Lock()
	fx.nat.getHook = func(path string, w http.ResponseWriter, r *http.Request) bool {
		if path == "/permission" {
			ev.Set("e2")
		}
		return false
	}
	fx.nat.mu.Unlock()
	batch, err := fx.src.PermissionView(context.Background(), srcSession)
	for _, p := range batch.Permissions {
		if p.Attribution == "verified" {
			t.Fatalf("verified across a replacement: %+v (err %v)", p, err)
		}
	}
	fx.nat.mu.Lock()
	fx.nat.getHook = nil
	fx.nat.mu.Unlock()
	_, rerr := fx.m.ReplyPermission(context.Background(), fx.in())
	wantErr(t, rerr, "FORBIDDEN", ReasonNotVerified)
	fx.noWrites("epoch changed during the native reads")
}

// allow_once stays refused with zero native calls when an epoch makes everything else verifiable.
func TestAllowOnceStaysRefusedWithAnEpoch(t *testing.T) {
	fx := newSourceFixture(t, epochCfg(&epochVar{epoch: "e1"}))
	before := len(fx.nat.reqsCopy())
	in := fx.in()
	in.Decision = "allow_once"
	_, err := fx.m.ReplyPermission(context.Background(), in)
	if err == nil {
		t.Fatal("allow_once was accepted")
	}
	if after := len(fx.nat.reqsCopy()); after != before {
		t.Fatalf("allow_once made %d native calls", after-before)
	}
	fx.noWrites("allow_once with an epoch")
}

func (n *nativeFake) reqsCopy() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.reqs...)
}

// outerFlip is a source whose epoch changes exactly on the SECOND read that carries no deadline. The reads
// viewAcrossEpoch makes with the view's own context (before and after the snapshot) have no deadline; every
// read the observer makes while it snapshots (its scope checks) runs under the observer's deadline. So the
// flip lands after all of the observer's own checks and before nothing but viewAcrossEpoch's second read:
// only that layer can see it (#414 P2-1).
type outerFlip struct {
	mu     sync.Mutex
	epoch  string
	flipTo string
	outer  int // reads without a deadline since arm
	armed  bool
}

func (o *outerFlip) arm() { o.mu.Lock(); o.outer, o.armed = 0, true; o.mu.Unlock() }
func (o *outerFlip) NativeEpoch(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && o.armed {
		o.outer++
		if o.outer == 2 {
			return o.flipTo, nil // the read right after the snapshot
		}
	}
	return o.epoch, nil
}

// THROUGH PermissionView: the production path, not viewAcrossEpoch on its own. With the call replaced by a
// direct call of the snapshot closure this test goes RED (the second read never happens).
func TestPermissionViewChecksTheEpochAgainAfterTheSnapshot(t *testing.T) {
	for name, flipTo := range map[string]string{"another epoch": "e2", "no longer armed": ""} {
		of := &outerFlip{epoch: "e1", flipTo: flipTo}
		fx := newSourceFixture(t, PermissionSourceConfig{Incarnation: of})
		of.arm()
		p := attributedTo(t, fx) // PermissionView
		if p.Attribution == "verified" || p.Reason != "epoch_changed" || p.ExecutionID != "" || p.UserMessageID != "" {
			t.Errorf("%s: a view that ended under another epoch attributed a request: %+v", name, p)
		}
		of.mu.Lock()
		if of.outer != 2 {
			t.Errorf("%s: %d reads without a deadline, want 2 (before and after the snapshot)", name, of.outer)
		}
		of.mu.Unlock()
		// the epoch stays changed: the reply path reads a fresh view of its own (under its own deadline, so
		// the one-shot flip no longer applies) and is refused, with no native write and no attempt line
		of.mu.Lock()
		of.epoch, of.armed = flipTo, false
		of.mu.Unlock()
		_, err := fx.m.ReplyPermission(context.Background(), fx.in())
		wantErr(t, err, "FORBIDDEN", ReasonNotVerified)
		fx.noWrites(name + ": reply after a view under another epoch")
	}
}

// Control: with no change the same view is verified (the flip is what the test above detects).
func TestPermissionViewKeepsAttributionWhenTheEpochHoldsAcrossTheSnapshot(t *testing.T) {
	of := &outerFlip{epoch: "e1", flipTo: "e1"}
	fx := newSourceFixture(t, PermissionSourceConfig{Incarnation: of})
	of.arm()
	if p := attributedTo(t, fx); p.Attribution != "verified" {
		t.Fatalf("control: %+v", p)
	}
}
