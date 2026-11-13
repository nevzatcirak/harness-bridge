package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ADR-021 §9: session_id widens to an anyOf of the OpenCode and Claude Code
// patterns, with the same maximum length. The shape is accepted by the schema;
// no Claude session is served until the adapter is wired, so execution refuses it.
func TestSessionIDSchemaWidensForClaudeCode(t *testing.T) {
	client, _, _, _ := integration(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tool := range tools.Tools {
		raw, _ := json.Marshal(tool.InputSchema)
		var s struct {
			Properties map[string]struct {
				AnyOf     []struct{ Pattern string } `json:"anyOf"`
				Pattern   string                     `json:"pattern"`
				MaxLength int                        `json:"maxLength"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		p, ok := s.Properties["session_id"]
		if !ok {
			continue
		}
		seen++
		if p.Pattern != "" || len(p.AnyOf) != 2 || p.AnyOf[0].Pattern != `^opencode:ses[A-Za-z0-9_-]+$` || p.AnyOf[1].Pattern != claudeSessionPattern || p.MaxLength != 265 {
			t.Errorf("%s: session_id schema = %+v", tool.Name, p)
		}
	}
	if seen == 0 {
		t.Fatal("no tool has a session_id input")
	}
	const claude = "claudecode:4e59c1bd-4333-4a71-875d-cba9301bd4a9"
	for _, tool := range []string{"get_session", "read_session_history", "get_harness_capabilities"} {
		r := invoke(t, client, tool, map[string]any{"session_id": claude}, true) // refused, not a schema error
		if txt := toolText(r); !strings.Contains(txt, "not enabled") {
			t.Errorf("%s: %q", tool, txt)
		}
	}
	r := invoke(t, client, "send_prompt", map[string]any{"session_id": claude, "prompt": "p", "idempotency_key": "claude-key-0001"}, true)
	if txt := toolText(r); !strings.Contains(txt, "not enabled") {
		t.Errorf("send_prompt: %q", txt)
	}
	// Still rejected by the schema: uppercase, a bare UUID, a wrong prefix.
	for _, bad := range []string{strings.ToUpper(claude), "4e59c1bd-4333-4a71-875d-cba9301bd4a9", "claudecode:4e59c1bd", "claude:4e59c1bd-4333-4a71-875d-cba9301bd4a9"} {
		ctx2, c2 := context.WithTimeout(context.Background(), 2*time.Second)
		res, err := client.CallTool(ctx2, &sdk.CallToolParams{Name: "get_session", Arguments: map[string]any{"session_id": bad}})
		c2()
		if err == nil && (res == nil || !res.IsError) {
			t.Errorf("%q passed the schema", bad)
		}
	}
	// An OpenCode ID is unaffected.
	invoke(t, client, "get_session", map[string]any{"session_id": "opencode:ses_existing"}, false)
}

func toolText(r *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
