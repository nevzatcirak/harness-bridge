package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

func TestSDKLocalFailureContract(t *testing.T) {
	client, f, bridge, _ := integration(t, true)
	// MCP authorization and manager preflight succeed. Only the worker's fresh
	// pre-Prompt metadata read fails, without changing filesystem/session identity.
	var metadata atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/session/ses_existing" && metadata.Add(1) == 3 {
			http.Error(w, "fixture metadata unavailable", http.StatusServiceUnavailable)
			return
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)
	c, err := opencode.New(upstream.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	bridge.Runs.Close()
	bridge.Client = c
	bridge.Runs = execution.NewWithTiming(c, time.Second, 5*time.Millisecond)
	t.Cleanup(bridge.Runs.Close)
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 11 {
		t.Fatal(tools, err)
	}
	schemas := map[string]*jsonschema.Resolved{}
	for _, tool := range tools.Tools {
		b, _ := json.Marshal(tool.OutputSchema)
		var schema jsonschema.Schema
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		schemas[tool.Name] = resolved
	}
	for _, name := range []string{"list_sessions", "get_session", "read_session_history", "send_prompt", "get_execution_status", "get_execution_result", "interrupt_execution"} {
		if schemas[name] == nil {
			t.Fatalf("legacy tool removed: %s", name)
		}
	}
	check := func(name string, args any, wantError bool) *sdk.CallToolResult {
		r := invoke(t, client, name, args, wantError)
		b, _ := json.Marshal(r.StructuredContent)
		var value any
		if err := json.Unmarshal(b, &value); err != nil {
			t.Fatal(err)
		}
		if err := schemas[name].Validate(value); err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		return r
	}
	input := PromptInput{"opencode:ses_existing", "fixture", "local-failure-key"}
	x := decode[execution.Execution](t, check("send_prompt", input, false))
	// Do not let a status tool's authorization GET race with the worker's third
	// metadata GET: wait via local-only Snapshot before making more MCP calls.
	deadline := time.Now().Add(2 * time.Second)
	for {
		v, err := bridge.Runs.Snapshot(context.Background(), x.ID)
		if err != nil {
			t.Fatal(err)
		}
		if v.State == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not finalize pre-submission failure")
		}
		time.Sleep(time.Millisecond)
	}
	failed := awaitExecution(t, client, x.ID, "failed")
	if failed.LocalFailure == nil || failed.LocalFailure.Code != "PRE_SUBMISSION_UNAVAILABLE" || failed.Ended != nil || failed.Unresolved != nil {
		t.Fatal(failed)
	}
	check("get_execution_status", ExecutionInput{x.ID}, false)
	result := decode[Result](t, check("get_execution_result", ExecutionInput{x.ID}, false))
	if len(result.Messages) != 0 || result.Execution.LocalFailure == nil || result.Execution.Ended != nil {
		t.Fatal(result)
	}
	replay := decode[execution.Execution](t, check("send_prompt", input, false))
	if replay.ID != x.ID || replay.MessageID != x.MessageID || replay.State != "failed" || replay.LocalFailure == nil {
		t.Fatal(replay)
	}
	interruption := decode[execution.Error](t, check("interrupt_execution", ExecutionInput{x.ID}, true))
	if interruption.Code != "INVALID_INPUT" {
		t.Fatal(interruption)
	}
	f.mu.Lock()
	posts, aborts := f.posts, f.aborts
	f.mu.Unlock()
	if posts != 0 || aborts != 0 {
		t.Fatal(posts, aborts)
	}
	input.IdempotencyKey = "next-local-key"
	next := decode[execution.Execution](t, check("send_prompt", input, false))
	completed := awaitExecution(t, client, next.ID, "completed")
	if completed.LocalFailure != nil || completed.Ended == nil {
		t.Fatal(completed)
	}
	check("interrupt_execution", ExecutionInput{x.ID}, true)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 || f.aborts != 0 {
		t.Fatal(f.posts, f.aborts)
	}
}
