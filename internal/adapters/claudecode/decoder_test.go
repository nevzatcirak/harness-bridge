package claudecode

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecoderReadsLinesAndSkipsBlanks(t *testing.T) {
	in := `{"type":"system","subtype":"init","session_id":"` + testUUID + `","cwd":"/r"}` + "\n\n   \n" +
		`{"type":"result","subtype":"success","is_error":false,"num_turns":1,"session_id":"` + testUUID + `","terminal_reason":"completed"}` + "\n"
	d := NewDecoder(strings.NewReader(in), Limits{})
	e1, err := d.Next()
	if err != nil || e1.Type != "system" || e1.Subtype != "init" || e1.SessionID != testUUID || e1.Init == nil || e1.Init.CWD != "/r" {
		t.Fatalf("init: %+v %v", e1, err)
	}
	e2, err := d.Next()
	if err != nil || e2.Type != "result" || e2.Result == nil || e2.Result.IsError == nil || *e2.Result.IsError || e2.Result.TerminalReason != "completed" {
		t.Fatalf("result: %+v %v", e2, err)
	}
	if _, err = d.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestDecoderMarksUndecodableLines(t *testing.T) {
	for _, line := range []string{`not json`, `{"type":`, `[1,2]`, `"str"`, `42`, `null`, `{"type":"result"} trailing`} {
		d := NewDecoder(strings.NewReader(line+"\n"), Limits{})
		e, err := d.Next()
		if err != nil || !e.Undecodable {
			t.Errorf("%q: want undecodable event, got %+v %v", line, e, err)
		}
	}
	// A torn final line (no newline at EOF) is returned as a line, undecodable.
	d := NewDecoder(strings.NewReader(`{"type":"result","subty`), Limits{})
	if e, err := d.Next(); err != nil || !e.Undecodable {
		t.Fatalf("torn tail: %+v %v", e, err)
	}
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF after the torn tail, got %v", err)
	}
}

func TestDecoderBounds(t *testing.T) {
	long := `{"type":"x","pad":"` + strings.Repeat("a", 200) + `"}` + "\n"
	d := NewDecoder(strings.NewReader(long), Limits{MaxLine: 100, MaxTotal: 1 << 20})
	if _, err := d.Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("oversized line: %v", err)
	}
	if _, err := d.Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("decoder must stay failed after a bound error, got %v", err)
	}
	// A line exactly at the limit is accepted (the newline is not counted against it).
	ok := `{"type":"` + strings.Repeat("b", 100-11) + `"}`
	if len(ok) != 100 {
		t.Fatalf("test bug: %d", len(ok))
	}
	d = NewDecoder(strings.NewReader(ok+"\n"), Limits{MaxLine: 100, MaxTotal: 1 << 20})
	if e, err := d.Next(); err != nil || e.Undecodable {
		t.Fatalf("line at the limit: %+v %v", e, err)
	}
	small := `{"type":"a"}` + "\n"
	d = NewDecoder(strings.NewReader(strings.Repeat(small, 10)), Limits{MaxLine: 100, MaxTotal: 50})
	var n int
	var err error
	for err == nil {
		_, err = d.Next()
		n++
	}
	if !errors.Is(err, ErrStreamTooLarge) || n > 5 {
		t.Fatalf("total bound: n=%d err=%v", n, err)
	}
}

func TestDecoderDefaultLimits(t *testing.T) {
	l := Limits{}.withDefaults()
	if l.MaxLine != 1<<20 || l.MaxTotal != 64<<20 {
		t.Fatalf("defaults = %+v", l)
	}
}

func TestDecodeInitFields(t *testing.T) {
	e := DecodeLine([]byte(`{"type":"system","subtype":"init","cwd":"/r","session_id":"` + testUUID + `","model":"m","permissionMode":"default","apiKeySource":"none","claude_code_version":"2.1.322","mcp_servers":[],"plugins":[],"tools":["Read","Glob","Grep"]}`))
	i := e.Init
	if i == nil || i.PermissionMode != "default" || i.APIKeySource != "none" || i.Version != "2.1.322" || !i.MCPServersKnown || i.MCPServers != 0 || !i.PluginsKnown || i.Plugins != 0 || !i.ToolsKnown || len(i.Tools) != 3 {
		t.Fatalf("init = %+v", i)
	}
	// Absent lists are reported as unknown, never as empty.
	e = DecodeLine([]byte(`{"type":"system","subtype":"init","cwd":"/r","session_id":"x"}`))
	if e.Init.MCPServersKnown || e.Init.PluginsKnown || e.Init.ToolsKnown {
		t.Fatalf("absent lists must be unknown: %+v", e.Init)
	}
	e = DecodeLine([]byte(`{"type":"system","subtype":"init","mcp_servers":[{"name":"x"}],"plugins":[1],"tools":"Read"}`))
	if !e.Init.MCPServersKnown || e.Init.MCPServers != 1 || e.Init.Plugins != 1 || e.Init.ToolsKnown {
		t.Fatalf("init = %+v", e.Init)
	}
}

func TestDecodeResultFields(t *testing.T) {
	e := DecodeLine([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":0,"session_id":"` + testUUID + `","errors":["No conversation found with session ID: x"],"total_cost_usd":0.5,"terminal_reason":"aborted_tools","permission_denials":[{"tool_name":"Write","tool_use_id":"toolu_1","tool_input":{"file_path":"/r/n","content":"hi"}}]}`))
	r := e.Result
	if r == nil || r.Subtype != "error_during_execution" || r.IsError == nil || !*r.IsError || r.NumTurns == nil || *r.NumTurns != 0 || len(r.Errors) != 1 || r.CostUSD != 0.5 || len(r.Denials) != 1 || r.Denials[0].ToolName != "Write" {
		t.Fatalf("result = %+v", r)
	}
	if strings.Contains(r.Denials[0].InputSummary, "\n") || len(r.Denials[0].InputSummary) == 0 {
		t.Fatalf("denial summary = %q", r.Denials[0].InputSummary)
	}
	// Missing is_error / num_turns stay nil: nothing is defaulted into success.
	e = DecodeLine([]byte(`{"type":"result","subtype":"success"}`))
	if e.Result.IsError != nil || e.Result.NumTurns != nil {
		t.Fatalf("defaults leaked: %+v", e.Result)
	}
}

func TestDenialSummaryIsBoundedAndRedacted(t *testing.T) {
	big := strings.Repeat("x", 5000)
	e := DecodeLine([]byte(`{"type":"result","subtype":"success","permission_denials":[{"tool_name":"Bash","tool_use_id":"t","tool_input":{"command":"curl -H 'Authorization: Bearer sk-ant-api03-ABCDEFGHIJKLMNOP' ` + big + `"}}]}`))
	s := e.Result.Denials[0].InputSummary
	if len(s) > 256+len("…") || strings.Contains(s, "sk-ant") || strings.Contains(s, "ABCDEFGHIJKLMNOP") {
		t.Fatalf("summary not bounded/redacted: %d %q", len(s), s)
	}
}

func TestDecodeAssistantAndToolResultBlocks(t *testing.T) {
	a := DecodeLine([]byte(`{"type":"assistant","session_id":"s","message":{"content":[{"type":"text","text":"hello"},{"type":"tool_use","name":"Read","input":{"file_path":"/r/a"}},{"type":"thinking","thinking":"x"}]}}`))
	if len(a.Blocks) != 2 || a.Blocks[0].Text != "hello" || a.Blocks[1].Name != "Read" || a.Blocks[1].Input != `{"file_path":"/r/a"}` {
		t.Fatalf("blocks = %+v", a.Blocks)
	}
	u := DecodeLine([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":"plain"},{"type":"tool_result","tool_use_id":"tu2","content":[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"image"}]},{"type":"text","text":"ignored"}]}}`))
	if len(u.ToolResults) != 2 || u.ToolResults[0].Content != "plain" || u.ToolResults[1].Content != "ab" || u.ToolResults[1].ToolUseID != "tu2" {
		t.Fatalf("tool results = %+v", u.ToolResults)
	}
	// Malformed or oversized content never panics and stays bounded.
	big := strings.Repeat("a", 200000)
	b := DecodeLine([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"` + big + `"}]}}`))
	if len(b.Blocks) != 1 || len(b.Blocks[0].Text) > maxBlockText {
		t.Fatalf("text not bounded: %d", len(b.Blocks[0].Text))
	}
	for _, bad := range []string{`{"type":"assistant","message":5}`, `{"type":"assistant"}`, `{"type":"user","message":{"content":"s"}}`} {
		if e := DecodeLine([]byte(bad)); len(e.Blocks)+len(e.ToolResults) != 0 {
			t.Errorf("%s decoded blocks", bad)
		}
	}
}
