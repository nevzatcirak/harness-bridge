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
}

func (s *Server) inspectDirectory(ctx context.Context, v opencode.Session, members ...string) directoryEvidence {
	c := s.Client.WithDirectory(v.Directory)
	status, se := c.Status(ctx)
	permissions, pe := c.Permissions(ctx)
	d := directoryEvidence{status: map[string]opencode.Status{}, statusOK: se == nil, permissions: map[string]bool{}, permissionsOK: pe == nil}
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
		out.NativeState = d.status[v.ID].Type
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
	out.Repository = d.repository
	out.Workspace = d.workspace
	return out
}
func (s *Server) describe(ctx context.Context, v opencode.Session, _ *opencode.Client) Session {
	visible := false
	if opencode.ValidID(v.ParentID, "ses") {
		_, _, e := s.authorized(ctx, v.ParentID)
		visible = e == nil
	}
	return s.view(v, s.inspectDirectory(ctx, v), visible)
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
	directoryIndex := map[string]int{}
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
		if _, ok := directoryIndex[v.Directory]; !ok {
			directoryIndex[v.Directory] = len(directories)
			directories = append(directories, v)
			groups = append(groups, nil)
		}
		groups[directoryIndex[v.Directory]] = append(groups[directoryIndex[v.Directory]], id)
	}
	evidence := make([]directoryEvidence, len(directories))
	parallel(ctx, len(directories), func(i int) { evidence[i] = s.inspectDirectory(ctx, directories[i], groups[i]...) })
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Revalidate all paths before projecting either sessions or parent links.
	// This remains a best-effort observation, not an atomic filesystem snapshot.
	for i, id := range ids {
		if authorized[id] && !s.Policy.Allows(id, metadata[i].Directory) {
			delete(authorized, id)
		}
	}
	for i, id := range ids {
		if authorized[id] {
			v := metadata[i]
			out = append(out, s.view(v, evidence[directoryIndex[v.Directory]], authorized[v.ParentID]))
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
		_, c, e := s.authorized(ctx, id)
		if e != nil {
			return nil, e
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
