package main

import (
	"github.com/nevzatcirak/cirak/internal/claudewire"
	"github.com/nevzatcirak/cirak/internal/execution"
)

// acquireOpenCodeOwnership is the OpenCode writer's host-local lock step of main (ADR-010), one
// owner per session (#236 P3-4): it takes the lock of every ACL entry EXCEPT the claudecode: ones,
// which the Claude Code adapter owns itself (same directory, same hashed names; a second flock of
// one name in this process would be refused). It returns a nil Ownership when the ACL holds no
// OpenCode entry, so the caller must not Close it unconditionally.
func acquireOpenCodeOwnership(dir string, acl []string) (*execution.Ownership, error) {
	_, opencodeSessions := claudewire.ClaudeSessionIDs(acl)
	if len(opencodeSessions) == 0 {
		return nil, nil
	}
	return execution.AcquireOwnership(dir, opencodeSessions)
}
