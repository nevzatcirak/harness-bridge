package claudecode

import "testing"

// ADR-021 §3 row 3 reads "init correlated, THEN exactly one result". A correlated
// result that arrives before a valid init never reaches rows 3, 4 or 5.
func TestResultBeforeInitIsNeverTerminalProof(t *testing.T) {
	succ, abort := line(resultJSON(nil)), line(abortedJSON())
	maxTurns := line(resultJSON(map[string]any{"subtype": "error_max_turns", "is_error": true, "terminal_reason": "max_turns"}))
	cases := []struct {
		name  string
		steps []Step
	}{
		{"success result, valid init, EOF, exit 0 (review #219 probe)", []Step{succ, line(initJSON(nil)), eof(), exit(0)}},
		{"aborted result + user interrupt, then init", []Step{userInt, abort, line(initJSON(nil)), eof(), exit(0)}},
		{"aborted result, user interrupt after, then init", []Step{abort, userInt, line(initJSON(nil)), eof(), exit(0)}},
		{"failure result, valid init, exit 1 is not row 4", []Step{maxTurns, line(initJSON(nil)), eof(), exit(1)}},
		{"success result, valid init, exit 1 is not row 7 either", []Step{succ, line(initJSON(nil)), eof(), exit(1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Decide(testCfg(), tc.steps)
			assertVerdict(t, v, "10", "unknown", "", false)
			if v.Detail != "result_before_init" {
				t.Fatalf("detail = %q", v.Detail)
			}
		})
	}
	// Controls: the normal order is unchanged, and the no-init NOT_FOUND shape (row 2) still works.
	assertVerdict(t, Decide(testCfg(), []Step{line(initJSON(nil)), succ, eof(), exit(0)}), "3", "completed", "", true)
	assertVerdict(t, Decide(testCfg(), []Step{line(notFoundJSON(testUUID)), eof(), exit(1)}), "2", "failed", "", true)
	// A violating init after a result is still row 0 (the latch wins).
	assertVerdict(t, Decide(testCfg(), []Step{succ, line(initJSON(map[string]any{"permissionMode": "auto"})), eof(), exit(0)}), "0", "unknown", "IDENTITY_MISMATCH", false)
}

// Row 5 (and the row 4 SIGINT shape, and the 6b condition) accept only
// terminal_reason aborted_tools or aborted_streaming, as ADR row 5 says.
func TestOnlyTheTwoAbortedReasonsAreAborted(t *testing.T) {
	other := line(resultJSON(map[string]any{"subtype": "error_during_execution", "is_error": true, "terminal_reason": "aborted_other", "num_turns": 2}))
	v := Decide(testCfg(), []Step{line(initJSON(nil)), userInt, other, eof(), exit(0)})
	assertVerdict(t, v, "10", "unknown", "", false)
	v = Decide(testCfg(), []Step{line(initJSON(nil)), other, eof(), exit(0)})
	assertVerdict(t, v, "10", "unknown", "", false) // no interrupt: not the row-4 SIGINT shape either
	// 6b still latches: an aborted_other result is NOT an acknowledgement.
	v = Decide(testCfg(), []Step{line(initJSON(nil)), userInt, other, exhausted})
	assertVerdict(t, v, "6b", "unknown", "ABORT_UNCONFIRMED", false)
	for _, reason := range []string{"aborted_tools", "aborted_streaming"} {
		r := line(resultJSON(map[string]any{"subtype": "error_during_execution", "is_error": true, "terminal_reason": reason}))
		assertVerdict(t, Decide(testCfg(), []Step{line(initJSON(nil)), userInt, r, eof(), exit(0)}), "5", "interrupted", "", true)
	}
}

// A failed wait leaves nothing to signal reliably: row 6a is latched with
// child_unconfirmed, no grace interrupt is requested and no reason recorded.
func TestExitStatusUnavailableAfterAResultSendsNoInterrupt(t *testing.T) {
	m := NewMachine(testCfg())
	m.Feed(line(initJSON(nil)))
	m.Feed(line(resultJSON(nil)))
	m.Feed(eof())
	if a := m.Feed(Step{Kind: StepExit, ExitUnknown: true}); a.Interrupt != "" {
		t.Fatalf("interrupt requested after a failed wait: %+v", a)
	}
	v := m.Verdict()
	assertVerdict(t, v, "6a", "unknown", "EXIT_UNCONFIRMED", false)
	if !v.ChildUnconfirmed || len(v.InterruptReasons) != 0 || v.Detail != "exit_status_unavailable" {
		t.Fatalf("verdict = %+v", v)
	}
}
