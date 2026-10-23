package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLiveSchemaTransport(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "opencode" || p != "secret" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("directory") != "/workspace with spaces" {
			t.Error("directory lost")
		}
		calls[r.Method+" "+r.URL.Path]++
		switch r.URL.Path {
		case "/session":
			w.Write([]byte(`[{"id":"ses_test","directory":"/workspace with spaces"}]`))
		case "/session/ses_test":
			w.Write([]byte(`{"id":"ses_test","directory":"/workspace with spaces"}`))
		case "/session/ses_test/message":
			w.Write([]byte(`[{"info":{"id":"msg_reply","role":"assistant","sessionID":"ses_test","parentID":"msg_prompt","finish":"stop","time":{"completed":10}},"parts":[]}]`))
		case "/session/status":
			w.Write([]byte(`{}`))
		case "/permission":
			w.Write([]byte(`[{"sessionID":"ses_test"}]`))
		case "/session/ses_test/prompt_async":
			var p struct {
				MessageID string                  `json:"messageID"`
				Parts     []struct{ Text string } `json:"parts"`
			}
			if e := json.NewDecoder(r.Body).Decode(&p); e != nil || p.MessageID != "msg_prompt" || p.Parts[0].Text != "hello" {
				t.Error("bad prompt")
			}
			w.WriteHeader(204)
		case "/session/ses_test/abort":
			w.Write([]byte(`true`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c, e := New(server.URL, "", "secret")
	if e != nil {
		t.Fatal(e)
	}
	c = c.WithDirectory("/workspace with spaces")
	ctx := context.Background()
	list, e := c.List(ctx)
	if e != nil || len(list) != 1 {
		t.Fatal(list, e)
	}
	s, e := c.Get(ctx, "ses_test")
	if e != nil || s.Directory != "/workspace with spaces" {
		t.Fatal(s, e)
	}
	msgs, e := c.History(ctx, "ses_test")
	if e != nil || msgs[0].Info.ParentID != "msg_prompt" || msgs[0].Info.Time.Completed != 10 {
		t.Fatal(msgs, e)
	}
	if _, e = c.Status(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Permissions(ctx); e != nil {
		t.Fatal(e)
	}
	if e = c.Prompt(ctx, "ses_test", "hello", "msg_prompt"); e != nil {
		t.Fatal(e)
	}
	if e = c.Abort(ctx, "ses_test"); e != nil {
		t.Fatal(e)
	}
	if calls["POST /session/ses_test/prompt_async"] != 1 {
		t.Fatal(calls)
	}
}
func TestErrorsAndNoRedirectOrLeak(t *testing.T) {
	for _, code := range []int{401, 403, 404, 500, 302} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://example.com")
				w.WriteHeader(code)
				w.Write([]byte("private upstream secret"))
			}))
			defer server.Close()
			c, _ := New(server.URL, "", "")
			_, e := c.List(context.Background())
			if e == nil || strings.Contains(e.Error(), "private") {
				t.Fatal(e)
			}
		})
	}
	for _, body := range []string{"bad-json", strings.Repeat("x", (8<<20)+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		c, _ := New(server.URL, "", "")
		if _, e := c.List(context.Background()); e == nil {
			t.Fatal("invalid response accepted")
		}
		server.Close()
	}
}
func TestLocalURLAndIDs(t *testing.T) {
	for _, u := range []string{"https://example.com", "http://127.0.0.1/path", "http://a:b@localhost", "http://localhost?directory=x", "file:///tmp/x"} {
		if _, e := New(u, "", ""); e == nil {
			t.Fatal(u)
		}
	}
	c, _ := New("http://127.0.0.1:1", "", "")
	if e := c.Prompt(context.Background(), "ses_test", "x", "random"); e == nil {
		t.Fatal("bad message ID")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := c.List(ctx); e == nil {
		t.Fatal("canceled request")
	}
}

func TestTimeoutDisconnectAndAbortRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/ses_test/abort" {
			w.Write([]byte(`false`))
			return
		}
		<-r.Context().Done()
	}))
	c, _ := New(server.URL, "", "")
	c.http.Timeout = 10 * time.Millisecond
	if _, e := c.Status(context.Background()); e == nil {
		t.Fatal("timeout accepted")
	}
	if e := c.Abort(context.Background(), "ses_test"); e == nil {
		t.Fatal("abort rejection accepted")
	}
	server.Close()
	if _, e := c.Status(context.Background()); e == nil {
		t.Fatal("disconnect accepted")
	}
}
