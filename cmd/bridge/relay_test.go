package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/audit"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

func TestPermissionRelayModeSwitch(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "BRIDGE_PERMISSION_RELAY" {
				return v
			}
			return ""
		}
	}
	for in, want := range map[string]string{"": "reject_only", "reject_only": "reject_only", " reject_only ": "reject_only", "off": "off"} {
		if got, err := permissionRelayMode(env(in)); err != nil || got != want {
			t.Errorf("%q -> %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"policy", "on", "REJECT_ONLY", "allow_once", "true"} {
		if _, err := permissionRelayMode(env(in)); err == nil {
			t.Errorf("%q accepted: policy mode is not started and unknown values must not fall back", in)
		}
	}
	// policy is refused with a message that says why
	if _, err := permissionRelayMode(env("policy")); err == nil || !strings.Contains(err.Error(), "not started") {
		t.Errorf("policy error = %v", err)
	}
}

// relayRunner is the manager's native view: one session whose guard is held waiting for permission.
type relayRunner struct {
	mu        sync.Mutex
	directory string
	prompted  bool
	messages  []opencode.Message
}

func (r *relayRunner) Get(_ context.Context, id string) (opencode.Session, error) {
	return opencode.Session{ID: id, Directory: r.directory}, nil
}
func (r *relayRunner) Status(context.Context) (map[string]opencode.Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.prompted {
		return map[string]opencode.Status{}, nil
	}
	return map[string]opencode.Status{"ses_a": {Type: "busy"}}, nil
}
func (r *relayRunner) Permissions(context.Context) ([]opencode.Permission, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.prompted {
		return []opencode.Permission{{SessionID: "ses_a"}}, nil
	}
	return nil, nil
}
func (r *relayRunner) History(context.Context, string) ([]opencode.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]opencode.Message(nil), r.messages...), nil
}
func (r *relayRunner) Prompt(_ context.Context, session, _, mid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := opencode.Message{}
	m.Info.ID, m.Info.Role, m.Info.SessionID = mid, "user", session
	r.messages, r.prompted = append(r.messages, m), true
	return nil
}
func (r *relayRunner) Abort(context.Context, string) error { return nil }

// relaySource is the verified view of the one pending request (a fake: no production source exists).
type relaySource struct{ execID string }

func (s relaySource) PermissionView(_ context.Context, session string) (opencode.PermissionObservationBatch, error) {
	return opencode.PermissionObservationBatch{Status: "available", Completeness: "point_in_time_only", Permissions: []opencode.ObservedPermission{{
		RequestID: "per_0001", SessionID: session, ToolMessageID: "msg_assistant1", ToolCallID: "call_1", Kind: "edit", Tool: "edit",
		ScopeDigest: strings.Repeat("ab", 32), PatternCount: 2, State: "pending", Attribution: "verified", ExecutionID: s.execID, UserMessageID: "msg_user1",
	}}}, nil
}

type nativeRequest struct{ method, path, directory, body string }

type relayStack struct {
	t      *testing.T
	srv    *mcp.Server
	runs   *execution.Manager
	execID string
	mu     sync.Mutex
	reqs   []nativeRequest
	client *sdk.ClientSession
	// serve, when set, answers a native request before the default handler (guarded by mu).
	serve func(w http.ResponseWriter, r *http.Request) bool
	url   string
}

// useSource replaces the production source by a fake and lets a background read that was already in
// flight finish, so a later count of native calls sees only what the call under test made. (The
// production source reads natively on the manager's own reconcile cadence while a request waits.)
func (s *relayStack) useSource(src execution.PermissionSource) {
	s.runs.SetPermissionSource(src)
	time.Sleep(60 * time.Millisecond)
}

func (s *relayStack) nativeURL() string { return s.url }

func (s *relayStack) native() []nativeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]nativeRequest(nil), s.reqs...)
}
func (s *relayStack) posts() []nativeRequest {
	var out []nativeRequest
	for _, r := range s.native() {
		if r.method == "POST" {
			out = append(out, r)
		}
	}
	return out
}

// newRelayStack wires the production pieces (the real OpenCode client as replier, audit.Default() as
// the writer, wirePermissionRelay) against a recording native server and a waiting execution.
func newRelayStack(t *testing.T, mode string, readOnly bool) *relayStack {
	t.Helper()
	root := t.TempDir()
	st := &relayStack{t: t}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		st.mu.Lock()
		st.reqs = append(st.reqs, nativeRequest{r.Method, r.URL.Path, r.URL.Query().Get("directory"), string(b)})
		serve := st.serve
		st.mu.Unlock()
		if serve != nil && serve(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/permission/per_0001/reply":
			w.Write([]byte("true"))
		case r.URL.Path == "/permission":
			// One pending request of the session WITHOUT a tool object: it can never be attributed.
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"per_0001","sessionID":"ses_a","permission":"bash","patterns":["echo X"],"metadata":{},"always":[]}]`))
		default:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: root})
		}
	}))
	t.Cleanup(ts.Close)
	st.url = ts.URL
	client, err := opencode.New(ts.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	runner := &relayRunner{directory: root}
	runs := execution.NewWithTiming(runner, 30*time.Second, 2*time.Millisecond)
	t.Cleanup(runs.Close)
	pol, err := security.New([]string{root}, []string{"ses_a"})
	if err != nil {
		t.Fatal(err)
	}
	st.runs = runs
	st.srv = &mcp.Server{Client: client, Runs: runs, Token: strings.Repeat("t", 32), Policy: pol, ReadOnly: readOnly}
	if err := wirePermissionRelay(st.srv, runs, client, mode); err != nil {
		t.Fatal(err)
	}
	x, e := runs.Send(context.Background(), "ses_a", "prompt", "relay-key-0001")
	if e != nil {
		t.Fatal(e)
	}
	st.execID = x.ID
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(time.Millisecond) {
		if v, e := runs.Snapshot(context.Background(), x.ID); e == nil && v.State == "waiting_for_permission" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never waiting_for_permission")
		}
	}
	front := httptest.NewServer(st.srv)
	t.Cleanup(front.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st.client, err = mcpclient.Connect(ctx, front.URL, st.srv.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.client.Close() })
	return st
}

func (s *relayStack) call(args map[string]any) (*sdk.CallToolResult, map[string]any) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := s.client.CallTool(ctx, &sdk.CallToolParams{Name: "reply_permission", Arguments: args})
	if err != nil {
		s.t.Fatal(err)
	}
	raw, _ := json.Marshal(r.StructuredContent)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return r, m
}
func (s *relayStack) args(decision string) map[string]any {
	return map[string]any{"execution_id": s.execID, "permission_id": "per_0001", "decision": decision}
}

// journal routes the process-wide audit writer to a buffer and clears its latch for this test.
func journal(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := audit.Default().SetJournalForTesting(slog.NewTextHandler(&lockedWriter{w: &buf}, nil))
	t.Cleanup(restore)
	return &buf
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

type failingJournal struct{}

func (failingJournal) Enabled(context.Context, slog.Level) bool  { return true }
func (failingJournal) Handle(context.Context, slog.Record) error { return errors.New("journal gone") }
func (h failingJournal) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h failingJournal) WithGroup(string) slog.Handler           { return h }

// D15 Q3: reject_only registers reply_permission by default; off keeps the eleven tools.
func TestRegistrationDefaultAndOff(t *testing.T) {
	journal(t)
	for mode, want := range map[string]int{"reject_only": 12, "off": 11} {
		st := newRelayStack(t, mode, false)
		tools, err := st.client.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		has := false
		for _, tool := range tools.Tools {
			has = has || tool.Name == "reply_permission"
		}
		if len(tools.Tools) != want || has != (mode == "reject_only") {
			t.Errorf("mode %s: %d tools, reply_permission listed %v; want %d", mode, len(tools.Tools), has, want)
		}
	}
}

// ADR-024 S6: wirePermissionRelay installs the PRODUCTION PermissionSource. With no native
// incarnation evidence (none exists) it observes the pending request but can never verify it, so the
// tool refuses with FORBIDDEN not_verified: nothing is written natively and no attempt line is
// written. (Before S6 the same call failed with UPSTREAM_FAILURE because no source existed.)
func TestProductionWiringInstallsTheSourceAndStaysUnattributed(t *testing.T) {
	buf := journal(t)
	st := newRelayStack(t, "reject_only", false)
	r, m := st.call(st.args("reject"))
	if !r.IsError || m["code"] != "FORBIDDEN" || m["reason"] != "not_verified" {
		t.Fatalf("%v", m)
	}
	var read bool
	for _, n := range st.native() {
		read = read || n.method == "GET" && n.path == "/permission"
	}
	if !read {
		t.Fatalf("the production source made no native permission read: %v", st.native())
	}
	if len(st.posts()) != 0 {
		t.Fatalf("native writes: %v", st.posts())
	}
	if strings.Contains(buf.String(), "phase=attempt") || !strings.Contains(buf.String(), "phase=refused") {
		t.Fatalf("journal:\n%s", buf.String())
	}
}

// Mode off installs nothing: no source, no replier, no tool.
func TestWiringOffInstallsNothing(t *testing.T) {
	journal(t)
	st := newRelayStack(t, "off", false)
	before := len(st.native())
	time.Sleep(30 * time.Millisecond)
	for _, n := range st.native()[before:] {
		if n.method == "GET" && n.path == "/permission" {
			t.Fatalf("mode off made an observer read: %v", n)
		}
	}
	if st.srv.PermissionRelay != nil {
		t.Fatal("mode off registered the relay")
	}
}

// A wiring error is returned, not swallowed.
func TestWirePermissionRelayReturnsTheSourceError(t *testing.T) {
	if err := wirePermissionRelay(&mcp.Server{}, nil, nil, "reject_only"); err == nil {
		t.Fatal("a missing client and manager were wired")
	}
	if err := wirePermissionRelay(&mcp.Server{}, nil, nil, "off"); err != nil {
		t.Fatal(err)
	}
}

func TestProductionWiringReplyIsOneRejectPostWithAuditLines(t *testing.T) {
	buf := journal(t)
	st := newRelayStack(t, "reject_only", false)
	st.useSource(relaySource{st.execID})
	r, m := st.call(st.args("reject"))
	if r.IsError || m["outcome"] != "replied" || m["decided_by"] != "chat_client" || m["human_verified"] != false {
		t.Fatalf("%v", m)
	}
	posts := st.posts()
	if len(posts) != 1 || posts[0].path != "/permission/per_0001/reply" || posts[0].body != `{"reply":"reject"}` || posts[0].directory == "" {
		t.Fatalf("native writes = %+v", posts)
	}
	var phases []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if !strings.Contains(l, "event=permission_reply") {
			continue
		}
		if !strings.Contains(l, "decided_by=chat_client") || !strings.Contains(l, "human_verified=false") {
			t.Fatalf("line lacks decided_by/human_verified: %s", l)
		}
		for _, p := range []string{"attempt", "result", "refused"} {
			if strings.Contains(l, "phase="+p) {
				phases = append(phases, p)
			}
		}
	}
	if strings.Join(phases, ",") != "attempt,result" {
		t.Fatalf("phases = %v\n%s", phases, buf.String())
	}
}

func TestProductionWiringRefusalsMakeNoNativeCall(t *testing.T) {
	journal(t)
	st := newRelayStack(t, "reject_only", false)
	st.useSource(relaySource{st.execID})
	before := len(st.native())
	r, m := st.call(st.args("allow_once"))
	if !r.IsError || m["code"] != "FORBIDDEN" || m["reason"] != "allow_once_disabled" {
		t.Fatalf("%v", m)
	}
	if len(st.native()) != before {
		t.Fatalf("allow_once made native calls: %v", st.native()[before:])
	}
	ro := newRelayStack(t, "reject_only", true)
	ro.useSource(relaySource{ro.execID})
	before = len(ro.native())
	r, m = ro.call(ro.args("reject"))
	if !r.IsError || m["code"] != "FORBIDDEN" || len(ro.native()) != before {
		t.Fatalf("read-only: %v, native calls %d -> %d", m, before, len(ro.native()))
	}
}

// Attempt-line failure on the production writer: zero native writes, audit_unavailable, and the
// next call is refused before any native read.
func TestProductionWiringAttemptLineFailureMeansZeroNativeWrites(t *testing.T) {
	journal(t)
	restore := audit.Default().SetJournalForTesting(failingJournal{})
	defer restore()
	st := newRelayStack(t, "reject_only", false)
	st.useSource(relaySource{st.execID})
	r, m := st.call(st.args("reject"))
	if !r.IsError || m["code"] != "UPSTREAM_FAILURE" || m["reason"] != "audit_unavailable" || m["retryable"] != true {
		t.Fatalf("%v", m)
	}
	if len(st.posts()) != 0 {
		t.Fatalf("native writes after a failed attempt line: %v", st.posts())
	}
	before := len(st.native())
	r, m = st.call(st.args("reject"))
	if !r.IsError || m["reason"] != "audit_unavailable" || len(st.native()) != before {
		t.Fatalf("a later call was served: %v", m)
	}
}

type epochSource struct{}

func (epochSource) NativeEpoch(context.Context) (string, error) { return "test-epoch", nil }

// attributableNative makes the native server describe ONE pending request that the observer can
// attribute to the stack's execution: a tool object, an assistant message whose parent is the
// execution's own user message, and a workspace that matches.
func (s *relayStack) attributableNative(root, userMessageID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serve = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/permission":
			w.Write([]byte(`[{"id":"per_0001","sessionID":"ses_a","permission":"bash","patterns":["echo X"],"metadata":{},"always":[],"tool":{"messageID":"msg_assistant1","callID":"call_1"}}]`))
		case "/session/ses_a/message/msg_assistant1":
			json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"id": "msg_assistant1", "sessionID": "ses_a", "role": "assistant", "parentID": userMessageID,
				"time": map[string]int{"created": 1}, "modelID": "f", "providerID": "f", "mode": "build", "agent": "build", "path": map[string]string{"cwd": root, "root": root}, "cost": 0,
				"tokens": map[string]any{"input": 1, "output": 1, "reasoning": 0, "cache": map[string]int{"read": 0, "write": 0}}},
				"parts": []any{map[string]any{"id": "prt_tool", "sessionID": "ses_a", "messageID": "msg_assistant1", "type": "tool", "callID": "call_1", "tool": "bash", "state": map[string]any{"status": "running", "input": map[string]string{"command": "X"}, "time": map[string]int{"start": 1}}}}})
		case "/session/ses_a/message/" + userMessageID:
			json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"id": userMessageID, "sessionID": "ses_a", "role": "user", "time": map[string]int{"created": 0}, "agent": "build", "model": map[string]string{"providerID": "f", "modelID": "f"}}, "parts": []any{}})
		default:
			return false
		}
		return true
	}
}

// Review #310 P3-3: the production default "no native incarnation evidence" is pinned at the
// wiring layer. The request here is tool-bearing and attributable (the control below proves it: the
// SAME native state verifies once a source is given an epoch), so the only reason the wired source
// refuses it is the absent epoch. A NativeIncarnation fabricated in wirePermissionRelay makes the
// call succeed (a native write) and turns this test RED.
func TestProductionWiringHasNoIncarnationEvidence(t *testing.T) {
	buf := journal(t)
	st := newRelayStack(t, "reject_only", false)
	x, err := st.runs.Snapshot(context.Background(), st.execID)
	if err != nil {
		t.Fatal(err)
	}
	root := ""
	for _, n := range st.native() {
		if n.directory != "" {
			root = n.directory
			break
		}
	}
	if root == "" {
		t.Fatal("no native request carried the session directory")
	}
	st.attributableNative(root, x.MessageID)

	// Control: a source WITH an epoch verifies this very request, so the fixture is attributable.
	cli, err := opencode.New(st.nativeURL(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	withEpoch, err := execution.NewOpenCodePermissionSource(cli, st.runs, execution.PermissionSourceConfig{Incarnation: epochSource{}})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := withEpoch.PermissionView(context.Background(), "ses_a")
	if err != nil || len(batch.Permissions) != 1 || batch.Permissions[0].Attribution != "verified" {
		t.Fatalf("control: the fixture is not attributable: %+v %v", batch, err)
	}

	// The wired production source has none: not verified, no native write, no attempt line.
	r, m := st.call(st.args("reject"))
	if !r.IsError || m["code"] != "FORBIDDEN" || m["reason"] != "not_verified" {
		t.Fatalf("wired source verified a request: %v", m)
	}
	if len(st.posts()) != 0 {
		t.Fatalf("native writes: %v", st.posts())
	}
	if strings.Contains(buf.String(), "phase=attempt") {
		t.Fatalf("journal:\n%s", buf.String())
	}
}
