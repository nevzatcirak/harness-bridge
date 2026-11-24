package execution

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
	"github.com/nevzatcirak/cirak/internal/audit"
)

// ADR-024 S5: the result and refused lines, the latch refusal and the hardening of the S3 reviews
// (#285 B, #286 C). The fakes validate every line with the real audit validator.

func (fx *replyFixture) tctx() (context.Context, *ReplyTrace) {
	ctx, tr := WithReplyTrace(fx.ctx())
	return ctx, tr
}

func TestResultLineForEveryNativeOutcome(t *testing.T) {
	for _, c := range []struct {
		name            string
		native          error
		outcome, code   string
		reason          string
		accepted        bool
		wantAttemptLine int
	}{
		{"replied", nil, "replied", "", "", true, 1},
		{"404 resolved elsewhere", &opencode.HTTPError{Status: 404}, "not_found", "NOT_FOUND", ReasonResolvedElsewhere, false, 1},
		{"400 native rejected the id", &opencode.HTTPError{Status: 400}, "native_rejected_id", "UPSTREAM_FAILURE", ReasonNativeRejectedID, false, 1},
		{"lost response", errors.New("lost"), "unknown_outcome", "UNKNOWN_OUTCOME", "", false, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := newReplyFixture(t)
			if c.native != nil {
				fx.rep.failWith(c.native)
			}
			ctx, tr := fx.tctx()
			res, err := fx.m.ReplyPermission(ctx, fx.in())
			if c.accepted && err != nil {
				t.Fatal(err)
			}
			lines := fx.aw.phaseLines("result")
			if len(lines) != 1 {
				t.Fatalf("result lines = %d, want exactly one", len(lines))
			}
			l := fx.aw.written()[0]
			r := lines[0]
			if r["outcome"] != c.outcome || (r["accepted"] == "true") != c.accepted || r["error_code"] != c.code || r["reason"] != c.reason {
				t.Fatalf("result line = %v", r)
			}
			for _, k := range []string{"attempt", "attempt_id", "decision", "permission_id", "execution_id", "session_id", "native_tool", "kind", "scope_digest", "pattern_count", "principal", "process_start"} {
				if r[k] != l[k] {
					t.Fatalf("result line %s = %q, attempt line has %q", k, r[k], l[k])
				}
			}
			if c.accepted && r["attempt_id"] != res.AttemptID {
				t.Fatal("the result line does not carry the attempt_id returned to the caller")
			}
			// The call already closed with a result line: the generic hook writes nothing more.
			fx.m.AuditReply(ctx, fx.in(), res, err)
			if len(fx.aw.phaseLines("refused")) != 0 || len(fx.aw.phaseLines("result")) != 1 {
				t.Fatal("AuditReply wrote a second line for a call that has its result line")
			}
			if !tr.resultDone || !tr.attemptWriten {
				t.Fatalf("trace = %+v", tr)
			}
		})
	}
}

// ADR-024 4.2 step 9: the native write cannot be unsent. A result line that cannot be written
// leaves the real outcome and the record intact and refuses every later call.
func TestResultLineFailureKeepsTheOutcomeAndRefusesLaterCalls(t *testing.T) {
	fx := newReplyFixture(t)
	fx.aw.mu.Lock()
	fx.aw.phaseErrs = []error{audit.ErrUnavailable}
	fx.aw.mu.Unlock()
	res, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	if err != nil || res.Outcome != "replied" {
		t.Fatalf("the real outcome was not returned: %+v %v", res, err)
	}
	if len(fx.rep.calls()) != 1 {
		t.Fatal("native writes != 1")
	}
	// every later call, including a replay that would send nothing and another permission id, is refused
	for _, in := range []ReplyInput{fx.in(), {ExecutionID: fx.x.ID, PermissionID: "per_other", Decision: "reject", Principal: "local"}} {
		_, err = fx.m.ReplyPermission(fx.ctx(), in)
		if e := wantErr(t, err, "UPSTREAM_FAILURE", ReasonAuditUnavailable); !e.Retryable {
			t.Fatal("audit_unavailable must be retryable")
		}
	}
	if len(fx.rep.calls()) != 1 || len(fx.aw.written()) != 1 {
		t.Fatalf("a refused call reached the native side: writes %d lines %d", len(fx.rep.calls()), len(fx.aw.written()))
	}
	if n := fx.ev.count("read"); n != 1 {
		t.Fatalf("permission reads = %d: a latched refusal reads nothing", n)
	}
}

func TestLatchedWriterRefusesBeforeAnythingElse(t *testing.T) {
	fx := newReplyFixture(t)
	fx.aw.latch()
	for _, in := range []ReplyInput{fx.in(), {ExecutionID: "0123456789abcdef0123456789abcdef", PermissionID: testPerm, Decision: "reject"}} {
		_, err := fx.m.ReplyPermission(fx.ctx(), in)
		wantErr(t, err, "UPSTREAM_FAILURE", ReasonAuditUnavailable)
	}
	fx.noSideEffects("latched")
	if fx.ev.count("read") != 0 {
		t.Fatal("a latched refusal read natively")
	}
}

func TestManagerRefusesAllowOnceBeforeAnyNativeRead(t *testing.T) {
	fx := newReplyFixture(t)
	in := fx.in()
	in.Decision = "allow_once"
	_, err := fx.m.ReplyPermission(fx.ctx(), in)
	wantErr(t, err, "FORBIDDEN", ReasonAllowOnceDisabled)
	if fx.ev.count("read") != 0 {
		t.Fatal("allow_once made a permission read before it was refused")
	}
	fx.noSideEffects("allow_once")
}

func TestRefusedAndReplayLinesCoverEveryReturnPath(t *testing.T) {
	fx := newReplyFixture(t)
	ctx, _ := fx.tctx()
	lines := func(phase string) []audit.Fields { return fx.aw.phaseLines(phase) }
	// 1. refusal before any attempt: one refused line, values reduced to closed shapes
	fx.remove()
	_, err := fx.m.ReplyPermission(ctx, fx.in())
	fx.m.AuditReply(ctx, fx.in(), nil, err)
	if len(lines("refused")) != 1 {
		t.Fatalf("refused lines = %d", len(lines("refused")))
	}
	r := lines("refused")[0]
	if r["outcome"] != "refused" || r["accepted"] != "false" || r["error_code"] != "NOT_FOUND" || r["reason"] != ReasonResolvedElsewhere ||
		r["decision"] != "reject" || r["permission_id"] != testPerm || r["execution_id"] != fx.x.ID || r["session_id"] != "opencode:ses_test" || r["principal"] != "local" {
		t.Fatalf("refused line = %v", r)
	}
	for _, banned := range []string{"attempt", "attempt_id", "native_tool", "kind", "scope_digest", "pattern_count"} {
		if _, ok := r[banned]; ok {
			t.Fatalf("a refused line carries %s", banned)
		}
	}
	// 2. an input that is not even well formed: the line carries only what is closed-shaped
	bad := ReplyInput{ExecutionID: "not-an-execution", PermissionID: "../../etc/passwd", Decision: "always", Supersedes: "x", Principal: "local"}
	fx.m.AuditReply(fx.ctx(), bad, nil, Fail("INVALID_INPUT", "invalid reply_permission input"))
	r = lines("refused")[1]
	for _, k := range []string{"execution_id", "permission_id", "decision", "supersedes"} {
		if _, ok := r[k]; ok {
			t.Fatalf("malformed %s reached the line: %v", k, r)
		}
	}
	if r["error_code"] != "INVALID_INPUT" || r["principal"] != "local" {
		t.Fatalf("line = %v", r)
	}
	// 3. a free-text reason or an unknown error code never reaches the line
	fx.m.AuditReply(fx.ctx(), fx.in(), nil, &Error{Code: "weird code!", Message: "leak /etc/shadow", Reason: "free text reason"})
	r = lines("refused")[2]
	if r["error_code"] != "INTERNAL" && r["error_code"] != "" {
		t.Fatalf("an unknown error code reached the line: %v", r)
	}
	if _, ok := r["reason"]; ok {
		t.Fatalf("a free-text reason reached the line: %v", r)
	}
	for _, v := range r {
		if strings.Contains(v, "shadow") {
			t.Fatalf("native text reached the line: %v", r)
		}
	}
	// 4. allow_once_disabled records the closed policy_rule
	in := fx.in()
	in.Decision = "allow_once"
	fx.m.AuditReply(fx.ctx(), in, nil, refusal("FORBIDDEN", ReasonAllowOnceDisabled, "x", false))
	if r = lines("refused")[3]; r["policy_rule"] != "allow_once_disabled" || r["decision"] != "allow_once" || r["reason"] != ReasonAllowOnceDisabled {
		t.Fatalf("line = %v", r)
	}
	// 5. the recorded UNKNOWN_OUTCOME replay: a refused line that quotes the attempt of the record
	fx.restore()
	fx.rep.failWith(errors.New("lost"))
	_, err = fx.m.ReplyPermission(fx.ctx(), fx.in())
	first := wantErr(t, err, "UNKNOWN_OUTCOME", "")
	_, err = fx.m.ReplyPermission(fx.ctx(), fx.in())
	fx.m.AuditReply(fx.ctx(), fx.in(), nil, err)
	r = lines("refused")[4]
	if r["error_code"] != "UNKNOWN_OUTCOME" || r["attempt_id"] != first.AttemptID || r["attempt"] != "1" {
		t.Fatalf("replayed unknown line = %v", r)
	}
	// 6. the recorded success replay: one result line with outcome replay
	fx2 := newReplyFixture(t)
	res, err := fx2.m.ReplyPermission(fx2.ctx(), fx2.in())
	if err != nil {
		t.Fatal(err)
	}
	again, err := fx2.m.ReplyPermission(fx2.ctx(), fx2.in())
	if err != nil || again != res {
		t.Fatalf("replay = %+v %v", again, err)
	}
	fx2.m.AuditReply(fx2.ctx(), fx2.in(), again, err)
	rl := fx2.aw.phaseLines("result")
	if len(rl) != 2 || rl[1]["outcome"] != "replay" || rl[1]["accepted"] != "true" || rl[1]["attempt_id"] != res.AttemptID {
		t.Fatalf("result lines = %v", rl)
	}
	if len(fx2.aw.written()) != 1 || len(fx2.rep.calls()) != 1 {
		t.Fatal("a replay wrote an attempt line or a native write")
	}
}

func TestRefusedLineIsNotWrittenWhenTheWriterIsMissingOrLatched(t *testing.T) {
	fx := newReplyFixture(t)
	fx.m.SetAttemptWriter(nil)
	fx.m.AuditReply(fx.ctx(), fx.in(), nil, Fail("NOT_FOUND", "x")) // must not panic
	fx.m.SetAttemptWriter(fx.aw)
	fx.aw.latch()
	fx.m.AuditReply(fx.ctx(), fx.in(), nil, Fail("NOT_FOUND", "x"))
	if len(fx.aw.phaseLines("refused")) != 0 {
		t.Fatal("a line was written through a latched writer")
	}
}

// Reviews #285 B P3-5 / #286 C P3-1: a replier that panics leaves no record in_flight forever.
func TestReplierPanicSettlesTheRecordUnknown(t *testing.T) {
	fx := newReplyFixture(t)
	fx.rep.mu.Lock()
	fx.rep.panicOn = true
	fx.rep.mu.Unlock()
	ctx, tr := fx.tctx()
	_, err := fx.m.ReplyPermission(ctx, fx.in())
	first := wantErr(t, err, "UNKNOWN_OUTCOME", "")
	if first.AttemptID == "" || first.Attempt != 1 {
		t.Fatalf("envelope = %+v", first)
	}
	if rl := fx.aw.phaseLines("result"); len(rl) != 1 || rl[0]["outcome"] != "unknown_outcome" {
		t.Fatalf("result lines after a panic = %v", rl)
	}
	if !tr.resultDone {
		t.Fatal("the trace does not know the result line was written")
	}
	// the record is unknown, not in_flight: the same key returns the recorded envelope ...
	_, err = fx.m.ReplyPermission(fx.ctx(), fx.in())
	if e := wantErr(t, err, "UNKNOWN_OUTCOME", ""); e.AttemptID != first.AttemptID {
		t.Fatalf("recorded envelope = %+v", e)
	}
	// ... and an explicit supersede works (it would be SESSION_BUSY forever if the record stayed in_flight)
	fx.rep.mu.Lock()
	fx.rep.panicOn = false
	fx.rep.mu.Unlock()
	in := fx.in()
	in.Supersedes = first.AttemptID
	res, err := fx.m.ReplyPermission(fx.ctx(), in)
	if err != nil || res.Attempt != 2 {
		t.Fatalf("supersede after a panic: %+v %v", res, err)
	}
}

// Reviews #285 B P3-2 / #286 C P3-2: the bounds of the tool, the manager and the audit line agree,
// so no id the tool accepts can turn into INTERNAL at the attempt line.
func TestMaximumLengthPermissionIDReachesTheAttemptLine(t *testing.T) {
	long := "per" + strings.Repeat("a", 125)
	fx := newReplyFixture(t)
	fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission { p.RequestID = long; return p })
	in := fx.in()
	in.PermissionID = long
	res, err := fx.m.ReplyPermission(fx.ctx(), in)
	if err != nil {
		t.Fatalf("a maximum-length permission id: %v", err)
	}
	if got := fx.aw.written()[0]["permission_id"]; got != long || res.PermissionID != long {
		t.Fatalf("attempt line permission_id = %q", got)
	}
	if ok := ValidPermissionID(long + "a"); ok {
		t.Fatal("the tool accepts an id one past the bound")
	}
}

// Review #286 C P3-3: the workspace identity re-check before the write is pinned.
func TestWorkspaceIdentityRecheckBeforeTheWrite(t *testing.T) {
	t.Run("workspace replaced: refused, no write, no attempt line", func(t *testing.T) {
		fx := newReplyFixture(t)
		other, err := os.Stat(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		x, _ := fx.m.find(fx.x.ID)
		// swapped inside the call's own re-read: after the waiting/guard check, before the identity check
		fx.ev.setHook(func() { x.mu.Lock(); x.workspace = other; x.mu.Unlock() })
		_, err = fx.m.ReplyPermission(fx.ctx(), fx.in())
		wantErr(t, err, "FORBIDDEN", "")
		fx.noSideEffects("replaced workspace")
	})
	t.Run("filesystem check times out: retryable refusal, no write, no attempt line", func(t *testing.T) {
		fx := newReplyFixture(t)
		// Blocked inside the call's own re-read (after the waiting/guard check), so the manager's
		// monitor probes cannot move the state first.
		gate := make(chan struct{})
		var restore func()
		fx.ev.setHook(func() {
			restore = SetFSHookForTesting(func(op, path string) { <-gate })
			fx.ev.setHook(nil)
		})
		t.Cleanup(func() {
			close(gate)
			if restore != nil {
				restore()
			}
			waitFSIdle(t)
		})
		within(t, 20*time.Second, "reply_permission", func() {
			_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
			wantTimeout(t, err, "reply_permission")
		})
		fx.noSideEffects("filesystem timeout")
	})
}

// Review #285 B P3-1: a native 401/403/429/5xx may have been processed: UNKNOWN_OUTCOME, never a
// definite refusal, and the record allows exactly one explicit supersede.
func TestNativeAuthAndServerFailuresAreUnknownOutcome(t *testing.T) {
	for _, status := range []int{401, 403, 408, 409, 429, 500, 502, 503, 504} {
		t.Run(strings.TrimSpace(strings.Join([]string{"HTTP", string(rune('0' + status/100)), string(rune('0' + status/10%10)), string(rune('0' + status%10))}, "")), func(t *testing.T) {
			fx := newReplyFixture(t)
			fx.rep.failWith(&opencode.HTTPError{Status: status})
			_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
			e := wantErr(t, err, "UNKNOWN_OUTCOME", "")
			if e.AttemptID == "" {
				t.Fatal("no attempt_id to quote")
			}
			if rl := fx.aw.phaseLines("result"); len(rl) != 1 || rl[0]["outcome"] != "unknown_outcome" {
				t.Fatalf("result lines = %v", rl)
			}
			if len(fx.rep.calls()) != 1 {
				t.Fatal("resent")
			}
		})
	}
}

// Review #285 B P3-3: waiting for the run token past the deadline is a retryable timeout, and
// nothing was sent.
func TestTokenWaitDeadlineIsARetryableTimeout(t *testing.T) {
	fx := newReplyFixture(t)
	entered, hold := fx.rep.arm()
	done := make(chan error, 1)
	go func() { _, err := fx.m.ReplyPermission(fx.ctx(), fx.in()); done <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the first reply never reached the native write")
	}
	// another permission id passes the in-memory record rules and waits for the token the first call holds
	ctx, cancel := context.WithTimeout(fx.ctx(), 100*time.Millisecond)
	defer cancel()
	in := fx.in()
	in.PermissionID = "per_other"
	_, err := fx.m.ReplyPermission(ctx, in)
	wantTimeout(t, err, "token wait")
	if len(fx.rep.calls()) != 1 {
		t.Fatalf("native writes = %d while the second call waited", len(fx.rep.calls()))
	}
	close(hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Review #293 P3-2: every kind the observer path accepts reaches the attempt line; one the Bridge
// does not accept is refused as not verified before any attempt line (fail closed).
func TestKindBoundaryReachesTheAttemptLine(t *testing.T) {
	for _, kind := range []string{"Bash2", "mcp.tool", "mcp:server-tool_1", strings.Repeat("k", 64)} {
		t.Run(kind, func(t *testing.T) {
			fx := newReplyFixture(t)
			fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission { p.Kind = kind; return p })
			if _, err := fx.m.ReplyPermission(fx.ctx(), fx.in()); err != nil {
				t.Fatalf("kind %q: %v", kind, err)
			}
			if got := fx.aw.written()[0]["kind"]; got != kind {
				t.Fatalf("attempt line kind = %q", got)
			}
		})
	}
	for _, kind := range []string{strings.Repeat("k", 65), "a b", "a=b"} {
		fx := newReplyFixture(t)
		fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission { p.Kind = kind; return p })
		_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
		wantErr(t, err, "FORBIDDEN", ReasonNotVerified)
		fx.noSideEffects("kind " + kind)
	}
}

// Reviews #292 P3-1 / #293 P3-3: the result line and the refused/replay lines are written on a
// context that outlives the caller's (a canceled caller does not lose its audit line) and carry a
// bound of their own. A mutation that passes the caller's context makes these fail.
func TestResultLineSurvivesTheCallersCancellation(t *testing.T) {
	for _, c := range []struct {
		name    string
		native  error
		outcome string
	}{
		{"replied", nil, "replied"},
		{"404", &opencode.HTTPError{Status: 404}, "not_found"},
		{"lost response", errors.New("lost"), "unknown_outcome"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := newReplyFixture(t)
			if c.native != nil {
				fx.rep.failWith(c.native)
			}
			ctx, cancel := context.WithCancel(fx.ctx())
			defer cancel()
			fx.rep.mu.Lock()
			fx.rep.after = cancel // the caller goes away as the native write ends
			fx.rep.mu.Unlock()
			_, _ = fx.m.ReplyPermission(ctx, fx.in())
			if ctx.Err() == nil {
				t.Fatal("the caller's context was not canceled")
			}
			rl := fx.aw.phaseLines("result")
			if len(rl) != 1 || rl[0]["outcome"] != c.outcome {
				t.Fatalf("result lines after the caller went away = %v, want one %q", rl, c.outcome)
			}
			fx.aw.mu.Lock()
			defer fx.aw.mu.Unlock()
			if len(fx.aw.bounds) != 1 || fx.aw.bounds[0] <= 0 || fx.aw.bounds[0] > auditWriteTimeout {
				t.Fatalf("the result line write is not bounded by its own %v deadline: %v", auditWriteTimeout, fx.aw.bounds)
			}
		})
	}
}

func TestRefusedAndReplayLinesSurviveTheCallersCancellation(t *testing.T) {
	fx := newReplyFixture(t)
	fx.remove()
	ctx, cancel := context.WithCancel(fx.ctx())
	_, err := fx.m.ReplyPermission(ctx, fx.in())
	cancel() // the caller goes away before the line of its refusal is written
	fx.m.AuditReply(ctx, fx.in(), nil, err)
	rl := fx.aw.phaseLines("refused")
	if len(rl) != 1 || rl[0]["error_code"] != "NOT_FOUND" {
		t.Fatalf("refused lines after the caller went away = %v", rl)
	}
	// the replay of a recorded success
	fx2 := newReplyFixture(t)
	res, err := fx2.m.ReplyPermission(fx2.ctx(), fx2.in())
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(fx2.ctx())
	again, err := fx2.m.ReplyPermission(ctx2, fx2.in())
	cancel2()
	fx2.m.AuditReply(ctx2, fx2.in(), again, err)
	if rl := fx2.aw.phaseLines("result"); len(rl) != 2 || rl[1]["outcome"] != "replay" || rl[1]["attempt_id"] != res.AttemptID {
		t.Fatalf("replay lines after the caller went away = %v", rl)
	}
	for _, f := range []*replyFixture{fx, fx2} {
		f.aw.mu.Lock()
		for _, b := range f.aw.bounds {
			if b <= 0 || b > auditWriteTimeout {
				t.Fatalf("a line write is not bounded by its own %v deadline: %v", auditWriteTimeout, f.aw.bounds)
			}
		}
		f.aw.mu.Unlock()
	}
}
