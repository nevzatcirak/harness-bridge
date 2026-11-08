package execution

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// ADR-020 option (b), accepted 2026-11-08: operator-only fenced guard release.
// ReleaseGuard deletes the busy guard of one session only when the Bridge itself
// observes native quiescence twice, a settle interval apart. It never changes
// the execution's state, error, unresolved or ended_at, its idempotency keys or
// fingerprints, or the ADR-010 lock files. ADR-011 promotion stays possible.

// DefaultReleaseSettle is the interval between the two native reads.
const DefaultReleaseSettle = 5 * time.Second

var releaseExecutionPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ReleaseOptions are the operator's explicit choices for one release.
type ReleaseOptions struct {
	// AllowMissingUserMessage permits a release when the correlated user
	// message is absent natively (the lost-Prompt case). It is audited.
	AllowMissingUserMessage bool
	// Settle is the interval between the two native reads; <= 0 means
	// DefaultReleaseSettle.
	Settle time.Duration
}

// ReleaseEvidence is what the release observed. Fields describe the last
// native read; NativeRead is false when no read happened.
type ReleaseEvidence struct {
	NativeRead        bool       `json:"native_read"`
	NativeState       string     `json:"native_state,omitempty"`
	Unfinished        bool       `json:"unfinished"`
	PermissionPending bool       `json:"permission_pending"`
	CorrelatedUser    string     `json:"correlated_user,omitempty"`
	SettleMS          int64      `json:"settle_ms,omitempty"`
	GuardReleasedAt   *time.Time `json:"guard_released_at,omitempty"`
}

type releaseObservation struct {
	status                       string
	ids                          []string
	unfinished, pending, userHit bool
}

func releaseTimeout() error {
	return &Error{Code: "UPSTREAM_TIMEOUT", Message: "native read did not complete within the bound; guard kept", Retryable: true}
}

// ReleaseGuard releases the busy guard of the native session if, and only if,
// it is still held by executionID and every ADR-020 (b) precondition holds.
func (m *Manager) ReleaseGuard(ctx context.Context, session, executionID string, opt ReleaseOptions) (ReleaseEvidence, error) {
	var ev ReleaseEvidence
	if !opencode.ValidID(session, "ses") || !releaseExecutionPattern.MatchString(executionID) {
		return ev, Fail("INVALID_INPUT", "invalid session or execution ID")
	}
	settle := opt.Settle
	if settle <= 0 {
		settle = DefaultReleaseSettle
	}
	if !m.beginRead() {
		return ev, Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	defer m.wg.Done()
	// 1-2. Compare-and-swap lookup, state and monitor checks.
	x, err := m.releaseHolder(session, executionID)
	if err != nil {
		return ev, err
	}
	// 3-4. First native read under x.op, then the preconditions.
	var first releaseObservation
	err = m.underOp(ctx, x, func(bounded context.Context) error {
		var err error
		if first, err = m.releaseObserve(bounded, x); err != nil {
			return err
		}
		ev.record(first)
		return releaseChecks(first, opt)
	})
	if err != nil {
		return ev, err
	}
	// 5. Settle without holding x.op, so polls and interrupts are not blocked.
	ev.SettleMS = settle.Milliseconds()
	if err = m.settleSleep(ctx, settle); err != nil {
		return ev, err
	}
	// 6-8. Re-take x.op, repeat the compare-and-swap and state checks, read
	// again, compare, and commit, all without giving x.op back in between.
	err = m.underOp(ctx, x, func(bounded context.Context) error {
		if _, err := m.releaseHolder(session, executionID); err != nil {
			return err
		}
		second, err := m.releaseObserve(bounded, x)
		if err != nil {
			return err
		}
		ev.record(second)
		if err = releaseChecks(second, opt); err != nil {
			return err
		}
		if second.status != first.status || !slices.Equal(second.ids, first.ids) {
			return Fail("SESSION_BUSY", "native state changed during the settle interval")
		}
		return m.commitRelease(x, session, executionID, &ev)
	})
	return ev, err
}

func (ev *ReleaseEvidence) record(o releaseObservation) {
	ev.NativeRead = true
	ev.NativeState = o.status
	ev.Unfinished = o.unfinished
	ev.PermissionPending = o.pending
	ev.CorrelatedUser = "absent"
	if o.userHit {
		ev.CorrelatedUser = "found"
	}
}

// releaseHolder is the compare-and-swap lookup plus the state checks. It never
// holds m.mu and run.mu together.
func (m *Manager) releaseHolder(session, executionID string) (*run, error) {
	m.mu.Lock()
	holder := m.busy[session]
	x := m.runs[executionID]
	m.mu.Unlock()
	if holder != executionID || x == nil || x.native != session {
		return nil, Fail("INVALID_INPUT", "guard is not held by this execution")
	}
	x.mu.Lock()
	state, monitorDone := x.view.State, x.monitorDone
	x.mu.Unlock()
	if state != "unknown" || !monitorDone {
		return nil, Fail("INVALID_INPUT", "execution is not unknown with its monitor ended")
	}
	return x, nil
}

// underOp runs fn holding a reconcile slot and the run's x.op token, within the
// bounded read deadline, as refresh and Interrupt do.
func (m *Manager) underOp(ctx context.Context, x *run, fn func(context.Context) error) error {
	bounded, cancel := m.readContext(ctx)
	defer cancel()
	if err := take(bounded, m.reconcileSlots); err != nil {
		return m.releaseContextError()
	}
	defer give(m.reconcileSlots)
	if err := take(bounded, x.op); err != nil {
		return m.releaseContextError()
	}
	defer give(x.op)
	return fn(bounded)
}

func (m *Manager) releaseContextError() error {
	if m.ctx.Err() != nil {
		return Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	return releaseTimeout()
}

func (m *Manager) settleSleep(ctx context.Context, d time.Duration) error {
	if m.releaseSleep != nil {
		return m.releaseSleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-m.ctx.Done():
		return Fail("UNKNOWN_OUTCOME", "manager closed")
	case <-ctx.Done():
		return releaseTimeout()
	}
}

// releaseObserve is one native read through the run's own runner, with the
// identity and workspace check before and after it, as in probe. It never
// changes the run.
func (m *Manager) releaseObserve(ctx context.Context, x *run) (releaseObservation, error) {
	var o releaseObservation
	x.mu.Lock()
	messageID := x.view.MessageID
	x.mu.Unlock()
	readFailed := func() error {
		if ctx.Err() != nil {
			return m.releaseContextError()
		}
		return &Error{Code: "UPSTREAM_FAILURE", Message: "native read failed; guard kept", Retryable: true}
	}
	identity := func(err error) error {
		if errors.Is(err, errFSTimeout) {
			return fsUnavailable("workspace filesystem check did not complete; guard kept")
		}
		return err
	}
	meta, err := x.runner.Get(ctx, x.native)
	if err != nil {
		return o, readFailed()
	}
	if err = validateSession(ctx, x, meta); err != nil {
		return o, identity(err)
	}
	statuses, err := x.runner.Status(ctx)
	if err != nil {
		return o, readFailed()
	}
	history, err := x.runner.History(ctx, x.native)
	if err != nil {
		return o, readFailed()
	}
	permissions, err := x.runner.Permissions(ctx)
	if err != nil {
		return o, readFailed()
	}
	if err = revalidateSession(ctx, x, meta); err != nil {
		return o, identity(err)
	}
	if ctx.Err() != nil {
		return o, m.releaseContextError()
	}
	o.status = "absent"
	if s, ok := statuses[x.native]; ok {
		switch s.Type {
		case "idle", "busy", "retry":
			o.status = s.Type
		default:
			o.status = "unknown"
		}
	}
	o.unfinished = opencode.UnfinishedAssistant(history)
	for _, msg := range history {
		o.ids = append(o.ids, msg.Info.ID)
		if msg.Info.SessionID == x.native && msg.Info.ID == messageID && msg.Info.Role == "user" {
			o.userHit = true
		}
	}
	for _, p := range permissions {
		if p.SessionID == x.native {
			o.pending = true
		}
	}
	return o, nil
}

func releaseChecks(o releaseObservation, opt ReleaseOptions) error {
	if o.status != "absent" && o.status != "idle" {
		return Fail("SESSION_BUSY", "native session not idle")
	}
	if o.unfinished {
		return Fail("SESSION_BUSY", "unfinished native assistant message")
	}
	if o.pending {
		return Fail("SESSION_BUSY", "native permission pending")
	}
	if !o.userHit && !opt.AllowMissingUserMessage {
		return Fail("INVALID_INPUT", "correlated user message absent; release needs the explicit override")
	}
	return nil
}

// commitRelease is the final check and delete under run.mu then m.mu, the lock
// order of finalizeUnsubmitted. It records only guard_released_at.
func (m *Manager) commitRelease(x *run, session, executionID string, ev *ReleaseEvidence) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.view.State != "unknown" || !x.monitorDone {
		return Fail("INVALID_INPUT", "execution is not unknown with its monitor ended")
	}
	m.mu.Lock()
	if m.busy[session] != executionID {
		m.mu.Unlock()
		return Fail("INVALID_INPUT", "guard is not held by this execution")
	}
	delete(m.busy, session)
	m.mu.Unlock()
	now := time.Now().UTC()
	x.view.GuardReleasedAt = &now
	ev.GuardReleasedAt = copyTime(&now)
	return nil
}
