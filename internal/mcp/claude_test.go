package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// ADR-021 wiring (default off): routing of the claudecode: IDs to the adapter when enabled, and
// the proof that OpenCode behaviour and the tool set are unchanged either way.

const (
	claudeUUID   = "4e59c1bd-4333-4a71-875d-cba9301bd4a9"
	claudeGlobal = "claudecode:" + claudeUUID
	claudeOther  = "claudecode:00000000-0000-4000-8000-000000000009"
)

// fakeClaude is an in-memory ClaudeHarness.
type fakeClaude struct {
	mu          sync.Mutex
	sess        []claudecode.Session
	state       string
	sends       []string
	execs       map[string]execution.Execution
	keys        map[string]string
	interrupted []string
	history     claudecode.ReadResult
	spawned     int
}

func newFakeClaude() *fakeClaude {
	return &fakeClaude{sess: []claudecode.Session{{ID: claudeGlobal, Root: "/srv/proj", Label: "demo"}}, state: "idle", execs: map[string]execution.Execution{}, keys: map[string]string{}}
}

func (f *fakeClaude) Sessions() []claudecode.Session { return f.sess }
func (f *fakeClaude) Session(id string) (claudecode.Session, bool) {
	for _, s := range f.sess {
		if s.ID == id {
			return s, true
		}
	}
	return claudecode.Session{}, false
}
func (f *fakeClaude) State(string) string { f.mu.Lock(); defer f.mu.Unlock(); return f.state }
func (f *fakeClaude) Version() string     { return "2.1.322" }
func (f *fakeClaude) Send(_ context.Context, session, prompt, key, principal string) (execution.Execution, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Session(session); !ok {
		return execution.Execution{}, false, execution.Fail("NOT_FOUND", "session is not in the Claude allowlist")
	}
	if id, ok := f.keys[key]; ok {
		return f.execs[id], true, nil
	}
	if f.state == "running" {
		return execution.Execution{}, false, execution.Fail("SESSION_BUSY", "session has an active or uncertain execution")
	}
	f.spawned++
	id := fmt.Sprintf("%032x", f.spawned)
	x := execution.Execution{ID: id, SessionID: session, MessageID: "msg_" + id[:16], State: "running", Started: time.Unix(1700000000, 0).UTC(), Updated: time.Unix(1700000000, 0).UTC(), ChildUnconfirmed: false}
	f.execs[id], f.keys[key], f.state = x, id, "running"
	f.sends = append(f.sends, principal+"|"+prompt)
	return x, false, nil
}
func (f *fakeClaude) Owns(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.execs[id]
	return ok
}
func (f *fakeClaude) Get(id string) (execution.Execution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.execs[id]
	if !ok {
		return execution.Execution{}, execution.Fail("NOT_FOUND", "unknown execution")
	}
	return x, nil
}
func (f *fakeClaude) Interrupt(id string) (execution.Execution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupted = append(f.interrupted, id)
	return f.execs[id], nil
}
func (f *fakeClaude) History(string) (claudecode.ReadResult, error) { return f.history, nil }

func enableClaude(t *testing.T, bridge *Server) *fakeClaude {
	t.Helper()
	f := newFakeClaude()
	bridge.Policy.Sessions[claudeGlobal] = true
	bridge.Claude = f
	return f
}

func structured(t *testing.T, r *sdk.CallToolResult) map[string]any {
	t.Helper()
	b, _ := json.Marshal(r.StructuredContent)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return m
}

// Disabled (the default): the 11-tool set and every OpenCode answer carry none of the Claude
// fields, and every claudecode: ID is refused "not enabled".
func TestClaudeDisabledChangesNothing(t *testing.T) {
	client, _, bridge, _ := integration(t, true)
	if bridge.Claude != nil {
		t.Fatal("the adapter is on by default")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 11 {
		t.Fatalf("%d tools, want the deployed 11", len(tools.Tools))
	}
	for _, tool := range []string{"get_session", "read_session_history", "get_harness_capabilities"} {
		if txt := toolText(invoke(t, client, tool, map[string]any{"session_id": claudeGlobal}, true)); !strings.Contains(txt, "not enabled") {
			t.Errorf("%s: %q", tool, txt)
		}
	}
	if txt := toolText(invoke(t, client, "send_prompt", map[string]any{"session_id": claudeGlobal, "prompt": "p", "idempotency_key": "claude-key-0001"}, true)); !strings.Contains(txt, "not enabled") {
		t.Errorf("send_prompt: %q", txt)
	}
	// OpenCode answers carry no Claude-only key.
	banned := []string{"child_unconfirmed", "permission_denials", "cost_usd_reported", `"exit"`, "claudecode", "bridge_record", "history_complete", "first_recorded_at", "history_truncated"}
	sess := toolText(invoke(t, client, "get_session", map[string]any{"session_id": "opencode:ses_existing"}, false))
	hist := toolText(invoke(t, client, "read_session_history", map[string]any{"session_id": "opencode:ses_existing"}, false))
	caps := toolText(invoke(t, client, "get_harness_capabilities", map[string]any{"session_id": "opencode:ses_existing"}, false))
	send := invoke(t, client, "send_prompt", map[string]any{"session_id": "opencode:ses_existing", "prompt": "p", "idempotency_key": "oc-key-0000001"}, false)
	id := structured(t, send)["execution_id"].(string)
	status := toolText(invoke(t, client, "get_execution_status", map[string]any{"execution_id": id}, false))
	for name, body := range map[string]string{"get_session": sess, "history": hist, "capabilities": caps, "send_prompt": toolText(send), "status": status} {
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Errorf("%s carries %q while the adapter is off: %s", name, b, body)
			}
		}
	}
}

func TestClaudeEnabledRoutesTheSessionTools(t *testing.T) {
	client, _, bridge, _ := integration(t, true)
	f := enableClaude(t, bridge)
	// list_sessions / find_sessions merge: the Claude session next to the OpenCode ones, sorted
	// by session ID. (The discovery fixture of this package cannot serve a full native listing,
	// so the merge step is exercised directly.)
	merged := bridge.mergeClaude([]Session{{SessionID: "opencode:ses_b", Harness: "opencode"}, {SessionID: "opencode:ses_a", Harness: "opencode"}})
	var ids []string
	for _, s := range merged {
		ids = append(ids, s.SessionID)
	}
	if len(ids) != 3 || ids[0] != claudeGlobal || !sortedStrings(ids) || merged[0].Harness != "claudecode" || merged[0].Directory != "/srv/proj" || merged[0].Title != "demo" {
		t.Fatalf("merged = %v", merged)
	}
	delete(bridge.Policy.Sessions, claudeGlobal)
	if v := bridge.claudeViews(); len(v) != 0 {
		t.Fatalf("a session outside the ACL is listed: %v", v)
	}
	bridge.Policy.Sessions[claudeGlobal] = true
	g := structured(t, invoke(t, client, "get_session", map[string]any{"session_id": claudeGlobal}, false))
	if g["harness"] != "claudecode" || g["native_id"] != claudeUUID || g["directory"] != "/srv/proj" || g["title"] != "demo" || g["state"] != "idle" {
		t.Fatalf("get_session = %v", g)
	}
	// an ID outside the allowlist: NOT_FOUND, no send
	if txt := toolText(invoke(t, client, "get_session", map[string]any{"session_id": claudeOther}, true)); !strings.Contains(txt, "NOT_FOUND") {
		t.Errorf("unlisted get_session: %q", txt)
	}
	if txt := toolText(invoke(t, client, "send_prompt", map[string]any{"session_id": claudeOther, "prompt": "p", "idempotency_key": "claude-key-0002"}, true)); !strings.Contains(txt, "NOT_FOUND") || f.spawned != 0 {
		t.Errorf("unlisted send_prompt: %q (spawned %d)", txt, f.spawned)
	}
	// an ID in the allowlist but not in the ACL: FORBIDDEN
	delete(bridge.Policy.Sessions, claudeGlobal)
	if txt := toolText(invoke(t, client, "get_session", map[string]any{"session_id": claudeGlobal}, true)); !strings.Contains(txt, "FORBIDDEN") {
		t.Errorf("get_session outside the ACL: %q", txt)
	}
	bridge.Policy.Sessions[claudeGlobal] = true
	// capabilities
	c := structured(t, invoke(t, client, "get_harness_capabilities", map[string]any{"session_id": claudeGlobal}, false))
	blk, _ := c["claudecode"].(map[string]any)
	if blk == nil || blk["harness"] != "claudecode" || blk["version"] != "2.1.322" || blk["resume_exact_id"] != true || blk["native_lock"] != false || blk["history_source"] != "bridge_record" ||
		blk["permission_relay"] != false || blk["interrupt"] != "sigint_then_sigterm" || blk["active_terminal_control"] != false || blk["session_creation"] != false || blk["read_only_tools"] != true {
		t.Fatalf("capabilities = %v", c)
	}
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

func TestClaudeEnabledSendStatusResultInterrupt(t *testing.T) {
	buf := captureLog(t)
	client, _, bridge, _ := integration(t, true)
	f := enableClaude(t, bridge)
	r := invoke(t, client, "send_prompt", map[string]any{"session_id": claudeGlobal, "prompt": "list the files", "idempotency_key": "claude-key-0003"}, false)
	x := structured(t, r)
	id := x["execution_id"].(string)
	if x["session_id"] != claudeGlobal || x["state"] != "running" || len(f.sends) != 1 || f.sends[0] != "local|list the files" {
		t.Fatalf("send_prompt = %v sends %v", x, f.sends)
	}
	// a second send while busy: SESSION_BUSY, still one child
	if txt := toolText(invoke(t, client, "send_prompt", map[string]any{"session_id": claudeGlobal, "prompt": "again", "idempotency_key": "claude-key-0004"}, true)); !strings.Contains(txt, "SESSION_BUSY") || f.spawned != 1 {
		t.Errorf("second send: %q (spawned %d)", txt, f.spawned)
	}
	// the same key replays
	if y := structured(t, invoke(t, client, "send_prompt", map[string]any{"session_id": claudeGlobal, "prompt": "list the files", "idempotency_key": "claude-key-0003"}, false)); y["execution_id"] != id {
		t.Errorf("replay = %v", y)
	}
	if s := structured(t, invoke(t, client, "get_execution_status", map[string]any{"execution_id": id}, false)); s["execution_id"] != id {
		t.Errorf("status = %v", s)
	}
	// interrupt_execution reaches the adapter and is audited
	if i := structured(t, invoke(t, client, "interrupt_execution", map[string]any{"execution_id": id}, false)); i["execution_id"] != id || len(f.interrupted) != 1 || f.interrupted[0] != id {
		t.Errorf("interrupt = %v (%v)", i, f.interrupted)
	}
	// get_execution_result: the execution plus the messages recorded for it
	f.history = claudecode.ReadResult{Records: []claudecode.Record{
		{Seq: 1, ExecutionID: id, Kind: claudecode.KindTurnStart, Prompt: "list the files", At: time.Unix(1700000001, 0)},
		{Seq: 2, ExecutionID: "ffffffffffffffffffffffffffffffff", Kind: claudecode.KindTurnStart, Prompt: "other", At: time.Unix(1700000002, 0)},
	}}
	res := structured(t, invoke(t, client, "get_execution_result", map[string]any{"execution_id": id}, false))
	msgs, _ := res["messages"].([]any)
	if len(msgs) != 1 || res["execution"].(map[string]any)["execution_id"] != id {
		t.Fatalf("result = %v", res)
	}
	// one audit line per mutating call, with the harness and the Claude session ID
	lines := auditLines(t, buf)
	var mut []map[string]any
	for _, l := range lines {
		if l["event"] == "prompt_submission" || l["event"] == "interrupt" {
			mut = append(mut, l)
		}
	}
	// calls: send ok, send busy, send replay, interrupt = 4 mutating calls
	if len(mut) != 4 {
		t.Fatalf("%d audit lines for 4 mutating calls: %v", len(mut), mut)
	}
	for _, l := range mut {
		if l["harness"] != "claudecode" || l["session_id"] != claudeGlobal || l["principal"] != "local" {
			t.Errorf("audit line %v", l)
		}
	}
	if mut[0]["outcome"] != "new" || mut[1]["outcome"] != "rejected" || mut[1]["error_code"] != "SESSION_BUSY" || mut[2]["outcome"] != "replay" || mut[3]["event"] != "interrupt" {
		t.Errorf("audit outcomes %v", mut)
	}
	if strings.Contains(buf.String(), "list the files") || strings.Contains(buf.String(), "claude-key-0003") {
		t.Error("the prompt or the raw key reached the audit log")
	}
}

func TestClaudeHistoryIsTheBridgeRecord(t *testing.T) {
	client, _, bridge, _ := integration(t, true)
	f := enableClaude(t, bridge)
	f.history = claudecode.ReadResult{Partial: true, Records: []claudecode.Record{
		{Seq: 1, ExecutionID: "a", Kind: claudecode.KindTurnStart, Prompt: "hello", At: time.Unix(1700000001, 0)},
		{Seq: 2, ExecutionID: "a", Kind: claudecode.KindAssistantBlock, Block: &claudecode.Block{Type: "text", Text: "hi"}, At: time.Unix(1700000002, 0)},
		{Seq: 3, ExecutionID: "a", Kind: claudecode.KindTurnEnd, State: "completed", At: time.Unix(1700000003, 0)},
	}}
	h := structured(t, invoke(t, client, "read_session_history", map[string]any{"session_id": claudeGlobal}, false))
	msgs, _ := h["messages"].([]any)
	if h["source"] != "bridge_record" || len(msgs) != 3 || h["history_complete"] != false || h["history_truncated"] != true || h["first_recorded_at"] == nil {
		t.Fatalf("history = %v", h)
	}
	roles := []string{}
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["info"].(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "user,assistant,system" {
		t.Errorf("roles %v", roles)
	}
	// limit takes the newest; a cursor is not supported
	l := structured(t, invoke(t, client, "read_session_history", map[string]any{"session_id": claudeGlobal, "limit": 1}, false))
	if len(l["messages"].([]any)) != 1 {
		t.Errorf("limit: %v", l)
	}
	if txt := toolText(invoke(t, client, "read_session_history", map[string]any{"session_id": claudeGlobal, "before": "abc"}, true)); !strings.Contains(txt, "INVALID_INPUT") {
		t.Errorf("cursor: %q", txt)
	}
}

// With the adapter on, OpenCode sessions behave as before and Claude executions never shadow them.
func TestClaudeEnabledLeavesOpenCodeAlone(t *testing.T) {
	client, _, bridge, _ := integration(t, true)
	enableClaude(t, bridge)
	g := structured(t, invoke(t, client, "get_session", map[string]any{"session_id": "opencode:ses_existing"}, false))
	if g["harness"] != "opencode" {
		t.Fatalf("get_session = %v", g)
	}
	x := structured(t, invoke(t, client, "send_prompt", map[string]any{"session_id": "opencode:ses_existing", "prompt": "p", "idempotency_key": "oc-key-0000002"}, false))
	id := x["execution_id"].(string)
	if s := structured(t, invoke(t, client, "get_execution_status", map[string]any{"execution_id": id}, false)); s["session_id"] != "opencode:ses_existing" {
		t.Fatalf("status = %v", s)
	}
	for _, b := range []string{"child_unconfirmed", "permission_denials", "claudecode"} {
		if _, ok := x[b]; ok {
			t.Errorf("an OpenCode execution carries %q", b)
		}
	}
}
