package dashboard

import (
	"log/slog"
	"testing"
)

// ADR-021 slice 4 and the wiring slice: the Claude Code guard_release keys and the mutation
// harness key reach the ring (#257/#259 P3-5). Literal expectations, not the allowlist itself.
func TestRingKeepsTheClaudeReleaseKeys(t *testing.T) {
	l, ring, _ := teeLogger(slog.LevelInfo)
	l.Info("guard release", "event", "guard_release", "tool", "admin", "harness", "claudecode", "outcome", "released", "accepted", true,
		"session_id", "claudecode:4e59c1bd-4333-4a71-875d-cba9301bd4a9", "execution_id", "0f1e2d3c4b5a69788796a5b4c3d2e1f0",
		"child_exited", true, "group_empty", true, "rechecked", true, "row", "6", "exit_class", "signal", "settle_ms", int64(5000),
		"ticket", "OPS-77", "principal", "admin:uid=996", "prompt", "TOP SECRET", "stderr", "SECRET")
	l.Info("prompt submission", "event", "prompt_submission", "tool", "send_prompt", "outcome", "new", "accepted", true,
		"session_id", "claudecode:4e59c1bd-4333-4a71-875d-cba9301bd4a9", "harness", "claudecode", "principal", "local")
	got := ring.Entries()
	if len(got) != 2 {
		t.Fatalf("%d entries", len(got))
	}
	rel := got[1] // oldest first in the ring's return order: the release was logged first
	for k, want := range map[string]string{"harness": "claudecode", "child_exited": "true", "group_empty": "true", "rechecked": "true", "row": "6", "exit_class": "signal"} {
		if v := rel.Attr(k); v != want {
			t.Errorf("%s = %q, want %q (%+v)", k, v, want, rel)
		}
	}
	if rel.Attr("prompt") != "" || rel.Attr("stderr") != "" {
		t.Errorf("a non-allowlisted attribute reached the ring: %+v", rel)
	}
	if got[0].Attr("harness") != "claudecode" {
		t.Errorf("the mutation line lost its harness key: %+v", got[0])
	}
}
