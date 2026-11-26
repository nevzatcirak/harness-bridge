package dashboard

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is what a Source returns for a session it does not list (not in
// the ACL, or not known). The handler answers 404 with a fixed text and never
// echoes the requested identifier.
var ErrNotFound = errors.New("dashboard: session not found")

// Guard is the Bridge busy guard of one session (ADR-023 section 1.1). It is
// display data: State is the state of the execution that holds it.
type Guard struct {
	Held  bool
	State string
}

// Execution is one execution of a session as the detail view shows it.
type Execution struct {
	ID      string
	State   string
	Started time.Time
	Updated time.Time
	Ended   *time.Time
	// Current marks the newest non-terminal execution of the session.
	Current bool
	// SessionID is the harness-qualified session ID the execution belongs to ("opencode:ses_..."), copied from the
	// Manager. The /executions page builds its session link by splitting it at the first colon; an ID the routes
	// would refuse renders without a link. Display data: the session detail does not need it and leaves it empty.
	SessionID string
	// Liveness is the ADR-017 report block the Bridge already holds for the execution (A1.8): display data, a
	// copy, never a verdict. nil when the Bridge holds none.
	Liveness *Liveness
	// Permission summarises the pending native permission requests of a waiting execution (ADR-024, A1.8): the
	// count and, only when the Bridge attributes one to the execution, the fields ADR-024 already shows Chat.
	// It never carries a pattern or native text. nil when nothing is pending.
	Permission *PermissionSummary
}

// Liveness is the ADR-017 (A1 and A2) report-only block of one execution, copied from the Manager's record. Every
// word is a closed word of that ADR; a word this package does not know is shown as "Unknown", never as received.
type Liveness struct {
	// NativeStatus is busy, retry, absent or unavailable.
	NativeStatus string
	// RetryAttempt and RetryNextAt accompany a native retry.
	RetryAttempt *int64
	RetryNextAt  *time.Time
	// LastNativeProgressAt is the latest native progress timestamp.
	LastNativeProgressAt *time.Time
	// StallSuspected is carried for completeness; the page keys on StallReason, not on this flag (review #459 P2-1).
	StallSuspected bool
	StallReason    string
	// DegradationReason is ADR-017 A2 Variant A: PROVIDER_RETRYING, PROVIDER_RETRY_OVERDUE or PROVIDER_ERROR.
	DegradationReason string
	RetryOverdue      bool
	// CauseHint is one of network, rate_limit, auth, server, unknown; CauseSource is structured, bounded_text or none.
	CauseHint, CauseSource string
	// Tool is the oldest live running tool (its name is untrusted display text); Explanation is TOOL_RUNNING_LONG
	// once that tool has run for ToolLongS seconds or more.
	Tool        *LivenessTool
	Explanation string
	// Thresholds are the operator thresholds actually applied (A2.6), nil for a block built without them.
	Thresholds *LivenessThresholds
	// ObservedAt is the Bridge time of the probe that produced the block.
	ObservedAt time.Time
}

// LivenessTool is the running-tool object of a liveness block.
type LivenessTool struct {
	Name         string
	RunningSince time.Time
}

// LivenessThresholds are the A2.6 thresholds in seconds.
type LivenessThresholds struct {
	NoProgressS int
	ToolLongS   int
}

// PermissionSummary is the pending-permission summary of one execution.
type PermissionSummary struct {
	// Count is the number of pending native permission requests the Bridge sees for the session.
	Count int
	// Attributed is true only when the Bridge verified that one request belongs to this execution (ADR-024); only
	// then are Tool, Kind, PatternCount and Digest set. The patterns themselves never reach the dashboard.
	Attributed   bool
	Tool, Kind   string
	PatternCount int
	Digest       string
}

// Session is one row the dashboard may show.
//
// Q6 (D17): the scope is every ACL-authorized session plus, later, the ADR-022
// lifecycle roots. The lifecycle roots are NOT wired yet: no source supplies
// them (TODO(ADR-022 lifecycle roots, after its slice S2)), and a Source must
// not invent them. Q7 (D17): archived sessions are hidden by default with a
// filter; no archive overlay exists yet (ADR-022 S8, S6), so there is no
// filter and no archived row in this slice.
type Session struct {
	Harness string // "opencode" or "claudecode"
	ID      string // the harness-native session ID
	Title   string
	Root    string
	// LifecycleRoot is the ADR-022 lifecycle root that contains the session directory, as a
	// LABEL only (D17 Q6): it widens nothing. Empty when no lifecycle root contains it.
	LifecycleRoot string
	// Archived marks an overlay-archived session (D17 Q7); such sessions are listed only
	// when the request asks for them (WithArchived).
	Archived bool
	State    string // idle|running|waiting_for_permission|unknown
	Guard    Guard
	// CurrentExecution is the ID of the newest non-terminal execution, if any.
	CurrentExecution string
	LastActivity     time.Time
}

// SessionDetail is one session with its executions, newest first.
type SessionDetail struct {
	Session
	Executions []Execution
}

// Health is what the About and health page shows (ADR-023 A1.4 item 6, A1.8): facts about the process and the
// outcome of the last session-list read. It has no secret: the connector token's EXPIRY is a timestamp, the token
// itself, the Host and the origin never reach it. A field that is not known is left out, never defaulted.
type Health struct {
	// Version is the version of the binary; Build is its VCS revision ("" when the binary carries none).
	Version, Build string
	// TokenExpires is the connector token's expiry in remote mode; nil in local mode (ADR-019: no runtime expiry).
	TokenExpires *time.Time
	// Features are the on/off settings the page lists, one row each.
	Features []Feature
	// Reach is the reachability of each harness, taken from the outcome of the last session-list read: no extra
	// native probe is made for it.
	Reach []Reachability
}

// Feature is one on/off setting of the Bridge: lifecycle, permission relay, Claude Code.
type Feature struct {
	Name string
	On   bool
}

// Reachability is the outcome of the last session-list read of one harness: "reachable", "unreachable", or "not
// read" when no read has happened yet (or the harness is not read at all). At is the time of that read.
type Reachability struct {
	Harness string
	State   string
	At      time.Time
}

// Source is the dashboard's only path to data, and it is read-only: every
// method is a reader and none can mutate (ADR-023 T11). A test pins the method
// set. Nothing in this package depends on the execution manager or the MCP
// server; a Source implementation lives outside it.
type Source interface {
	Sessions(ctx context.Context) ([]Session, error)
	// SessionDetail returns ErrNotFound for a session that is not listed.
	SessionDetail(ctx context.Context, harness, id string) (SessionDetail, error)
	// SessionTurns lists the user turns of a session, newest last, with the presence
	// state of each native info.summary.diffs. ErrNotFound for an unlisted session.
	SessionTurns(ctx context.Context, harness, id string) ([]Turn, error)
	// TurnDiff reads the per-turn diff of one user message, on demand. It is called
	// only for a turn whose state is TurnChanges. ErrNotFound for an unlisted session.
	TurnDiff(ctx context.Context, harness, id, messageID string) (DiffResult, error)
	// WorkingTreeDiff reads the native working-tree diff (/vcs/diff) of the session's
	// approved root: the default source (D17 Q2, Q9). The Bridge runs no git for it.
	WorkingTreeDiff(ctx context.Context, harness, id string) (DiffResult, error)
	// Executions lists the executions of the sessions in the SAME scope set the Sessions list produces (the ACL
	// scope of D17 Q6, the archived flag of D17 Q7 read from the request context), BEFORE the Sessions page's
	// 200-row display cap, newest first, then capped at 200 AFTER the filter (A1.4 item 3, A1.8). An execution
	// whose session is not in that set is dropped, never shown. It makes no native read beyond the discovery
	// the Sessions list already makes.
	Executions(ctx context.Context) ([]Execution, error)
	// Health reports the process facts and the reachability taken from the last session-list read (A1.8). It
	// makes no native call.
	Health(ctx context.Context) (Health, error)
}

// NullSource is the source used when no data source is connected.
type NullSource struct{}

// Sessions returns no rows.
func (NullSource) Sessions(context.Context) ([]Session, error) { return nil, nil }

// SessionDetail finds nothing.
func (NullSource) SessionDetail(context.Context, string, string) (SessionDetail, error) {
	return SessionDetail{}, ErrNotFound
}

// SessionTurns finds nothing.
func (NullSource) SessionTurns(context.Context, string, string) ([]Turn, error) {
	return nil, ErrNotFound
}

// TurnDiff finds nothing.
func (NullSource) TurnDiff(context.Context, string, string, string) (DiffResult, error) {
	return DiffResult{}, ErrNotFound
}

// WorkingTreeDiff finds nothing.
func (NullSource) WorkingTreeDiff(context.Context, string, string) (DiffResult, error) {
	return DiffResult{}, ErrNotFound
}

// Executions lists nothing.
func (NullSource) Executions(context.Context) ([]Execution, error) { return nil, nil }

// Health knows nothing: no data source is connected.
func (NullSource) Health(context.Context) (Health, error) { return Health{}, nil }

type archivedKey struct{}

// WithArchived marks a request context as asking for archived sessions too (D17 Q7: hidden by
// default, with a filter). The handler sets it from the query "archived=1"; a Source reads it
// with ArchivedShown. It is a filter, never an authority: a Source still lists only what the
// existing authorization lists.
func WithArchived(ctx context.Context) context.Context {
	return context.WithValue(ctx, archivedKey{}, true)
}

// ArchivedShown reports whether the request asked for archived sessions.
func ArchivedShown(ctx context.Context) bool {
	v, _ := ctx.Value(archivedKey{}).(bool)
	return v
}
