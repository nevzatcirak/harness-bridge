package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// F-4 (R3b): OpenCode 1.19.4 SessionStatus.set(idle) deletes the session from the
// per-instance status map and SessionStatus.get returns idle for an absent
// session, so GET /session/status lists only non-idle sessions. Discovery must
// infer idle from absence, but only from complete, successful evidence.

const inferredKey = "native_state_inferred_from_absence"

// wireSessions returns the JSON wire form of ses_a from get_session,
// list_sessions and find_sessions.
func wireSessions(t *testing.T, s *Server) []map[string]any {
	t.Helper()
	ctx := context.Background()
	got, e := s.call(ctx, "get_session", args{SessionID: "opencode:ses_a"})
	if e != nil {
		t.Fatal(e)
	}
	listed, e := s.call(ctx, "list_sessions", args{})
	if e != nil || len(listed.(Sessions).Sessions) != 1 {
		t.Fatal(listed, e)
	}
	found, e := s.call(ctx, "find_sessions", args{})
	if e != nil || len(found.(FoundSessions).Sessions) != 1 {
		t.Fatal(found, e)
	}
	out := []map[string]any{}
	for _, v := range []Session{got.(Session), listed.(Sessions).Sessions[0], found.(FoundSessions).Sessions[0]} {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		m := map[string]any{}
		if e := json.Unmarshal(b, &m); e != nil {
			t.Fatal(e)
		}
		out = append(out, m)
	}
	return out
}

// wantWire asserts state, native_state ("" = omitted) and the inferred flag
// (false = omitted) on every discovery surface.
func wantWire(t *testing.T, s *Server, state, nativeState string, inferred bool) {
	t.Helper()
	for i, m := range wireSessions(t, s) {
		tool := []string{"get_session", "list_sessions", "find_sessions"}[i]
		if m["state"] != state {
			t.Errorf("%s state = %v, want %q (%v)", tool, m["state"], state, m)
		}
		if got, _ := m["native_state"].(string); got != nativeState {
			t.Errorf("%s native_state = %q, want %q (%v)", tool, got, nativeState, m)
		}
		flag, present := m[inferredKey]
		if inferred && flag != true {
			t.Errorf("%s %s = %v, want true (%v)", tool, inferredKey, flag, m)
		}
		if !inferred && present {
			t.Errorf("%s %s present = %v, want omitted (%v)", tool, inferredKey, flag, m)
		}
	}
}

func TestStateAbsenceInfersIdle(t *testing.T) {
	root := t.TempDir()
	wantWire(t, guardServer(t, root, nil, `{}`, `[]`), "idle", "idle", true)
	// A Manager with no execution holds no guard: absence is still idle.
	wantWire(t, guardServer(t, root, heldManager(t, root, ""), `{}`, `[]`), "idle", "idle", true)
}

func TestStateExplicitIdleNotInferred(t *testing.T) {
	root := t.TempDir()
	wantWire(t, guardServer(t, root, nil, `{"ses_a":{"type":"idle"}}`, `[]`), "idle", "idle", false)
}

func TestStateAbsenceStatusError(t *testing.T) {
	root := t.TempDir()
	wantWire(t, guardServer(t, root, nil, "error", `[]`), "unknown", "", false)
}

func TestStateAbsencePermissionError(t *testing.T) {
	root := t.TempDir()
	// Status evidence is still reported; state must not be idle.
	wantWire(t, guardServer(t, root, nil, `{}`, "error"), "unknown", "idle", true)
}

func TestStateAbsencePendingPermission(t *testing.T) {
	root := t.TempDir()
	wantWire(t, guardServer(t, root, nil, `{}`, `[{"sessionID":"ses_a"}]`), "waiting_for_permission", "idle", true)
}

func TestStateAbsenceGuardHeldNeverIdle(t *testing.T) {
	for mode, want := range map[string]string{
		"reserved":               "running",
		"submitting":             "running",
		"running":                "running",
		"waiting_for_permission": "waiting_for_permission",
		"unknown":                "unknown",
	} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			// native_state still reports the native observation; state follows the guard.
			wantWire(t, guardServer(t, root, heldManager(t, root, mode), `{}`, `[]`), want, "idle", true)
		})
	}
}

func TestStateUnknownNativeType(t *testing.T) {
	root := t.TempDir()
	wantWire(t, guardServer(t, root, nil, `{"ses_a":{"type":"foo"}}`, `[]`), "unknown", "foo", false)
}

// Absence counts only in the status map of the session's own directory-scoped
// client: another directory's map never makes a busy session idle, and a session
// listed busy elsewhere does not block idle inference in its own directory.
func TestStateAbsenceSameDirectory(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	var mu sync.Mutex
	statuses := map[string]string{rootA: `{"ses_b":{"type":"busy"}}`, rootB: `{"ses_a":{"type":"busy"}}`}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			mu.Lock()
			v, ok := statuses[r.URL.Query().Get("directory")]
			mu.Unlock()
			if !ok {
				w.WriteHeader(500)
				return
			}
			w.Write([]byte(v))
		case "/permission":
			w.Write([]byte(`[]`))
		case "/session/ses_a":
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: rootA})
		case "/session/ses_b":
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_b", Directory: rootB})
		case "/session/ses_a/message", "/session/ses_b/message":
			w.Write([]byte(`[]`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	c, _ := opencode.New(up.URL, "", "")
	p, _ := security.New([]string{rootA, rootB}, []string{"ses_a", "ses_b"})
	s := &Server{Client: c, Policy: p}
	listed, e := s.call(context.Background(), "list_sessions", args{})
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]string{}
	for _, v := range listed.(Sessions).Sessions {
		got[v.NativeID] = v.State
	}
	if got["ses_a"] != "idle" || got["ses_b"] != "idle" {
		t.Fatalf("states %v, want both idle from their own directory's absence", got)
	}
	mu.Lock()
	statuses[rootA] = `{"ses_a":{"type":"busy"}}`
	statuses[rootB] = `{}`
	mu.Unlock()
	one, e := s.call(context.Background(), "get_session", args{SessionID: "opencode:ses_a"})
	if e != nil || one.(Session).State != "running" {
		t.Fatalf("busy in own directory: %+v %v", one, e)
	}
}
