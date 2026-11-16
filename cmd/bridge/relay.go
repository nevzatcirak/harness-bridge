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
// OpenCode client as the one native replier (reject only, routed to the session's directory), the
// process-wide failure-reporting audit writer and the production PermissionSource (ADR-024 S6: a
// fresh observer per read, with the authority taken from the manager's own runs). Mode off installs
// nothing. It adds no allow_once path: the manager and the client both refuse anything but reject.
//
// The source has no native incarnation evidence (docs/P2_PERM_CORE_02_OBSERVATION.md: none is
// fabricated), so on a deployed Bridge every request stays "unattributed": pending_permission is
// count-only and reply_permission refuses with FORBIDDEN not_verified until such a source exists.
func wirePermissionRelay(srv *mcp.Server, runs *execution.Manager, client *opencode.Client, mode string) error {
	if mode != "reject_only" {
		return nil
	}
	source, err := execution.NewOpenCodePermissionSource(client, runs, execution.PermissionSourceConfig{})
	if err != nil {
		return err
	}
	runs.SetPermissionSource(source)
	runs.SetPermissionReplier(client)
	runs.SetAttemptWriter(audit.Default())
	srv.PermissionRelay = &mcp.PermissionRelayConfig{Mode: "reject_only"}
	return nil
}
