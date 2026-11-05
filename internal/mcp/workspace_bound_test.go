package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// H4A-F2-FOLLOWUP: a directory-scoped request makes OpenCode load (bootstrap)
// the instance for that directory, which imports its .opencode plugins
// (H4A-F2-VERIFY). A workspace-bound session is served by its workspace's
// instance, so discovery must not issue directory-scoped native reads for it.

type recordedUpstream struct {
	mu       sync.Mutex
	requests []string
}

func (r *recordedUpstream) scoped() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, req := range r.requests {
		if strings.Contains(req, "directory=") {
			out = append(out, req)
		}
	}
	sort.Strings(out)
	return out
}

// recordingServer serves two sessions in one root: ses_a with workspaceID
// (empty or not) and, optionally, the plain session ses_b.
func recordingServer(t *testing.T, workspaceID string, withPlain bool) (*Server, *recordedUpstream, string) {
	t.Helper()
	root := t.TempDir()
	rec := &recordedUpstream{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		path := r.URL.Path
		if q := r.URL.Query().Get("directory"); q != "" {
			path += "?directory=" + q
		}
		rec.requests = append(rec.requests, r.Method+" "+path)
		rec.mu.Unlock()
		switch r.URL.Path {
		case "/session/status":
			w.Write([]byte(`{"ses_a":{"type":"busy"},"ses_b":{"type":"busy"}}`))
		case "/permission":
			w.Write([]byte(`[]`))
		case "/agent":
			w.Write([]byte(`[{"name":"build","mode":"primary"}]`))
		case "/doc":
			w.Write([]byte(`{"paths":{}}`))
		case "/session/ses_a":
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: root, WorkspaceID: workspaceID})
		case "/session/ses_b":
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_b", Directory: root})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(up.Close)
	c, e := opencode.New(up.URL, "", "")
	if e != nil {
		t.Fatal(e)
	}
	ids := []string{"ses_a"}
	if withPlain {
		ids = append(ids, "ses_b")
	}
	p, e := security.New([]string{root}, ids)
	if e != nil {
		t.Fatal(e)
	}
	return &Server{Client: c, Policy: p}, rec, root
}

func callAll(t *testing.T, s *Server, capabilitiesFail bool) {
	t.Helper()
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		a    args
	}{
		{"get_session", args{SessionID: "opencode:ses_a"}},
		{"list_sessions", args{}},
		{"find_sessions", args{}},
		{"list_repositories", args{}},
	} {
		if _, e := s.call(ctx, tc.name, tc.a); e != nil {
			t.Fatalf("%s: %v", tc.name, e)
		}
	}
	_, e := s.call(ctx, "get_harness_capabilities", args{SessionID: "opencode:ses_a"})
	if capabilitiesFail {
		var typed *execution.Error
		if e == nil || !errors.As(e, &typed) || typed.Code != "INVALID_INPUT" || typed.Retryable || !strings.Contains(typed.Message, "workspace-bound") {
			t.Fatalf("get_harness_capabilities for a workspace-bound session: %v", e)
		}
	} else if e != nil {
		t.Fatalf("get_harness_capabilities: %v", e)
	}
}

func TestWorkspaceBoundSessionMakesNoDirectoryScopedCalls(t *testing.T) {
	s, rec, _ := recordingServer(t, "wrk_remote", false)
	callAll(t, s, true)
	if got := rec.scoped(); len(got) != 0 {
		t.Fatalf("workspace-bound session issued directory-scoped native requests: %v", got)
	}
	got, e := s.call(context.Background(), "get_session", args{SessionID: "opencode:ses_a"})
	if e != nil {
		t.Fatal(e)
	}
	v := got.(Session)
	if v.State != "unknown" || v.NativeState != "" || v.NativeStateInferredFromAbsence || v.NativeWorkspaceID != "wrk_remote" {
		t.Fatalf("workspace-bound view: %+v", v)
	}
}

// A non-workspace session keeps exactly the directory-scoped request set it had
// before this change: status and permission per discovery, /agent and /doc for
// capabilities.
func TestNonWorkspaceSessionRequestSetUnchanged(t *testing.T) {
	s, rec, root := recordingServer(t, "", false)
	callAll(t, s, false)
	d := "?directory=" + root
	want := []string{}
	// get_session, list_sessions, find_sessions and list_repositories each read
	// status and permission once; get_harness_capabilities reads /agent and /doc.
	for i := 0; i < 4; i++ {
		want = append(want, "GET /permission"+d, "GET /session/status"+d)
	}
	want = append(want, "GET /agent"+d, "GET /doc"+d)
	sort.Strings(want)
	if got := rec.scoped(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("directory-scoped requests changed:\ngot  %v\nwant %v", got, want)
	}
}

// A workspace-bound and a plain session in the same directory: only the plain
// session's group reads native state, and the workspace-bound one does not
// inherit that evidence.
func TestWorkspaceBoundDoesNotShareDirectoryEvidence(t *testing.T) {
	s, rec, root := recordingServer(t, "wrk_remote", true)
	listed, e := s.call(context.Background(), "list_sessions", args{})
	if e != nil {
		t.Fatal(e)
	}
	states := map[string]Session{}
	for _, v := range listed.(Sessions).Sessions {
		states[v.NativeID] = v
	}
	if a := states["ses_a"]; a.State != "unknown" || a.NativeState != "" {
		t.Errorf("workspace-bound ses_a = %+v, want unknown without native_state", a)
	}
	if b := states["ses_b"]; b.State != "running" || b.NativeState != "busy" {
		t.Errorf("plain ses_b = %+v, want running/busy", b)
	}
	d := "?directory=" + root
	want := []string{"GET /permission" + d, "GET /session/status" + d}
	if got := rec.scoped(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("directory-scoped requests: got %v want %v", got, want)
	}
}
