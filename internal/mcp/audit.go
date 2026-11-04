package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"regexp"

	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// Audit lines for mutating tools (AUDIT-01). Exactly one line is written per
// send_prompt or interrupt_execution call outcome. Keys are stable; see
// docs/CONTRACTS.md "Mutation audit log". Prompt text, history, result text,
// tokens, headers and raw idempotency keys are never logged.

// auditRecord collects what call() learns about one mutating call.
type auditRecord struct {
	tool     string
	readOnly bool
	// replay is the execution manager's own decision (SendWithRunnerOutcome).
	replay bool
}

var auditExecutionPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func mutating(name string) bool { return name == "send_prompt" || name == "interrupt_execution" }

// auditPrincipal is the configured remote operator; loopback-token mode has none.
func (s *Server) auditPrincipal() string {
	if s.Remote != nil {
		return s.Remote.Principal()
	}
	return "local"
}

// idemTag is a non-reversible correlator: the first 12 hex characters of
// sha256(idempotency_key). The raw key is never logged.
func idemTag(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// auditSession returns the global session ID only for a well-formed input.
func auditSession(raw string) string {
	if id, e := native(raw); e == nil {
		return "opencode:" + id
	}
	return ""
}

// auditExecution returns an execution ID only in the manager's token format, so
// arbitrary client input is never echoed into the journal.
func auditExecution(id string) string {
	if auditExecutionPattern.MatchString(id) {
		return id
	}
	return ""
}

func (s *Server) audit(r *auditRecord, a args, out any, err error) {
	event, outcome := "prompt_submission", "new"
	if r.tool == "interrupt_execution" {
		event, outcome = "interrupt", "accepted"
	}
	sessionID, executionID := auditSession(a.SessionID), auditExecution(a.ExecutionID)
	if x, ok := out.(execution.Execution); ok {
		if v := auditSession(x.SessionID); v != "" {
			sessionID = v
		}
		if v := auditExecution(x.ID); v != "" {
			executionID = v
		}
	}
	if err == nil && r.replay {
		outcome = "replay"
	}
	attrs := []any{"event", event, "tool", r.tool}
	if err != nil {
		outcome = "rejected"
	}
	attrs = append(attrs, "outcome", outcome, "accepted", err == nil)
	if err != nil {
		attrs = append(attrs, "error_code", envelope(err).Code)
	}
	if sessionID != "" {
		attrs = append(attrs, "session_id", sessionID)
	}
	if executionID != "" {
		attrs = append(attrs, "execution_id", executionID)
	}
	if event == "prompt_submission" {
		if tag := idemTag(a.IdempotencyKey); tag != "" {
			attrs = append(attrs, "idem", tag)
		}
	}
	attrs = append(attrs, "principal", s.auditPrincipal())
	message := "prompt submission"
	switch {
	case r.readOnly:
		message = "read-only mutation denied"
	case event == "interrupt":
		message = "explicit interrupt"
	}
	slog.Info(message, attrs...)
}
