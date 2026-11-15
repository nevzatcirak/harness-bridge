package mcp

import (
	"context"

	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// replyPermissionCall is ADR-024 4.2 steps 2 and 3 for reply_permission; steps 4-9 are
// execution.Manager.ReplyPermission. BRIDGE_READ_ONLY (step 1) was already refused in call:
// the tool is in mutating(), so a read-only Bridge reads nothing natively.
func (s *Server) replyPermissionCall(ctx context.Context, a args) (any, error) {
	if !s.PermissionRelay.enabled() || s.Runs == nil {
		return nil, execution.Fail("INVALID_INPUT", "unknown tool")
	}
	// Step 2: needs only configuration and input, so it makes zero native calls. reject_only
	// refuses allow_once always; there is no policy mode in this slice (D15 Q1).
	if a.Decision != "reject" {
		return nil, &execution.Error{Code: "FORBIDDEN", Message: "allow_once is disabled on this bridge", Reason: execution.ReasonAllowOnceDisabled}
	}
	// ADR-024 section 7: the relay is not possible for Claude Code.
	if s.Claude != nil && s.Claude.Owns(a.ExecutionID) {
		return nil, execution.Fail("INVALID_INPUT", "reply_permission accepts only OpenCode executions")
	}
	// Step 3: the execution exists in this writer and its session is authorized (ADR-012 ACL
	// and workspace), exactly as for the other execution tools.
	x, e := s.Runs.Snapshot(ctx, a.ExecutionID)
	if e != nil {
		return nil, e
	}
	id, e := native(x.SessionID)
	if e != nil {
		return nil, e
	}
	meta, _, e := s.authorized(ctx, id)
	if e != nil {
		return nil, e
	}
	if e = s.Runs.ValidateSession(ctx, a.ExecutionID, meta); e != nil {
		return nil, e
	}
	res, e := s.Runs.ReplyPermission(ctx, execution.ReplyInput{ExecutionID: a.ExecutionID, PermissionID: a.PermissionID, Decision: a.Decision, Supersedes: a.Supersedes, Principal: s.auditPrincipal()})
	return res, e
}
