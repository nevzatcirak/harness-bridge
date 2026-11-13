package claudecode

import (
	"reflect"
	"testing"
)

// Every §3 row, table-driven. "released" is the automatic guard release.
func TestRows(t *testing.T) {
	ok := []Step{line(initJSON(nil)), line(resultJSON(nil)), eof()}
	cases := []struct {
		name            string
		steps           []Step
		row, state, rsn string
		released        bool
		code            string
	}{
		{"3 success needs all four: completed", append(append([]Step{}, ok...), exit(0)), "3", "completed", "", true, ""},
		{"7 success-shaped result, exit non-zero", append(append([]Step{}, ok...), exit(1)), "7", "unknown", "", false, ""},
		{"7 success-shaped result, exit 143", append(append([]Step{}, ok...), exit(143)), "7", "unknown", "", false, ""},
		{"10 is_error:true on exit 0 is never completed", []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"is_error": true})), eof(), exit(0)}, "10", "unknown", "", false, ""},
		{"10 is_error missing is never success", []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"is_error": nil})), eof(), exit(0)}, "10", "unknown", "", false, ""},
		{"10 subtype not success on exit 0", []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"subtype": "weird"})), eof(), exit(0)}, "10", "unknown", "", false, ""},
		{"10 unlisted terminal_reason on a success result", []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"terminal_reason": "mystery"})), eof(), exit(0)}, "10", "unknown", "", false, ""},
		{"10 missing terminal_reason on a success result", []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"terminal_reason": nil})), eof(), exit(0)}, "10", "unknown", "", false, ""},
		{"10 exit status not obtainable without a result", []Step{line(initJSON(nil)), eof(), {Kind: StepExit, ExitUnknown: true}}, "10", "unknown", "", false, ""},
		{"4 max turns, exit 1", []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"subtype": "error_max_turns", "is_error": true, "terminal_reason": "max_turns", "num_turns": 2})), eof(), exit(1)}, "4", "failed", "", true, "ERROR_MAX_TURNS"},
		{"4 max budget, exit 1", []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"subtype": "error_max_budget_usd", "is_error": true, "terminal_reason": "max_budget"})), eof(), exit(1)}, "4", "failed", "", true, "ERROR_MAX_BUDGET_USD"},
		{"4 error_during_execution that is not the row-2 shape", []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"subtype": "error_during_execution", "is_error": true, "num_turns": 1, "terminal_reason": "x"})), eof(), exit(1)}, "4", "failed", "", true, "ERROR_DURING_EXECUTION"},
		{"4 SIGINT shape without a requested interrupt is failed", []Step{line(initJSON(nil)), line(abortedJSON()), eof(), exit(0)}, "4", "failed", "", true, "ERROR_DURING_EXECUTION"},
		{"4 aborted shape, exit 130, no interrupt", []Step{line(initJSON(nil)), line(abortedJSON()), eof(), exit(130)}, "4", "failed", "", true, "ERROR_DURING_EXECUTION"},
		{"10 error result with a user interrupt that is not aborted_*", []Step{line(initJSON(nil)), userInt, line(resultJSON(map[string]any{"subtype": "error_max_turns", "is_error": true, "terminal_reason": "max_turns"})), eof(), exit(1)}, "10", "unknown", "", false, ""},
		{"5 user interrupt acknowledged by aborted_tools, exit 0", []Step{line(initJSON(nil)), userInt, line(abortedJSON()), eof(), exit(0)}, "5", "interrupted", "", true, ""},
		{"5 aborted_streaming", []Step{line(initJSON(nil)), userInt, line(resultJSON(map[string]any{"subtype": "error_during_execution", "is_error": true, "terminal_reason": "aborted_streaming"})), eof(), exit(0)}, "5", "interrupted", "", true, ""},
		{"5 exit code is not used", []Step{line(initJSON(nil)), userInt, line(abortedJSON()), eof(), exit(1)}, "5", "interrupted", "", true, ""},
		{"6 SIGTERM exit 143, no result", []Step{line(initJSON(nil)), eof(), exit(143)}, "6", "unknown", "PROCESS_ENDED_WITHOUT_RESULT", false, ""},
		{"6 init seen, stdout closed, exit 0", []Step{line(initJSON(nil)), eof(), exit(0)}, "6", "unknown", "PROCESS_ENDED_WITHOUT_RESULT", false, ""},
		{"6 crash without any output", []Step{eof(), exit(1)}, "6", "unknown", "PROCESS_ENDED_WITHOUT_RESULT", false, ""},
		{"6 user interrupt and exit 143 with no result", []Step{line(initJSON(nil)), userInt, eof(), exit(143)}, "6", "unknown", "ABORT_UNCONFIRMED", false, ""},
		{"6 broken stream, no result", []Step{line(initJSON(nil)), {Kind: StepStreamBroken}, eof(), exit(0)}, "6", "unknown", "PROCESS_ENDED_WITHOUT_RESULT", false, ""},
		{"1 spawn failed", []Step{{Kind: StepSpawnFailed}}, "1", "failed", "", true, "PRE_SUBMISSION_UNAVAILABLE"},
		{"1 no prompt byte accepted by stdin", []Step{{Kind: StepStdinFailed, Accepted: 0}}, "1", "failed", "", true, "PRE_SUBMISSION_UNAVAILABLE"},
		{"1s stdin failed after some bytes", []Step{{Kind: StepStdinFailed, Accepted: 17}}, "1s", "unknown", "SUBMISSION_UNCONFIRMED", false, ""},
		{"2 unknown session id (OBSERVED shape)", []Step{line(notFoundJSON(testUUID)), eof(), exit(1)}, "2", "failed", "", true, "NOT_FOUND"},
		{"8 row-2 shape plus a trailing line is row 8, not row 2", []Step{line(notFoundJSON(testUUID)), line(`{"type":"system","subtype":"x"}`), eof(), exit(1)}, "8", "unknown", "", false, ""},
		{"4 not-found shape with num_turns 1 is not row 2", []Step{line(notFoundJSON(testUUID, "num_turns", 1)), eof(), exit(1)}, "4", "failed", "", true, "ERROR_DURING_EXECUTION"},
		{"4 not-found shape with exit 2 is not row 2", []Step{line(notFoundJSON(testUUID)), eof(), exit(2)}, "4", "failed", "", true, "ERROR_DURING_EXECUTION"},
		{"4 not-found text missing is not row 2", []Step{line(notFoundJSON(testUUID, "errors", []any{"something else"})), eof(), exit(1)}, "4", "failed", "", true, "ERROR_DURING_EXECUTION"},
		{"8 two result lines", []Step{line(initJSON(nil)), line(resultJSON(nil)), line(resultJSON(nil)), eof(), exit(0)}, "8", "unknown", "", false, ""},
		{"8 tool_progress after the final result counts", []Step{line(initJSON(nil)), line(resultJSON(nil)), line(`{"type":"system","subtype":"tool_progress","session_id":"` + testUUID + `"}`), eof(), exit(0)}, "8", "unknown", "", false, ""},
		{"8 api_retry after the final result counts", []Step{line(initJSON(nil)), line(resultJSON(nil)), line(`{"type":"system","subtype":"api_retry","session_id":"` + testUUID + `"}`), eof(), exit(0)}, "8", "unknown", "", false, ""},
		{"3 tool_progress BEFORE the final result does not count", []Step{line(initJSON(nil)), line(`{"type":"system","subtype":"tool_progress","session_id":"` + testUUID + `"}`), line(`{"type":"system","subtype":"api_retry","session_id":"` + testUUID + `"}`), line(resultJSON(nil)), eof(), exit(0)}, "3", "completed", "", true, ""},
		{"8 an undecodable line after a success-shaped run, exit 0, is not row 3", []Step{line(initJSON(nil)), line(resultJSON(nil)), line(`{"type":"resu`), eof(), exit(0)}, "8", "unknown", "", false, ""},
		{"8 an unknown event type after the final result counts", []Step{line(initJSON(nil)), line(resultJSON(nil)), line(`{"type":"brand_new_event"}`), eof(), exit(0)}, "8", "unknown", "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Decide(testCfg(), tc.steps)
			assertVerdict(t, v, tc.row, tc.state, tc.rsn, tc.released)
			if tc.code != "" && v.Code != tc.code {
				t.Fatalf("code = %q want %q", v.Code, tc.code)
			}
			if tc.state == "unknown" && v.GuardReleased {
				t.Fatal("an unknown verdict released the guard")
			}
		})
	}
}

func notFoundJSON(session string, kv ...any) string {
	m := map[string]any{
		"type": "result", "subtype": "error_during_execution", "duration_ms": 0, "is_error": true, "num_turns": 0,
		"stop_reason": nil, "session_id": session, "total_cost_usd": 0, "permission_denials": []any{},
		"errors": []any{"No conversation found with session ID: " + session},
	}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return mustJSON(m)
}

// The evaluation order as a table of OVERLAPPING evidence (ADR-021 §10).
func TestOverlappingEvidence(t *testing.T) {
	init0 := line(initJSON(nil))
	succ, abort := line(resultJSON(nil)), line(abortedJSON())
	cases := []struct {
		name            string
		steps           []Step
		row, state, rsn string
		unconfirmed     bool
		interrupts      []string
	}{
		{"cap + success result + exit 0 is row 9", []Step{init0, capFired, succ, eof(), exit(0)}, "9", "unknown", "MONITOR_EXPIRED", false, []string{"cap"}},
		{"cap + success result BEFORE the cap + exit 0 is row 9", []Step{init0, succ, capFired, eof(), exit(0)}, "9", "unknown", "MONITOR_EXPIRED", false, []string{"cap"}},
		{"cap + aborted_* result + exit 0 is row 9, never 5", []Step{init0, capFired, abort, eof(), exit(0)}, "9", "unknown", "MONITOR_EXPIRED", false, []string{"cap"}},
		{"cap + no result + exit 143 is row 9, never row 6", []Step{init0, capFired, eof(), exit(143)}, "9", "unknown", "MONITOR_EXPIRED", false, []string{"cap"}},
		{"cap + escalation exhausted gains child_unconfirmed", []Step{init0, capFired, exhausted}, "9", "unknown", "MONITOR_EXPIRED", true, []string{"cap"}},
		{"grace expired + late aborted_* result is 6a", []Step{init0, succ, graceEnd, abort, eof(), exit(0)}, "6a", "unknown", "EXIT_UNCONFIRMED", true, []string{"grace"}},
		{"grace expired + late success with exit 0 is 6a", []Step{init0, succ, graceEnd, eof(), exit(0)}, "6a", "unknown", "EXIT_UNCONFIRMED", true, []string{"grace"}},
		{"result + hang (grace expires, exit never comes) is 6a", []Step{init0, succ, graceEnd}, "6a", "unknown", "EXIT_UNCONFIRMED", true, []string{"grace"}},
		{"result + exit status not obtainable is 6a", []Step{init0, succ, eof(), {Kind: StepExit, ExitUnknown: true}}, "6a", "unknown", "EXIT_UNCONFIRMED", true, nil},
		{"result + second result, child exits in time, is row 8", []Step{init0, succ, succ, eof(), exit(0)}, "8", "unknown", "", false, nil},
		{"result + second result, child hangs, is 6a", []Step{init0, succ, succ, graceEnd}, "6a", "unknown", "EXIT_UNCONFIRMED", true, []string{"grace"}},
		{"shutdown + aborted_* result is 9s, never row 5", []Step{init0, shutdown, abort, eof(), exit(0)}, "9s", "unknown", "MANAGER_CLOSED", false, []string{"shutdown"}},
		{"user interrupt + aborted_* + exit 0, no latch, is row 5", []Step{init0, userInt, abort, eof(), exit(0)}, "5", "interrupted", "", false, []string{"user"}},
		{"user interrupt whose escalation is exhausted is 6b", []Step{init0, userInt, exhausted}, "6b", "unknown", "ABORT_UNCONFIRMED", true, []string{"user"}},
		{"user interrupt exhausted + a late aborted_* result stays 6b", []Step{init0, userInt, exhausted, abort, eof(), exit(0)}, "6b", "unknown", "ABORT_UNCONFIRMED", true, []string{"user"}},
		{"user interrupt + exit 143 without result is row 6 ABORT_UNCONFIRMED", []Step{init0, userInt, eof(), exit(143)}, "6", "unknown", "ABORT_UNCONFIRMED", false, []string{"user"}},
		{"init latch + aborted_* result is row 0", []Step{line(initJSON(map[string]any{"permissionMode": "acceptEdits"})), abort, eof(), exit(0)}, "0", "unknown", "IDENTITY_MISMATCH", false, []string{"latch"}},
		{"init latch + success result + exit 0 is row 0, never 3", []Step{line(initJSON(map[string]any{"apiKeySource": "ANTHROPIC_API_KEY"})), succ, eof(), exit(0)}, "0", "unknown", "IDENTITY_MISMATCH", false, []string{"latch"}},
		{"init latch + no result + exit 143 is row 0, never row 6", []Step{line(initJSON(map[string]any{"cwd": "/elsewhere"})), eof(), exit(143)}, "0", "unknown", "IDENTITY_MISMATCH", false, []string{"latch"}},
		{"user interrupt then cap: both reasons, row 9", []Step{init0, userInt, capFired, abort, eof(), exit(0)}, "9", "unknown", "MONITOR_EXPIRED", false, []string{"user", "cap"}},
		{"earliest latch wins: init violation then cap is row 0", []Step{line(initJSON(map[string]any{"permissionMode": "auto"})), capFired}, "0", "unknown", "IDENTITY_MISMATCH", false, []string{"latch"}},
		{"earliest latch wins: cap then shutdown is row 9", []Step{init0, capFired, shutdown}, "9", "unknown", "MONITOR_EXPIRED", false, []string{"cap"}},
		{"earliest latch wins: shutdown then cap is row 9s", []Step{init0, shutdown, capFired}, "9s", "unknown", "MANAGER_CLOSED", false, []string{"shutdown"}},
		{"cap then grace: still row 9", []Step{init0, succ, capFired, graceEnd}, "9", "unknown", "MONITOR_EXPIRED", false, []string{"cap"}},
		{"grace then cap: still 6a", []Step{init0, succ, graceEnd, capFired}, "6a", "unknown", "EXIT_UNCONFIRMED", true, []string{"grace"}},
		{"grace with no result is a no-op (nothing to wait for)", []Step{init0, graceEnd, eof(), exit(0)}, "6", "unknown", "PROCESS_ENDED_WITHOUT_RESULT", false, nil},
		{"exhausted with no interrupt is a no-op", []Step{init0, exhausted, eof(), exit(143)}, "6", "unknown", "PROCESS_ENDED_WITHOUT_RESULT", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Decide(testCfg(), tc.steps)
			assertVerdict(t, v, tc.row, tc.state, tc.rsn, tc.state == "interrupted")
			if v.ChildUnconfirmed != tc.unconfirmed {
				t.Fatalf("child_unconfirmed = %v want %v", v.ChildUnconfirmed, tc.unconfirmed)
			}
			if !reflect.DeepEqual(v.InterruptReasons, tc.interrupts) && !(len(v.InterruptReasons) == 0 && len(tc.interrupts) == 0) {
				t.Fatalf("interrupt reasons = %v want %v", v.InterruptReasons, tc.interrupts)
			}
			if v.GuardReleased && v.State == "unknown" {
				t.Fatal("a latched or unknown verdict released the guard")
			}
		})
	}
}

// A latch can only fire while the run is non-terminal.
func TestNoLatchAfterADecidedRun(t *testing.T) {
	base := []Step{line(initJSON(nil)), line(resultJSON(nil)), eof(), exit(0)}
	for name, late := range map[string]Step{"cap": capFired, "grace": graceEnd, "shutdown": shutdown, "exhausted": exhausted, "user": userInt} {
		v := Decide(testCfg(), append(append([]Step{}, base...), late))
		assertVerdict(t, v, "3", "completed", "", true)
		if len(v.InterruptReasons) != 0 {
			t.Errorf("%s: a late event recorded an interrupt: %v", name, v.InterruptReasons)
		}
	}
}

// After any latch every later line, result and exit is audit-only.
func TestAuditOnlyAfterALatch(t *testing.T) {
	steps := []Step{
		line(initJSON(nil)), capFired,
		line(`{"type":"assistant"}`), line(`{"type":"user"}`), line(resultJSON(nil)), line(`garbage`), eof(), exit(0),
	}
	v := Decide(testCfg(), steps)
	assertVerdict(t, v, "9", "unknown", "MONITOR_EXPIRED", false)
	if v.LateLines != 4 || !v.LateResult {
		t.Fatalf("late_lines_after_verdict=%d late_result_seen=%v, want 4/true", v.LateLines, v.LateResult)
	}
	v = Decide(testCfg(), []Step{line(initJSON(nil)), line(resultJSON(nil)), graceEnd, line(abortedJSON()), exit(0)})
	if v.Row != "6a" || v.LateLines != 1 || !v.LateResult {
		t.Fatalf("6a late: %+v", v)
	}
	// A run with no latch reports no late lines.
	v = Decide(testCfg(), []Step{line(initJSON(nil)), line(resultJSON(nil)), eof(), exit(0)})
	if v.LateLines != 0 || v.LateResult {
		t.Fatalf("no latch, but late audit fields set: %+v", v)
	}
}

func TestInitVerificationLatch(t *testing.T) {
	violations := map[string]string{
		"permissionMode":  initJSON(map[string]any{"permissionMode": "bypassPermissions"}),
		"apiKeySource":    initJSON(map[string]any{"apiKeySource": "ANTHROPIC_API_KEY"}),
		"apiKeySource 2":  initJSON(map[string]any{"apiKeySource": nil}),
		"cwd":             initJSON(map[string]any{"cwd": testRoot + "/sub"}),
		"cwd missing":     initJSON(map[string]any{"cwd": nil}),
		"session_id":      initJSON(map[string]any{"session_id": "00000000-0000-4000-8000-000000000000"}),
		"version":         initJSON(map[string]any{"claude_code_version": "2.1.323"}),
		"version missing": initJSON(map[string]any{"claude_code_version": nil}),
		"mcp_servers":     initJSON(map[string]any{"mcp_servers": []any{map[string]any{"name": "x"}}}),
		"mcp absent":      initJSON(map[string]any{"mcp_servers": nil}),
		"plugins":         initJSON(map[string]any{"plugins": []any{map[string]any{"name": "p"}}}),
		"plugins absent":  initJSON(map[string]any{"plugins": nil}),
		"tools extra":     initJSON(map[string]any{"tools": []any{"Read", "Glob", "Grep", "Bash"}}),
		"tools missing":   initJSON(map[string]any{"tools": []any{"Read", "Glob"}}),
		"tools absent":    initJSON(map[string]any{"tools": nil}),
		"tools dup":       initJSON(map[string]any{"tools": []any{"Read", "Read", "Glob", "Grep"}}),
	}
	for name, initLine := range violations {
		t.Run(name, func(t *testing.T) {
			// Beats a later success result, exit 0, and an aborted result.
			for _, tail := range [][]Step{
				{line(resultJSON(nil)), eof(), exit(0)},
				{line(abortedJSON()), eof(), exit(0)},
				{eof(), exit(143)},
			} {
				v := Decide(testCfg(), append([]Step{line(initLine)}, tail...))
				assertVerdict(t, v, "0", "unknown", "IDENTITY_MISMATCH", false)
				if len(v.Violations) == 0 {
					t.Fatal("no violation recorded")
				}
				if !reflect.DeepEqual(v.InterruptReasons, []string{"latch"}) {
					t.Fatalf("interrupt reasons = %v", v.InterruptReasons)
				}
			}
		})
	}
	// Tool order is not significant.
	v := Decide(testCfg(), []Step{line(initJSON(map[string]any{"tools": []any{"Grep", "Read", "Glob"}})), line(resultJSON(nil)), eof(), exit(0)})
	assertVerdict(t, v, "3", "completed", "", true)
	// A hook event before init latches immediately, even if init never arrives.
	for _, sub := range []string{"hook_started", "hook_progress", "hook_response"} {
		v = Decide(testCfg(), []Step{line(`{"type":"system","subtype":"` + sub + `"}`), line(initJSON(nil)), line(resultJSON(nil)), eof(), exit(0)})
		assertVerdict(t, v, "0", "unknown", "IDENTITY_MISMATCH", false)
	}
	v = Decide(testCfg(), []Step{line(`{"type":"system","subtype":"plugin_install"}`), eof(), exit(0)})
	assertVerdict(t, v, "0", "unknown", "IDENTITY_MISMATCH", false)
	// The same hook event AFTER a compliant init is not a latch by itself.
	v = Decide(testCfg(), []Step{line(initJSON(nil)), line(`{"type":"system","subtype":"hook_started"}`), line(resultJSON(nil)), eof(), exit(0)})
	assertVerdict(t, v, "3", "completed", "", true)
}

// Events of another session are ignored for state and counted only.
func TestCorrelationBySessionID(t *testing.T) {
	foreign := "00000000-0000-4000-8000-000000000000"
	v := Decide(testCfg(), []Step{
		line(initJSON(nil)),
		line(resultJSON(map[string]any{"session_id": foreign})), // a subagent / forked id
		line(resultJSON(nil)), eof(), exit(0),
	})
	// The foreign result came BEFORE the correlated one, so it is not a counting
	// event after the final result and is not a second result.
	assertVerdict(t, v, "3", "completed", "", true)
	if v.ForeignEvents != 1 {
		t.Fatalf("foreign events = %d", v.ForeignEvents)
	}
	// A success-shaped result of another session never completes the run.
	v = Decide(testCfg(), []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"session_id": foreign})), eof(), exit(0)})
	assertVerdict(t, v, "6", "unknown", "PROCESS_ENDED_WITHOUT_RESULT", false)
	// A result with no session_id is not correlated either.
	v = Decide(testCfg(), []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"session_id": nil})), eof(), exit(0)})
	assertVerdict(t, v, "6", "unknown", "PROCESS_ENDED_WITHOUT_RESULT", false)
	// A foreign line AFTER the final correlated result still counts (fail closed).
	v = Decide(testCfg(), []Step{line(initJSON(nil)), line(resultJSON(nil)), line(resultJSON(map[string]any{"session_id": foreign})), eof(), exit(0)})
	assertVerdict(t, v, "8", "unknown", "", false)
}

func TestOnlyAUserInterruptCanProduceInterrupted(t *testing.T) {
	for name, tr := range map[string]Step{"cap": capFired, "shutdown": shutdown} {
		v := Decide(testCfg(), []Step{line(initJSON(nil)), tr, line(abortedJSON()), eof(), exit(0)})
		if v.State == "interrupted" || v.Row == "5" {
			t.Fatalf("%s interrupt produced %+v", name, v)
		}
	}
}

func TestMachineFeedReportsInterruptActions(t *testing.T) {
	m := NewMachine(testCfg())
	if a := m.Feed(line(initJSON(nil))); a.Interrupt != "" {
		t.Fatalf("compliant init asked for an interrupt: %+v", a)
	}
	if a := m.Feed(capFired); a.Interrupt != "cap" {
		t.Fatalf("cap action = %+v", a)
	}
	if a := m.Feed(capFired); a.Interrupt != "" {
		t.Fatalf("a second cap must not interrupt again: %+v", a)
	}
	m = NewMachine(testCfg())
	if a := m.Feed(line(initJSON(map[string]any{"permissionMode": "auto"}))); a.Interrupt != "latch" {
		t.Fatalf("init violation action = %+v", a)
	}
	m = NewMachine(testCfg())
	m.Feed(line(initJSON(nil)))
	m.Feed(line(resultJSON(nil)))
	if a := m.Feed(graceEnd); a.Interrupt != "grace" {
		t.Fatalf("grace action = %+v", a)
	}
	m = NewMachine(testCfg())
	if a := m.Feed(shutdown); a.Interrupt != "shutdown" {
		t.Fatalf("shutdown action = %+v", a)
	}
	if v := m.Verdict(); !v.Decided {
		t.Fatalf("a latched verdict must be decided immediately: %+v", v)
	}
	m = NewMachine(testCfg())
	m.Feed(line(initJSON(nil)))
	if v := m.Verdict(); v.Decided || v.State != "running" {
		t.Fatalf("a live run is not decided: %+v", v)
	}
}

func TestDeniedToolsAreCopiedNotPromoted(t *testing.T) {
	denial := map[string]any{"tool_name": "Write", "tool_use_id": "toolu_01", "tool_input": map[string]any{"file_path": testRoot + "/note.txt", "content": "hello"}}
	v := Decide(testCfg(), []Step{line(initJSON(nil)), line(resultJSON(map[string]any{"permission_denials": []any{denial}})), eof(), exit(0)})
	assertVerdict(t, v, "3", "completed", "", true)
	if len(v.PermissionDenials) != 1 || v.PermissionDenials[0].ToolName != "Write" {
		t.Fatalf("denials = %+v", v.PermissionDenials)
	}
	if v.TerminalReason != "completed" || v.Subtype != "success" || v.ExitClass != "zero" {
		t.Fatalf("recorded fields = %+v", v)
	}
}

func TestExitClasses(t *testing.T) {
	for _, tc := range []struct {
		st   Step
		want string
	}{{exit(0), "zero"}, {exit(1), "nonzero"}, {Step{Kind: StepExit, ExitSignal: "terminated"}, "signal"}, {Step{Kind: StepExit, ExitUnknown: true}, "unknown"}} {
		v := Decide(testCfg(), []Step{line(initJSON(nil)), eof(), tc.st})
		if v.ExitClass != tc.want {
			t.Errorf("%+v: class %q want %q", tc.st, v.ExitClass, tc.want)
		}
	}
}

// Success needs all four conditions: subtype success, is_error false, equal
// session_id, exit code 0 (plus init correlated and exactly one result).
func TestSuccessNeedsAllFour(t *testing.T) {
	good := []Step{line(initJSON(nil)), line(resultJSON(nil)), eof(), exit(0)}
	assertVerdict(t, Decide(testCfg(), good), "3", "completed", "", true)
	foreign := "00000000-0000-4000-8000-000000000000"
	for name, steps := range map[string][]Step{
		"subtype":     {line(initJSON(nil)), line(resultJSON(map[string]any{"subtype": "error_max_turns"})), eof(), exit(0)},
		"is_error":    {line(initJSON(nil)), line(resultJSON(map[string]any{"is_error": true})), eof(), exit(0)},
		"session id":  {line(initJSON(nil)), line(resultJSON(map[string]any{"session_id": foreign})), eof(), exit(0)},
		"exit code":   {line(initJSON(nil)), line(resultJSON(nil)), eof(), exit(1)},
		"exit signal": {line(initJSON(nil)), line(resultJSON(nil)), eof(), {Kind: StepExit, ExitSignal: "killed"}},
		"no init":     {line(resultJSON(nil)), eof(), exit(0)},
		"no eof yet":  {line(initJSON(nil)), line(resultJSON(nil)), exit(0)},
		"no exit yet": {line(initJSON(nil)), line(resultJSON(nil)), eof()},
	} {
		v := Decide(testCfg(), steps)
		if v.State == "completed" {
			t.Errorf("%s: completed with %+v", name, v)
		}
	}
	// The guard is never released before both EOF and the child's exit.
	m := NewMachine(testCfg())
	for _, s := range []Step{line(initJSON(nil)), line(resultJSON(nil))} {
		m.Feed(s)
	}
	if v := m.Verdict(); v.Decided || v.GuardReleased {
		t.Fatalf("decided at the result line: %+v", v)
	}
}

// Escalation exhaustion latches 6b only while no aborted_* result proves the
// interrupt was acknowledged; afterwards the exit grace (6a) is the guard.
func TestEscalationAfterAnAbortedResultIsNot6b(t *testing.T) {
	v := Decide(testCfg(), []Step{line(initJSON(nil)), userInt, line(abortedJSON()), exhausted, eof(), exit(0)})
	assertVerdict(t, v, "5", "interrupted", "", true)
	v = Decide(testCfg(), []Step{line(initJSON(nil)), userInt, line(abortedJSON()), exhausted, graceEnd})
	assertVerdict(t, v, "6a", "unknown", "EXIT_UNCONFIRMED", false)
}
