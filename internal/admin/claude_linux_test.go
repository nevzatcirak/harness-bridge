//go:build linux

package admin

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode/runner"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// ADR-021 slice 4: the admin channel for a claudecode: session. The OpenCode path and its
// tests are unchanged.

const claudeSession1 = "claudecode:00000000-0000-4000-8000-000000000001"

type stubClaude struct {
	mu    sync.Mutex
	calls []string
	opts  []runner.ReleaseOptions
	ev    runner.ReleaseEvidence
	err   error
}

func (s *stubClaude) ReleaseGuard(_ context.Context, session, id string, opt runner.ReleaseOptions) (runner.ReleaseEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, session+" "+id)
	s.opts = append(s.opts, opt)
	return s.ev, s.err
}

func claudeRequest() Request {
	return Request{Op: OpReleaseGuard, SessionID: claudeSession1, ExecutionID: testExecution, Ticket: "OPS-77"}
}

// Default off: with no Claude releaser (the Executor is not constructed in production) a
// claudecode: session is refused with a clear reason, with exactly one audit line, and the
// OpenCode releaser is never called.
func TestClaudeSessionIsRefusedWhileNoExecutorExists(t *testing.T) {
	stub := &stubReleaser{}
	h := start(t, os.Geteuid(), stub)
	r := call(t, h, claudeRequest())
	refused(t, r, "INVALID_INPUT")
	if r.Error.Message == "" || r.SessionID != claudeSession1 || r.ClaudeEvidence != nil {
		t.Fatalf("response %+v", r)
	}
	if stub.count() != 0 {
		t.Fatal("the OpenCode releaser was called for a claudecode: session")
	}
	wantFields(t, onlyLine(t, h), map[string]any{"outcome": "refused", "accepted": false, "error_code": "INVALID_INPUT", "harness": "claudecode",
		"session_id": claudeSession1, "execution_id": testExecution, "ticket": "OPS-77", "principal": principal, "native_state": nil})
}

func TestClaudeReleaseHappyPathWritesOneAuditLine(t *testing.T) {
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	cl := &stubClaude{ev: runner.ReleaseEvidence{Harness: "claudecode", Row: "6", ExitClass: "signal", ChildExited: true, GroupEmpty: true, Rechecked: true, SettleMS: 5000, GuardReleasedAt: &at}}
	oc := &stubReleaser{}
	h := startWith(t, oc, func(s *Server) { s.Claude = cl })
	r := call(t, h, claudeRequest())
	if r.Outcome != "released" || r.ClaudeEvidence == nil || r.Evidence != nil || !r.ClaudeEvidence.Rechecked || r.ClaudeEvidence.NativeRead {
		t.Fatalf("response %+v", r)
	}
	if len(cl.calls) != 1 || cl.calls[0] != claudeSession1+" "+testExecution || oc.count() != 0 {
		t.Fatalf("calls %v / opencode %d", cl.calls, oc.count())
	}
	wantFields(t, onlyLine(t, h), map[string]any{
		"msg": "guard release", "event": "guard_release", "tool": "admin", "harness": "claudecode", "outcome": "released", "accepted": true,
		"error_code": nil, "session_id": claudeSession1, "execution_id": testExecution, "child_exited": true, "group_empty": true, "rechecked": true,
		"row": "6", "exit_class": "signal", "settle_ms": float64(5000), "ticket": "OPS-77", "principal": principal,
		"native_state": nil, "unfinished": nil, "permission_pending": nil, "correlated_user": nil, "allow_missing_user_message": nil,
	})
}

func TestClaudeRefusalCarriesTheReasonAndOneAuditLine(t *testing.T) {
	cl := &stubClaude{ev: runner.ReleaseEvidence{Harness: "claudecode", ChildExited: false}, err: &execution.Error{Code: "SESSION_BUSY", Message: "child exit not confirmed"}}
	h := startWith(t, &stubReleaser{}, func(s *Server) { s.Claude = cl })
	r := call(t, h, claudeRequest())
	refused(t, r, "SESSION_BUSY")
	if r.ClaudeEvidence == nil || r.ClaudeEvidence.ChildExited {
		t.Fatalf("response %+v", r)
	}
	wantFields(t, onlyLine(t, h), map[string]any{"outcome": "refused", "accepted": false, "error_code": "SESSION_BUSY", "child_exited": false, "harness": "claudecode"})
}

func TestClaudeRequestValidation(t *testing.T) {
	cl := &stubClaude{}
	h := startWith(t, &stubReleaser{}, func(s *Server) { s.Claude = cl })
	for _, mod := range map[string]func(*Request){
		"uppercase uuid": func(r *Request) { r.SessionID = "claudecode:4E59C1BD-4333-4A71-875D-CBA9301BD4A9" },
		"short uuid":     func(r *Request) { r.SessionID = "claudecode:1234" },
		"trailing text":  func(r *Request) { r.SessionID = claudeSession1 + "x" },
		"bad execution":  func(r *Request) { r.ExecutionID = "xyz" },
		"override flag":  func(r *Request) { r.AllowMissingUserMessage = true },
		"bad ticket":     func(r *Request) { r.Ticket = "has space" },
	} {
		req := claudeRequest()
		mod(&req)
		refused(t, call(t, h, req), "INVALID_INPUT")
	}
	if len(cl.calls) != 0 {
		t.Fatalf("an invalid request reached the releaser: %v", cl.calls)
	}
	if n := len(h.logs.lines(t)); n != 6 {
		t.Fatalf("%d audit lines for 6 refused requests", n)
	}
}

// The settle interval is passed through, and an OpenCode request is untouched.
func TestClaudeSettlePassThroughAndOpenCodeUnchanged(t *testing.T) {
	cl := &stubClaude{}
	oc := &stubReleaser{}
	h := startWith(t, oc, func(s *Server) { s.Claude = cl; s.Settle = 123 * time.Millisecond })
	_ = call(t, h, claudeRequest())
	if cl.opts[0].Settle != 123*time.Millisecond {
		t.Fatalf("settle = %v", cl.opts[0].Settle)
	}
	r := call(t, h, validRequest())
	if r.Outcome != "released" && r.Outcome != "refused" {
		t.Fatalf("response %+v", r)
	}
	if oc.count() != 1 || len(cl.calls) != 1 {
		t.Fatalf("opencode %d claude %d", oc.count(), len(cl.calls))
	}
}
