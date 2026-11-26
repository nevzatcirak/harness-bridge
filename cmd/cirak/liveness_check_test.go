package main

import (
	"strings"
	"testing"
)

// ADR-017 A2 S3 (A2.6, D32 Q13) / ADR-027 section 1.5: `config check` refuses an operator threshold
// the daemon would refuse, so a configuration that checks clean is the one that starts.
func TestCheckRefusesAnOutOfRangeLivenessThreshold(t *testing.T) {
	f := newFixture(t)
	f.write(t, cleanConfig)
	out, code := check(t, f, map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "29"}, "--config", f.cfg)
	if code != 2 || !strings.Contains(out, "liveness:") ||
		!strings.Contains(out, "CIRAK_LIVENESS_NO_PROGRESS_S must be an integer between 30 and 900") {
		t.Fatalf("out-of-range threshold: exit %d\n%s", code, out)
	}
	out, code = check(t, f, map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "60", "CIRAK_LIVENESS_TOOL_LONG_S": "90"}, "--config", f.cfg)
	if code != 0 || !strings.Contains(out, "liveness thresholds") {
		t.Fatalf("valid thresholds: exit %d\n%s", code, out)
	}
}
