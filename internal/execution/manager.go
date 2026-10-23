// Package execution tracks in-memory, at-most-once submissions to existing sessions.
package execution

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

type Runner interface {
	History(context.Context, string) ([]opencode.Message, error)
	Status(context.Context) (map[string]opencode.Status, error)
	Prompt(context.Context, string, string, string) error
	Abort(context.Context, string) error
}
type permissionReader interface {
	Permissions(context.Context) ([]opencode.Permission, error)
}

type Error struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Retryable   bool   `json:"retryable"`
	ExecutionID string `json:"execution_id,omitempty"`
}

func (e *Error) Error() string        { return e.Code + ": " + e.Message }
func Fail(code, message string) error { return &Error{Code: code, Message: message} }

type Execution struct {
	ID        string     `json:"execution_id"`
	SessionID string     `json:"session_id"`
	MessageID string     `json:"message_id"`
	State     string     `json:"state"`
	Error     string     `json:"error,omitempty"`
	Started   time.Time  `json:"started_at"`
	Updated   time.Time  `json:"updated_at"`
	Ended     *time.Time `json:"ended_at,omitempty"`
}
type run struct {
	mu             sync.Mutex
	view           Execution
	native         string
	runner         Runner
	messages       []opencode.Message
	abortRequested bool
}
type Manager struct {
	mu                sync.Mutex
	r                 Runner
	runs              map[string]*run
	busy              map[string]string
	keys              map[string]string
	fingerprints      map[string][32]byte
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	timeout, interval time.Duration
	closed            bool
}

func New(r Runner) *Manager { return NewWithTiming(r, 5*time.Minute, 500*time.Millisecond) }
func NewWithTiming(r Runner, timeout, interval time.Duration) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{r: r, runs: map[string]*run{}, busy: map[string]string{}, keys: map[string]string{}, fingerprints: map[string][32]byte{}, ctx: ctx, cancel: cancel, timeout: timeout, interval: interval}
}
func (m *Manager) Close() { m.mu.Lock(); m.closed = true; m.cancel(); m.mu.Unlock(); m.wg.Wait() }
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
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Execution{}, Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	if id, ok := m.keys[k]; ok {
		if m.fingerprints[k] != hash {
			m.mu.Unlock()
			return Execution{}, Fail("INVALID_INPUT", "idempotency key conflict")
		}
		x := m.runs[id]
		m.mu.Unlock()
		x.mu.Lock()
		defer x.mu.Unlock()
		return x.view, nil
	}
	if m.busy[session] != "" {
		m.mu.Unlock()
		return Execution{}, Fail("SESSION_BUSY", "session has an active or uncertain execution")
	}
	// Reserve before any network IO, without blocking unrelated sessions.
	id := token()
	m.busy[session] = id
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		if m.busy[session] == id {
			delete(m.busy, session)
		}
		m.mu.Unlock()
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
		if msg.Info.Role == "assistant" && msg.Info.Time.Completed == 0 {
			release()
			return Execution{}, Fail("SESSION_BUSY", "unfinished native assistant message")
		}
	}
	if p, ok := r.(permissionReader); ok {
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
	x := &run{native: session, runner: r, view: Execution{ID: id, SessionID: "opencode:" + session, MessageID: messageID(), State: "submitting", Started: now, Updated: now}}
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
	initial := x.view
	go m.work(x, prompt)
	return initial, nil
}
func (m *Manager) work(x *run, prompt string) {
	defer m.wg.Done()
	ctx, cancel := context.WithTimeout(m.ctx, m.timeout)
	defer cancel()
	// The caller disconnecting after acceptance must not cancel a submitted native prompt.
	if err := x.runner.Prompt(ctx, x.native, prompt, x.view.MessageID); err != nil {
		x.mu.Lock()
		x.view.State = "unknown"
		x.view.Error = "UNKNOWN_OUTCOME: submission not confirmed; never retry blindly"
		x.view.Updated = time.Now().UTC()
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
			x.view.State = "unknown"
			x.view.Error = "UNKNOWN_OUTCOME: monitor timeout or bridge shutdown; native execution may continue"
			x.view.Updated = time.Now().UTC()
			x.mu.Unlock()
			return
		case <-ticker.C:
		}
	}
}
func (m *Manager) observe(ctx context.Context, x *run) bool {
	statuses, err := x.runner.Status(ctx)
	if err != nil {
		x.mu.Lock()
		defer x.mu.Unlock()
		x.view.State = "unknown"
		x.view.Error = "UPSTREAM_FAILURE: status unavailable"
		x.view.Updated = time.Now().UTC()
		return false
	}
	msgs, err := x.runner.History(ctx, x.native)
	if err != nil {
		x.mu.Lock()
		defer x.mu.Unlock()
		x.view.State = "unknown"
		x.view.Error = "UPSTREAM_FAILURE: history unavailable"
		x.view.Updated = time.Now().UTC()
		return false
	}
	var permissions []opencode.Permission
	var permissionErr error
	if p, ok := x.runner.(permissionReader); ok {
		permissions, permissionErr = p.Permissions(ctx)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.messages = nil
	for _, msg := range msgs {
		if msg.Info.SessionID != x.native {
			continue
		}
		if msg.Info.ID == x.view.MessageID && msg.Info.Role == "user" || msg.Info.ParentID == x.view.MessageID && msg.Info.Role == "assistant" {
			x.messages = append(x.messages, msg)
		}
	}
	x.view.State = "running"
	x.view.Error = ""
	x.view.Updated = time.Now().UTC()
	status, exists := statuses[x.native]
	for _, msg := range x.messages {
		if msg.Info.Role != "assistant" || msg.Info.Time.Completed == 0 {
			continue
		}
		state := ""
		if len(msg.Info.Error) > 0 && string(msg.Info.Error) != "null" {
			state = "failed"
			var v struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(msg.Info.Error, &v)
			if v.Name == "MessageAbortedError" {
				state = "interrupted"
			}
		}
		if msg.Info.Finish == "stop" || msg.Info.Finish == "end_turn" {
			if state == "" {
				state = "completed"
			}
		}
		// A tool-calls finish is only an intermediate assistant message.
		if state != "" && (!exists || status.Type == "idle") {
			x.view.State = state
			now := time.Now().UTC()
			x.view.Ended = &now
			if state == "failed" {
				x.view.Error = "UPSTREAM_FAILURE: correlated assistant error (inspect result)"
			}
			m.mu.Lock()
			if m.busy[x.native] == x.view.ID {
				delete(m.busy, x.native)
			}
			m.mu.Unlock()
			return true
		}
	}
	if _, ok := x.runner.(permissionReader); ok {
		if permissionErr != nil {
			x.view.State = "unknown"
			x.view.Error = "UPSTREAM_FAILURE: permissions unavailable"
		} else {
			for _, p := range permissions {
				if p.SessionID == x.native {
					x.view.State = "waiting_for_permission"
					return false
				}
			}
		}
	}
	if !exists {
		x.view.State = "unknown"
		x.view.Error = "UNKNOWN_OUTCOME: native status absent without terminal message"
	} else if status.Type != "idle" && status.Type != "busy" && status.Type != "retry" {
		x.view.State = "unknown"
		x.view.Error = "UNKNOWN_OUTCOME: unsupported native status"
	}
	return false
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
func (m *Manager) Get(ctx context.Context, id string) (Execution, error) {
	if e := ctx.Err(); e != nil {
		return Execution{}, e
	}
	x, e := m.find(id)
	if e != nil {
		return Execution{}, e
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.view, nil
}
func (m *Manager) Result(ctx context.Context, id string) (Execution, []opencode.Message, error) {
	if e := ctx.Err(); e != nil {
		return Execution{}, nil, e
	}
	x, e := m.find(id)
	if e != nil {
		return Execution{}, nil, e
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.view, append([]opencode.Message(nil), x.messages...), nil
}
func (m *Manager) Interrupt(ctx context.Context, id string) (Execution, error) {
	x, e := m.find(id)
	if e != nil {
		return Execution{}, e
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if terminal(x.view.State) {
		return x.view, Fail("INVALID_INPUT", "execution already terminal")
	}
	if x.view.State == "submitting" {
		return x.view, Fail("SESSION_BUSY", "submission in progress; retry interruption after acknowledgement")
	}
	// Abort acknowledgement is not completion; keep the guard until correlated terminal proof.
	if !x.abortRequested {
		if e = x.runner.Abort(ctx, x.native); e != nil {
			x.view.State = "unknown"
			x.view.Error = "UNKNOWN_OUTCOME: abort not confirmed"
			return x.view, e
		}
		x.abortRequested = true
	}
	x.view.Updated = time.Now().UTC()
	return x.view, nil
}
