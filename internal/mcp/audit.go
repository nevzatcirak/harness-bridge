package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"regexp"
	"sync/atomic"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
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

// auditMark records that the tool handler wrote the call's audit line, so the
// receiving middleware never writes a second one.
type auditMark struct{ written atomic.Bool }
type auditMarkKey struct{}

// auditMiddleware is SDK receiving middleware (go-sdk v1.8.0
// Server.AddReceivingMiddleware). The SDK validates tool arguments against the
// input schema inside the tool wrapper, before the typed handler runs, and
// returns an IsError result without calling it (AUDIT-01b). Such mutating calls
// get exactly one rejected line here; calls that reach the handler are audited
// there and skipped here.
func (s *Server) auditMiddleware(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		call, ok := req.(*sdk.CallToolRequest)
		if method != "tools/call" || !ok || call.Params == nil || !mutating(call.Params.Name) {
			return next(ctx, method, req)
		}
		mark := &auditMark{}
		res, err := next(context.WithValue(ctx, auditMarkKey{}, mark), method, req)
		if !mark.written.Load() {
			s.audit(ctx, &auditRecord{tool: call.Params.Name}, rawAuditArgs(call.Params.Arguments), nil, execution.Fail("INVALID_INPUT", "arguments rejected before the tool handler"))
		}
		return res, err
	}
}

// rawAuditArgs extracts only the identifiers audit may log from untrusted raw
// arguments: string values of session_id, execution_id and idempotency_key.
// audit still format-checks the IDs and only hashes the key. The prompt and
// any non-string value are dropped.
func rawAuditArgs(raw json.RawMessage) args {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return args{}
	}
	str := func(key string) string {
		var v string
		if json.Unmarshal(fields[key], &v) != nil {
			return ""
		}
		return v
	}
	return args{SessionID: str("session_id"), ExecutionID: str("execution_id"), IdempotencyKey: str("idempotency_key")}
}

func (s *Server) audit(ctx context.Context, r *auditRecord, a args, out any, err error) {
	if mark, ok := ctx.Value(auditMarkKey{}).(*auditMark); ok {
		mark.written.Store(true)
	}
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
