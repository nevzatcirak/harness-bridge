package execution

import (
	"context"
	"regexp"
	"sync/atomic"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// ADR-024 section 3 (slice S1): additive, report-only exposure of a pending
// native permission request. It grants no ability to reply, changes no state,
// error, unresolved, ended_at, guard or idempotency, and carries no pattern, no
// metadata and no native text.

// PendingPermission describes the one native permission request that is verified
// to belong to THIS execution (observer attribution "verified").
type PendingPermission struct {
	PermissionID string `json:"permission_id"`
	CallID       string `json:"call_id"`
	MessageID    string `json:"message_id"`
	Tool         string `json:"tool"`
	Kind         string `json:"kind"`
	PatternCount int    `json:"pattern_count"`
	ScopeDigest  string `json:"scope_digest"`
	Attribution  string `json:"attribution"`
}

// PermissionSource supplies the permission observer's point-in-time view of one
// native session, routed through the session's own directory/workspace. It may
// perform bounded native reads, so it is called only from the reconcile path,
// never with a run or manager mutex held. A nil source (the default) means no
// request is ever reported as verified: only the count is exposed.
type PermissionSource interface {
	PermissionView(ctx context.Context, session string) (opencode.PermissionObservationBatch, error)
}

type permissionSourceBox struct{ s PermissionSource }
type permissionSourceRef = atomic.Pointer[permissionSourceBox]

// SetPermissionSource installs (or with nil removes) the source used for the
// actionable pending_permission object. The count needs no source.
func (m *Manager) SetPermissionSource(s PermissionSource) {
	if s == nil {
		m.permSource.Store(nil)
		return
	}
	m.permSource.Store(&permissionSourceBox{s})
}

var (
	pendingScopeDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// selectPendingPermission returns the single request that the observer verified
// for this execution, or nil. It fails closed: nil unless the batch is
// "available", and nil when zero or more than one request is verified for the
// execution (the ADR-024 object is singular), when any verified entry has a
// field outside its closed charset, or when the entry is not exactly pending.
// Unattributed, conflicting, tool-less, resolved and uncertain entries are never
// returned.
func selectPendingPermission(b opencode.PermissionObservationBatch, session, executionID string) *PendingPermission {
	if b.Status != "available" || !opencode.ValidID(session, "ses") || executionID == "" {
		return nil
	}
	var found *PendingPermission
	verified := 0
	for _, p := range b.Permissions {
		if p.Attribution != "verified" || p.SessionID != session || p.ExecutionID != executionID {
			continue
		}
		verified++
		if p.State != "pending" || !opencode.ValidID(p.RequestID, "per") || !opencode.ValidID(p.ToolMessageID, "msg") ||
			!safeCallID.MatchString(p.ToolCallID) || !safeTool.MatchString(p.Tool) || !safeTool.MatchString(p.Kind) ||
			!pendingScopeDigest.MatchString(p.ScopeDigest) || p.PatternCount < 0 || p.PatternCount > 64 {
			return nil
		}
		found = &PendingPermission{PermissionID: p.RequestID, CallID: p.ToolCallID, MessageID: p.ToolMessageID, Tool: p.Tool, Kind: p.Kind, PatternCount: p.PatternCount, ScopeDigest: p.ScopeDigest, Attribution: "verified"}
	}
	if verified != 1 {
		return nil
	}
	return found
}

// pendingDetail asks the installed source for the verified request of this
// execution. Any error, a missing source or an unselectable batch yields nil.
func (m *Manager) pendingDetail(ctx context.Context, session, executionID string) *PendingPermission {
	box := m.permSource.Load()
	if box == nil {
		return nil
	}
	batch, err := box.s.PermissionView(ctx, session)
	if err != nil {
		return nil
	}
	return selectPendingPermission(batch, session, executionID)
}

// pendingCount counts the pending native requests of one session in the
// instance-wide list the manager already reads. Unattributed, tool-less and
// conflicting requests are included: the count says only that something waits.
func pendingCount(permissions []opencode.Permission, session string) int {
	n := 0
	for _, p := range permissions {
		if p.SessionID == session {
			n++
		}
	}
	return n
}

func copyPending(p *PendingPermission) *PendingPermission {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

func copyCount(n *int) *int {
	if n == nil {
		return nil
	}
	c := *n
	return &c
}
