package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// ClaudeHarness is what the MCP layer needs from the ADR-021 Claude Code adapter. A nil
// Server.Claude means the adapter is off (the default): a claudecode: session ID keeps its
// "not enabled" refusal and nothing below runs. The implementation is internal/claudewire.
type ClaudeHarness interface {
	Sessions() []claudecode.Session
	Session(id string) (claudecode.Session, bool)
	State(id string) string
	Version() string
	Send(ctx context.Context, session, prompt, key, principal string) (execution.Execution, bool, error)
	Owns(executionID string) bool
	Get(executionID string) (execution.Execution, error)
	Interrupt(executionID string) (execution.Execution, error)
	History(session string) (claudecode.ReadResult, error)
}

// claudeID returns the canonical claudecode:<uuid> ID of raw when the adapter is enabled.
// A malformed or non-canonical ID is not a Claude ID: it falls through to the existing paths
// (and their refusals).
func (s *Server) claudeID(raw string) (string, bool) {
	if s.Claude == nil {
		return "", false
	}
	return claudeCanonical(raw)
}

func claudeCanonical(raw string) (string, bool) {
	uuid, err := claudecode.ParseSessionID(raw)
	if err != nil || raw != claudecode.SessionPrefix+uuid {
		return "", false
	}
	return raw, true
}

// claudeSession authorizes a Claude session: it must be in the operator allowlist (NOT_FOUND
// otherwise, with no spawn) and in the per-session ACL (FORBIDDEN otherwise; the Executor
// enforces the same at construction).
func (s *Server) claudeSession(id string) (claudecode.Session, error) {
	sess, ok := s.Claude.Session(id)
	if !ok {
		return claudecode.Session{}, execution.Fail("NOT_FOUND", "session not found")
	}
	if s.Policy == nil || !s.Policy.Sessions[id] {
		return claudecode.Session{}, execution.Fail("FORBIDDEN", "session not authorized")
	}
	return sess, nil
}

func (s *Server) claudeView(sess claudecode.Session) Session {
	return Session{SessionID: sess.ID, NativeID: sess.UUID(), Harness: "claudecode", Directory: sess.Root, Title: sess.Label, State: s.Claude.State(sess.ID)}
}

// claudeViews lists the allowlisted sessions the ACL authorizes (ADR-021 §6: only through
// the operator list).
func (s *Server) claudeViews() []Session {
	var out []Session
	for _, sess := range s.Claude.Sessions() {
		if s.Policy != nil && s.Policy.Sessions[sess.ID] {
			out = append(out, s.claudeView(sess))
		}
	}
	return out
}

// mergeClaude appends the Claude sessions to a discovery list and restores the session-ID
// order. It is only called when the adapter is enabled.
func (s *Server) mergeClaude(list []Session) []Session {
	list = append(list, s.claudeViews()...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].SessionID < list[j].SessionID })
	return list
}

// claudeSessionCall serves get_session, read_session_history and send_prompt for a Claude ID.
func (s *Server) claudeSessionCall(ctx context.Context, name, id string, a args, record *auditRecord) (any, error) {
	sess, e := s.claudeSession(id)
	if e != nil {
		return nil, e
	}
	switch name {
	case "get_session":
		return s.claudeView(sess), nil
	case "read_session_history":
		return s.claudeHistory(id, a, "")
	}
	x, replay, e := s.Claude.Send(ctx, id, a.Prompt, a.IdempotencyKey, s.auditPrincipal())
	if record != nil {
		record.replay = replay
	}
	return x, e
}

// History additions for the Claude harness (ADR-021 §5, §9): Bridge-normalized messages with
// source "bridge_record". Each record becomes one message whose single part is the record's
// API whitelist projection; prompt text is the operator's own prompt and tool output is
// already redacted by the store.
func (s *Server) claudeHistory(id string, a args, onlyExecution string) (History, error) {
	if a.Before != "" {
		return History{}, execution.Fail("INVALID_INPUT", "a history cursor is not supported for claudecode sessions")
	}
	res, err := s.Claude.History(id)
	if err != nil {
		return History{}, &execution.Error{Code: "UPSTREAM_FAILURE", Message: "claudecode history unavailable"}
	}
	msgs := []opencode.Message{}
	for _, r := range res.Records {
		if onlyExecution != "" && r.ExecutionID != onlyExecution {
			continue
		}
		msgs = append(msgs, claudeMessage(id, r))
	}
	if a.Limit > 0 && len(msgs) > a.Limit {
		msgs = msgs[len(msgs)-a.Limit:]
	}
	complete, truncated := !res.Partial && !res.Corrupt, res.Partial
	h := History{Messages: msgs, Source: "bridge_record", HistoryComplete: &complete, HistoryTruncated: &truncated}
	if len(res.Records) > 0 {
		t := res.Records[0].At.UTC()
		h.FirstRecordedAt = &t
	}
	return h, nil
}

func claudeMessage(session string, r claudecode.Record) opencode.Message {
	api := r.API()
	role := "system"
	switch r.Kind {
	case claudecode.KindTurnStart:
		role = "user"
	case claudecode.KindAssistantBlock:
		role = "assistant"
	case claudecode.KindToolResult:
		role = "tool"
	}
	raw, _ := json.Marshal(api)
	m := opencode.Message{Parts: []json.RawMessage{raw}}
	m.Info.ID = "rec_" + strconv.FormatInt(api.Seq, 10)
	m.Info.Role = role
	m.Info.SessionID = session
	m.Info.Time.Created = api.At.UnixMilli()
	return m
}

// claudeExecutionCall serves the execution tools for an execution the adapter owns.
func (s *Server) claudeExecutionCall(name string, a args) (any, error) {
	switch name {
	case "get_execution_status":
		return s.Claude.Get(a.ExecutionID)
	case "interrupt_execution":
		return s.Claude.Interrupt(a.ExecutionID)
	}
	x, e := s.Claude.Get(a.ExecutionID)
	if e != nil {
		return nil, e
	}
	h, e := s.claudeHistory(x.SessionID, args{}, x.ID)
	if e != nil {
		return nil, e
	}
	return Result{Execution: x, Messages: nonnil(h.Messages)}, nil
}

// ClaudeCapabilities is the per-harness block of ADR-021 §9. Every fact is declared by the
// Bridge, none is probed from the CLI.
type ClaudeCapabilities struct {
	Harness               string   `json:"harness"`
	Version               string   `json:"version,omitempty"`
	Capabilities          []string `json:"capabilities"`
	ResumeExactID         bool     `json:"resume_exact_id"`
	NativeLock            bool     `json:"native_lock"`
	HistorySource         string   `json:"history_source"`
	PermissionRelay       bool     `json:"permission_relay"`
	Interrupt             string   `json:"interrupt"`
	ActiveTerminalControl bool     `json:"active_terminal_control"`
	SessionCreation       bool     `json:"session_creation"`
	ReadOnlyTools         bool     `json:"read_only_tools"`
}

func (s *Server) claudeCapabilities(id string) (any, error) {
	if _, e := s.claudeSession(id); e != nil {
		return nil, e
	}
	return Capabilities{
		HarnessInstanceID: "claudecode", PrimaryAgents: []string{}, Subagents: []string{}, NativeDelegation: "unknown",
		Evidence: "Declared by the Bridge (ADR-021 section 9); nothing was probed from the Claude Code CLI",
		Guidance: "Claude Code sessions are continued by exact ID with a read-only tool profile; the Bridge has no approval path, so a denied tool is reported in permission_denials. Do not resume the same session in a terminal at the same time.",
		ClaudeCode: &ClaudeCapabilities{Harness: "claudecode", Version: s.Claude.Version(), Capabilities: []string{"Read", "Glob", "Grep"}, ResumeExactID: true, NativeLock: false,
			HistorySource: "bridge_record", PermissionRelay: false, Interrupt: "sigint_then_sigterm", ActiveTerminalControl: false, SessionCreation: false, ReadOnlyTools: true},
	}, nil
}
