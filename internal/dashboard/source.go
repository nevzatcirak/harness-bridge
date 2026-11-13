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
	State   string // idle|running|waiting_for_permission|unknown
	Guard   Guard
	// CurrentExecution is the ID of the newest non-terminal execution, if any.
	CurrentExecution string
	LastActivity     time.Time
}

// SessionDetail is one session with its executions, newest first.
type SessionDetail struct {
	Session
	Executions []Execution
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
