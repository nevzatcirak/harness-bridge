package mcp

import (
	"context"
	"encoding/json"
	"github.com/nevzatcirak/harness-bridge/internal/repository"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

type EmptyInput struct{}
type SessionInput struct {
	SessionID string `json:"session_id"`
}
type ExecutionInput struct {
	ExecutionID string `json:"execution_id"`
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
}
type Sessions struct {
	Sessions []Session `json:"sessions"`
}
type History struct {
	NextCursor string             `json:"next_cursor,omitempty"`
	Messages   []opencode.Message `json:"messages"`
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
			p.Pattern = `^opencode:ses[A-Za-z0-9_-]+$`
			p.MaxLength = intp(265)
		case "execution_id":
			p.MinLength = intp(1)
		case "prompt":
			p.MinLength = intp(1)
			p.MaxLength = intp(32768)
		case "idempotency_key":
			p.MinLength = intp(8)
			p.MaxLength = intp(128)
		}
	}
	sdk.AddTool(server, &sdk.Tool{Name: name, Description: description, InputSchema: input, OutputSchema: &jsonschema.Schema{Type: "object", AnyOf: []*jsonschema.Schema{schema[O](), schema[execution.Error]()}}}, func(ctx context.Context, _ *sdk.CallToolRequest, in I) (*sdk.CallToolResult, toolOutput[O], error) {
		a := convert(in)
		err := validateArgs(name, a)
		var value any
		if err == nil {
			value, err = bridge.call(ctx, name, a)
		}
		if err != nil {
			e := envelope(err)
			return &sdk.CallToolResult{IsError: true}, toolOutput[O]{failure: e}, nil
		}
		out := value.(O)
		return nil, toolOutput[O]{success: &out}, nil
	})
}
func intp(n int) *int { return &n }

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
