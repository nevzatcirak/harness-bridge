package mcp

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/claudecode"
	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
	"github.com/nevzatcirak/cirak/internal/dashboard"
	"github.com/nevzatcirak/cirak/internal/execution"
	"github.com/nevzatcirak/cirak/internal/security"
	"github.com/nevzatcirak/cirak/internal/version"
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
// archive may be nil: the Server's catalog overlay when the lifecycle catalog is configured,
// otherwise nothing is archived (EmptyArchiveOverlay). When the catalog is configured its
// lifecycle roots label the rows (D17 Q6): the label widens nothing, the rows are still exactly
// the ACL-authorized sessions (the rule of list_catalog, ADR-022 6.1).
func (s *Server) DashboardSource(archive ArchiveOverlay) dashboard.Source {
	d := &dashSource{runs: s.Runs, archive: archive, claude: map[string]bool{}, remote: s.Remote,
		features: []dashboard.Feature{{Name: "Lifecycle", On: s.Catalog != nil}, {Name: "Permission relay", On: s.PermissionRelay != nil}, {Name: "Claude Code", On: s.Claude != nil}}}
	if s.Catalog != nil {
		d.roots = s.Catalog.Roots
		if d.archive == nil {
			d.archive = s.Catalog.overlay()
		}
	}
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
	roots   *security.LifecycleRoots // nil: no lifecycle configuration, no label
	claude  map[string]bool          // claudecode: IDs without the prefix

	// What Health reports (ADR-023 A1.8): the Bridge's own settings, fixed at construction, and the outcome of the
	// last session-list read, which every Sessions call records. Health makes no native call of its own.
	remote   *security.Remote
	features []dashboard.Feature
	mu       sync.Mutex
	read     struct {
		at   time.Time
		seen bool
		ok   bool
	}
}

func (d *dashSource) noteRead(err error) {
	d.mu.Lock()
	d.read.at, d.read.seen, d.read.ok = time.Now(), true, err == nil
	d.mu.Unlock()
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
			r.Liveness = dashLiveness(e.Liveness)
			r.UnavailableSince = copyTimePtr(e.UnavailableSince)
			break
		}
	}
	return r
}

func copyTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

func (d *dashSource) openCodeRow(v dashView, m Session, native opencode.Session) dashboard.Session {
	r := v.row("opencode", native.ID, "opencode:"+native.ID)
	r.Title, r.Root, r.State = m.Title, m.Directory, m.State
	r.LifecycleRoot = d.roots.RootOf(m.Directory)
	if t, ok := native.UpdatedAt(); ok {
		r.LastActivity = t
	}
	return r
}

// Sessions lists the ACL-authorized OpenCode sessions exactly as list_sessions discovers
// them (sorted by ID), then the pending claudecode: rows sorted by ID.
func (d *dashSource) Sessions(ctx context.Context) ([]dashboard.Session, error) {
	all, err := d.srv.discoverNative(ctx)
	d.noteRead(err)
	if err != nil {
		return nil, err
	}
	v := d.snapshot()
	out := make([]dashboard.Session, 0, len(all)+len(d.claude))
	for _, x := range all {
		archived, err := d.archivedState(ctx, "opencode:"+x.native.ID)
		if err != nil {
			return nil, err
		}
		if archived && !dashboard.ArchivedShown(ctx) {
			continue
		}
		row := d.openCodeRow(v, x.view, x.native)
		row.Archived = archived
		out = append(out, row)
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

// archivedState reads the overlay. An error means the answer is unknown: it fails the call, as
// list_catalog does, so a session is never listed or hidden by a guess.
func (d *dashSource) archivedState(ctx context.Context, id string) (bool, error) {
	if d.archive == nil {
		return false, nil
	}
	return d.archive.Archived(ctx, id)
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
	archived, err := d.archivedState(ctx, "opencode:"+id)
	if err != nil {
		return dashboard.SessionDetail{}, err
	}
	if archived && !dashboard.ArchivedShown(ctx) {
		return dashboard.SessionDetail{}, dashboard.ErrNotFound
	}
	m := d.srv.describe(ctx, v, nil)
	row := d.openCodeRow(snap, m, v)
	row.Archived = archived
	return detail(snap, row, "opencode:"+id), nil
}

func detail(v dashView, r dashboard.Session, qualified string) dashboard.SessionDetail {
	out := dashboard.SessionDetail{Session: r}
	for _, e := range v.bySession[qualified] {
		x := dashboard.Execution{ID: e.ID, State: e.State, Started: e.Started, Updated: e.Updated, Ended: e.Ended, Current: e.ID == r.CurrentExecution,
			Liveness: dashLiveness(e.Liveness), Permission: dashPermission(e), UnavailableSince: copyTimePtr(e.UnavailableSince)}
		// SessionID is carried by the /executions page only; the detail of one session does not need it
		out.Executions = append(out.Executions, x)
	}
	return out
}

// dashLiveness copies the ADR-017 report block into the dashboard's own type. The copy shares no pointer with the
// Manager's record (T10): every pointer field is copied by value. The block is report-only; nothing here reads or
// changes a state.
func dashLiveness(l *execution.Liveness) *dashboard.Liveness {
	if l == nil {
		return nil
	}
	out := &dashboard.Liveness{
		NativeStatus: l.NativeStatus, StallSuspected: l.StallSuspected, StallReason: l.StallReason,
		DegradationReason: l.DegradationReason, Degraded: l.Degraded || l.DegradationReason != "", RetryOverdue: l.RetryOverdue,
		CauseHint: l.CauseHint, CauseSource: l.CauseSource, Explanation: l.Explanation, ObservedAt: l.ObservedAt,
		ProgressSource: l.ProgressSource,
	}
	if l.RetryAttempt != nil {
		v := *l.RetryAttempt
		out.RetryAttempt = &v
	}
	if l.RetryMax != nil {
		v := *l.RetryMax
		out.RetryMax = &v
	}
	if l.RetryNextAt != nil {
		v := *l.RetryNextAt
		out.RetryNextAt = &v
	}
	if l.LastNativeProgressAt != nil {
		v := *l.LastNativeProgressAt
		out.LastNativeProgressAt = &v
	}
	if l.Tool != nil {
		out.Tool = &dashboard.LivenessTool{Name: l.Tool.ToolName, RunningSince: l.Tool.RunningSince}
	}
	if l.Thresholds != nil {
		out.Thresholds = &dashboard.LivenessThresholds{NoProgressS: l.Thresholds.NoProgressS, ToolLongS: l.Thresholds.ToolLongS}
	}
	return out
}

// dashPermission is what ADR-024 already shows Chat about a pending native permission, reduced further: the count
// and, when the Bridge verified that one request belongs to this execution, its tool, kind, pattern count and
// scope digest. It never carries the permission ID, the call or message ID, a pattern or any native text.
func dashPermission(e execution.Execution) *dashboard.PermissionSummary {
	if e.PendingPermissionCount == nil && e.PendingPermission == nil {
		return nil
	}
	p := &dashboard.PermissionSummary{}
	if e.PendingPermissionCount != nil {
		p.Count = *e.PendingPermissionCount
	}
	if e.PendingPermission != nil {
		p.Attributed = true
		p.Tool, p.Kind, p.PatternCount, p.Digest = e.PendingPermission.Tool, e.PendingPermission.Kind, e.PendingPermission.PatternCount, e.PendingPermission.ScopeDigest
		if p.Count < 1 {
			p.Count = 1
		}
	}
	return p
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

// maxDashExecutions is the cap of the /executions list, applied AFTER the scope filter (A1.4 item 3): a hidden
// session's executions can neither appear nor crowd a visible one out.
const maxDashExecutions = 200

// Executions lists the executions of exactly the sessions the Sessions call lists for this request: the scope set is
// the one Sessions produces (the ACL, the archived flag in the request context, the claudecode: rows), taken BEFORE
// the Sessions page's 200-row display cap because the cap belongs to the page, not to Sessions. The Manager's
// snapshot holds every execution (no cap, no native read); one whose session is not in the scope set is dropped,
// never shown. The result is newest first and capped at maxDashExecutions after the filter. The rows are copies.
func (d *dashSource) Executions(ctx context.Context) ([]dashboard.Execution, error) {
	scope, err := d.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	current := make(map[string]string, len(scope)) // harness-qualified ID -> the session's current execution
	for _, r := range scope {
		current[r.Harness+":"+r.ID] = r.CurrentExecution
	}
	if d.runs == nil {
		return []dashboard.Execution{}, nil
	}
	var out []dashboard.Execution
	for _, e := range d.runs.DashboardSnapshot().Executions {
		cur, in := current[e.SessionID]
		if !in {
			continue
		}
		out = append(out, dashboard.Execution{ID: e.ID, State: e.State, Started: e.Started, Updated: e.Updated, Ended: e.Ended,
			Current: cur != "" && cur == e.ID, SessionID: e.SessionID, Liveness: dashLiveness(e.Liveness),
			UnavailableSince: copyTimePtr(e.UnavailableSince)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Started.Equal(out[j].Started) {
			return out[i].Started.After(out[j].Started)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > maxDashExecutions {
		out = out[:maxDashExecutions]
	}
	if out == nil {
		out = []dashboard.Execution{}
	}
	return out, nil
}

// Health reports the Bridge's own facts and the outcome of the last session-list read (ADR-023 A1.8). It makes no
// native call: a harness that has not been read yet is "not read", never guessed. The connector token's expiry is a
// timestamp from the remote settings' read-only getter; the token, the origin and the principal never reach it.
func (d *dashSource) Health(context.Context) (dashboard.Health, error) {
	h := dashboard.Health{Version: version.Version(), Features: append([]dashboard.Feature(nil), d.features...)}
	if rev, _ := version.Build(); rev != "" {
		h.Build = rev
	}
	if d.remote != nil {
		if t := d.remote.Expires(); !t.IsZero() {
			h.TokenExpires = &t
		}
	}
	d.mu.Lock()
	read := d.read
	d.mu.Unlock()
	oc := dashboard.Reachability{Harness: "opencode", State: "not read"}
	if read.seen {
		oc.At, oc.State = read.at, "unreachable"
		if read.ok {
			oc.State = "reachable"
		}
	}
	// Claude Code sessions are listed as pending rows only (ADR-021): the dashboard reads nothing from that harness.
	h.Reach = []dashboard.Reachability{oc, {Harness: "claudecode", State: "not read"}}
	return h, nil
}
