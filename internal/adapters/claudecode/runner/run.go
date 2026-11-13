package runner

import (
	"io"
	"sync"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// run is one execution: one child, one state machine, one history turn. Every
// access to the machine, the view and the child's signal state is under mu.
// Lock order: run.mu, then Executor.mu (never the reverse).
type run struct {
	e                    *Executor
	id, session, uuid    string
	root                 string
	done                 chan struct{}
	mu                   sync.Mutex
	view                 execution.Execution
	m                    *claudecode.Machine
	child                Child
	verdict              claudecode.Verdict
	decided, shut        bool
	pendingInt           bool
	pendingRelease       bool // a decided verdict frees the guard, but a child still runs
	graceStarted, exited bool
	timers               []*time.Timer
}

func (r *run) snapshot() execution.Execution {
	r.mu.Lock()
	defer r.mu.Unlock()
	return clone(r.view)
}

func (r *run) verdictCopy() claudecode.Verdict {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m.Verdict()
}

// feed gives the machine one step and carries out what it asks for.
func (r *run) feed(s claudecode.Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.after(r.m.Feed(s))
}

func (r *run) shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shut = true
	r.after(r.m.Feed(claudecode.Step{Kind: claudecode.StepShutdown}))
}

// after carries out an Action and applies a newly decided verdict. Caller holds mu.
func (r *run) after(a claudecode.Action) {
	if a.Interrupt != "" {
		r.interrupt()
	}
	r.sync()
}

// interrupt sends SIGINT to the child's group. Without a child yet, the interrupt is
// remembered and sent as soon as it exists. Nothing stronger is ever sent: a child that
// ignores SIGINT keeps its run, its guard and its slot until it exits (an operator
// action removes it, ADR-021 §4).
func (r *run) interrupt() {
	if r.child == nil {
		r.pendingInt = true
		return
	}
	_ = r.child.Signal(SigInt)
}

// sync applies the first decided verdict: the execution view, the history
// turn_end, and the guard release when the verdict allows it.
func (r *run) sync() {
	v := r.m.Verdict()
	if !v.Decided || r.decided {
		return
	}
	r.decided, r.verdict = true, v
	now := r.e.cfg.Now().UTC()
	x := &r.view
	x.State, x.Updated = v.State, now
	switch v.State {
	case "completed", "interrupted":
		x.Ended = &now
	case "failed":
		x.Error = v.Code + ": " + v.Detail
		if v.Code == "PRE_SUBMISSION_UNAVAILABLE" {
			x.LocalFailure = &execution.LocalFailure{Code: v.Code, RecordedAt: now}
		} else {
			x.Ended = &now
		}
	case "unknown":
		reason := v.Reason
		if reason == "" {
			reason = "UNKNOWN_OUTCOME"
		}
		x.Unresolved = &execution.Unresolved{Reason: reason, Since: now}
	}
	_ = r.e.cfg.History.EndTurn(r.uuid, r.id, claudecode.TurnEnd{State: v.State, Row: v.Row, Reason: v.Reason, Subtype: v.Subtype,
		TerminalReason: v.TerminalReason, ExitClass: v.ExitClass, NumTurns: v.NumTurns, Denials: len(v.PermissionDenials), CostUSD: v.CostUSD})
	if v.GuardReleased {
		if r.child != nil && !r.exited {
			// One child per session (§2): the verdict may be decided early, but the
			// guard is freed only after the child was reaped (markExited). A child
			// that cannot be confirmed reaped keeps the guard held.
			r.pendingRelease = true
		} else {
			r.e.release(r.session, r.id)
		}
	}
}

// line records one stdout event: history first (only of this session, only while
// the turn is open), then the machine.
func (r *run) line(ev claudecode.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	correlated := ev.SessionID == r.uuid
	if correlated && !r.decided {
		for _, b := range ev.Blocks {
			b := b
			_ = r.e.cfg.History.Append(r.uuid, r.id, claudecode.Record{Kind: claudecode.KindAssistantBlock, Block: &b})
		}
		for _, tr := range ev.ToolResults {
			_ = r.e.cfg.History.Append(r.uuid, r.id, claudecode.Record{Kind: claudecode.KindToolResult, ToolUseID: tr.ToolUseID, Content: tr.Content})
		}
	}
	if correlated && ev.Type == "result" && !r.graceStarted && !r.exited {
		r.graceStarted = true
		r.timers = append(r.timers, time.AfterFunc(r.e.cfg.Grace, func() { r.graceExpired() }))
	}
	r.after(r.m.Feed(claudecode.Step{Kind: claudecode.StepLine, Event: ev}))
}

func (r *run) graceExpired() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exited {
		return
	}
	r.after(r.m.Feed(claudecode.Step{Kind: claudecode.StepGraceExpired}))
}

func (r *run) capFired() { r.feed(claudecode.Step{Kind: claudecode.StepCap}) }

// attach records the started child, sends an interrupt that arrived before it
// existed, and starts the wall-clock cap.
func (r *run) attach(c Child) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.child = c
	r.view.State, r.view.Updated = "running", r.e.cfg.Now().UTC()
	if r.decided {
		r.view.State = r.verdict.State
	}
	r.timers = append(r.timers, time.AfterFunc(r.e.cfg.RunMax, r.capFired))
	if r.pendingInt {
		r.pendingInt = false
		r.interrupt()
	}
}

func (r *run) stopTimers() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.timers {
		t.Stop()
	}
}

// markExited records the result of Wait. Only a Wait that returned a known status
// counts as a reap: it sets exited and frees a guard whose release was deferred. A Wait
// that returned Exit{Unknown:true} confirms nothing, so the child may still be alive:
// exited stays false and the guard stays held (the only recovery is a Bridge restart,
// the guard being memory-only).
func (r *run) markExited(x Exit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if x.Unknown {
		return
	}
	r.exited = true
	if r.pendingRelease {
		r.pendingRelease = false
		r.e.release(r.session, r.id)
	}
}

func (r *run) isShut() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shut
}

// work is the life of one child: spawn, prompt over stdin, decode, wait.
func (e *Executor) work(r *run, prompt string) {
	defer close(r.done)
	defer e.childDone()
	defer r.stopTimers()

	argv, err := claudecode.BuildArgv(claudecode.ArgvConfig{UUID: r.uuid, Model: e.cfg.Model, MaxTurns: e.cfg.MaxTurns, MaxBudgetUSD: e.cfg.MaxBudgetUSD})
	if err == nil {
		err = claudecode.CheckArgv(argv)
	}
	var env []string
	if err == nil {
		env, err = claudecode.ScrubEnv(nil, e.cfg.Env)
	}
	if err != nil {
		r.feed(claudecode.Step{Kind: claudecode.StepSpawnFailed})
		return
	}
	if r.isShut() {
		return // closed before any child existed: nothing was sent, the verdict stays as latched
	}
	child, err := e.cfg.Spawner.Start(Spec{Path: e.cfg.Wrapper, Argv: argv, Env: env, Dir: r.root})
	if err != nil {
		r.feed(claudecode.Step{Kind: claudecode.StepSpawnFailed})
		return
	}
	r.attach(child)

	n, werr := child.WriteStdin([]byte(prompt))
	_ = child.CloseStdin()
	if werr != nil || n < len(prompt) {
		r.feed(claudecode.Step{Kind: claudecode.StepStdinFailed, Accepted: n})
		// Whatever the verdict, a child that did not get its prompt is not left running.
		r.mu.Lock()
		r.interrupt()
		r.mu.Unlock()
	}

	dec := claudecode.NewDecoder(child.Stdout(), claudecode.Limits{})
	broken := false
	for {
		ev, err := dec.Next()
		if err == nil {
			r.line(ev)
			continue
		}
		broken = err != io.EOF
		break
	}
	if broken {
		// The stream is unusable. It is fed BEFORE EOF and exit (§3 row 8 depends on
		// the order), and the rest of stdout is drained and discarded so the child can
		// finish; the wall-clock cap still bounds it.
		r.feed(claudecode.Step{Kind: claudecode.StepStreamBroken})
		_, _ = io.Copy(io.Discard, child.Stdout())
	}
	r.feed(claudecode.Step{Kind: claudecode.StepEOF})
	x := child.Wait()
	r.markExited(x)
	r.feed(claudecode.Step{Kind: claudecode.StepExit, ExitCode: x.Code, ExitSignal: x.Signal, ExitUnknown: x.Unknown})
}
