package runner

import (
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// ADR-021 §10 slice 3, code part: init verification wired through the runner, the §4
// escalation (SIGINT, bounded wait, SIGTERM, bounded wait, child_unconfirmed, never SIGKILL)
// and the overlap pairs of §3, all against fake children that see real Signal calls.

const (
	intWait  = 120 * time.Millisecond
	termWait = 120 * time.Millisecond
)

func s3(c *Config, _ *claudecode.StoreConfig) {
	c.IntWait, c.TermWait = intWait, termWait
}

// initWith is the compliant init line with fields replaced; a nil value deletes the key.
func (h *harness) initWith(n int, mods map[string]any) string {
	var m map[string]any
	if err := jsonUnmarshal(h.initLine(n), &m); err != nil {
		h.t.Fatal(err)
	}
	for k, v := range mods {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return js(m)
}

func (h *harness) abortedLine(n int, reason string) string {
	return h.resultLine(n, map[string]any{"subtype": "error_during_execution", "is_error": true, "terminal_reason": reason})
}

func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) guardHeld(n int) bool {
	held, _ := h.ex.SessionGuard(h.sessions[n-1].ID)
	return held
}

func (h *harness) decided(id string) claudecode.Verdict {
	h.t.Helper()
	until(h.t, "a verdict", func() bool { return h.verdict(id).Decided })
	return h.verdict(id)
}

// sigs lists the signals a child received, in order.
func sigs(c *fakeChild) []Signal {
	var out []Signal
	for _, r := range c.received() {
		out = append(out, r.sig)
	}
	return out
}

// reaction makes a child answer a signal in a goroutine (the runner holds its lock while it
// signals, so a reaction that writes stdout must not run inline).
func reaction(f func(c *fakeChild, s Signal)) func(*fakeChild, Signal) {
	return func(c *fakeChild, s Signal) { go f(c, s) }
}

// ---------------------------------------------------------------------------------------
// Init verification (§8.3 item 4, §3 row 0)

var initViolations = []struct {
	name string
	mods map[string]any
}{
	{"permission mode", map[string]any{"permissionMode": "acceptEdits"}},
	{"api key source", map[string]any{"apiKeySource": "ANTHROPIC_API_KEY"}},
	{"cwd", map[string]any{"cwd": "/somewhere/else"}},
	{"session id", map[string]any{"session_id": uuidN(9)}},
	{"version outside the allowlist", map[string]any{"claude_code_version": "2.1.999"}},
	{"mcp server present", map[string]any{"mcp_servers": []any{map[string]any{"name": "x", "status": "connected"}}}},
	{"mcp_servers absent", map[string]any{"mcp_servers": nil}},
	{"plugin present", map[string]any{"plugins": []any{map[string]any{"name": "p"}}}},
	{"plugins absent", map[string]any{"plugins": nil}},
	{"extra tool", map[string]any{"tools": []any{"Read", "Glob", "Grep", "Bash"}}},
	{"missing tool", map[string]any{"tools": []any{"Read", "Glob"}}},
	{"tools absent", map[string]any{"tools": nil}},
}

// Every init violation ends the run unknown / IDENTITY_MISMATCH, interrupts the child and
// keeps the guard: a second send is SESSION_BUSY and no second child starts.
func TestEachInitViolationLatchesInterruptsAndKeepsTheGuard(t *testing.T) {
	for _, tc := range initViolations {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, s3)
			x := h.mustSend(1, "p", "key-init-0000001")
			c := h.next(1)
			c.emit(h.initWith(1, tc.mods))
			if !c.gotSignal(SigInt) {
				t.Fatal("an init violation must interrupt the child")
			}
			x = h.wait(x.ID)
			v := h.verdict(x.ID)
			if x.State != "unknown" || v.Row != "0" || v.Reason != "IDENTITY_MISMATCH" || v.GuardReleased || len(v.Violations) == 0 {
				t.Fatalf("state %s verdict %+v", x.State, v)
			}
			if got := strings.Join(v.InterruptReasons, ","); got != "latch" {
				t.Fatalf("interrupt reasons = %q", got)
			}
			if !h.guardHeld(1) {
				t.Fatal("the guard must stay held after an init violation")
			}
			if _, _, err := h.send(1, "again", "key-init-0000002"); errCode(err) != "SESSION_BUSY" {
				t.Fatalf("second send: %v", err)
			}
			if h.sp.count() != 1 {
				t.Fatalf("%d children started", h.sp.count())
			}
		})
	}
}

// A hook event before init is a violation too.
func TestPreInitHookEventLatches(t *testing.T) {
	for _, sub := range []string{"hook_started", "hook_progress", "hook_response", "plugin_install"} {
		h := newHarness(t, s3)
		x := h.mustSend(1, "p", "key-hook-0000001")
		c := h.next(1)
		c.emit(js(map[string]any{"type": "system", "subtype": sub, "session_id": uuidN(1)}))
		if !c.gotSignal(SigInt) {
			t.Fatalf("%s: no interrupt", sub)
		}
		h.wait(x.ID)
		if v := h.verdict(x.ID); v.Row != "0" || v.Reason != "IDENTITY_MISMATCH" || v.GuardReleased {
			t.Fatalf("%s: %+v", sub, v)
		}
	}
}

// A success-shaped result after an init violation is never completed; an aborted_* result
// after the latch interrupt is neither row 5 nor a guard release, even with a user interrupt
// recorded before the violation.
func TestResultsAfterAnInitViolationNeverCompleteOrRelease(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  func(h *harness) string
		code int
		user bool
	}{
		{"success and exit 0", func(h *harness) string { return h.resultLine(1, nil) }, 0, false},
		{"aborted_tools and exit 0", func(h *harness) string { return h.abortedLine(1, "aborted_tools") }, 0, false},
		{"aborted_streaming and exit 0", func(h *harness) string { return h.abortedLine(1, "aborted_streaming") }, 0, false},
		{"user interrupt first, then aborted_tools", func(h *harness) string { return h.abortedLine(1, "aborted_tools") }, 0, true},
		{"user interrupt first, then success", func(h *harness) string { return h.resultLine(1, nil) }, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, s3)
			h.sp.tweak = func(c *fakeChild) { c.ignoreIn, c.ignoreTerm = true, true }
			x := h.mustSend(1, "p", "key-late-0000001")
			c := h.next(1)
			if tc.user {
				if _, err := h.ex.Interrupt(x.ID); err != nil {
					t.Fatal(err)
				}
			}
			c.emit(h.initWith(1, map[string]any{"permissionMode": "bypassPermissions"}))
			v := h.decided(x.ID)
			if v.Row != "0" {
				t.Fatalf("verdict %+v", v)
			}
			c.emit(tc.res(h))
			c.finish(Exit{Code: tc.code})
			x = h.wait(x.ID)
			v = h.verdict(x.ID)
			if x.State != "unknown" || v.Row != "0" || v.Reason != "IDENTITY_MISMATCH" || v.GuardReleased || !v.LateResult {
				t.Fatalf("state %s verdict %+v", x.State, v)
			}
			if !h.guardHeld(1) {
				t.Fatal("the guard was released")
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// A result with the child alive (§3 "Exit before decision", rows 6a, 3)

func TestResultWithTheChildAliveKeepsRunningAndTheGuard(t *testing.T) {
	h := newHarness(t, s3)
	x := h.mustSend(1, "p", "key-alive-000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	time.Sleep(60 * time.Millisecond) // the runner has read the result; the child lives
	got, _ := h.ex.Get(x.ID)
	if got.State != "running" || h.verdict(x.ID).Decided {
		t.Fatalf("a result with the child alive must stay running, got %s", got.State)
	}
	if _, _, err := h.send(1, "second", "key-alive-000002"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("second send: %v", err)
	}
	c.finish(Exit{Code: 0})
	x = h.wait(x.ID)
	if x.State != "completed" || h.verdict(x.ID).Row != "3" {
		t.Fatalf("state %s verdict %+v", x.State, h.verdict(x.ID))
	}
	until(t, "the guard release after the reap", func() bool { return !h.guardHeld(1) })
}

// A correlated result whose child does not exit within the grace: row 6a, EXIT_UNCONFIRMED,
// child_unconfirmed, guard kept, never PROCESS_ENDED_WITHOUT_RESULT, whatever arrives late.
func TestResultWhoseChildOutlivesTheGraceIsExitUnconfirmed(t *testing.T) {
	for _, tc := range []struct {
		name string
		late func(h *harness, c *fakeChild)
	}{
		{"nothing late, SIGTERM ends it", func(h *harness, c *fakeChild) {}},
		{"late aborted_tools and exit 0", func(h *harness, c *fakeChild) {
			c.emit(h.abortedLine(1, "aborted_tools"))
			c.finish(Exit{Code: 0})
		}},
		{"late exit 0", func(h *harness, c *fakeChild) { c.finish(Exit{Code: 0}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.Grace = 60 * time.Millisecond })
			h.sp.tweak = func(c *fakeChild) { c.ignoreIn = true; c.ignoreTerm = tc.name != "nothing late, SIGTERM ends it" }
			x := h.mustSend(1, "p", "key-grace-00002")
			c := h.next(1)
			c.emit(h.initLine(1))
			c.emit(h.resultLine(1, nil))
			v := h.decided(x.ID)
			if v.Row != "6a" || v.Reason != "EXIT_UNCONFIRMED" || !v.ChildUnconfirmed || v.GuardReleased || v.State != "unknown" {
				t.Fatalf("verdict %+v", v)
			}
			tc.late(h, c)
			if tc.name == "nothing late, SIGTERM ends it" {
				h.wait(x.ID) // SIGINT ignored, SIGTERM at IntWait ends it
			} else {
				h.wait(x.ID)
			}
			v = h.verdict(x.ID)
			if v.Row != "6a" || v.Reason != "EXIT_UNCONFIRMED" || v.GuardReleased || !h.guardHeld(1) {
				t.Fatalf("late input changed the verdict: %+v", v)
			}
			if _, _, err := h.send(1, "second", "key-grace-00003"); errCode(err) != "SESSION_BUSY" {
				t.Fatalf("second send: %v", err)
			}
			if h.sp.count() != 1 {
				t.Fatalf("a second child was admitted (%d)", h.sp.count())
			}
		})
	}
}

// A Wait that fails after a result is also row 6a / EXIT_UNCONFIRMED; the runner sends no
// grace interrupt for it (the child was reaped or its status is simply unavailable).
func TestWaitFailureAfterAResultIsExitUnconfirmed(t *testing.T) {
	h := newHarness(t, s3)
	x := h.mustSend(1, "p", "key-waitfail-001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Unknown: true})
	h.wait(x.ID)
	v := h.verdict(x.ID)
	if v.Row != "6a" || v.Reason != "EXIT_UNCONFIRMED" || !v.ChildUnconfirmed || v.GuardReleased {
		t.Fatalf("verdict %+v", v)
	}
	if len(v.InterruptReasons) != 0 {
		t.Fatalf("interrupts recorded for a wait failure: %v", v.InterruptReasons)
	}
	if !h.guardHeld(1) {
		t.Fatal("the guard must stay held")
	}
	// The run already has its verdict and no interrupt applied: a later user interrupt is
	// not sent to a child whose status is unknown, and records no reason.
	at, err := h.ex.Interrupt(x.ID)
	if err != nil || at.State != "unknown" {
		t.Fatalf("interrupt of a decided run: %v %+v", err, at)
	}
	if n := len(c.received()); n != 0 {
		t.Fatalf("%d signals sent to a decided run with an unconfirmed wait", n)
	}
	if v := h.verdict(x.ID); len(v.InterruptReasons) != 0 {
		t.Fatalf("interrupt reasons after the late request: %v", v.InterruptReasons)
	}
}

// ---------------------------------------------------------------------------------------
// Overlap pairs (§3, §10 slice 3)

func TestCapPlusSuccessOrAbortedIsMonitorExpired(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  func(h *harness) string
	}{
		{"cap, success, exit 0", func(h *harness) string { return h.resultLine(1, nil) }},
		{"cap, aborted_tools, exit 0", func(h *harness) string { return h.abortedLine(1, "aborted_tools") }},
		{"cap, aborted_streaming, exit 0", func(h *harness) string { return h.abortedLine(1, "aborted_streaming") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.RunMax = 60 * time.Millisecond })
			h.sp.tweak = func(c *fakeChild) { c.ignoreIn, c.ignoreTerm = true, true }
			x := h.mustSend(1, "p", "key-cap-0000002")
			c := h.next(1)
			c.emit(h.initLine(1))
			v := h.decided(x.ID)
			if v.Row != "9" {
				t.Fatalf("verdict %+v", v)
			}
			c.emit(tc.res(h))
			c.finish(Exit{Code: 0})
			x = h.wait(x.ID)
			v = h.verdict(x.ID)
			if x.State != "unknown" || v.Row != "9" || v.Reason != "MONITOR_EXPIRED" || v.GuardReleased || !v.LateResult {
				t.Fatalf("state %s verdict %+v", x.State, v)
			}
			if !h.guardHeld(1) {
				t.Fatal("the guard was released")
			}
		})
	}
}

// Row 6 is for runs with NO result: a cap that is followed by a child exit without a result
// is still row 9.
func TestCapWithoutAResultIsRowNineNotSix(t *testing.T) {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.RunMax = 50 * time.Millisecond })
	x := h.mustSend(1, "p", "key-cap-0000003")
	c := h.next(1)
	c.emit(h.initLine(1))
	h.wait(x.ID) // SIGINT ends it with exit 130 and no result
	if v := h.verdict(x.ID); v.Row != "9" || v.Reason != "MONITOR_EXPIRED" || v.GuardReleased {
		t.Fatalf("verdict %+v", v)
	}
}

func TestShutdownPlusAbortedIsManagerClosed(t *testing.T) {
	h := newHarness(t, s3)
	h.sp.tweak = func(c *fakeChild) {
		c.sessionID = uuidN(1)
		c.onSignal = reaction(func(c *fakeChild, s Signal) {
			if s == SigInt {
				c.emit(js(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "num_turns": 1,
					"session_id": c.sessionFor(), "terminal_reason": "aborted_tools", "permission_denials": []any{}}))
				c.finish(Exit{Code: 0})
			}
		})
	}
	x := h.mustSend(1, "p", "key-shut-0000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	h.ex.Close()
	h.wait(x.ID)
	v := h.verdict(x.ID)
	if v.Row != "9s" || v.Reason != "MANAGER_CLOSED" || v.GuardReleased || got(v.InterruptReasons) != "shutdown" {
		t.Fatalf("verdict %+v", v)
	}
}

func got(l []string) string { return strings.Join(l, ",") }

// Only a user interrupt acknowledged by aborted_* is interrupted (row 5).
func TestUserInterruptAcknowledgedByAbortedIsInterrupted(t *testing.T) {
	for _, reason := range []string{"aborted_tools", "aborted_streaming"} {
		t.Run(reason, func(t *testing.T) {
			h := newHarness(t, s3)
			h.sp.tweak = func(c *fakeChild) {
				c.sessionID = uuidN(1)
				c.onSignal = reaction(func(c *fakeChild, s Signal) {
					if s == SigInt {
						c.emit(js(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "num_turns": 1,
							"session_id": c.sessionID, "terminal_reason": reason, "permission_denials": []any{}}))
						c.finish(Exit{Code: 0})
					}
				})
			}
			x := h.mustSend(1, "p", "key-user-0000001")
			c := h.next(1)
			c.emit(h.initLine(1))
			at, err := h.ex.Interrupt(x.ID)
			if err != nil {
				t.Fatal(err)
			}
			if at.State != "running" {
				t.Fatalf("the interrupt returns the state at request time, got %s", at.State)
			}
			x = h.wait(x.ID)
			v := h.verdict(x.ID)
			if x.State != "interrupted" || v.Row != "5" || !v.GuardReleased || got(v.InterruptReasons) != "user" {
				t.Fatalf("state %s verdict %+v", x.State, v)
			}
			until(t, "the guard release", func() bool { return !h.guardHeld(1) })
			if sg := sigs(c); len(sg) != 1 || sg[0] != SigInt {
				t.Fatalf("signals = %v: an acknowledged interrupt needs no escalation", sg)
			}
		})
	}
}

// A cap, grace, shutdown or latch interrupt acknowledged by aborted_* is never row 5 (the
// first three are above; the latch is in TestResultsAfterAnInitViolation...). A child that
// exits 0 after a user SIGINT WITHOUT a result is not completed: row 6, ABORT_UNCONFIRMED.
func TestExitZeroAfterAUserSigintWithoutAResultIsNotCompleted(t *testing.T) {
	h := newHarness(t, s3)
	h.sp.tweak = func(c *fakeChild) {
		c.onSignal = reaction(func(c *fakeChild, s Signal) { c.finish(Exit{Code: 0}) })
	}
	x := h.mustSend(1, "p", "key-user-0000002")
	c := h.next(1)
	c.emit(h.initLine(1))
	if _, err := h.ex.Interrupt(x.ID); err != nil {
		t.Fatal(err)
	}
	x = h.wait(x.ID)
	v := h.verdict(x.ID)
	if x.State != "unknown" || v.Row != "6" || v.Reason != "ABORT_UNCONFIRMED" || v.GuardReleased {
		t.Fatalf("state %s verdict %+v", x.State, v)
	}
}

// ---------------------------------------------------------------------------------------
// Escalation: SIGINT, wait, SIGTERM, wait, child_unconfirmed. Never SIGKILL.

func TestUserInterruptEscalatesToSigtermThenGivesUp(t *testing.T) {
	h := newHarness(t, s3)
	h.sp.tweak = func(c *fakeChild) { c.ignoreIn, c.ignoreTerm = true, true }
	x := h.mustSend(1, "p", "key-esc-00000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	start := time.Now()
	if _, err := h.ex.Interrupt(x.ID); err != nil {
		t.Fatal(err)
	}
	v := h.decided(x.ID)
	took := time.Since(start)
	if v.Row != "6b" || v.Reason != "ABORT_UNCONFIRMED" || !v.ChildUnconfirmed || v.GuardReleased || v.State != "unknown" {
		t.Fatalf("verdict %+v", v)
	}
	if took < intWait+termWait-20*time.Millisecond {
		t.Fatalf("the escalation gave up after %v, before IntWait+TermWait", took)
	}
	log := c.received()
	if len(log) != 2 || log[0].sig != SigInt || log[1].sig != SigTerm {
		t.Fatalf("signals = %v: want SIGINT then SIGTERM", sigs(c))
	}
	if gap := log[1].at.Sub(log[0].at); gap < intWait-20*time.Millisecond || gap > intWait+400*time.Millisecond {
		t.Fatalf("SIGTERM followed SIGINT after %v, want about %v", gap, intWait)
	}
	time.Sleep(3 * (intWait + termWait)) // nothing else is ever sent
	if n := len(c.received()); n != 2 {
		t.Fatalf("%d signals after the escalation was exhausted", n)
	}
	if !h.guardHeld(1) {
		t.Fatal("the guard must stay held (child_unconfirmed)")
	}
	if _, _, err := h.send(1, "p2", "key-esc-00000002"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("second send: %v", err)
	}
	// operator action: the child is removed, the run ends, the verdict and the guard stay.
	c.finish(Exit{Code: 137, Signal: "killed"})
	h.wait(x.ID)
	if v := h.verdict(x.ID); v.Row != "6b" || v.GuardReleased || !h.guardHeld(1) {
		t.Fatalf("after the operator removed the child: %+v", v)
	}
}

// A child that dies on SIGTERM ends the escalation early; with no result and a user
// interrupt that is row 6 / ABORT_UNCONFIRMED (not 6b: the child is confirmed gone).
func TestSigtermEndsAChildThatIgnoredSigint(t *testing.T) {
	h := newHarness(t, s3)
	h.sp.tweak = func(c *fakeChild) { c.ignoreIn = true }
	x := h.mustSend(1, "p", "key-esc-00000003")
	c := h.next(1)
	c.emit(h.initLine(1))
	if _, err := h.ex.Interrupt(x.ID); err != nil {
		t.Fatal(err)
	}
	x = h.wait(x.ID)
	v := h.verdict(x.ID)
	if x.State != "unknown" || v.Row != "6" || v.Reason != "ABORT_UNCONFIRMED" || v.ChildUnconfirmed || v.GuardReleased {
		t.Fatalf("state %s verdict %+v", x.State, v)
	}
	if sg := sigs(c); len(sg) != 2 || sg[0] != SigInt || sg[1] != SigTerm {
		t.Fatalf("signals = %v", sg)
	}
	time.Sleep(2 * termWait)
	if n := len(c.received()); n != 2 {
		t.Fatalf("%d signals", n)
	}
}

// A child that exits after SIGINT never sees SIGTERM.
func TestNoSigtermAfterTheChildExited(t *testing.T) {
	h := newHarness(t, s3)
	x := h.mustSend(1, "p", "key-esc-00000004")
	c := h.next(1)
	c.emit(h.initLine(1))
	if _, err := h.ex.Interrupt(x.ID); err != nil {
		t.Fatal(err)
	}
	h.wait(x.ID)
	time.Sleep(3 * intWait)
	if sg := sigs(c); len(sg) != 1 || sg[0] != SigInt {
		t.Fatalf("signals = %v", sg)
	}
}

// Every automatic interrupt reason escalates the same way and gains child_unconfirmed
// without changing its latched row.
func TestLatchedRowsGainChildUnconfirmedWhenTheEscalationIsExhausted(t *testing.T) {
	for _, tc := range []struct {
		name, row, reason string
		trigger           func(h *harness, c *fakeChild)
		mod               func(*Config)
	}{
		{"cap", "9", "MONITOR_EXPIRED", func(h *harness, c *fakeChild) { c.emit(h.initLine(1)) }, func(c *Config) { c.RunMax = 50 * time.Millisecond }},
		{"latch", "0", "IDENTITY_MISMATCH", func(h *harness, c *fakeChild) { c.emit(h.initWith(1, map[string]any{"apiKeySource": "x"})) }, nil},
		{"grace", "6a", "EXIT_UNCONFIRMED", func(h *harness, c *fakeChild) { c.emit(h.initLine(1)); c.emit(h.resultLine(1, nil)) }, func(c *Config) { c.Grace = 50 * time.Millisecond }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) {
				s3(c, sc)
				if tc.mod != nil {
					tc.mod(c)
				}
			})
			h.sp.tweak = func(c *fakeChild) { c.ignoreIn, c.ignoreTerm = true, true }
			x := h.mustSend(1, "p", "key-esc-00000005")
			c := h.next(1)
			tc.trigger(h, c)
			until(t, "SIGINT then SIGTERM", func() bool { return len(c.received()) >= 2 })
			time.Sleep(termWait + 80*time.Millisecond) // the escalation is exhausted
			until(t, "child_unconfirmed", func() bool { return h.verdict(x.ID).ChildUnconfirmed })
			v := h.verdict(x.ID)
			if v.Row != tc.row || v.Reason != tc.reason || v.GuardReleased {
				t.Fatalf("verdict %+v", v)
			}
			if sg := sigs(c); len(sg) != 2 || sg[0] != SigInt || sg[1] != SigTerm {
				t.Fatalf("signals = %v", sg)
			}
			c.finish(Exit{Code: 1})
			h.wait(x.ID)
		})
	}
}

// The stdin-failure path escalates too, and still never kills: a child that ignores both
// keeps the guard (row 1 held until reaped).
func TestStdinFailureEscalatesToSigtermAndNeverKills(t *testing.T) {
	h := newHarness(t, s3)
	h.sp.tweak = func(c *fakeChild) { c.writeN, c.writeErr, c.ignoreIn = 0, errClosed, true }
	x := h.mustSend(1, "p", "key-esc-00000006")
	c := h.next(1)
	h.waitState(x.ID, "failed")
	h.wait(x.ID) // SIGTERM ends it
	if sg := sigs(c); len(sg) != 2 || sg[0] != SigInt || sg[1] != SigTerm {
		t.Fatalf("signals = %v", sg)
	}
	until(t, "the guard release after the reap", func() bool { return !h.guardHeld(1) })
}

// ---------------------------------------------------------------------------------------
// Interrupt API

func TestInterruptOfAFinishedOrUnknownExecutionSendsNothing(t *testing.T) {
	h := newHarness(t, s3)
	x := h.mustSend(1, "p", "key-int-00000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Code: 0})
	x = h.wait(x.ID)
	at, err := h.ex.Interrupt(x.ID)
	if err != nil || at.State != "completed" {
		t.Fatalf("interrupt of a completed execution: %v %+v", err, at)
	}
	if n := len(c.received()); n != 0 {
		t.Fatalf("%d signals sent to a finished run", n)
	}
	if _, err := h.ex.Interrupt("no-such-execution"); errCode(err) != "NOT_FOUND" {
		t.Fatalf("unknown id: %v", err)
	}
}

// A user interrupt on a run whose verdict is already latched sends nothing new and does not
// make a later aborted_* result row 5.
func TestUserInterruptAfterALatchIsIgnored(t *testing.T) {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.RunMax = 50 * time.Millisecond })
	h.sp.tweak = func(c *fakeChild) { c.ignoreIn, c.ignoreTerm = true, true }
	x := h.mustSend(1, "p", "key-int-00000002")
	c := h.next(1)
	c.emit(h.initLine(1))
	h.decided(x.ID)
	if _, err := h.ex.Interrupt(x.ID); err != nil {
		t.Fatal(err)
	}
	c.emit(h.abortedLine(1, "aborted_tools"))
	c.finish(Exit{Code: 0})
	h.wait(x.ID)
	v := h.verdict(x.ID)
	if v.Row != "9" || got(v.InterruptReasons) != "cap" || v.GuardReleased {
		t.Fatalf("verdict %+v", v)
	}
}

// ---------------------------------------------------------------------------------------
// §3 row 8: nothing after the final result is ignorable.

func TestToolProgressAndApiRetryAfterTheFinalResultAreRowEight(t *testing.T) {
	for name, line := range map[string]string{
		"tool_progress": js(map[string]any{"type": "tool_progress", "session_id": uuidN(1)}),
		"api_retry":     js(map[string]any{"type": "system", "subtype": "api_retry", "session_id": uuidN(1)}),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, s3)
			x := h.mustSend(1, "p", "key-row8-0000001")
			c := h.next(1)
			c.emit(h.initLine(1))
			c.emit(h.resultLine(1, nil))
			c.emit(line)
			c.finish(Exit{Code: 0})
			x = h.wait(x.ID)
			v := h.verdict(x.ID)
			if x.State != "unknown" || v.Row != "8" || v.GuardReleased {
				t.Fatalf("state %s verdict %+v", x.State, v)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// Carried P3s (S2 reviews)

// #239 P3-2 / #242 P3-3: a repeated Close does not wait CloseWait again and does not start
// another waiter; it reports what is still unreaped.
func TestRepeatedCloseIsCheapAndReportsTheSameAbandonedRun(t *testing.T) {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.CloseWait = 300 * time.Millisecond })
	h.sp.tweak = func(c *fakeChild) { c.ignoreIn, c.ignoreTerm = true, true }
	x := h.mustSend(1, "p", "key-close-00009")
	c := h.next(1)
	c.emit(h.initLine(1))
	start := time.Now()
	first := h.ex.Close()
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("the first Close returned after %v, before CloseWait", d)
	}
	if len(first) != 1 || first[0] != x.ID {
		t.Fatalf("first Close abandoned %v", first)
	}
	for n := 0; n < 3; n++ {
		start = time.Now()
		again := h.ex.Close()
		if d := time.Since(start); d > 100*time.Millisecond {
			t.Fatalf("repeated Close #%d took %v (it re-waited CloseWait)", n+2, d)
		}
		if len(again) != 1 || again[0] != x.ID {
			t.Fatalf("repeated Close reported %v", again)
		}
	}
	c.finish(Exit{Code: 0}) // the operator removed it
	h.wait(x.ID)
	if again := h.ex.Close(); len(again) != 0 {
		t.Fatalf("after the child ended Close still reports %v", again)
	}
}

// #235 P3-3 (M-B2, whose reapUnconfirmed no longer exists since the SIGKILL was removed in
// #237): an unknown Wait on the NO-stdin-failure path. The run decides row 10, the guard
// stays held (a second send is SESSION_BUSY, one child), nothing stronger than the §4
// signals is sent, and the run does not block waiting for a reaper.
func TestUnknownWaitWithoutAStdinFailureHoldsTheGuard(t *testing.T) {
	h := newHarness(t, s3)
	x := h.mustSend(1, "p", "key-uwait-000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.finish(Exit{Unknown: true})
	start := time.Now()
	h.wait(x.ID)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the run blocked %v on an unknown wait", d)
	}
	v := h.verdict(x.ID)
	if v.State != "unknown" || v.Row != "10" || v.GuardReleased {
		t.Fatalf("verdict %+v", v)
	}
	if _, _, err := h.send(1, "again", "key-uwait-000002"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("second send: %v", err)
	}
	if h.sp.count() != 1 {
		t.Fatalf("%d children", h.sp.count())
	}
	for _, s := range sigs(c) {
		if s != SigInt && s != SigTerm {
			t.Fatalf("signal %d", s)
		}
	}
}

// #236 P3-6: after a broken stream the drain is bounded; a child that keeps writing does not
// hold the runner past DrainMax, and the run still ends through the cap's interrupt.
func TestDrainAfterABrokenStreamIsBounded(t *testing.T) {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) {
		s3(c, sc)
		c.DrainMax = 4096
		c.RunMax = 200 * time.Millisecond
	})
	h.sp.tweak = func(c *fakeChild) { c.ignoreIn, c.ignoreTerm = true, true }
	x := h.mustSend(1, "p", "key-drain-000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	// one line over the 1 MiB per-line bound breaks the stream
	go func() { c.emitRaw(strings.Repeat("a", (1<<20)+10) + "\n") }()
	h.decided(x.ID) // the cap latches row 9
	// 64 KiB of further output is more than DrainMax: the runner stops reading at the bound
	// (the writer blocks on the pipe), it does not read forever.
	done := make(chan struct{})
	go func() { c.emitRaw(strings.Repeat("b", 64<<10)); close(done) }()
	select {
	case <-done:
		t.Fatal("the runner kept draining past DrainMax")
	case <-time.After(500 * time.Millisecond):
	}
	c.finish(Exit{Code: 0}) // unblocks the writer too
	h.wait(x.ID)
	if v := h.verdict(x.ID); v.Row != "9" || v.GuardReleased {
		t.Fatalf("verdict %+v", v)
	}
}

// §8.3 item 2 through the runner: the argv the child is started with carries every hardening
// flag (and still no prompt), and the settings carry the deny rules for this session's root.
func TestChildArgvCarriesTheHardeningFlags(t *testing.T) {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.ConfigDir = "" })
	h.mustSend(1, "secret prompt text", "key-argv-0000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Code: 0})
	h.sp.mu.Lock()
	spec := h.sp.specs[0]
	h.sp.mu.Unlock()
	if err := claudecode.CheckArgv(spec.Argv); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(spec.Argv, "\x00")
	for _, want := range []string{"--setting-sources\x00user", "--strict-mcp-config", "--settings\x00{", "--tools\x00Read,Glob,Grep", "--disallowedTools\x00mcp__*", "Read(~/.claude/**)", "Grep(~/.config/anthropic/**)", h.root(1) + "/CLAUDE.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q", strings.ReplaceAll(want, "\x00", " "))
		}
	}
	if strings.Contains(joined, "secret prompt text") {
		t.Error("the prompt reached argv")
	}
}

// New refuses what the §8.5 and §7 configuration forbids, so no wiring can skip it.
func TestNewRefusesAnEmptyOrWildcardVersionListAndABadToolProfile(t *testing.T) {
	for name, mod := range map[string]func(*Config){
		"no versions":      func(c *Config) { c.AllowedVersions = nil },
		"wildcard version": func(c *Config) { c.AllowedVersions = []string{"2.1.*"} },
		"range version":    func(c *Config) { c.AllowedVersions = []string{"^2.1.322"} },
		"no tools":         func(c *Config) { c.Tools = nil },
		"write tool":       func(c *Config) { c.Tools = []string{"Read", "Write"} },
	} {
		h := newHarness(t, nil)
		cfg := h.cfg
		mod(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted it", name)
		}
	}
}

// #253 P3-3: a run whose Wait was unknown ended its pipeline, but its child (or a group
// member) may be alive and its guard stays held: Close reports it as abandoned.
func TestCloseReportsARunWithAnUnknownWait(t *testing.T) {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.CloseWait = 300 * time.Millisecond })
	x := h.mustSend(1, "p", "key-uclose-00001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.finish(Exit{Unknown: true})
	h.wait(x.ID)
	got := h.ex.Close()
	if len(got) != 1 || got[0] != x.ID {
		t.Fatalf("Close abandoned %v, want the unknown-Wait run %s", got, x.ID)
	}
	if again := h.ex.Close(); len(again) != 1 || again[0] != x.ID {
		t.Fatalf("a repeated Close reported %v", again)
	}
	// a cleanly reaped run is not reported
	h2 := newHarness(t, s3)
	y := h2.mustSend(1, "p", "key-uclose-00002")
	c2 := h2.next(1)
	c2.emit(h2.initLine(1))
	c2.emit(h2.resultLine(1, nil))
	c2.finish(Exit{Code: 0})
	h2.wait(y.ID)
	if got := h2.ex.Close(); len(got) != 0 {
		t.Fatalf("a reaped run was reported: %v", got)
	}
}

// #253 P3-5 (X7/X7b): the escalation timers are stopped when the run's pipeline ends, but a timer
// that already fired can still be waiting for the run's lock when the child is reaped. A step that
// runs after the confirmed reap must be a no-op: no SIGTERM to a reaped child, no late
// StepEscalationExhausted. The steps are called directly to pin exactly that window.
func TestAnEscalationStepAfterTheReapIsANoOp(t *testing.T) {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.IntWait, c.TermWait = time.Hour, time.Hour })
	x := h.mustSend(1, "p", "key-noop-0000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	if _, err := h.ex.Interrupt(x.ID); err != nil { // SIGINT ends the fake child with a known exit
		t.Fatal(err)
	}
	h.wait(x.ID)
	h.ex.mu.Lock()
	r := h.ex.runs[x.ID]
	h.ex.mu.Unlock()
	before := len(c.received())
	v := h.verdict(x.ID)
	r.escalateTerm()
	r.escalationExhausted()
	if n := len(c.received()); n != before {
		t.Fatalf("%d signals sent by an escalation step after the confirmed reap", n-before)
	}
	if got := h.verdict(x.ID); got.Row != v.Row || got.ChildUnconfirmed != v.ChildUnconfirmed || got.Reason != v.Reason {
		t.Fatalf("a late escalation step changed the verdict: %+v -> %+v", v, got)
	}
}

// ADR-021 §9: the additive Claude fields of the execution view are filled from the machine,
// after every step (child_unconfirmed can appear after the verdict was decided).
func TestExecutionViewCarriesTheClaudeFields(t *testing.T) {
	h := newHarness(t, s3)
	x := h.mustSend(1, "p", "key-view-0000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, map[string]any{"total_cost_usd": 0.25, "permission_denials": []any{map[string]any{"tool_name": "Write", "tool_use_id": "t1", "tool_input": map[string]any{"file_path": "/x"}}}}))
	c.finish(Exit{Code: 0})
	x = h.wait(x.ID)
	if x.State != "completed" || x.ChildUnconfirmed || x.CostUSDReported != 0.25 || x.Exit == nil || x.Exit.Class != "zero" ||
		len(x.PermissionDenials) != 1 || x.PermissionDenials[0].ToolName != "Write" || !strings.Contains(x.PermissionDenials[0].Input, "/x") {
		t.Fatalf("completed view %+v", x)
	}
	// a latched row whose escalation is exhausted gains child_unconfirmed after the verdict
	h2 := newHarness(t, s3)
	h2.sp.tweak = func(c *fakeChild) { c.ignoreIn, c.ignoreTerm = true, true }
	y := h2.mustSend(1, "p", "key-view-0000002")
	c2 := h2.next(1)
	c2.emit(h2.initLine(1))
	if _, err := h2.ex.Interrupt(y.ID); err != nil {
		t.Fatal(err)
	}
	until(t, "child_unconfirmed in the view", func() bool { v, _ := h2.ex.Get(y.ID); return v.ChildUnconfirmed })
	v, _ := h2.ex.Get(y.ID)
	if v.State != "unknown" || v.Unresolved == nil || v.Unresolved.Reason != "ABORT_UNCONFIRMED" {
		t.Fatalf("unknown view %+v", v)
	}
	c2.finish(Exit{Code: 137, Signal: "killed"})
	h2.wait(y.ID)
	if v, _ = h2.ex.Get(y.ID); v.Exit == nil || v.Exit.Class != "signal" || !v.ChildUnconfirmed {
		t.Fatalf("after the operator removed the child: %+v", v)
	}
}
