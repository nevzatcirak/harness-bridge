package claudecode

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// R317-FIX-2 (#329 P2-1): no public path may print true/pass for a check that was not observed.

// The exported API has no entry that assumes every extra check was made: a caller must pass the
// observations (LinesWith).
func TestNoExportedLinesWithoutObservations(t *testing.T) {
	if _, ok := reflect.TypeOf(CanaryReport{}).MethodByName("Lines"); ok {
		t.Fatal("CanaryReport.Lines() is exported: it assumes every extra check was observed")
	}
	if _, ok := reflect.TypeOf(CanaryReport{}).MethodByName("LinesWith"); !ok {
		t.Fatal("CanaryReport.LinesWith is missing")
	}
}

// A passing report with nothing observed never prints true or pass for the three extra checks.
func TestLinesWithNothingObservedNeverClaimsPass(t *testing.T) {
	rep := evaluate(CanaryFormA, streamOpts{}, nil)
	if !rep.Pass {
		t.Fatalf("fixture report must pass: %v", rep.Failures)
	}
	text := strings.Join(rep.LinesWith(CanaryObservations{}), "\n")
	for _, bad := range []string{"no_decoy_leak=true", "no_file_canary=true", "no_sink_request=true", "verdict=pass"} {
		if strings.Contains(text, bad) {
			t.Errorf("unobserved evidence contains %q:\n%s", bad, text)
		}
	}
	for _, want := range []string{"no_decoy_leak=unknown", "no_file_canary=unknown", "no_sink_request=unknown", "verdict=incomplete"} {
		if !strings.Contains(text, want) {
			t.Errorf("unobserved evidence lacks %q:\n%s", want, text)
		}
	}
}

// The binding acceptance text names the observed path, not the removed Lines().
func TestAcceptanceTextNamesTheObservedPath(t *testing.T) {
	b, err := os.ReadFile("ACCEPTANCE_S3.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "CanaryReport.Lines()") {
		t.Error("ACCEPTANCE_S3.md still tells the operator to commit CanaryReport.Lines()")
	}
	if !strings.Contains(s, "LinesWith") || !strings.Contains(s, "hb-claude-acceptance") {
		t.Error("ACCEPTANCE_S3.md does not name the driver / LinesWith with observations")
	}
}
