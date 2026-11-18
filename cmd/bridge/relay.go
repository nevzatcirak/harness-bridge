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

// permissionIncarnationMode resolves BRIDGE_PERMISSION_INCARNATION (ADR-024 Amendment A1 S7, D21 Q2):
// off (the default, also when unset) leaves the relay without incarnation evidence, so every request stays
// unattributed (not_verified); observed starts the Bridge-observed continuity epoch (one subscription of
// GET /global/event) and attributes only requests of runs admitted under the current epoch. Anything else
// is a startup error. observed has no effect when BRIDGE_PERMISSION_RELAY=off: no tracker is started.
func permissionIncarnationMode(env func(string) string) (string, error) {
	switch v := strings.TrimSpace(env("BRIDGE_PERMISSION_INCARNATION")); v {
	case "", "off":
		return "off", nil
	case "observed":
		return "observed", nil
	default:
		return "", fmt.Errorf("BRIDGE_PERMISSION_INCARNATION must be off or observed")
	}
}

// startIncarnation starts the epoch tracker when, and only when, the relay is reject_only and the incarnation
// mode is observed. It returns the source (nil when not started) and its stop function (never nil).
func startIncarnation(relayMode, incMode string, client *opencode.Client) (execution.NativeIncarnation, func()) {
	if relayMode != "reject_only" || incMode != "observed" {
		return nil, func() {}
	}
	tracker, err := opencode.NewEpochTracker(opencode.EpochTrackerConfig{Open: client.GlobalEventOpener()})
	if err != nil {
		return nil, func() {}
	}
	tracker.Start()
	return tracker, tracker.Stop
}

// wirePermissionRelay is wirePermissionRelayWith without an incarnation source (the production default).
func wirePermissionRelay(srv *mcp.Server, runs *execution.Manager, client *opencode.Client, mode string) error {
	return wirePermissionRelayWith(srv, runs, client, mode, nil)
}

// wirePermissionRelayWith installs the production parts of reply_permission for mode reject_only: the
// OpenCode client as the one native replier (reject only, routed to the session's directory), the
// process-wide failure-reporting audit writer and the production PermissionSource (ADR-024 S6: a
// fresh observer per read, with the authority taken from the manager's own runs). Mode off installs
// nothing. It adds no allow_once path: the manager and the client both refuse anything but reject.
//
// inc is the incarnation source (ADR-024 A1 S7), nil by default. When set it is installed in BOTH places:
// the manager (which records the epoch armed when a run is admitted) and the source (which supplies the
// epoch only for a run admitted under the current one). With none, every request stays "unattributed":
// pending_permission is count-only and reply_permission refuses with FORBIDDEN not_verified.
func wirePermissionRelayWith(srv *mcp.Server, runs *execution.Manager, client *opencode.Client, mode string, inc execution.NativeIncarnation) error {
	if mode != "reject_only" {
		return nil
	}
	source, err := execution.NewOpenCodePermissionSource(client, runs, execution.PermissionSourceConfig{Incarnation: inc})
	if err != nil {
		return err
	}
	runs.SetIncarnation(inc)
	runs.SetPermissionSource(source)
	runs.SetPermissionReplier(client)
	runs.SetAttemptWriter(audit.Default())
	srv.PermissionRelay = &mcp.PermissionRelayConfig{Mode: "reject_only"}
	return nil
}
