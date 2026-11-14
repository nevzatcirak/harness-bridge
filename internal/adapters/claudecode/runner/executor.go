package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// Config is everything an Executor needs. Nothing here is read from the
// environment or from a Claude user's files.
type Config struct {
	Wrapper string // absolute path (BRIDGE_CLAUDE_COMMAND), validated by ValidateWrapper
	FS      claudecode.FS
	Env     claudecode.EnvConfig

	Model        string
	MaxTurns     int
	MaxBudgetUSD string
	// ConfigDir is the test-only CLAUDE_CONFIG_DIR deny rule (§8.3 item 6); production
	// leaves it empty.
	ConfigDir string

	// AllowedVersions (BRIDGE_CLAUDE_VERSIONS, parsed by claudecode.ParseVersions) must be a
	// non-empty list of exact versions and Tools a valid profile: New refuses anything else,
	// so no wiring can skip the §8.5 check.
	AllowedVersions []string
	Tools           []string

	Sessions []claudecode.Session
	// AllowedSessions is BRIDGE_ALLOWED_SESSIONS: every allowlisted session must also
	// be listed here (§6). New enforces it, so no wiring can skip the check.
	AllowedSessions []string
	Locks           *Locks
	History         *claudecode.Store
	Spawner         Spawner

	// RootCheck re-validates a session root at admission (default: absolute,
	// a directory, no symlink in the path).
	RootCheck func(root string) error

	RunMax      time.Duration // wall-clock cap per child (default 15 min)
	Grace       time.Duration // exit grace after a result (default 10 s)
	IntWait     time.Duration // §4: wait after SIGINT before SIGTERM (default 10 s)
	TermWait    time.Duration // §4: wait after SIGTERM before the escalation is exhausted (default 5 s)
	DrainMax    int64         // bytes read and discarded after a broken stream (default 64 MiB)
	CloseWait   time.Duration // how long Close waits for interrupted children before it abandons them (default 15 s); nothing is ever killed
	MaxChildren int           // concurrent children (default 4)
	Now         func() time.Time
}

// Request is one send_prompt.
type Request struct {
	Session   string // claudecode:<uuid>
	Prompt    string
	Key       string // idempotency key, 8..128 bytes
	Principal string
}

// Executor admits executions and runs one child per execution, at most one per
// session (ADR-010 semantics, ADR-021 §2). Its state is memory only.
type Executor struct {
	cfg      Config
	sessions map[string]claudecode.Session

	mu       sync.Mutex
	closed   bool
	runs     map[string]*run
	busy     map[string]string
	keys     map[string]string
	fps      map[string][32]byte
	pending  map[string]chan struct{}
	closing  chan struct{} // closed by the first Close
	children int
	wg       sync.WaitGroup
}

// New validates cfg and returns an Executor. It refuses an invalid wrapper and a
// session whose writer lock this process does not hold.
func New(cfg Config) (*Executor, error) {
	if cfg.History == nil || cfg.Locks == nil || cfg.Spawner == nil {
		return nil, errors.New("runner: history store, locks and spawner are required")
	}
	if cfg.FS == nil {
		cfg.FS = claudecode.OSFS{}
	}
	if cfg.RootCheck == nil {
		cfg.RootCheck = checkRoot
	}
	if cfg.RunMax <= 0 {
		cfg.RunMax = 15 * time.Minute
	}
	if cfg.Grace <= 0 {
		cfg.Grace = 10 * time.Second
	}
	if cfg.IntWait <= 0 {
		cfg.IntWait = 10 * time.Second
	}
	if cfg.TermWait <= 0 {
		cfg.TermWait = 5 * time.Second
	}
	if cfg.DrainMax <= 0 {
		cfg.DrainMax = 64 << 20
	}
	if cfg.CloseWait <= 0 {
		cfg.CloseWait = 15 * time.Second
	}
	if cfg.MaxChildren <= 0 {
		cfg.MaxChildren = 4
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := claudecode.ValidateWrapper(cfg.FS, cfg.Wrapper); err != nil {
		return nil, err
	}
	if err := claudecode.CheckVersions(cfg.AllowedVersions); err != nil {
		return nil, err
	}
	if err := claudecode.CheckToolProfile(cfg.Tools); err != nil {
		return nil, err
	}
	if err := claudecode.CheckSessionACL(cfg.Sessions, cfg.AllowedSessions); err != nil {
		return nil, err
	}
	e := &Executor{cfg: cfg, sessions: map[string]claudecode.Session{}, runs: map[string]*run{}, busy: map[string]string{},
		keys: map[string]string{}, fps: map[string][32]byte{}, pending: map[string]chan struct{}{}, closing: make(chan struct{})}
	for _, s := range cfg.Sessions {
		if !cfg.Locks.Held(s.ID) {
			return nil, errors.New("runner: the writer lock of " + s.ID + " is not held")
		}
		e.sessions[s.ID] = s
	}
	// A turn_start without a turn_end belongs to a run the previous process never
	// finished: mark it restart_gap (§5). Execution state is memory only and starts
	// empty; the marker is the only trace.
	for _, s := range cfg.Sessions {
		if _, err := cfg.History.RecoverOpenTurns(s.UUID()); err != nil {
			return nil, errors.New("runner: history recovery failed for " + s.ID)
		}
	}
	return e, nil
}

func checkRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("root is not absolute and clean")
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil || real != root {
		return errors.New("root is not a real path (symlink alias or missing)")
	}
	if info, err := (claudecode.OSFS{}).Lstat(root); err != nil || !info.Mode.IsDir() {
		return errors.New("root is not a directory")
	}
	return nil
}

func token() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func clone(x execution.Execution) execution.Execution {
	if x.Unresolved != nil {
		u := *x.Unresolved
		x.Unresolved = &u
	}
	if x.LocalFailure != nil {
		l := *x.LocalFailure
		x.LocalFailure = &l
	}
	if x.Ended != nil {
		t := *x.Ended
		x.Ended = &t
	}
	if x.GuardReleasedAt != nil {
		t := *x.GuardReleasedAt
		x.GuardReleasedAt = &t
	}
	return x
}

func busyErr(msg string, retryable bool) error {
	return &execution.Error{Code: "SESSION_BUSY", Message: msg, Retryable: retryable}
}

// Send admits one execution. replay is true when the same session, key and
// payload returned the existing execution without a new child.
func (e *Executor) Send(ctx context.Context, req Request) (execution.Execution, bool, error) {
	uuid, perr := claudecode.ParseSessionID(req.Session)
	if perr != nil || strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > 32768 || len(req.Key) < 8 || len(req.Key) > 128 {
		return execution.Execution{}, false, execution.Fail("INVALID_INPUT", "invalid session, prompt or idempotency key")
	}
	k := req.Session + "\x00" + req.Key
	fp := sha256.Sum256([]byte(req.Prompt))
retry:
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return execution.Execution{}, false, execution.Fail("UNKNOWN_OUTCOME", "manager closed")
	}
	if id, ok := e.keys[k]; ok {
		if e.fps[k] != fp {
			e.mu.Unlock()
			return execution.Execution{}, false, execution.Fail("IDEMPOTENCY_CONFLICT", "idempotency key reused with different payload")
		}
		r := e.runs[id]
		e.mu.Unlock()
		return r.snapshot(), true, nil
	}
	if p, ok := e.pending[k]; ok {
		if e.fps[k] != fp {
			e.mu.Unlock()
			return execution.Execution{}, false, execution.Fail("IDEMPOTENCY_CONFLICT", "idempotency key reused with different payload")
		}
		e.mu.Unlock()
		select {
		case <-p:
			goto retry
		case <-ctx.Done():
			return execution.Execution{}, false, ctx.Err()
		}
	}
	if e.busy[req.Session] != "" {
		e.mu.Unlock()
		return execution.Execution{}, false, execution.Fail("SESSION_BUSY", "session has an active or uncertain execution")
	}
	// Reserve the guard before any IO, so unrelated sessions are not blocked. The child
	// slot is taken only after the preflight: a session that is refused is told so
	// (NOT_FOUND, FORBIDDEN) even at capacity.
	r := e.newRun(uuid, req)
	e.busy[req.Session] = r.id
	e.runs[r.id] = r
	e.pending[k] = make(chan struct{})
	e.fps[k] = fp
	e.wg.Add(1)
	e.mu.Unlock()

	slot := false
	reject := func(err error) (execution.Execution, bool, error) {
		e.mu.Lock()
		if e.busy[req.Session] == r.id {
			delete(e.busy, req.Session)
		}
		delete(e.runs, r.id)
		delete(e.fps, k)
		close(e.pending[k])
		delete(e.pending, k)
		if slot {
			e.children--
		}
		e.mu.Unlock()
		e.wg.Done()
		return execution.Execution{}, false, err
	}
	if err := e.preflight(r); err != nil {
		return reject(err)
	}
	e.mu.Lock()
	if e.children >= e.cfg.MaxChildren {
		e.mu.Unlock()
		return reject(busyErr("child capacity reached", true))
	}
	e.children++
	slot = true
	e.mu.Unlock()
	err := e.cfg.History.BeginTurn(uuid, r.id, claudecode.TurnStart{Prompt: req.Prompt, Principal: req.Principal, KeyHashPrefix: keyPrefix(req.Key)})
	switch {
	case errors.Is(err, claudecode.ErrSessionHistoryFull):
		return reject(execution.Fail("UPSTREAM_FAILURE", "session history is at its size bound; archive segments before a new execution"))
	case err != nil:
		return reject(execution.Fail("UPSTREAM_FAILURE", "history store unavailable"))
	}
	e.mu.Lock()
	e.keys[k] = r.id
	close(e.pending[k])
	delete(e.pending, k)
	e.mu.Unlock()
	go e.work(r, req.Prompt)
	return r.snapshot(), false, nil
}

func keyPrefix(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])[:16]
}

// preflight is the Claude admission preflight (§2): there is nothing native to
// read, so the checks are the allowlist, the writer lock, the root, the wrapper.
// A failure starts no child and the caller releases only its own reservation.
func (e *Executor) preflight(r *run) error {
	s, ok := e.sessions[r.session]
	if !ok {
		return execution.Fail("NOT_FOUND", "session is not in the Claude allowlist")
	}
	if !e.cfg.Locks.Held(r.session) {
		return execution.Fail("FORBIDDEN", "writer ownership of the session is not held")
	}
	if err := e.cfg.RootCheck(s.Root); err != nil {
		return execution.Fail("FORBIDDEN", "session root failed the path checks")
	}
	if err := claudecode.ValidateWrapper(e.cfg.FS, e.cfg.Wrapper); err != nil {
		return execution.Fail("FORBIDDEN", "the wrapper failed validation")
	}
	r.root = s.Root
	return nil
}

func (e *Executor) release(session, id string) {
	e.mu.Lock()
	if e.busy[session] == id {
		delete(e.busy, session)
	}
	e.mu.Unlock()
}

// childDone gives the child slot back once the process was reaped or never started.
func (e *Executor) childDone() {
	e.mu.Lock()
	e.children--
	e.mu.Unlock()
	e.wg.Done()
}

func (e *Executor) find(id string) (*run, error) {
	e.mu.Lock()
	r := e.runs[id]
	e.mu.Unlock()
	if r == nil {
		return nil, execution.Fail("NOT_FOUND", "unknown execution")
	}
	return r, nil
}

// Get returns a snapshot of an execution.
func (e *Executor) Get(id string) (execution.Execution, error) {
	r, err := e.find(id)
	if err != nil {
		return execution.Execution{}, err
	}
	return r.snapshot(), nil
}

// Verdict returns the state machine's verdict of an execution (decided or not).
func (e *Executor) Verdict(id string) (claudecode.Verdict, error) {
	r, err := e.find(id)
	if err != nil {
		return claudecode.Verdict{}, err
	}
	return r.verdictCopy(), nil
}

// Wait blocks until the child of an execution was reaped (or ctx ends) and
// returns the final snapshot.
func (e *Executor) Wait(ctx context.Context, id string) (execution.Execution, error) {
	r, err := e.find(id)
	if err != nil {
		return execution.Execution{}, err
	}
	select {
	case <-r.done:
		return r.snapshot(), nil
	case <-ctx.Done():
		return r.snapshot(), ctx.Err()
	}
}

// SessionGuard reports whether the in-process guard of a session is held and the
// execution state behind it, like execution.Manager.SessionGuard.
func (e *Executor) SessionGuard(session string) (held bool, state string) {
	e.mu.Lock()
	id := e.busy[session]
	r := e.runs[id]
	e.mu.Unlock()
	if id == "" || r == nil {
		return false, ""
	}
	return true, r.snapshot().State
}

// Interrupt is interrupt_execution (ADR-021 §4, reason user): SIGINT to the child's group,
// then the bounded SIGINT-to-SIGTERM escalation, and the machine decides (row 5 only when the
// CLI acknowledges with an aborted_* result, row 6b when the escalation is exhausted). It
// returns the execution as it was at request time (it can still be running) and the caller
// polls. An execution that already has a verdict is returned unchanged and nothing is sent.
// Authorization is the caller's (wiring slice): it is not exposed through MCP here.
func (e *Executor) Interrupt(id string) (execution.Execution, error) {
	r, err := e.find(id)
	if err != nil {
		return execution.Execution{}, err
	}
	return r.userInterrupt(), nil
}

// Close stops admitting and interrupts every live child (SIGINT, reason shutdown, §3
// row 9s; then the §4 escalation). It waits up to CloseWait for them to be reaped, then
// ABANDONS the wait: a child that ignores SIGINT is not killed (ADR-021 §4), its run keeps
// its guard held, and its execution ID is returned so the caller can log the abandonment.
// A run whose Wait returned an unknown status (the pipeline ended, the child may not have)
// is returned too (#253 P3-3). The
// guard of an interrupted run stays held in any case: the verdict is unknown.
//
// Close is idempotent: only the first call interrupts and waits. A repeated call returns
// at once with the executions that are STILL not reaped at that moment (no second wait).
func (e *Executor) Close() []string {
	e.mu.Lock()
	first := !e.closed
	e.closed = true
	if first {
		close(e.closing)
	}
	runs := make([]*run, 0, len(e.runs))
	for _, r := range e.runs {
		runs = append(runs, r)
	}
	e.mu.Unlock()
	if first {
		for _, r := range runs {
			r.shutdown()
		}
		done := make(chan struct{})
		go func() { e.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(e.cfg.CloseWait):
		}
	}
	var abandoned []string
	for _, r := range runs {
		if r.unreaped() {
			abandoned = append(abandoned, r.id)
		}
	}
	sort.Strings(abandoned)
	return abandoned
}

func (e *Executor) newRun(uuid string, req Request) *run {
	now := e.cfg.Now().UTC()
	id := token()
	root := e.sessions[req.Session].Root
	return &run{
		e: e, id: id, session: req.Session, uuid: uuid, root: root, done: make(chan struct{}),
		view: execution.Execution{ID: id, SessionID: req.Session, MessageID: "msg_" + id[:16], State: "submitting", Started: now, Updated: now},
		m:    claudecode.NewMachine(claudecode.Config{RequestedUUID: uuid, CanonicalRoot: root, AllowedVersions: e.cfg.AllowedVersions, Tools: e.cfg.Tools}),
	}
}
