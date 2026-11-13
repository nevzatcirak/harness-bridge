package runner

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// A normal run: prompt over stdin, argv without the prompt, a scrubbed env,
// history turn_start/assistant_block/turn_end, row 3 completed, guard released.
func TestHappyPathCompletes(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-poison-0123456789")
	h := newHarness(t, nil)
	x := h.mustSend(1, "do the thing", "key-0001-aaaa")
	// The admission view is "submitting", or "running" if the child already started: both
	// are correct and the race between them is not part of the contract (#229 P3-5).
	if (x.State != "submitting" && x.State != "running") || x.SessionID != "claudecode:"+uuidN(1) {
		t.Fatalf("admission view = %+v", x)
	}
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.assistantLine(1, "done"))
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Code: 0})
	x = h.wait(x.ID)
	if x.State != "completed" || x.Ended == nil || x.Unresolved != nil {
		t.Fatalf("final view = %+v", x)
	}
	if v := h.verdict(x.ID); v.Row != "3" || !v.GuardReleased {
		t.Fatalf("verdict = %+v", v)
	}
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); held {
		t.Fatal("the guard must be released after a completed run")
	}
	if got := c.stdinString(); got != "do the thing" {
		t.Fatalf("stdin = %q", got)
	}
	spec := h.sp.specs[0]
	for _, a := range spec.Argv {
		if strings.Contains(a, "do the thing") {
			t.Fatalf("the prompt reached argv: %v", spec.Argv)
		}
	}
	if err := claudecode.CheckArgv(spec.Argv); err != nil {
		t.Fatalf("argv: %v", err)
	}
	for _, kv := range spec.Env {
		if strings.Contains(kv, "ANTHROPIC") || strings.Contains(kv, "poison") {
			t.Fatalf("the parent environment leaked: %q", kv)
		}
	}
	if spec.Path != wrapperPath || spec.Dir != h.root(1) {
		t.Fatalf("spec = %+v", spec)
	}
	want := []string{"turn_start", "assistant_block", "turn_end"}
	if got := h.historyKinds(1); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("history = %v", got)
	}
}

// Two concurrent send_prompt for one session start ONE child.
func TestConcurrentSendsForOneSessionStartOneChild(t *testing.T) {
	h := newHarness(t, nil)
	const n = 16
	var wg sync.WaitGroup
	res := make(chan error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, err := h.send(1, "p", "key-"+strings.Repeat("x", 4)+string(rune('a'+i))+"-0000")
			res <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(res)
	ok, busy := 0, 0
	for err := range res {
		switch errCode(err) {
		case "":
			ok++
		case "SESSION_BUSY":
			busy++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 || busy != n-1 {
		t.Fatalf("admitted %d, busy %d", ok, busy)
	}
	c := h.next(1)
	c.finish(Exit{Code: 1})
	time.Sleep(20 * time.Millisecond)
	if h.sp.count() != 1 {
		t.Fatalf("%d children were started for one session", h.sp.count())
	}
}

// The same key and payload replays the existing execution, concurrently too.
func TestSameKeyReplays(t *testing.T) {
	h := newHarness(t, nil)
	first := h.mustSend(1, "hello", "key-replay-01")
	const n = 8
	var wg sync.WaitGroup
	got := make(chan execution.Execution, n)
	replays := make(chan bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x, replay, err := h.send(1, "hello", "key-replay-01")
			if err != nil {
				t.Errorf("replay: %v", err)
			}
			got <- x
			replays <- replay
		}()
	}
	wg.Wait()
	close(got)
	close(replays)
	for x := range got {
		if x.ID != first.ID {
			t.Fatalf("replay returned another execution %s != %s", x.ID, first.ID)
		}
	}
	for r := range replays {
		if !r {
			t.Fatal("a same-key call was not reported as a replay")
		}
	}
	if _, _, err := h.send(1, "different", "key-replay-01"); errCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("changed payload: %v", err)
	}
	if _, _, err := h.send(1, "hello", "key-other-0002"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("a different key while running: %v", err)
	}
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Code: 0})
	h.wait(first.ID)
	// Even after completion the same key never starts a second child.
	x, replay, err := h.send(1, "hello", "key-replay-01")
	if err != nil || !replay || x.ID != first.ID || x.State != "completed" {
		t.Fatalf("replay after completion: %+v %v %v", x, replay, err)
	}
	if h.sp.count() != 1 {
		t.Fatalf("children started: %d", h.sp.count())
	}
}

// A failed fork/exec finalizes failed (PRE_SUBMISSION_UNAVAILABLE) and releases the guard.
func TestFailedSpawnFinalizesFailedAndReleasesTheGuard(t *testing.T) {
	h := newHarness(t, nil)
	h.sp.failure = errors.New("fork/exec: no such file")
	x := h.mustSend(1, "p", "key-spawn-0001")
	x = h.wait(x.ID)
	if x.State != "failed" || x.LocalFailure == nil || x.LocalFailure.Code != "PRE_SUBMISSION_UNAVAILABLE" || x.Ended != nil {
		t.Fatalf("view = %+v", x)
	}
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); held {
		t.Fatal("the guard must be released: the prompt was provably not delivered")
	}
	// The same key replays the failed execution: no retry with the same key (§2).
	y, replay, err := h.send(1, "p", "key-spawn-0001")
	if err != nil || !replay || y.ID != x.ID || h.sp.count() != 1 {
		t.Fatalf("the same key must not start a second child: %+v replay=%v err=%v spawns=%d", y, replay, err, h.sp.count())
	}
	// A new key may start a new child now that the guard is free.
	h.sp.failure = nil
	z := h.mustSend(1, "p", "key-spawn-0002")
	c := h.next(1)
	c.finish(Exit{Code: 1})
	h.wait(z.ID)
}

// A stdin write that fails after some bytes is unknown, guard kept; with no byte it is failed.
func TestStdinFailure(t *testing.T) {
	t.Run("partial write is unknown and keeps the guard", func(t *testing.T) {
		h := newHarness(t, nil)
		h.sp.tweak = func(c *fakeChild) { c.writeN, c.writeErr = 5, io.ErrClosedPipe }
		x := h.mustSend(1, "a longer prompt", "key-stdin-0001")
		c := h.next(1)
		c.finish(Exit{Code: 1})
		x = h.wait(x.ID)
		if x.State != "unknown" || x.Unresolved == nil || x.Unresolved.Reason != "SUBMISSION_UNCONFIRMED" {
			t.Fatalf("view = %+v", x)
		}
		if held, st := h.ex.SessionGuard("claudecode:" + uuidN(1)); !held || st != "unknown" {
			t.Fatalf("guard held=%v state=%q", held, st)
		}
		if _, _, err := h.send(1, "x", "key-stdin-0002"); errCode(err) != "SESSION_BUSY" {
			t.Fatalf("a new execution on an uncertain session: %v", err)
		}
	})
	t.Run("no byte accepted is failed and releases the guard", func(t *testing.T) {
		h := newHarness(t, nil)
		h.sp.tweak = func(c *fakeChild) { c.writeN, c.writeErr = 0, io.ErrClosedPipe }
		x := h.mustSend(1, "p", "key-stdin-0003")
		c := h.next(1)
		c.finish(Exit{Code: 1})
		x = h.wait(x.ID)
		if x.State != "failed" || x.LocalFailure == nil {
			t.Fatalf("view = %+v", x)
		}
		if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); held {
			t.Fatal("guard must be released")
		}
	})
}

// stdout cut mid-stream (no result): unknown, guard kept.
func TestStdoutCutMidStreamIsUnknown(t *testing.T) {
	for name, exit := range map[string]Exit{"exit 1": {Code: 1}, "SIGTERM": {Code: 143, Signal: "terminated"}} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			x := h.mustSend(1, "p", "key-cut-000001")
			c := h.next(1)
			c.emit(h.initLine(1))
			c.emit(h.assistantLine(1, "partial"))
			c.emitRaw(`{"type":"assistant","message":{"content":[{"type":"te`) // cut inside a line
			c.finish(exit)
			x = h.wait(x.ID)
			if x.State != "unknown" || x.Unresolved == nil {
				t.Fatalf("view = %+v", x)
			}
			v := h.verdict(x.ID)
			if v.Row != "6" || v.Reason != "PROCESS_ENDED_WITHOUT_RESULT" || v.GuardReleased {
				t.Fatalf("verdict = %+v", v)
			}
			if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
				t.Fatal("the guard must stay held")
			}
		})
	}
}

// With no result and an exit status that cannot be obtained the verdict is row 10, guard kept.
func TestExitStatusUnavailableWithoutAResult(t *testing.T) {
	// The run holds its child slot for ReapAfter after an unconfirmed wait (#233 P2-1).
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.ReapAfter = 100 * time.Millisecond })
	x := h.mustSend(1, "p", "key-wait-000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.finish(Exit{Unknown: true})
	x = h.wait(x.ID)
	if v := h.verdict(x.ID); x.State != "unknown" || v.Row != "10" || v.GuardReleased {
		t.Fatalf("state %s verdict %+v", x.State, v)
	}
}

// The runner feeds StepStreamBroken BEFORE EOF and exit (#218 P3-1, #219 P3-6):
// a success result, then an over-long line, then a clean exit is row 8, not row 3.
func TestRunnerFeedsStreamBrokenBeforeEOF(t *testing.T) {
	h := newHarness(t, nil)
	x := h.mustSend(1, "p", "key-broken-0001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	go func() {
		c.emitRaw(strings.Repeat("a", 1<<20+10) + "\n") // over the 1 MiB line bound; the runner drains the rest
		c.finish(Exit{Code: 0})
	}()
	x = h.wait(x.ID)
	v := h.verdict(x.ID)
	if x.State != "unknown" || v.Row != "8" || v.Detail != "stream_broken_after_result" || v.GuardReleased {
		t.Fatalf("state %s verdict %+v", x.State, v)
	}
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
		t.Fatal("a broken stream after a result must keep the guard")
	}
}

// The wall-clock cap latches row 9 and interrupts the child.
func TestWallClockCap(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.RunMax = 60 * time.Millisecond })
	x := h.mustSend(1, "p", "key-cap-0000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	if !c.gotSignal(SigInt) {
		t.Fatal("the cap did not interrupt the child")
	}
	x = h.wait(x.ID)
	v := h.verdict(x.ID)
	if x.State != "unknown" || v.Row != "9" || v.Reason != "MONITOR_EXPIRED" || v.GuardReleased {
		t.Fatalf("state %s verdict %+v", x.State, v)
	}
	if got := strings.Join(v.InterruptReasons, ","); got != "cap" {
		t.Fatalf("interrupt reasons = %q", got)
	}
}

// A result whose child does not exit within the grace is row 6a, with an interrupt.
func TestExitGraceAfterAResult(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.Grace = 60 * time.Millisecond })
	x := h.mustSend(1, "p", "key-grace-00001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	if !c.gotSignal(SigInt) {
		t.Fatal("the grace did not interrupt the child")
	}
	x = h.wait(x.ID)
	v := h.verdict(x.ID)
	if x.State != "unknown" || v.Row != "6a" || !v.ChildUnconfirmed || v.GuardReleased {
		t.Fatalf("state %s verdict %+v", x.State, v)
	}
}

// A child that ignores SIGINT is reaped with SIGKILL after ReapAfter; the verdict
// is the one already latched.
func TestReapsAChildThatIgnoresTheInterrupt(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) {
		c.RunMax = 40 * time.Millisecond
		c.ReapAfter = 80 * time.Millisecond
	})
	h.sp.tweak = func(c *fakeChild) { c.ignoreIn = true }
	x := h.mustSend(1, "p", "key-reap-000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	if !c.gotSignal(SigKill) {
		t.Fatal("a child that ignores SIGINT was never reaped")
	}
	x = h.wait(x.ID)
	if v := h.verdict(x.ID); v.Row != "9" || x.State != "unknown" {
		t.Fatalf("state %s verdict %+v", x.State, v)
	}
}

// Bridge shutdown interrupts live children (row 9s) and keeps the guard.
func TestCloseInterruptsChildren(t *testing.T) {
	h := newHarness(t, nil)
	a := h.mustSend(1, "p", "key-close-00001")
	b := h.mustSend(2, "p", "key-close-00002")
	ca, cb := h.next(1), h.next(2)
	ca.emit(h.initLine(1))
	closed := make(chan struct{})
	go func() { h.ex.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return: a child was not interrupted")
	}
	if !ca.gotSignal(SigInt) || !cb.gotSignal(SigInt) {
		t.Fatal("not every child received SIGINT")
	}
	for _, id := range []string{a.ID, b.ID} {
		x, _ := h.ex.Get(id)
		v := h.verdict(id)
		if x.State != "unknown" || v.Row != "9s" || v.Reason != "MANAGER_CLOSED" || v.GuardReleased {
			t.Fatalf("after Close: state %s verdict %+v", x.State, v)
		}
	}
	if _, _, err := h.send(3, "p", "key-close-00003"); errCode(err) != "UNKNOWN_OUTCOME" {
		t.Fatalf("send after Close: %v", err)
	}
	h.ex.Close() // idempotent
}

// The global child cap refuses the next session without starting anything.
func TestChildCap(t *testing.T) {
	h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) { c.MaxChildren = 2 })
	a := h.mustSend(1, "p", "key-capc-000001")
	h.mustSend(2, "p", "key-capc-000002")
	ca, cb := h.next(1), h.next(2)
	_, _, err := h.send(3, "p", "key-capc-000003")
	if e, ok := err.(*execution.Error); !ok || e.Code != "SESSION_BUSY" || !e.Retryable {
		t.Fatalf("at the cap: %v", err)
	}
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(3)); held {
		t.Fatal("a refused admission must release only its own reservation")
	}
	if h.sp.count() != 2 {
		t.Fatalf("%d children", h.sp.count())
	}
	ca.emit(h.initLine(1))
	ca.emit(h.resultLine(1, nil))
	ca.finish(Exit{Code: 0})
	h.wait(a.ID)
	// A slot is free again.
	var z execution.Execution
	deadline := time.Now().Add(3 * time.Second)
	for {
		var err error
		if z, _, err = h.send(3, "p", "key-capc-000004"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a slot never came free: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cc := h.next(3)
	cc.finish(Exit{Code: 1})
	cb.finish(Exit{Code: 1})
	h.wait(z.ID)
}

// A session at its history size bound gets no new execution and no child.
func TestHistoryCapRefusesNewExecutions(t *testing.T) {
	h := newHarness(t, func(_ *Config, s *claudecode.StoreConfig) { s.MaxSessionBytes = 400; s.MaxRecordBytes = 400 })
	x := h.mustSend(1, strings.Repeat("a", 300), "key-hist-000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Code: 0})
	h.wait(x.ID)
	_, _, err := h.send(1, "next", "key-hist-000002")
	if errCode(err) != "INTERNAL" || !strings.Contains(err.Error(), "history") {
		t.Fatalf("at the history bound: %v", err)
	}
	if h.sp.count() != 1 {
		t.Fatalf("a refused execution started a child (%d)", h.sp.count())
	}
	if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); held {
		t.Fatal("a refused execution must release its guard")
	}
}

// The restart-gap marker: a turn the previous process never ended is marked at startup, once.
func TestRestartGapMarker(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.hist.BeginTurn(uuidN(1), "orphan-exec", claudecode.TurnStart{Prompt: "p", KeyHashPrefix: "abcd"}); err != nil {
		t.Fatal(err)
	}
	h.ex.Close()
	h.locks.Close()
	locks, err := AcquireLocks(h.lockDir, h.sessions)
	if err != nil {
		t.Fatal(err)
	}
	defer locks.Close()
	cfg := h.cfg
	cfg.Locks = locks
	for i := 0; i < 2; i++ { // the second start must not duplicate the marker
		ex, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		ex.Close()
	}
	res, _ := h.hist.Read(uuidN(1))
	gaps := 0
	for _, r := range res.Records {
		if r.Kind == claudecode.KindRestartGap && r.ExecutionID == "orphan-exec" {
			gaps++
		}
	}
	if gaps != 1 {
		t.Fatalf("restart_gap markers = %d", gaps)
	}
}

// Admission preflight: a failure starts no child and releases only its own reservation.
func TestPreflightFailures(t *testing.T) {
	t.Run("session not in the allowlist", func(t *testing.T) {
		h := newHarness(t, nil)
		_, _, err := h.ex.Send(context.Background(), Request{Session: "claudecode:" + uuidN(9), Prompt: "p", Key: "key-pre-0000001"})
		if errCode(err) != "NOT_FOUND" || h.sp.count() != 0 {
			t.Fatalf("%v spawns=%d", err, h.sp.count())
		}
	})
	t.Run("malformed input", func(t *testing.T) {
		h := newHarness(t, nil)
		for name, r := range map[string]Request{
			"opencode id": {Session: "opencode:ses_abc", Prompt: "p", Key: "key-pre-0000001"},
			"empty":       {Session: "claudecode:" + uuidN(1), Prompt: "  ", Key: "key-pre-0000001"},
			"short key":   {Session: "claudecode:" + uuidN(1), Prompt: "p", Key: "k"},
			"huge":        {Session: "claudecode:" + uuidN(1), Prompt: strings.Repeat("a", 32769), Key: "key-pre-0000001"},
		} {
			if _, _, err := h.ex.Send(context.Background(), r); errCode(err) != "INVALID_INPUT" {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	t.Run("wrapper no longer valid", func(t *testing.T) {
		h := newHarness(t, nil)
		h.fsys.breakIt()
		_, _, err := h.send(1, "p", "key-pre-0000002")
		if errCode(err) != "FORBIDDEN" || h.sp.count() != 0 {
			t.Fatalf("%v", err)
		}
		if held, _ := h.ex.SessionGuard("claudecode:" + uuidN(1)); held {
			t.Fatal("reservation not released")
		}
	})
	t.Run("root fails the path checks", func(t *testing.T) {
		h := newHarness(t, func(c *Config, _ *claudecode.StoreConfig) {
			c.RootCheck = func(string) error { return errors.New("symlink alias") }
		})
		if _, _, err := h.send(1, "p", "key-pre-0000003"); errCode(err) != "FORBIDDEN" || h.sp.count() != 0 {
			t.Fatalf("%v", err)
		}
		// Another session still works after the refusal: only its own reservation was released.
		h.cfg.RootCheck = nil
	})
}

// New refuses a session whose writer lock the process does not hold, and an invalid wrapper.
func TestNewRefuses(t *testing.T) {
	h := newHarness(t, nil)
	cfg := h.cfg
	cfg.Sessions = append(append([]claudecode.Session{}, cfg.Sessions...), claudecode.Session{ID: "claudecode:" + uuidN(7), Root: h.root(1)})
	if _, err := New(cfg); err == nil {
		t.Error("a session without a held lock must be refused")
	}
	cfg = h.cfg
	cfg.Wrapper = "/tmp/not-root-owned"
	if _, err := New(cfg); err == nil {
		t.Error("an invalid wrapper must be refused")
	}
	cfg = h.cfg
	cfg.Spawner = nil
	if _, err := New(cfg); err == nil {
		t.Error("a missing spawner must be refused")
	}
	// BRIDGE_ALLOWED_SESSIONS is enforced by the constructor (#229 P3-4): a listed
	// session missing from it, or no ACL at all, is refused.
	cfg = h.cfg
	cfg.AllowedSessions = cfg.AllowedSessions[:2]
	if _, err := New(cfg); err == nil {
		t.Error("an allowlisted session missing from the allowed sessions must be refused")
	}
	cfg = h.cfg
	cfg.AllowedSessions = nil
	if _, err := New(cfg); err == nil {
		t.Error("no allowed sessions at all must be refused")
	}
}
