package dashboard

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// JournalEnabled tells a caller whether the journal branch alone wants a record: Enabled is true
// when either branch does (the ring shows Info), so it cannot answer that. The failure-reporting
// attempt writer (internal/audit) relies on it.
func TestAuditTeeJournalEnabledIsTheJournalBranchOnly(t *testing.T) {
	ctx := context.Background()
	warn := NewAuditTee(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}), NewAuditRing())
	info := NewAuditTee(slog.NewTextHandler(io.Discard, nil), NewAuditRing())
	for name, tc := range map[string]struct {
		h              slog.Handler
		enabled, jgate bool
	}{
		"warn journal": {warn, true, false},
		"info journal": {info, true, true},
		"with attrs":   {warn.WithAttrs([]slog.Attr{slog.String("k", "v")}), true, false},
		"with group":   {info.WithGroup("g"), true, true},
	} {
		g, ok := tc.h.(interface {
			JournalEnabled(context.Context, slog.Level) bool
		})
		if !ok {
			t.Fatalf("%s: the tee does not expose JournalEnabled", name)
		}
		if tc.h.Enabled(ctx, slog.LevelInfo) != tc.enabled || g.JournalEnabled(ctx, slog.LevelInfo) != tc.jgate {
			t.Fatalf("%s: Enabled=%v JournalEnabled=%v, want %v %v", name, tc.h.Enabled(ctx, slog.LevelInfo), g.JournalEnabled(ctx, slog.LevelInfo), tc.enabled, tc.jgate)
		}
	}
}
