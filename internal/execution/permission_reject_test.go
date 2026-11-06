package execution

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// F-7 (R5b, 2026-11-06): current behavior after a native permission rejection.
//
// These tests document what main does today; they do not change production
// code. The native shape follows OpenCode v1.19.4 (148842ef):
//   - Permission.reply("reject") fails the pending ask with RejectedError
//     (permission/index.ts:121-127);
//   - the processor stores the tool part as status "error" with only the
//     error message string (session/processor.ts:186-199);
//   - with experimental.continue_loop_on_deny unset the processor returns
//     "stop" and the prompt loop breaks (processor.ts:200-201, 647, 694;
//     prompt.ts:1319), so the last assistant message keeps the step-finish
//     reason "tool-calls" (processor.ts:457) and has no error.
// See docs/adr/ADR-018-permission-rejection-outcome.md.

const rejectedText = "The user rejected permission to use this specific tool call."

// rejectedParts is the persisted part sequence of the R5b assistant message:
// step-start, the rejected bash tool part, step-finish "tool-calls".
func rejectedParts(at time.Time) []string {
	start, end := ms(at)-2, ms(at)-1
	return []string{
		`{"type":"step-start"}`,
		fmt.Sprintf(`{"type":"tool","tool":"bash","callID":"call_r5b","state":{"status":"error","input":{"command":"true"},"error":%q,"time":{"start":%d,"end":%d}}}`, rejectedText, start, end),
		`{"type":"step-finish","reason":"tool-calls"}`,
	}
}

// pendingThenRejected drives the R5b sequence on f after admission: native
// busy with a pending permission, then the human rejects it, the assistant
// message completes with finish "tool-calls" and the session goes absent.
func pendingThenRejected(t *testing.T, m *Manager, f *fake, x Execution) {
	t.Helper()
	locked(f, func() {
		f.status = "busy"
		f.permission = true
	})
	synctest.Wait()
	time.Sleep(time.Second) // one monitor poll
	synctest.Wait()
	got, err := m.Get(context.Background(), x.ID)
	if err != nil || got.State != "waiting_for_permission" {
		t.Fatalf("pending permission: %+v %v", got, err)
	}
	now := time.Now()
	locked(f, func() { f.permission = false; f.missing = true })
	assistant(f, x.MessageID, ms(now.Add(-time.Second)), ms(now), "tool-calls", "", rejectedParts(now)...)
	synctest.Wait()
	time.Sleep(time.Second)
	synctest.Wait()
}

func noNativeMutation(t *testing.T, f *fake) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends != 1 || f.aborts != 0 {
		t.Fatalf("native mutation: sends=%d aborts=%d", f.sends, f.aborts)
	}
}

// R5b on main: the rejection is not distinguishable and stays unknown.
// Main (unlike writer 692992d) adds the ADR-017 report-only liveness block
// COMPLETED_WITHOUT_TERMINAL_EVIDENCE; state, error and guard are unchanged.
func TestF7PermissionRejectStaysUnknown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{status: "idle"}
		m := NewWithTiming(f, 300*time.Second, time.Second)
		defer m.Close()
		x, err := m.Send(context.Background(), "ses_test", "r5b fixture", "f7-reject-key")
		if err != nil {
			t.Fatal(err)
		}
		pendingThenRejected(t, m, f, x)

		got, err := m.Get(context.Background(), x.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != "unknown" || got.Unresolved == nil || got.Unresolved.Reason != "OBSERVATION_UNAVAILABLE" || got.Ended != nil {
			t.Fatalf("before monitor expiry: %+v", got)
		}
		if l := got.Liveness; l == nil || !l.StallSuspected || l.StallReason != "COMPLETED_WITHOUT_TERMINAL_EVIDENCE" || l.NativeStatus != "absent" {
			t.Fatalf("liveness before expiry: %+v", got.Liveness)
		}
		guardHeld(t, m)

		time.Sleep(300 * time.Second)
		synctest.Wait()
		got, err = m.Get(context.Background(), x.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != "unknown" || got.Unresolved == nil || got.Unresolved.Reason != "MONITOR_EXPIRED" || got.Ended != nil {
			t.Fatalf("after monitor expiry: %+v", got)
		}
		if l := got.Liveness; l == nil || l.StallReason != "COMPLETED_WITHOUT_TERMINAL_EVIDENCE" {
			t.Fatalf("liveness after expiry: %+v", got.Liveness)
		}
		guardHeld(t, m)
		noNativeMutation(t, f)

		// The Bridge exposes no rejection fact: the error stays the generic
		// unknown-outcome text and the result carries the raw native parts only.
		if !strings.HasPrefix(got.Error, "UNKNOWN_OUTCOME: ") || strings.Contains(got.Error, "PERMISSION") {
			t.Fatalf("error %q", got.Error)
		}
	})
}

// Same rejection, but native status reports "idle" instead of being absent.
// OpenCode v1.19.4 deletes idle entries from /session/status (status.ts:42-44),
// so this shape is defensive. Without terminal proof main reports "running"
// (liveness native_status "absent") until monitor expiry, then unknown.
func TestF7PermissionRejectIdleStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{status: "idle"}
		m := NewWithTiming(f, 300*time.Second, time.Second)
		defer m.Close()
		x, err := m.Send(context.Background(), "ses_test", "r5b fixture", "f7-idle-key")
		if err != nil {
			t.Fatal(err)
		}
		pendingThenRejected(t, m, f, x)
		locked(f, func() { f.missing = false; f.status = "idle" })
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()

		got, err := m.Get(context.Background(), x.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != "running" || got.Ended != nil {
			t.Fatalf("idle without proof: %+v", got)
		}
		if l := got.Liveness; l == nil || l.StallReason != "COMPLETED_WITHOUT_TERMINAL_EVIDENCE" || l.NativeStatus != "absent" {
			t.Fatalf("liveness: %+v", got.Liveness)
		}
		guardHeld(t, m)

		time.Sleep(300 * time.Second)
		synctest.Wait()
		got, err = m.Get(context.Background(), x.ID)
		if err != nil || got.State != "unknown" || got.Unresolved == nil || got.Unresolved.Reason != "MONITOR_EXPIRED" {
			t.Fatalf("after expiry: %+v %v", got, err)
		}
		guardHeld(t, m)
		noNativeMutation(t, f)
	})
}

// Reject with feedback (CorrectedError) or continue_loop_on_deny: the native
// loop continues and a later assistant message may finish "stop". Main then
// promotes to completed and releases the guard, with no record that a
// permission was rejected on the way.
func TestF7PermissionRejectThenContinueCompletes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{status: "idle"}
		m := NewWithTiming(f, 300*time.Second, time.Second)
		defer m.Close()
		x, err := m.Send(context.Background(), "ses_test", "r5b fixture", "f7-continue-key")
		if err != nil {
			t.Fatal(err)
		}
		pendingThenRejected(t, m, f, x)
		now := time.Now()
		assistant(f, x.MessageID, ms(now), ms(now.Add(time.Millisecond)), "stop", "", textPart(now.Add(time.Millisecond)))
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()

		got, err := m.Get(context.Background(), x.ID)
		if err != nil || got.State != "completed" || got.Ended == nil || got.Error != "" {
			t.Fatalf("continued run: %+v %v", got, err)
		}
		assertReleased(t, m)
		noNativeMutation(t, f)
	})
}
