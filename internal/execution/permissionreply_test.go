package execution

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/audit"
)

// events is the shared, ordered log of every observable side effect, so a test can assert
// "the attempt record was written before the native call" and "nothing was written natively".
type events struct {
	mu   sync.Mutex
	log  []string
	hook func() // runs inside the call under test's permission read (marked context only)
}

func (e *events) setHook(f func()) { e.mu.Lock(); e.hook = f; e.mu.Unlock() }
func (e *events) runHook() {
	e.mu.Lock()
	f := e.hook
	e.mu.Unlock()
	if f != nil {
		f()
	}
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, s)
}
func (e *events) count(prefix string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, s := range e.log {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}
func (e *events) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

type replierFake struct {
	ev      *events
	mu      sync.Mutex
	errs    []error // consumed one per call; nil entry or exhausted = success
	args    [][3]string
	dirs    []string
	panicOn bool
	entered chan struct{} // closed on the first call when set
	hold    chan struct{} // blocks every call until closed when set
}

func (r *replierFake) ReplyPermission(ctx context.Context, session, directory, id, reply string) error {
	r.ev.add("native:" + reply)
	r.mu.Lock()
	r.args = append(r.args, [3]string{session, id, reply})
	r.dirs = append(r.dirs, directory)
	var err error
	if len(r.errs) > 0 {
		err, r.errs = r.errs[0], r.errs[1:]
	}
	entered, hold, pan := r.entered, r.hold, r.panicOn
	r.entered = nil
	r.mu.Unlock()
	if pan {
		panic("replier panic")
	}
	if entered != nil {
		close(entered)
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// arm makes the next call report entry and then block until hold is closed.
func (r *replierFake) arm() (entered, hold chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entered, r.hold = make(chan struct{}), make(chan struct{})
	return r.entered, r.hold
}
func (r *replierFake) failWith(errs ...error) {
	r.mu.Lock()
	r.errs = errs
	r.mu.Unlock()
}
func (r *replierFake) calls() [][3]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][3]string(nil), r.args...)
}

type phaseLine struct {
	phase string
	f     audit.Fields
}

type attemptFake struct {
	ev        *events
	mu        sync.Mutex
	errs      []error
	lines     []audit.Fields
	phases    []phaseLine
	latched   bool
	phaseErrs []error
}

func (a *attemptFake) Unavailable() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.latched }
func (a *attemptFake) latch()            { a.mu.Lock(); a.latched = true; a.mu.Unlock() }

// WritePhase records the result and refused lines apart from the attempt lines.
func (a *attemptFake) WritePhase(_ context.Context, phase string, f audit.Fields) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ev.add("line:" + phase)
	if len(a.phaseErrs) > 0 {
		var err error
		err, a.phaseErrs = a.phaseErrs[0], a.phaseErrs[1:]
		if err != nil {
			a.latched = true
			return err
		}
	}
	c := audit.Fields{}
	for k, v := range f {
		c[k] = v
	}
	if _, err := audit.CheckForTesting(phase, c); err != nil {
		return err
	}
	a.phases = append(a.phases, phaseLine{phase, c})
	return nil
}
func (a *attemptFake) phaseLines(phase string) []audit.Fields {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []audit.Fields
	for _, l := range a.phases {
		if l.phase == phase {
			out = append(out, l.f)
		}
	}
	return out
}

func (a *attemptFake) Write(_ context.Context, f audit.Fields) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ev.add("attempt")
	if len(a.errs) > 0 {
		var err error
		err, a.errs = a.errs[0], a.errs[1:]
		if err != nil {
			return err
		}
	}
	c := audit.Fields{}
	for k, v := range f {
		c[k] = v
	}
	if _, err := audit.CheckForTesting(audit.PhaseAttempt, c); err != nil {
		return err
	}
	a.lines = append(a.lines, c)
	return nil
}
func (a *attemptFake) failWith(errs ...error) {
	a.mu.Lock()
	a.errs = errs
	a.mu.Unlock()
}
func (a *attemptFake) written() []audit.Fields {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Fields(nil), a.lines...)
}

// replyCallKey marks a context created by the test for the call under test: the manager's own
// S1 monitor keeps reading the permission source while the execution waits, so only reads made
// on a marked context count as reads by ReplyPermission.
type replyCallKey struct{}

type markedSource struct {
	inner *sourceFake
	ev    *events
}

func (s markedSource) PermissionView(ctx context.Context, session string) (opencode.PermissionObservationBatch, error) {
	if ctx.Value(replyCallKey{}) != nil {
		s.ev.add("read")
		s.ev.runHook()
	}
	return s.inner.PermissionView(ctx, session)
}

type replyFixture struct {
	t    *testing.T
	m    *Manager
	f    *fake
	x    Execution
	src  *sourceFake
	rep  *replierFake
	aw   *attemptFake
	ev   *events
	exec string
	mode func(opencode.ObservedPermission) opencode.ObservedPermission
	mu   sync.Mutex
	gone bool
}

// newReplyFixture drives a fake runner to waiting_for_permission with one verified pending
// request (testPerm) and installs the source, replier and attempt writer fakes.
func newReplyFixture(t *testing.T) *replyFixture {
	t.Helper()
	fx := &replyFixture{t: t, ev: &events{}}
	fx.rep = &replierFake{ev: fx.ev}
	fx.aw = &attemptFake{ev: fx.ev}
	fx.src = &sourceFake{}
	fx.src.build = func(session string) opencode.PermissionObservationBatch {
		fx.mu.Lock()
		defer fx.mu.Unlock()
		if fx.gone {
			return batchOf("available")
		}
		p := observed(session, fx.exec)
		if fx.mode != nil {
			p = fx.mode(p)
		}
		return batchOf("available", p)
	}
	fx.m, fx.f, fx.x = waitingExecution(t, nil, fx.src)
	fx.m.SetPermissionSource(markedSource{fx.src, fx.ev})
	fx.mu.Lock()
	fx.exec = fx.x.ID
	fx.mu.Unlock()
	fx.m.SetPermissionReplier(fx.rep)
	fx.m.SetAttemptWriter(fx.aw)
	// Forget the reads the S1 monitor did while the execution was reaching its state.
	fx.ev.mu.Lock()
	fx.ev.log = nil
	fx.ev.mu.Unlock()
	return fx
}
func (fx *replyFixture) ctx() context.Context {
	return context.WithValue(context.Background(), replyCallKey{}, true)
}
func (fx *replyFixture) in() ReplyInput {
	return ReplyInput{ExecutionID: fx.x.ID, PermissionID: testPerm, Decision: "reject", Principal: "local"}
}
func (fx *replyFixture) setMode(f func(opencode.ObservedPermission) opencode.ObservedPermission) {
	fx.mu.Lock()
	fx.mode = f
	fx.mu.Unlock()
}
func (fx *replyFixture) setSourceErr(err error) {
	fx.src.mu.Lock()
	fx.src.err = err
	fx.src.mu.Unlock()
}
func (fx *replyFixture) remove() { fx.mu.Lock(); fx.gone = true; fx.mu.Unlock() }
func (fx *replyFixture) restore() {
	fx.mu.Lock()
	fx.gone = false
	fx.mu.Unlock()
}

// noSideEffects fails unless the call made no native write and wrote no attempt line.
func (fx *replyFixture) noSideEffects(what string) {
	fx.t.Helper()
	if n := len(fx.rep.calls()); n != 0 {
		fx.t.Fatalf("%s: %d native writes, want 0", what, n)
	}
	if n := len(fx.aw.written()); n != 0 {
		fx.t.Fatalf("%s: %d attempt lines, want 0", what, n)
	}
}

func asErr(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *Error", err)
	}
	return e
}
func wantErr(t *testing.T, err error, code, reason string) *Error {
	t.Helper()
	e := asErr(t, err)
	if e.Code != code || e.Reason != reason {
		t.Fatalf("error = %s/%q (%s), want %s/%q", e.Code, e.Reason, e.Message, code, reason)
	}
	return e
}

var attemptIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

func TestReplyRejectOneNativeWriteAfterTheAttemptRecord(t *testing.T) {
	fx := newReplyFixture(t)
	res, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "replied" || res.Decision != "reject" || res.Attempt != 1 || res.DecidedBy != "chat_client" || res.HumanVerified ||
		res.ExecutionID != fx.x.ID || res.PermissionID != testPerm || !attemptIDRe.MatchString(res.AttemptID) {
		t.Fatalf("result = %+v", res)
	}
	// Exactly one native write, reply "reject", on the execution's native session; the
	// interface has no message parameter, so none can be sent.
	calls := fx.rep.calls()
	if len(calls) != 1 || calls[0] != [3]string{"ses_test", testPerm, "reject"} {
		t.Fatalf("native writes = %v", calls)
	}
	// Order: the fresh re-read, then the attempt record, then the native write.
	if got := strings.Join(fx.ev.snapshot(), ","); got != "read,attempt,native:reject,line:result" {
		t.Fatalf("event order = %s, want read,attempt,native:reject,line:result", got)
	}
	if dirs := func() []string { fx.rep.mu.Lock(); defer fx.rep.mu.Unlock(); return fx.rep.dirs }(); len(dirs) != 1 || dirs[0] != "/" {
		t.Fatalf("native write routed to %v, want the execution's directory", dirs)
	}
	lines := fx.aw.written()
	if len(lines) != 1 {
		t.Fatalf("attempt lines = %d", len(lines))
	}
	l := lines[0]
	for k, want := range map[string]string{
		"tool": "reply_permission", "attempt": "1", "attempt_id": res.AttemptID, "decision": "reject", "permission_id": testPerm,
		"execution_id": fx.x.ID, "session_id": "opencode:ses_test", "native_tool": "edit", "kind": "edit", "scope_digest": testDigest,
		"pattern_count": "2", "policy_mode": "reject_only", "policy_rule": "reject_allowed", "principal": "local",
	} {
		if l[k] != want {
			t.Fatalf("attempt field %s = %q, want %q (%v)", k, l[k], want, l)
		}
	}
	if _, ok := l["supersedes"]; ok {
		t.Fatal("attempt 1 carries supersedes")
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`).MatchString(l["process_start"]) {
		t.Fatalf("process_start = %q, want RFC 3339 UTC second precision", l["process_start"])
	}
	// The relay never releases the guard and never decides the outcome.
	if held, _ := fx.m.SessionGuard("ses_test"); !held {
		t.Fatal("guard released by reply_permission")
	}
	// The re-read was routed by the execution's native session.
	fx.src.mu.Lock()
	defer fx.src.mu.Unlock()
	if len(fx.src.sessions) == 0 || fx.src.sessions[len(fx.src.sessions)-1] != "ses_test" {
		t.Fatalf("re-read sessions = %v", fx.src.sessions)
	}
}

func TestReplyBindingRefusalsMakeNoNativeWriteAndNoAttemptLine(t *testing.T) {
	type tc struct {
		name         string
		mutate       func(*replyFixture)
		in           func(*replyFixture) ReplyInput
		code, reason string
	}
	same := func(fx *replyFixture) ReplyInput { return fx.in() }
	cases := []tc{
		{"request gone (TUI answered first)", func(fx *replyFixture) { fx.remove() }, same, "NOT_FOUND", ReasonResolvedElsewhere},
		{"request of another session", func(fx *replyFixture) {
			fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission { p.SessionID = "ses_other"; return p })
		}, same, "FORBIDDEN", ReasonNotVerified},
		{"unattributed", func(fx *replyFixture) {
			fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission {
				p.Attribution = "unattributed"
				return p
			})
		}, same, "FORBIDDEN", ReasonNotVerified},
		{"conflicting", func(fx *replyFixture) {
			fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission {
				p.Attribution = "conflicting"
				return p
			})
		}, same, "FORBIDDEN", ReasonNotVerified},
		{"tool-less request", func(fx *replyFixture) {
			fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission {
				p.ToolMessageID, p.ToolCallID, p.Attribution, p.ExecutionID = "", "", "unattributed", ""
				return p
			})
		}, same, "FORBIDDEN", ReasonNotVerified},
		{"verified for another execution", func(fx *replyFixture) {
			fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission {
				p.ExecutionID = "someone-else"
				return p
			})
		}, same, "FORBIDDEN", ReasonNotVerified},
		{"resolved externally", func(fx *replyFixture) {
			fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission {
				p.State = "resolved_externally"
				return p
			})
		}, same, "NOT_FOUND", ReasonResolvedElsewhere},
		{"observation uncertain", func(fx *replyFixture) {
			fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission {
				p.State = "observation_uncertain"
				return p
			})
		}, same, "UPSTREAM_FAILURE", ""},
		{"field outside its closed charset", func(fx *replyFixture) {
			fx.setMode(func(p opencode.ObservedPermission) opencode.ObservedPermission { p.Tool = "has spaces"; return p })
		}, same, "FORBIDDEN", ReasonNotVerified},
		{"other permission id", nil, func(fx *replyFixture) ReplyInput { i := fx.in(); i.PermissionID = "per_other"; return i }, "NOT_FOUND", ReasonResolvedElsewhere},
		{"observer unavailable", func(fx *replyFixture) { fx.setSourceErr(errors.New("down")) }, same, "UPSTREAM_FAILURE", ""},
		{"observer timed out", func(fx *replyFixture) { fx.setSourceErr(context.DeadlineExceeded) }, same, "UPSTREAM_TIMEOUT", ""},
		{"observer canceled (package error)", func(fx *replyFixture) { fx.setSourceErr(opencode.ErrPermissionCanceled) }, same, "UPSTREAM_TIMEOUT", ""},
		{"no permission source", func(fx *replyFixture) { fx.m.SetPermissionSource(nil) }, same, "UPSTREAM_FAILURE", ""},
		{"unknown execution", nil, func(fx *replyFixture) ReplyInput {
			i := fx.in()
			i.ExecutionID = "0123456789abcdef0123456789abcdef"
			return i
		}, "NOT_FOUND", ""},
		{"malformed permission id", nil, func(fx *replyFixture) ReplyInput { i := fx.in(); i.PermissionID = "per/../x"; return i }, "INVALID_INPUT", ""},
		{"permission id without per prefix", nil, func(fx *replyFixture) ReplyInput { i := fx.in(); i.PermissionID = "abc123"; return i }, "INVALID_INPUT", ""},
		{"bad decision", nil, func(fx *replyFixture) ReplyInput { i := fx.in(); i.Decision = "always"; return i }, "INVALID_INPUT", ""},
		{"malformed supersedes", nil, func(fx *replyFixture) ReplyInput { i := fx.in(); i.Supersedes = "short"; return i }, "INVALID_INPUT", ""},
		{"allow_once reaching the manager is never sent", nil, func(fx *replyFixture) ReplyInput { i := fx.in(); i.Decision = "allow_once"; return i }, "FORBIDDEN", ReasonAllowOnceDisabled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newReplyFixture(t)
			if c.mutate != nil {
				c.mutate(fx)
			}
			_, err := fx.m.ReplyPermission(fx.ctx(), c.in(fx))
			e := wantErr(t, err, c.code, c.reason)
			if e.AttemptID != "" || e.Attempt != 0 {
				t.Fatalf("a refusal carries attempt fields: %+v", e)
			}
			fx.noSideEffects(c.name)
			// The same fixture still serves a good call afterwards: the refusal changed no state.
			if c.name == "unknown execution" || strings.HasPrefix(c.name, "malformed") || c.name == "bad decision" || c.name == "permission id without per prefix" || strings.HasPrefix(c.name, "allow_once") {
				return
			}
			fx.setSourceErr(nil)
			fx.restore()
			fx.setMode(nil)
			fx.m.SetPermissionSource(markedSource{fx.src, fx.ev})
			if _, err := fx.m.ReplyPermission(fx.ctx(), fx.in()); err != nil {
				t.Fatalf("a good call after the refusal: %v", err)
			}
		})
	}
}

func TestReplyRefusedWhenNotWaitingForPermission(t *testing.T) {
	f := &fake{status: "idle"}
	m := setup(t, f)
	src := &sourceFake{build: func(string) opencode.PermissionObservationBatch { return batchOf("available") }}
	ev := &events{}
	rep, aw := &replierFake{ev: ev}, &attemptFake{ev: ev}
	m.SetPermissionSource(src)
	m.SetPermissionReplier(rep)
	m.SetAttemptWriter(aw)
	x, e := m.Send(context.Background(), "ses_test", "prompt", "reply-key-0001")
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, m, x.ID, "running")
	_, err := m.ReplyPermission(context.Background(), ReplyInput{ExecutionID: x.ID, PermissionID: testPerm, Decision: "reject", Principal: "local"})
	wantErr(t, err, "NOT_FOUND", ReasonResolvedElsewhere)
	if len(rep.calls()) != 0 || len(aw.written()) != 0 || src.callCount() != 0 {
		t.Fatalf("a running execution reached the native side: writes %d lines %d reads %d", len(rep.calls()), len(aw.written()), src.callCount())
	}
}

func TestReplyAttemptRecordFailureMeansZeroNativeWrites(t *testing.T) {
	fx := newReplyFixture(t)
	fx.aw.failWith(audit.ErrUnavailable)
	_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	e := wantErr(t, err, "UPSTREAM_FAILURE", ReasonAuditUnavailable)
	if !e.Retryable {
		t.Fatal("audit_unavailable must be retryable")
	}
	if n := len(fx.rep.calls()); n != 0 {
		t.Fatalf("%d native writes after a failed attempt record", n)
	}
	// The record is refused (nothing was sent): a retry is evaluated normally as attempt 1.
	res, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	if err != nil || res.Attempt != 1 {
		t.Fatalf("retry after refused: %+v %v", res, err)
	}
	if len(fx.rep.calls()) != 1 {
		t.Fatalf("native writes = %d, want exactly 1", len(fx.rep.calls()))
	}
}

func TestReplyFailsClosedWithoutWriterOrReplier(t *testing.T) {
	fx := newReplyFixture(t)
	fx.m.SetAttemptWriter(nil)
	_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	wantErr(t, err, "UPSTREAM_FAILURE", ReasonAuditUnavailable)
	fx.noSideEffects("no attempt writer")
	fx.m.SetAttemptWriter(fx.aw)
	fx.m.SetPermissionReplier(nil)
	_, err = fx.m.ReplyPermission(fx.ctx(), fx.in())
	wantErr(t, err, "UPSTREAM_FAILURE", "")
	fx.noSideEffects("no replier")
}

func TestReplyInvalidAttemptFieldsFailClosed(t *testing.T) {
	fx := newReplyFixture(t)
	fx.aw.failWith(audit.ErrInvalid)
	_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	wantErr(t, err, "INTERNAL", "")
	if len(fx.rep.calls()) != 0 {
		t.Fatal("native write after an invalid attempt record")
	}
}

func TestReplyLostResponseIsUnknownOutcomeAndNeverResent(t *testing.T) {
	fx := newReplyFixture(t)
	fx.rep.failWith(errors.New("opencode transport unavailable or request canceled"))
	_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	e := wantErr(t, err, "UNKNOWN_OUTCOME", "")
	if !attemptIDRe.MatchString(e.AttemptID) || e.Attempt != 1 || e.Retryable {
		t.Fatalf("envelope = %+v", e)
	}
	if len(fx.rep.calls()) != 1 || len(fx.aw.written()) != 1 {
		t.Fatalf("writes %d lines %d, want 1 and 1", len(fx.rep.calls()), len(fx.aw.written()))
	}
	// Same key again: the recorded UNKNOWN_OUTCOME with the same token, nothing sent, no new
	// permission read, no new attempt line - whatever decision the new call carries.
	reads := fx.ev.count("read")
	for _, decision := range []string{"reject", "allow_once"} {
		in := fx.in()
		in.Decision = decision
		_, err = fx.m.ReplyPermission(fx.ctx(), in)
		e2 := wantErr(t, err, "UNKNOWN_OUTCOME", "")
		if e2.AttemptID != e.AttemptID || e2.Attempt != 1 {
			t.Fatalf("recorded envelope = %+v, want attempt_id %s", e2, e.AttemptID)
		}
	}
	if len(fx.rep.calls()) != 1 || len(fx.aw.written()) != 1 || fx.ev.count("read") != reads {
		t.Fatalf("a same-key call touched the native side: writes %d lines %d reads %d->%d", len(fx.rep.calls()), len(fx.aw.written()), reads, fx.ev.count("read"))
	}
}

func TestReplyNativeStatusMapping(t *testing.T) {
	for _, c := range []struct {
		name         string
		err          error
		code, reason string
		unknown      bool
	}{
		{"404 first-wins: resolved elsewhere", &opencode.HTTPError{Status: 404}, "NOT_FOUND", ReasonResolvedElsewhere, false},
		{"400 native rejected the id", &opencode.HTTPError{Status: 400}, "UPSTREAM_FAILURE", ReasonNativeRejectedID, false},
		{"500 may have landed", &opencode.HTTPError{Status: 500}, "UNKNOWN_OUTCOME", "", true},
		{"deadline", context.DeadlineExceeded, "UNKNOWN_OUTCOME", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := newReplyFixture(t)
			fx.rep.failWith(c.err)
			_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
			e := wantErr(t, err, c.code, c.reason)
			if (e.AttemptID != "") != c.unknown {
				t.Fatalf("attempt fields on %s: %+v", c.code, e)
			}
			if len(fx.rep.calls()) != 1 {
				t.Fatalf("native writes = %d", len(fx.rep.calls()))
			}
			if !c.unknown {
				// nothing took effect: the record is refused, a retry is evaluated normally
				if _, err := fx.m.ReplyPermission(fx.ctx(), fx.in()); err != nil {
					t.Fatalf("retry after a definite refusal: %v", err)
				}
				if len(fx.rep.calls()) != 2 {
					t.Fatalf("native writes after retry = %d", len(fx.rep.calls()))
				}
			}
		})
	}
}

func TestReplySupersedeSendsExactlyOneExtraWriteWithAttempt2(t *testing.T) {
	fx := newReplyFixture(t)
	fx.rep.failWith(errors.New("lost"))
	_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	first := wantErr(t, err, "UNKNOWN_OUTCOME", "")
	in := fx.in()
	in.Supersedes = first.AttemptID
	res, err := fx.m.ReplyPermission(fx.ctx(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Attempt != 2 || res.AttemptID == first.AttemptID || !attemptIDRe.MatchString(res.AttemptID) || res.Outcome != "replied" {
		t.Fatalf("result = %+v", res)
	}
	if len(fx.rep.calls()) != 2 {
		t.Fatalf("native writes = %d, want 2 (one explicit extra)", len(fx.rep.calls()))
	}
	lines := fx.aw.written()
	if len(lines) != 2 || lines[1]["attempt"] != "2" || lines[1]["supersedes"] != first.AttemptID || lines[1]["attempt_id"] != res.AttemptID {
		t.Fatalf("attempt lines = %v", lines)
	}
	// After a replied attempt 2 the request is answered: a further supersede is refused.
	in.Supersedes = res.AttemptID
	_, err = fx.m.ReplyPermission(fx.ctx(), in)
	wantErr(t, err, "IDEMPOTENCY_CONFLICT", ReasonAlreadyReplied)
	if len(fx.rep.calls()) != 2 {
		t.Fatal("a refused supersede wrote natively")
	}
}

func TestReplySecondSupersedeIsRefused(t *testing.T) {
	fx := newReplyFixture(t)
	fx.rep.failWith(errors.New("lost 1"), errors.New("lost 2"))
	_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	e1 := wantErr(t, err, "UNKNOWN_OUTCOME", "")
	in := fx.in()
	in.Supersedes = e1.AttemptID
	_, err = fx.m.ReplyPermission(fx.ctx(), in)
	e2 := wantErr(t, err, "UNKNOWN_OUTCOME", "")
	if e2.Attempt != 2 || e2.AttemptID == e1.AttemptID {
		t.Fatalf("second envelope = %+v", e2)
	}
	in.Supersedes = e2.AttemptID
	reads, lines := fx.ev.count("read"), len(fx.aw.written())
	_, err = fx.m.ReplyPermission(fx.ctx(), in)
	wantErr(t, err, "FORBIDDEN", ReasonSupersedeLimit)
	if len(fx.rep.calls()) != 2 || fx.ev.count("read") != reads || len(fx.aw.written()) != lines {
		t.Fatal("a refused second supersede touched the native side")
	}
}

func TestReplySupersedeRefusalsAreInMemoryOnly(t *testing.T) {
	seed := func(fx *replyFixture, rec *replyRecord) {
		fx.m.replyMu.Lock()
		fx.m.replies[replyKey{fx.x.ID, testPerm}] = rec
		fx.m.replyMu.Unlock()
	}
	good := "AAAAAAAAAAAAAAAAAAAAAA"
	cases := []struct {
		name         string
		rec          *replyRecord
		supersedes   string
		decision     string
		code, reason string
		retryable    bool
	}{
		{"no record", nil, good, "reject", "INVALID_INPUT", ReasonNothingToSupersede, false},
		{"refused record", &replyRecord{decision: "reject", attemptID: good, attempt: 1, state: "refused"}, good, "reject", "INVALID_INPUT", ReasonNothingToSupersede, false},
		{"in flight", &replyRecord{decision: "reject", attemptID: good, attempt: 1, state: "in_flight"}, good, "reject", "SESSION_BUSY", ReasonAttemptInFlight, true},
		{"replied", &replyRecord{decision: "reject", attemptID: good, attempt: 1, state: "replied"}, good, "reject", "IDEMPOTENCY_CONFLICT", ReasonAlreadyReplied, false},
		{"wrong value", &replyRecord{decision: "reject", attemptID: good, attempt: 1, state: "unknown"}, "BBBBBBBBBBBBBBBBBBBBBB", "reject", "INVALID_INPUT", ReasonSupersedesMismatch, false},
		{"second supersede", &replyRecord{decision: "reject", attemptID: good, attempt: 2, state: "unknown"}, good, "reject", "FORBIDDEN", ReasonSupersedeLimit, false},
		{"allow_once record", &replyRecord{decision: "allow_once", attemptID: good, attempt: 1, state: "unknown"}, good, "allow_once", "FORBIDDEN", ReasonSupersedeAllowOnceNo, false},
		{"different decision", &replyRecord{decision: "allow_once", attemptID: good, attempt: 1, state: "unknown"}, good, "reject", "FORBIDDEN", ReasonSupersedeAllowOnceNo, false},
		{"different decision on a reject record", &replyRecord{decision: "reject", attemptID: good, attempt: 1, state: "unknown"}, good, "allow_once", "INVALID_INPUT", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newReplyFixture(t)
			if c.rec != nil {
				seed(fx, c.rec)
			}
			in := fx.in()
			in.Supersedes, in.Decision = c.supersedes, c.decision
			_, err := fx.m.ReplyPermission(fx.ctx(), in)
			e := wantErr(t, err, c.code, c.reason)
			if e.Retryable != c.retryable {
				t.Fatalf("retryable = %v", e.Retryable)
			}
			if e.AttemptID != "" || e.Attempt != 0 {
				t.Fatalf("only UNKNOWN_OUTCOME carries attempt fields: %+v", e)
			}
			fx.noSideEffects(c.name)
			if n := fx.ev.count("read"); n != 0 {
				t.Fatalf("%d permission reads: rule 0 refusals make none", n)
			}
		})
	}
}

func TestReplySupersedeWhenRequestGoneIsNotFoundWithNoNativeWrite(t *testing.T) {
	fx := newReplyFixture(t)
	fx.rep.failWith(errors.New("lost"))
	_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	first := wantErr(t, err, "UNKNOWN_OUTCOME", "")
	fx.remove()
	in := fx.in()
	in.Supersedes = first.AttemptID
	_, err = fx.m.ReplyPermission(fx.ctx(), in)
	wantErr(t, err, "NOT_FOUND", ReasonResolvedElsewhere)
	if len(fx.rep.calls()) != 1 || len(fx.aw.written()) != 1 {
		t.Fatalf("writes %d lines %d, want 1 and 1 (the re-read is a read)", len(fx.rep.calls()), len(fx.aw.written()))
	}
	// The record still says unknown: the same key still returns the recorded envelope.
	fx.restore()
	_, err = fx.m.ReplyPermission(fx.ctx(), fx.in())
	if e := wantErr(t, err, "UNKNOWN_OUTCOME", ""); e.AttemptID != first.AttemptID {
		t.Fatalf("record changed: %+v", e)
	}
}

func TestReplyFailedSupersedeAttemptRecordKeepsTheUnknownAttempt(t *testing.T) {
	fx := newReplyFixture(t)
	fx.rep.failWith(errors.New("lost"))
	_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	first := wantErr(t, err, "UNKNOWN_OUTCOME", "")
	fx.aw.failWith(audit.ErrUnavailable)
	in := fx.in()
	in.Supersedes = first.AttemptID
	_, err = fx.m.ReplyPermission(fx.ctx(), in)
	wantErr(t, err, "UPSTREAM_FAILURE", ReasonAuditUnavailable)
	if len(fx.rep.calls()) != 1 {
		t.Fatal("native write after a failed supersede attempt record")
	}
	// The earlier attempt is still unknown: a plain same-key call must not become a fresh
	// first attempt (that would send a second reply for a request whose first may have landed).
	_, err = fx.m.ReplyPermission(fx.ctx(), fx.in())
	if e := wantErr(t, err, "UNKNOWN_OUTCOME", ""); e.AttemptID != first.AttemptID || e.Attempt != 1 {
		t.Fatalf("record after a failed supersede = %+v", e)
	}
	if len(fx.rep.calls()) != 1 {
		t.Fatal("a same-key call resent")
	}
	// ...and the explicit supersede still works afterwards.
	if res, err := fx.m.ReplyPermission(fx.ctx(), in); err != nil || res.Attempt != 2 {
		t.Fatalf("supersede after the failed one: %+v %v", res, err)
	}
}

func TestReplyIdempotentSameDecisionReturnsTheRecordedResult(t *testing.T) {
	fx := newReplyFixture(t)
	first, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	if err != nil {
		t.Fatal(err)
	}
	reads := fx.ev.count("read")
	again, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	if err != nil || again != first {
		t.Fatalf("replay = %+v %v, want %+v", again, err, first)
	}
	if len(fx.rep.calls()) != 1 || len(fx.aw.written()) != 1 || fx.ev.count("read") != reads {
		t.Fatal("a same-key replay touched the native side or wrote a second line")
	}
	// A different decision on an answered request conflicts (reachable via a seeded allow_once
	// record only; the tool refuses allow_once before this layer in reject_only mode).
	fx.m.replyMu.Lock()
	fx.m.replies[replyKey{fx.x.ID, testPerm}].decision = "allow_once"
	fx.m.replyMu.Unlock()
	_, err = fx.m.ReplyPermission(fx.ctx(), fx.in())
	wantErr(t, err, "IDEMPOTENCY_CONFLICT", "")
}

func TestReplyOverlappingCallWhileInFlightSendsNothing(t *testing.T) {
	fx := newReplyFixture(t)
	entered, hold := fx.rep.arm()
	done := make(chan error, 1)
	var first ReplyResult
	go func() {
		var err error
		first, err = fx.m.ReplyPermission(fx.ctx(), fx.in())
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the first call never reached the native write")
	}
	// Same key, no supersedes: the recorded UNKNOWN_OUTCOME with the in-flight attempt's token.
	_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
	e := wantErr(t, err, "UNKNOWN_OUTCOME", "")
	if !attemptIDRe.MatchString(e.AttemptID) || e.Attempt != 1 {
		t.Fatalf("overlapping envelope = %+v", e)
	}
	// supersedes while in flight: SESSION_BUSY, retryable.
	in := fx.in()
	in.Supersedes = e.AttemptID
	_, err = fx.m.ReplyPermission(fx.ctx(), in)
	if b := wantErr(t, err, "SESSION_BUSY", ReasonAttemptInFlight); !b.Retryable {
		t.Fatal("attempt_in_flight must be retryable")
	}
	if len(fx.rep.calls()) != 1 {
		t.Fatalf("native writes while in flight = %d, want 1", len(fx.rep.calls()))
	}
	close(hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if first.AttemptID != e.AttemptID {
		t.Fatal("the overlapping envelope did not carry the in-flight attempt's token")
	}
}

func TestReplyHoldsTheRunTokenSoInterruptCannotInterleave(t *testing.T) {
	fx := newReplyFixture(t)
	entered, hold := fx.rep.arm()
	done := make(chan error, 1)
	go func() { _, err := fx.m.ReplyPermission(fx.ctx(), fx.in()); done <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the reply never reached the native write")
	}
	ictx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := fx.m.Interrupt(ictx, fx.x.ID); err == nil {
		t.Fatal("interrupt ran while the reply held the run token")
	}
	fx.f.mu.Lock()
	aborts := fx.f.aborts
	fx.f.mu.Unlock()
	if aborts != 0 {
		t.Fatalf("%d aborts interleaved with the native reply", aborts)
	}
	close(hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReplyConcurrentCallsSendAtMostOneNativeWrite(t *testing.T) {
	fx := newReplyFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			var e *Error
			if !errors.As(err, &e) || e.Code != "UNKNOWN_OUTCOME" {
				t.Fatalf("unexpected error %v", err)
			}
		}
	}
	if n := len(fx.rep.calls()); n != 1 {
		t.Fatalf("%d native writes from 8 concurrent same-key calls, want 1", n)
	}
	if n := len(fx.aw.written()); n != 1 {
		t.Fatalf("%d attempt lines, want 1", n)
	}
}

func TestReplyAttemptIDsAreDistinct22CharBase64URL(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		id, err := newAttemptID()
		if err != nil || !attemptIDRe.MatchString(id) || seen[id] {
			t.Fatalf("id %q err %v duplicate %v", id, err, seen[id])
		}
		seen[id] = true
	}
}

func TestReplyReasonsAreTheClosedEnum(t *testing.T) {
	closed := map[string]bool{}
	for _, r := range []string{ReasonNotVerified, ReasonUnrecognizedKind, ReasonAllowOnceDisabled, ReasonPolicyTool, ReasonPolicyPattern, ReasonPolicyScope, ReasonPolicyRate,
		ReasonAttemptInFlight, ReasonAlreadyReplied, ReasonNothingToSupersede, ReasonSupersedesMismatch, ReasonSupersedeLimit, ReasonSupersedeAllowOnceNo,
		ReasonResolvedElsewhere, ReasonAuditUnavailable, ReasonNativeRejectedID} {
		closed[r] = true
	}
	if len(closed) != 16 {
		t.Fatalf("closed enum has %d members, want 16 (ADR-024 section 4)", len(closed))
	}
	if !closed[AuditUnavailable().Reason] {
		t.Fatal("AuditUnavailable reason outside the enum")
	}
	_ = fmt.Sprint
}

// A superseding attempt whose native write comes back 404 or 400 must not erase the unknown
// first attempt: attempt 1 may have landed (a 404 is exactly what a landed reply looks like). The
// record stays unknown at attempt 2, so the next same-key call is not a fresh first attempt and a
// further supersede is refused.
func TestReplyDefiniteRefusalOfTheSupersedeKeepsTheRequestUnknown(t *testing.T) {
	for _, c := range []struct {
		name         string
		native       error
		code, reason string
	}{
		{"404", &opencode.HTTPError{Status: 404}, "NOT_FOUND", ReasonResolvedElsewhere},
		{"400", &opencode.HTTPError{Status: 400}, "UPSTREAM_FAILURE", ReasonNativeRejectedID},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := newReplyFixture(t)
			fx.rep.failWith(errors.New("lost"), c.native)
			_, err := fx.m.ReplyPermission(fx.ctx(), fx.in())
			first := wantErr(t, err, "UNKNOWN_OUTCOME", "")
			in := fx.in()
			in.Supersedes = first.AttemptID
			_, err = fx.m.ReplyPermission(fx.ctx(), in)
			wantErr(t, err, c.code, c.reason)
			if len(fx.rep.calls()) != 2 {
				t.Fatalf("native writes = %d, want 2", len(fx.rep.calls()))
			}
			// Same key again: the recorded UNKNOWN_OUTCOME of attempt 2, never a fresh attempt 1.
			_, err = fx.m.ReplyPermission(fx.ctx(), fx.in())
			e2 := wantErr(t, err, "UNKNOWN_OUTCOME", "")
			if e2.Attempt != 2 || e2.AttemptID == first.AttemptID {
				t.Fatalf("record after the refused supersede = %+v", e2)
			}
			in.Supersedes = e2.AttemptID
			_, err = fx.m.ReplyPermission(fx.ctx(), in)
			wantErr(t, err, "FORBIDDEN", ReasonSupersedeLimit)
			if len(fx.rep.calls()) != 2 || len(fx.aw.written()) != 2 {
				t.Fatalf("writes %d lines %d, want 2 and 2", len(fx.rep.calls()), len(fx.aw.written()))
			}
		})
	}
}
