// Package claudewire wires the ADR-021 Claude Code adapter into the Bridge, default off. Open
// builds the whole adapter (allowlist, version list, history store, writer locks, Executor)
// from one explicit Config or fails with a clear error and holds nothing: there is no partial
// enablement. A Bridge without a Config has no Harness, so a claudecode: session ID keeps its
// "not enabled" refusal and every OpenCode response is untouched.
package claudewire

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode/runner"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// Config is everything Open needs. Nothing is read from the environment or from a Claude
// user's files except the allowlist file named by SessionsFile.
type Config struct {
	Command      string // BRIDGE_CLAUDE_COMMAND: the root-owned wrapper (absolute path)
	SessionsFile string // BRIDGE_CLAUDE_SESSIONS
	Versions     string // BRIDGE_CLAUDE_VERSIONS (raw value, exact versions only)
	HistoryDir   string // BRIDGE_CLAUDE_HISTORY_DIR
	LockDir      string // BRIDGE_WRITER_LOCK_DIR (shared with the OpenCode writer)
	Home         string // child HOME (the Claude service user's)
	Lang         string // child LANG; default C.UTF-8
	Model        string
	MaxTurns     int
	MaxBudgetUSD string

	AllowedRoots    []string // BRIDGE_ALLOWED_ROOTS
	AllowedSessions []string // BRIDGE_ALLOWED_SESSIONS (every entry; claudecode: ones are used)
	ServiceUID      uint32

	// Test seams and optional limits; zero values take the production defaults.
	FS      claudecode.FS
	Spawner runner.Spawner
	Runner  runner.Config // limits only (RunMax, Grace, IntWait, ...); the rest is overwritten
}

// Harness is the running adapter.
type Harness struct {
	ex    *runner.Executor
	store *claudecode.Store
	locks *runner.Locks
}

// ClaudeSessionIDs filters the claudecode: entries out of an ACL list. The OpenCode writer
// lock must not take them: the Claude locks (runner.AcquireLocks) own the same hashed lock
// names, and a second flock of one name in one process is refused (#236 P3-4).
func ClaudeSessionIDs(all []string) (claude, other []string) {
	for _, id := range all {
		if strings.HasPrefix(id, claudecode.SessionPrefix) {
			claude = append(claude, id)
		} else {
			other = append(other, id)
		}
	}
	return claude, other
}

// Open validates cfg and builds the adapter. On any error nothing stays open.
func Open(cfg Config) (*Harness, error) {
	versions, err := claudecode.ParseVersions(cfg.Versions)
	if err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}
	// The per-run values are checked once here, so a bad model, turn limit, budget or child
	// HOME stops the start instead of failing every run at spawn time.
	tools := []string{"Read", "Glob", "Grep"}
	if _, err := claudecode.ScrubEnv(nil, claudecode.EnvConfig{Home: cfg.Home, Lang: orDefault(cfg.Lang, "C.UTF-8")}); err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}
	if _, err := claudecode.BuildArgv(claudecode.ArgvConfig{UUID: "00000000-0000-4000-8000-000000000000", Model: cfg.Model, MaxTurns: cfg.MaxTurns,
		MaxBudgetUSD: cfg.MaxBudgetUSD, Root: "/srv/claude-root", Tools: tools}); err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}
	fsys := cfg.FS
	if fsys == nil {
		fsys = claudecode.OSFS{}
	}
	sessions, err := claudecode.LoadSessions(fsys, cfg.SessionsFile, cfg.ServiceUID, cfg.AllowedRoots)
	if err != nil {
		return nil, fmt.Errorf("claude: allowlist: %w", err)
	}
	if len(sessions) == 0 {
		return nil, errors.New("claude: the session allowlist is empty")
	}
	acl, _ := ClaudeSessionIDs(cfg.AllowedSessions)
	if cfg.LockDir == "" {
		return nil, errors.New("claude: the writer lock directory is required")
	}
	store, err := claudecode.OpenStore(cfg.HistoryDir, claudecode.DefaultStoreConfig())
	if err != nil {
		return nil, fmt.Errorf("claude: history store: %w", err)
	}
	locks, err := runner.AcquireLocks(cfg.LockDir, sessions)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("claude: writer locks: %w", err)
	}
	rc := cfg.Runner
	rc.Wrapper, rc.FS = cfg.Command, fsys
	rc.Env = claudecode.EnvConfig{Home: cfg.Home, Lang: orDefault(cfg.Lang, "C.UTF-8")}
	rc.Model, rc.MaxTurns, rc.MaxBudgetUSD = cfg.Model, cfg.MaxTurns, cfg.MaxBudgetUSD
	rc.AllowedVersions, rc.Tools = versions, tools
	rc.Sessions, rc.AllowedSessions = sessions, acl
	rc.Locks, rc.History = locks, store
	if cfg.Spawner != nil {
		rc.Spawner = cfg.Spawner
	} else {
		rc.Spawner = runner.ExecSpawner{}
	}
	ex, err := runner.New(rc)
	if err != nil {
		locks.Close()
		_ = store.Close()
		return nil, fmt.Errorf("claude: %w", err)
	}
	return &Harness{ex: ex, store: store, locks: locks}, nil
}

func orDefault(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

// Close interrupts every live child (never kills one), then releases the history store and
// the writer locks. A child that ignored the interrupt keeps its lock until the process ends.
func (h *Harness) Close() []string {
	if h == nil {
		return nil
	}
	abandoned := h.ex.Close()
	if len(abandoned) == 0 {
		h.locks.Close()
		_ = h.store.Close()
	}
	return abandoned
}

// Executor returns the process side (admin release, tests).
func (h *Harness) Executor() *runner.Executor { return h.ex }

// ---- the surface the MCP layer routes to ----

// Sessions are the allowlisted sessions, in allowlist order.
func (h *Harness) Sessions() []claudecode.Session { return h.ex.Sessions() }

// Session looks up one allowlisted session by its canonical claudecode:<uuid> ID.
func (h *Harness) Session(id string) (claudecode.Session, bool) {
	for _, s := range h.ex.Sessions() {
		if s.ID == id {
			return s, true
		}
	}
	return claudecode.Session{}, false
}

// State is the Claude session state of ADR-021 §6: there is no native evidence, so it is
// "running" while the Bridge runs a child, "unknown" while an unknown or unreaped execution
// holds the guard, otherwise "idle" (only "the Bridge holds no guard and runs no child").
func (h *Harness) State(id string) string {
	held, state := h.ex.SessionGuard(id)
	switch {
	case !held:
		return "idle"
	case state == "running" || state == "submitting":
		return "running"
	default:
		return "unknown"
	}
}

// Version is the version last seen in an init ("" before any run).
func (h *Harness) Version() string { return h.ex.Version() }

// Send admits one execution (ADR-010 semantics over the Executor).
func (h *Harness) Send(ctx context.Context, session, prompt, key, principal string) (execution.Execution, bool, error) {
	return h.ex.Send(ctx, runner.Request{Session: session, Prompt: prompt, Key: key, Principal: principal})
}

// Owns reports whether the execution belongs to this adapter.
func (h *Harness) Owns(id string) bool { return h.ex.Owns(id) }

// Get is get_execution_status.
func (h *Harness) Get(id string) (execution.Execution, error) { return h.ex.Get(id) }

// Interrupt is interrupt_execution (reason user).
func (h *Harness) Interrupt(id string) (execution.Execution, error) { return h.ex.Interrupt(id) }

// History reads the Bridge's own record of a session's turns.
func (h *Harness) History(session string) (claudecode.ReadResult, error) {
	uuid, err := claudecode.ParseSessionID(session)
	if err != nil {
		return claudecode.ReadResult{}, err
	}
	return h.store.Read(uuid)
}

// ReleaseGuard is the admin release (ADR-020 extension, slice 4).
func (h *Harness) ReleaseGuard(ctx context.Context, session, executionID string, opt claudecode.ReleaseOptions) (claudecode.ReleaseEvidence, error) {
	return h.ex.ReleaseGuard(ctx, session, executionID, opt)
}
