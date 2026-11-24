package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// ADR-017 A2 S1 (D32): OpenCode structured retry/error degradation and the
// D32 Q5 bounded-text fallback. Every fixture is fabricated; no live service.

const a2Threshold = 2 * time.Minute

func a2Raw(s string) json.RawMessage { return json.RawMessage(s) }

func a2MessageText(s string) json.RawMessage { return json.RawMessage(strconv.Quote(s)) }

func a2Action(reason string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"reason":%q,"provider":"fixture"}`, reason))
}

func a2RetryStatus(attempt int64, next time.Time, action, message string) opencode.Status {
	st := opencode.Status{Type: "retry", Attempt: a2Raw(fmt.Sprint(attempt)), Next: a2Raw(fmt.Sprint(next.UnixMilli()))}
	if action != "" {
		st.Action = a2Action(action)
	}
	if message != "" {
		st.Message = a2MessageText(message)
	}
	return st
}

func a2Assistant(created, completed int64, errJSON string, parts ...string) opencode.Message {
	msg := opencode.Message{}
	msg.Info.ID = "msg_a2"
	msg.Info.Role = "assistant"
	msg.Info.SessionID = "ses_test"
	msg.Info.ParentID = "msg_prompt"
	msg.Info.Time.Created = created
	msg.Info.Time.Completed = completed
	msg.Info.Path.Cwd = "/"
	if errJSON != "" {
		msg.Info.Error = a2Raw(errJSON)
	}
	for _, p := range parts {
		msg.Parts = append(msg.Parts, a2Raw(p))
	}
	return msg
}

func a2Evidence(now time.Time, st opencode.Status, exists bool, assistants ...opencode.Message) livenessEvidence {
	return livenessEvidence{
		status: st, statusExists: exists, userFound: true, assistants: assistants,
		started: now.Add(-10 * time.Minute),
	}
}

func a2RunningTool(start time.Time) string {
	return fmt.Sprintf(`{"type":"tool","callID":"call_1","tool":"bash","state":{"status":"running","time":{"start":%d}}}`, start.UnixMilli())
}

func a2RetryPart(created int64, errJSON string) string {
	return fmt.Sprintf(`{"type":"retry","attempt":2,"error":%s,"time":{"created":%d}}`, errJSON, created)
}

// A healthy retry is degradation, never a stall; the structured action reason
// wins over the message.
func TestA2RetryHealthyDegradation(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	st := a2RetryStatus(2, now.Add(time.Minute), "free_tier_limit", "rate limited, retrying")
	l := assessLiveness(now, a2Threshold, a2Evidence(now, st, true))
	if l.NativeStatus != "retry" || l.DegradationReason != "PROVIDER_RETRYING" || !l.Degraded {
		t.Fatalf("degradation %+v", l)
	}
	if l.CauseHint != "rate_limit" || l.CauseSource != "structured" {
		t.Fatalf("cause %q/%q", l.CauseHint, l.CauseSource)
	}
	if l.RetryOverdue || l.StallSuspected || l.StallReason != "" {
		t.Fatalf("healthy retry is a stall: %+v", l)
	}
}

// An overdue retry uses the separate PROVIDER_RETRY_OVERDUE reason (D32 Q11)
// and keeps A1's wedged-retry NO_PROGRESS.
func TestA2RetryOverdueReason(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	st := a2RetryStatus(2, now.Add(-5*time.Minute), "account_rate_limit", "")
	e := a2Evidence(now, st, true)
	e.retrySince = now.Add(-5 * time.Minute)
	l := assessLiveness(now, a2Threshold, e)
	if !l.RetryOverdue || l.DegradationReason != "PROVIDER_RETRY_OVERDUE" || !l.Degraded {
		t.Fatalf("overdue %+v", l)
	}
	if l.CauseHint != "rate_limit" || l.CauseSource != "structured" {
		t.Fatalf("cause %q/%q", l.CauseHint, l.CauseSource)
	}
	if !l.StallSuspected || l.StallReason != "NO_PROGRESS" {
		t.Fatalf("wedged retry lost A1 NO_PROGRESS: %+v", l)
	}
}

// The grace/attempt window is independent of the wedged NO_PROGRESS: a young
// attempt can be overdue-past next yet not retry_overdue.
func TestA2RetryNotOverdueYoungAttempt(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	st := a2RetryStatus(2, now.Add(-5*time.Minute), "", "")
	e := a2Evidence(now, st, true)
	e.retrySince = now.Add(-30 * time.Second)
	l := assessLiveness(now, a2Threshold, e)
	if l.RetryOverdue || l.DegradationReason != "PROVIDER_RETRYING" {
		t.Fatalf("young attempt %+v", l)
	}
	if !l.StallSuspected || l.StallReason != "NO_PROGRESS" {
		t.Fatalf("wedged retry lost A1 NO_PROGRESS: %+v", l)
	}
}

// A running part never suppresses the wedged-retry NO_PROGRESS once its start
// is older than the threshold (A1 rule kept).
func TestA2RetryWedgedWithRunningToolKeepsNoProgress(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	st := a2RetryStatus(2, now.Add(-time.Minute), "", "")
	e := a2Evidence(now, st, true, a2Assistant(now.Add(-5*time.Minute).UnixMilli(), 0, "", a2RunningTool(now.Add(-4*time.Minute))))
	l := assessLiveness(now, a2Threshold, e)
	if !l.StallSuspected || l.StallReason != "NO_PROGRESS" {
		t.Fatalf("running part suppressed the stall: %+v", l)
	}
	if l.DegradationReason != "PROVIDER_RETRYING" || l.Degraded != true {
		t.Fatalf("degradation %+v", l)
	}
}

// The D32 Q5 bounded fallback maps only the closed pattern set; no raw text is
// exported.
func TestA2BoundedTextFallback(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	cases := []struct {
		message string
		hint    string
		source  string
	}{
		{"HTTP 429 Too Many Requests", "rate_limit", "bounded_text"},
		{"401 Unauthorized", "auth", "bounded_text"},
		{"403 Forbidden", "auth", "bounded_text"},
		{"503 Service Unavailable", "server", "bounded_text"},
		{"provider overloaded", "server", "bounded_text"},
		{"dial tcp: lookup api.example: no such host", "network", "bounded_text"},
		{"TLS handshake failure", "network", "bounded_text"},
		{"read tcp: connection reset by peer", "network", "bounded_text"},
		{"context deadline exceeded (timeout)", "network", "bounded_text"},
		{"request id 15003 completed", "unknown", "none"},
		{"something odd happened", "unknown", "none"},
	}
	for _, c := range cases {
		t.Run(c.message, func(t *testing.T) {
			st := a2RetryStatus(1, now.Add(time.Minute), "", c.message)
			l := assessLiveness(now, a2Threshold, a2Evidence(now, st, true))
			if l.CauseHint != c.hint || l.CauseSource != c.source {
				t.Fatalf("cause %q/%q, want %q/%q", l.CauseHint, l.CauseSource, c.hint, c.source)
			}
			if l.DegradationReason != "PROVIDER_RETRYING" || l.StallSuspected {
				t.Fatalf("degradation %+v", l)
			}
			b, _ := json.Marshal(l)
			if strings.Contains(string(b), c.message) {
				t.Fatalf("raw provider text exported: %s", b)
			}
		})
	}
}

// The fallback reads at most 4 KiB: a pattern after the bound is not matched.
func TestA2BoundedTextIsBounded(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	atBound := a2RetryStatus(1, now.Add(time.Minute), "", "429 "+strings.Repeat("x", 5000))
	l := assessLiveness(now, a2Threshold, a2Evidence(now, atBound, true))
	if l.CauseHint != "rate_limit" || l.CauseSource != "bounded_text" {
		t.Fatalf("in-bound pattern missed: %q/%q", l.CauseHint, l.CauseSource)
	}
	afterBound := a2RetryStatus(1, now.Add(time.Minute), "", strings.Repeat("x", 4096)+" 429 rate limit")
	l = assessLiveness(now, a2Threshold, a2Evidence(now, afterBound, true))
	if l.CauseHint != "unknown" || l.CauseSource != "none" {
		t.Fatalf("pattern after 4 KiB matched: %q/%q", l.CauseHint, l.CauseSource)
	}
}

// An open action.reason that is not one of the two usage-limit values is
// unknown but still structured.
func TestA2ActionReasonUnknownStructured(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	st := a2RetryStatus(1, now.Add(time.Minute), "free_tier_limit_v2", "429 rate limit")
	l := assessLiveness(now, a2Threshold, a2Evidence(now, st, true))
	if l.CauseHint != "unknown" || l.CauseSource != "structured" {
		t.Fatalf("cause %q/%q", l.CauseHint, l.CauseSource)
	}
}

// PROVIDER_ERROR from an assistant typed error without terminal proof.
func TestA2ProviderErrorFromAssistantError(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	cases := []struct {
		name string
		err  string
		hint string
	}{
		{"api_5xx", `{"name":"APIError","data":{"statusCode":503,"message":"upstream"}}`, "server"},
		{"api_429", `{"name":"APIError","data":{"statusCode":429}}`, "rate_limit"},
		{"api_no_status", `{"name":"APIError","data":{"message":"socket closed"}}`, "network"},
		{"api_401", `{"name":"APIError","data":{"statusCode":401}}`, "auth"},
		{"api_other", `{"name":"APIError","data":{"statusCode":418}}`, "unknown"},
		{"auth", `{"name":"ProviderAuthError","data":{"providerID":"fixture"}}`, "auth"},
		{"unknown", `{"name":"UnknownError","data":{"message":"x"}}`, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := a2Evidence(now, opencode.Status{Type: "busy"}, true, a2Assistant(now.UnixMilli(), 0, c.err))
			// No progress for longer than the threshold would also be A1
			// NO_PROGRESS; keep the started baseline recent so only the
			// degradation dimension is exercised.
			e.started = now.Add(-time.Second)
			l := assessLiveness(now, a2Threshold, e)
			if l.NativeStatus != "busy" || l.DegradationReason != "PROVIDER_ERROR" || !l.Degraded {
				t.Fatalf("degradation %+v", l)
			}
			if l.CauseHint != c.hint || l.CauseSource != "structured" {
				t.Fatalf("cause %q/%q, want %q", l.CauseHint, l.CauseSource, c.hint)
			}
			if l.StallSuspected {
				t.Fatalf("provider error became a stall: %+v", l)
			}
		})
	}
}

// A persisted retry part is a typed error source for PROVIDER_ERROR and the
// cause (Q7 stays UNVERIFIED; the fixture fabricates the part).
func TestA2ProviderErrorFromRetryPart(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	part := a2RetryPart(now.UnixMilli(), `{"name":"APIError","data":{"statusCode":429}}`)
	e := a2Evidence(now, opencode.Status{Type: "idle"}, true, a2Assistant(now.UnixMilli(), 0, "", part))
	l := assessLiveness(now, a2Threshold, e)
	if l.DegradationReason != "PROVIDER_ERROR" || l.CauseHint != "rate_limit" || l.CauseSource != "structured" {
		t.Fatalf("retry part %+v", l)
	}
}

// A retry part's time.created is not native progress (unknown part type).
func TestA2RetryPartIsNotProgress(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	if v := partProgress(a2Raw(a2RetryPart(now.UnixMilli(), `{"name":"APIError"}`)), now.UnixMilli()); v != 0 {
		t.Fatalf("retry part counted as progress: %d", v)
	}
}

// Terminal proof clears the provider degradation; only an unresolved execution
// carries it.
func TestA2ProviderErrorNeedsNoTerminalProof(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	e := a2Evidence(now, opencode.Status{Type: "busy"}, true, a2Assistant(now.UnixMilli(), 0, `{"name":"APIError","data":{"statusCode":503}}`))
	e.proof = true
	l := assessLiveness(now, a2Threshold, e)
	if l.DegradationReason != "" || l.Degraded || l.CauseHint != "" || l.CauseSource != "" {
		t.Fatalf("terminal proof kept degradation: %+v", l)
	}
}

// No retry status and no typed error means no degradation fields at all.
func TestA2NoDegradationWithoutSignal(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	l := assessLiveness(now, a2Threshold, a2Evidence(now, opencode.Status{Type: "busy"}, true))
	if l.DegradationReason != "" || l.Degraded || l.RetryOverdue || l.CauseHint != "" || l.CauseSource != "" {
		t.Fatalf("spurious degradation: %+v", l)
	}
}

// A pending permission suppresses the stall reason but not the provider
// degradation dimension (A2.4).
func TestA2PendingPermissionKeepsDegradation(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	st := a2RetryStatus(2, now.Add(-5*time.Minute), "account_rate_limit", "")
	e := a2Evidence(now, st, true)
	e.permissionPending = true
	e.retrySince = now.Add(-30 * time.Second)
	l := assessLiveness(now, a2Threshold, e)
	if l.StallSuspected || l.StallReason != "" {
		t.Fatalf("permission did not suppress the stall: %+v", l)
	}
	if l.DegradationReason != "PROVIDER_RETRYING" || l.CauseHint != "rate_limit" {
		t.Fatalf("permission dropped degradation: %+v", l)
	}
}

// The retry_attempt_since memory drives retry_overdue on the execution record:
// it is kept while the attempt is unchanged, reset by an attempt change, and
// cleared when the status is no longer retry.
func TestA2RetryAttemptSinceMemory(t *testing.T) {
	f := &fake{status: "idle"}
	start := time.Now()
	clock := &fakeClock{t: start}
	m := NewWithTiming(f, time.Minute, 2*time.Millisecond)
	m.clock = clock.Now
	m.stallThreshold = time.Minute
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "fixture", "a2-since-key")
	if err != nil {
		t.Fatal(err)
	}
	setStatus(f, &opencode.Status{Type: "retry", Attempt: a2Raw(`3`), Next: a2Raw(fmt.Sprint(ms(start.Add(10 * time.Minute)))),
		Action: a2Action("account_rate_limit")})
	got := waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && x.Liveness.NativeStatus == "retry" && x.RetryAttemptSince != nil
	})
	if !got.RetryAttemptSince.Equal(start) {
		t.Fatalf("since = %v, want %v", got.RetryAttemptSince, start)
	}
	if got.Liveness.DegradationReason != "PROVIDER_RETRYING" || got.Liveness.RetryOverdue {
		t.Fatalf("healthy retry %+v", got.Liveness)
	}
	// The attempt is unchanged, so the memory survives the advance; with next
	// past beyond the grace the retry becomes overdue.
	clock.Advance(3 * time.Minute)
	setStatus(f, &opencode.Status{Type: "retry", Attempt: a2Raw(`3`), Next: a2Raw(fmt.Sprint(ms(start))),
		Action: a2Action("account_rate_limit")})
	got = waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && x.Liveness.RetryOverdue
	})
	if !got.RetryAttemptSince.Equal(start) {
		t.Fatalf("since moved without an attempt change: %v", got.RetryAttemptSince)
	}
	if got.Liveness.DegradationReason != "PROVIDER_RETRY_OVERDUE" || !got.Liveness.Degraded {
		t.Fatalf("overdue reason %+v", got.Liveness)
	}
	if got.State != "running" {
		t.Fatalf("degradation changed state: %+v", got)
	}
	guardHeld(t, m)
	// An attempt change resets the memory and clears retry_overdue.
	setStatus(f, &opencode.Status{Type: "retry", Attempt: a2Raw(`4`),
		Next: a2Raw(fmt.Sprint(ms(clock.Now().Add(10 * time.Minute)))), Action: a2Action("account_rate_limit")})
	got = waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && x.Liveness.RetryAttempt != nil && *x.Liveness.RetryAttempt == 4 && x.RetryAttemptSince != nil && !x.Liveness.RetryOverdue
	})
	if !got.RetryAttemptSince.Equal(clock.Now()) {
		t.Fatalf("attempt change did not reset since: %v (now %v)", got.RetryAttemptSince, clock.Now())
	}
	// The retry ends: the memory is cleared.
	setStatus(f, &opencode.Status{Type: "busy"})
	got = waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && x.Liveness.NativeStatus == "busy" && x.RetryAttemptSince == nil
	})
	if got.Liveness.DegradationReason != "" {
		t.Fatalf("busy kept degradation: %+v", got.Liveness)
	}
}

// With the report switched off for comparison tests, the A2 memory is not
// written either.
func TestA2RetryMemoryHonoursNoLiveness(t *testing.T) {
	f := &fake{status: "idle"}
	start := time.Now()
	clock := &fakeClock{t: start}
	m := NewWithTiming(f, time.Minute, 2*time.Millisecond)
	m.clock = clock.Now
	m.noLiveness = true
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "fixture", "a2-off-key")
	if err != nil {
		t.Fatal(err)
	}
	setStatus(f, &opencode.Status{Type: "retry", Attempt: a2Raw(`2`), Next: a2Raw(fmt.Sprint(ms(start.Add(time.Minute))))})
	time.Sleep(20 * time.Millisecond)
	got, err := m.Snapshot(context.Background(), x.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Liveness != nil || got.RetryAttemptSince != nil {
		t.Fatalf("report off but A2 fields present: liveness=%+v since=%v", got.Liveness, got.RetryAttemptSince)
	}
}

// The record copy does not alias the memory pointer.
func TestA2RetryAttemptSinceSnapshotCopy(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	v := Execution{RetryAttemptSince: &now}
	c := snapshot(v)
	if c.RetryAttemptSince == v.RetryAttemptSince {
		t.Fatal("snapshot aliased retry_attempt_since")
	}
	if !reflect.DeepEqual(c.RetryAttemptSince, v.RetryAttemptSince) {
		t.Fatalf("snapshot %v", c.RetryAttemptSince)
	}
}
