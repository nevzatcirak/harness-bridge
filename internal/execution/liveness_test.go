package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// ADR-017 option (a): report-only liveness. The plan in ADR-017 §5 is
// implemented below as unit tests against the fake runner; no live services.

// fakeClock is a liveness observation clock that tests advance explicitly.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// assistant appends a correlated assistant message for mid with the given
// native times (epoch ms; 0 = absent), finish, error JSON and raw parts.
func assistant(f *fake, mid string, created, completed int64, finish, errorJSON string, parts ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msg := opencode.Message{}
	msg.Info.ID = fmt.Sprintf("msg_assistant_%s_%d", mid, len(f.messages))
	msg.Info.Role = "assistant"
	msg.Info.SessionID = "ses_test"
	msg.Info.ParentID = mid
	msg.Info.Finish = finish
	msg.Info.Time.Created = created
	msg.Info.Time.Completed = completed
	msg.Info.Path.Cwd = "/"
	if errorJSON != "" {
		msg.Info.Error = json.RawMessage(errorJSON)
	}
	for _, p := range parts {
		msg.Parts = append(msg.Parts, json.RawMessage(p))
	}
	f.messages = append(f.messages, msg)
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func textPart(end time.Time) string {
	return fmt.Sprintf(`{"type":"text","text":"x","time":{"start":%d,"end":%d}}`, ms(end)-1, ms(end))
}

// busyNow switches the fake to native busy after admission (the preflight
// refuses a non-idle session).
func busyNow(f *fake) { locked(f, func() { f.status = "busy" }) }

func locked(f *fake, fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func setStatus(f *fake, s *opencode.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nativeStatus = s
}

// waitLiveness polls Get until ok accepts the execution.
func waitLiveness(t *testing.T, m *Manager, id string, ok func(Execution) bool) Execution {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var x Execution
	var err error
	for time.Now().Before(deadline) {
		x, err = m.Get(context.Background(), id)
		if err == nil && ok(x) {
			return x
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("liveness condition not reached: %+v liveness=%+v err=%v", x, x.Liveness, err)
	return x
}

func suspected(reason string) func(Execution) bool {
	return func(x Execution) bool {
		return x.Liveness != nil && x.Liveness.StallSuspected && x.Liveness.StallReason == reason
	}
}

func guardHeld(t *testing.T, m *Manager) {
	t.Helper()
	if held, _ := m.SessionGuard("ses_test"); !held {
		t.Fatal("session guard released by liveness")
	}
	assertBusy(t, m)
}

// 1. R3a shape: completed assistant without finish/error, status absent, no
// permission -> COMPLETED_WITHOUT_TERMINAL_EVIDENCE; state stays unknown, guard held.
func TestLivenessR3aShape(t *testing.T) {
	t.Run("after_monitor_expiry", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := &fake{missing: true}
			m, x := expired(t, f)
			defer m.Close()
			now := time.Now()
			assistant(f, x.MessageID, ms(now.Add(-2*time.Second)), ms(now.Add(-time.Second)), "", "")
			got, err := m.Get(context.Background(), x.ID)
			if err != nil {
				t.Fatal(err)
			}
			l := got.Liveness
			if l == nil || !l.StallSuspected || l.StallReason != "COMPLETED_WITHOUT_TERMINAL_EVIDENCE" || l.NativeStatus != "absent" {
				t.Fatalf("liveness %+v", l)
			}
			if got.State != "unknown" || got.Unresolved == nil || got.Unresolved.Reason != "MONITOR_EXPIRED" || got.Ended != nil {
				t.Fatalf("state changed: %+v", got)
			}
			if l.LastNativeProgressAt == nil || !l.LastNativeProgressAt.Equal(time.UnixMilli(ms(now.Add(-time.Second)))) {
				t.Fatalf("last progress %v", l.LastNativeProgressAt)
			}
			guardHeld(t, m)
		})
	})
	t.Run("before_monitor_expiry", func(t *testing.T) {
		f := &fake{missing: true}
		m := setup(t, f)
		x, err := m.Send(context.Background(), "ses_test", "fixture", "r3a-shape-key")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		assistant(f, x.MessageID, ms(now), ms(now), "", "")
		got := waitLiveness(t, m, x.ID, suspected("COMPLETED_WITHOUT_TERMINAL_EVIDENCE"))
		if got.State != "unknown" || got.Ended != nil {
			t.Fatalf("state changed: %+v", got)
		}
		guardHeld(t, m)
	})
}

// The user message exists, no correlated assistant, status absent.
func TestLivenessNoCorrelatedAssistant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{missing: true}
		m, x := expired(t, f)
		defer m.Close()
		got, err := m.Get(context.Background(), x.ID)
		if err != nil {
			t.Fatal(err)
		}
		if l := got.Liveness; l == nil || !l.StallSuspected || l.StallReason != "NO_CORRELATED_ASSISTANT" || l.LastNativeProgressAt != nil {
			t.Fatalf("liveness %+v", got.Liveness)
		}
		if got.State != "unknown" || got.Ended != nil {
			t.Fatalf("state changed: %+v", got)
		}
		guardHeld(t, m)
	})
}

// 2. Real terminal proof wins: completed, guard released, no liveness block.
func TestLivenessTerminalProofWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{missing: true}
		m, x := expired(t, f)
		defer m.Close()
		now := time.Now()
		assistant(f, x.MessageID, ms(now), ms(now), "", "")
		if got, _ := m.Get(context.Background(), x.ID); got.Liveness == nil || !got.Liveness.StallSuspected {
			t.Fatalf("precondition: %+v", got.Liveness)
		}
		time.Sleep(time.Second)
		assistant(f, x.MessageID, ms(time.Now()), ms(time.Now()), "stop", "")
		got, err := m.Get(context.Background(), x.ID)
		if err != nil || got.State != "completed" || got.Ended == nil {
			t.Fatal(got, err)
		}
		if got.Liveness != nil {
			t.Fatalf("terminal execution kept liveness %+v", got.Liveness)
		}
		assertReleased(t, m)
	})
}

// 3. Retry with a future next is not a stall.
func TestLivenessRetryIsNotStall(t *testing.T) {
	f := &fake{status: "idle"}
	start := time.Now()
	next := start.Add(time.Hour)
	clock := &fakeClock{t: start}
	m := NewWithTiming(f, time.Minute, 2*time.Millisecond)
	m.clock = clock.Now
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "fixture", "retry-key")
	if err != nil {
		t.Fatal(err)
	}
	setStatus(f, &opencode.Status{Type: "retry", Attempt: json.RawMessage(`3`), Next: json.RawMessage(fmt.Sprint(ms(next)))})
	got := waitLiveness(t, m, x.ID, func(x Execution) bool { return x.Liveness != nil && x.Liveness.NativeStatus == "retry" })
	l := got.Liveness
	if l.StallSuspected || l.StallReason != "" {
		t.Fatalf("retry reported as stall: %+v", l)
	}
	if l.RetryAttempt == nil || *l.RetryAttempt != 3 || l.RetryNextAt == nil || !l.RetryNextAt.Equal(time.UnixMilli(ms(next))) {
		t.Fatalf("retry details %+v", l)
	}
	if got.State != "running" {
		t.Fatalf("state %+v", got)
	}
	// Even with no progress for longer than the threshold, a retry whose next
	// attempt is still in the future is scheduled work, not a stall.
	clock.Advance(m.stallThreshold + time.Minute)
	got = waitLiveness(t, m, x.ID, func(x Execution) bool { return x.Liveness != nil && !x.Liveness.ObservedAt.Before(clock.Now()) })
	if got.Liveness.StallSuspected || got.Liveness.NativeStatus != "retry" {
		t.Fatalf("future retry suspected after threshold: %+v", got.Liveness)
	}
}

// 4. NO_PROGRESS: busy beyond the threshold (injected clock) is suspected,
// below it is not, and a newer part timestamp clears it. Retry with a past
// next is treated like busy.
func TestLivenessNoProgressThreshold(t *testing.T) {
	for _, status := range []string{"busy", "retry_past"} {
		t.Run(status, func(t *testing.T) {
			f := &fake{status: "idle"}
			start := time.Now()
			clock := &fakeClock{t: start}
			m := NewWithTiming(f, time.Minute, 2*time.Millisecond)
			m.clock = clock.Now
			t.Cleanup(m.Close)
			x, err := m.Send(context.Background(), "ses_test", "fixture", "progress-key")
			if err != nil {
				t.Fatal(err)
			}
			busyNow(f)
			if status == "retry_past" {
				setStatus(f, &opencode.Status{Type: "retry", Attempt: json.RawMessage(`1`), Next: json.RawMessage(fmt.Sprint(ms(start.Add(-time.Second))))})
			}
			assistant(f, x.MessageID, ms(start), 0, "", "", textPart(start))
			// Only observations of the busy/retry status with the assistant in
			// history count: a probe before the switch correctly sees an idle
			// session without an assistant (NO_CORRELATED_ASSISTANT).
			observed := func(after time.Time) func(Execution) bool {
				return func(x Execution) bool {
					l := x.Liveness
					return l != nil && !l.ObservedAt.Before(after) && l.NativeStatus != "absent" && l.LastNativeProgressAt != nil
				}
			}
			got := waitLiveness(t, m, x.ID, observed(start))
			if got.Liveness.StallSuspected {
				t.Fatalf("suspected below threshold: %+v", got.Liveness)
			}
			clock.Advance(m.stallThreshold - time.Second)
			got = waitLiveness(t, m, x.ID, observed(clock.Now()))
			if got.Liveness.StallSuspected {
				t.Fatalf("suspected just below threshold: %+v", got.Liveness)
			}
			clock.Advance(2 * time.Second)
			got = waitLiveness(t, m, x.ID, suspected("NO_PROGRESS"))
			if got.State != "running" {
				t.Fatalf("state changed: %+v", got)
			}
			guardHeld(t, m)
			// A newer native part timestamp is progress and clears the suspicion.
			assistant(f, x.MessageID, ms(start), 0, "", "", textPart(clock.Now()))
			got = waitLiveness(t, m, x.ID, func(x Execution) bool {
				return x.Liveness != nil && !x.Liveness.StallSuspected && x.Liveness.LastNativeProgressAt != nil && x.Liveness.LastNativeProgressAt.Equal(time.UnixMilli(ms(clock.Now())))
			})
			if got.State != "running" {
				t.Fatalf("state changed: %+v", got)
			}
		})
	}
}

// 5. Pending permission takes precedence over any suspicion.
func TestLivenessPendingPermissionPrecedence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{missing: true}
		m, x := expired(t, f)
		defer m.Close()
		now := time.Now()
		assistant(f, x.MessageID, ms(now), ms(now), "", "")
		f.mu.Lock()
		f.permission = true
		f.mu.Unlock()
		got, err := m.Get(context.Background(), x.ID)
		if err != nil || got.State != "waiting_for_permission" {
			t.Fatal(got, err)
		}
		if got.Liveness == nil || got.Liveness.StallSuspected || got.Liveness.StallReason != "" {
			t.Fatalf("suspicion despite pending permission: %+v", got.Liveness)
		}
		guardHeld(t, m)
	})
}

// 6. Probes with suspicion make no native mutation: exactly the original
// Prompt and zero Abort calls.
func TestLivenessNoNativeMutation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{missing: true}
		m, x := expired(t, f)
		defer m.Close()
		now := time.Now()
		assistant(f, x.MessageID, ms(now), ms(now), "", "")
		for i := 0; i < 5; i++ {
			got, err := m.Get(context.Background(), x.ID)
			if err != nil || got.Liveness == nil || !got.Liveness.StallSuspected {
				t.Fatal(got, err)
			}
			if _, _, err := m.Result(context.Background(), x.ID); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Second)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sends != 1 || f.aborts != 0 {
			t.Fatalf("native mutation: sends=%d aborts=%d", f.sends, f.aborts)
		}
	})
}

// 7. Replay is unchanged: same key and payload return the same execution,
// including its liveness block; a different payload still conflicts.
func TestLivenessReplayUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{missing: true}
		m, x := expired(t, f)
		defer m.Close()
		now := time.Now()
		assistant(f, x.MessageID, ms(now), ms(now), "", "")
		got, err := m.Get(context.Background(), x.ID)
		if err != nil || got.Liveness == nil || !got.Liveness.StallSuspected {
			t.Fatal(got, err)
		}
		replay, isReplay, err := m.SendWithRunnerOutcome(context.Background(), f, "ses_test", "controlled fixture", "late-key")
		if err != nil || !isReplay || replay.ID != x.ID || !reflect.DeepEqual(replay.Liveness, got.Liveness) {
			t.Fatalf("replay %+v %v %v (want liveness %+v)", replay, isReplay, err, got.Liveness)
		}
		_, err = m.Send(context.Background(), "ses_test", "other payload", "late-key")
		var e *Error
		if !errors.As(err, &e) || e.Code != "IDEMPOTENCY_CONFLICT" {
			t.Fatalf("conflict %v", err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sends != 1 {
			t.Fatalf("replay resubmitted: %d", f.sends)
		}
	})
}

// 8. Restart: a fresh manager knows nothing about the old execution. The
// existing TestRestartLosesIdempotencyAndExecutionRecords stays unchanged.
func TestLivenessRestartNotFound(t *testing.T) {
	f := &fake{missing: true}
	first := NewWithTiming(f, time.Second, 2*time.Millisecond)
	x, err := first.Send(context.Background(), "ses_test", "fixture", "restart-key")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	assistant(f, x.MessageID, ms(now), ms(now), "", "")
	waitLiveness(t, first, x.ID, suspected("COMPLETED_WITHOUT_TERMINAL_EVIDENCE"))
	first.Close()
	second := setup(t, f)
	_, err = second.Get(context.Background(), x.ID)
	var e *Error
	if !errors.As(err, &e) || e.Code != "NOT_FOUND" {
		t.Fatalf("fresh manager: %v", err)
	}
	if held, _ := second.SessionGuard("ses_test"); held {
		t.Fatal("fresh manager claims a guard")
	}
}

// 9. Hostile native JSON: oversized, unknown, malformed parts and timestamps
// are ignored without panic, and last_native_progress_at is never after the
// probe time.
func TestLivenessHostileNativeJSON(t *testing.T) {
	f := &fake{status: "idle"}
	start := time.Now()
	clock := &fakeClock{t: start}
	m := NewWithTiming(f, time.Minute, 2*time.Millisecond)
	m.clock = clock.Now
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "fixture", "hostile-key")
	if err != nil {
		t.Fatal(err)
	}
	setStatus(f, &opencode.Status{Type: "busy", Attempt: json.RawMessage(`"x"`), Next: json.RawMessage(`-5`)})
	valid := start.Add(-time.Minute)
	future := start.Add(time.Hour)
	huge := strings.Repeat("a", 2<<20)
	deep := strings.Repeat("[", 20000) + strings.Repeat("]", 20000)
	parts := []string{
		textPart(valid),
		`{`,
		`null`,
		`[]`,
		`"text"`,
		`{"type":"text","time":{"start":"soon","end":{"a":1}}}`,
		`{"type":"text","time":{"end":-7}}`,
		`{"type":"text","time":{"end":1e300}}`,
		`{"type":"text","time":{"end":9223372036854775807}}`,
		fmt.Sprintf(`{"type":"text","time":{"end":%d}}`, ms(future)),
		fmt.Sprintf(`{"type":"tool","state":{"time":{"start":%d,"end":%d}}}`, ms(future), ms(future)),
		fmt.Sprintf(`{"type":"step-finish","time":{"end":%d}}`, ms(start)),
		fmt.Sprintf(`{"type":"unknown-kind","time":{"end":%d}}`, ms(start)),
		fmt.Sprintf(`{"type":"text","text":%q,"time":{"end":%d}}`, huge, ms(start)),
		deep,
		fmt.Sprintf(`{"type":"tool","state":"broken","time":{"end":%d}}`, ms(future)),
	}
	assistant(f, x.MessageID, ms(future), 0, "", "", parts...)
	clock.Advance(time.Millisecond)
	observedAfter := clock.Now()
	got := waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && !x.Liveness.ObservedAt.Before(observedAfter)
	})
	l := got.Liveness
	if l.LastNativeProgressAt == nil || l.LastNativeProgressAt.After(l.ObservedAt) {
		t.Fatalf("progress %v after observation %v", l.LastNativeProgressAt, l.ObservedAt)
	}
	if !l.LastNativeProgressAt.Equal(time.UnixMilli(ms(valid))) {
		t.Fatalf("progress %v, want the only valid recognized timestamp %v", l.LastNativeProgressAt, time.UnixMilli(ms(valid)))
	}
	if l.RetryAttempt != nil || l.RetryNextAt != nil || l.NativeStatus != "busy" {
		t.Fatalf("malformed retry fields used: %+v", l)
	}
	// Malformed retry values on a retry status are dropped, not guessed.
	setStatus(f, &opencode.Status{Type: "retry", Attempt: json.RawMessage(`-1`), Next: json.RawMessage(`"tomorrow"`)})
	clock.Advance(time.Second)
	got = waitLiveness(t, m, x.ID, func(x Execution) bool { return x.Liveness != nil && x.Liveness.NativeStatus == "retry" })
	if got.Liveness.RetryAttempt != nil || got.Liveness.RetryNextAt != nil || got.Liveness.StallSuspected {
		t.Fatalf("malformed retry used: %+v", got.Liveness)
	}
	// An unrecognized native status type is reported unavailable, not guessed.
	setStatus(f, &opencode.Status{Type: "zombie"})
	clock.Advance(time.Second)
	got = waitLiveness(t, m, x.ID, func(x Execution) bool { return x.Liveness != nil && x.Liveness.NativeStatus == "unavailable" })
	if got.Liveness.StallSuspected {
		t.Fatalf("unknown status suspected: %+v", got.Liveness)
	}
}

// Failed native reads report native_status unavailable and never suspicion.
func TestLivenessUnavailableReads(t *testing.T) {
	f := &fake{status: "idle"}
	m := setup(t, f)
	x, err := m.Send(context.Background(), "ses_test", "fixture", "offline-key")
	if err != nil {
		t.Fatal(err)
	}
	busyNow(f)
	waitLiveness(t, m, x.ID, func(x Execution) bool { return x.Liveness != nil && x.Liveness.NativeStatus == "busy" })
	f.mu.Lock()
	f.disconnected = true
	f.mu.Unlock()
	got := waitLiveness(t, m, x.ID, func(x Execution) bool { return x.Liveness != nil && x.Liveness.NativeStatus == "unavailable" })
	if got.Liveness.StallSuspected || got.Liveness.LastNativeProgressAt != nil {
		t.Fatalf("unavailable reads produced evidence: %+v", got.Liveness)
	}
}

// 10. JSON contract: the block is optional and additive; parsers written for
// the previous Execution shape still decode, and the field names are exact.
func TestLivenessJSONContract(t *testing.T) {
	type previousExecution struct {
		ID             string        `json:"execution_id"`
		SessionID      string        `json:"session_id"`
		MessageID      string        `json:"message_id"`
		State          string        `json:"state"`
		Error          string        `json:"error,omitempty"`
		Started        time.Time     `json:"started_at"`
		Updated        time.Time     `json:"updated_at"`
		Ended          *time.Time    `json:"ended_at,omitempty"`
		Unresolved     *Unresolved   `json:"unresolved,omitempty"`
		MonitorEndedAt *time.Time    `json:"monitor_ended_at,omitempty"`
		ReconciledAt   *time.Time    `json:"reconciled_at,omitempty"`
		LocalFailure   *LocalFailure `json:"local_failure,omitempty"`
	}
	now := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	attempt := int64(2)
	x := Execution{ID: "e", SessionID: "opencode:ses_test", MessageID: "msg_x", State: "unknown", Started: now, Updated: now}
	plain, err := json.Marshal(x)
	if err != nil || strings.Contains(string(plain), "liveness") {
		t.Fatalf("absent liveness must be omitted: %s %v", plain, err)
	}
	x.Liveness = &Liveness{NativeStatus: "retry", RetryAttempt: &attempt, RetryNextAt: &now, LastNativeProgressAt: &now, StallSuspected: true, StallReason: "NO_PROGRESS", ObservedAt: now}
	b, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	var old previousExecution
	if err := json.Unmarshal(b, &old); err != nil || old.ID != "e" || old.State != "unknown" || !old.Started.Equal(now) {
		t.Fatalf("previous parser: %+v %v", old, err)
	}
	var generic map[string]map[string]any
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(b, &wrapper); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"l":`+string(wrapper["liveness"])+`}`), &generic); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for k := range generic["l"] {
		keys = append(keys, k)
	}
	want := []string{"last_native_progress_at", "native_status", "observed_at", "retry_attempt", "retry_next_at", "stall_reason", "stall_suspected"}
	if strings.Join(sortedStrings(keys), ",") != strings.Join(want, ",") {
		t.Fatalf("liveness keys %v, want %v", sortedStrings(keys), want)
	}
	// A minimal block keeps only the always-present fields.
	x.Liveness = &Liveness{NativeStatus: "busy", ObservedAt: now}
	b, _ = json.Marshal(x.Liveness)
	if string(b) != `{"native_status":"busy","stall_suspected":false,"observed_at":"2026-11-05T08:00:00Z"}` {
		t.Fatalf("minimal block %s", b)
	}
}

func sortedStrings(v []string) []string {
	out := append([]string(nil), v...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// 11. Race: concurrent probes, status/result reads, snapshots, replay and guard
// reads while suspicion toggles. Run with -race -count=20.
func TestLivenessConcurrentReads(t *testing.T) {
	f := &fake{status: "idle"}
	clock := &fakeClock{t: time.Now()}
	m := NewWithTiming(f, time.Minute, time.Millisecond)
	m.clock = clock.Now
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "fixture", "race-key")
	if err != nil {
		t.Fatal(err)
	}
	busyNow(f)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	reader := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					fn()
				}
			}
		}()
	}
	reader(func() {
		if v, err := m.Get(context.Background(), x.ID); err == nil {
			_, _ = json.Marshal(v)
		}
	})
	reader(func() { _, _, _ = m.Result(context.Background(), x.ID) })
	reader(func() {
		if v, err := m.Snapshot(context.Background(), x.ID); err == nil && v.Liveness != nil {
			_ = v.Liveness.LastNativeProgressAt
			_, _ = json.Marshal(v.Liveness)
		}
	})
	reader(func() { _, _ = m.SessionGuard("ses_test") })
	reader(func() { _, _, _ = m.SendWithRunnerOutcome(context.Background(), f, "ses_test", "fixture", "race-key") })
	for i := 0; i < 50; i++ {
		clock.Advance(time.Minute)
		assistant(f, x.MessageID, ms(clock.Now()), 0, "", "", textPart(clock.Now()))
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	got, err := m.Snapshot(context.Background(), x.ID)
	if err != nil || got.State != "running" {
		t.Fatal(got, err)
	}
	guardHeld(t, m)
}

// Liveness never changes state, unresolved, ended_at, error or the guard: the
// same native shapes give identical results with the report switched off.
func TestLivenessNeverChangesStateOrGuard(t *testing.T) {
	shapes := []struct {
		name  string
		setup func(f *fake, mid string, now time.Time)
	}{
		{"r3a", func(f *fake, mid string, now time.Time) {
			locked(f, func() { f.missing = true })
			assistant(f, mid, ms(now), ms(now), "", "")
		}},
		{"no_assistant", func(f *fake, mid string, now time.Time) { locked(f, func() { f.missing = true }) }},
		{"busy_no_progress", func(f *fake, mid string, now time.Time) {
			assistant(f, mid, ms(now.Add(-time.Hour)), 0, "", "")
		}},
		{"retry_past", func(f *fake, mid string, now time.Time) {
			setStatus(f, &opencode.Status{Type: "retry", Attempt: json.RawMessage(`4`), Next: json.RawMessage(fmt.Sprint(ms(now.Add(-time.Hour))))})
			assistant(f, mid, ms(now.Add(-time.Hour)), 0, "", "")
		}},
		{"permission", func(f *fake, mid string, now time.Time) {
			locked(f, func() { f.missing = true; f.permission = true })
			assistant(f, mid, ms(now), ms(now), "", "")
		}},
		{"completed", func(f *fake, mid string, now time.Time) {
			locked(f, func() { f.missing = true })
			assistant(f, mid, ms(now), ms(now), "stop", "")
		}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				type observation struct {
					State, Error, Reason string
					Ended                bool
					Held                 bool
					GuardState           string
				}
				observe := func(report bool) (observation, *Liveness) {
					f := &fake{status: "idle"}
					m := NewWithTiming(f, 300*time.Second, time.Second)
					m.noLiveness = !report
					defer m.Close()
					x, err := m.Send(context.Background(), "ses_test", "controlled fixture", "late-key")
					if err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					busyNow(f)
					shape.setup(f, x.MessageID, time.Now())
					time.Sleep(10 * time.Second)
					synctest.Wait()
					before, _ := m.Snapshot(context.Background(), x.ID)
					time.Sleep(300 * time.Second)
					synctest.Wait()
					got, err := m.Get(context.Background(), x.ID)
					if err != nil {
						t.Fatal(err)
					}
					o := observation{State: before.State + "/" + got.State, Error: before.Error + "/" + got.Error, Ended: got.Ended != nil}
					if got.Unresolved != nil {
						o.Reason = got.Unresolved.Reason
					}
					o.Held, o.GuardState = m.SessionGuard("ses_test")
					return o, got.Liveness
				}
				with, liveness := observe(true)
				without, none := observe(false)
				if none != nil {
					t.Fatalf("report switched off but liveness %+v", none)
				}
				if with != without {
					t.Fatalf("liveness changed the outcome:\nwith    %+v (liveness %+v)\nwithout %+v", with, liveness, without)
				}
				if shape.name != "completed" && (liveness == nil || !with.Held) {
					t.Fatalf("expected a held guard and a liveness block: %+v %+v", with, liveness)
				}
			})
		})
	}
}

// Interrupt still requires valid observation and sends Abort only on request;
// suspicion alone never aborts.
func TestLivenessSuspicionNeverAborts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{missing: true}
		m, x := expired(t, f)
		defer m.Close()
		now := time.Now()
		assistant(f, x.MessageID, ms(now), ms(now), "", "")
		for i := 0; i < 3; i++ {
			if _, err := m.Get(context.Background(), x.ID); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Second)
		}
		f.mu.Lock()
		aborts := f.aborts
		f.mu.Unlock()
		if aborts != 0 {
			t.Fatalf("suspicion aborted: %d", aborts)
		}
	})
}
