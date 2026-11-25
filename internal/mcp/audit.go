package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync/atomic"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/cirak/internal/execution"
)

// Audit lines for mutating tools (AUDIT-01). Exactly one line is written per
// send_prompt or interrupt_execution call outcome. Keys are stable; see
// docs/CONTRACTS.md "Mutation audit log". Prompt text, history, result text,
// tokens, headers and raw idempotency keys are never logged.

// auditRecord collects what call() learns about one mutating call.
type auditRecord struct {
	tool     string
	readOnly bool
	// replyTrace is set for reply_permission only: what the manager wrote for this call.
	replyTrace *execution.ReplyTrace
	replyCtx   context.Context
	// replay is the execution manager's own decision (SendWithRunnerOutcome).
	replay bool
}

var auditExecutionPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// mutating names the tools that mutate a native session. The set gates CIRAK_READ_ONLY (call)
// and the AUDIT-01 middleware. reply_permission (ADR-024 S3) is in it: a tool missing here would
// be neither blocked in read-only mode nor audited.
func mutating(name string) bool {
	return name == "send_prompt" || name == "interrupt_execution" || name == "reply_permission"
}

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
	if id, ok := claudeCanonical(raw); ok {
		return id
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
		if method != "tools/call" || !ok || call.Params == nil || !(mutating(call.Params.Name) || call.Params.Name == "create_session" || isArchiveTool(call.Params.Name)) {
			return next(ctx, method, req)
		}
		mark := &auditMark{}
		res, err := next(context.WithValue(ctx, auditMarkKey{}, mark), method, req)
		if !mark.written.Load() {
			rejected := execution.Fail("INVALID_INPUT", "arguments rejected before the tool handler")
			if call.Params.Name == "create_session" {
				s.auditCreate(ctx, nil, rawAuditArgs(call.Params.Arguments), nil, rejected)
			} else if isArchiveTool(call.Params.Name) {
				s.auditArchive(ctx, &archiveEvidence{rootIdx: -1, tool: call.Params.Name}, rawAuditArgs(call.Params.Arguments), nil, rejected)
			} else {
				s.audit(ctx, &auditRecord{tool: call.Params.Name}, rawAuditArgs(call.Params.Arguments), nil, rejected)
			}
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
	return args{SessionID: str("session_id"), ExecutionID: str("execution_id"), IdempotencyKey: str("idempotency_key"),
		PermissionID: str("permission_id"), Decision: str("decision"), Supersedes: str("supersedes")}
}

func (s *Server) audit(ctx context.Context, r *auditRecord, a args, out any, err error) {
	if mark, ok := ctx.Value(auditMarkKey{}).(*auditMark); ok {
		mark.written.Store(true)
	}
	if r.tool == "reply_permission" {
		// ADR-024 section 6 (S5): the permission_reply lines are written by internal/audit
		// through the process-wide failure-reporting writer, never by this generic writer (whose
		// event would be a prompt submission). The attempt line is written before the native call
		// and the result line after it, both by the manager; this hook writes the one line of every
		// other return path (a refused line, or a replay result line). It is the single place every
		// path passes through, including a read-only denial and an input the SDK rejected.
		if s.Runs != nil {
			ctx := ctx
			if r.replyCtx != nil {
				ctx = r.replyCtx
			}
			s.Runs.AuditReply(ctx, execution.ReplyInput{ExecutionID: a.ExecutionID, PermissionID: a.PermissionID, Decision: a.Decision, Supersedes: a.Supersedes, Principal: s.auditPrincipal()}, out, err)
		}
		return
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
		if _, ok := claudeCanonical(sessionID); ok {
			attrs = append(attrs, "harness", "claudecode")
		}
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

// auditCreate writes the one AUDIT-01 line of a create_session call (ADR-022 4.3), on every
// return path: event=session_create, outcome (created, replay, rejected), accepted, error_code,
// root_id (the index of the configured root, never the path), session_id only when well formed,
// key_hash (the first 16 hex characters of sha256(idempotency_key), the same value as the hb_key
// metadata hint; never the raw key, and none for a key outside 8 to 128 bytes, whose hash would
// reveal it), limit_state (counts at the end of the call: hour, root, total, "?" when not
// established), bootstrap_may_be_outside_root and replay (only when true), principal. The title,
// the subdirectory text, the prompt and every native response text are never logged.
func (s *Server) auditCreate(ctx context.Context, ev *createEvidence, a args, out any, err error) {
	if mark, ok := ctx.Value(auditMarkKey{}).(*auditMark); ok {
		mark.written.Store(true)
	}
	if ev == nil {
		ev = &createEvidence{rootIdx: -1}
	}
	outcome := "created"
	switch {
	case err != nil:
		outcome = "rejected"
	case ev.replay:
		outcome = "replay"
	}
	attrs := []any{"event", "session_create", "tool", "create_session", "outcome", outcome, "accepted", err == nil}
	if err != nil {
		attrs = append(attrs, "error_code", envelope(err).Code)
	}
	if ev.rootIdx >= 0 {
		attrs = append(attrs, "root_id", ev.rootIdx)
	}
	sessionID := ""
	if o, ok := out.(CreateOutput); ok {
		sessionID = auditSession(o.SessionID)
	} else if ev.sessionID != "" {
		sessionID = auditSession(ev.sessionID)
	}
	if sessionID != "" {
		attrs = append(attrs, "session_id", sessionID)
	}
	if n := len(a.IdempotencyKey); n >= 8 && n <= 128 {
		sum := sha256.Sum256([]byte(a.IdempotencyKey))
		attrs = append(attrs, "key_hash", hex.EncodeToString(sum[:])[:16])
	}
	if ev.hourKnown {
		count := func(n int, known bool) string {
			if !known {
				return "?"
			}
			return fmt.Sprint(n)
		}
		attrs = append(attrs, "limit_state", fmt.Sprintf("hour:%d/%d,root:%s/%d,total:%s/%d",
			ev.hour, ev.hourMax, count(ev.rootN, ev.counted), ev.rootMax, count(ev.totalN, ev.counted), ev.totalMax))
	}
	var ce *createError
	if errors.As(err, &ce) && ce.BootstrapMayBeOutsideRoot {
		attrs = append(attrs, "bootstrap_may_be_outside_root", true)
	}
	if ev.replay && err != nil {
		attrs = append(attrs, "replay", true)
	}
	attrs = append(attrs, "principal", s.auditPrincipal())
	slog.Info("session create", attrs...)
}

// auditArchive writes the one AUDIT-01 line of an archive_session, unarchive_session or
// close_session call (ADR-025 4.6), on every return path: event (session_archive,
// session_unarchive, session_close), tool, outcome, accepted, error_code, session_id only when
// well formed, root_id (the index of the configured root, never the path), the closed counters
// overlay_generation and overlay_entries after the call, archived_in_window, interrupt
// (close_session only), principal. No title, no path, no free text, never the file contents.
func (s *Server) auditArchive(ctx context.Context, ev *archiveEvidence, a args, out any, err error) {
	if mark, ok := ctx.Value(auditMarkKey{}).(*auditMark); ok {
		mark.written.Store(true)
	}
	if ev == nil {
		ev = &archiveEvidence{rootIdx: -1}
	}
	event := map[string]string{"archive_session": "session_archive", "unarchive_session": "session_unarchive", "close_session": "session_close"}[ev.tool]
	outcome := "rejected"
	if err == nil {
		switch {
		case ev.tool == "unarchive_session" && ev.changed:
			outcome = "unarchived"
		case ev.tool == "unarchive_session":
			outcome = "not_archived"
		case ev.tool == "close_session" && ev.changed:
			outcome = "closed"
		case ev.changed:
			outcome = "archived"
		default:
			outcome = "already_archived"
		}
	}
	attrs := []any{"event", event, "tool", ev.tool, "outcome", outcome, "accepted", err == nil}
	if err != nil {
		attrs = append(attrs, "error_code", envelope(err).Code)
		if r := envelope(err).Reason; r != "" {
			attrs = append(attrs, "reason", r)
		}
	}
	sessionID := ""
	if o, ok := out.(ArchiveOutput); ok {
		sessionID = auditSession(o.SessionID)
	} else if ev.sessionID != "" {
		sessionID = auditSession(ev.sessionID)
	} else {
		sessionID = auditSession(a.SessionID)
	}
	if sessionID != "" {
		attrs = append(attrs, "session_id", sessionID)
	}
	if ev.rootIdx >= 0 {
		attrs = append(attrs, "root_id", ev.rootIdx)
	}
	if s.Archive != nil && s.Archive.store != nil {
		attrs = append(attrs, "overlay_generation", s.Archive.store.Generation(), "overlay_entries", s.Archive.store.Entries())
	}
	if ev.window {
		attrs = append(attrs, "archived_in_window", ev.inWindow)
	}
	if ev.tool == "close_session" && ev.interrupt != "" {
		attrs = append(attrs, "interrupt", ev.interrupt)
	}
	attrs = append(attrs, "principal", s.auditPrincipal())
	slog.Info("session lifecycle", attrs...)
}
