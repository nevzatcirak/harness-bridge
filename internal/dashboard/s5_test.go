package dashboard

import (
	"log/slog"
	"strings"
	"testing"
)

// ADR-022 slice S5: the session_create line is kept by the audit ring with its closed-token
// keys only; a title, a path or any other key is dropped.
func TestRingKeepsTheSessionCreateLine(t *testing.T) {
	l, ring, _ := teeLogger(slog.LevelInfo)
	l.Info("session create", "event", "session_create", "tool", "create_session", "outcome", "created", "accepted", true,
		"root_id", 0, "session_id", "opencode:ses_abc", "key_hash", "0123456789abcdef", "limit_state", "hour:1/5,root:1/3,total:1/5",
		"bootstrap_may_be_outside_root", true, "replay", true, "principal", "local", "title", "SECRET-TITLE", "directory", "/free/path")
	got := ring.Entries()
	if len(got) != 1 || got[0].Event != "session_create" {
		t.Fatalf("%+v", got)
	}
	keys := map[string]string{}
	for _, a := range got[0].Attrs {
		keys[a.Key] = a.Value
	}
	for _, k := range []string{"root_id", "key_hash", "limit_state", "bootstrap_may_be_outside_root", "replay", "session_id", "outcome"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("%s missing: %v", k, keys)
		}
	}
	for _, bad := range []string{"title", "directory"} {
		if _, ok := keys[bad]; ok {
			t.Errorf("%s must not be kept", bad)
		}
	}
	if b := strings.Join(func() []string {
		var v []string
		for _, x := range keys {
			v = append(v, x)
		}
		return v
	}(), " "); strings.Contains(b, "SECRET") || strings.Contains(b, "/free") {
		t.Fatalf("free text in the ring: %v", keys)
	}
}
