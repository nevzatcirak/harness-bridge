package claudewire

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode/runner"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

const (
	wrapper = "/usr/local/libexec/hb-claude"
	uuid1   = "00000000-0000-4000-8000-000000000001"
	uuid2   = "00000000-0000-4000-8000-000000000002"
	opcode  = "ses_example"
)

// wrapperFS answers the wrapper and its ancestors as root-owned (so ValidateWrapper passes
// without a real root-owned file) and everything else from the real filesystem (the allowlist
// file, the history and lock directories are real).
type wrapperFS struct{ claudecode.OSFS }

func (wrapperFS) Lstat(p string) (claudecode.FileInfo, error) {
	switch {
	case p == wrapper:
		return claudecode.FileInfo{Mode: 0o755, UID: 0, Nlink: 1}, nil
	case p == "/" || strings.HasPrefix(wrapper, strings.TrimSuffix(p, "/")+"/"):
		return claudecode.FileInfo{Mode: fs.ModeDir | 0o755, UID: 0, Nlink: 2}, nil
	}
	return claudecode.OSFS{}.Lstat(p)
}

type fakeChild struct {
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	exitCh  chan runner.Exit
	once    sync.Once
}

func newFakeChild() *fakeChild {
	r, w := io.Pipe()
	return &fakeChild{stdoutR: r, stdoutW: w, exitCh: make(chan runner.Exit, 1)}
}
func (c *fakeChild) Stdout() io.Reader                { return c.stdoutR }
func (c *fakeChild) WriteStdin(p []byte) (int, error) { return len(p), nil }
func (c *fakeChild) CloseStdin() error                { return nil }
func (c *fakeChild) Wait() runner.Exit                { return <-c.exitCh }
func (c *fakeChild) GroupEmpty() (bool, error)        { return true, nil }
func (c *fakeChild) finish(x runner.Exit)             { c.once.Do(func() { _ = c.stdoutW.Close(); c.exitCh <- x }) }
func (c *fakeChild) emit(line string)                 { _, _ = io.WriteString(c.stdoutW, line+"\n") }
func (c *fakeChild) Signal(s runner.Signal) error {
	c.finish(runner.Exit{Code: 130, Signal: "interrupt"})
	return nil
}

type spawner struct {
	mu       sync.Mutex
	children []*fakeChild
	specs    []runner.Spec
}

func (s *spawner) Start(spec runner.Spec) (runner.Child, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := newFakeChild()
	s.children = append(s.children, c)
	s.specs = append(s.specs, spec)
	return c, nil
}
func (s *spawner) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.children) }
func (s *spawner) last() *fakeChild {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.children[len(s.children)-1]
}

type env struct {
	cfg     Config
	sp      *spawner
	roots   string
	lockDir string
}

func private(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	list := []claudecode.Session{{ID: "claudecode:" + uuid1, Root: proj, Label: "p1"}}
	b, _ := json.Marshal(list)
	file := filepath.Join(private(t), "sessions.json")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sp := &spawner{}
	e := &env{sp: sp, roots: root, lockDir: private(t)}
	e.cfg = Config{Command: wrapper, SessionsFile: file, Versions: "2.1.322", HistoryDir: private(t), LockDir: e.lockDir, Home: "/home/claude",
		Model: "claude-sonnet-5-5", MaxTurns: 5, AllowedRoots: []string{root}, AllowedSessions: []string{opcode, "claudecode:" + uuid1}, ServiceUID: uint32(os.Geteuid()),
		FS: wrapperFS{}, Spawner: sp, Runner: runner.Config{RunMax: 5 * time.Second, Grace: 5 * time.Second, IntWait: 200 * time.Millisecond, TermWait: 200 * time.Millisecond, CloseWait: 2 * time.Second}}
	return e
}

func (e *env) open(t *testing.T) *Harness {
	t.Helper()
	h, err := Open(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func (e *env) initLine(session, root string) string {
	b, _ := json.Marshal(map[string]any{"type": "system", "subtype": "init", "cwd": root, "session_id": session, "model": "m", "permissionMode": "default",
		"apiKeySource": "none", "claude_code_version": "2.1.322", "mcp_servers": []any{}, "plugins": []any{}, "tools": []any{"Read", "Glob", "Grep"}})
	return string(b)
}

func (e *env) root(t *testing.T) string {
	t.Helper()
	list, _ := os.ReadFile(e.cfg.SessionsFile)
	var s []claudecode.Session
	_ = json.Unmarshal(list, &s)
	return s[0].Root
}

func errCode(err error) string {
	if x, ok := err.(*execution.Error); ok {
		return x.Code
	}
	if err == nil {
		return ""
	}
	return "other:" + err.Error()
}

func lockFree(t *testing.T, dir, id string) bool {
	t.Helper()
	o, err := execution.AcquireOwnership(dir, []string{id})
	if err != nil {
		return false
	}
	o.Close()
	return true
}

func TestClaudeSessionIDsSplitsTheACL(t *testing.T) {
	c, o := ClaudeSessionIDs([]string{"ses_a", "claudecode:" + uuid1, "ses_b", "claudecode:" + uuid2, "claudecodex"})
	if fmt.Sprint(c) != fmt.Sprint([]string{"claudecode:" + uuid1, "claudecode:" + uuid2}) || fmt.Sprint(o) != fmt.Sprint([]string{"ses_a", "ses_b", "claudecodex"}) {
		t.Fatalf("claude %v other %v", c, o)
	}
}

// Every invalid configuration fails closed and holds nothing: after the failure the writer lock
// of the session is free again (no partial enablement).
func TestOpenRefusesInvalidConfigurationAndHoldsNothing(t *testing.T) {
	for name, mod := range map[string]func(*env){
		"no versions":            func(e *env) { e.cfg.Versions = "" },
		"wildcard version":       func(e *env) { e.cfg.Versions = "2.1.*" },
		"missing allowlist file": func(e *env) { e.cfg.SessionsFile = filepath.Join(e.roots, "nope.json") },
		"allowlist mode 0644":    func(e *env) { _ = os.Chmod(e.cfg.SessionsFile, 0o644) },
		"session not in the ACL": func(e *env) { e.cfg.AllowedSessions = []string{opcode} },
		"bad wrapper":            func(e *env) { e.cfg.Command = "/nonexistent/hb-claude" },
		"relative wrapper":       func(e *env) { e.cfg.Command = "hb-claude" },
		"no lock dir":            func(e *env) { e.cfg.LockDir = "" },
		"history dir missing":    func(e *env) { e.cfg.HistoryDir = filepath.Join(e.roots, "missing") },
		"no model":               func(e *env) { e.cfg.Model = "" },
		"bad home":               func(e *env) { e.cfg.Home = "relative" },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			mod(e)
			if h, err := Open(e.cfg); err == nil {
				h.Close()
				t.Fatal("Open accepted it")
			}
			if !lockFree(t, e.lockDir, "claudecode:"+uuid1) {
				t.Fatal("a failed Open left the writer lock held")
			}
		})
	}
}

// #236 P3-4: ONE owner per session. The OpenCode writer takes the non-Claude IDs only; the
// adapter takes the Claude ones. Both fit in one directory and one process.
func TestOneWriterLockOwnerPerSession(t *testing.T) {
	e := newEnv(t)
	_, other := ClaudeSessionIDs(e.cfg.AllowedSessions)
	owner, err := execution.AcquireOwnership(e.lockDir, other)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	h := e.open(t)
	if lockFree(t, e.lockDir, "claudecode:"+uuid1) || lockFree(t, e.lockDir, opcode) {
		t.Fatal("a session lock is free while its owner runs")
	}
	h.Close()
	if !lockFree(t, e.lockDir, "claudecode:"+uuid1) {
		t.Fatal("Close did not release the adapter's lock")
	}
	if lockFree(t, e.lockDir, opcode) {
		t.Fatal("closing the adapter released the OpenCode lock")
	}
}

// The failure the filter prevents: taking the Claude IDs in the OpenCode loop too makes the
// adapter's own lock impossible (a second flock of one name in one process is refused).
func TestTakingTheClaudeIDsTwiceIsRefused(t *testing.T) {
	e := newEnv(t)
	owner, err := execution.AcquireOwnership(e.lockDir, e.cfg.AllowedSessions) // unfiltered: includes the Claude ID
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if h, err := Open(e.cfg); err == nil {
		h.Close()
		t.Fatal("Open succeeded although the OpenCode owner already holds the Claude session's lock")
	}
}

func TestExactIDResumeAndSessionBusy(t *testing.T) {
	e := newEnv(t)
	h := e.open(t)
	id := "claudecode:" + uuid1
	x, replay, err := h.Send(context.Background(), id, "hello", "key-wire-000001", "local")
	if err != nil || replay {
		t.Fatalf("send: %v %v", err, replay)
	}
	waitFor(t, func() bool { return e.sp.count() == 1 })
	spec := e.sp.specs[0]
	joined := strings.Join(spec.Argv, " ")
	if !strings.Contains(joined, "--resume "+uuid1) || spec.Dir != e.root(t) || strings.Contains(joined, "hello") {
		t.Fatalf("spec dir %q argv %v", spec.Dir, spec.Argv)
	}
	if _, _, err := h.Send(context.Background(), id, "second", "key-wire-000002", "local"); errCode(err) != "SESSION_BUSY" || e.sp.count() != 1 {
		t.Fatalf("second send while busy: %v (children %d)", err, e.sp.count())
	}
	if got := h.State(id); got != "running" {
		t.Fatalf("state %q", got)
	}
	// same key replays
	if y, replay, err := h.Send(context.Background(), id, "hello", "key-wire-000001", "local"); err != nil || !replay || y.ID != x.ID {
		t.Fatalf("replay: %v %v %v", y.ID, replay, err)
	}
	c := e.sp.last()
	c.emit(e.initLine(uuid1, e.root(t)))
	if h.Version() == "" {
		waitFor(t, func() bool { return h.Version() == "2.1.322" })
	}
	if !h.Owns(x.ID) || h.Owns("0123456789abcdef0123456789abcdef") {
		t.Fatal("Owns")
	}
	if _, err := h.Interrupt(x.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { y, _ := h.Get(x.ID); return y.State != "running" && y.State != "submitting" })
}

// An ID outside the allowlist is NOT_FOUND and starts no child.
func TestSessionOutsideTheAllowlistStartsNothing(t *testing.T) {
	e := newEnv(t)
	h := e.open(t)
	if _, _, err := h.Send(context.Background(), "claudecode:"+uuid2, "p", "key-wire-000003", "local"); errCode(err) != "NOT_FOUND" || e.sp.count() != 0 {
		t.Fatalf("%v (children %d)", err, e.sp.count())
	}
	if _, ok := h.Session("claudecode:" + uuid2); ok {
		t.Fatal("Session found an unlisted ID")
	}
}

// Concurrent send_prompt for one session: one child, the rest SESSION_BUSY, no deadlock between
// the guard and the flock.
func TestConcurrentSendsStartOneChild(t *testing.T) {
	e := newEnv(t)
	h := e.open(t)
	id := "claudecode:" + uuid1
	var wg sync.WaitGroup
	codes := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := h.Send(context.Background(), id, "p", fmt.Sprintf("key-wire-conc-%03d", i), "local")
			codes <- errCode(err)
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent sends deadlocked")
	}
	close(codes)
	ok, busy := 0, 0
	for c := range codes {
		switch c {
		case "":
			ok++
		case "SESSION_BUSY":
			busy++
		default:
			t.Errorf("unexpected %s", c)
		}
	}
	waitFor(t, func() bool { return e.sp.count() >= 1 })
	if ok != 1 || busy != 11 || e.sp.count() != 1 {
		t.Fatalf("ok %d busy %d children %d", ok, busy, e.sp.count())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out")
}
