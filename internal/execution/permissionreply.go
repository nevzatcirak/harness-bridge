package execution

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"strconv"
	"sync/atomic"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/audit"
)

// ADR-024 slice S3: reply_permission with decision reject. This file is the whole decision
// logic below the MCP layer: the memory-only record keyed by (execution_id, permission_id), the
// same-key and supersedes rules (4.3), the binding checks and fresh re-read (4.2 steps 3-5), the
// attempt record before the one native write (step 8). It sends nothing but reply "reject" with
// no message, never retries, and never releases a guard or decides an execution's outcome.
//
// Not in this slice: allow_once and any policy (S2/S4), the phase=result and phase=refused lines
// (S5), a production native client (the tool is not registered in the production server).

// Closed values of Error.Reason (ADR-024 section 4). Any other value is a defect.
const (
	ReasonNotVerified           = "not_verified"
	ReasonUnrecognizedKind      = "unrecognized_kind"
	ReasonAllowOnceDisabled     = "allow_once_disabled"
	ReasonPolicyTool            = "policy_tool"
	ReasonPolicyPattern         = "policy_pattern"
	ReasonPolicyScope           = "policy_scope"
	ReasonPolicyRate            = "policy_rate"
	ReasonAttemptInFlight       = "attempt_in_flight"
	ReasonAlreadyReplied        = "already_replied"
	ReasonNothingToSupersede    = "nothing_to_supersede"
	ReasonSupersedesMismatch    = "supersedes_mismatch"
	ReasonSupersedeLimit        = "supersede_limit"
	ReasonSupersedeAllowOnceNo  = "supersede_allow_once_not_offered"
	ReasonResolvedElsewhere     = "resolved_elsewhere_or_dropped"
	ReasonAuditUnavailable      = "audit_unavailable"
	ReasonNativeRejectedID      = "native_rejected_id"
	decisionReject              = "reject"
	decisionAllowOnce           = "allow_once"
	policyModeRejectOnly        = "reject_only"
	policyRuleRejectAllowed     = "reject_allowed"
	stateInFlight, stateRefused = "in_flight", "refused"
	stateReplied, stateUnknown  = "replied", "unknown"
	decidedByChatClient         = "chat_client"
	nativeReplyReject           = "reject"
	maxSupersede                = 2
)

var (
	// permissionIDPattern is the closed pattern of ADR-024 section 4 (native IDs start with per).
	permissionIDPattern = regexp.MustCompile(`^per[A-Za-z0-9_-]{1,125}$`)
	// attemptIDPattern is the 22-character base64url value of 16 random bytes.
	attemptIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
)

// ValidPermissionID and ValidAttemptID expose the two closed input patterns to the MCP layer.
func ValidPermissionID(s string) bool { return permissionIDPattern.MatchString(s) }
func ValidAttemptID(s string) bool    { return attemptIDPattern.MatchString(s) }

// PermissionReplier performs the one native reply write, routed through the session's own
// directory/workspace exactly like PermissionSource. Nil (the default) means no reply can be
// sent: the call fails closed before the attempt record is written.
type PermissionReplier interface {
	ReplyPermission(ctx context.Context, session, requestID, reply string) error
}

// AttemptWriter is the failure-reporting attempt record (internal/audit.Writer in production).
// The error contract is the writer's: any non-nil error means the line was not written.
type AttemptWriter interface {
	Write(ctx context.Context, f audit.Fields) error
}

type replierBox struct{ r PermissionReplier }
type replierRef = atomic.Pointer[replierBox]
type attemptBox struct{ w AttemptWriter }
type attemptRef = atomic.Pointer[attemptBox]

// SetPermissionReplier installs (or with nil removes) the native reply seam.
func (m *Manager) SetPermissionReplier(r PermissionReplier) {
	if r == nil {
		m.replier.Store(nil)
		return
	}
	m.replier.Store(&replierBox{r})
}

// SetAttemptWriter installs (or with nil removes) the attempt writer. Production passes
// audit.Default(), the one process-wide writer and latch.
func (m *Manager) SetAttemptWriter(w AttemptWriter) {
	if w == nil {
		m.attempts.Store(nil)
		return
	}
	m.attempts.Store(&attemptBox{w})
}

// ReplyInput is the validated tool input plus the audit principal.
type ReplyInput struct {
	ExecutionID  string
	PermissionID string
	Decision     string
	Supersedes   string
	Principal    string
}

// ReplyResult is the success output (ADR-024 section 4). DecidedBy and HumanVerified are in
// every result so no client can present the answer as verified.
type ReplyResult struct {
	ExecutionID   string `json:"execution_id"`
	PermissionID  string `json:"permission_id"`
	Decision      string `json:"decision"`
	Outcome       string `json:"outcome"`
	AttemptID     string `json:"attempt_id"`
	Attempt       int    `json:"attempt"`
	DecidedBy     string `json:"decided_by"`
	HumanVerified bool   `json:"human_verified"`
}

type replyKey struct{ execution, permission string }

// replyRecord is memory-only (ADR-010) and empty after a restart.
type replyRecord struct {
	decision  string
	attemptID string
	attempt   int
	state     string
}

// AuditUnavailable is the typed refusal when the attempt record could not be written (ADR-024
// 4.2 step 8): UPSTREAM_FAILURE, reason audit_unavailable, retryable. CONTRACTS has no
// local-failure code, so this is the nearest existing one (a proposal).
func AuditUnavailable() *Error {
	return &Error{Code: "UPSTREAM_FAILURE", Message: "audit record could not be written; no native write was made", Retryable: true, Reason: ReasonAuditUnavailable}
}

func refusal(code, reason, message string, retryable bool) *Error {
	return &Error{Code: code, Message: message, Retryable: retryable, Reason: reason}
}

func newAttemptID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (r *replyRecord) result(in ReplyInput) ReplyResult {
	return ReplyResult{ExecutionID: in.ExecutionID, PermissionID: in.PermissionID, Decision: r.decision, Outcome: "replied", AttemptID: r.attemptID, Attempt: r.attempt, DecidedBy: decidedByChatClient}
}

// unknownOutcome is the recorded UNKNOWN_OUTCOME envelope: the only error that carries
// attempt_id and attempt, so a caller always has the value to quote in supersedes.
func (r *replyRecord) unknownOutcome(message string) *Error {
	return &Error{Code: "UNKNOWN_OUTCOME", Message: message, AttemptID: r.attemptID, Attempt: r.attempt}
}

// gate runs the in-memory refusals of ADR-024 4.2 step 4 and 4.3 against the record. It reads
// only the Bridge's own record: no permission read, no native write. When it returns a result
// the call is a same-key replay and sends nothing.
func (m *Manager) gate(in ReplyInput) (*ReplyResult, error) {
	key := replyKey{in.ExecutionID, in.PermissionID}
	m.replyMu.Lock()
	defer m.replyMu.Unlock()
	rec := m.replies[key]
	if in.Supersedes == "" {
		if rec == nil || rec.state == stateRefused {
			return nil, nil
		}
		switch rec.state {
		case stateReplied:
			if rec.decision != in.Decision {
				return nil, &Error{Code: "IDEMPOTENCY_CONFLICT", Message: "this permission request was already answered with a different decision"}
			}
			res := rec.result(in)
			return &res, nil
		default: // unknown, or in_flight (an overlapping call): the recorded outcome, nothing sent
			return nil, rec.unknownOutcome("an earlier reply for this request has an unknown outcome; nothing was sent; quote attempt_id in supersedes to make one explicit reject attempt")
		}
	}
	// supersedes is honored only from state unknown (4.3 rule 0).
	switch {
	case rec == nil || rec.state == stateRefused:
		return nil, refusal("INVALID_INPUT", ReasonNothingToSupersede, "nothing to supersede: a refused call sent nothing, retry without supersedes", false)
	case rec.state == stateInFlight:
		return nil, refusal("SESSION_BUSY", ReasonAttemptInFlight, "an earlier attempt for this request has not settled; try again once it has", true)
	case rec.state == stateReplied:
		return nil, refusal("IDEMPOTENCY_CONFLICT", ReasonAlreadyReplied, "this permission request was already answered", false)
	case in.Supersedes != rec.attemptID:
		return nil, refusal("INVALID_INPUT", ReasonSupersedesMismatch, "supersedes is not the attempt_id of the unknown attempt", false)
	case rec.attempt >= maxSupersede:
		return nil, refusal("FORBIDDEN", ReasonSupersedeLimit, "at most one superseding attempt; the operator answers natively", false)
	case rec.decision == decisionAllowOnce:
		return nil, refusal("FORBIDDEN", ReasonSupersedeAllowOnceNo, "a superseding attempt is offered for reject only; the operator answers natively", false)
	case rec.decision != in.Decision:
		return nil, Fail("INVALID_INPUT", "a superseding attempt must carry the same decision as the unknown attempt")
	}
	return nil, nil
}

// waitingGuard is ADR-024 4.2 step 3, second half: the execution is waiting for permission and
// its session guard is held by THIS execution. In-memory only.
func (m *Manager) waitingGuard(x *run) error {
	x.mu.Lock()
	state, session, id := x.view.State, x.native, x.view.ID
	x.mu.Unlock()
	m.mu.Lock()
	held := m.busy[session] == id
	m.mu.Unlock()
	if state != "waiting_for_permission" || !held {
		return refusal("NOT_FOUND", ReasonResolvedElsewhere, "no pending permission request for this execution", false)
	}
	return nil
}

// ReplyPermission answers one pending native permission request of one execution with reject.
// The caller (the MCP layer) has authenticated, refused allow_once and authorized the session;
// everything from the execution state on is here, in the order of ADR-024 4.2.
func (m *Manager) ReplyPermission(ctx context.Context, in ReplyInput) (ReplyResult, error) {
	if !permissionIDPattern.MatchString(in.PermissionID) || (in.Decision != decisionReject && in.Decision != decisionAllowOnce) ||
		(in.Supersedes != "" && !attemptIDPattern.MatchString(in.Supersedes)) {
		return ReplyResult{}, Fail("INVALID_INPUT", "invalid reply_permission input")
	}
	x, err := m.find(in.ExecutionID)
	if err != nil {
		return ReplyResult{}, err
	}
	if err = m.waitingGuard(x); err != nil {
		return ReplyResult{}, err
	}
	if res, err := m.gate(in); err != nil || res != nil {
		return derefReply(res), err
	}
	if !m.beginRead() {
		return ReplyResult{}, Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	defer m.wg.Done()
	bounded, cancel := m.readContext(ctx)
	defer cancel()
	// Serialize with prompt, interrupt, reconcile and the ADR-020 release: the run's own token.
	if err = take(bounded, m.reconcileSlots); err != nil {
		return ReplyResult{}, err
	}
	defer give(m.reconcileSlots)
	if err = take(bounded, x.op); err != nil {
		return ReplyResult{}, err
	}
	defer give(x.op)
	// Everything above was read before waiting for the token: check again, a concurrent call
	// may have settled or created the record, and the state may have moved.
	if err = m.waitingGuard(x); err != nil {
		return ReplyResult{}, err
	}
	if res, err := m.gate(in); err != nil || res != nil {
		return derefReply(res), err
	}
	return m.replyLocked(ctx, bounded, x, in)
}

func derefReply(r *ReplyResult) ReplyResult {
	if r == nil {
		return ReplyResult{}
	}
	return *r
}

// replyLocked runs steps 5, 7, 8 with the run token held and the gate passed.
func (m *Manager) replyLocked(ctx, bounded context.Context, x *run, in ReplyInput) (ReplyResult, error) {
	x.mu.Lock()
	session := x.native
	x.mu.Unlock()
	// Step 5: a fresh native re-read through the permission observer, never a cached view.
	entry, err := m.verifyPending(bounded, session, in)
	if err != nil {
		return ReplyResult{}, err
	}
	// Step 7: directory identity recheck immediately before the write.
	if err = revalidateSession(bounded, x, opencode.Session{ID: x.native, Directory: x.directory}); err != nil {
		if errors.Is(err, errFSTimeout) {
			return ReplyResult{}, fsUnavailable("workspace filesystem check did not complete; reply not submitted")
		}
		return ReplyResult{}, err
	}
	if in.Decision != decisionReject {
		// Defense in depth: the MCP layer refuses allow_once before any native call (step 2).
		// S4 (policy) is not started; nothing but reject is ever sent.
		return ReplyResult{}, refusal("FORBIDDEN", ReasonAllowOnceDisabled, "allow_once is disabled", false)
	}
	rbox := m.replier.Load()
	abox := m.attempts.Load()
	if rbox == nil {
		return ReplyResult{}, Fail("UPSTREAM_FAILURE", "native reply unavailable; no native write was made")
	}
	if abox == nil {
		return ReplyResult{}, AuditUnavailable()
	}
	// Step 8: the attempt record, written BEFORE the native call. The record is in_flight first
	// (4.3), so an overlapping call is refused without a native write.
	id, err := newAttemptID()
	if err != nil {
		return ReplyResult{}, Fail("INTERNAL", "attempt id unavailable")
	}
	attempt := 1
	if in.Supersedes != "" {
		attempt = 2
	}
	key := replyKey{in.ExecutionID, in.PermissionID}
	m.replyMu.Lock()
	prev := m.replies[key]
	var restore *replyRecord
	if prev != nil {
		c := *prev
		restore = &c
	}
	m.replies[key] = &replyRecord{decision: in.Decision, attemptID: id, attempt: attempt, state: stateInFlight}
	m.replyMu.Unlock()
	settle := func(state string) {
		m.replyMu.Lock()
		defer m.replyMu.Unlock()
		if r := m.replies[key]; r != nil && r.attemptID == id {
			r.state = state
		}
	}
	// A failed attempt write sent nothing. Attempt 1: the record is refused. Attempt 2: the
	// earlier unknown attempt is restored, so a failed supersede cannot reopen a fresh first
	// attempt for a request whose first reply may have landed.
	abandon := func() {
		m.replyMu.Lock()
		defer m.replyMu.Unlock()
		if r := m.replies[key]; r != nil && r.attemptID == id {
			if restore != nil && restore.state == stateUnknown {
				m.replies[key] = restore
			} else {
				r.state = stateRefused
			}
		}
	}
	fields := audit.Fields{
		"tool": "reply_permission", "attempt": strconv.Itoa(attempt), "attempt_id": id, "decision": in.Decision,
		"process_start": m.processStart, "permission_id": in.PermissionID, "execution_id": in.ExecutionID,
		"session_id": "opencode:" + session, "native_tool": entry.Tool, "kind": entry.Kind, "scope_digest": entry.ScopeDigest,
		"pattern_count": strconv.Itoa(entry.PatternCount), "policy_mode": policyModeRejectOnly, "policy_rule": policyRuleRejectAllowed,
		"principal": in.Principal,
	}
	if in.Supersedes != "" {
		fields["supersedes"] = in.Supersedes
	}
	if werr := abox.w.Write(bounded, fields); werr != nil {
		abandon()
		if errors.Is(werr, audit.ErrInvalid) {
			return ReplyResult{}, Fail("INTERNAL", "attempt record rejected its own fields; no native write was made")
		}
		return ReplyResult{}, AuditUnavailable()
	}
	// The one native write: reply reject, no message, under its own 5 s bound.
	wctx, wcancel := m.readContext(ctx)
	defer wcancel()
	werr := rbox.r.ReplyPermission(wctx, session, in.PermissionID, nativeReplyReject)
	if werr == nil {
		settle(stateReplied)
		return ReplyResult{ExecutionID: in.ExecutionID, PermissionID: in.PermissionID, Decision: in.Decision, Outcome: "replied", AttemptID: id, Attempt: attempt, DecidedBy: decidedByChatClient}, nil
	}
	var h *opencode.HTTPError
	if errors.As(werr, &h) {
		// A definite refusal of attempt 1 leaves nothing outstanding: the record is refused. For
		// attempt 2 the unknown first attempt may have landed (a 404 is exactly what a landed
		// reply looks like), so the record stays unknown at attempt 2: the next same-key call is
		// the recorded UNKNOWN_OUTCOME, never a fresh first attempt, and no third write is possible.
		definite := stateRefused
		if attempt == maxSupersede {
			definite = stateUnknown
		}
		switch h.Status {
		case 404: // first-wins: the request is gone (the TUI answered first, or it was dropped)
			settle(definite)
			return ReplyResult{}, refusal("NOT_FOUND", ReasonResolvedElsewhere, "the permission request was resolved elsewhere or dropped", false)
		case 400:
			settle(definite)
			return ReplyResult{}, refusal("UPSTREAM_FAILURE", ReasonNativeRejectedID, "the native side rejected the permission id; no state changed", false)
		}
	}
	// A timeout, a lost response or any other failure: the write may have landed. Never resend.
	settle(stateUnknown)
	return ReplyResult{}, (&replyRecord{attemptID: id, attempt: attempt}).unknownOutcome("the native reply has an unknown outcome and was not resent; quote attempt_id in supersedes to make one explicit reject attempt")
}

// verifyPending is ADR-024 4.2 step 5: the request with id == permission_id must exist, belong
// to this execution's native session, carry attribution "verified" for this execution (a
// tool-less request is never verified) and be exactly pending.
func (m *Manager) verifyPending(ctx context.Context, session string, in ReplyInput) (opencode.ObservedPermission, error) {
	box := m.permSource.Load()
	if box == nil {
		return opencode.ObservedPermission{}, Fail("UPSTREAM_FAILURE", "permission observation unavailable")
	}
	batch, err := box.s.PermissionView(ctx, session)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, opencode.ErrPermissionCanceled) {
			return opencode.ObservedPermission{}, &Error{Code: "UPSTREAM_TIMEOUT", Message: "permission observation did not complete", Retryable: true}
		}
		return opencode.ObservedPermission{}, Fail("UPSTREAM_FAILURE", "permission observation unavailable")
	}
	if batch.Status != "available" {
		return opencode.ObservedPermission{}, Fail("UPSTREAM_FAILURE", "permission observation unavailable")
	}
	var found []opencode.ObservedPermission
	for _, p := range batch.Permissions {
		if p.RequestID == in.PermissionID {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return opencode.ObservedPermission{}, refusal("NOT_FOUND", ReasonResolvedElsewhere, "the permission request was resolved elsewhere or dropped", false)
	case 1:
	default:
		return opencode.ObservedPermission{}, refusal("FORBIDDEN", ReasonNotVerified, "the permission request is not verified for this execution", false)
	}
	p := found[0]
	if p.SessionID != session || p.Attribution != "verified" || p.ExecutionID != in.ExecutionID {
		return opencode.ObservedPermission{}, refusal("FORBIDDEN", ReasonNotVerified, "the permission request is not verified for this execution", false)
	}
	switch p.State {
	case "pending":
	case "resolved_externally":
		return opencode.ObservedPermission{}, refusal("NOT_FOUND", ReasonResolvedElsewhere, "the permission request was resolved elsewhere or dropped", false)
	default:
		return opencode.ObservedPermission{}, Fail("UPSTREAM_FAILURE", "permission observation unavailable")
	}
	if !verifiedFieldsOK(p, session) {
		return opencode.ObservedPermission{}, refusal("FORBIDDEN", ReasonNotVerified, "the permission request is not verified for this execution", false)
	}
	return p, nil
}

// verifiedFieldsOK holds the closed charsets the attempt record and the exposure share
// (pendingpermission.go): a verified entry outside them is not trusted.
func verifiedFieldsOK(p opencode.ObservedPermission, session string) bool {
	return opencode.ValidID(session, "ses") && opencode.ValidID(p.RequestID, "per") && opencode.ValidID(p.ToolMessageID, "msg") &&
		safeCallID.MatchString(p.ToolCallID) && safeTool.MatchString(p.Tool) && safeTool.MatchString(p.Kind) &&
		pendingScopeDigest.MatchString(p.ScopeDigest) && p.PatternCount >= 0 && p.PatternCount <= 64
}
