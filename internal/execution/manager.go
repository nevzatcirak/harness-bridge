// Package execution tracks in-memory, at-most-once submissions to existing sessions.
package execution

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

type Runner interface {
	Get(context.Context, string) (opencode.Session, error)
	Permissions(context.Context) ([]opencode.Permission, error)
	History(context.Context, string) ([]opencode.Message, error)
	Status(context.Context) (map[string]opencode.Status, error)
	Prompt(context.Context, string, string, string) error
	Abort(context.Context, string) error
}
type Error struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Retryable   bool   `json:"retryable"`
	ExecutionID string `json:"execution_id,omitempty"`
}

func (e *Error) Error() string        { return e.Code + ": " + e.Message }
func Fail(code, message string) error { return &Error{Code: code, Message: message} }

// Unresolved describes an outcome requiring evidence, never a native failure.
type Unresolved struct {
	Reason        string     `json:"reason"`
	Since         time.Time  `json:"since"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
}

// LocalFailure is Bridge evidence of a failure before native Prompt invocation.
// RecordedAt is a local observation time, never a native completion timestamp.
type LocalFailure struct {
	Code       string    `json:"code"`
	RecordedAt time.Time `json:"recorded_at"`
}

type Execution struct {
	ID             string        `json:"execution_id"`
	SessionID      string        `json:"session_id"`
	MessageID      string        `json:"message_id"`
	State          string        `json:"state"`
	Error          string        `json:"error,omitempty"`
	Started        time.Time     `json:"started_at"`
	Updated        time.Time     `json:"updated_at"`
	Ended          *time.Time    `json:"ended_at,omitempty"`
	Unresolved     *Unresolved   `json:"unresolved,omitempty"`
	MonitorEndedAt *time.Time    `json:"monitor_ended_at,omitempty"`
	ReconciledAt   *time.Time    `json:"reconciled_at,omitempty"`
	LocalFailure   *LocalFailure `json:"local_failure,omitempty"`
	Liveness       *Liveness     `json:"liveness,omitempty"`
	// PermissionRejection is the ADR-018 (d) report-only block; see rejection.go.
	PermissionRejection *PermissionRejection `json:"permission_rejection,omitempty"`
	// PendingPermission and PendingPermissionCount are the ADR-024 S1 report-only
	// exposure of a pending native permission request; see pendingpermission.go.
	// They are present only while the state is waiting_for_permission.
	PendingPermission      *PendingPermission `json:"pending_permission,omitempty"`
	PendingPermissionCount *int               `json:"pending_permission_count,omitempty"`
	// GuardReleasedAt is set only by the ADR-020 (b) operator release.
	GuardReleasedAt *time.Time `json:"guard_released_at,omitempty"`
}
type run struct {
	mu             sync.Mutex
	view           Execution
	native         string
	runner         Runner
	messages       []opencode.Message
	abortRequested bool
	// Protected by mu; set once immediately before invoking Runner.Prompt.
	// A true value is conservative even if the transport sends no bytes.
	submissionAttempted           bool
	op                            chan struct{}
	monitorDone                   bool
	directory, canonicalDirectory string
	workspace                     os.FileInfo
	flight                        *reconciliation
	nextProbe                     time.Time
	probeError                    error
	observationValid              bool
}
type Manager struct {
	mu                sync.Mutex
	r                 Runner
	runs              map[string]*run
	busy              map[string]string
	keys              map[string]string
	fingerprints      map[string][32]byte
	pending           map[string]chan struct{}
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	timeout, interval time.Duration
	closed            bool
	reconcileTimeout  time.Duration
	reconcileSlots    chan struct{}
	admissionSlots    chan struct{}
	// ADR-017 option (a): report-only liveness. stallThreshold is the NO_PROGRESS
	// report threshold; clock is the liveness observation clock (tests replace it
	// before the first Send). noLiveness disables the report for comparison tests
	// only; it is never set outside this package.
	stallThreshold time.Duration
	clock          func() time.Time
	noLiveness     bool
	// ADR-018 (d): optional in-memory witness for basis native_event.
	witness rejectionWitness
	// ADR-024 S1: optional source for the verified pending_permission object.
	permSource permissionSourceRef
	// releaseSleep replaces the ADR-020 settle sleep in tests only.
	releaseSleep func(context.Context, time.Duration) error
}

func New(r Runner) *Manager { return NewWithTiming(r, 5*time.Minute, 500*time.Millisecond) }
func NewWithTiming(r Runner, timeout, interval time.Duration) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{r: r, runs: map[string]*run{}, busy: map[string]string{}, keys: map[string]string{}, fingerprints: map[string][32]byte{}, pending: map[string]chan struct{}{}, ctx: ctx, cancel: cancel, timeout: timeout, interval: interval, reconcileTimeout: 5 * time.Second, reconcileSlots: make(chan struct{}, 4), admissionSlots: make(chan struct{}, 4), stallThreshold: defaultStallThreshold, clock: time.Now}
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
	m.mu.Lock()
	runs := make([]*run, 0, len(m.runs))
	for _, x := range m.runs {
		runs = append(runs, x)
	}
	m.mu.Unlock()
	for _, x := range runs {
		x.mu.Lock()
		if !terminal(x.view.State) {
			unresolved(x, "MANAGER_CLOSED", "bridge shutdown; native work may continue", time.Now().UTC(), false)
		}
		x.mu.Unlock()
	}
}
func token() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

var lastMessageClock atomic.Uint64

// OpenCode ascending IDs encode milliseconds and a 12-bit counter in 6 bytes.
// Random-only IDs can reorder user history and break the native context builder.
func messageID() string {
	var clock uint64
	for {
		previous := lastMessageClock.Load()
		clock = uint64(time.Now().UnixMilli()) << 12
		if clock <= previous {
			clock = previous + 1
		}
		if lastMessageClock.CompareAndSwap(previous, clock) {
			break
		}
	}
	return fmt.Sprintf("msg_%012x%s", clock&((1<<48)-1), token()[:14])
}
func terminal(s string) bool { return s == "completed" || s == "failed" || s == "interrupted" }
func (m *Manager) Send(ctx context.Context, session, prompt, key string) (Execution, error) {
	return m.SendWithRunner(ctx, m.r, session, prompt, key)
}

// SendWithRunner permits a directory-scoped client while keeping one global session guard.
func (m *Manager) SendWithRunner(ctx context.Context, r Runner, session, prompt, key string) (Execution, error) {
	x, _, err := m.SendWithRunnerOutcome(ctx, r, session, prompt, key)
	return x, err
}

// SendWithRunnerOutcome is SendWithRunner plus the manager's own admission
// decision for audit: replay is true only when the same session, idempotency key
// and payload returned the existing accepted execution without a new submission.
// It adds no other behavior.
func (m *Manager) SendWithRunnerOutcome(ctx context.Context, r Runner, session, prompt, key string) (Execution, bool, error) {
	if !opencode.ValidID(session, "ses") || strings.TrimSpace(prompt) == "" || len(prompt) > 32768 || len(key) < 8 || len(key) > 128 {
		return Execution{}, false, Fail("INVALID_INPUT", "invalid session, prompt or idempotency key")
	}
	k := session + "\x00" + key
	hash := sha256.Sum256([]byte(prompt))
retry:
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Execution{}, false, Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	if id, ok := m.keys[k]; ok {
		if m.fingerprints[k] != hash {
			m.mu.Unlock()
			return Execution{}, false, Fail("IDEMPOTENCY_CONFLICT", "idempotency key reused with different payload")
		}
		x := m.runs[id]
		m.mu.Unlock()
		if scoped, ok := r.(directoryScoped); ok && x.directory != "" && scoped.Directory() != "" && scoped.Directory() != x.directory {
			return Execution{}, false, Fail("FORBIDDEN", "execution workspace changed")
		}
		if err := validateSession(ctx, x, opencode.Session{ID: x.native, Directory: x.directory}); err != nil {
			if errors.Is(err, errFSTimeout) {
				// V1: identity unverified, so no snapshot; nothing new is submitted.
				return Execution{}, false, fsUnavailable("workspace filesystem check did not complete; replay not verified, nothing submitted")
			}
			return Execution{}, false, err
		}
		x.mu.Lock()
		defer x.mu.Unlock()
		return snapshot(x.view), true, nil
	}
	if pending, ok := m.pending[k]; ok {
		if m.fingerprints[k] != hash {
			m.mu.Unlock()
			return Execution{}, false, Fail("IDEMPOTENCY_CONFLICT", "idempotency key reused with different payload")
		}
		m.mu.Unlock()
		select {
		case <-pending:
			goto retry
		case <-ctx.Done():
			return Execution{}, false, ctx.Err()
		}
	}
	if m.busy[session] != "" {
		m.mu.Unlock()
		return Execution{}, false, Fail("SESSION_BUSY", "session has an active or uncertain execution")
	}
	// Reserve before any network IO, without blocking unrelated sessions.
	id := token()
	m.busy[session] = id
	m.pending[k] = make(chan struct{})
	m.fingerprints[k] = hash
	// Admission is managed work too: Close cancels and joins native preflight
	// reads before returning ownership to its caller. Add under the closed gate.
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	ctx, cancel := m.readContext(ctx)
	defer cancel()
	// Wake same-key callers only after preflight has accepted or rejected.
	defer func() {
		m.mu.Lock()
		close(m.pending[k])
		delete(m.pending, k)
		if _, accepted := m.keys[k]; !accepted {
			delete(m.fingerprints, k)
		}
		m.mu.Unlock()
	}()
	release := func() {
		m.mu.Lock()
		if m.busy[session] == id {
			delete(m.busy, session)
		}
		m.mu.Unlock()
	}
	// Queueing consumes the same admission deadline; many distinct sessions must
	// not create unbounded concurrent native preflights. Reconciliation has its
	// own slots so admission cannot consume read-side recovery capacity.
	if err := take(ctx, m.admissionSlots); err != nil {
		release()
		return Execution{}, false, err
	}
	defer give(m.admissionSlots)
	directory, canonical := "", ""
	var workspace os.FileInfo
	{
		reader := r
		meta, err := reader.Get(ctx, session)
		if err != nil {
			release()
			return Execution{}, false, err
		}
		if meta.ID != session || !filepath.IsAbs(meta.Directory) {
			release()
			return Execution{}, false, Fail("FORBIDDEN", "native session identity mismatch")
		}
		directory = meta.Directory
		if scoped, ok := r.(directoryScoped); ok && scoped.Directory() != "" && scoped.Directory() != directory {
			release()
			return Execution{}, false, Fail("FORBIDDEN", "native workspace changed")
		}
		// P1/P2: bounded. A check that does not complete releases the reservation
		// through the existing path; nothing is sent and the key is not accepted.
		resolved, err := evalSymlinks(ctx, directory)
		if errors.Is(err, errFSTimeout) {
			release()
			return Execution{}, false, fsUnavailable("workspace filesystem check did not complete; nothing submitted")
		}
		if err != nil || resolved != directory || filepath.Clean(directory) != directory {
			release()
			return Execution{}, false, Fail("FORBIDDEN", "native workspace unavailable")
		}
		canonical = resolved
		workspace, err = stat(ctx, canonical)
		if errors.Is(err, errFSTimeout) {
			release()
			return Execution{}, false, fsUnavailable("workspace filesystem check did not complete; nothing submitted")
		}
		if err != nil || !workspace.IsDir() {
			release()
			return Execution{}, false, Fail("FORBIDDEN", "native workspace unavailable")
		}
	}
	statuses, err := r.Status(ctx)
	if err != nil {
		release()
		return Execution{}, false, err
	}
	if s, ok := statuses[session]; ok && s.Type != "idle" {
		release()
		return Execution{}, false, Fail("SESSION_BUSY", "native session busy")
	}
	history, err := r.History(ctx, session)
	if err != nil {
		release()
		return Execution{}, false, err
	}
	if opencode.UnfinishedAssistant(history) {
		release()
		return Execution{}, false, Fail("SESSION_BUSY", "unfinished native assistant message")
	}
	{
		p := r
		permissions, e := p.Permissions(ctx)
		if e != nil {
			release()
			return Execution{}, false, e
		}
		for _, v := range permissions {
			if v.SessionID == session {
				release()
				return Execution{}, false, Fail("SESSION_BUSY", "native permission pending")
			}
		}
	}
	if err = ctx.Err(); err != nil {
		release()
		return Execution{}, false, err
	}
	now := time.Now().UTC()
	x := &run{native: session, runner: r, op: make(chan struct{}, 1), directory: directory, canonicalDirectory: canonical, workspace: workspace, view: Execution{ID: id, SessionID: "opencode:" + session, MessageID: messageID(), State: "submitting", Started: now, Updated: now}}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		release()
		return Execution{}, false, Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	m.runs[id] = x
	m.keys[k] = id
	m.fingerprints[k] = hash
	m.wg.Add(1)
	m.mu.Unlock()
	initial := snapshot(x.view)
	go m.work(x, prompt)
	return initial, false, nil
}

// finalizeUnsubmitted commits local terminal evidence and releases only this run's
// reservation. The accepted key/IDs remain immutable tombstones until restart.
func (m *Manager) finalizeUnsubmitted(x *run) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.submissionAttempted || terminal(x.view.State) {
		return
	}
	now := time.Now().UTC()
	x.view.State = "failed"
	x.view.Error = "PRE_SUBMISSION_UNAVAILABLE: native Prompt was not invoked"
	x.view.LocalFailure = &LocalFailure{Code: "PRE_SUBMISSION_UNAVAILABLE", RecordedAt: now}
	x.view.Updated = now
	x.view.Unresolved = nil
	x.view.Ended = nil
	x.view.ReconciledAt = nil
	x.messages = nil
	x.observationValid = false
	x.monitorDone = true
	x.view.MonitorEndedAt = &now
	m.mu.Lock()
	if m.busy[x.native] == x.view.ID {
		delete(m.busy, x.native)
	}
	m.mu.Unlock()
}

func (m *Manager) work(x *run, prompt string) {
	defer m.wg.Done()
	ctx, cancel := context.WithTimeout(m.ctx, m.timeout)
	defer cancel()
	defer func() {
		x.mu.Lock()
		defer x.mu.Unlock()
		now := time.Now().UTC()
		if !x.monitorDone {
			x.monitorDone = true
			x.view.MonitorEndedAt = &now
		}
		if !terminal(x.view.State) && m.ctx.Err() != nil {
			unresolved(x, "MANAGER_CLOSED", "bridge shutdown; native work may continue", now, false)
		}
	}()
	// Recheck filesystem identity and routing immediately before submission. This
	// detects observed replacements, not an atomic lease on native path lookup.
	meta, err := x.runner.Get(ctx, x.native)
	if err == nil {
		// V2: any failure, including a check that does not complete, finalizes the
		// run as never submitted (Prompt is not invoked before this point).
		err = validateSession(ctx, x, meta)
	}
	if err != nil || ctx.Err() != nil {
		m.finalizeUnsubmitted(x)
		return
	}
	// This is the conservative invocation boundary. Once marked, always invoke
	// Prompt exactly once, even if cancellation races with the call. No later
	// timeout, missing history or reason string may clear this marker.
	x.mu.Lock()
	if terminal(x.view.State) || x.submissionAttempted {
		x.mu.Unlock()
		return
	}
	if ctx.Err() != nil {
		x.mu.Unlock()
		m.finalizeUnsubmitted(x)
		return
	}
	x.submissionAttempted = true
	x.mu.Unlock()
	// Accepted work is detached from the caller. No retry of Prompt, even on timeout.
	if err := x.runner.Prompt(ctx, x.native, prompt, x.view.MessageID); err != nil {
		x.mu.Lock()
		unresolved(x, "SUBMISSION_UNCONFIRMED", "submission not confirmed; never replay blindly", time.Now().UTC(), false)
		x.mu.Unlock()
		return
	}
	x.mu.Lock()
	if terminal(x.view.State) {
		x.mu.Unlock()
		return
	}
	x.view.State = "running"
	x.view.Updated = time.Now().UTC()
	x.mu.Unlock()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		if m.observe(ctx, x) {
			return
		}
		select {
		case <-ctx.Done():
			x.mu.Lock()
			if !terminal(x.view.State) {
				unresolved(x, "MONITOR_EXPIRED", "background monitor expired; native work may continue", time.Now().UTC(), false)
			}
			x.mu.Unlock()
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) observe(ctx context.Context, x *run) bool {
	if err := take(ctx, x.op); err != nil {
		return false
	}
	defer give(x.op)
	_ = m.probe(ctx, x, false)
	x.mu.Lock()
	defer x.mu.Unlock()
	return terminal(x.view.State)
}
func (m *Manager) find(id string) (*run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.runs[id]
	if x == nil {
		return nil, Fail("NOT_FOUND", "execution not found")
	}
	return x, nil
}

// Snapshot never calls the native API. MCP uses it to authorize BEFORE reconciliation.
func (m *Manager) Snapshot(ctx context.Context, id string) (Execution, error) {
	if err := ctx.Err(); err != nil {
		return Execution{}, err
	}
	x, err := m.find(id)
	if err != nil {
		return Execution{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return snapshot(x.view), nil
}

// SessionGuard reports whether this Manager holds the busy guard for a native
// session and, if so, the held execution's state. It performs no native I/O and
// never holds m.mu and run.mu together. A reservation whose run is not published
// yet reports "submitting". A terminal run is released in the same run.mu
// critical section that made it terminal, so observing one means the guard moved;
// re-read, and stay conservative if the race persists.
func (m *Manager) SessionGuard(session string) (held bool, state string) {
	for attempt := 0; attempt < 3; attempt++ {
		m.mu.Lock()
		id := m.busy[session]
		x := m.runs[id]
		m.mu.Unlock()
		if id == "" {
			return false, ""
		}
		if x == nil {
			return true, "submitting"
		}
		x.mu.Lock()
		state = x.view.State
		x.mu.Unlock()
		if !terminal(state) {
			return true, state
		}
	}
	return true, "unknown"
}
func (m *Manager) Get(ctx context.Context, id string) (Execution, error) {
	if err := ctx.Err(); err != nil {
		return Execution{}, err
	}
	x, err := m.find(id)
	if err != nil {
		return Execution{}, err
	}
	if err = m.refresh(ctx, x); err != nil {
		return Execution{}, err
	}
	return m.Snapshot(ctx, id)
}
func (m *Manager) Result(ctx context.Context, id string) (Execution, []opencode.Message, error) {
	if err := ctx.Err(); err != nil {
		return Execution{}, nil, err
	}
	x, err := m.find(id)
	if err != nil {
		return Execution{}, nil, err
	}
	if err = m.refresh(ctx, x); err != nil {
		return Execution{}, nil, err
	}
	if err = ctx.Err(); err != nil {
		return Execution{}, nil, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return snapshot(x.view), copyMessages(x.messages), nil
}
func (m *Manager) Interrupt(ctx context.Context, id string) (Execution, error) {
	x, err := m.find(id)
	if err != nil {
		return Execution{}, err
	}
	if !m.beginRead() {
		return Execution{}, Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	defer m.wg.Done()
	bounded, cancel := m.readContext(ctx)
	defer cancel()
	if err = take(bounded, m.reconcileSlots); err != nil {
		return Execution{}, err
	}
	defer give(m.reconcileSlots)
	if err = take(bounded, x.op); err != nil {
		return Execution{}, err
	}
	defer give(x.op)
	x.mu.Lock()
	v := snapshot(x.view)
	attempted := x.submissionAttempted
	x.mu.Unlock()
	if terminal(v.State) {
		return v, Fail("INVALID_INPUT", "execution already terminal")
	}
	if !attempted || v.State == "submitting" {
		return v, Fail("SESSION_BUSY", "submission in progress; wait for acknowledgement")
	}
	// Serialize fresh evidence and explicit Abort with the monitor/reconciler.
	// A newly proven terminal result must never be aborted by this request.
	if err = m.probe(bounded, x, true); err != nil {
		return Execution{}, err
	}
	x.mu.Lock()
	if terminal(x.view.State) {
		v := snapshot(x.view)
		x.mu.Unlock()
		return v, Fail("INVALID_INPUT", "execution already terminal")
	}
	if !x.observationValid {
		v := snapshot(x.view)
		x.mu.Unlock()
		return v, Fail("UNKNOWN_OUTCOME", "native identity/state unavailable; interruption not submitted")
	}
	requested := x.abortRequested
	x.mu.Unlock()
	if err = bounded.Err(); err != nil {
		return Execution{}, err
	}
	if !requested {
		if err = validateSession(bounded, x, opencode.Session{ID: x.native, Directory: x.directory}); err != nil {
			if errors.Is(err, errFSTimeout) {
				// V3: no Abort without a completed identity check; state and guard unchanged.
				return Execution{}, fsUnavailable("workspace filesystem check did not complete; interruption not submitted")
			}
			return Execution{}, err
		}
		if err = x.runner.Abort(bounded, x.native); err != nil {
			x.mu.Lock()
			unresolved(x, "ABORT_UNCONFIRMED", "abort not confirmed", time.Now().UTC(), false)
			v := snapshot(x.view)
			x.mu.Unlock()
			return v, err
		}
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.abortRequested = true
	x.view.Updated = time.Now().UTC()
	return snapshot(x.view), nil
}
