package mcp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/cirak/internal/audit"
	"github.com/nevzatcirak/cirak/internal/execution"
)

// ADR-024 S5: one line per phase on every return path, written through the failure-reporting
// writer; decided_by and human_verified in every line.

func (e *relayEnv) counts() (attempt, result, refused int) {
	return e.aw.n(), len(e.aw.linesOf("result")), len(e.aw.linesOf("refused"))
}

func TestEveryReturnPathWritesExactlyOneClosingLine(t *testing.T) {
	type tc struct {
		name           string
		setup          func(*relayEnv)
		args           func(*relayEnv) args
		wantAttempt    int
		wantResult     int
		wantRefused    int
		wantRefusedErr string
	}
	same := func(e *relayEnv) args { return e.args("reject") }
	cases := []tc{
		{"success: attempt then result", nil, same, 1, 1, 0, ""},
		{"read-only denial", func(e *relayEnv) { e.s.ReadOnly = true }, same, 0, 0, 1, "FORBIDDEN"},
		{"allow_once_disabled", nil, func(e *relayEnv) args { return e.args("allow_once") }, 0, 0, 1, "FORBIDDEN"},
		{"unknown execution", nil, func(e *relayEnv) args {
			a := e.args("reject")
			a.ExecutionID = "0123456789abcdef0123456789abcdef"
			return a
		}, 0, 0, 1, "NOT_FOUND"},
		{"claude execution", func(e *relayEnv) {
			f := newFakeClaude()
			f.execs[e.execID] = execution.Execution{ID: e.execID}
			e.s.Claude = f
		}, same, 0, 0, 1, "INVALID_INPUT"},
		{"request gone (manager refusal, before any attempt)", func(e *relayEnv) { e.src.set(func() { e.src.gone = true }) }, same, 0, 0, 1, "NOT_FOUND"},
		{"native lost response: attempt then a result line", func(e *relayEnv) { e.rep.err = errors.New("lost") }, same, 1, 1, 0, ""},
		{"attempt record failure", func(e *relayEnv) { e.aw.err = audit.ErrUnavailable }, same, 0, 0, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newRelayEnv(t, "reject_only")
			if c.setup != nil {
				c.setup(e)
			}
			_, err := e.s.call(marked(), "reply_permission", c.args(e))
			a, r, f := e.counts()
			if a != c.wantAttempt || r != c.wantResult || f != c.wantRefused {
				t.Fatalf("lines attempt/result/refused = %d/%d/%d, want %d/%d/%d (err %v)", a, r, f, c.wantAttempt, c.wantResult, c.wantRefused, err)
			}
			if c.wantRefused == 1 {
				l := e.aw.linesOf("refused")[0]
				if l["error_code"] != c.wantRefusedErr || l["outcome"] != "refused" || l["accepted"] != "false" || l["principal"] != "local" {
					t.Fatalf("refused line = %v", l)
				}
			}
			if c.wantAttempt == 0 && e.rep.n() != 0 {
				t.Fatal("a call with no attempt line made a native write")
			}
		})
	}
}

// Replays that send nothing still write one line (a result line with outcome replay for a recorded
// success, a refused line for the recorded UNKNOWN_OUTCOME).
func TestReplaysWriteOneLineAndSendNothing(t *testing.T) {
	e := newRelayEnv(t, "reject_only")
	if _, err := e.s.call(marked(), "reply_permission", e.args("reject")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.call(marked(), "reply_permission", e.args("reject")); err != nil {
		t.Fatal(err)
	}
	a, r, f := e.counts()
	if a != 1 || r != 2 || f != 0 || e.rep.n() != 1 {
		t.Fatalf("attempt/result/refused = %d/%d/%d writes %d", a, r, f, e.rep.n())
	}
	if l := e.aw.linesOf("result")[1]; l["outcome"] != "replay" || l["accepted"] != "true" {
		t.Fatalf("replay line = %v", l)
	}
	e2 := newRelayEnv(t, "reject_only")
	e2.rep.err = errors.New("lost")
	_, _ = e2.s.call(marked(), "reply_permission", e2.args("reject"))
	_, err := e2.s.call(marked(), "reply_permission", e2.args("reject"))
	if code(t, err).Code != "UNKNOWN_OUTCOME" {
		t.Fatal(err)
	}
	a, r, f = e2.counts()
	if a != 1 || r != 1 || f != 1 || e2.rep.n() != 1 {
		t.Fatalf("attempt/result/refused = %d/%d/%d writes %d", a, r, f, e2.rep.n())
	}
	if l := e2.aw.linesOf("refused")[0]; l["error_code"] != "UNKNOWN_OUTCOME" || l["attempt_id"] == "" {
		t.Fatalf("recorded envelope line = %v", l)
	}
}

// An input the SDK rejects before the handler (AUDIT-01b) still gets its one refused line, with
// only closed-shape values: a malformed id and a smuggled field never reach the journal.
func TestSchemaRejectedInputWritesARefusedLine(t *testing.T) {
	e := newRelayEnv(t, "reject_only")
	client := e.connect()
	type tc struct {
		args map[string]any
		// the closed-shape identifiers of the input that must reach the line, and the keys that must not
		want   map[string]string
		absent []string
	}
	good := func(m map[string]any) map[string]any {
		out := map[string]any{"execution_id": e.execID, "permission_id": "per_0001", "decision": "reject"}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	cases := []tc{
		{good(map[string]any{"permission_id": "per/../../etc/passwd"}), map[string]string{"execution_id": e.execID, "decision": "reject"}, []string{"permission_id"}},
		{good(map[string]any{"decision": "always"}), map[string]string{"execution_id": e.execID, "permission_id": "per_0001"}, []string{"decision"}},
		{good(map[string]any{"message": "SECRET-TEXT"}), map[string]string{"execution_id": e.execID, "permission_id": "per_0001", "decision": "reject"}, nil},
		{good(map[string]any{"supersedes": "x"}), map[string]string{"execution_id": e.execID, "permission_id": "per_0001", "decision": "reject"}, []string{"supersedes"}},
	}
	for _, c := range cases {
		before := len(e.aw.linesOf("refused"))
		r, err := client.CallTool(context.Background(), &sdk.CallToolParams{Name: "reply_permission", Arguments: c.args})
		if err == nil && (r == nil || !r.IsError) {
			t.Fatalf("%v accepted", c.args)
		}
		lines := e.aw.linesOf("refused")
		if len(lines) != before+1 {
			t.Fatalf("%v: refused lines %d -> %d, want one more", c.args, before, len(lines))
		}
		l := lines[len(lines)-1]
		for k, v := range l {
			if strings.Contains(v, "SECRET") || strings.Contains(v, "passwd") || strings.Contains(v, "always") {
				t.Fatalf("input text reached the line: %s=%q", k, v)
			}
		}
		for k, v := range c.want {
			if l[k] != v {
				t.Fatalf("%v: the well-formed %s did not reach the refused line: %v", c.args, k, l)
			}
		}
		for _, k := range c.absent {
			if _, ok := l[k]; ok {
				t.Fatalf("%v: malformed %s reached the refused line: %v", c.args, k, l)
			}
		}
		if l["error_code"] != "INVALID_INPUT" {
			t.Fatalf("refused line = %v", l)
		}
	}
	if e.rep.n() != 0 || e.aw.n() != 0 {
		t.Fatal("a rejected input reached the native side or wrote an attempt")
	}
}

// With the REAL process-wide writer: every journal line of a call carries decided_by=chat_client
// and human_verified=false, and no line carries input text, a pattern or native text.
func TestRealWriterLinesCarryDecidedByAndHumanVerified(t *testing.T) {
	var journal syncBuffer
	restore := audit.Default().SetJournalForTesting(slog.NewTextHandler(&journal, nil))
	defer restore()
	e := newRelayEnv(t, "reject_only")
	e.m.SetAttemptWriter(audit.Default())
	if _, err := e.s.call(marked(), "reply_permission", e.args("reject")); err != nil { // attempt + result
		t.Fatal(err)
	}
	_, _ = e.s.call(marked(), "reply_permission", e.args("allow_once")) // refused
	_, _ = e.s.call(marked(), "reply_permission", e.args("reject"))     // replay
	lines := strings.Split(strings.TrimSpace(journal.String()), "\n")
	var phases []string
	for _, l := range lines {
		if !strings.Contains(l, "event=permission_reply") {
			continue
		}
		if !strings.Contains(l, "decided_by=chat_client") || !strings.Contains(l, "human_verified=false") {
			t.Fatalf("a line lacks decided_by/human_verified: %s", l)
		}
		for _, banned := range []string{"patterns", "message=", "prompt", "token"} {
			if strings.Contains(l, banned) {
				t.Fatalf("line carries %q: %s", banned, l)
			}
		}
		for _, p := range []string{"phase=attempt", "phase=result", "phase=refused"} {
			if strings.Contains(l, p) {
				phases = append(phases, p)
			}
		}
	}
	if strings.Join(phases, ",") != "phase=attempt,phase=result,phase=refused,phase=result" {
		t.Fatalf("phases = %v\n%s", phases, journal.String())
	}
}

// Attempt-line failure on the production writer: zero native writes, audit_unavailable, and every
// later call refused (the process-wide latch), whatever the call.
func TestAttemptLineFailureOnTheRealWriterMeansZeroNativeWrites(t *testing.T) {
	restore := audit.Default().SetJournalForTesting(failingHandler{})
	defer restore()
	e := newRelayEnv(t, "reject_only")
	e.m.SetAttemptWriter(audit.Default())
	_, err := e.s.call(marked(), "reply_permission", e.args("reject"))
	if x := code(t, err); x.Code != "UPSTREAM_FAILURE" || x.Reason != execution.ReasonAuditUnavailable || !x.Retryable {
		t.Fatalf("%+v", x)
	}
	if e.rep.n() != 0 {
		t.Fatalf("%d native writes after a failed attempt line", e.rep.n())
	}
	if !audit.Default().Unavailable() {
		t.Fatal("the process-wide latch is not set")
	}
	reads := e.src.count()
	_, err = e.s.call(marked(), "reply_permission", e.args("reject"))
	if code(t, err).Reason != execution.ReasonAuditUnavailable || e.rep.n() != 0 || e.src.count() != reads {
		t.Fatalf("a later call was served: %v writes %d reads %d->%d", err, e.rep.n(), reads, e.src.count())
	}
}

type failingHandler struct{}

func (failingHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (failingHandler) Handle(context.Context, slog.Record) error { return errors.New("journal gone") }
func (h failingHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h failingHandler) WithGroup(string) slog.Handler           { return h }
