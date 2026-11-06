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
		case "/session/ses_a/message":
			w.Write([]byte(`[]`)) // F-5: no unfinished assistant message
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
	// H4A-F2-FOLLOWUP: no directory-scoped permission read is made for a
	// workspace-bound session, so a permission pending in the directory
	// instance is not evidence either.
	wantWire(t, workspaceServer(t, "wrk_1", `{}`, `[{"sessionID":"ses_a"}]`), "unknown", "", false)
}

// H4A-F2-FOLLOWUP: directory-scoped status is not read for a workspace-bound
// session, so even an explicit entry in the directory instance's map is not
// reported (it describes an instance that does not serve the session).
func TestStateWorkspaceBoundExplicitBusy(t *testing.T) {
	wantWire(t, workspaceServer(t, "wrk_1", `{"ses_a":{"type":"busy"}}`, `[]`), "unknown", "", false)
	wantWire(t, workspaceServer(t, "wrk_1", `{"ses_a":{"type":"idle"}}`, `[]`), "unknown", "", false)
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

// TEST-HARDENING-01 (#45 gate P3-2): a workspace-bound session with a held
// Bridge guard is reported unknown on every discovery surface (CONTRACTS.md,
// "Session state and the Bridge guard"). Discovery makes no permission read for
// it, so the guard maps as for a failed permission read. The directory
// instance's status and pending permission must not leak into the result.
func TestStateWorkspaceBoundHeldGuardUnknown(t *testing.T) {
	for _, mode := range []string{"reserved", "submitting", "running", "waiting_for_permission", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			for _, upstream := range []struct{ status, permissions string }{
				{`{}`, `[]`},
				{`{"ses_a":{"type":"busy"}}`, `[{"sessionID":"ses_a"}]`},
			} {
				s := workspaceServer(t, "wrk_1", upstream.status, upstream.permissions)
				s.Runs = heldManager(t, t.TempDir(), mode)
				wantWire(t, s, "unknown", "", false)
			}
		})
	}
}
