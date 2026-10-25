// Package execution tracks in-memory, at-most-once submissions to existing sessions.
package execution

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

type Execution struct {
	ID             string      `json:"execution_id"`
	SessionID      string      `json:"session_id"`
	MessageID      string      `json:"message_id"`
	State          string      `json:"state"`
	Error          string      `json:"error,omitempty"`
	Started        time.Time   `json:"started_at"`
	Updated        time.Time   `json:"updated_at"`
	Ended          *time.Time  `json:"ended_at,omitempty"`
	Unresolved     *Unresolved `json:"unresolved,omitempty"`
	MonitorEndedAt *time.Time  `json:"monitor_ended_at,omitempty"`
	ReconciledAt   *time.Time  `json:"reconciled_at,omitempty"`
}
type run struct {
	mu                            sync.Mutex
	view                          Execution
	native                        string
	runner                        Runner
	messages                      []opencode.Message
	abortRequested                bool
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
}

func New(r Runner) *Manager { return NewWithTiming(r, 5*time.Minute, 500*time.Millisecond) }
func NewWithTiming(r Runner, timeout, interval time.Duration) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{r: r, runs: map[string]*run{}, busy: map[string]string{}, keys: map[string]string{}, fingerprints: map[string][32]byte{}, pending: map[string]chan struct{}{}, ctx: ctx, cancel: cancel, timeout: timeout, interval: interval, reconcileTimeout: 5 * time.Second, reconcileSlots: make(chan struct{}, 4), admissionSlots: make(chan struct{}, 4)}
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
	if !opencode.ValidID(session, "ses") || strings.TrimSpace(prompt) == "" || len(prompt) > 32768 || len(key) < 8 || len(key) > 128 {
		return Execution{}, Fail("INVALID_INPUT", "invalid session, prompt or idempotency key")
	}
	k := session + "\x00" + key
	hash := sha256.Sum256([]byte(prompt))
retry:
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Execution{}, Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	if id, ok := m.keys[k]; ok {
		if m.fingerprints[k] != hash {
			m.mu.Unlock()
			return Execution{}, Fail("IDEMPOTENCY_CONFLICT", "idempotency key reused with different payload")
		}
		x := m.runs[id]
		m.mu.Unlock()
		if scoped, ok := r.(directoryScoped); ok && x.directory != "" && scoped.Directory() != "" && scoped.Directory() != x.directory {
			return Execution{}, Fail("FORBIDDEN", "execution workspace changed")
		}
		if err := validateSession(x, opencode.Session{ID: x.native, Directory: x.directory}); err != nil {
			return Execution{}, err
		}
		x.mu.Lock()
		defer x.mu.Unlock()
		return snapshot(x.view), nil
	}
	if pending, ok := m.pending[k]; ok {
		if m.fingerprints[k] != hash {
			m.mu.Unlock()
			return Execution{}, Fail("IDEMPOTENCY_CONFLICT", "idempotency key reused with different payload")
		}
		m.mu.Unlock()
		select {
		case <-pending:
			goto retry
		case <-ctx.Done():
			return Execution{}, ctx.Err()
		}
	}
	if m.busy[session] != "" {
		m.mu.Unlock()
		return Execution{}, Fail("SESSION_BUSY", "session has an active or uncertain execution")
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
		return Execution{}, err
	}
	defer give(m.admissionSlots)
	directory, canonical := "", ""
	var workspace os.FileInfo
	{
		reader := r
		meta, err := reader.Get(ctx, session)
		if err != nil {
			release()
			return Execution{}, err
		}
		if meta.ID != session || !filepath.IsAbs(meta.Directory) {
			release()
			return Execution{}, Fail("FORBIDDEN", "native session identity mismatch")
		}
		directory = meta.Directory
		if scoped, ok := r.(directoryScoped); ok && scoped.Directory() != "" && scoped.Directory() != directory {
			release()
			return Execution{}, Fail("FORBIDDEN", "native workspace changed")
		}
		resolved, err := filepath.EvalSymlinks(directory)
		if err != nil || resolved != directory || filepath.Clean(directory) != directory {
			release()
			return Execution{}, Fail("FORBIDDEN", "native workspace unavailable")
		}
		canonical = resolved
		workspace, err = os.Stat(canonical)
		if err != nil || !workspace.IsDir() {
			release()
			return Execution{}, Fail("FORBIDDEN", "native workspace unavailable")
		}
	}
	statuses, err := r.Status(ctx)
	if err != nil {
		release()
		return Execution{}, err
	}
	if s, ok := statuses[session]; ok && s.Type != "idle" {
		release()
		return Execution{}, Fail("SESSION_BUSY", "native session busy")
	}
	history, err := r.History(ctx, session)
	if err != nil {
		release()
		return Execution{}, err
	}
	for _, msg := range history {
		if msg.Info.Role == "assistant" && msg.Info.Time.Completed <= 0 {
			release()
			return Execution{}, Fail("SESSION_BUSY", "unfinished native assistant message")
		}
	}
	{
		p := r
		permissions, e := p.Permissions(ctx)
		if e != nil {
			release()
			return Execution{}, e
		}
		for _, v := range permissions {
			if v.SessionID == session {
				release()
				return Execution{}, Fail("SESSION_BUSY", "native permission pending")
			}
		}
	}
	if err = ctx.Err(); err != nil {
		release()
		return Execution{}, err
	}
	now := time.Now().UTC()
	x := &run{native: session, runner: r, op: make(chan struct{}, 1), directory: directory, canonicalDirectory: canonical, workspace: workspace, view: Execution{ID: id, SessionID: "opencode:" + session, MessageID: messageID(), State: "submitting", Started: now, Updated: now}}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		release()
		return Execution{}, Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	m.runs[id] = x
	m.keys[k] = id
	m.fingerprints[k] = hash
	m.wg.Add(1)
	m.mu.Unlock()
	initial := snapshot(x.view)
	go m.work(x, prompt)
	return initial, nil
}
func (m *Manager) work(x *run, prompt string) {
	defer m.wg.Done()
	ctx, cancel := context.WithTimeout(m.ctx, m.timeout)
	defer cancel()
	defer func() {
		x.mu.Lock()
		defer x.mu.Unlock()
		now := time.Now().UTC()
		x.monitorDone = true
		x.view.MonitorEndedAt = &now
		if !terminal(x.view.State) && m.ctx.Err() != nil {
			unresolved(x, "MANAGER_CLOSED", "bridge shutdown; native work may continue", now, false)
		}
	}()
	// Recheck filesystem identity and routing immediately before submission. This
	// detects observed replacements, not an atomic lease on native path lookup.
	meta, err := x.runner.Get(ctx, x.native)
	if err == nil {
		err = validateSession(x, meta)
	}
	if err != nil || ctx.Err() != nil {
		x.mu.Lock()
		unresolved(x, "PRE_SUBMISSION_UNAVAILABLE", "accepted workspace/session could not be revalidated; prompt not submitted", time.Now().UTC(), false)
		x.mu.Unlock()
		return
	}
	// Accepted work is detached from the caller. No retry of Prompt, even on timeout.
	if err := x.runner.Prompt(ctx, x.native, prompt, x.view.MessageID); err != nil {
		x.mu.Lock()
		unresolved(x, "SUBMISSION_UNCONFIRMED", "submission not confirmed; never replay blindly", time.Now().UTC(), false)
		x.mu.Unlock()
		return
	}
	x.mu.Lock()
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
	x.mu.Unlock()
	if terminal(v.State) {
		return v, Fail("INVALID_INPUT", "execution already terminal")
	}
	if v.State == "submitting" {
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
		if err = validateSession(x, opencode.Session{ID: x.native, Directory: x.directory}); err != nil {
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
