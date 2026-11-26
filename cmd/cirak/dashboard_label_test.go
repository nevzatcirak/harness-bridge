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
