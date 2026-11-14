package opencode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ADR-022 slice S4: the native create and the bounded list, against a fake native.
func TestCreateSessionSendsOnlyTheFixedBodyToTheResolvedDirectory(t *testing.T) {
	var method, path, dir, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, path, dir, body = r.Method, r.URL.Path, r.URL.Query().Get("directory"), string(b)
		_ = json.NewEncoder(w).Encode(Session{ID: "ses_new", Title: "t", Directory: dir})
	}))
	defer srv.Close()
	c, err := New(srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.CreateSession(context.Background(), "/srv/proj", "t", map[string]string{"hb_origin": "bridge", "hb_key": "0123456789abcdef"})
	if err != nil || s.ID != "ses_new" || s.Directory != "/srv/proj" {
		t.Fatalf("%+v %v", s, err)
	}
	if method != "POST" || path != "/session" || dir != "/srv/proj" {
		t.Fatalf("%s %s ?directory=%s", method, path, dir)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &m) != nil || len(m) != 2 || m["title"] == nil || m["metadata"] == nil {
		t.Fatalf("body %s", body)
	}
	for _, banned := range []string{"permission", "agent", "model", "parentID", "workspaceID", "prompt"} {
		if strings.Contains(body, banned) {
			t.Errorf("the body carries %q", banned)
		}
	}
	// An empty title is omitted.
	if _, err := c.CreateSession(context.Background(), "/srv/proj", "", map[string]string{"hb_origin": "bridge"}); err != nil || strings.Contains(body, `"title"`) {
		t.Fatalf("%v %s", err, body)
	}
}

func TestCreateSessionRefusesBadInputBeforeAnyRequest(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	c, _ := New(srv.URL, "", "")
	for name, tc := range map[string]struct{ dir, title string }{
		"relative":       {"srv/proj", "t"},
		"unclean":        {"/srv/../proj", "t"},
		"trailing slash": {"/srv/proj/", "t"},
		"empty":          {"", "t"},
		"long title":     {"/srv/proj", strings.Repeat("x", 201)},
		"control":        {"/srv/proj", "a\nb"},
	} {
		if _, err := c.CreateSession(context.Background(), tc.dir, tc.title, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := c.CreateSession(context.Background(), "/srv/proj", "t", map[string]string{"1": "", "2": "", "3": "", "4": "", "5": "", "6": "", "7": "", "8": "", "9": ""}); err == nil {
		t.Error("too many metadata keys accepted")
	}
	if hit {
		t.Fatal("a request was made for a refused input")
	}
}

func TestCreateSessionErrorKinds(t *testing.T) {
	for _, status := range []int{400, 403, 404, 500, 503} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		c, _ := New(srv.URL, "", "")
		_, err := c.CreateSession(context.Background(), "/srv/proj", "t", nil)
		if !IsDefiniteRefusal(err) {
			t.Errorf("%d: %v is not a definite refusal", status, err)
		}
		srv.Close()
	}
	// Not definite: a timeout, a closed connection, an unreadable answer.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) }))
	c, _ := New(slow.URL, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err := c.CreateSession(ctx, "/srv/proj", "t", nil)
	cancel()
	if err == nil || IsDefiniteRefusal(err) {
		t.Errorf("a timeout: %v", err)
	}
	slow.CloseClientConnections()
	slow.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{{")) }))
	defer bad.Close()
	c, _ = New(bad.URL, "", "")
	if _, err := c.CreateSession(context.Background(), "/srv/proj", "t", nil); err == nil || IsDefiniteRefusal(err) {
		t.Errorf("an unreadable 200: %v", err)
	}
}

func TestListDirectoryAsksForABoundedListOfOneDirectory(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path + " " + r.URL.Query().Get("directory") + " " + r.URL.Query().Get("limit")
		_, _ = w.Write([]byte(`[{"id":"ses_a","directory":"/srv/proj"}]`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "", "")
	l, err := c.ListDirectory(context.Background(), "/srv/proj", 1001)
	if err != nil || len(l) != 1 || got != "GET /session /srv/proj 1001" {
		t.Fatalf("%v %v %q", l, err, got)
	}
	for _, bad := range []struct {
		dir   string
		limit int
	}{{"rel", 10}, {"/a/../b", 10}, {"/a", 0}, {"/a", 100001}} {
		if _, err := c.ListDirectory(context.Background(), bad.dir, bad.limit); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
