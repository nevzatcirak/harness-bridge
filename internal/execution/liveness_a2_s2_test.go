package execution

// ADR-017 A2 S2 (D32): the running-tool object, the TOOL_RUNNING_LONG explanation,
// the absent/stale case and the wedged retry with a live/stale part. A1's
// NO_PROGRESS is never suppressed by a running tool. Every fixture is fabricated;
// no live service is involved.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
)

// a2ToolPart builds one native tool part. status is the native
// state.status (running/completed/error); start and end are epoch ms (0 omits).
func a2ToolPart(callID, name, status string, start, end int64) string {
	t := fmt.Sprintf(`"start":%d`, start)
	if end > 0 {
		t += fmt.Sprintf(`,"end":%d`, end)
	}
	return fmt.Sprintf(`{"type":"tool","callID":%q,"tool":%q,"state":{"status":%q,"time":{%s}}}`,
		callID, name, status, t)
}

// a2BusyTool is the one-assistant busy evidence with the given running tool.
func a2BusyTool(now time.Time, parts ...string) livenessEvidence {
	return a2Evidence(now, opencode.Status{Type: "busy"}, true,
		a2Assistant(now.Add(-10*time.Minute).UnixMilli(), 0, "", parts...))
}

// The running tool never suppresses A1's NO_PROGRESS: 121 s is a stall, 600 s
// (the default tool_long_s) adds the explanation, and the tool object is
// present throughout.
func TestA2S2BusyRunningToolDoesNotSuppressNoProgress(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	cases := []struct {
		age         time.Duration
		stall, expl bool
	}{
		{time.Second, false, false},
		{119 * time.Second, false, false},
		{120 * time.Second, false, false}, // A1 uses "more than", not "at least"
		{121 * time.Second, true, false},
		{10 * time.Minute, true, true}, // the explanation boundary is "at or after"
		{11 * time.Minute, true, true},
	}
	for _, c := range cases {
		t.Run(c.age.String(), func(t *testing.T) {
			start := now.Add(-c.age)
			e := a2BusyTool(now, a2ToolPart("call_1", "bash", "running", start.UnixMilli(), 0))
			l := assessLiveness(now, a2Threshold, e)
			if l.StallSuspected != c.stall || (l.StallReason == "NO_PROGRESS") != c.stall {
				t.Fatalf("stall = %v/%q, want %v: %+v", l.StallSuspected, l.StallReason, c.stall, l)
			}
			if l.Tool == nil || l.Tool.ToolName != "bash" || !l.Tool.RunningSince.Equal(start) {
				t.Fatalf("tool %+v, want bash at %v", l.Tool, start)
			}
			if got := l.Explanation == "TOOL_RUNNING_LONG"; got != c.expl {
				t.Fatalf("explanation %q, want TOOL_RUNNING_LONG=%v", l.Explanation, c.expl)
			}
			if l.Explanation != "" && l.Explanation != "TOOL_RUNNING_LONG" {
				t.Fatalf("unexpected explanation %q", l.Explanation)
			}
		})
	}
}

// A busy execution with no tool keeps A1's boundary (119 no, 121 yes) and has no
// tool object and no explanation.
func TestA2S2BusyWithoutTool(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	for age, want := range map[time.Duration]bool{119 * time.Second: false, 121 * time.Second: true} {
		e := a2Evidence(now, opencode.Status{Type: "busy"}, true,
			a2Assistant(now.Add(-age).UnixMilli(), 0, "", ""))
		l := assessLiveness(now, a2Threshold, e)
		if l.StallSuspected != want {
			t.Fatalf("%v: stall = %+v, want %v", age, l, want)
		}
		if l.Tool != nil || l.Explanation != "" {
			t.Fatalf("%v: tool/explanation without a tool: %+v", age, l)
		}
	}
}

// A running part with an absent native status is stale: it is not a running
// tool and never TOOL_RUNNING_LONG; A1's rules still apply.
func TestA2S2AbsentStaleRunningPartIsNotARunningTool(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	start := now.Add(-10 * time.Minute)
	// Completed assistant without finish/error -> A1 COMPLETED_WITHOUT_TERMINAL_EVIDENCE.
	e := a2Evidence(now, opencode.Status{Type: "idle"}, true,
		a2Assistant(start.UnixMilli(), now.Add(-time.Minute).UnixMilli(), "", "",
			a2ToolPart("call_1", "bash", "running", start.UnixMilli(), 0)))
	l := assessLiveness(now, a2Threshold, e)
	if l.NativeStatus != "absent" || l.StallReason != "COMPLETED_WITHOUT_TERMINAL_EVIDENCE" || !l.StallSuspected {
		t.Fatalf("A1 rule changed: %+v", l)
	}
	if l.Tool != nil || l.Explanation != "" {
		t.Fatalf("a stale part became a running tool: %+v", l)
	}
	// An unfinished assistant under an absent status is not a stall either.
	e = a2Evidence(now, opencode.Status{}, false,
		a2Assistant(start.UnixMilli(), 0, "", "", a2ToolPart("call_1", "bash", "running", start.UnixMilli(), 0)))
	l = assessLiveness(now, a2Threshold, e)
	if l.StallSuspected || l.Tool != nil || l.Explanation != "" {
		t.Fatalf("absent unfinished assistant: %+v", l)
	}
}

// A wedged retry with a live running part keeps A1's NO_PROGRESS, carries the
// PROVIDER_RETRYING degradation and gains the tool object; a long-running tool
// adds the explanation.
func TestA2S2WedgedRetryWithLiveTool(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	next := now.Add(-5 * time.Minute)
	for _, c := range []struct {
		age     time.Duration
		explain bool
	}{
		{4 * time.Minute, false},
		{10 * time.Minute, true},
	} {
		t.Run(c.age.String(), func(t *testing.T) {
			start := now.Add(-c.age)
			st := a2RetryStatus(2, next, "account_rate_limit", "")
			e := a2Evidence(now, st, true,
				a2Assistant(start.UnixMilli(), 0, "", "", a2ToolPart("call_1", "bash", "running", start.UnixMilli(), 0)))
			e.retrySince = now.Add(-30 * time.Second) // young attempt: not retry_overdue
			l := assessLiveness(now, a2Threshold, e)
			if l.StallReason != "NO_PROGRESS" || !l.StallSuspected {
				t.Fatalf("wedged retry lost NO_PROGRESS: %+v", l)
			}
			if l.DegradationReason != "PROVIDER_RETRYING" || !l.Degraded || l.RetryOverdue {
				t.Fatalf("degradation %+v", l)
			}
			if l.Tool == nil || l.Tool.ToolName != "bash" || !l.Tool.RunningSince.Equal(start) {
				t.Fatalf("tool %+v", l.Tool)
			}
			if got := l.Explanation == "TOOL_RUNNING_LONG"; got != c.explain {
				t.Fatalf("explanation %q", l.Explanation)
			}
		})
	}
}

// A wedged retry with a stale running part (a later completed part for the same
// call) is NO_PROGRESS with no tool object and no explanation.
func TestA2S2WedgedRetryWithStaleTool(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	start := now.Add(-10 * time.Minute)
	st := a2RetryStatus(2, now.Add(-5*time.Minute), "", "")
	e := a2Evidence(now, st, true, a2Assistant(start.UnixMilli(), 0, "", "",
		a2ToolPart("call_1", "bash", "running", start.UnixMilli(), 0),
		a2ToolPart("call_1", "bash", "completed", start.UnixMilli(), now.Add(-9*time.Minute).UnixMilli())))
	e.retrySince = now.Add(-30 * time.Second)
	l := assessLiveness(now, a2Threshold, e)
	if l.StallReason != "NO_PROGRESS" || !l.StallSuspected || l.DegradationReason != "PROVIDER_RETRYING" {
		t.Fatalf("wedged retry %+v", l)
	}
	if l.Tool != nil || l.Explanation != "" {
		t.Fatalf("stale part reported as a running tool: %+v", l)
	}
}

// A healthy retry (a future next) is degradation only: no tool object, no
// explanation, no stall.
func TestA2S2HealthyRetryHasNoTool(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	start := now.Add(-10 * time.Minute)
	st := a2RetryStatus(2, now.Add(5*time.Minute), "", "")
	e := a2Evidence(now, st, true,
		a2Assistant(start.UnixMilli(), 0, "", "", a2ToolPart("call_1", "bash", "running", start.UnixMilli(), 0)))
	e.retrySince = now.Add(-30 * time.Second)
	l := assessLiveness(now, a2Threshold, e)
	if l.StallSuspected || l.StallReason != "" || l.DegradationReason != "PROVIDER_RETRYING" {
		t.Fatalf("healthy retry %+v", l)
	}
	if l.Tool != nil || l.Explanation != "" {
		t.Fatalf("healthy retry carried a tool: %+v", l)
	}
}

// A pending native permission suppresses the stall reason and its explanation
// (including the tool object it qualifies), and nothing else.
func TestA2S2PendingPermissionSuppressesTheExplanation(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	e := a2BusyTool(now, a2ToolPart("call_1", "bash", "running", now.Add(-10*time.Minute).UnixMilli(), 0))
	e.permissionPending = true
	l := assessLiveness(now, a2Threshold, e)
	if l.StallSuspected || l.StallReason != "" || l.Explanation != "" || l.Tool != nil {
		t.Fatalf("permission did not suppress the explanation: %+v", l)
	}
}

// The tool name is untrusted display data: at most 64 bytes, the closed
// character set, or "other".
func TestA2S2ToolNameIsSanitized(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	cases := []struct{ in, want string }{
		{"bash", "bash"},
		{"mcp__server__tool-1.2", "mcp__server__tool-1.2"},
		{strings.Repeat("a", 65), strings.Repeat("a", 64)},
		{"bad name!", "other"},
		{"has/slash", "other"},
		{"", "other"},
		{"baş", "other"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			e := a2BusyTool(now, a2ToolPart("call_1", c.in, "running", now.Add(-time.Second).UnixMilli(), 0))
			l := assessLiveness(now, a2Threshold, e)
			if l.Tool == nil || l.Tool.ToolName != c.want {
				t.Fatalf("tool name %q, want %q: %+v", l.Tool, c.want, l.Tool)
			}
			if len(l.Tool.ToolName) > 64 {
				t.Fatalf("tool name longer than 64 bytes: %d", len(l.Tool.ToolName))
			}
		})
	}
}

// A native start after observed_at is ignored (hostile timestamp), so no tool
// object and no explanation.
func TestA2S2FutureToolStartIsIgnored(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	e := a2BusyTool(now, a2ToolPart("call_1", "bash", "running", now.Add(time.Minute).UnixMilli(), 0))
	e.started = now.Add(-time.Second)
	l := assessLiveness(now, a2Threshold, e)
	if l.Tool != nil || l.Explanation != "" || l.StallSuspected {
		t.Fatalf("future tool start used: %+v", l)
	}
}

// The oldest live running tool of the newest correlated assistant is the one
// reported, and a newer assistant without a tool ends the search.
func TestA2S2OldestLiveToolOfTheNewestAssistant(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	old := now.Add(-5 * time.Minute)
	young := now.Add(-3 * time.Minute)
	e := a2BusyTool(now,
		a2ToolPart("call_1", "read", "running", young.UnixMilli(), 0),
		a2ToolPart("call_2", "bash", "running", old.UnixMilli(), 0))
	l := assessLiveness(now, a2Threshold, e)
	if l.Tool == nil || l.Tool.ToolName != "bash" || !l.Tool.RunningSince.Equal(old) {
		t.Fatalf("oldest tool %+v, want bash at %v", l.Tool, old)
	}
	// A newer assistant (a later created timestamp) with no tool is the one
	// inspected; the older assistant's running tool is not the newest.
	news := a2Evidence(now, opencode.Status{Type: "busy"}, true,
		a2Assistant(old.UnixMilli(), 0, "", "", a2ToolPart("call_1", "bash", "running", old.UnixMilli(), 0)),
		a2Assistant(now.Add(-time.Minute).UnixMilli(), 0, "", "", textPart(now.Add(-time.Minute))))
	news.started = now.Add(-10 * time.Minute)
	l = assessLiveness(now, a2Threshold, news)
	if l.Tool != nil || l.Explanation != "" {
		t.Fatalf("tool taken from an older assistant: %+v", l)
	}
}

// The explanation is display-only: it never sets stall_suspected on its own.
func TestA2S2ExplanationDoesNotSetStallSuspected(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	start := now.Add(-11 * time.Minute)
	e := a2BusyTool(now,
		a2ToolPart("call_1", "bash", "running", start.UnixMilli(), 0),
		textPart(now.Add(-time.Second))) // newer native progress: no NO_PROGRESS
	l := assessLiveness(now, a2Threshold, e)
	if l.Tool == nil || l.Tool.ToolName != "bash" {
		t.Fatalf("tool %+v", l.Tool)
	}
	if l.Explanation != "TOOL_RUNNING_LONG" {
		t.Fatalf("explanation %q", l.Explanation)
	}
	if l.StallSuspected || l.StallReason != "" {
		t.Fatalf("the explanation set a stall: %+v", l)
	}
}

// The JSON is additive and optional: tool/explanation are omitted when absent,
// and a previous parser still decodes the execution.
func TestA2S2JSONContract(t *testing.T) {
	now := time.Date(2026, 11, 21, 17, 48, 5, 0, time.UTC)
	l := &Liveness{
		NativeStatus: "busy",
		Tool:         &Tool{ToolName: "bash", RunningSince: now.Add(-time.Minute)},
		Explanation:  "TOOL_RUNNING_LONG",
		ObservedAt:   now,
	}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var tool struct {
		ToolName     string    `json:"tool_name"`
		RunningSince time.Time `json:"running_since"`
	}
	if err := json.Unmarshal(m["tool"], &tool); err != nil {
		t.Fatalf("tool object: %v (%s)", err, b)
	}
	if tool.ToolName != "bash" || !tool.RunningSince.Equal(now.Add(-time.Minute)) {
		t.Fatalf("tool %+v", tool)
	}
	if string(m["explanation"]) != `"TOOL_RUNNING_LONG"` {
		t.Fatalf("explanation %s", m["explanation"])
	}
	// Optional: a block without a tool omits both fields.
	plain, err := json.Marshal(&Liveness{NativeStatus: "busy", ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	var pm map[string]json.RawMessage
	if err := json.Unmarshal(plain, &pm); err != nil {
		t.Fatal(err)
	}
	if _, ok := pm["tool"]; ok {
		t.Fatalf("empty tool exported: %s", plain)
	}
	if _, ok := pm["explanation"]; ok {
		t.Fatalf("empty explanation exported: %s", plain)
	}
	// A previous execution parser ignores the additive block.
	var previous struct {
		ID    string `json:"execution_id"`
		State string `json:"state"`
	}
	x := Execution{ID: "e", State: "running", Started: now, Updated: now, Liveness: l}
	full, _ := json.Marshal(x)
	if err := json.Unmarshal(full, &previous); err != nil || previous.ID != "e" || previous.State != "running" {
		t.Fatalf("previous parser: %+v %v", previous, err)
	}
}

// The snapshot copy does not alias the tool pointer.
func TestA2S2CopyLivenessTool(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	l := &Liveness{NativeStatus: "busy", Tool: &Tool{ToolName: "bash", RunningSince: now}, ObservedAt: now}
	c := copyLiveness(l)
	if c.Tool == l.Tool {
		t.Fatal("copyLiveness aliased the tool")
	}
	c.Tool.ToolName = "read"
	if l.Tool.ToolName != "bash" {
		t.Fatalf("mutating the copy changed the source: %+v", l.Tool)
	}
}

// End-to-end through the manager: the running tool is exported, NO_PROGRESS is
// not suppressed, the explanation appears at tool_long_s, and the report makes
// no native mutation and changes no state or guard.
func TestA2S2BusyRunningToolIntegration(t *testing.T) {
	f := &fake{status: "idle"}
	start := time.Now()
	clock := &fakeClock{t: start}
	m := NewWithTiming(f, time.Minute, 2*time.Millisecond)
	m.clock = clock.Now
	m.stallThreshold = time.Minute
	m.toolLongThreshold = 2 * time.Minute
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "fixture", "a2-s2-tool-key")
	if err != nil {
		t.Fatal(err)
	}
	busyNow(f)
	assistant(f, x.MessageID, ms(start), 0, "", "", a2RunningTool(start))

	got := waitLiveness(t, m, x.ID, func(x Execution) bool { return x.Liveness != nil && x.Liveness.Tool != nil })
	if got.Liveness.Tool.ToolName != "bash" || !got.Liveness.Tool.RunningSince.Equal(time.UnixMilli(ms(start))) {
		t.Fatalf("tool %+v", got.Liveness.Tool)
	}
	if got.Liveness.StallSuspected || got.Liveness.Explanation != "" {
		t.Fatalf("fresh tool: %+v", got.Liveness)
	}

	clock.Advance(30 * time.Second)
	got = waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && !x.Liveness.ObservedAt.Before(clock.Now())
	})
	if got.Liveness.StallSuspected || got.Liveness.Tool == nil || got.Liveness.Explanation != "" {
		t.Fatalf("30 s: %+v", got.Liveness)
	}

	clock.Advance(31 * time.Second) // 61 s > the 60 s stall threshold
	got = waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && x.Liveness.ObservedAt.After(clock.Now().Add(-time.Second))
	})
	if !got.Liveness.StallSuspected || got.Liveness.StallReason != "NO_PROGRESS" {
		t.Fatalf("61 s: %+v", got.Liveness)
	}
	if got.Liveness.Tool == nil || got.Liveness.Explanation != "" {
		t.Fatalf("61 s tool/explanation: %+v", got.Liveness)
	}

	clock.Advance(60 * time.Second) // 121 s >= the 120 s tool_long threshold
	got = waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && x.Liveness.Explanation == "TOOL_RUNNING_LONG"
	})
	if got.Liveness.Tool == nil || !got.Liveness.StallSuspected || got.Liveness.StallReason != "NO_PROGRESS" {
		t.Fatalf("121 s: %+v", got.Liveness)
	}
	if got.State != "running" {
		t.Fatalf("liveness changed state: %+v", got.State)
	}
	guardHeld(t, m)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends != 1 || f.aborts != 0 {
		t.Fatalf("native mutation: sends=%d aborts=%d", f.sends, f.aborts)
	}
}

// A wedged retry with a live running part through the manager: NO_PROGRESS,
// PROVIDER_RETRYING and the tool object.
func TestA2S2WedgedRetryToolIntegration(t *testing.T) {
	f := &fake{status: "idle"}
	start := time.Now()
	clock := &fakeClock{t: start}
	m := NewWithTiming(f, time.Minute, 2*time.Millisecond)
	m.clock = clock.Now
	m.stallThreshold = time.Minute
	t.Cleanup(m.Close) // the default 10 min tool_long
	x, err := m.Send(context.Background(), "ses_test", "fixture", "a2-s2-retry-key")
	if err != nil {
		t.Fatal(err)
	}
	// The execution started at the fake clock's zero; move past it so the
	// running tool (started after the execution) is the older baseline.
	clock.Advance(5 * time.Minute)
	now := clock.Now()
	setStatus(f, &opencode.Status{Type: "retry", Attempt: a2Raw(`2`), Next: a2Raw(fmt.Sprint(ms(now.Add(-time.Minute))))})
	assistant(f, x.MessageID, ms(now.Add(-5*time.Minute)), 0, "", "",
		a2RunningTool(now.Add(-4*time.Minute)))
	got := waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && x.Liveness.Tool != nil && x.Liveness.StallReason == "NO_PROGRESS"
	})
	l := got.Liveness
	if !l.StallSuspected || l.DegradationReason != "PROVIDER_RETRYING" || l.RetryOverdue {
		t.Fatalf("wedged retry %+v", l)
	}
	if l.Tool.ToolName != "bash" || !l.Tool.RunningSince.Equal(time.UnixMilli(ms(now.Add(-4*time.Minute)))) {
		t.Fatalf("tool %+v", l.Tool)
	}
	if l.Explanation != "" { // 4 min < the 10 min default
		t.Fatalf("premature explanation %q", l.Explanation)
	}
	if got.State != "running" {
		t.Fatalf("state %+v", got.State)
	}
	guardHeld(t, m)
}
