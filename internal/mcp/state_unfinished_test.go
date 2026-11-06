package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// F-5 (R5b): an OpenCode restart during a tool call left an assistant message
// without time.completed. OpenCode's status map had no entry, so discovery
// inferred idle (F-4) while send_prompt admission refused the session with
// SESSION_BUSY "unfinished native assistant message". Discovery must not
// report idle for a session that admission would refuse for this reason.

const unfinishedKey = "native_unfinished_message"

const (
	historyUnfinished = `[` +
		`{"info":{"id":"msg_1","role":"user","sessionID":"ses_a","time":{"created":1}},"parts":[]},` +
		`{"info":{"id":"msg_2","role":"assistant","sessionID":"ses_a","time":{"created":2}},"parts":[]}]`
	historyCompleted = `[` +
		`{"info":{"id":"msg_1","role":"user","sessionID":"ses_a","time":{"created":1}},"parts":[]},` +
		`{"info":{"id":"msg_2","role":"assistant","sessionID":"ses_a","time":{"created":2,"completed":3}},"parts":[]}]`
)

// historyUpstream serves ses_a with the given status, permissions and history.
// history is a JSON array, "error" (HTTP 500) or "hang" (never answers until
// the request is cancelled or the test ends).
type historyUpstream struct {
	reads  atomic.Int64
	mu     sync.Mutex
	limits []string
}

func (h *historyUpstream) seenLimits() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.limits...)
}

func historyServer(t *testing.T, root, workspaceID string, runs *execution.Manager, status, permissions, history string) (*Server, *historyUpstream) {
	t.Helper()
	h := &historyUpstream{}
	stop := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			if status == "error" {
				w.WriteHeader(500)
				return
			}
			w.Write([]byte(status))
		case "/permission":
			if permissions == "error" {
				w.WriteHeader(500)
				return
			}
			w.Write([]byte(permissions))
		case "/session/ses_a/message":
			h.reads.Add(1)
			h.mu.Lock()
			h.limits = append(h.limits, r.URL.Query().Get("limit"))
			h.mu.Unlock()
			switch history {
			case "error":
				w.WriteHeader(500)
			case "hang":
				select {
				case <-r.Context().Done():
				case <-stop:
				}
			default:
				w.Write([]byte(history))
			}
		case "/session/ses_a":
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: root, WorkspaceID: workspaceID})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(up.Close)
	t.Cleanup(func() { close(stop) }) // runs before up.Close (LIFO)
	c, e := opencode.New(up.URL, "", "")
	if e != nil {
		t.Fatal(e)
	}
	p, e := security.New([]string{root}, []string{"ses_a"})
	if e != nil {
		t.Fatal(e)
	}
	return &Server{Client: c, Runs: runs, Policy: p}, h
}

// wantUnfinished asserts state and the native_unfinished_message flag on
// get_session, list_sessions and find_sessions.
func wantUnfinished(t *testing.T, s *Server, state string, flag bool) {
	t.Helper()
	for i, m := range wireSessions(t, s) {
		tool := []string{"get_session", "list_sessions", "find_sessions"}[i]
		if m["state"] != state {
			t.Errorf("%s state = %v, want %q (%v)", tool, m["state"], state, m)
		}
		got, present := m[unfinishedKey]
		if flag && got != true {
			t.Errorf("%s %s = %v, want true (%v)", tool, unfinishedKey, got, m)
		}
		if !flag && present {
			t.Errorf("%s %s present = %v, want omitted (%v)", tool, unfinishedKey, got, m)
		}
	}
}

func TestUnfinishedInferredIdleIsUnknown(t *testing.T) {
	s, h := historyServer(t, t.TempDir(), "", nil, `{}`, `[]`, historyUnfinished)
	wantUnfinished(t, s, "unknown", true)
	if h.reads.Load() == 0 {
		t.Fatal("no native history read")
	}
}

func TestUnfinishedObservedIdleIsUnknown(t *testing.T) {
	s, _ := historyServer(t, t.TempDir(), "", nil, `{"ses_a":{"type":"idle"}}`, `[]`, historyUnfinished)
	wantUnfinished(t, s, "unknown", true)
}

func TestUnfinishedAllCompletedStaysIdle(t *testing.T) {
	root := t.TempDir()
	s, _ := historyServer(t, root, "", nil, `{}`, `[]`, historyCompleted)
	wantUnfinished(t, s, "idle", false)
	wantWire(t, s, "idle", "idle", true) // F-4 fields unchanged
	s, _ = historyServer(t, root, "", nil, `{"ses_a":{"type":"idle"}}`, `[]`, historyCompleted)
	wantUnfinished(t, s, "idle", false)
	s, _ = historyServer(t, root, "", nil, `{}`, `[]`, `[]`)
	wantUnfinished(t, s, "idle", false)
}

// A user message without completion time is not an unfinished assistant message.
func TestUnfinishedUserMessageIgnored(t *testing.T) {
	history := `[{"info":{"id":"msg_1","role":"user","sessionID":"ses_a","time":{"created":1}},"parts":[]}]`
	s, _ := historyServer(t, t.TempDir(), "", nil, `{}`, `[]`, history)
	wantUnfinished(t, s, "idle", false)
}

func TestUnfinishedHistoryErrorIsUnknown(t *testing.T) {
	s, _ := historyServer(t, t.TempDir(), "", nil, `{}`, `[]`, "error")
	wantUnfinished(t, s, "unknown", false)
	s, _ = historyServer(t, t.TempDir(), "", nil, `{}`, `[]`, `{"not":"a list"}`)
	wantUnfinished(t, s, "unknown", false)
	// A 2xx JSON null is not an empty history.
	s, _ = historyServer(t, t.TempDir(), "", nil, `{}`, `[]`, `null`)
	wantUnfinished(t, s, "unknown", false)
}

func TestUnfinishedHistoryTimeoutIsUnknown(t *testing.T) {
	old := unfinishedBudget
	unfinishedBudget = 300 * time.Millisecond
	t.Cleanup(func() { unfinishedBudget = old })
	s, h := historyServer(t, t.TempDir(), "", nil, `{}`, `[]`, "hang")
	// get_session waits for the history budget, not the 3 s bounded-call limit.
	started := time.Now()
	one, e := s.call(context.Background(), "get_session", args{SessionID: "opencode:ses_a"})
	if e != nil || one.(Session).State != "unknown" || one.(Session).NativeUnfinishedMessage {
		t.Fatalf("get_session: %+v %v", one, e)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("get_session history timeout took %s, want about the 300 ms budget", elapsed)
	}
	started = time.Now()
	wantUnfinished(t, s, "unknown", false)
	if h.reads.Load() == 0 {
		t.Fatal("no native history read")
	}
	// Three surfaces; each waits at most the history budget, not the 10 s
	// discovery deadline or an unbounded read.
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("history timeout took %s", elapsed)
	}
}

// A workspace-bound session has no directory-scoped native evidence: it stays
// unknown and its history is not read for state.
func TestUnfinishedWorkspaceBoundUnchanged(t *testing.T) {
	for _, history := range []string{historyUnfinished, historyCompleted} {
		s, h := historyServer(t, t.TempDir(), "wrk_1", nil, `{}`, `[]`, history)
		wantUnfinished(t, s, "unknown", false)
		if n := h.reads.Load(); n != 0 {
			t.Fatalf("workspace-bound session history reads = %d, want 0", n)
		}
	}
}

// Permission precedence is unchanged: a pending permission is reported, and the
// session is not idle, so no history read is needed for its state.
func TestUnfinishedPendingPermissionPrecedence(t *testing.T) {
	s, _ := historyServer(t, t.TempDir(), "", nil, `{}`, `[{"sessionID":"ses_a"}]`, historyUnfinished)
	wantUnfinished(t, s, "waiting_for_permission", false)
	s, _ = historyServer(t, t.TempDir(), "", nil, `{}`, "error", historyCompleted)
	wantUnfinished(t, s, "unknown", false)
}

// Busy native status keeps its existing state; history is not consulted.
func TestUnfinishedBusyUnchanged(t *testing.T) {
	s, h := historyServer(t, t.TempDir(), "", nil, `{"ses_a":{"type":"busy"}}`, `[]`, historyUnfinished)
	wantUnfinished(t, s, "running", false)
	if n := h.reads.Load(); n != 0 {
		t.Fatalf("busy session history reads = %d, want 0", n)
	}
}

// The Bridge guard still wins over native evidence.
func TestUnfinishedGuardHeldWins(t *testing.T) {
	for mode, want := range map[string]string{
		"running":                "running",
		"waiting_for_permission": "waiting_for_permission",
		"unknown":                "unknown",
	} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			for _, history := range []string{historyUnfinished, historyCompleted, "error"} {
				s, _ := historyServer(t, root, "", heldManager(t, root, mode), `{}`, `[]`, history)
				for i, m := range wireSessions(t, s) {
					if m["state"] != want {
						t.Errorf("%s history=%.20s state = %v, want %q", []string{"get_session", "list_sessions", "find_sessions"}[i], history, m["state"], want)
					}
				}
			}
		})
	}
}

// get_session reads the full history, as admission does; list_sessions and
// find_sessions read only the bounded latest page (OpenCode ?limit=).
func TestUnfinishedHistoryReadShape(t *testing.T) {
	s, h := historyServer(t, t.TempDir(), "", nil, `{}`, `[]`, historyCompleted)
	ctx := context.Background()
	if _, e := s.call(ctx, "get_session", args{SessionID: "opencode:ses_a"}); e != nil {
		t.Fatal(e)
	}
	if got := h.seenLimits(); len(got) != 1 || got[0] != "" {
		t.Fatalf("get_session history limits = %q, want one full read", got)
	}
	if _, e := s.call(ctx, "list_sessions", args{}); e != nil {
		t.Fatal(e)
	}
	got := h.seenLimits()
	if len(got) != 2 || got[1] == "" || strings.ContainsAny(got[1], "-.") {
		t.Fatalf("list_sessions history limits = %q, want one bounded page", got)
	}
}
