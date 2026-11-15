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
		"native_tool": "bash", "kind": "bash", "pattern_count": "2", "policy_mode": "reject_only",
		"policy_rule": "reject_always_allowed", "principal": "local"}
}

const goodLine = "INFO permission reply attempt event=permission_reply tool=reply_permission phase=attempt attempt=1 attempt_id=" + aid +
	" decision=reject permission_id=per_abc123 execution_id=" + exID + " session_id=opencode:ses_abc native_tool=bash kind=bash" +
	" pattern_count=2 policy_mode=reject_only policy_rule=reject_always_allowed principal=local decided_by=chat_client human_verified=false\n"

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
	w := NewWriter()
	if err := w.Write(context.Background(), good()); err != nil {
		t.Fatal(err)
	}
	if buf.String() != goodLine {
		t.Fatalf("line = %q\nwant %q", buf.String(), goodLine)
	}
	attrs, err := good().attrs()
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
	if err := NewWriter().Write(context.Background(), good()); err != nil {
		t.Fatal(err)
	}
	capture(t, &teed)
	restore := dashboard.InstallAuditTee(dashboard.NewAuditRing())
	defer restore()
	if err := NewWriter().Write(context.Background(), good()); err != nil {
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
	w := NewWriter()
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
			w := NewWriter()
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
			w := NewWriter()
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
			w := NewWriter()
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
	_ = NewWriter().Write(context.Background(), f)
	_ = NewWriter().Write(context.Background(), good())
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
	w := NewWriter()
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

func TestRefusal(t *testing.T) {
	r := Refusal()
	if r.Code != "UPSTREAM_FAILURE" || !r.Retryable || !strings.HasPrefix(r.Message, "audit_unavailable") {
		t.Fatalf("%+v", r)
	}
}
