package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ADR-017 A2 S1: the status read keeps the retry free-text message and the
// structured action raw, and an unexpected shape never fails the decode.
func TestStatusKeepsRetryMessageAndAction(t *testing.T) {
	var s Status
	if err := json.Unmarshal([]byte(`{"type":"retry","attempt":3,"next":1700000000000,"message":"429 rate limited","action":{"reason":"account_rate_limit","provider":"p","title":"t"}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Type != "retry" || string(s.Attempt) != "3" || string(s.Message) != `"429 rate limited"` {
		t.Fatalf("status %+v", s)
	}
	var a struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(s.Action, &a); err != nil || a.Reason != "account_rate_limit" {
		t.Fatalf("action %s %v", s.Action, err)
	}
	// Hostile shapes are kept raw, not decoded into a failing typed field.
	if err := json.Unmarshal([]byte(`{"type":"retry","message":{},"action":"x"}`), &s); err != nil {
		t.Fatalf("hostile status failed the read: %v", err)
	}
	if string(s.Message) != "{}" || string(s.Action) != `"x"` {
		t.Fatalf("raw fields %s %s", s.Message, s.Action)
	}
}

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

// Fixture semantics mirror the installed 1.19.4 observation; the adapter must
// preserve each native page's sequence and forward its opaque cursor unchanged.
func TestHistoryPageOrderingAndLegacy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/session/ses_existing/message" || r.URL.Query().Get("directory") != "/approved" {
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(400)
			return
		}
		switch r.URL.Query().Get("limit") {
		case "":
			if r.URL.Query().Get("before") != "" {
				t.Error("legacy query changed")
			}
			w.Write([]byte(`[{"info":{"id":"msg_old","time":{"created":1}},"parts":[]},{"info":{"id":"msg_new","time":{"created":2}},"parts":[]}]`))
		case "2":
			if r.URL.Query().Get("before") == "" {
				w.Header().Set("X-Next-Cursor", "opaque_cursor")
				w.Write([]byte(`[{"info":{"id":"msg_later1","time":{"created":3}},"parts":[]},{"info":{"id":"msg_later2","time":{"created":4}},"parts":[]}]`))
			} else if r.URL.Query().Get("before") == "opaque_cursor" {
				w.Write([]byte(`[{"info":{"id":"msg_older1","time":{"created":1}},"parts":[]},{"info":{"id":"msg_older2","time":{"created":2}},"parts":[]}]`))
			} else {
				t.Error("cursor changed")
			}
		default:
			w.Write([]byte(`[{"info":{"id":"msg_1"}},{"info":{"id":"msg_2"}}]`))
		}
	}))
	defer up.Close()
	c, _ := New(up.URL, "", "")
	c = c.WithDirectory("/approved")
	page, next, e := c.HistoryPage(context.Background(), "ses_existing", 2, "")
	if e != nil || len(page) != 2 || page[0].Info.ID != "msg_later1" || next != "opaque_cursor" {
		t.Fatal(page, next, e)
	}
	older, end, e := c.HistoryPage(context.Background(), "ses_existing", 2, next)
	if e != nil || len(older) != 2 || end != "" || older[0].Info.Time.Created >= page[0].Info.Time.Created {
		t.Fatal(older, end, e)
	}
	legacy, e := c.History(context.Background(), "ses_existing")
	if e != nil || len(legacy) != 2 || legacy[0].Info.ID != "msg_old" {
		t.Fatal(legacy, e)
	}
	if _, _, e := c.HistoryPage(context.Background(), "ses_existing", 1, ""); e == nil {
		t.Fatal("oversized page accepted")
	}
}

// F-5: the one predicate shared by send_prompt admission and discovery.
func TestUnfinishedAssistant(t *testing.T) {
	msg := func(role string, completed int64) Message {
		m := Message{}
		m.Info.Role = role
		m.Info.Time.Created = 1
		m.Info.Time.Completed = completed
		return m
	}
	for _, tc := range []struct {
		name string
		msgs []Message
		want bool
	}{
		{"nil", nil, false},
		{"empty", []Message{}, false},
		{"completed", []Message{msg("user", 0), msg("assistant", 2)}, false},
		{"user without completion", []Message{msg("user", 0)}, false},
		{"unfinished last", []Message{msg("user", 0), msg("assistant", 0)}, true},
		{"unfinished earlier", []Message{msg("assistant", 0), msg("user", 0), msg("assistant", 3)}, true},
		{"negative completion", []Message{msg("assistant", -1)}, true},
	} {
		if got := UnfinishedAssistant(tc.msgs); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ADR-022 S3: the native time and metadata are kept raw and read tolerantly, so an
// unexpected shape never fails a session read (and with it every discovery).
func TestSessionTimeAndMetadataAreTolerant(t *testing.T) {
	var s Session
	if err := json.Unmarshal([]byte(`{"id":"ses_a","time":{"created":1767225600000,"updated":1768003200000},"metadata":{"hb_origin":"bridge"}}`), &s); err != nil {
		t.Fatal(err)
	}
	got, ok := s.UpdatedAt()
	if !ok || !got.Equal(time.UnixMilli(1768003200000)) || got.Location() != time.UTC || !s.CreatedByBridge() {
		t.Fatalf("%v %v %v", got, ok, s.CreatedByBridge())
	}
	for name, body := range map[string]string{
		"no time":        `{"id":"ses_a"}`,
		"time string":    `{"id":"ses_a","time":"yesterday","metadata":"x"}`,
		"time array":     `{"id":"ses_a","time":[1],"metadata":[1,2]}`,
		"updated string": `{"id":"ses_a","time":{"updated":"2026"}}`,
		"updated zero":   `{"id":"ses_a","time":{"updated":0}}`,
		"updated neg":    `{"id":"ses_a","time":{"updated":-5}}`,
		"updated huge":   `{"id":"ses_a","time":{"updated":1e30}}`,
		"updated null":   `{"id":"ses_a","time":{"updated":null},"metadata":null}`,
		"origin number":  `{"id":"ses_a","metadata":{"hb_origin":7}}`,
		"origin other":   `{"id":"ses_a","metadata":{"hb_origin":"Bridge"}}`,
	} {
		var v Session
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Errorf("%s: a session with an unexpected shape failed to decode: %v", name, err)
			continue
		}
		if _, ok := v.UpdatedAt(); ok {
			t.Errorf("%s: an update time was invented", name)
		}
		if v.CreatedByBridge() {
			t.Errorf("%s: a provenance label was invented", name)
		}
	}
}

// ADR-024 4.2 step 8 / D15 Q4: the production reply is one POST /permission/:requestID/reply with
// {"reply":"reject"} and nothing else, routed to the session's directory.
func TestReplyPermissionSendsOnlyRejectOnTheCurrentRoute(t *testing.T) {
	type seen struct {
		method, path, directory, body, auth string
	}
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u, p, _ := r.BasicAuth()
		got = append(got, seen{r.Method, r.URL.Path, r.URL.Query().Get("directory"), string(b), u + ":" + p})
		w.Write([]byte("true"))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "user", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ReplyPermission(context.Background(), "ses_abc", "/srv/work", "per_0001", "reject"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (seen{"POST", "/permission/per_0001/reply", "/srv/work", `{"reply":"reject"}`, "user:pw"}) {
		t.Fatalf("native requests = %+v", got)
	}
	// Nothing but reject, and only for well-formed targets: no request is made.
	for name, args := range map[string][4]string{
		"once":           {"ses_abc", "/srv/work", "per_0001", "once"},
		"always":         {"ses_abc", "/srv/work", "per_0001", "always"},
		"empty reply":    {"ses_abc", "/srv/work", "per_0001", ""},
		"bad session":    {"abc", "/srv/work", "per_0001", "reject"},
		"bad request id": {"ses_abc", "/srv/work", "../per", "reject"},
		"no per prefix":  {"ses_abc", "/srv/work", "x_0001", "reject"},
		"relative dir":   {"ses_abc", "work", "per_0001", "reject"},
		"empty dir":      {"ses_abc", "", "per_0001", "reject"},
		"path in id":     {"ses_abc", "/srv/work", "per_1/reply?x=1", "reject"},
	} {
		if err := c.ReplyPermission(context.Background(), args[0], args[1], args[2], args[3]); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if len(got) != 1 {
		t.Fatalf("a refused call reached the native server: %+v", got[1:])
	}
}

func TestReplyPermissionReturnsTheHTTPStatus(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		c, _ := New(srv.URL, "", "")
		err := c.ReplyPermission(context.Background(), "ses_abc", "/srv/work", "per_0001", "reject")
		var h *HTTPError
		if !errors.As(err, &h) || h.Status != status {
			t.Errorf("status %d: err = %v", status, err)
		}
		srv.Close()
	}
}
