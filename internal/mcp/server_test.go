package mcp

import (
	"context"
	"encoding/json"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPAuthentication(t *testing.T) {
	s := &Server{Token: strings.Repeat("t", 32)}
	for _, auth := range []string{"", s.Token, "Bearer wrong"} {
		r := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}

}
func TestSessionACLHistoryAndDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	historyCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			w.Write([]byte(`{}`))
		case "/permission":
			w.Write([]byte(`[]`))
		case "/session/ses_allowed":
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_allowed", Directory: root})
		case "/session/ses_outside":
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_outside", Directory: outside})
		case "/session/ses_allowed/message":
			historyCalls++
			if r.URL.Query().Get("directory") != root {
				t.Error("lost directory")
			}
			w.Write([]byte(`[]`))
		default:
			t.Error("unauthorized upstream path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	c, _ := opencode.New(upstream.URL, "", "")
	p, _ := security.New([]string{root}, []string{"ses_allowed", "ses_outside"})
	m := execution.New(c)
	defer m.Close()
	s := &Server{Client: c, Runs: m, Policy: p}
	ctx := context.Background()
	v, e := s.call(ctx, "list_sessions", args{})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), "opencode:ses_allowed") || strings.Contains(string(b), "ses_outside") {
		t.Fatal(string(b))
	}
	if _, e = s.call(ctx, "read_session_history", args{SessionID: "opencode:ses_allowed"}); e != nil || historyCalls != 1 {
		t.Fatal(e, historyCalls)
	}
	for _, id := range []string{"ses_allowed", "opencode:ses_outside", "opencode:ses_denied", "opencode:ses_../escape"} {
		if _, e = s.call(ctx, "read_session_history", args{SessionID: id}); e == nil {
			t.Fatal("authorized", id)
		}
	}
}
