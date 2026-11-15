package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/audit"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

const relayDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type relaySource struct {
	mu     sync.Mutex
	execID string
	reads  int
	gone   bool
}

// markKey marks a context made by the test for the call under test: the manager's own S1 monitor
// keeps reading the source while the execution waits, so only marked reads count.
type markKey struct{}

func marked() context.Context { return context.WithValue(context.Background(), markKey{}, true) }

func (r *relaySource) PermissionView(ctx context.Context, session string) (opencode.PermissionObservationBatch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Value(markKey{}) != nil {
		r.reads++
	}
	if r.gone {
		return opencode.PermissionObservationBatch{Status: "available", Completeness: "point_in_time_only"}, nil
	}
	return opencode.PermissionObservationBatch{Status: "available", Completeness: "point_in_time_only", Permissions: []opencode.ObservedPermission{{
		RequestID: "per_0001", SessionID: session, ToolMessageID: "msg_assistant1", ToolCallID: "call_1", Kind: "edit", Tool: "edit", ScopeDigest: relayDigest,
		PatternCount: 2, State: "pending", Attribution: "verified", ExecutionID: r.execID, UserMessageID: "msg_user1", Reason: "exact_native_parent_and_tool",
	}}}, nil
}
func (r *relaySource) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.reads }
func (r *relaySource) set(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f()
}

type relayReplier struct {
	mu    sync.Mutex
	calls [][3]string
	dirs  []string
	err   error
}

func (r *relayReplier) ReplyPermission(_ context.Context, session, directory, id, reply string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, [3]string{session, id, reply})
	r.dirs = append(r.dirs, directory)
	return r.err
}
func (r *relayReplier) n() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.calls) }

type relayAttempts struct {
	mu      sync.Mutex
	lines   []audit.Fields
	phases  []audit.Fields
	phaseOf []string
	err     error
	latched bool
}

func (a *relayAttempts) Unavailable() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.latched }

// WritePhase validates with the real validator and records the result and refused lines.
func (a *relayAttempts) WritePhase(_ context.Context, phase string, f audit.Fields) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		a.latched = true
		return a.err
	}
	if _, err := audit.CheckForTesting(phase, f); err != nil {
		return err
	}
	c := audit.Fields{}
	for k, v := range f {
		c[k] = v
	}
	a.phases, a.phaseOf = append(a.phases, c), append(a.phaseOf, phase)
	return nil
}
func (a *relayAttempts) linesOf(phase string) []audit.Fields {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []audit.Fields
	for i, p := range a.phaseOf {
		if p == phase {
			out = append(out, a.phases[i])
		}
	}
	return out
}

func (a *relayAttempts) Write(_ context.Context, f audit.Fields) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		a.latched = true
		return a.err
	}
	if _, err := audit.CheckForTesting(audit.PhaseAttempt, f); err != nil {
		return err
	}
	c := audit.Fields{}
	for k, v := range f {
		c[k] = v
	}
	a.lines = append(a.lines, c)
	return nil
}
func (a *relayAttempts) n() int { a.mu.Lock(); defer a.mu.Unlock(); return len(a.lines) }

type relayEnv struct {
	t      *testing.T
	s      *Server
	m      *execution.Manager
	execID string
	src    *relaySource
	rep    *relayReplier
	aw     *relayAttempts
	hits   *atomic.Int64
}

// newRelayEnv holds the guard of ses_a in waiting_for_permission and serves the ACL read from a
// counting upstream, so a test can prove a refusal made no native call at all.
func newRelayEnv(t *testing.T, mode string) *relayEnv {
	t.Helper()
	root := t.TempDir()
	g := &guardRunner{directory: root, mode: "waiting_for_permission", entered: make(chan struct{}), release: make(chan struct{})}
	m := execution.NewWithTiming(g, 30*time.Second, 2*time.Millisecond)
	t.Cleanup(func() { close(g.release); m.Close() })
	x, e := m.Send(context.Background(), "ses_a", "prompt", "relay-key-0001")
	if e != nil {
		t.Fatal(e)
	}
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(time.Millisecond) {
		if v, e := m.Snapshot(context.Background(), x.ID); e == nil && v.State == "waiting_for_permission" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never waiting_for_permission")
		}
	}
	hits := &atomic.Int64{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/session/status":
			w.Write([]byte(`{}`))
		case "/permission":
			w.Write([]byte(`[]`))
		case "/session/ses_a/message":
			w.Write([]byte(`[]`))
		default:
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: root})
		}
	}))
	t.Cleanup(up.Close)
	c, err := opencode.New(up.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := security.New([]string{root}, []string{"ses_a"})
	if err != nil {
		t.Fatal(err)
	}
	e2 := &relayEnv{t: t, m: m, execID: x.ID, src: &relaySource{execID: x.ID}, rep: &relayReplier{}, aw: &relayAttempts{}, hits: hits}
	m.SetPermissionSource(e2.src)
	m.SetPermissionReplier(e2.rep)
	m.SetAttemptWriter(e2.aw)
	e2.s = &Server{Client: c, Runs: m, Policy: pol, Token: strings.Repeat("t", 32)}
	if mode != "" {
		e2.s.PermissionRelay = &PermissionRelayConfig{Mode: mode}
	}
	return e2
}

func (e *relayEnv) args(decision string) args {
	return args{ExecutionID: e.execID, PermissionID: "per_0001", Decision: decision}
}

func (e *relayEnv) connect() *sdk.ClientSession {
	e.t.Helper()
	ts := httptest.NewServer(e.s)
	e.t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := mcpclient.Connect(ctx, ts.URL, e.s.Token)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = c.Close() })
	return c
}

func code(t *testing.T, err error) *execution.Error {
	t.Helper()
	var x *execution.Error
	if !errors.As(err, &x) {
		t.Fatalf("err = %v", err)
	}
	return x
}

func TestReplyPermissionIsUnregisteredByDefault(t *testing.T) {
	for _, mode := range []string{"", "policy", "off"} {
		e := newRelayEnv(t, mode)
		if _, err := e.s.call(marked(), "reply_permission", e.args("reject")); err == nil || code(t, err).Code != "INVALID_INPUT" {
			t.Fatalf("mode %q: a direct call was served: %v", mode, err)
		}
		if e.rep.n() != 0 || e.aw.n() != 0 || e.src.count() != 0 || e.hits.Load() != 0 {
			t.Fatalf("mode %q: native activity", mode)
		}
		tools, err := e.connect().ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range tools.Tools {
			if tool.Name == "reply_permission" {
				t.Fatalf("mode %q: reply_permission is listed", mode)
			}
		}
		if len(tools.Tools) != 11 {
			t.Fatalf("mode %q: %d tools, want the deployed 11", mode, len(tools.Tools))
		}
	}
}

func TestReplyPermissionContractOverTheMCPTransport(t *testing.T) {
	e := newRelayEnv(t, "reject_only")
	client := e.connect()
	ctx := context.Background()
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 12 {
		t.Fatalf("tools %d %v", len(tools.Tools), err)
	}
	var tool *sdk.Tool
	for _, x := range tools.Tools {
		if x.Name == "reply_permission" {
			tool = x
		}
	}
	if tool == nil {
		t.Fatal("not listed")
	}
	raw, _ := json.Marshal(tool.InputSchema)
	var in struct {
		Type       string `json:"type"`
		Required   []string
		Additional json.RawMessage           `json:"additionalProperties"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	props := make([]string, 0, 4)
	for k := range in.Properties {
		props = append(props, k)
	}
	if len(props) != 4 || in.Properties["execution_id"] == nil || in.Properties["permission_id"] == nil || in.Properties["decision"] == nil || in.Properties["supersedes"] == nil {
		t.Fatalf("input properties = %v, want exactly execution_id, permission_id, decision, supersedes", props)
	}
	if string(in.Additional) != "false" {
		t.Fatalf("additionalProperties = %s, want false", in.Additional)
	}
	req := map[string]bool{}
	for _, k := range in.Required {
		req[k] = true
	}
	if len(req) != 3 || !req["execution_id"] || !req["permission_id"] || !req["decision"] || req["supersedes"] {
		t.Fatalf("required = %v: supersedes is the only optional field", in.Required)
	}
	if got := in.Properties["permission_id"]["pattern"]; got != `^per[A-Za-z0-9_-]{1,125}$` {
		t.Fatalf("permission_id pattern = %v", got)
	}
	if got := in.Properties["supersedes"]["pattern"]; got != `^[A-Za-z0-9_-]{22}$` {
		t.Fatalf("supersedes pattern = %v", got)
	}
	enum, _ := in.Properties["decision"]["enum"].([]any)
	if len(enum) != 2 || enum[0] != "reject" || enum[1] != "allow_once" {
		t.Fatalf("decision enum = %v: no always, ever", enum)
	}
	if a := tool.Annotations; a == nil || a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Fatalf("annotations = %+v, want destructive and not read-only", a)
	}
	// The schema refuses every other field, always, and malformed ids before any handler.
	base := func() map[string]any {
		return map[string]any{"execution_id": e.execID, "permission_id": "per_0001", "decision": "reject"}
	}
	for name, mod := range map[string]func(map[string]any){
		"extra field message":    func(m map[string]any) { m["message"] = "because" },
		"extra field reply":      func(m map[string]any) { m["reply"] = "once" },
		"extra field session_id": func(m map[string]any) { m["session_id"] = "opencode:ses_a" },
		"always":                 func(m map[string]any) { m["decision"] = "always" },
		"once":                   func(m map[string]any) { m["decision"] = "once" },
		"permission without per": func(m map[string]any) { m["permission_id"] = "abc" },
		"permission with slash":  func(m map[string]any) { m["permission_id"] = "per/../x" },
		"short supersedes":       func(m map[string]any) { m["supersedes"] = "short" },
		"long supersedes":        func(m map[string]any) { m["supersedes"] = strings.Repeat("A", 23) },
		"supersedes charset":     func(m map[string]any) { m["supersedes"] = strings.Repeat("!", 22) },
		"empty execution":        func(m map[string]any) { m["execution_id"] = "" },
		"missing decision":       func(m map[string]any) { delete(m, "decision") },
	} {
		m := base()
		mod(m)
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		r, err := client.CallTool(cctx, &sdk.CallToolParams{Name: "reply_permission", Arguments: m})
		cancel()
		if err == nil && (r == nil || !r.IsError) {
			t.Errorf("%s accepted", name)
		}
	}
	if e.rep.n() != 0 || e.aw.n() != 0 {
		t.Fatalf("a schema-rejected call reached the native side: writes %d lines %d", e.rep.n(), e.aw.n())
	}
	// Success: the result always carries attempt_id, attempt, decided_by and human_verified=false.
	r := invoke(t, client, "reply_permission", base(), false)
	raw2, _ := json.Marshal(r.StructuredContent)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw2, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"execution_id", "permission_id", "decision", "outcome", "attempt_id", "attempt", "decided_by", "human_verified"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("result lacks %s: %s", k, raw2)
		}
	}
	out := decode[execution.ReplyResult](t, r)
	if out.Outcome != "replied" || out.Decision != "reject" || out.Attempt != 1 || out.DecidedBy != "chat_client" || out.HumanVerified || !regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`).MatchString(out.AttemptID) {
		t.Fatalf("%+v", out)
	}
	if string(m["human_verified"]) != "false" {
		t.Fatalf("human_verified = %s", m["human_verified"])
	}
	if e.rep.n() != 1 || e.rep.calls[0] != [3]string{"ses_a", "per_0001", "reject"} || e.aw.n() != 1 {
		t.Fatalf("writes %v lines %d", e.rep.calls, e.aw.n())
	}
}

func TestReplyPermissionErrorEnvelopeFields(t *testing.T) {
	e := newRelayEnv(t, "reject_only")
	client := e.connect()
	base := map[string]any{"execution_id": e.execID, "permission_id": "per_0001", "decision": "reject"}
	keys := func(r *sdk.CallToolResult) map[string]json.RawMessage {
		raw, _ := json.Marshal(r.StructuredContent)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	closed := map[string]bool{execution.ReasonNotVerified: true, execution.ReasonAllowOnceDisabled: true, execution.ReasonAttemptInFlight: true, execution.ReasonAlreadyReplied: true,
		execution.ReasonNothingToSupersede: true, execution.ReasonSupersedesMismatch: true, execution.ReasonSupersedeLimit: true, execution.ReasonSupersedeAllowOnceNo: true,
		execution.ReasonResolvedElsewhere: true, execution.ReasonAuditUnavailable: true, execution.ReasonNativeRejectedID: true, execution.ReasonUnrecognizedKind: true}
	checkReason := func(m map[string]json.RawMessage) {
		if r, ok := m["reason"]; ok {
			var s string
			_ = json.Unmarshal(r, &s)
			if !closed[s] {
				t.Fatalf("reason %q is outside the closed enum", s)
			}
		}
	}
	// A refusal: reason present, attempt fields absent.
	e.src.set(func() { e.src.gone = true })
	m := keys(invoke(t, client, "reply_permission", base, true))
	if _, ok := m["attempt_id"]; ok {
		t.Fatalf("NOT_FOUND carries attempt_id: %v", m)
	}
	if _, ok := m["attempt"]; ok {
		t.Fatalf("NOT_FOUND carries attempt: %v", m)
	}
	checkReason(m)
	var reason string
	_ = json.Unmarshal(m["reason"], &reason)
	if reason != execution.ReasonResolvedElsewhere {
		t.Fatalf("reason = %q", reason)
	}
	e.src.set(func() { e.src.gone = false })
	// The audit unavailable refusal.
	e.aw.mu.Lock()
	e.aw.err = audit.ErrUnavailable
	e.aw.mu.Unlock()
	m = keys(invoke(t, client, "reply_permission", base, true))
	checkReason(m)
	if string(m["code"]) != `"UPSTREAM_FAILURE"` || string(m["retryable"]) != "true" {
		t.Fatalf("audit_unavailable envelope = %v", m)
	}
	e.aw.mu.Lock()
	e.aw.err = nil
	e.aw.latched = false // test only: the latch is sticky until a restart, which this stands in for
	e.aw.mu.Unlock()
	// UNKNOWN_OUTCOME (a lost response): the only error that carries attempt_id and attempt.
	e.rep.mu.Lock()
	e.rep.err = errors.New("lost")
	e.rep.mu.Unlock()
	m = keys(invoke(t, client, "reply_permission", base, true))
	var id string
	var attempt int
	if json.Unmarshal(m["attempt_id"], &id) != nil || json.Unmarshal(m["attempt"], &attempt) != nil || len(id) != 22 || attempt != 1 || string(m["code"]) != `"UNKNOWN_OUTCOME"` {
		t.Fatalf("UNKNOWN_OUTCOME envelope = %v", m)
	}
	// The recorded envelope for the same key carries the same token and sends nothing more.
	before := e.rep.n()
	m2 := keys(invoke(t, client, "reply_permission", base, true))
	var id2 string
	_ = json.Unmarshal(m2["attempt_id"], &id2)
	if id2 != id || e.rep.n() != before {
		t.Fatalf("recorded envelope id %q vs %q, writes %d -> %d", id2, id, before, e.rep.n())
	}
}

func TestReplyPermissionRefusalsMakeZeroNativeCalls(t *testing.T) {
	type tc struct {
		name         string
		setup        func(*relayEnv)
		args         func(*relayEnv) args
		code, reason string
	}
	cases := []tc{
		{"read-only (reject included)", func(e *relayEnv) { e.s.ReadOnly = true }, func(e *relayEnv) args { return e.args("reject") }, "FORBIDDEN", ""},
		{"read-only allow_once", func(e *relayEnv) { e.s.ReadOnly = true }, func(e *relayEnv) args { return e.args("allow_once") }, "FORBIDDEN", ""},
		{"allow_once in reject_only", nil, func(e *relayEnv) args { return e.args("allow_once") }, "FORBIDDEN", execution.ReasonAllowOnceDisabled},
		{"allow_once for an unknown execution is still allow_once_disabled (step 2 first)", nil, func(e *relayEnv) args {
			a := e.args("allow_once")
			a.ExecutionID = "0123456789abcdef0123456789abcdef"
			return a
		}, "FORBIDDEN", execution.ReasonAllowOnceDisabled},
		{"claude execution", func(e *relayEnv) {
			f := newFakeClaude()
			f.execs[e.execID] = execution.Execution{ID: e.execID}
			e.s.Claude = f
		}, func(e *relayEnv) args { return e.args("reject") }, "INVALID_INPUT", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newRelayEnv(t, "reject_only")
			if c.setup != nil {
				c.setup(e)
			}
			before := e.hits.Load()
			_, err := e.s.call(marked(), "reply_permission", c.args(e))
			x := code(t, err)
			if x.Code != c.code || x.Reason != c.reason {
				t.Fatalf("error = %s/%q (%s), want %s/%q", x.Code, x.Reason, x.Message, c.code, c.reason)
			}
			if e.hits.Load() != before || e.src.count() != 0 || e.rep.n() != 0 || e.aw.n() != 0 {
				t.Fatalf("native activity: upstream %d->%d reads %d writes %d lines %d", before, e.hits.Load(), e.src.count(), e.rep.n(), e.aw.n())
			}
		})
	}
	// Step 3: an execution whose session is not in the ACL is refused after the ACL check only:
	// no permission read, no native write.
	e := newRelayEnv(t, "reject_only")
	pol, _ := security.New([]string{t.TempDir()}, []string{"ses_other"})
	e.s.Policy = pol
	_, err := e.s.call(marked(), "reply_permission", e.args("reject"))
	if x := code(t, err); x.Code != "FORBIDDEN" {
		t.Fatalf("%+v", x)
	}
	if e.src.count() != 0 || e.rep.n() != 0 || e.aw.n() != 0 {
		t.Fatal("an unauthorized session reached the permission read or write")
	}
	// An unknown execution (reject): NOT_FOUND from the writer's own table, no native call.
	e = newRelayEnv(t, "reject_only")
	a := e.args("reject")
	a.ExecutionID = "0123456789abcdef0123456789abcdef"
	before := e.hits.Load()
	if _, err = e.s.call(marked(), "reply_permission", a); code(t, err).Code != "NOT_FOUND" || e.hits.Load() != before {
		t.Fatalf("unknown execution: %v (upstream %d->%d)", err, before, e.hits.Load())
	}
}

func TestReplyPermissionIsMutatingAndProducesNoPromptShapedAuditLine(t *testing.T) {
	if !mutating("reply_permission") {
		t.Fatal("reply_permission must be in mutating(): it gates BRIDGE_READ_ONLY and the audit middleware")
	}
	var buf bytes.Buffer
	w := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(w)
	e := newRelayEnv(t, "reject_only")
	if _, err := e.s.call(marked(), "reply_permission", e.args("reject")); err != nil {
		t.Fatal(err)
	}
	e.s.ReadOnly = true
	_, _ = e.s.call(marked(), "reply_permission", e.args("reject"))
	if strings.Contains(buf.String(), "reply_permission") || strings.Contains(buf.String(), "prompt_submission") {
		t.Fatalf("the generic AUDIT-01 writer produced a line for reply_permission (its lines are the attempt record and S5): %q", buf.String())
	}
	// The existing mutating tools are unchanged.
	for _, n := range []string{"send_prompt", "interrupt_execution"} {
		if !mutating(n) {
			t.Fatalf("%s lost its mutating status", n)
		}
	}
	for _, n := range []string{"get_session", "list_sessions", "create_session", "get_execution_status"} {
		if mutating(n) {
			t.Fatalf("%s became mutating", n)
		}
	}
}

func TestReplyPermissionSupersedeOverTheTransport(t *testing.T) {
	e := newRelayEnv(t, "reject_only")
	client := e.connect()
	e.rep.mu.Lock()
	e.rep.err = errors.New("lost")
	e.rep.mu.Unlock()
	base := map[string]any{"execution_id": e.execID, "permission_id": "per_0001", "decision": "reject"}
	r := invoke(t, client, "reply_permission", base, true)
	raw, _ := json.Marshal(r.StructuredContent)
	var first execution.Error
	if err := json.Unmarshal(raw, &first); err != nil || first.AttemptID == "" {
		t.Fatalf("%s %v", raw, err)
	}
	e.rep.mu.Lock()
	e.rep.err = nil
	e.rep.mu.Unlock()
	base["supersedes"] = first.AttemptID
	out := decode[execution.ReplyResult](t, invoke(t, client, "reply_permission", base, false))
	if out.Attempt != 2 || out.AttemptID == first.AttemptID {
		t.Fatalf("%+v", out)
	}
	if e.rep.n() != 2 || e.aw.n() != 2 {
		t.Fatalf("writes %d lines %d, want 2 and 2", e.rep.n(), e.aw.n())
	}
	if e.aw.lines[1]["supersedes"] != first.AttemptID || e.aw.lines[1]["attempt"] != "2" {
		t.Fatalf("attempt line 2 = %v", e.aw.lines[1])
	}
}

// The tool surface after the merge of the relay: eleven (no create, no relay), twelve (either one),
// thirteen (both). create_session is configured by tests only; cmd/bridge does not wire it.
func TestToolCountsElevenTwelveThirteen(t *testing.T) {
	count := func(create, relay bool) int {
		e := newCreateEnv(t, nil)
		e.s.Policy, _ = security.New([]string{e.rootA}, []string{"ses_none"})
		e.s.Token = strings.Repeat("t", 32)
		if !create {
			e.s.Create = nil
		}
		if relay {
			e.s.PermissionRelay = &PermissionRelayConfig{Mode: "reject_only"}
		}
		ts := httptest.NewServer(e.s)
		t.Cleanup(ts.Close)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := mcpclient.Connect(ctx, ts.URL, e.s.Token)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		tools, err := c.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return len(tools.Tools)
	}
	for _, c := range []struct {
		create, relay bool
		want          int
	}{{false, false, 11}, {false, true, 12}, {true, false, 12}, {true, true, 13}} {
		if got := count(c.create, c.relay); got != c.want {
			t.Errorf("create=%v relay=%v: %d tools, want %d", c.create, c.relay, got, c.want)
		}
	}
}
