package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// R2-PR16-REVIEW: OpenCode 1.19.4 routes session-bound requests by the session's
// workspaceID, but /session/status carries no session ID and is served by the
// ?directory= instance. A workspace-bound session busy in its workspace instance
// is therefore absent from the map the Bridge reads, so absence proves nothing.

// workspaceServer serves ses_a with the given workspaceID and the given raw
// /session/status and /permission bodies.
func workspaceServer(t *testing.T, workspaceID, status, permissions string) *Server {
	t.Helper()
	root := t.TempDir()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			w.Write([]byte(status))
		case "/permission":
			w.Write([]byte(permissions))
		default:
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: root, WorkspaceID: workspaceID})
		}
	}))
	t.Cleanup(up.Close)
	c, e := opencode.New(up.URL, "", "")
	if e != nil {
		t.Fatal(e)
	}
	p, e := security.New([]string{root}, []string{"ses_a"})
	if e != nil {
		t.Fatal(e)
	}
	return &Server{Client: c, Policy: p}
}

// Finding 1: workspace-bound + absent + successful reads -> unknown, no inference.
func TestStateAbsenceWorkspaceBoundUnknown(t *testing.T) {
	wantWire(t, workspaceServer(t, "wrk_1", `{}`, `[]`), "unknown", "", false)
	// A pending permission still wins; absence still infers nothing.
	wantWire(t, workspaceServer(t, "wrk_1", `{}`, `[{"sessionID":"ses_a"}]`), "waiting_for_permission", "", false)
}

// Finding 1: an explicit entry for a workspace-bound session is unchanged.
func TestStateWorkspaceBoundExplicitBusy(t *testing.T) {
	wantWire(t, workspaceServer(t, "wrk_1", `{"ses_a":{"type":"busy"}}`, `[]`), "running", "busy", false)
	wantWire(t, workspaceServer(t, "wrk_1", `{"ses_a":{"type":"idle"}}`, `[]`), "idle", "idle", false)
}

// Finding 1: a session without a workspace still infers idle from absence.
func TestStateAbsenceNoWorkspaceStillIdle(t *testing.T) {
	wantWire(t, workspaceServer(t, "", `{}`, `[]`), "idle", "idle", true)
}

// Finding 2: a 2xx JSON null status body is not a successful empty map.
func TestStateNullStatusBodyUnknown(t *testing.T) {
	wantWire(t, workspaceServer(t, "", `null`, `[]`), "unknown", "", false)
	wantWire(t, workspaceServer(t, "", ` null `, `[]`), "unknown", "", false)
}
