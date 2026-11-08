package mcp

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/repository"
)

type FindInput struct {
	RepositoryID    string `json:"repository_id,omitempty"`
	WorkspaceID     string `json:"workspace_id,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	Offset          int    `json:"offset,omitempty"`
}
type RepositoryInput struct {
	RepositoryID string `json:"repository_id"`
}
type Repositories struct {
	Repositories []repository.Repository `json:"repositories"`
}
type FoundSessions struct {
	Sessions   []Session `json:"sessions"`
	NextOffset *int      `json:"next_offset,omitempty"`
}
type Capabilities struct {
	HarnessInstanceID      string   `json:"harness_instance_id"`
	PrimaryAgents          []string `json:"primary_agents"`
	Subagents              []string `json:"subagents"`
	NativeDelegation       string   `json:"native_delegation"`
	ChildSessionInspection bool     `json:"child_session_inspection"`
	NativeSessionCreation  bool     `json:"native_session_creation"`
	BridgeSessionCreation  bool     `json:"bridge_session_creation"`
	Evidence               string   `json:"evidence"`
	Guidance               string   `json:"guidance"`
}

// directoryEvidence is request-local. Sharing it never claims an atomic native snapshot.
type directoryEvidence struct {
	status        map[string]opencode.Status
	statusOK      bool
	permissions   map[string]bool
	permissionsOK bool
	repository    *repository.Repository
	workspace     *repository.Workspace
	// history holds completed F-5 checks by session ID. A session whose state
	// would be idle and that has no completed check is reported unknown.
	history map[string]historyEvidence
}

// workspaceBound reports whether OpenCode routes the session's own requests by
// its workspaceID rather than by its directory (H4A-F2-FOLLOWUP).
func workspaceBound(v opencode.Session) bool { return v.WorkspaceID != "" }

func (s *Server) inspectDirectory(ctx context.Context, v opencode.Session, members ...string) directoryEvidence {
	d := directoryEvidence{status: map[string]opencode.Status{}, permissions: map[string]bool{}, history: map[string]historyEvidence{}}
	if !workspaceBound(v) {
		// Directory-scoped native reads load (bootstrap) the OpenCode instance for
		// v.Directory. A workspace-bound session lives in its workspace's instance,
		// so these reads would boot a local instance that is otherwise not needed
		// and prove nothing about the session (R2-PR16-REVIEW, H4A-F2-VERIFY). They
		// are skipped; statusOK and permissionsOK stay false, so state is unknown.
		c := s.Client.WithDirectory(v.Directory)
		status, se := c.Status(ctx)
		permissions, pe := c.Permissions(ctx)
		// A 2xx JSON null decodes to a nil map without error; it is not an empty
		// status map and must not support absence inference (R2-PR16-REVIEW).
		d.statusOK, d.permissionsOK = se == nil && status != nil, pe == nil
		if len(members) == 0 {
			members = []string{v.ID}
		}
		allowed := map[string]bool{}
		for _, id := range members {
			allowed[id] = true
			if value, ok := status[id]; ok {
				d.status[id] = value
			}
		}
		for _, p := range permissions {
			if allowed[p.SessionID] {
				d.permissions[p.SessionID] = true
			}
		}
	}
	// Retain only this directory's authorized members, not the full native inventory.
	r, w, e := repository.Inspect(ctx, v.Directory, s.Policy, v.ID)
	if e == nil {
		d.repository = &r
		d.workspace = &w
	}
	return d
}
func (s *Server) view(v opencode.Session, d directoryEvidence, parentVisible bool) Session {
	out := sessionView(v)
	out.HarnessInstanceID = s.Client.InstanceID()
	out.NativeProjectID = v.ProjectID
	out.NativeWorkspaceID = v.WorkspaceID
	if parentVisible {
		out.ParentSessionID = "opencode:" + v.ParentID
	}
	if d.statusOK {
		native, listed := d.status[v.ID]
		if !listed && v.WorkspaceID == "" {
			// OpenCode drops idle sessions from its per-directory status map and
			// reports idle for absent ones. Absence counts only from this successful
			// read of the session's own directory-scoped client. Permission and
			// guard evidence below can still override it.
			//
			// A workspace-bound session is excluded: OpenCode routes its
			// session-bound requests by workspaceID, while /session/status has no
			// session ID and is served by the ?directory= instance, so the session
			// can be busy elsewhere and still be absent here (R2-PR16-REVIEW).
			// Its state stays unknown.
			native.Type = "idle"
			out.NativeStateInferredFromAbsence = true
		}
		out.NativeState = native.Type
		switch out.NativeState {
		case "idle":
			out.State = "idle"
		case "busy", "retry":
			out.State = "running"
		}
	}
	if !d.permissionsOK {
		out.State = "unknown"
	} else if d.permissions[v.ID] {
		out.State = "waiting_for_permission"
	}
	// F-5: idle needs a completed history check without an unfinished
	// assistant message. Anything else is unknown, never idle.
	if out.State == "idle" {
		h := d.history[v.ID]
		if !h.checked || h.unfinished {
			out.State = "unknown"
		}
		out.NativeUnfinishedMessage = h.checked && h.unfinished
	}
	// A session guarded by this Bridge is never idle, whatever native status says.
	if s.Runs != nil {
		if held, executionState := s.Runs.SessionGuard(v.ID); held {
			out.State = guardedState(executionState, d.permissionsOK, d.permissions[v.ID])
		}
	}
	out.Repository = d.repository
	out.Workspace = d.workspace
	return out
}

// guardedState maps a held execution to session state: permission evidence first
// (as for unguarded sessions), then the execution; native status is ignored.
func guardedState(executionState string, permissionsOK, pending bool) string {
	if !permissionsOK {
		return "unknown"
	}
	if pending {
		return "waiting_for_permission"
	}
	switch executionState {
	case "submitting", "running":
		return "running"
	case "waiting_for_permission":
		return "waiting_for_permission"
	}
	return "unknown"
}
func (s *Server) describe(ctx context.Context, v opencode.Session, _ *opencode.Client) Session {
	visible := false
	if opencode.ValidID(v.ParentID, "ses") {
		_, _, e := s.authorized(ctx, v.ParentID)
		visible = e == nil
	}
	d := s.inspectDirectory(ctx, v)
	if s.needsHistory(v, d) {
		// Full history, as admission reads it: get_session is authoritative.
		check, cancel := context.WithTimeout(ctx, unfinishedBudget)
		d.history[v.ID] = s.checkUnfinished(check, v, 0)
		cancel()
	}
	runGuardWindowHook()
	return s.view(v, d, visible)
}

// parallel performs at most four jobs concurrently, with no per-session goroutine.
func parallel(ctx context.Context, n int, fn func(int)) {
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() == nil {
					fn(i)
				}
			}
		}()
	}
send:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
}
func (s *Server) discover(ctx context.Context) ([]Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out := []Session{}
	if s.Policy == nil {
		return out, nil
	}
	if len(s.Policy.Sessions) > 1000 {
		return nil, execution.Fail("INVALID_INPUT", "discovery ACL exceeds 1000 sessions")
	}
	// At most two discoveries per Server; queueing consumes the same deadline.
	s.discoveryOnce.Do(func() { s.discoverySlots = make(chan struct{}, 2) })
	select {
	case s.discoverySlots <- struct{}{}:
		defer func() { <-s.discoverySlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ids := []string{}
	for id, allowed := range s.Policy.Sessions {
		if allowed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	metadata := make([]opencode.Session, len(ids))
	errs := make([]error, len(ids))
	parallel(ctx, len(ids), func(i int) { metadata[i], _, errs[i] = s.authorized(ctx, ids[i]) })
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	authorized := map[string]bool{}
	directories := []opencode.Session{}
	// Workspace-bound sessions form their own groups: their evidence comes from
	// no native call, and they must not share a directory's native reads.
	type groupKey struct {
		directory      string
		workspaceBound bool
	}
	directoryIndex := map[groupKey]int{}
	groups := [][]string{}
	for i, id := range ids {
		if errs[i] != nil {
			code := envelope(errs[i]).Code
			if code == "FORBIDDEN" || code == "NOT_FOUND" {
				continue
			}
			return nil, errs[i]
		}
		authorized[id] = true
		v := metadata[i]
		key := groupKey{v.Directory, workspaceBound(v)}
		if _, ok := directoryIndex[key]; !ok {
			directoryIndex[key] = len(directories)
			directories = append(directories, v)
			groups = append(groups, nil)
		}
		groups[directoryIndex[key]] = append(groups[directoryIndex[key]], id)
	}
	evidence := make([]directoryEvidence, len(directories))
	parallel(ctx, len(directories), func(i int) { evidence[i] = s.inspectDirectory(ctx, directories[i], groups[i]...) })
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// F-5: unguarded sessions whose state would be idle get a bounded latest-page history
	// check, at most four at once, within one phase budget. A check that does
	// not complete leaves the session unknown; it does not fail the listing.
	pending := []int{}
	for i, id := range ids {
		if authorized[id] && s.needsHistory(metadata[i], evidence[directoryIndex[groupKey{metadata[i].Directory, workspaceBound(metadata[i])}]]) {
			pending = append(pending, i)
		}
	}
	checks := make([]historyEvidence, len(pending))
	phase, cancelPhase := context.WithTimeout(ctx, unfinishedBudget)
	parallel(phase, len(pending), func(j int) { checks[j] = s.checkUnfinished(phase, metadata[pending[j]], unfinishedPage) })
	cancelPhase()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for j, i := range pending {
		v := metadata[i]
		evidence[directoryIndex[groupKey{v.Directory, workspaceBound(v)}]].history[v.ID] = checks[j]
	}
	runGuardWindowHook()
	// Revalidate all paths before projecting either sessions or parent links.
	// This remains a best-effort observation, not an atomic filesystem snapshot.
	for i, id := range ids {
		if !authorized[id] {
			continue
		}
		allowed, e := s.Policy.AllowsContext(ctx, id, metadata[i].Directory)
		if e != nil {
			return nil, workspaceCheckUnavailable()
		}
		if !allowed {
			delete(authorized, id)
		}
	}
	for i, id := range ids {
		if authorized[id] {
			v := metadata[i]
			out = append(out, s.view(v, evidence[directoryIndex[groupKey{v.Directory, workspaceBound(v)}]], authorized[v.ParentID]))
		}
	}
	return out, nil
}
func (s *Server) discoveryCall(ctx context.Context, name string, a args) (any, error) {
	if name == "get_harness_capabilities" {
		id, e := native(a.SessionID)
		if e != nil {
			return nil, e
		}
		v, c, e := s.authorized(ctx, id)
		if e != nil {
			return nil, e
		}
		if workspaceBound(v) {
			// /agent and /doc are directory-scoped and would boot a local instance
			// that does not serve this session (H4A-F2-FOLLOWUP).
			return nil, execution.Fail("INVALID_INPUT", "get_harness_capabilities is unavailable for workspace-bound sessions (native_workspace_id set); their native instance is not reachable by directory")
		}
		agents, e := c.Agents(ctx)
		if e != nil {
			return nil, e
		}
		paths, e := c.Documented(ctx)
		if e != nil {
			return nil, e
		}
		out := Capabilities{HarnessInstanceID: c.InstanceID(), PrimaryAgents: []string{}, Subagents: []string{}, NativeDelegation: "unknown", Evidence: "Live /agent inventory and /doc endpoint declarations; creation/delegation not executed", Guidance: "For large exploration or review tasks, ask the existing native primary agent to use its available native subagents when appropriate and permitted. Claude Chat has no assumed programmable subagent-spawning API. Inspect authorized child sessions; never bypass native permissions."}
		for _, a := range agents {
			if a.Hidden {
				continue
			}
			if a.Mode == "primary" || a.Mode == "all" {
				out.PrimaryAgents = append(out.PrimaryAgents, a.Name)
			}
			if a.Mode == "subagent" || a.Mode == "all" {
				out.Subagents = append(out.Subagents, a.Name)
			}
		}
		sort.Strings(out.PrimaryAgents)
		sort.Strings(out.Subagents)
		out.ChildSessionInspection = paths["/session/{sessionID}/children"]["get"] != nil
		out.NativeSessionCreation = paths["/session"]["post"] != nil
		return out, nil
	}
	all, e := s.discover(ctx)
	if e != nil {
		return nil, e
	}
	if name == "list_repositories" || name == "get_repository" {
		repos := []repository.Repository{}
		seen := map[string]bool{}
		for _, v := range all {
			if v.Repository != nil && !seen[v.Repository.ID] {
				seen[v.Repository.ID] = true
				repos = append(repos, *v.Repository)
			}
		}
		sort.Slice(repos, func(i, j int) bool { return repos[i].ID < repos[j].ID })
		if name == "get_repository" {
			for _, r := range repos {
				if r.ID == a.RepositoryID {
					return r, nil
				}
			}
			return nil, execution.Fail("NOT_FOUND", "repository not visible")
		}
		return Repositories{repos}, nil
	}
	matched := []Session{}
	for _, v := range all {
		if a.RepositoryID != "" && (v.Repository == nil || v.Repository.ID != a.RepositoryID) {
			continue
		}
		if a.WorkspaceID != "" && (v.Workspace == nil || v.Workspace.ID != a.WorkspaceID) {
			continue
		}
		if a.ParentSessionID != "" && v.ParentSessionID != a.ParentSessionID {
			continue
		}
		matched = append(matched, v)
	}
	limit := a.Limit
	if limit == 0 {
		limit = 50
	}
	start := a.Offset
	if start > len(matched) {
		start = len(matched)
	}
	end := start + limit
	if end > len(matched) {
		end = len(matched)
	}
	out := FoundSessions{Sessions: matched[start:end]}
	if end < len(matched) {
		out.NextOffset = &end
	}
	return out, nil
}
