package mcp

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// maxDashTurns bounds the turns one detail read returns (the newest are kept; the
// history response itself is bounded by the client at 8 MiB). The dashboard shows
// fewer (maxTurnsShown); this is the Source-side ceiling.
const maxDashTurns = 1000

// DashboardSource is the production, read-only data source of the ADR-023 dashboard for
// OpenCode sessions (slice 5a). It reuses the MCP discovery pipeline of this Server over
// a derived ACL, so the list equals what list_sessions shows, and reads turns and diffs
// with the same directory-scoped read-only client calls the tools use. It makes no
// mutating call, runs no git (ADR-023 section 1.3), and never calls the native harness
// for a claudecode: session (ADR-021 is pending; D17 Q3).
//
// archive may be nil (nothing archived, like EmptyArchiveOverlay).
func (s *Server) DashboardSource(archive ArchiveOverlay) dashboard.Source {
	d := &dashSource{runs: s.Runs, archive: archive, claude: map[string]bool{}}
	d.srv = &Server{Client: s.Client, Runs: s.Runs, ReadOnly: true}
	if s.Policy != nil {
		p := *s.Policy
		p.Sessions = map[string]bool{}
		for id, allowed := range s.Policy.Sessions {
			if !allowed {
				continue
			}
			if _, err := claudecode.ParseSessionID(id); err == nil {
				// A Claude Code ID never reaches the OpenCode client: it is listed as a
				// pending row only.
				d.claude[strings.TrimPrefix(id, "claudecode:")] = true
				continue
			}
			p.Sessions[id] = true
		}
		d.srv.Policy = &p
	}
	return d
}

type dashSource struct {
	srv     *Server // derived: OpenCode ACL only, own discovery slots
	runs    *execution.Manager
	archive ArchiveOverlay
	claude  map[string]bool // claudecode: IDs without the prefix
}

func nonTerminal(state string) bool {
	return state != "completed" && state != "failed" && state != "interrupted"
}

// view is one read-only snapshot of the manager: executions by harness-qualified session
// ID, newest first, and the guard table.
type dashView struct {
	bySession map[string][]execution.Execution
	guards    map[string]execution.GuardView
}

func (d *dashSource) snapshot() dashView {
	v := dashView{bySession: map[string][]execution.Execution{}, guards: map[string]execution.GuardView{}}
	if d.runs == nil {
		return v
	}
	snap := d.runs.DashboardSnapshot()
	for _, e := range snap.Executions { // oldest first
		v.bySession[e.SessionID] = append(v.bySession[e.SessionID], e)
	}
	for k, list := range v.bySession {
		for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
			list[i], list[j] = list[j], list[i]
		}
		v.bySession[k] = list
	}
	v.guards = snap.Guards
	return v
}

func (v dashView) row(harness, id, qualified string) dashboard.Session {
	r := dashboard.Session{Harness: harness, ID: id, State: "unknown"}
	if g, ok := v.guards[qualified]; ok {
		r.Guard = dashboard.Guard{Held: true, State: g.State}
	}
	for _, e := range v.bySession[qualified] {
		if nonTerminal(e.State) {
			r.CurrentExecution = e.ID
			break
		}
	}
	return r
}

func (d *dashSource) openCodeRow(v dashView, m Session, native opencode.Session) dashboard.Session {
	r := v.row("opencode", native.ID, "opencode:"+native.ID)
	r.Title, r.Root, r.State = m.Title, m.Directory, m.State
	if t, ok := native.UpdatedAt(); ok {
		r.LastActivity = t
	}
	return r
}

// Sessions lists the ACL-authorized OpenCode sessions exactly as list_sessions discovers
// them (sorted by ID), then the pending claudecode: rows sorted by ID.
func (d *dashSource) Sessions(ctx context.Context) ([]dashboard.Session, error) {
	all, err := d.srv.discoverNative(ctx)
	if err != nil {
		return nil, err
	}
	v := d.snapshot()
	out := make([]dashboard.Session, 0, len(all)+len(d.claude))
	for _, x := range all {
		if d.archived(ctx, "opencode:"+x.native.ID) {
			continue
		}
		out = append(out, d.openCodeRow(v, x.view, x.native))
	}
	ids := make([]string, 0, len(d.claude))
	for id := range d.claude {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, v.row("claudecode", id, "claudecode:"+id))
	}
	return out, nil
}

func (d *dashSource) archived(ctx context.Context, id string) bool {
	if d.archive == nil {
		return false
	}
	archived, err := d.archive.Archived(ctx, id)
	return err == nil && archived // an unknown answer hides nothing in a read-only view
}

// notListed maps the denials that mean "not in this dashboard" to ErrNotFound.
func notListed(err error) error {
	switch envelope(err).Code {
	case "FORBIDDEN", "NOT_FOUND", "INVALID_INPUT":
		return dashboard.ErrNotFound
	}
	return err
}

// authorizedOpenCode resolves an OpenCode session only if the ACL lists it; an unlisted
// or invalid ID makes no native call.
func (d *dashSource) authorizedOpenCode(ctx context.Context, harness, id string) (opencode.Session, *opencode.Client, error) {
	if harness != "opencode" || !opencode.ValidID(id, "ses") || d.srv.Policy == nil || !d.srv.Policy.Sessions[id] {
		return opencode.Session{}, nil, dashboard.ErrNotFound
	}
	v, c, err := d.srv.authorized(ctx, id)
	if err != nil {
		return v, nil, notListed(err)
	}
	return v, c, nil
}

// SessionDetail returns one listed session with its executions, newest first.
func (d *dashSource) SessionDetail(ctx context.Context, harness, id string) (dashboard.SessionDetail, error) {
	snap := d.snapshot()
	if harness == "claudecode" {
		if !d.claude[id] {
			return dashboard.SessionDetail{}, dashboard.ErrNotFound
		}
		return detail(snap, snap.row("claudecode", id, "claudecode:"+id), "claudecode:"+id), nil
	}
	v, _, err := d.authorizedOpenCode(ctx, harness, id)
	if err != nil {
		return dashboard.SessionDetail{}, err
	}
	if d.archived(ctx, "opencode:"+id) {
		return dashboard.SessionDetail{}, dashboard.ErrNotFound
	}
	m := d.srv.describe(ctx, v, nil)
	return detail(snap, d.openCodeRow(snap, m, v), "opencode:"+id), nil
}

func detail(v dashView, r dashboard.Session, qualified string) dashboard.SessionDetail {
	out := dashboard.SessionDetail{Session: r}
	for _, e := range v.bySession[qualified] {
		x := dashboard.Execution{ID: e.ID, State: e.State, Started: e.Started, Updated: e.Updated, Ended: e.Ended, Current: e.ID == r.CurrentExecution}
		out.Executions = append(out.Executions, x)
	}
	return out
}

func turnState(s opencode.DiffState) dashboard.TurnState {
	switch s {
	case opencode.DiffComputedEmpty:
		return dashboard.TurnComputedEmpty
	case opencode.DiffChanges:
		return dashboard.TurnChanges
	}
	return dashboard.TurnNotComputed
}

// SessionTurns lists the user turns of an OpenCode session, newest last, from the native
// history (read only).
func (d *dashSource) SessionTurns(ctx context.Context, harness, id string) ([]dashboard.Turn, error) {
	_, c, err := d.authorizedOpenCode(ctx, harness, id)
	if err != nil {
		return nil, err
	}
	msgs, err := c.History(ctx, id)
	if err != nil {
		return nil, notListed(err)
	}
	turns := []dashboard.Turn{}
	for _, m := range msgs {
		if m.Info.Role != "user" {
			continue
		}
		turns = append(turns, dashboard.Turn{MessageID: m.Info.ID, Created: time.UnixMilli(m.Info.Time.Created), State: turnState(m.Info.DiffState())})
	}
	if len(turns) > maxDashTurns {
		turns = turns[len(turns)-maxDashTurns:]
	}
	return turns, nil
}

func diffResult(files []opencode.FileDiff) dashboard.DiffResult {
	out := dashboard.DiffResult{Files: make([]dashboard.DiffFile, 0, len(files))}
	for _, f := range files {
		x := dashboard.DiffFile{Status: f.Status, Additions: f.Additions, Deletions: f.Deletions}
		if f.File != nil {
			x.Path, x.HasPath = *f.File, true
		}
		if f.Patch != nil {
			x.Patch, x.HasPatch = *f.Patch, true
		}
		out.Files = append(out.Files, x)
	}
	return out
}

// TurnDiff reads GET /session/:id/diff?messageID=... of one turn.
func (d *dashSource) TurnDiff(ctx context.Context, harness, id, messageID string) (dashboard.DiffResult, error) {
	_, c, err := d.authorizedOpenCode(ctx, harness, id)
	if err != nil {
		return dashboard.DiffResult{}, err
	}
	files, err := c.SessionDiff(ctx, id, messageID)
	if err != nil {
		return dashboard.DiffResult{}, notListed(err)
	}
	return diffResult(files), nil
}

// WorkingTreeDiff reads the native GET /vcs/diff?mode=git of the session's approved
// directory (D17 Q2, Q9). The Bridge runs no git.
func (d *dashSource) WorkingTreeDiff(ctx context.Context, harness, id string) (dashboard.DiffResult, error) {
	_, c, err := d.authorizedOpenCode(ctx, harness, id)
	if err != nil {
		return dashboard.DiffResult{}, err
	}
	files, err := c.VCSDiff(ctx, opencode.VCSModeGit, -1)
	if err != nil {
		return dashboard.DiffResult{}, notListed(err)
	}
	return diffResult(files), nil
}
