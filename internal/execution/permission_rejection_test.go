package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// ADR-018 option (d) on (a), accepted 2026-11-06: a report-only
// permission_rejection block. These tests follow the ADR-018 test plan and
// extend the TestF7… tests (permission_reject_test.go). State, error, ended_at,
// the guard and idempotency stay exactly as in (a) in every case.

// fakeWitness is an in-memory RejectionWitness. It answers true only for the
// exact (session, execution, assistant message, tool call) it was given.
type fakeWitness struct {
	mu                        sync.Mutex
	ok                        bool
	session, exec, msg, call  string
	calls                     int
	lastSession, lastExec     string
	lastMessage, lastToolCall string
}

func (w *fakeWitness) VerifiedRejection(session, executionID, toolMessageID, toolCallID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	w.lastSession, w.lastExec, w.lastMessage, w.lastToolCall = session, executionID, toolMessageID, toolCallID
	return w.ok && session == w.session && executionID == w.exec && toolMessageID == w.msg && toolCallID == w.call
}

// lastAssistantID is the ID of the most recently appended correlated assistant.
func lastAssistantID(f *fake) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.messages) - 1; i >= 0; i-- {
		if f.messages[i].Info.Role == "assistant" {
			return f.messages[i].Info.ID
		}
	}
	return ""
}

// rejectedProbe drives the R5b sequence (pending permission, reject, assistant
// completed "tool-calls" with the rejected bash part, status absent), lets
// mutate change the native evidence, and returns the next Get. witness, when
// non-nil, is pointed at the rejected part unless prepare changes it.
func rejectedProbe(t *testing.T, w *fakeWitness, mutate func(f *fake, x Execution, now time.Time)) (*Manager, *fake, Execution, Execution) {
	t.Helper()
	f := &fake{status: "idle"}
	m := NewWithTiming(f, 300*time.Second, time.Second)
	t.Cleanup(m.Close)
	if w != nil {
		m.SetRejectionWitness(w)
	}
	x, err := m.Send(context.Background(), "ses_test", "r5b fixture", "adr018-key")
	if err != nil {
		t.Fatal(err)
	}
	pendingThenRejected(t, m, f, x)
	if w != nil {
		w.mu.Lock()
		if w.session == "" {
			w.session, w.exec, w.msg = "ses_test", x.ID, lastAssistantID(f)
		}
		if w.call == "" {
			w.call = "call_r5b"
		}
		w.mu.Unlock()
	}
	if mutate != nil {
		mutate(f, x, time.Now())
	}
	synctest.Wait()
	time.Sleep(time.Second)
	synctest.Wait()
	got, err := m.Get(context.Background(), x.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m, f, x, got
}

// unchangedA asserts the (a) outcome: unknown, generic error, no ended_at,
// guard held, exactly one Prompt and no Abort.
func unchangedA(t *testing.T, m *Manager, f *fake, got Execution, reason string) {
	t.Helper()
	if got.State != "unknown" || got.Unresolved == nil || got.Unresolved.Reason != reason || got.Ended != nil {
		t.Fatalf("state changed by the report: %+v", got)
	}
	if !strings.HasPrefix(got.Error, "UNKNOWN_OUTCOME: ") || strings.Contains(got.Error, "PERMISSION") {
		t.Fatalf("error changed by the report: %q", got.Error)
	}
	guardHeld(t, m)
	noNativeMutation(t, f)
}

func wantRejection(t *testing.T, got Execution, basis string) {
	t.Helper()
	r := got.PermissionRejection
	if r == nil || r.Basis != basis || r.Tool != "bash" || r.ToolCallID != "call_r5b" || r.ObservedAt.IsZero() {
		t.Fatalf("permission_rejection = %+v, want basis %q for bash/call_r5b", r, basis)
	}
}

func wantNoRejection(t *testing.T, got Execution) {
	t.Helper()
	if got.PermissionRejection != nil {
		t.Fatalf("permission_rejection = %+v, want omitted", got.PermissionRejection)
	}
}

// Test plan 1: native_event only when items 2-4 hold; persisted_text_only
// otherwise. State, error, ended_at and the guard are unchanged.
func TestADR018PersistedTextOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, f, x, got := rejectedProbe(t, nil, nil)
		wantRejection(t, got, "persisted_text_only")
		unchangedA(t, m, f, got, "OBSERVATION_UNAVAILABLE")

		// The block survives monitor expiry; (a) still owns the state.
		time.Sleep(300 * time.Second)
		synctest.Wait()
		got, err := m.Get(context.Background(), x.ID)
		if err != nil {
			t.Fatal(err)
		}
		wantRejection(t, got, "persisted_text_only")
		unchangedA(t, m, f, got, "MONITOR_EXPIRED")
	})
}

func TestADR018NativeEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &fakeWitness{ok: true}
		m, f, x, got := rejectedProbe(t, w, nil)
		wantRejection(t, got, "native_event")
		unchangedA(t, m, f, got, "OBSERVATION_UNAVAILABLE")
		w.mu.Lock()
		args := []string{w.lastSession, w.lastExec, w.lastMessage, w.lastToolCall}
		w.mu.Unlock()
		if want := []string{"ses_test", x.ID, lastAssistantID(f), "call_r5b"}; strings.Join(args, "|") != strings.Join(want, "|") {
			t.Fatalf("witness asked %v, want %v", args, want)
		}
	})
}

// Item 4 missing or contradicted: never native_event.
func TestADR018WitnessMissingOrContradicted(t *testing.T) {
	for name, w := range map[string]*fakeWitness{
		"observer gap or fence (no verified reply)": {ok: false},
		"mismatched tool call":                      {ok: true, call: "call_other"},
		"other execution":                           {ok: true, session: "ses_test", exec: "exec_other", msg: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, f, _, got := rejectedProbe(t, w, nil)
				wantRejection(t, got, "persisted_text_only")
				unchangedA(t, m, f, got, "OBSERVATION_UNAVAILABLE")
			})
		})
	}
}

// Items 2-3 removed or contradicted: no block at all, whatever the witness says.
func TestADR018PersistedEvidenceContradicted(t *testing.T) {
	part := func(call, status, errText string) string {
		return fmt.Sprintf(`{"type":"tool","tool":"bash","callID":%q,"state":{"status":%q,"error":%q}}`, call, status, errText)
	}
	extra := func(parts ...string) func(f *fake, x Execution, now time.Time) {
		return func(f *fake, x Execution, now time.Time) {
			f.mu.Lock()
			defer f.mu.Unlock()
			last := &f.messages[len(f.messages)-1]
			for _, p := range parts {
				last.Parts = append(last.Parts, json.RawMessage(p))
			}
		}
	}
	for name, mutate := range map[string]func(f *fake, x Execution, now time.Time){
		"later assistant unfinished": func(f *fake, x Execution, now time.Time) {
			assistant(f, x.MessageID, ms(now.Add(time.Millisecond)), 0, "", "")
		},
		"second errored part":  extra(part("call_two", "error", "boom")),
		"second rejected part": extra(part("call_two", "error", rejectedText)),
		"running part":         extra(part("call_two", "running", "")),
		"corrected error text": func(f *fake, x Execution, now time.Time) {
			f.mu.Lock()
			defer f.mu.Unlock()
			last := &f.messages[len(f.messages)-1]
			last.Parts[1] = json.RawMessage(part("call_r5b", "error", "The user rejected permission to use this specific tool call with the following feedback: no"))
		},
		"finish not tool-calls": func(f *fake, x Execution, now time.Time) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.messages[len(f.messages)-1].Info.Finish = "unknown"
		},
		"unparseable part": extra(`{"type":"tool","state":`),
		"hostile tool call id": func(f *fake, x Execution, now time.Time) {
			f.mu.Lock()
			defer f.mu.Unlock()
			last := &f.messages[len(f.messages)-1]
			last.Parts[1] = json.RawMessage(part("call r5b\n<script>", "error", rejectedText))
		},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, f, _, got := rejectedProbe(t, &fakeWitness{ok: true}, mutate)
				wantNoRejection(t, got)
				unchangedA(t, m, f, got, "OBSERVATION_UNAVAILABLE")
			})
		})
	}
}

// A later completed assistant (continue_loop_on_deny or CorrectedError): the
// existing promotion to completed is unchanged and carries no block.
func TestADR018ContinueCompletesWithoutBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, f, _, got := rejectedProbe(t, &fakeWitness{ok: true}, func(f *fake, x Execution, now time.Time) {
			assistant(f, x.MessageID, ms(now), ms(now.Add(time.Millisecond)), "stop", "", textPart(now.Add(time.Millisecond)))
		})
		if got.State != "completed" || got.Ended == nil || got.Error != "" {
			t.Fatalf("continued run: %+v", got)
		}
		wantNoRejection(t, got)
		assertReleased(t, m)
		noNativeMutation(t, f)
	})
}

// A pending permission for the session: waiting_for_permission as before, no block.
func TestADR018PendingPermissionNoBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, f, _, got := rejectedProbe(t, &fakeWitness{ok: true}, func(f *fake, x Execution, now time.Time) {
			locked(f, func() { f.permission = true })
		})
		if got.State != "waiting_for_permission" {
			t.Fatalf("pending permission: %+v", got)
		}
		wantNoRejection(t, got)
		guardHeld(t, m)
		noNativeMutation(t, f)
	})
}

// Test plan 3: the block never triggers an Abort or a reply; replay is unchanged.
func TestADR018NoMutationAndReplayUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, f, x, got := rejectedProbe(t, &fakeWitness{ok: true}, nil)
		wantRejection(t, got, "native_event")
		for i := 0; i < 3; i++ {
			if _, err := m.Get(context.Background(), x.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.Result(context.Background(), x.ID); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Second)
			synctest.Wait()
		}
		replay, err := m.Send(context.Background(), "ses_test", "r5b fixture", "adr018-key")
		if err != nil || replay.ID != x.ID || replay.State != "unknown" {
			t.Fatalf("replay: %+v %v", replay, err)
		}
		noNativeMutation(t, f)
		guardHeld(t, m)
	})
}

// Test plan 4: no extra native reads. One on-demand probe after monitor expiry
// makes exactly one Get, Status, History and Permissions read, as without the
// witness (TestLivenessAddsNoNativeReads).
func TestADR018AddsNoNativeReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, f, x, _ := rejectedProbe(t, &fakeWitness{ok: true}, nil)
		time.Sleep(300 * time.Second)
		synctest.Wait()
		if _, err := m.Get(context.Background(), x.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		before := f.reads()
		got, err := m.Get(context.Background(), x.ID)
		if err != nil {
			t.Fatal(err)
		}
		after := f.reads()
		wantRejection(t, got, "native_event")
		for i, name := range []string{"Get", "Status", "History", "Permissions"} {
			if d := after[i] - before[i]; d != 1 {
				t.Fatalf("%s reads in one probe = %d, want 1", name, d)
			}
		}
	})
}

// The result carries the same block as the status.
func TestADR018ResultCarriesBlock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _, x, _ := rejectedProbe(t, nil, nil)
		r, _, err := m.Result(context.Background(), x.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.PermissionRejection == nil || r.PermissionRejection.Basis != "persisted_text_only" {
			t.Fatalf("result block: %+v", r.PermissionRejection)
		}
		// Snapshots are copies: mutating one never changes the run.
		r.PermissionRejection.Basis = "native_event"
		again, _ := m.Snapshot(context.Background(), x.ID)
		if again.PermissionRejection == nil || again.PermissionRejection.Basis != "persisted_text_only" {
			t.Fatalf("snapshot aliased: %+v", again.PermissionRejection)
		}
	})
}

func TestADR018JSONContract(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	x := Execution{ID: "e", SessionID: "opencode:ses_test", MessageID: "msg_x", State: "unknown", Started: now, Updated: now}
	b, _ := json.Marshal(x)
	if strings.Contains(string(b), "permission_rejection") {
		t.Fatalf("absent block must be omitted: %s", b)
	}
	x.PermissionRejection = &PermissionRejection{ObservedAt: now, Tool: "bash", ToolCallID: "call_1", Basis: "persisted_text_only"}
	b, _ = json.Marshal(x.PermissionRejection)
	if string(b) != `{"observed_at":"2026-11-06T12:00:00Z","tool":"bash","tool_call_id":"call_1","basis":"persisted_text_only"}` {
		t.Fatalf("block %s", b)
	}
}

// The witness is asked about the rejected part's own tool call ID, not a fixed one.
func TestADR018WitnessUsesPartCallID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &fakeWitness{ok: true, call: "call_alt"}
		m, f, _, got := rejectedProbe(t, w, func(f *fake, x Execution, now time.Time) {
			f.mu.Lock()
			defer f.mu.Unlock()
			last := &f.messages[len(f.messages)-1]
			last.Parts[1] = json.RawMessage(fmt.Sprintf(`{"type":"tool","tool":"bash","callID":"call_alt","state":{"status":"error","error":%q}}`, rejectedText))
		})
		r := got.PermissionRejection
		if r == nil || r.Basis != "native_event" || r.ToolCallID != "call_alt" {
			t.Fatalf("permission_rejection = %+v, want native_event for call_alt", r)
		}
		unchangedA(t, m, f, got, "OBSERVATION_UNAVAILABLE")
	})
}

// Item 2 needs the rejected message to be the unique latest correlated
// assistant: a tie or an out-of-order completion is ambiguous, so no block.
func TestADR018LatestAssistantAmbiguous(t *testing.T) {
	sibling := func(dCreated, dCompleted int64) func(f *fake, x Execution, now time.Time) {
		return func(f *fake, x Execution, now time.Time) {
			f.mu.Lock()
			rejected := f.messages[len(f.messages)-1].Info
			f.mu.Unlock()
			assistant(f, x.MessageID, rejected.Time.Created+dCreated, rejected.Time.Completed+dCompleted, "tool-calls", "")
		}
	}
	for name, mutate := range map[string]func(f *fake, x Execution, now time.Time){
		"same created time":           sibling(0, 0),
		"earlier but completed later": sibling(-1, 5),
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, f, _, got := rejectedProbe(t, &fakeWitness{ok: true}, mutate)
				wantNoRejection(t, got)
				unchangedA(t, m, f, got, "OBSERVATION_UNAVAILABLE")
			})
		})
	}
}
