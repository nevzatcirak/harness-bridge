package mcp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// windowRunner is the execution manager's native view for the guard-window
// test: busy from Prompt until the test completes the turn, then absent (idle)
// with terminal proof.
type windowRunner struct {
	mu        sync.Mutex
	directory string
	prompted  bool
	done      bool
	messages  []opencode.Message
}

func (w *windowRunner) Get(_ context.Context, id string) (opencode.Session, error) {
	return opencode.Session{ID: id, Directory: w.directory}, nil
}
func (w *windowRunner) Status(context.Context) (map[string]opencode.Status, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.prompted || w.done {
		return map[string]opencode.Status{}, nil
	}
	return map[string]opencode.Status{"ses_a": {Type: "busy"}}, nil
}
func (w *windowRunner) Permissions(context.Context) ([]opencode.Permission, error) { return nil, nil }
func (w *windowRunner) History(context.Context, string) ([]opencode.Message, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]opencode.Message(nil), w.messages...), nil
}
func (w *windowRunner) Prompt(_ context.Context, session, _, mid string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	user := opencode.Message{}
	user.Info.ID, user.Info.Role, user.Info.SessionID = mid, "user", session
	w.messages = append(w.messages, user)
	w.prompted = true
	return nil
}
func (w *windowRunner) Abort(context.Context, string) error { return nil }
func (w *windowRunner) finish(mid string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	msg := opencode.Message{}
	msg.Info.ID, msg.Info.Role, msg.Info.SessionID, msg.Info.ParentID, msg.Info.Finish = "msg_done"+mid, "assistant", "ses_a", mid, "stop"
	msg.Info.Time.Completed = 1
	w.messages = append(w.messages, msg)
	w.done = true
}

// #95 finding 1: a guard held when needsHistory decides (so no history read
// happens) and released before view must give unknown, never idle. Native
// discovery evidence is idle with no pending permission throughout.
func TestGuardReleasedBetweenNeedsHistoryAndView(t *testing.T) {
	for _, tool := range []string{"get_session", "list_sessions", "find_sessions"} {
		t.Run(tool, func(t *testing.T) {
			root := t.TempDir()
			w := &windowRunner{directory: root}
			runs := execution.NewWithTiming(w, 30*time.Second, time.Millisecond)
			t.Cleanup(runs.Close)
			x, err := runs.Send(context.Background(), "ses_a", "prompt", "window-key-1")
			if err != nil {
				t.Fatal(err)
			}
			waitExecution(t, runs, x.ID, "running")
			s := guardServer(t, root, runs, `{"ses_a":{"type":"idle"}}`, `[]`)
			if held, _ := runs.SessionGuard("ses_a"); !held {
				t.Fatal("guard not held before the call")
			}
			fired := 0
			guardWindowHook = func() {
				fired++
				w.finish(x.MessageID)
				waitExecution(t, runs, x.ID, "completed")
				if held, _ := runs.SessionGuard("ses_a"); held {
					t.Fatal("guard still held after completion")
				}
			}
			t.Cleanup(func() { guardWindowHook = nil })
			ctx := context.Background()
			var state string
			var unfinished bool
			switch tool {
			case "get_session":
				v, e := s.call(ctx, tool, args{SessionID: "opencode:ses_a"})
				if e != nil {
					t.Fatal(e)
				}
				state, unfinished = v.(Session).State, v.(Session).NativeUnfinishedMessage
			case "list_sessions":
				v, e := s.call(ctx, tool, args{})
				if e != nil || len(v.(Sessions).Sessions) != 1 {
					t.Fatal(v, e)
				}
				state, unfinished = v.(Sessions).Sessions[0].State, v.(Sessions).Sessions[0].NativeUnfinishedMessage
			case "find_sessions":
				v, e := s.call(ctx, tool, args{})
				if e != nil || len(v.(FoundSessions).Sessions) != 1 {
					t.Fatal(v, e)
				}
				state, unfinished = v.(FoundSessions).Sessions[0].State, v.(FoundSessions).Sessions[0].NativeUnfinishedMessage
			}
			guardWindowHook = nil
			if fired != 1 {
				t.Fatalf("window hook fired %d times, want 1", fired)
			}
			if state != "unknown" || unfinished {
				t.Fatalf("%s after the guard was released in the window: state %q unfinished %v, want unknown", tool, state, unfinished)
			}
			// Outside the window the session is read normally and is idle.
			wantStates(t, s, "idle")
		})
	}
}
