package dashboard

// ADR-023 A1 slice S2 (detail page): the fixtures of every test of this slice. One detail page per liveness
// reason (ADR-017 A1 and A2), the pending-permission rows, a working-tree diff, a Claude Code session and a page
// full of hostile values. All of them use the fixed clock v2Now of S1 and a fake Source; nothing is read from a
// real Bridge.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// s2Session is the session of every S2 detail page: one running execution (the current one) and three ended ones.
func s2Session() Session {
	return Session{Harness: "opencode", ID: "ses_demo0001", Title: "Fix flaky retry test", Root: "/srv/demo/work/payments-api/services/billing",
		LifecycleRoot: "/srv/demo/work", State: "running", Guard: Guard{Held: true, State: "running"},
		CurrentExecution: "7a2c91e0", LastActivity: v2Now.Add(-53 * time.Second)}
}

func ptrTime(t time.Time) *time.Time { return &t }
func ptrInt(n int64) *int64          { return &n }

// s2Executions is the timeline of the fixture: the current execution carries the given liveness block and
// permission summary, then a completed, a failed and an interrupted one (newest first, as the Source supplies them).
func s2Executions(l *Liveness, p *PermissionSummary) []Execution {
	started := v2Now.Add(-8*time.Minute - 50*time.Second)
	end := func(from time.Time, d time.Duration) *time.Time { e := from.Add(d); return &e }
	c1, c2, c3 := v2Now.Add(-27*time.Minute), v2Now.Add(-46*time.Minute), v2Now.Add(-60*time.Minute)
	var unavailable *time.Time
	if l != nil && l.StallReason == "HARNESS_UNREACHABLE" {
		t := v2Now.Add(-3 * time.Minute)
		unavailable = &t
	}
	return []Execution{
		{ID: "7a2c91e0", State: "running", Started: started, Updated: v2Now.Add(-4 * time.Second), Current: true, Liveness: l, Permission: p, UnavailableSince: unavailable},
		{ID: "5d03aa17", State: "completed", Started: c1, Updated: c1.Add(6*time.Minute + 2*time.Second), Ended: end(c1, 6*time.Minute+2*time.Second),
			Liveness: &Liveness{NativeStatus: "absent", StallReason: "NO_PROGRESS", StallSuspected: true, ObservedAt: c1}},
		{ID: "0c9be452", State: "failed", Started: c2, Updated: c2.Add(75 * time.Second), Ended: end(c2, 75*time.Second)},
		{ID: "e41d77b9", State: "interrupted", Started: c3, Updated: c3.Add(3*time.Minute + 30*time.Second), Ended: end(c3, 3*time.Minute+30*time.Second)},
	}
}

const digest64 = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

// s2Liveness is one block per liveness case. The first group is one block per REASON word of ADR-017 A1 and A2;
// the second group are the combinations the page must not get wrong.
func s2Liveness() map[string]*Liveness {
	obs := v2Now.Add(-4 * time.Second)
	th := &LivenessThresholds{NoProgressS: 120, ToolLongS: 600}
	return map[string]*Liveness{
		"healthy":      {NativeStatus: "busy", LastNativeProgressAt: ptrTime(v2Now.Add(-10 * time.Second)), Thresholds: th, ObservedAt: obs},
		"noprogress":   {NativeStatus: "busy", StallSuspected: true, StallReason: "NO_PROGRESS", LastNativeProgressAt: ptrTime(v2Now.Add(-3*time.Minute - 15*time.Second)), Thresholds: th, ObservedAt: obs},
		"no-assistant": {NativeStatus: "absent", StallSuspected: true, StallReason: "NO_CORRELATED_ASSISTANT", Thresholds: th, ObservedAt: obs},
		"no-evidence":  {NativeStatus: "absent", StallSuspected: true, StallReason: "COMPLETED_WITHOUT_TERMINAL_EVIDENCE", Thresholds: th, ObservedAt: obs},
		"unreachable":  {NativeStatus: "unavailable", StallSuspected: true, StallReason: "HARNESS_UNREACHABLE", Thresholds: th, ObservedAt: obs},
		"retrying": {NativeStatus: "retry", RetryAttempt: ptrInt(2), RetryMax: ptrInt(5), RetryNextAt: ptrTime(v2Now.Add(20 * time.Second)), DegradationReason: "PROVIDER_RETRYING", ProgressSource: "native",
			LastNativeProgressAt: ptrTime(v2Now.Add(-10 * time.Second)),
			CauseHint:            "network", CauseSource: "bounded_text", Thresholds: th, ObservedAt: obs},
		"retry-overdue": {NativeStatus: "retry", RetryAttempt: ptrInt(3), RetryMax: ptrInt(5), RetryNextAt: ptrTime(v2Now.Add(-70 * time.Second)), RetryOverdue: true, DegradationReason: "PROVIDER_RETRY_OVERDUE", ProgressSource: "native",
			CauseHint: "rate_limit", CauseSource: "structured", Thresholds: th, ObservedAt: obs},
		"provider-error": {NativeStatus: "absent", DegradationReason: "PROVIDER_ERROR", CauseHint: "auth", CauseSource: "structured", Thresholds: th, ObservedAt: obs},
		"tool-long": {NativeStatus: "busy", Tool: &LivenessTool{Name: "bash", RunningSince: v2Now.Add(-12 * time.Minute)}, Explanation: "TOOL_RUNNING_LONG",
			LastNativeProgressAt: ptrTime(v2Now.Add(-12 * time.Minute)), Thresholds: th, ObservedAt: obs},
		// A wedged retry: the A2.8 Variant A combination (degradation AND the A1 stall), not one state.
		"wedged": {NativeStatus: "retry", RetryAttempt: ptrInt(4), RetryMax: ptrInt(5), RetryNextAt: ptrTime(v2Now.Add(-90 * time.Second)), RetryOverdue: true,
			DegradationReason: "PROVIDER_RETRY_OVERDUE", StallSuspected: true, StallReason: "NO_PROGRESS", ProgressSource: "native", Thresholds: th, ObservedAt: obs},
		// A busy running tool at the long threshold: stalled plus the tool-running explanation.
		"tool-stalled": {NativeStatus: "busy", StallSuspected: true, StallReason: "NO_PROGRESS", Tool: &LivenessTool{Name: "bash", RunningSince: v2Now.Add(-12 * time.Minute)},
			Explanation: "TOOL_RUNNING_LONG", LastNativeProgressAt: ptrTime(v2Now.Add(-12 * time.Minute)), Thresholds: th, ObservedAt: obs},
		// A stale tool part with status absent is not reported as tool running.
		"stale-tool": {NativeStatus: "absent", LastNativeProgressAt: ptrTime(v2Now.Add(-20 * time.Minute)), Thresholds: th, ObservedAt: obs},
		// The words a future Bridge may add: never shown as received, never shown as if known.
		"unknown-words": {NativeStatus: "weird", StallSuspected: true, StallReason: "FUTURE_STALL_<b>x</b>", DegradationReason: "FUTURE_DEGRADATION", Explanation: "FUTURE_EXPLANATION",
			CauseHint: "future_cause", CauseSource: "future_source", ObservedAt: obs},
		// A flag without a reason is not a stall the page can name (it keys on stall_reason, review #459 P2-1).
		"flag-only": {NativeStatus: "busy", StallSuspected: true, ObservedAt: obs},
		// A reason without the flag is still the reason.
		"reason-only": {NativeStatus: "busy", StallSuspected: false, StallReason: "NO_PROGRESS", ObservedAt: obs},
	}
}

// s2Diff is the working tree of the fixture: three files, one of them added, with hunks.
func s2Diff() DiffResult {
	return DiffResult{Files: []DiffFile{
		{Path: "services/billing/retry_test.go", HasPath: true, Status: "modified", Additions: 11, Deletions: 2, HasPatch: true,
			Patch: "--- a/services/billing/retry_test.go\n+++ b/services/billing/retry_test.go\n@@ -18,6 +18,15 @@ func TestRetryBackoff(t *testing.T) {\n     clock := fakeClock()\n-    time.Sleep(50 * time.Millisecond)\n+    clock.Advance(50 * time.Millisecond)\n+    if got := r.Attempts(); got != 3 {\n+        t.Fatalf(\"attempts = %d, want 3\", got)\n+    }\n"},
		{Path: "services/billing/retry.go", HasPath: true, Status: "modified", Additions: 30, Deletions: 5, HasPatch: true,
			Patch: "@@ -40,4 +40,6 @@\n+    delay := backoff(attempt)\n-    delay := time.Second\n \n\\ No newline at end of file\n"},
		{Path: "docs/NOTE.md", HasPath: true, Status: "added", Additions: 1, Deletions: 0, HasPatch: true, Patch: "@@ -0,0 +1 @@\n+Retry backoff is now injected.\n"},
	}}
}

func s2Turns() []Turn {
	return []Turn{
		{MessageID: "msg_turn_one", Created: v2Now.Add(-20 * time.Minute), State: TurnChanges},
		{MessageID: "msg_turn_two", Created: v2Now.Add(-10 * time.Minute), State: TurnNotComputed},
	}
}

// s2Source builds the fake Source of one detail page.
func s2Source(l *Liveness, p *PermissionSummary) *fakeSource {
	s := s2Session()
	return &fakeSource{
		rows:      []Session{s},
		details:   map[string]SessionDetail{"opencode/ses_demo0001": {Session: s, Executions: s2Executions(l, p)}},
		workDiff:  s2Diff(),
		turns:     s2Turns(),
		turnDiffs: map[string]DiffResult{"msg_turn_one": s2Diff()},
	}
}

func s2Page(t *testing.T, src Source, target string) (int, string) {
	t.Helper()
	code, body, _ := v2Get(t, v2Handler(src), target)
	return code, body
}

// hostileSource is a detail page whose every field a Source supplies is an injection attempt.
func hostileSource() *fakeSource {
	evil := `<script>alert(1)</script>"><img src=x onerror=alert(2)>`
	url := `javascript:alert(3)`
	s := Session{Harness: "opencode", ID: "ses_evil0001", Title: evil, Root: "/tmp/" + evil + "/" + url, LifecycleRoot: evil + "https://evil.example/x",
		State: "running", Guard: Guard{Held: true, State: evil}, CurrentExecution: evil, LastActivity: v2Now}
	l := &Liveness{NativeStatus: evil, StallSuspected: true, StallReason: evil, DegradationReason: evil, Explanation: evil, CauseHint: evil, CauseSource: evil,
		Tool: &LivenessTool{Name: evil + url, RunningSince: v2Now}, ObservedAt: v2Now}
	p := &PermissionSummary{Count: 1, Attributed: true, Tool: evil, Kind: url, PatternCount: 1, Digest: evil}
	execs := []Execution{{ID: evil, State: evil, Started: v2Now, Updated: v2Now, Current: true, Liveness: l, Permission: p},
		{ID: "e2", State: "completed", Started: v2Now.Add(-time.Hour), Updated: v2Now, Ended: ptrTime(v2Now)}}
	diff := DiffResult{Files: []DiffFile{
		{Path: evil, HasPath: true, Status: evil, Additions: 1, Deletions: 1, HasPatch: true,
			Patch: "@@ -1 +1 @@\n-<img src=x onerror=alert(4)>\n+</pre><script>alert(5)</script>\n+<a href=\"javascript:alert(6)\">x</a>\n+style=\"background:url(https://evil.example/p.png)\"\n"},
		{Path: "ok.go", HasPath: true, Status: "modified", Additions: 1, Deletions: 0, HasPatch: true, Patch: "@@ -1 +1 @@\n+safe\n"},
	}}
	return &fakeSource{rows: []Session{s}, details: map[string]SessionDetail{"opencode/ses_evil0001": {Session: s, Executions: execs}}, workDiff: diff,
		turns: []Turn{{MessageID: `msg"><script>alert(7)</script>`, Created: v2Now, State: TurnChanges}, {MessageID: "msg_ok", Created: v2Now, State: TurnChanges}}}
}

var (
	s2Mu    sync.Mutex
	s2Cache map[string]string
)

// s2Pages renders every S2 page and state with the fixed clock, once per process (the render is deterministic and
// read-only, and a dozen tests share it); every caller gets its own copy of the map. The names are the golden file
// names.
func s2Pages(t *testing.T) map[string]string {
	t.Helper()
	s2Mu.Lock()
	defer s2Mu.Unlock()
	if s2Cache == nil {
		s2Cache = renderS2Pages(t)
	}
	out := make(map[string]string, len(s2Cache))
	for k, v := range s2Cache {
		out[k] = v
	}
	return out
}

func renderS2Pages(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	get := func(name string, src Source, target string) {
		code, body := s2Page(t, src, target)
		if code != 200 {
			t.Fatalf("%s: GET %s = %d", name, target, code)
		}
		out[name] = body
	}
	for _, name := range []string{"healthy", "noprogress", "no-assistant", "no-evidence", "unreachable", "retrying", "retry-overdue", "provider-error", "tool-long", "wedged", "tool-stalled", "stale-tool", "unknown-words", "flag-only", "reason-only"} {
		get("detail-live-"+name, s2Source(s2Liveness()[name], nil), "/session/opencode/ses_demo0001")
	}
	get("detail-perm-unattributed", s2Source(s2Liveness()["healthy"], &PermissionSummary{Count: 1}), "/session/opencode/ses_demo0001")
	get("detail-perm-attributed", s2Source(s2Liveness()["healthy"], &PermissionSummary{Count: 2, Attributed: true, Tool: "bash", Kind: "bash", PatternCount: 2, Digest: digest64}), "/session/opencode/ses_demo0001")
	get("detail-no-liveness", s2Source(nil, nil), "/session/opencode/ses_demo0001")
	get("detail-hostile", hostileSource(), "/session/opencode/ses_evil0001")
	get("turn", s2Source(s2Liveness()["healthy"], nil), "/session/opencode/ses_demo0001/turn/msg_turn_one")

	noDiff := s2Source(s2Liveness()["healthy"], nil)
	noDiff.workErr, noDiff.turns = errors.New("down"), nil
	noDiff.turnsErr = errors.New("down")
	get("detail-diff-unavailable", noDiff, "/session/opencode/ses_demo0001")
	empty := s2Source(s2Liveness()["healthy"], nil)
	empty.workDiff = DiffResult{}
	get("detail-diff-empty", empty, "/session/opencode/ses_demo0001")

	claude := Session{Harness: "claudecode", ID: "claudecode-4e59c1bd", Title: "Docs pass", Root: "/srv/demo/work/site/docs", State: "unknown", LastActivity: v2Now.Add(-time.Hour)}
	get("detail-claude", &fakeSource{rows: []Session{claude}, details: map[string]SessionDetail{"claudecode/claudecode-4e59c1bd": {Session: claude}}}, "/session/claudecode/claudecode-4e59c1bd")
	return out
}

var _ = strings.Contains
var _ context.Context
