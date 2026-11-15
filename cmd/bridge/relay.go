package main

import (
	"fmt"
	"strings"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/audit"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcp"
)

// permissionRelayMode resolves BRIDGE_PERMISSION_RELAY (ADR-024 section 5, D15 Q3): reject_only
// (the default, also when unset) registers reply_permission; off leaves the Bridge with its
// eleven tools. policy is the mode of slices S2/S4, which are not started (D15 Q1): it is refused
// at startup rather than silently treated as reject_only, so an operator who sets it is told.
func permissionRelayMode(env func(string) string) (string, error) {
	switch v := strings.TrimSpace(env("BRIDGE_PERMISSION_RELAY")); v {
	case "", "reject_only":
		return "reject_only", nil
	case "off":
		return "off", nil
	case "policy":
		return "", fmt.Errorf("BRIDGE_PERMISSION_RELAY=policy is not available: the policy mode (ADR-024 S2/S4) is not started; use reject_only or off")
	default:
		return "", fmt.Errorf("BRIDGE_PERMISSION_RELAY must be reject_only or off")
	}
}

// wirePermissionRelay installs the production parts of reply_permission for mode reject_only: the
// OpenCode client as the one native replier (reject only, routed to the session's directory) and
// the process-wide failure-reporting audit writer. Mode off installs nothing. It adds no allow_once
// path: the manager and the client both refuse anything but reject.
//
// There is deliberately no PermissionSource here: the observer needs an authority built from the
// manager's submitted-run view (opencode.PermissionObservationAuthority), a slice of its own. Until
// then the tool is registered but the fresh re-read of ADR-024 4.2 step 5 fails closed
// (UPSTREAM_FAILURE, nothing sent) and pending_permission stays count-only.
func wirePermissionRelay(srv *mcp.Server, runs *execution.Manager, client *opencode.Client, mode string) {
	if mode != "reject_only" {
		return
	}
	runs.SetPermissionReplier(client)
	runs.SetAttemptWriter(audit.Default())
	srv.PermissionRelay = &mcp.PermissionRelayConfig{Mode: "reject_only"}
}
