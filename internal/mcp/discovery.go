package mcp

import (
	"context"
	"sort"
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

func (s *Server) describe(ctx context.Context, v opencode.Session, c *opencode.Client) Session {
	out := sessionView(v)
	out.HarnessInstanceID = s.Client.InstanceID()
	out.NativeProjectID = v.ProjectID
	out.NativeWorkspaceID = v.WorkspaceID
	// Never disclose a relationship to a session outside the ACL.
	if opencode.ValidID(v.ParentID, "ses") {
		if _, _, e := s.authorized(ctx, v.ParentID); e == nil {
			out.ParentSessionID = "opencode:" + v.ParentID
		}
	}
	status, se := c.Status(ctx)
	perms, pe := c.Permissions(ctx)
	if se == nil {
		out.NativeState = status[v.ID].Type
		switch out.NativeState {
		case "idle":
			out.State = "idle"
		case "busy", "retry":
			out.State = "running"
		}
	}
	if pe != nil {
		out.State = "unknown"
	} else {
		for _, p := range perms {
			if p.SessionID == v.ID {
				out.State = "waiting_for_permission"
			}
		}
	}
	r, w, e := repository.Inspect(ctx, v.Directory)
	if e == nil && s.Policy.Allows(v.ID, w.Root) && s.Policy.Allows(v.ID, r.CommonDirectory) && s.Policy.Allows(v.ID, w.GitDirectory) {
		out.Repository = &r
		out.Workspace = &w
	}
	return out
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
	ids := []string{}
	for id, allowed := range s.Policy.Sessions {
		if allowed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		v, c, e := s.authorized(ctx, id)
		if e != nil {
			if envelope(e).Code == "FORBIDDEN" || envelope(e).Code == "NOT_FOUND" {
				continue
			}
			return nil, e
		}
		out = append(out, s.describe(ctx, v, c))
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
