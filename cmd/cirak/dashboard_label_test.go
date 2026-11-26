package main

import (
	"strings"
	"testing"
)

// ADR-023 A1.8 (slice S3a): CIRAK_DASHBOARD_LABEL is a known name (config check does not call it unknown) and an
// invalid value refuses at startup and in config check, naming the variable and never the value.
func TestConfigCheckKnowsTheDashboardLabel(t *testing.T) {
	f := newFixture(t)
	f.write(t, cleanConfig)
	out, code := check(t, f, map[string]string{"CIRAK_DASHBOARD_LABEL": "bridge-a"}, "--config", f.cfg)
	if strings.Contains(out, "unknown environment name") || strings.Contains(out, "CIRAK_DASHBOARD_LABEL") || code == 2 {
		t.Errorf("exit %d:\n%s", code, out)
	}
	out, code = check(t, f, map[string]string{"CIRAK_DASHBOARD_LABEL": "<script>alert(1)</script>"}, "--config", f.cfg)
	if code != 2 || !strings.Contains(out, "CIRAK_DASHBOARD_LABEL must be at most 40 bytes") || strings.Contains(out, "<script>") {
		t.Errorf("an invalid label: exit %d:\n%s", code, out)
	}
	if _, err := openDashboard(func(k string) string {
		if k == "CIRAK_DASHBOARD_LABEL" {
			return strings.Repeat("a", 41)
		}
		return ""
	}, nil); err == nil || !strings.Contains(err.Error(), "CIRAK_DASHBOARD_LABEL") {
		t.Errorf("an over-long label did not stop the dashboard from opening: %v", err)
	}
}

// #576 P3-1: CIRAK_DASHBOARD_LABEL is new in ADR-026's prefix and never had a BRIDGE_ twin, so BRIDGE_DASHBOARD_LABEL is
// not read: `config check` says so, by name only, instead of treating it as the known canonical name and staying silent.
func TestConfigCheckWarnsThatTheLegacyDashboardLabelIsNotRead(t *testing.T) {
	if got := unknownEnvNames([]string{"BRIDGE_DASHBOARD_LABEL=legacy-a", "CIRAK_DASHBOARD_LABEL=ok", "PATH=/usr/bin"}); strings.Join(got, ",") != "BRIDGE_DASHBOARD_LABEL" {
		t.Errorf("unknown names = %v, want [BRIDGE_DASHBOARD_LABEL]", got)
	}
	f := newFixture(t)
	f.write(t, cleanConfig)
	out, code := check(t, f, map[string]string{"BRIDGE_DASHBOARD_LABEL": "legacy-secret-label"}, "--config", f.cfg)
	if !strings.Contains(out, "unknown environment name BRIDGE_DASHBOARD_LABEL is not read by the daemon") {
		t.Errorf("no warning for the legacy name: exit %d\n%s", code, out)
	}
	if strings.Contains(out, "legacy-secret-label") {
		t.Errorf("the value was echoed:\n%s", out)
	}
	if code == 2 {
		t.Errorf("a legacy name that is not read is a warning, not a refusal (exit %d)", code)
	}
}
