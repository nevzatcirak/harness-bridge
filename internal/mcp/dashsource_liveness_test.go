package mcp

import (
	"reflect"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/dashboard"
	"github.com/nevzatcirak/cirak/internal/execution"
)

func dashRow(current string) dashboard.Session {
	return dashboard.Session{Harness: "opencode", ID: "ses_a", CurrentExecution: current}
}

// ADR-023 A1.8 (slice S2): Execution.Liveness and the permission summary in the dashboard Source are copies of what
// the Manager already holds, mapped without a native read, never carrying a permission ID, a call or message ID, a
// pattern or any native text.

func TestDashLivenessIsNilForNoBlock(t *testing.T) {
	if dashLiveness(nil) != nil {
		t.Error("a nil block was mapped to a block")
	}
}

func TestDashLivenessCopiesEveryFieldAndSharesNoPointer(t *testing.T) {
	at := time.Date(2026, 11, 21, 17, 40, 0, 0, time.UTC)
	attempt := int64(3)
	next, last := at.Add(20*time.Second), at.Add(-time.Minute)
	src := &execution.Liveness{
		NativeStatus: "retry", RetryAttempt: &attempt, RetryNextAt: &next, LastNativeProgressAt: &last,
		StallSuspected: true, StallReason: "NO_PROGRESS", DegradationReason: "PROVIDER_RETRY_OVERDUE", Degraded: true, RetryOverdue: true,
		CauseHint: "rate_limit", CauseSource: "structured",
		Tool: &execution.Tool{ToolName: "bash", RunningSince: last}, Explanation: "TOOL_RUNNING_LONG",
		Thresholds: &execution.Thresholds{NoProgressS: 90, ToolLongS: 300}, ObservedAt: at,
	}
	got := dashLiveness(src)
	if got.NativeStatus != "retry" || got.StallReason != "NO_PROGRESS" || !got.StallSuspected || got.DegradationReason != "PROVIDER_RETRY_OVERDUE" || !got.RetryOverdue ||
		got.CauseHint != "rate_limit" || got.CauseSource != "structured" || got.Explanation != "TOOL_RUNNING_LONG" || !got.ObservedAt.Equal(at) {
		t.Errorf("mapped fields: %+v", got)
	}
	if got.RetryAttempt == nil || *got.RetryAttempt != 3 || got.RetryNextAt == nil || !got.RetryNextAt.Equal(next) || got.LastNativeProgressAt == nil || !got.LastNativeProgressAt.Equal(last) {
		t.Errorf("mapped pointers: %+v", got)
	}
	if got.Tool == nil || got.Tool.Name != "bash" || !got.Tool.RunningSince.Equal(last) || got.Thresholds == nil || got.Thresholds.NoProgressS != 90 || got.Thresholds.ToolLongS != 300 {
		t.Errorf("mapped tool and thresholds: %+v", got)
	}
	// T10: the copy shares no pointer with the Manager's record
	attempt, next, last = 99, at.Add(time.Hour), at.Add(time.Hour)
	src.Tool.ToolName = "changed"
	src.Thresholds.NoProgressS = 1
	if *got.RetryAttempt != 3 || !got.RetryNextAt.Equal(at.Add(20*time.Second)) || !got.LastNativeProgressAt.Equal(at.Add(-time.Minute)) || got.Tool.Name != "bash" || got.Thresholds.NoProgressS != 90 {
		t.Error("the dashboard copy aliases the Manager's record")
	}
}

func TestDashPermissionMapsOnlyTheFieldsADR024ShowsChat(t *testing.T) {
	if dashPermission(execution.Execution{}) != nil {
		t.Error("an execution with nothing pending has a permission summary")
	}
	n := 2
	got := dashPermission(execution.Execution{PendingPermissionCount: &n})
	if got == nil || got.Count != 2 || got.Attributed || got.Tool != "" || got.Kind != "" || got.Digest != "" || got.PatternCount != 0 {
		t.Errorf("a count without an attributed request: %+v", got)
	}
	e := execution.Execution{PendingPermissionCount: &n, PendingPermission: &execution.PendingPermission{
		PermissionID: "per_secret", CallID: "call_secret", MessageID: "msg_secret", Tool: "bash", Kind: "bash", PatternCount: 3, ScopeDigest: "d1", Attribution: "verified"}}
	got = dashPermission(e)
	if got == nil || !got.Attributed || got.Count != 2 || got.Tool != "bash" || got.Kind != "bash" || got.PatternCount != 3 || got.Digest != "d1" {
		t.Errorf("an attributed request: %+v", got)
	}
	// the summary type has no field that could carry an ID, a pattern or native text
	for i := 0; i < reflect.TypeOf(*got).NumField(); i++ {
		switch name := reflect.TypeOf(*got).Field(i).Name; name {
		case "Count", "Attributed", "Tool", "Kind", "PatternCount", "Digest":
		default:
			t.Errorf("PermissionSummary has the unexpected field %s", name)
		}
	}
	// an attributed request is at least one, whatever the count says
	one := dashPermission(execution.Execution{PendingPermission: &execution.PendingPermission{Tool: "t", Kind: "k"}})
	if one == nil || one.Count != 1 {
		t.Errorf("an attributed request without a count: %+v", one)
	}
}

func TestDetailCarriesLivenessAndPermissionOfEachExecution(t *testing.T) {
	at := time.Date(2026, 11, 21, 17, 40, 0, 0, time.UTC)
	n := 1
	v := dashView{bySession: map[string][]execution.Execution{"opencode:ses_a": {
		{ID: "exec_2", State: "waiting_for_permission", Started: at, Updated: at, Liveness: &execution.Liveness{NativeStatus: "busy", ObservedAt: at}, PendingPermissionCount: &n},
		{ID: "exec_1", State: "completed", Started: at.Add(-time.Hour), Updated: at},
	}}}
	d := detail(v, dashRow("exec_2"), "opencode:ses_a")
	if len(d.Executions) != 2 {
		t.Fatalf("%d executions", len(d.Executions))
	}
	cur, old := d.Executions[0], d.Executions[1]
	if !cur.Current || cur.Liveness == nil || cur.Liveness.NativeStatus != "busy" || cur.Permission == nil || cur.Permission.Count != 1 {
		t.Errorf("the current execution: %+v", cur)
	}
	if old.Current || old.Liveness != nil || old.Permission != nil {
		t.Errorf("the older execution: %+v", old)
	}
}

// #592 P3-1: the dashboard copies of the execution record's UnavailableSince and of the liveness RetryMax share no
// pointer with the Manager, on the session row and the detail's execution alike. Mutating the source values after
// the call must not change what the page already holds.
func TestRowAndDetailCopyUnavailableSinceAndRetryMax(t *testing.T) {
	at := time.Date(2026, 11, 21, 17, 40, 0, 0, time.UTC)
	unavailable := at.Add(-3 * time.Minute)
	retryMax := int64(5)
	attempt := int64(4)
	v := dashView{bySession: map[string][]execution.Execution{"opencode:ses_a": {{
		ID: "exec_1", State: "running", Started: at, Updated: at,
		UnavailableSince: &unavailable,
		Liveness:         &execution.Liveness{NativeStatus: "retry", RetryAttempt: &attempt, RetryMax: &retryMax, ObservedAt: at},
	}}}}
	row := v.row("opencode", "ses_a", "opencode:ses_a")
	det := detail(v, dashRow("exec_1"), "opencode:ses_a")
	if row.UnavailableSince == nil || !row.UnavailableSince.Equal(unavailable) || row.UnavailableSince == &unavailable {
		t.Errorf("the session row does not copy unavailable_since: %+v", row.UnavailableSince)
	}
	if len(det.Executions) != 1 || det.Executions[0].UnavailableSince == nil || !det.Executions[0].UnavailableSince.Equal(unavailable) ||
		det.Executions[0].UnavailableSince == &unavailable {
		t.Errorf("the detail does not copy unavailable_since: %+v", det.Executions)
	}
	if row.Liveness == nil || row.Liveness.RetryMax == nil || *row.Liveness.RetryMax != 5 || row.Liveness.RetryMax == &retryMax {
		t.Errorf("the session row does not copy retry_max: %+v", row.Liveness)
	}
	if det.Executions[0].Liveness == nil || det.Executions[0].Liveness.RetryMax == nil || *det.Executions[0].Liveness.RetryMax != 5 ||
		det.Executions[0].Liveness.RetryMax == &retryMax {
		t.Errorf("the detail does not copy retry_max: %+v", det.Executions[0].Liveness)
	}
	// mutate the source values: the copies must not move
	unavailable = at.Add(time.Hour)
	retryMax = 99
	if !row.UnavailableSince.Equal(at.Add(-3*time.Minute)) || !det.Executions[0].UnavailableSince.Equal(at.Add(-3*time.Minute)) {
		t.Error("the dashboard copy aliases the Manager's unavailable_since")
	}
	if *row.Liveness.RetryMax != 5 || *det.Executions[0].Liveness.RetryMax != 5 {
		t.Error("the dashboard copy aliases the Manager's retry_max")
	}
}
