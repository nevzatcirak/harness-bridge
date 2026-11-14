package admin

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

func captureAdminJournal(t *testing.T, install bool, write func()) (string, []dashboard.AuditEntry) {
	t.Helper()
	var buf bytes.Buffer
	prevW, prevF, prevD := log.Writer(), log.Flags(), slog.Default()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prevW); log.SetFlags(prevF); slog.SetDefault(prevD) }()
	ring := dashboard.NewAuditRing()
	if install {
		defer dashboard.InstallAuditTee(ring)()
	}
	write()
	return buf.String(), ring.Entries()
}

// The admin server logs through slog.Default() at call time, so the one tee covers its
// guard_release lines too (ADR-023 section 1.1); the journal bytes do not change.
func TestAdminGuardReleaseJournalIsByteIdenticalWithTheTee(t *testing.T) {
	s := &Server{} // no Logger: slog.Default()
	write := func() {
		req := Request{Op: OpReleaseGuard, SessionID: "opencode:ses_abc", ExecutionID: "0123456789abcdef0123456789abcdef", Ticket: "T-1"}
		s.audit("admin:uid=996", req, &execution.ReleaseEvidence{NativeRead: true, NativeState: "idle", Unfinished: false, PermissionPending: false, CorrelatedUser: "msg_1", SettleMS: 250}, nil)
		s.audit("admin:uid=996", Request{Op: OpReleaseGuard, SessionID: "opencode:ses_abc", AllowMissingUserMessage: true}, nil, &execution.Error{Code: "INVALID_INPUT", Message: "x"})
		s.audit("admin:uid=unknown", Request{SessionID: "<script>", ExecutionID: "zz"}, nil, &execution.Error{Code: "FORBIDDEN", Message: "peer is not the service user"})
	}
	without, _ := captureAdminJournal(t, false, write)
	with, entries := captureAdminJournal(t, true, write)
	if without == "" || strings.Count(without, "\n") != 3 {
		t.Fatalf("baseline journal:\n%s", without)
	}
	if without != with {
		t.Fatalf("the journal changed under the tee\n--- without:\n%s\n--- with:\n%s", without, with)
	}
	if len(entries) != 3 {
		t.Fatalf("the ring holds %d entries, want 3", len(entries))
	}
	for _, e := range entries {
		if e.Event != "guard_release" || e.Attr("tool") != "admin" || !strings.HasPrefix(e.Attr("principal"), "admin:uid=") {
			t.Errorf("entry %+v", e)
		}
		if strings.Contains(e.Attr("session_id"), "script") {
			t.Errorf("an unvalidated session ID reached the ring: %+v", e)
		}
	}
	rel := entries[2]
	if rel.Attr("outcome") != "released" || rel.Attr("native_state") != "idle" || rel.Attr("correlated_user") != "msg_1" || rel.Attr("settle_ms") != "250" || rel.Attr("ticket") != "T-1" || rel.Attr("unfinished") != "false" {
		t.Errorf("guard_release projection: %+v", rel)
	}
	if entries[0].Attr("outcome") != "refused" || entries[0].Attr("error_code") != "FORBIDDEN" {
		t.Errorf("newest first: %+v", entries[0])
	}
}
