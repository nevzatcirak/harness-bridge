package main

import (
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/claudewire"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// newMCPServer builds the one mcp.Server that serves /mcp and the dashboard source. The Claude
// Code adapter is set on it ONLY when it runs: a nil *claudewire.Harness assigned to the
// interface would be a non-nil interface, and a claudecode: tool call would dereference it
// instead of being refused as "not enabled" (review #276 P3-1; pinned by a test).
func newMCPServer(client *opencode.Client, runs *execution.Manager, token string, policy *security.Policy, remote *security.Remote, readOnly bool, claude *claudewire.Harness) *mcp.Server {
	s := &mcp.Server{Client: client, Runs: runs, Token: token, Policy: policy, Remote: remote, ReadOnly: readOnly}
	if claude != nil {
		s.Claude = claude
	}
	return s
}
