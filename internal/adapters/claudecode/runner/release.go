package runner

import (
	"context"
	"regexp"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// ADR-021 §3 (last bullet), allowed by D14 Q8 and implemented in slice 4: the ADR-020
// operator-only guard release, extended for the Claude harness ONLY. ADR-020's native
// reads do not exist here, so the preconditions are facts the Bridge observed about its own
// child, and the two-read settle is one re-check:
//
//   - the guard of the session is held by exactly that execution;
//   - the execution is `unknown` (a verdict was decided);
//   - its child is CONFIRMED exited: the runner's `Wait` returned a known status (an unknown
//     `Wait` confirms nothing), the run's pipeline has ended, and the child's process group
//     has no member left;
//   - after the settle interval the same holds again (the child is still gone);
//   - nothing is released when any of this cannot be established: every unknown fails closed.
//
// The release only deletes the in-memory guard and records the time. It never changes the
// execution's state, reason, verdict, history or idempotency keys, and sends no signal.
// There is no native read, no SIGKILL and no automatic path: the only caller is the admin
// channel, behind its peer-UID check and a ticket, and it is unreachable until the Executor
// is constructed in production (it is not).

// DefaultReleaseSettle is the interval between the check and the re-check.
const DefaultReleaseSettle = 5 * time.Second

var releaseExecutionPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ReleaseOptions are the operator's choices for one release.
type ReleaseOptions struct {
	// Settle is the interval between the check and the re-check; <= 0 means
	// DefaultReleaseSettle.
	Settle time.Duration
}

// ReleaseEvidence is what the release observed. There is never a native read.
type ReleaseEvidence struct {
	Harness         string     `json:"harness"`
	NativeRead      bool       `json:"native_read"`
	Row             string     `json:"row,omitempty"`
	ExitClass       string     `json:"exit_class,omitempty"`
	ChildExited     bool       `json:"child_exited"`
	GroupEmpty      bool       `json:"group_empty"`
	Rechecked       bool       `json:"rechecked"`
	SettleMS        int64      `json:"settle_ms,omitempty"`
	GuardReleasedAt *time.Time `json:"guard_released_at,omitempty"`
}

type releaseFacts struct {
	r      *run
	row    string
	exit   string
	exited bool
	empty  bool
}

// ReleaseGuard releases the guard of a claudecode: session when, and only when, every
// precondition above holds twice, a settle interval apart.
func (e *Executor) ReleaseGuard(ctx context.Context, session, executionID string, opt ReleaseOptions) (ReleaseEvidence, error) {
	ev := ReleaseEvidence{Harness: "claudecode"}
	uuid, perr := claudecode.ParseSessionID(session)
	if perr != nil || session != claudecode.SessionPrefix+uuid || !releaseExecutionPattern.MatchString(executionID) {
		return ev, execution.Fail("INVALID_INPUT", "invalid session or execution ID")
	}
	settle := opt.Settle
	if settle <= 0 {
		settle = DefaultReleaseSettle
	}
	if e.isClosed() {
		return ev, execution.Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	f, err := e.releaseCheck(session, executionID)
	ev.record(f)
	if err != nil {
		return ev, err
	}
	ev.SettleMS = settle.Milliseconds()
	timer := time.NewTimer(settle)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-e.closing:
		return ev, execution.Fail("UNKNOWN_OUTCOME", "manager closed")
	case <-ctx.Done():
		return ev, &execution.Error{Code: "UPSTREAM_TIMEOUT", Message: "release settle did not complete; guard kept", Retryable: true}
	}
	// The re-check and the commit are one critical section: run.mu, then Executor.mu.
	f2, err := e.releaseCommit(session, executionID, &ev)
	ev.Rechecked = true
	ev.record(f2)
	return ev, err
}

func (ev *ReleaseEvidence) record(f releaseFacts) {
	ev.Row, ev.ExitClass, ev.ChildExited, ev.GroupEmpty = f.row, f.exit, f.exited, f.empty
}

func (e *Executor) isClosed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

// releaseCheck evaluates the preconditions without changing anything.
func (e *Executor) releaseCheck(session, executionID string) (releaseFacts, error) {
	e.mu.Lock()
	holder := e.busy[session]
	r := e.runs[executionID]
	e.mu.Unlock()
	if holder != executionID || r == nil || r.session != session {
		return releaseFacts{}, execution.Fail("INVALID_INPUT", "guard is not held by this execution")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.releaseFactsLocked()
}

// releaseFactsLocked reads the run's facts. Caller holds r.mu.
func (r *run) releaseFactsLocked() (releaseFacts, error) {
	f := releaseFacts{r: r}
	if r.view.State != "unknown" || !r.decided {
		return f, execution.Fail("INVALID_INPUT", "execution is not unknown with a decided verdict")
	}
	f.row, f.exit = r.verdict.Row, r.verdict.ExitClass
	select {
	case <-r.done:
	default:
		return f, execution.Fail("SESSION_BUSY", "the run has not ended: child not confirmed exited; guard kept")
	}
	if r.child == nil {
		// The run ended before any child existed (for example a Close before the spawn):
		// nothing was started, so nothing can still be running.
		f.exited, f.empty = true, true
		return f, nil
	}
	if !r.exited {
		return f, execution.Fail("SESSION_BUSY", "child exit not confirmed (no known wait status); guard kept")
	}
	f.exited = true
	empty, err := r.child.GroupEmpty()
	if err != nil {
		return f, &execution.Error{Code: "UPSTREAM_FAILURE", Message: "process group check failed; guard kept", Retryable: true}
	}
	if !empty {
		return f, execution.Fail("SESSION_BUSY", "the child's process group is not empty; guard kept")
	}
	f.empty = true
	return f, nil
}

// releaseCommit repeats the compare-and-swap lookup and the checks, then deletes the guard,
// all under run.mu and Executor.mu (the lock order of the runner).
func (e *Executor) releaseCommit(session, executionID string, ev *ReleaseEvidence) (releaseFacts, error) {
	e.mu.Lock()
	r := e.runs[executionID]
	e.mu.Unlock()
	if r == nil || r.session != session {
		return releaseFacts{}, execution.Fail("INVALID_INPUT", "guard is not held by this execution")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := r.releaseFactsLocked()
	if err != nil {
		return f, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return f, execution.Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	if e.busy[session] != executionID {
		return f, execution.Fail("INVALID_INPUT", "guard is not held by this execution")
	}
	delete(e.busy, session)
	now := e.cfg.Now().UTC()
	r.view.GuardReleasedAt = &now
	t := now
	ev.GuardReleasedAt = &t
	return f, nil
}
