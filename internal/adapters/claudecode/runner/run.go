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
	esc                  int // escalation step: escNone, escInt (SIGINT sent), escTerm (SIGTERM sent), escDone (exhausted)
}

// Escalation steps of ADR-021 §4: SIGINT, a bounded wait, SIGTERM, a bounded wait, then
// the Bridge stops signalling. There is no further step, in particular no SIGKILL.
const (
	escNone = iota
	escInt
	escTerm
	escDone
)

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
	r.refreshView()
}

// refreshView copies the Claude-only additive fields of ADR-021 §9 from the machine into the
// execution view. It runs after every step: child_unconfirmed and the exit class can change
// after the verdict was decided (an exhausted escalation, a late exit). The verdict itself is
// never re-decided here. Caller holds mu.
func (r *run) refreshView() {
	if !r.decided {
		return
	}
	v := r.m.Verdict()
	x := &r.view
	x.ChildUnconfirmed = v.ChildUnconfirmed
	x.CostUSDReported = v.CostUSD
	x.PermissionDenials = nil
	for _, d := range v.PermissionDenials {
		x.PermissionDenials = append(x.PermissionDenials, execution.PermissionDenial{ToolName: d.ToolName, Input: d.InputSummary})
	}
	if v.ExitClass != "" && v.ExitClass != "none" {
		x.Exit = &execution.ExitInfo{Class: v.ExitClass}
	}
}

// interrupt starts the §4 escalation: SIGINT to the child's group now, SIGTERM after
// IntWait if the child has not exited, and after TermWait more the Bridge stops signalling
// and tells the machine the escalation is exhausted (StepEscalationExhausted: row 6b for a
// user interrupt, child_unconfirmed for a latched row). Nothing stronger is ever sent: a
// child that ignores both keeps its run, its guard and its slot until it exits (an
// operator action removes it, ADR-021 §4). A second interrupt while an escalation is in
// progress does not restart it. Without a child yet, the interrupt is remembered and sent
// as soon as it exists. Caller holds mu.
func (r *run) interrupt() {
	if r.child == nil {
		r.pendingInt = true
		return
	}
	if r.esc != escNone {
		return
	}
	r.esc = escInt
	_ = r.child.Signal(SigInt)
	r.timers = append(r.timers, time.AfterFunc(r.e.cfg.IntWait, r.escalateTerm))
}

// escalateTerm is the second step: SIGTERM, only if the child has not been reaped.
func (r *run) escalateTerm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// markExited sets esc to escDone under this same lock when the child is confirmed reaped,
	// so the step check below is the whole guard (a separate exited check was dead code).
	if r.esc != escInt {
		return
	}
	r.esc = escTerm
	_ = r.child.Signal(SigTerm)
	r.timers = append(r.timers, time.AfterFunc(r.e.cfg.TermWait, r.escalationExhausted))
}

// escalationExhausted ends the escalation with the child still alive: the Bridge stops
// signalling and the machine records child_unconfirmed (never a kill).
func (r *run) escalationExhausted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.esc != escTerm {
		return
	}
	r.esc = escDone
	r.after(r.m.Feed(claudecode.Step{Kind: claudecode.StepEscalationExhausted}))
}

// userInterrupt is interrupt_execution (reason user). It does nothing to a run that is
// already decided (a latched or finished run was interrupted or ended by its own row) and
// returns the snapshot at request time either way.
func (r *run) userInterrupt() execution.Execution {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.decided && !r.m.Verdict().Decided {
		r.after(r.m.Feed(claudecode.Step{Kind: claudecode.StepUserInterrupt}))
		r.interrupt()
	}
	return clone(r.view)
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
	if correlated && ev.Init != nil && ev.Init.Version != "" {
		r.e.setVersion(ev.Init.Version)
	}
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
//
// groupEmpty is the #242 residual check: the wrapper's known exit is not proof that the CLI
// behind `sudo` is gone, so a deferred guard release also needs the child's process group to
// be empty. With members left the guard stays held (a row 1 failed run stays held until a
// Bridge restart; an unknown run can be released by the operator once the group is empty).
func (r *run) markExited(x Exit, groupEmpty bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if x.Unknown {
		return
	}
	r.exited = true
	r.esc = escDone
	if r.pendingRelease && groupEmpty {
		r.pendingRelease = false
		r.e.release(r.session, r.id)
	}
}

// waitGroupEmpty polls the child's process group until it is empty or GroupWait ends. A
// failing check counts as "not empty": every unknown fails closed.
func (r *run) waitGroupEmpty(c Child) bool {
	deadline := time.Now().Add(r.e.cfg.GroupWait)
	for {
		if empty, err := c.GroupEmpty(); err == nil && empty {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
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

	argv, err := claudecode.BuildArgv(claudecode.ArgvConfig{UUID: r.uuid, Model: e.cfg.Model, MaxTurns: e.cfg.MaxTurns, MaxBudgetUSD: e.cfg.MaxBudgetUSD,
		Root: r.root, Tools: e.cfg.Tools, ConfigDir: e.cfg.ConfigDir})
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
		// Bounded (#236 P3-6): a child that keeps writing cannot hold this loop for the
		// whole wall-clock cap. At the bound the loop stops reading; the stream is broken
		// already, so the verdict does not depend on what is left, and the cap, the exit
		// grace and the §4 escalation still end a child that now blocks on a full pipe.
		_, _ = io.CopyN(io.Discard, child.Stdout(), e.cfg.DrainMax)
	}
	r.feed(claudecode.Step{Kind: claudecode.StepEOF})
	x := child.Wait()
	groupEmpty := true
	if !x.Unknown {
		groupEmpty = r.waitGroupEmpty(child)
	}
	r.markExited(x, groupEmpty)
	if !groupEmpty {
		// A known wrapper exit with the group still populated: the exit status of the wrapper
		// does not prove the run ended (ADR-021 §4 process tree). The machine gets an unknown
		// exit, so the verdict is unknown and nothing releases the guard.
		x = Exit{Unknown: true}
	}
	r.feed(claudecode.Step{Kind: claudecode.StepExit, ExitCode: x.Code, ExitSignal: x.Signal, ExitUnknown: x.Unknown})
}

// unreaped reports whether the run's child was started and is not confirmed reaped: either
// the run is still working, or its Wait returned an unknown status (#253 P3-3: the pipeline
// ended, but the child, or a member of its group, may still be alive and the guard stays held).
func (r *run) unreaped() bool {
	select {
	case <-r.done:
	default:
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.child != nil && !r.exited
}
