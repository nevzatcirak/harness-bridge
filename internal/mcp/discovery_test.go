package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

func TestRepositoryDiscovery(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v %s", e, b)
		}
	}
	git("init")
	git("-c", "user.name=Test", "-c", "user.email=t@example.invalid", "commit", "--allow-empty", "-m", "seed")
	linked := filepath.Join(t.TempDir(), "linked")
	git("worktree", "add", "-b", "linked", linked)
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if e := os.Symlink(outside, link); e != nil {
		t.Fatal(e)
	}
	sessions := map[string]opencode.Session{
		"ses_a": {ID: "ses_a", Directory: root}, "ses_b": {ID: "ses_b", Directory: root, ParentID: "ses_a"},
		"ses_c": {ID: "ses_c", Directory: linked, ParentID: "ses_denied"}, "ses_out": {ID: "ses_out", Directory: outside},
		"ses_link": {ID: "ses_link", Directory: link}, "ses_traversal": {ID: "ses_traversal", Directory: root + "/../" + filepath.Base(root)},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("mutation")
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/session/status":
			json.NewEncoder(w).Encode(map[string]opencode.Status{"ses_b": {Type: "busy"}})
		case "/permission":
			json.NewEncoder(w).Encode([]opencode.Permission{{SessionID: "ses_c"}})
		case "/agent":
			w.Write([]byte(`[{"name":"build","mode":"primary"},{"name":"explore","mode":"subagent"},{"name":"internal","mode":"primary","hidden":true}]`))
		case "/doc":
			w.Write([]byte(`{"paths":{"/session":{"post":{}},"/session/{sessionID}/children":{"get":{}}}}`))
		case "/session/ses_b/message":
			if r.URL.Query().Get("directory") != root || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("before") != "msg_cursor" {
				t.Error("pagination scope", r.URL)
			}
			w.Header().Set("X-Next-Cursor", "opaque_next")
			w.Write([]byte(`[{"info":{"id":"msg_one"},"parts":[]}]`))
		default:
			id := r.URL.Path[len("/session/"):]
			if id == "ses_denied" {
				t.Error("unauthorized lookup")
			}
			v, ok := sessions[id]
			if !ok {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(v)
		}
	}))
	defer upstream.Close()
	client, _ := opencode.New(upstream.URL, "", "")
	policy, _ := security.New([]string{root, linked}, []string{"ses_a", "ses_b", "ses_c", "ses_out", "ses_link", "ses_traversal"})
	s := &Server{Client: client, Policy: policy, ReadOnly: true}
	ctx := context.Background()
	all, e := s.discover(ctx)
	if e != nil || len(all) != 3 {
		t.Fatal(all, e)
	}
	if all[0].State != "unknown" || all[1].State != "running" || all[2].State != "waiting_for_permission" {
		t.Fatal(all)
	}
	if all[0].Repository == nil || all[0].Repository.ID != all[2].Repository.ID || all[0].Workspace.ID == all[2].Workspace.ID || all[0].Workspace.ID != all[1].Workspace.ID {
		t.Fatal("repository/worktree grouping", all)
	}
	if all[1].ParentSessionID != "opencode:ses_a" || all[2].ParentSessionID != "" {
		t.Fatal("relationship disclosure", all)
	}
	page, e := s.call(ctx, "find_sessions", args{RepositoryID: all[0].Repository.ID, Limit: 2})
	if e != nil {
		t.Fatal(e)
	}
	p := page.(FoundSessions)
	if len(p.Sessions) != 2 || p.NextOffset == nil || *p.NextOffset != 2 {
		t.Fatal(p)
	}
	page, e = s.call(ctx, "find_sessions", args{Offset: 2, Limit: 2})
	if e != nil || len(page.(FoundSessions).Sessions) != 1 {
		t.Fatal(page, e)
	}
	page, e = s.call(ctx, "find_sessions", args{ParentSessionID: "opencode:ses_a"})
	if e != nil || len(page.(FoundSessions).Sessions) != 1 {
		t.Fatal(page, e)
	}
	repos, e := s.call(ctx, "list_repositories", args{})
	if e != nil || len(repos.(Repositories).Repositories) != 1 {
		t.Fatal(repos, e)
	}
	if _, e = s.call(ctx, "get_repository", args{RepositoryID: "repo:invisible"}); envelope(e).Code != "NOT_FOUND" {
		t.Fatal(e)
	}
	caps, e := s.call(ctx, "get_harness_capabilities", args{SessionID: "opencode:ses_a"})
	if e != nil {
		t.Fatal(e)
	}
	c := caps.(Capabilities)
	if len(c.PrimaryAgents) != 1 || len(c.Subagents) != 1 || c.NativeDelegation != "unknown" || !c.ChildSessionInspection || !c.NativeSessionCreation || c.BridgeSessionCreation {
		t.Fatal(c)
	}
	if _, e = s.call(ctx, "get_harness_capabilities", args{SessionID: "opencode:ses_denied"}); e == nil {
		t.Fatal("capability ACL")
	}
	history, e := s.call(ctx, "read_session_history", args{SessionID: "opencode:ses_b", Limit: 1, Before: "msg_cursor"})
	if e != nil || len(history.(History).Messages) != 1 || history.(History).NextCursor != "opaque_next" {
		t.Fatal(history, e)
	}
	for _, a := range []args{{Limit: 201}, {Offset: -1}, {Before: "../escape"}} {
		if validateArgs("read_session_history", a) == nil {
			t.Fatal("accepted bounds", a)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.call(ctx, "find_sessions", args{Limit: 1})
			if e != nil || len(v.(FoundSessions).Sessions) != 1 {
				t.Error(v, e)
			}
		}()
	}
	wg.Wait()
}
func TestUnavailableNativeState(t *testing.T) {
	root := t.TempDir()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_a" {
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: root})
			return
		}
		w.WriteHeader(503)
	}))
	defer up.Close()
	c, _ := opencode.New(up.URL, "", "")
	p, _ := security.New([]string{root}, []string{"ses_a"})
	s := &Server{Client: c, Policy: p}
	v, e := s.call(context.Background(), "get_session", args{SessionID: "opencode:ses_a"})
	if e != nil || v.(Session).State != "unknown" {
		t.Fatal(v, e)
	}
}

func TestStateEvidence(t *testing.T) {
	for _, tc := range []struct {
		status      string
		permissions string
		want        string
	}{
		{`{}`, `[]`, "unknown"},
		{`{"ses_a":{"type":"idle"}}`, `[]`, "idle"},
		{`{"ses_a":{"type":"busy"}}`, `[]`, "running"},
		{`{"ses_a":{"type":"retry"}}`, `[]`, "running"},
		{`{"ses_a":{"type":"future"}}`, `[]`, "unknown"},
		{`{"ses_a":{"type":"idle"}}`, `error`, "unknown"},
		{`{}`, `[{"sessionID":"ses_a"}]`, "waiting_for_permission"},
	} {
		t.Run(tc.want+tc.status, func(t *testing.T) {
			root := t.TempDir()
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/session/status":
					w.Write([]byte(tc.status))
				case "/permission":
					if tc.permissions == "error" {
						w.WriteHeader(500)
					} else {
						w.Write([]byte(tc.permissions))
					}
				default:
					json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: root})
				}
			}))
			defer up.Close()
			c, _ := opencode.New(up.URL, "", "")
			p, _ := security.New([]string{root}, []string{"ses_a"})
			s := &Server{Client: c, Policy: p}
			v, e := s.call(context.Background(), "get_session", args{SessionID: "opencode:ses_a"})
			if e != nil || v.(Session).State != tc.want {
				t.Fatal(v, e)
			}
		})
	}
}
