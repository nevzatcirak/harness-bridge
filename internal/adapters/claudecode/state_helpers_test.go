package claudecode

import (
	"encoding/json"
	"testing"
)

const testRoot = "/srv/projects/proj-x"

func testCfg() Config {
	return Config{RequestedUUID: testUUID, CanonicalRoot: testRoot, AllowedVersions: []string{"2.1.322"}, Tools: []string{"Read", "Glob", "Grep"}}
}

func mustJSON(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// initJSON builds a compliant system/init line; mods override or (nil) delete fields.
func initJSON(mods map[string]any) string {
	m := map[string]any{
		"type": "system", "subtype": "init", "cwd": testRoot, "session_id": testUUID, "model": "claude-sonnet-5-5",
		"permissionMode": "default", "apiKeySource": "none", "claude_code_version": "2.1.322",
		"mcp_servers": []any{}, "plugins": []any{}, "tools": []any{"Read", "Glob", "Grep"},
	}
	for k, v := range mods {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return mustJSON(m)
}

func resultJSON(mods map[string]any) string {
	m := map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "num_turns": 1, "session_id": testUUID,
		"terminal_reason": "completed", "stop_reason": "end_turn", "total_cost_usd": 0.09, "permission_denials": []any{}, "result": "ok",
	}
	for k, v := range mods {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return mustJSON(m)
}

func abortedJSON() string {
	return resultJSON(map[string]any{"subtype": "error_during_execution", "is_error": true, "terminal_reason": "aborted_tools", "num_turns": 3})
}

func line(s string) Step { return Step{Kind: StepLine, Event: DecodeLine([]byte(s))} }
func eof() Step          { return Step{Kind: StepEOF} }
func exit(code int) Step { return Step{Kind: StepExit, ExitCode: code} }

var (
	userInt   = Step{Kind: StepUserInterrupt}
	capFired  = Step{Kind: StepCap}
	graceEnd  = Step{Kind: StepGraceExpired}
	shutdown  = Step{Kind: StepShutdown}
	exhausted = Step{Kind: StepEscalationExhausted}
)

func assertVerdict(t *testing.T, v Verdict, row, state, reason string, released bool) {
	t.Helper()
	if !v.Decided || v.Row != row || v.State != state || v.Reason != reason || v.GuardReleased != released {
		t.Fatalf("verdict = {decided:%v row:%q state:%q code:%q reason:%q detail:%q released:%v unconfirmed:%v violations:%v}\nwant row %q state %q reason %q released %v",
			v.Decided, v.Row, v.State, v.Code, v.Reason, v.Detail, v.GuardReleased, v.ChildUnconfirmed, v.Violations, row, state, reason, released)
	}
}
