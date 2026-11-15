package audit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
)

const (
	aid  = "AAAAAAAAAAAAAAAAAAAAAA" // 22 characters
	exID = "0123456789abcdef0123456789abcdef"
)

func good() Fields {
	return Fields{"tool": "reply_permission", "attempt": "1", "attempt_id": aid, "decision": "reject",
		"permission_id": "per_abc123", "execution_id": exID, "session_id": "opencode:ses_abc",
		"native_tool": "bash", "kind": "bash", "scope_digest": "0123456789abcdef", "pattern_count": "2", "policy_mode": "reject_only",
		"policy_rule": "reject_always_allowed", "principal": "local"}
}

const goodLine = "INFO permission reply attempt event=permission_reply tool=reply_permission phase=attempt attempt=1 attempt_id=" + aid +
	" decision=reject permission_id=per_abc123 execution_id=" + exID + " session_id=opencode:ses_abc native_tool=bash kind=bash" +
	" scope_digest=0123456789abcdef pattern_count=2 policy_mode=reject_only policy_rule=reject_always_allowed principal=local decided_by=chat_client human_verified=false\n"

// capture routes the process logger (slog default and log package) to w with no timestamp, and
// restores both.
func capture(t *testing.T, w io.Writer) {
	t.Helper()
	pd, pw, pf, pp := slog.Default(), log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(w)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() { slog.SetDefault(pd); log.SetOutput(pw); log.SetFlags(pf); log.SetPrefix(pp) })
}

// native models the S5 call site: the native write happens only if the attempt record was written.
type native struct{ calls int }

func (n *native) attempt(w *Writer, f Fields) error {
	if err := w.Write(context.Background(), f); err != nil {
		return err
	}
	n.calls++
	return nil
}

type failWriter struct {
	calls int
	err   error
}

func (f *failWriter) Write(p []byte) (int, error) { f.calls++; return 0, f.err }

type shortWriter struct{ calls int }

func (s *shortWriter) Write(p []byte) (int, error) { s.calls++; return len(p) / 2, io.ErrShortWrite }

// TestShapeIsAudit01 — the success line has exactly the AUDIT-01 attribute shape, and equals the
// bytes an slog.Info call with the same attributes writes.
func TestShapeIsAudit01(t *testing.T) {
	var buf bytes.Buffer
	capture(t, &buf)
	w := newWriter()
	if err := w.Write(context.Background(), good()); err != nil {
		t.Fatal(err)
	}
	if buf.String() != goodLine {
		t.Fatalf("line = %q\nwant %q", buf.String(), goodLine)
	}
	attrs, err := good().attrs(PhaseAttempt)
	if err != nil {
		t.Fatal(err)
	}
	var ref bytes.Buffer
	log.SetOutput(&ref)
	slog.Info(Message, attrs...)
	if ref.String() != buf.String() {
		t.Fatalf("slog.Info writes %q, the attempt writer %q", ref.String(), buf.String())
	}
	if w.Unavailable() {
		t.Fatal("latched after a successful write")
	}
}

// TestReachesTee — with the dashboard tee installed the journal bytes are identical and the tee's
// journal branch is the one written (the same destination and lock).
func TestReachesTee(t *testing.T) {
	var plain, teed bytes.Buffer
	capture(t, &plain)
	if err := newWriter().Write(context.Background(), good()); err != nil {
		t.Fatal(err)
	}
	capture(t, &teed)
	restore := dashboard.InstallAuditTee(dashboard.NewAuditRing())
	defer restore()
	if err := newWriter().Write(context.Background(), good()); err != nil {
		t.Fatal(err)
	}
	if teed.String() != plain.String() || plain.String() != goodLine {
		t.Fatalf("teed %q\nplain %q", teed.String(), plain.String())
	}
}

// TestTeeJournalFailureIsReported — the failure of the journal destination behind the tee reaches
// the caller.
func TestTeeJournalFailureIsReported(t *testing.T) {
	fw := &failWriter{err: errors.New("disk full")}
	capture(t, fw)
	restore := dashboard.InstallAuditTee(dashboard.NewAuditRing())
	defer restore()
	w := newWriter()
	err := w.Write(context.Background(), good())
	if !errors.Is(err, ErrUnavailable) || !w.Unavailable() {
		t.Fatalf("err = %v, latched = %v", err, w.Unavailable())
	}
}

// TestFailureLatches — a failing, short or closed destination returns the error, sets the latch, and
// every later attempt is refused without calling the writer or the native fake.
func TestFailureLatches(t *testing.T) {
	closedFile := func() io.Writer {
		f, _ := os.CreateTemp(t.TempDir(), "j")
		f.Close()
		return f
	}
	brokenPipe := func() io.Writer {
		r, w, _ := os.Pipe()
		r.Close()
		t.Cleanup(func() { w.Close() })
		return w
	}
	cases := map[string]func() (io.Writer, *int){
		"error":       func() (io.Writer, *int) { f := &failWriter{err: io.ErrClosedPipe}; return f, &f.calls },
		"short":       func() (io.Writer, *int) { s := &shortWriter{}; return s, &s.calls },
		"closed file": func() (io.Writer, *int) { var n int; return closedFile(), &n },
		"EPIPE":       func() (io.Writer, *int) { var n int; return brokenPipe(), &n },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			dest, calls := mk()
			capture(t, dest)
			w := newWriter()
			var n native
			err := n.attempt(w, good())
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
			if !w.Unavailable() || n.calls != 0 {
				t.Fatalf("latched = %v, native calls = %d", w.Unavailable(), n.calls)
			}
			first := *calls
			// the destination recovers: the latch must hold anyway (until restart)
			var healthy bytes.Buffer
			log.SetOutput(&healthy)
			for i := 0; i < 3; i++ {
				if err := n.attempt(w, good()); !errors.Is(err, ErrUnavailable) {
					t.Fatalf("later attempt %d: err = %v", i, err)
				}
			}
			if *calls != first || healthy.Len() != 0 || n.calls != 0 {
				t.Fatalf("writer called %d more times, %d bytes written, native calls %d", *calls-first, healthy.Len(), n.calls)
			}
			if !w.Unavailable() {
				t.Fatal("latch reset by a later attempt")
			}
		})
	}
}

// TestLatchIsProcessWide — Default is one writer for every caller, and a failure through it refuses
// the next caller.
func TestLatchIsProcessWide(t *testing.T) {
	if Default() != Default() {
		t.Fatal("Default is not a single writer")
	}
	capture(t, &failWriter{err: io.ErrClosedPipe})
	d := Default()
	t.Cleanup(func() { d.unavailable.Store(false) }) // test isolation only: production has no reset
	if err := d.Write(context.Background(), good()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	if err := Default().Write(context.Background(), good()); !errors.Is(err, ErrUnavailable) || buf.Len() != 0 {
		t.Fatalf("second caller: err = %v, wrote %d bytes", err, buf.Len())
	}
}

// TestHandlerDisabledOrPanicIsFailure — a handler that does not accept Info, or panics, is a failed write.
func TestHandlerDisabledOrPanicIsFailure(t *testing.T) {
	for name, h := range map[string]slog.Handler{"disabled": disabled{}, "panic": panicky{}, "nil": nil} {
		t.Run(name, func(t *testing.T) {
			w := newWriter()
			w.handler = func() slog.Handler { return h }
			err := w.Write(context.Background(), good())
			if !errors.Is(err, ErrUnavailable) || !w.Unavailable() {
				t.Fatalf("err = %v, latched = %v", err, w.Unavailable())
			}
		})
	}
}

type disabled struct{ slog.Handler }

func (disabled) Enabled(context.Context, slog.Level) bool  { return false }
func (disabled) Handle(context.Context, slog.Record) error { return nil }

type panicky struct{ slog.Handler }

func (panicky) Enabled(context.Context, slog.Level) bool  { return true }
func (panicky) Handle(context.Context, slog.Record) error { panic("boom") }

// TestInvalidWritesNothingAndDoesNotLatch — refused fields never reach the journal and never set the latch.
func TestInvalidWritesNothingAndDoesNotLatch(t *testing.T) {
	secret := "sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	mut := map[string]func(Fields){
		"unknown key":           func(f Fields) { f["prompt"] = secret },
		"secret in free field":  func(f Fields) { f["reason"] = secret },
		"secret in principal":   func(f Fields) { f["principal"] = "x " + secret },
		"newline injection":     func(f Fields) { f["principal"] = "local\nINFO forged event=interrupt" },
		"space injection":       func(f Fields) { f["native_tool"] = "bash event=x" },
		"event override":        func(f Fields) { f["event"] = "interrupt" },
		"phase override":        func(f Fields) { f["phase"] = "result" },
		"decided_by override":   func(f Fields) { f["decided_by"] = "human" },
		"human_verified":        func(f Fields) { f["human_verified"] = "true" },
		"pattern text":          func(f Fields) { f["patterns"] = "/etc/shadow" },
		"missing attempt_id":    func(f Fields) { delete(f, "attempt_id") },
		"missing decision":      func(f Fields) { delete(f, "decision") },
		"bad decision":          func(f Fields) { f["decision"] = "always" },
		"bad attempt":           func(f Fields) { f["attempt"] = "3" },
		"bad attempt_id length": func(f Fields) { f["attempt_id"] = "short" },
		"bad session":           func(f Fields) { f["session_id"] = "claudecode:xyz" },
	}
	for name, m := range mut {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			capture(t, &buf)
			w := newWriter()
			f := good()
			m(f)
			if err := w.Write(context.Background(), f); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if buf.Len() != 0 || w.Unavailable() {
				t.Fatalf("wrote %q, latched = %v", buf.String(), w.Unavailable())
			}
			if err := w.Write(context.Background(), good()); err != nil {
				t.Fatalf("a valid write after an invalid one: %v", err)
			}
		})
	}
}

// TestNoSecretInLine — a secret-looking value in a non-allowlisted field never appears, and the line
// of a valid call carries only allowlisted keys.
func TestNoSecretInLine(t *testing.T) {
	var buf bytes.Buffer
	capture(t, &buf)
	f := good()
	f["token"] = "sk-ant-api03-SECRETSECRETSECRET"
	_ = newWriter().Write(context.Background(), f)
	_ = newWriter().Write(context.Background(), good())
	if strings.Contains(buf.String(), "SECRET") || strings.Contains(buf.String(), "token=") {
		t.Fatalf("secret reached the journal: %q", buf.String())
	}
	for _, kv := range strings.Fields(strings.TrimPrefix(buf.String(), "INFO permission reply attempt")) {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := fields[k]; !ok && k != "event" && k != "phase" && k != "decided_by" && k != "human_verified" {
			t.Fatalf("key %q is not allowlisted", k)
		}
	}
}

// TestConcurrentWithJournalTraffic — attempts, slog.Info and log.Printf at once through the tee: every
// line complete, none torn or interleaved, no deadlock (5 s deadline).
func TestConcurrentWithJournalTraffic(t *testing.T) {
	var buf bytes.Buffer // unsynchronised on purpose: the shared lock is what protects it (-race)
	capture(t, &buf)
	restore := dashboard.InstallAuditTee(dashboard.NewAuditRing())
	defer restore()
	w := newWriter()
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _ = w.Write(context.Background(), good()) }()
		go func() {
			defer wg.Done()
			slog.Info("explicit interrupt", "event", "interrupt", "tool", "interrupt_execution", "accepted", true)
		}()
		go func() { defer wg.Done(); log.Printf("plain log line") }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: concurrent writers did not finish in 5 s")
	}
	var attempts, interrupts, plain int
	for _, l := range strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		switch {
		case l+"\n" == goodLine:
			attempts++
		case l == "INFO explicit interrupt event=interrupt tool=interrupt_execution accepted=true":
			interrupts++
		case l == "plain log line" || l == "INFO plain log line":
			plain++
		default:
			t.Fatalf("torn or interleaved line: %q", l)
		}
	}
	if attempts != n || interrupts != n || plain != n {
		t.Fatalf("attempts=%d interrupts=%d plain=%d, want %d each", attempts, interrupts, plain, n)
	}
}

// P3-1 (#277 C): a tee over a Warn-level journal reports Info enabled (for the ring) but writes
// nothing to the journal; Write must not report that as a written attempt record.
func TestTeeOverWarnJournalIsNotAnAcceptedWrite(t *testing.T) {
	var journal bytes.Buffer
	tee := dashboard.NewAuditTee(slog.NewTextHandler(&journal, &slog.HandlerOptions{Level: slog.LevelWarn}), dashboard.NewAuditRing())
	w := newWriter()
	w.handler = func() slog.Handler { return tee }
	err := w.Write(context.Background(), good())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable (no journal line was written)", err)
	}
	if journal.Len() != 0 {
		t.Fatalf("journal got %q", journal.String())
	}
	if !w.Unavailable() {
		t.Fatal("the writer is not latched after a record that never reached the journal")
	}
	// the same tee over an Info journal still works, also through WithAttrs
	var ok bytes.Buffer
	tee2 := dashboard.NewAuditTee(slog.NewTextHandler(&ok, nil), dashboard.NewAuditRing()).WithAttrs([]slog.Attr{slog.String("k", "v")})
	w2 := newWriter()
	w2.handler = func() slog.Handler { return tee2 }
	if err := w2.Write(context.Background(), good()); err != nil || ok.Len() == 0 {
		t.Fatalf("Info journal: err = %v, wrote %d bytes", err, ok.Len())
	}
}

// P3-3 (#278 B): cross-field consistency the S3 call site can now state.
func TestCrossFieldConsistency(t *testing.T) {
	second := func(f Fields) { f["attempt"] = "2"; f["supersedes"] = "BBBBBBBBBBBBBBBBBBBBBB" }
	cases := map[string]func(Fields){
		"attempt 2 without supersedes":    func(f Fields) { f["attempt"] = "2" },
		"supersedes on attempt 1":         func(f Fields) { f["supersedes"] = "BBBBBBBBBBBBBBBBBBBBBB" },
		"supersedes equals attempt_id":    func(f Fields) { second(f); f["supersedes"] = aid },
		"attempt missing":                 func(f Fields) { delete(f, "attempt") },
		"allow_once under reject_only":    func(f Fields) { f["decision"] = "allow_once" },
		"native_tool without kind":        func(f Fields) { delete(f, "kind") },
		"scope_digest without pattern_ct": func(f Fields) { delete(f, "pattern_count") },
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			capture(t, &buf)
			w := newWriter()
			f := good()
			m(f)
			if err := w.Write(context.Background(), f); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if buf.Len() != 0 || w.Unavailable() {
				t.Fatalf("wrote %q, latched = %v", buf.String(), w.Unavailable())
			}
		})
	}
	// consistent combinations are accepted
	var buf bytes.Buffer
	capture(t, &buf)
	f := good()
	second(f)
	if err := newWriter().Write(context.Background(), f); err != nil {
		t.Fatalf("attempt 2 with supersedes: %v", err)
	}
	g := good()
	g["decision"] = "allow_once"
	g["policy_mode"] = "policy"
	if err := newWriter().Write(context.Background(), g); err != nil {
		t.Fatalf("allow_once under policy: %v", err)
	}
}

func resultFields() Fields {
	f := good()
	f["outcome"], f["accepted"] = "replied", "true"
	return f
}
func refusedFields() Fields {
	return Fields{"tool": "reply_permission", "outcome": "refused", "accepted": "false", "error_code": "NOT_FOUND", "reason": "resolved_elsewhere_or_dropped",
		"decision": "reject", "permission_id": "per_abc123", "execution_id": exID, "session_id": "opencode:ses_abc", "process_start": "2026-11-15T10:00:00Z",
		"policy_mode": "reject_only", "principal": "local"}
}

// ADR-024 section 6: one line per phase. The three phases share the AUDIT-01 shape, the writer and
// the latch; decided_by and human_verified are in every one of them.
func TestPhaseLinesAreClosedAndShareTheWriter(t *testing.T) {
	var buf bytes.Buffer
	capture(t, &buf)
	w := newWriter()
	ctx := context.Background()
	if err := w.WritePhase(ctx, PhaseResult, resultFields()); err != nil {
		t.Fatal(err)
	}
	if err := w.WritePhase(ctx, PhaseRefused, refusedFields()); err != nil {
		t.Fatal(err)
	}
	wantResult := "INFO permission reply result event=permission_reply tool=reply_permission phase=result attempt=1 attempt_id=" + aid +
		" outcome=replied accepted=true decision=reject permission_id=per_abc123 execution_id=" + exID + " session_id=opencode:ses_abc native_tool=bash kind=bash" +
		" scope_digest=0123456789abcdef pattern_count=2 policy_mode=reject_only policy_rule=reject_always_allowed principal=local decided_by=chat_client human_verified=false\n"
	wantRefused := "INFO permission reply refused event=permission_reply tool=reply_permission phase=refused outcome=refused accepted=false error_code=NOT_FOUND" +
		" decision=reject process_start=2026-11-15T10:00:00Z permission_id=per_abc123 execution_id=" + exID + " session_id=opencode:ses_abc policy_mode=reject_only" +
		" reason=resolved_elsewhere_or_dropped principal=local decided_by=chat_client human_verified=false\n"
	if buf.String() != wantResult+wantRefused {
		t.Fatalf("lines =\n%q\nwant\n%q", buf.String(), wantResult+wantRefused)
	}
}

func TestPhaseFieldRules(t *testing.T) {
	type tc struct {
		name  string
		phase string
		f     func() Fields
	}
	del := func(k string) func(Fields) { return func(f Fields) { delete(f, k) } }
	set := func(k, v string) func(Fields) { return func(f Fields) { f[k] = v } }
	with := func(base func() Fields, m ...func(Fields)) func() Fields {
		return func() Fields {
			f := base()
			for _, x := range m {
				x(f)
			}
			return f
		}
	}
	cases := []tc{
		{"result without outcome", PhaseResult, with(resultFields, del("outcome"))},
		{"result without accepted", PhaseResult, with(resultFields, del("accepted"))},
		{"result without attempt_id", PhaseResult, with(resultFields, del("attempt_id"))},
		{"result outcome outside the set", PhaseResult, with(resultFields, set("outcome", "weird"))},
		{"replied but not accepted", PhaseResult, with(resultFields, set("accepted", "false"))},
		{"replied with an error_code", PhaseResult, with(resultFields, set("error_code", "NOT_FOUND"))},
		{"unknown_outcome accepted", PhaseResult, with(resultFields, set("outcome", "unknown_outcome"), set("error_code", "UNKNOWN_OUTCOME"))},
		{"unknown_outcome without error_code", PhaseResult, with(resultFields, set("outcome", "unknown_outcome"), set("accepted", "false"))},
		{"refused with an attempt number only", PhaseRefused, with(refusedFields, set("attempt", "1"))},
		{"refused carrying a native description", PhaseRefused, with(refusedFields, set("native_tool", "bash"), set("kind", "bash"), set("scope_digest", "0123456789abcdef"), set("pattern_count", "1"))},
		{"refused accepted", PhaseRefused, with(refusedFields, set("accepted", "true"))},
		{"refused with another outcome", PhaseRefused, with(refusedFields, set("outcome", "replied"))},
		{"refused without error_code", PhaseRefused, with(refusedFields, del("error_code"))},
		{"free text in a refused reason", PhaseRefused, with(refusedFields, set("reason", "because the user said /etc/shadow"))},
		{"unknown phase", "bogus", with(refusedFields)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			capture(t, &buf)
			w := newWriter()
			if err := w.WritePhase(context.Background(), c.phase, c.f()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if buf.Len() != 0 || w.Unavailable() {
				t.Fatalf("wrote %q latched %v", buf.String(), w.Unavailable())
			}
		})
	}
	// good shapes: replay, a recorded UNKNOWN_OUTCOME quoted on a refused line, a failed result
	var buf bytes.Buffer
	capture(t, &buf)
	w := newWriter()
	for name, f := range map[string]struct {
		phase string
		f     Fields
	}{
		"replay":                           {PhaseResult, with(resultFields, set("outcome", "replay"))()},
		"unknown result":                   {PhaseResult, with(resultFields, set("outcome", "unknown_outcome"), set("accepted", "false"), set("error_code", "UNKNOWN_OUTCOME"))()},
		"not found result":                 {PhaseResult, with(resultFields, set("outcome", "not_found"), set("accepted", "false"), set("error_code", "NOT_FOUND"), set("reason", "resolved_elsewhere_or_dropped"))()},
		"refused with a recorded envelope": {PhaseRefused, with(refusedFields, set("error_code", "UNKNOWN_OUTCOME"), set("attempt", "1"), set("attempt_id", aid))()},
		"refused minimal":                  {PhaseRefused, Fields{"tool": "reply_permission", "outcome": "refused", "accepted": "false", "error_code": "INVALID_INPUT"}},
	} {
		if err := w.WritePhase(context.Background(), f.phase, f.f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestCleanDropsEverythingOutsideTheClosedShapes(t *testing.T) {
	in := Fields{"tool": "reply_permission", "permission_id": "../etc/passwd", "decision": "always", "execution_id": exID, "prompt": "secret", "reason": "free text here", "principal": "local"}
	got := Clean(in)
	if len(got) != 3 || got["tool"] != "reply_permission" || got["execution_id"] != exID || got["principal"] != "local" {
		t.Fatalf("Clean = %v", got)
	}
	if _, ok := got["prompt"]; ok {
		t.Fatal("a non-allowlisted key survived Clean")
	}
}

// Review #277 P3-2, decided at S5: the write honours its context. A journal that hangs past the
// deadline latches the writer and refuses the call; a caller that merely went away refuses the
// call without latching.
func TestWriteIsBoundedByTheContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	w := newWriter()
	w.handler = func() slog.Handler { return hangHandler{release} }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	errc := make(chan error, 1)
	go func() { errc <- w.Write(ctx, good()) }()
	var err error
	select {
	case err = <-errc:
	case <-time.After(3 * time.Second):
		t.Fatal("Write did not return: the attempt write is not bounded by its context")
	}
	if !errors.Is(err, ErrUnavailable) || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
	if !w.Unavailable() {
		t.Fatal("a journal that hung past the deadline did not latch the writer")
	}
	// the latch refuses the next call without calling the handler
	if err := w.Write(context.Background(), good()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	// a canceled caller: refused, not latched
	w2 := newWriter()
	w2.handler = func() slog.Handler { return hangHandler{release} }
	cctx, ccancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); ccancel() }()
	if err := w2.Write(cctx, good()); !errors.Is(err, ErrUnavailable) || w2.Unavailable() {
		t.Fatalf("canceled: err = %v latched = %v", err, w2.Unavailable())
	}
	// an already-done context: nothing is written, nothing latched
	var buf bytes.Buffer
	capture(t, &buf)
	w3 := newWriter()
	dctx, dcancel := context.WithCancel(context.Background())
	dcancel()
	if err := w3.Write(dctx, good()); !errors.Is(err, ErrUnavailable) || w3.Unavailable() || buf.Len() != 0 {
		t.Fatalf("done ctx: err = %v latched = %v wrote %q", err, w3.Unavailable(), buf.String())
	}
}

type hangHandler struct{ release chan struct{} }

func (hangHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h hangHandler) Handle(context.Context, slog.Record) error {
	<-h.release
	return nil
}
func (h hangHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h hangHandler) WithGroup(string) slog.Handler      { return h }

// Review #277 P3-2 / #285 B P3-2: the bounds of the tool, the manager and the audit field agree.
func TestMaximumLengthIdsAreAccepted(t *testing.T) {
	var buf bytes.Buffer
	capture(t, &buf)
	f := good()
	f["permission_id"] = "per" + strings.Repeat("a", 125)
	f["session_id"] = "opencode:ses" + strings.Repeat("b", 253)
	if err := newWriter().Write(context.Background(), f); err != nil {
		t.Fatalf("maximum-length ids refused: %v", err)
	}
	f["permission_id"] = "per" + strings.Repeat("a", 126)
	if err := newWriter().Write(context.Background(), f); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an over-long permission id: %v", err)
	}
}

func TestSetJournalForTestingClearsAndRestoresTheLatch(t *testing.T) {
	w := newWriter()
	w.unavailable.Store(true)
	var buf bytes.Buffer
	restore := w.SetJournalForTesting(slog.NewTextHandler(&buf, nil))
	if w.Unavailable() {
		t.Fatal("the latch survived the seam")
	}
	if err := w.Write(context.Background(), good()); err != nil || buf.Len() == 0 {
		t.Fatalf("%v %q", err, buf.String())
	}
	w.unavailable.Store(true)
	restore()
	if w.Unavailable() {
		t.Fatal("restore left the latch set")
	}
}

// Review #293 P3-2: the attempt line accepts exactly the kinds the Bridge accepts from the
// observer (safeTool: [A-Za-z0-9_.:-]{1,64}); anything outside that closed charset still refuses.
func TestKindShapeMatchesTheBridgesAcceptedShape(t *testing.T) {
	var buf bytes.Buffer
	capture(t, &buf)
	for _, kind := range []string{"edit", "Bash2", "mcp.tool", "mcp:server-tool_1", strings.Repeat("k", 64)} {
		f := good()
		f["kind"] = kind
		if err := newWriter().Write(context.Background(), f); err != nil {
			t.Errorf("kind %q refused: %v", kind, err)
		}
	}
	for name, kind := range map[string]string{
		"65 characters": strings.Repeat("k", 65), "empty": "", "space": "a b", "equals": "a=b", "quote": `a"b`, "newline": "a\nb",
		"slash": "a/b", "unicode": "k\u00e9", "forged attribute": "x event=interrupt",
	} {
		f := good()
		f["kind"] = kind
		w := newWriter()
		if err := w.Write(context.Background(), f); !errors.Is(err, ErrInvalid) || w.Unavailable() {
			t.Errorf("%s: err = %v latched = %v, want ErrInvalid without a latch", name, err, w.Unavailable())
		}
	}
}
