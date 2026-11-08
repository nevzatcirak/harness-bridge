package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// ADR-020 (b) test plan group 10: the guard release is not an MCP tool. The
// tool list is exactly the existing one, in local and remote mode.
func TestGuardReleaseNotOnMCPSurface(t *testing.T) {
	want := []string{"find_sessions", "get_execution_result", "get_execution_status", "get_harness_capabilities", "get_repository", "get_session",
		"interrupt_execution", "list_repositories", "list_sessions", "read_session_history", "send_prompt"}
	for _, remote := range []bool{false, true} {
		client, _, _, _ := integrationMode(t, true, remote)
		tools, err := client.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, tool := range tools.Tools {
			got = append(got, tool.Name)
			text := strings.ToLower(tool.Name + " " + tool.Description)
			for _, word := range []string{"release", "admin", "guard"} {
				if strings.Contains(text, word) {
					t.Errorf("remote=%v: tool %s mentions %q", remote, tool.Name, word)
				}
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("remote=%v: tools %v, want %v", remote, got, want)
		}
	}
}
