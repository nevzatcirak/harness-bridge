package mcp

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"strings"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

const goldenExec = "0123456789abcdef0123456789abcdef"

// writeAuditLines drives the REAL AUDIT-01 writer of this package through every outcome
// kind: new, replay, rejected, read-only denied, interrupt accepted and rejected, and an
// input the writer must drop (a malformed session ID).
func writeAuditLines(s *Server) {
	ctx := context.Background()
	x := execution.Execution{ID: goldenExec, SessionID: "opencode:ses_abc"}
	s.audit(ctx, &auditRecord{tool: "send_prompt"}, args{SessionID: "opencode:ses_abc", IdempotencyKey: "key-1234567890"}, x, nil)
	s.audit(ctx, &auditRecord{tool: "send_prompt", replay: true}, args{SessionID: "opencode:ses_abc", IdempotencyKey: "key-1234567890"}, x, nil)
	s.audit(ctx, &auditRecord{tool: "send_prompt"}, args{SessionID: "opencode:ses_abc", IdempotencyKey: "key-other-0000"}, nil, execution.Fail("SESSION_BUSY", "busy <b>"))
	s.audit(ctx, &auditRecord{tool: "send_prompt", readOnly: true}, args{SessionID: "opencode:ses_abc", IdempotencyKey: "key-1234567890"}, nil, execution.Fail("FORBIDDEN", "mutations disabled on this bridge"))
	s.audit(ctx, &auditRecord{tool: "interrupt_execution"}, args{ExecutionID: goldenExec}, x, nil)
	s.audit(ctx, &auditRecord{tool: "interrupt_execution"}, args{ExecutionID: "not-an-id"}, nil, execution.Fail("NOT_FOUND", "x"))
	s.audit(ctx, &auditRecord{tool: "send_prompt"}, args{SessionID: "opencode:../../etc/passwd", Prompt: "SECRET PROMPT", IdempotencyKey: "k"}, nil, execution.Fail("INVALID_INPUT", "bad"))
}

func captureJournal(t *testing.T, install bool, write func()) (string, []dashboard.AuditEntry) {
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

// Slice S4, journal branch: with the tee installed the AUDIT-01 output of the real mcp
// writer is byte-identical, and the ring gets exactly the allowlisted projection.
func TestMCPAuditJournalIsByteIdenticalWithTheTee(t *testing.T) {
	s := &Server{}
	without, _ := captureJournal(t, false, func() { writeAuditLines(s) })
	with, entries := captureJournal(t, true, func() { writeAuditLines(s) })
	if without == "" || strings.Count(without, "\n") != 7 {
		t.Fatalf("the baseline journal has %d lines, want 7:\n%s", strings.Count(without, "\n"), without)
	}
	if without != with {
		t.Fatalf("the journal changed under the tee\n--- without:\n%s\n--- with:\n%s", without, with)
	}
	if len(entries) != 7 {
		t.Fatalf("the ring holds %d entries, want 7", len(entries))
	}
	for _, e := range entries {
		if e.Event != "prompt_submission" && e.Event != "interrupt" {
			t.Errorf("unexpected event %q", e.Event)
		}
		for _, a := range e.Attrs {
			if strings.Contains(a.Value, "SECRET") || strings.Contains(a.Value, "passwd") {
				t.Errorf("an untrusted value reached the ring: %+v", a)
			}
		}
		if e.Attr("principal") != "local" || e.Attr("tool") == "" {
			t.Errorf("entry %+v", e)
		}
	}
	// Newest first: the last line written is the first entry.
	if entries[0].Attr("outcome") != "rejected" || entries[0].Attr("error_code") != "INVALID_INPUT" || entries[6].Attr("outcome") != "new" || entries[6].Attr("idem") == "" {
		t.Errorf("order or content: first %+v last %+v", entries[0], entries[6])
	}
	if strings.Contains(with, "SECRET PROMPT") || strings.Contains(with, "key-1234567890") {
		t.Errorf("the journal carries the prompt or a raw key")
	}
}
