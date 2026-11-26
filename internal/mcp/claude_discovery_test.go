package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
	"github.com/nevzatcirak/cirak/internal/mcpclient"
	"github.com/nevzatcirak/cirak/internal/security"
)

// #267 P2-1 (task #268): a claudecode: ACL entry must never reach the OpenCode client. The wiring
// requires every Claude ID in CIRAK_ALLOWED_SESSIONS, so the discovery loop sees it; without a
// skip it was sent to Client.Get, refused locally ("invalid native session ID", neither FORBIDDEN
// nor NOT_FOUND) and the whole listing failed, with the adapter on or off.

type discoveryEnv struct {
	t      *testing.T
	root   string
	s      *Server
	client *sdk.ClientSession
}

func newDiscoveryEnv(t *testing.T, withClaudeInACL bool) *discoveryEnv {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "claudecode") {
			t.Errorf("a claudecode: ID reached the native server: %s", r.URL)
		}
		switch {
		case r.URL.Path == "/session/status":
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/permission":
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/message"):
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/session/ses_oc":
			_ = json.NewEncoder(w).Encode(opencode.Session{ID: "ses_oc", Title: "opencode session", Directory: root})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(upstream.Close)
	c, err := opencode.New(upstream.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	acl := []string{"ses_oc"}
	if withClaudeInACL {
		acl = append(acl, claudeGlobal)
	}
	p, err := security.New([]string{root}, acl)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Client: c, Policy: p, Token: strings.Repeat("t", 32)}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	cl, err := mcpclient.Connect(ctx, srv.URL, s.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return &discoveryEnv{t: t, root: root, s: s, client: cl}
}

func sessionIDs(t *testing.T, r *sdk.CallToolResult) []string {
	t.Helper()
	var ids []string
	for _, s := range structured(t, r)["sessions"].([]any) {
		ids = append(ids, s.(map[string]any)["session_id"].(string))
	}
	return ids
}

func TestDiscoveryListsClaudeAndOpenCodeSessionsWhenTheAdapterIsOn(t *testing.T) {
	e := newDiscoveryEnv(t, true)
	enableClaude(t, e.s)
	for _, tool := range []string{"list_sessions", "find_sessions"} {
		ids := sessionIDs(t, invoke(t, e.client, tool, map[string]any{}, false))
		if strings.Join(ids, ",") != claudeGlobal+",opencode:ses_oc" {
			t.Errorf("%s = %v, want the Claude and the OpenCode session, sorted by ID", tool, ids)
		}
	}
	if _, err := e.s.call(context.Background(), "list_repositories", args{}); err != nil {
		t.Errorf("list_repositories: %v", err)
	}
	// a filter that no Claude session can match returns the OpenCode side only
	ids := sessionIDs(t, invoke(t, e.client, "find_sessions", map[string]any{"repository_id": "nope"}, false))
	if len(ids) != 0 {
		t.Errorf("filtered find_sessions = %v", ids)
	}
}

// With the adapter off the OpenCode listing is exactly what it was: the Claude ID in the ACL is
// not an error and not a row.
func TestDiscoveryIgnoresAClaudeAclEntryWhenTheAdapterIsOff(t *testing.T) {
	e := newDiscoveryEnv(t, true)
	for _, tool := range []string{"list_sessions", "find_sessions"} {
		ids := sessionIDs(t, invoke(t, e.client, tool, map[string]any{}, false))
		if strings.Join(ids, ",") != "opencode:ses_oc" {
			t.Errorf("%s = %v", tool, ids)
		}
	}
	if _, err := e.s.call(context.Background(), "list_repositories", args{}); err != nil {
		t.Errorf("list_repositories: %v", err)
	}
}

// list_catalog stops failing; the Claude session stays omitted (BACKLOG: deviation from ADR-022 6.2).
func TestCatalogIgnoresAClaudeAclEntry(t *testing.T) {
	e := newCatalogEnv(t, nil)
	e.s.Policy.Sessions[claudeGlobal] = true
	cat := e.mustCall(args{})
	for _, s := range cat.Sessions {
		if b, _ := json.Marshal(s); strings.Contains(string(b), "claudecode") {
			t.Errorf("a Claude session is in the catalog: %s", b)
		}
	}
	if len(cat.Sessions) == 0 {
		t.Fatal("the catalog is empty")
	}
	e.s.Claude = newFakeClaude()
	if got := e.mustCall(args{}); len(got.Sessions) != len(cat.Sessions) {
		t.Errorf("enabling the adapter changed the catalog: %d -> %d", len(cat.Sessions), len(got.Sessions))
	}
}
