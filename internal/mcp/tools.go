package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
	"github.com/nevzatcirak/cirak/internal/execution"
	"github.com/nevzatcirak/cirak/internal/repository"
)

type EmptyInput struct{}
type SessionInput struct {
	SessionID string `json:"session_id"`
}
type ExecutionInput struct {
	ExecutionID string `json:"execution_id"`
}

// ReplyPermissionInput is the whole input of reply_permission (ADR-024 section 4): four
// properties, additionalProperties false, supersedes the only optional one.
type ReplyPermissionInput struct {
	ExecutionID  string `json:"execution_id"`
	PermissionID string `json:"permission_id"`
	Decision     string `json:"decision"`
	Supersedes   string `json:"supersedes,omitempty"`
}
type PromptInput struct {
	SessionID      string `json:"session_id"`
	Prompt         string `json:"prompt"`
	IdempotencyKey string `json:"idempotency_key"`
}
type HistoryInput struct {
	SessionID string `json:"session_id"`
	Limit     int    `json:"limit,omitempty"`
	Before    string `json:"before,omitempty"`
}
type Session struct {
	Repository        *repository.Repository `json:"repository,omitempty"`
	Workspace         *repository.Workspace  `json:"workspace,omitempty"`
	ParentSessionID   string                 `json:"parent_session_id,omitempty"`
	HarnessInstanceID string                 `json:"harness_instance_id,omitempty"`
	NativeProjectID   string                 `json:"native_project_id,omitempty"`
	NativeWorkspaceID string                 `json:"native_workspace_id,omitempty"`
	NativeState       string                 `json:"native_state,omitempty"`
	SessionID         string                 `json:"session_id"`
	NativeID          string                 `json:"native_id"`
	Harness           string                 `json:"harness"`
	Directory         string                 `json:"directory"`
	Title             string                 `json:"title"`
	State             string                 `json:"state"`
	// GuardReason is "archive" only while a lifecycle reservation (ADR-022 5.1) is held: the
	// session reads submitting/running (the existing conservative value, no new state).
	GuardReason string `json:"guard_reason,omitempty"`

	// NativeStateInferredFromAbsence is true when native_state "idle" was inferred
	// because the session is absent from a successful native status read of its
	// own directory. Omitted otherwise, including for an explicit native "idle".
	NativeStateInferredFromAbsence bool `json:"native_state_inferred_from_absence,omitempty"`

	// NativeUnfinishedMessage is true when the session's native history has an
	// assistant message without time.completed (F-5). Such a session is never
	// reported idle; send_prompt admission refuses it with SESSION_BUSY.
	NativeUnfinishedMessage bool `json:"native_unfinished_message,omitempty"`

	// PendingPermissionCount is set only by get_session, only while state is
	// waiting_for_permission (ADR-024 S1). A count, never request details.
	PendingPermissionCount *int `json:"pending_permission_count,omitempty"`
}
type Sessions struct {
	Sessions []Session `json:"sessions"`
}
type History struct {
	NextCursor string             `json:"next_cursor,omitempty"`
	Messages   []opencode.Message `json:"messages"`
	// ADR-021 §5/§9, Claude Code sessions only (omitted for OpenCode).
	Source           string     `json:"source,omitempty"`
	HistoryComplete  *bool      `json:"history_complete,omitempty"`
	FirstRecordedAt  *time.Time `json:"first_recorded_at,omitempty"`
	HistoryTruncated *bool      `json:"history_truncated,omitempty"`
}
type Result struct {
	Execution execution.Execution `json:"execution"`
	Messages  []opencode.Message  `json:"messages"`
}

func nonnil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// SDK inference supplies strict object schemas. Raw native parts/errors retain
// their upstream JSON shape rather than being misrepresented as base64 bytes.
func schema[T any]() *jsonschema.Schema {
	s, err := jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}}})
	if err != nil {
		panic(err)
	}
	return s
}
func register[I, O any](server *sdk.Server, bridge *Server, name, description string, convert func(I) args) {
	input := schema[I]()
	for key, p := range input.Properties {
		switch key {
		case "session_id":
			// ADR-021 §9: an anyOf of the two harness patterns. The Claude pattern is the
			// lowercase 8-4-4-4-12 UUID. Accepting the shape is not serving it: a
			// claudecode: ID is refused at execution until the adapter is wired.
			p.AnyOf = []*jsonschema.Schema{{Pattern: `^opencode:ses[A-Za-z0-9_-]+$`}, {Pattern: claudeSessionPattern}}
			p.MaxLength = intp(265)
		case "root":
			p.MaxLength = intp(maxCatalogRootBytes)
		case "state":
			p.Enum = []any{"idle", "running", "waiting_for_permission", "unknown"}
		case "updated_since":
			p.MaxLength = intp(40)
		case "harness_name":
			p.Pattern = harnessNamePattern.String()
		case "subdirectory":
			p.MaxLength = intp(maxCreateSubdir)
		case "title":
			p.MinLength = intp(1)
			p.MaxLength = intp(maxCreateTitle)
		case "execution_id":
			p.MinLength = intp(1)
		case "prompt":
			p.MinLength = intp(1)
			p.MaxLength = intp(32768)
		case "idempotency_key":
			p.MinLength = intp(8)
			p.MaxLength = intp(128)
		case "permission_id":
			// ADR-024 section 4: native IDs start with per; closed pattern, no free text.
			p.Pattern = `^per[A-Za-z0-9_-]{1,125}$`
		case "decision":
			// No always, ever: the schema rejects any other value.
			p.Enum = []any{"reject", "allow_once"}
		case "supersedes":
			p.Pattern = `^[A-Za-z0-9_-]{22}$`
		}
	}
	sdk.AddTool(server, &sdk.Tool{Name: name, Description: description, Annotations: toolAnnotations[name], InputSchema: input, OutputSchema: &jsonschema.Schema{Type: "object", AnyOf: []*jsonschema.Schema{schema[O](), schema[execution.Error]()}}}, func(ctx context.Context, _ *sdk.CallToolRequest, in I) (*sdk.CallToolResult, toolOutput[O], error) {
		a := convert(in)
		err := validateArgs(name, a)
		var value any
		if err == nil {
			value, err = bridge.call(ctx, name, a)
		} else if mutating(name) {
			// Schema-valid but rejected arguments still produce one audit line.
			bridge.audit(ctx, &auditRecord{tool: name}, a, nil, err)
		}
		if err != nil {
			e := envelope(err)
			return &sdk.CallToolResult{IsError: true}, toolOutput[O]{failure: e}, nil
		}
		out := value.(O)
		return nil, toolOutput[O]{success: &out}, nil
	})
}

// toolAnnotations are untrusted hints to the client (research section 3), never authority.
// reply_permission (ADR-024 section 8) is marked as a destructive, non-read-only mutation so a
// client that honours per-call approval asks a human first; the server cannot see whether it did.
var toolAnnotations = map[string]*sdk.ToolAnnotations{
	"reply_permission": {ReadOnlyHint: false, DestructiveHint: boolp(true), IdempotentHint: false, OpenWorldHint: boolp(false)},
}

func boolp(b bool) *bool { return &b }
func intp(n int) *int    { return &n }

const claudeSessionPattern = `^claudecode:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

// Keep the H1 success shape while declaring and validating structured failures.
type toolOutput[T any] struct {
	success *T
	failure *execution.Error
}

func (o toolOutput[T]) MarshalJSON() ([]byte, error) {
	if o.failure != nil {
		return json.Marshal(o.failure)
	}
	return json.Marshal(o.success)
}
