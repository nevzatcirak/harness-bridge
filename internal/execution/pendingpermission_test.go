package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

const (
	testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testPerm   = "per_0001"
)

// sourceFake is a PermissionSource whose batch is built per call.
type sourceFake struct {
	mu       sync.Mutex
	build    func(session string) opencode.PermissionObservationBatch
	err      error
	calls    int
	sessions []string
}

func (s *sourceFake) PermissionView(_ context.Context, session string) (opencode.PermissionObservationBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.sessions = append(s.sessions, session)
	if s.err != nil {
		return opencode.PermissionObservationBatch{}, s.err
	}
	return s.build(session), nil
}
func (s *sourceFake) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func observed(session, execID string) opencode.ObservedPermission {
	return opencode.ObservedPermission{
		RequestID: testPerm, SessionID: session, ToolMessageID: "msg_assistant1", ToolCallID: "call_1",
		Kind: "edit", Tool: "edit", ScopeDigest: testDigest, PatternCount: 2,
		State: "pending", Attribution: "verified", ExecutionID: execID, UserMessageID: "msg_user1",
		Reason: "exact_native_parent_and_tool",
	}
}
func batchOf(status string, ps ...opencode.ObservedPermission) opencode.PermissionObservationBatch {
	return opencode.PermissionObservationBatch{Status: status, Completeness: "point_in_time_only", Permissions: ps}
}

// waitingExecution drives a fake runner to waiting_for_permission with the given
// native pending list. mutate may change the list after the execution exists.
func waitingExecution(t *testing.T, list []opencode.Permission, src *sourceFake) (*Manager, *fake, Execution) {
	t.Helper()
	f := &fake{status: "idle", permissionList: list}
	m := setup(t, f)
	if src != nil {
		m.SetPermissionSource(src)
	}
	x, e := m.Send(context.Background(), "ses_test", "prompt", "perm-key-0001")
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, m, x.ID, "running")
	f.mu.Lock()
	f.permission = true
	f.mu.Unlock()
	return m, f, waitState(t, m, x.ID, "waiting_for_permission")
}

func keys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The count needs no source: it is derived from the permission read the manager
// already does, counts only this session, and counts unattributed requests too.
func TestPendingCountWithoutSource(t *testing.T) {
	list := []opencode.Permission{{SessionID: "ses_test"}, {SessionID: "ses_other"}, {SessionID: "ses_test"}}
	_, _, x := waitingExecution(t, list, nil)
	if x.PendingPermissionCount == nil || *x.PendingPermissionCount != 2 {
		t.Fatalf("count = %v, want 2 (this session only)", x.PendingPermissionCount)
	}
	if x.PendingPermission != nil {
		t.Fatalf("no source: nothing may be reported as verified, got %+v", x.PendingPermission)
	}
}

func TestVerifiedPendingPermissionForThisExecution(t *testing.T) {
	src := &sourceFake{}
	var execID string
	src.build = func(session string) opencode.PermissionObservationBatch {
		return batchOf("available", observed(session, execID))
	}
	f := &fake{status: "idle"}
	m := setup(t, f)
	m.SetPermissionSource(src)
	x, e := m.Send(context.Background(), "ses_test", "prompt", "perm-key-0002")
	if e != nil {
		t.Fatal(e)
	}
	src.mu.Lock()
	execID = x.ID
	src.mu.Unlock()
	waitState(t, m, x.ID, "running")
	f.mu.Lock()
	f.permission = true
	f.mu.Unlock()
	got := waitState(t, m, x.ID, "waiting_for_permission")
	// The probe that set the state may have raced the ID assignment; wait for the
	// detail with the correct ID.
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && got.PendingPermission == nil; {
		time.Sleep(2 * time.Millisecond)
		got, _ = m.Get(context.Background(), x.ID)
	}
	p := got.PendingPermission
	if p == nil {
		t.Fatalf("verified request not exposed: %+v", got)
	}
	want := PendingPermission{PermissionID: testPerm, CallID: "call_1", MessageID: "msg_assistant1", Tool: "edit", Kind: "edit", PatternCount: 2, ScopeDigest: testDigest, Attribution: "verified"}
	if *p != want {
		t.Fatalf("pending_permission = %+v, want %+v", *p, want)
	}
	if got.PendingPermissionCount == nil || *got.PendingPermissionCount != 1 {
		t.Fatalf("count = %v", got.PendingPermissionCount)
	}
	src.mu.Lock()
	asked := append([]string(nil), src.sessions...)
	src.mu.Unlock()
	if len(asked) == 0 || asked[0] != "ses_test" {
		t.Fatalf("source asked for sessions %v, want the execution's native session", asked)
	}
	// Serialized output: exactly the ADR-024 section 3 keys; no pattern, no
	// metadata, no native text, no diagnostic reason, no user message ID.
	if k := keys(t, p); strings.Join(k, ",") != "attribution,call_id,kind,message_id,pattern_count,permission_id,scope_digest,tool" {
		t.Fatalf("pending_permission keys = %v", k)
	}
	raw, _ := json.Marshal(got)
	for _, banned := range []string{"pattern\"", "patterns", "metadata", "exact_native_parent_and_tool", "msg_user1", "always"} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("serialized execution contains %q: %s", banned, raw)
		}
	}
}

func TestPendingPermissionIsCountOnlyUnlessExactlyOneVerifiedPending(t *testing.T) {
	mk := func(mod func(*opencode.ObservedPermission)) opencode.ObservedPermission {
		o := observed("ses_test", "EXEC")
		mod(&o)
		return o
	}
	cases := map[string]opencode.PermissionObservationBatch{
		"unattributed": batchOf("available", mk(func(o *opencode.ObservedPermission) { o.Attribution = "unattributed" })),
		"conflicting":  batchOf("available", mk(func(o *opencode.ObservedPermission) { o.Attribution = "conflicting" })),
		"tool-less": batchOf("available", mk(func(o *opencode.ObservedPermission) {
			o.ToolMessageID, o.ToolCallID, o.Attribution, o.ExecutionID = "", "", "unattributed", ""
		})),
		"other execution":               batchOf("available", mk(func(o *opencode.ObservedPermission) { o.ExecutionID = "someone-else" })),
		"other session":                 batchOf("available", mk(func(o *opencode.ObservedPermission) { o.SessionID = "ses_other" })),
		"resolved externally":           batchOf("available", mk(func(o *opencode.ObservedPermission) { o.State = "resolved_externally" })),
		"observation uncertain batch":   batchOf("observation_uncertain", mk(func(o *opencode.ObservedPermission) {})),
		"observation unavailable batch": batchOf("observation_unavailable", mk(func(o *opencode.ObservedPermission) {})),
		"bad request id":                batchOf("available", mk(func(o *opencode.ObservedPermission) { o.RequestID = "../etc" })),
		"bad digest":                    batchOf("available", mk(func(o *opencode.ObservedPermission) { o.ScopeDigest = "ZZ" })),
		"bad call id":                   batchOf("available", mk(func(o *opencode.ObservedPermission) { o.ToolCallID = "call id with spaces" })),
		"two verified": batchOf("available", mk(func(o *opencode.ObservedPermission) {}),
			mk(func(o *opencode.ObservedPermission) { o.RequestID = "per_0002" })),
		"empty": batchOf("available"),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if got := selectPendingPermission(b, "ses_test", "EXEC"); got != nil {
				t.Fatalf("must be count-only, got %+v", got)
			}
		})
	}
	// And the one good shape is accepted (guards against a selector that refuses everything).
	if got := selectPendingPermission(batchOf("available", observed("ses_test", "EXEC")), "ses_test", "EXEC"); got == nil || got.PermissionID != testPerm {
		t.Fatalf("good batch refused: %+v", got)
	}
}

// Unattributed requests flow through the whole manager as count-only.
func TestManagerReportsUnattributedAsCountOnly(t *testing.T) {
	src := &sourceFake{build: func(session string) opencode.PermissionObservationBatch {
		o := observed(session, "")
		o.Attribution = "unattributed"
		return batchOf("available", o)
	}}
	_, _, x := waitingExecution(t, nil, src)
	if x.PendingPermission != nil {
		t.Fatalf("unattributed request exposed: %+v", x.PendingPermission)
	}
	if x.PendingPermissionCount == nil || *x.PendingPermissionCount != 1 {
		t.Fatalf("count = %v", x.PendingPermissionCount)
	}
}

// An observer failure must not change state reporting: still waiting, count
// present, no detail, never a fabricated verified request.
func TestObserverErrorDropsOnlyTheDetail(t *testing.T) {
	src := &sourceFake{err: errors.New("observer down")}
	_, _, x := waitingExecution(t, nil, src)
	if x.State != "waiting_for_permission" || x.Error != "" || x.Unresolved != nil {
		t.Fatalf("observer failure changed the state report: %+v", x)
	}
	if x.PendingPermission != nil {
		t.Fatalf("detail exposed on observer failure: %+v", x.PendingPermission)
	}
	if x.PendingPermissionCount == nil || *x.PendingPermissionCount != 1 {
		t.Fatalf("count = %v", x.PendingPermissionCount)
	}
	if src.callCount() == 0 {
		t.Fatal("source was never asked")
	}
}

// No pending request, or any other state: neither field, and the source is not
// asked (no extra native reads outside waiting_for_permission).
func TestOtherStatesReportNeitherFieldAndDoNotAskTheSource(t *testing.T) {
	src := &sourceFake{build: func(session string) opencode.PermissionObservationBatch { return batchOf("available") }}
	f := &fake{status: "idle"}
	m := setup(t, f)
	m.SetPermissionSource(src)
	x, e := m.Send(context.Background(), "ses_test", "prompt", "perm-key-0003")
	if e != nil {
		t.Fatal(e)
	}
	run := waitState(t, m, x.ID, "running")
	if run.PendingPermission != nil || run.PendingPermissionCount != nil {
		t.Fatalf("running reports pending fields: %+v", run)
	}
	raw, _ := json.Marshal(run)
	if strings.Contains(string(raw), "pending_permission") {
		t.Fatalf("running JSON contains pending_permission: %s", raw)
	}
	complete(f, x.MessageID, "stop", "")
	done := waitState(t, m, x.ID, "completed")
	if done.PendingPermission != nil || done.PendingPermissionCount != nil {
		t.Fatalf("completed reports pending fields: %+v", done)
	}
	if n := src.callCount(); n != 0 {
		t.Fatalf("source asked %d times without a pending request", n)
	}
}

// When the permission is answered and the run continues, a stale detail is gone.
func TestDetailDisappearsWhenNoLongerWaiting(t *testing.T) {
	src := &sourceFake{}
	f := &fake{status: "idle"}
	m := setup(t, f)
	src.build = func(session string) opencode.PermissionObservationBatch { return batchOf("available") }
	m.SetPermissionSource(src)
	x, _ := m.Send(context.Background(), "ses_test", "prompt", "perm-key-0004")
	waitState(t, m, x.ID, "running")
	f.mu.Lock()
	f.permission = true
	f.mu.Unlock()
	waitState(t, m, x.ID, "waiting_for_permission")
	f.mu.Lock()
	f.permission = false
	f.mu.Unlock()
	got := waitState(t, m, x.ID, "running")
	if got.PendingPermission != nil || got.PendingPermissionCount != nil {
		t.Fatalf("stale pending fields after leaving waiting_for_permission: %+v", got)
	}
}
