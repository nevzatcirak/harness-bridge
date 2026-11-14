package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

const wrapperPath = "/usr/local/libexec/hb-claude"

func uuidN(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// rootFS answers lstat for the wrapper (a root-owned regular file) and every one of
// its ancestors (root-owned, 0755), so ValidateWrapper passes without a real
// root-owned file.
type rootFS struct {
	mu      sync.Mutex
	wrapper string
	broken  bool
}

func (f *rootFS) Lstat(p string) (claudecode.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.broken {
		return claudecode.FileInfo{}, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
	}
	if p == f.wrapper {
		return claudecode.FileInfo{Mode: 0o755, UID: 0, Nlink: 1}, nil
	}
	if strings.HasPrefix(f.wrapper, strings.TrimSuffix(p, "/")+"/") || p == "/" {
		return claudecode.FileInfo{Mode: fs.ModeDir | 0o755, UID: 0, Nlink: 2}, nil
	}
	return claudecode.FileInfo{}, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
}

func (f *rootFS) breakIt() { f.mu.Lock(); f.broken = true; f.mu.Unlock() }

// fakeChild is a controllable process: the test writes stdout lines, decides the
// exit, and sees every signal.
type fakeChild struct {
	t        *testing.T
	stdoutR  *io.PipeReader
	stdoutW  *io.PipeWriter
	mu       sync.Mutex
	stdin    bytes.Buffer
	writeN   int // when writeErr != nil: bytes the pipe accepted
	writeErr error
	signals  chan Signal
	exitCh   chan Exit
	once     sync.Once
	// onSignal decides what a signal does; nil means SIGINT ends the child with exit 130.
	onSignal func(c *fakeChild, s Signal)
	ignoreIn bool // SIGINT is ignored
	// ignoreTerm: SIGTERM is ignored too. Without it SIGTERM ends the child like the real
	// CLI (exit 143, no result). The runner has no SIGKILL, so a child that ignores both
	// lives until the test (the operator) ends it.
	ignoreTerm bool
	log        []sigRec
	groupBusy  bool   // a member of the process group is still alive
	groupErr   error  // the group check fails
	sessionID  string // set by a test whose reaction writes a result line
}

func (c *fakeChild) sessionFor() string { return c.sessionID }

// sigRec is one signal the child received, with the time it arrived.
type sigRec struct {
	sig Signal
	at  time.Time
}

func (c *fakeChild) received() []sigRec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sigRec(nil), c.log...)
}

func newFakeChild(t *testing.T) *fakeChild {
	r, w := io.Pipe()
	return &fakeChild{t: t, stdoutR: r, stdoutW: w, signals: make(chan Signal, 16), exitCh: make(chan Exit, 1)}
}

func (c *fakeChild) Stdout() io.Reader { return c.stdoutR }
func (c *fakeChild) WriteStdin(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeErr != nil {
		return c.writeN, c.writeErr
	}
	c.stdin.Write(p)
	return len(p), nil
}
func (c *fakeChild) CloseStdin() error { return nil }

// GroupEmpty answers from the test: by default the group is empty once the child ended.
func (c *fakeChild) GroupEmpty() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.groupErr != nil {
		return false, c.groupErr
	}
	return !c.groupBusy, nil
}
func (c *fakeChild) Wait() Exit { return <-c.exitCh }
func (c *fakeChild) Signal(s Signal) error {
	c.mu.Lock()
	c.log = append(c.log, sigRec{s, time.Now()})
	c.mu.Unlock()
	c.signals <- s
	switch {
	case c.onSignal != nil:
		c.onSignal(c, s)
	case s == SigInt && !c.ignoreIn:
		c.finish(Exit{Code: 130, Signal: "interrupt"})
	case s == SigTerm && !c.ignoreTerm:
		c.finish(Exit{Code: 143, Signal: "terminated"})
	}
	return nil
}

// emit writes one stdout line (blocks until the runner consumed it).
func (c *fakeChild) emit(line string) { _, _ = io.WriteString(c.stdoutW, line+"\n") }
func (c *fakeChild) emitRaw(s string) { _, _ = io.WriteString(c.stdoutW, s) }

// finish closes stdout and records the exit; it is idempotent.
func (c *fakeChild) finish(x Exit) {
	c.once.Do(func() {
		_ = c.stdoutW.Close()
		c.exitCh <- x
	})
}

func (c *fakeChild) gotSignal(want Signal) bool {
	deadline := time.After(3 * time.Second)
	for {
		select {
		case s := <-c.signals:
			if s == want {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func (c *fakeChild) stdinString() string { c.mu.Lock(); defer c.mu.Unlock(); return c.stdin.String() }

type fakeSpawner struct {
	mu      sync.Mutex
	specs   []Spec
	failure error
	byDir   map[string]*fakeChild
	tweak   func(*fakeChild)
}

func newFakeSpawner() *fakeSpawner { return &fakeSpawner{byDir: map[string]*fakeChild{}} }

func (s *fakeSpawner) Start(spec Spec) (Child, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	fail := s.failure
	s.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	c := newFakeChild(nil)
	if s.tweak != nil {
		s.tweak(c)
	}
	s.mu.Lock()
	s.byDir[spec.Dir] = c
	s.mu.Unlock()
	return c, nil
}

func (s *fakeSpawner) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.specs) }

// child waits for the child started in dir (the session root): children of
// different sessions start in no fixed order.
func (s *fakeSpawner) child(t *testing.T, dir string) *fakeChild {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		c := s.byDir[dir]
		s.mu.Unlock()
		if c != nil {
			c.t = t
			return c
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no child was started in %s", dir)
	return nil
}

// harness wires an Executor over a temp history store, real writer locks, and fakes.
type harness struct {
	t        *testing.T
	ex       *Executor
	sp       *fakeSpawner
	fsys     *rootFS
	hist     *claudecode.Store
	histDir  string
	lockDir  string
	locks    *Locks
	sessions []claudecode.Session
	cfg      Config
}

func private(t *testing.T, name string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func newHarness(t *testing.T, mod func(*Config, *claudecode.StoreConfig)) *harness {
	t.Helper()
	h := &harness{t: t, sp: newFakeSpawner(), fsys: &rootFS{wrapper: wrapperPath}}
	h.histDir, h.lockDir = private(t, "hist"), private(t, "locks")
	for i := 1; i <= 3; i++ {
		root := filepath.Join(t.TempDir(), fmt.Sprintf("proj%d", i))
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		h.sessions = append(h.sessions, claudecode.Session{ID: "claudecode:" + uuidN(i), Root: root, Label: fmt.Sprintf("p%d", i)})
	}
	sc := claudecode.DefaultStoreConfig()
	h.cfg = Config{Wrapper: wrapperPath, FS: h.fsys, Env: claudecode.EnvConfig{Home: "/home/claude", Lang: "C.UTF-8"}, Model: "claude-sonnet-5-5", MaxTurns: 20,
		AllowedVersions: []string{"2.1.322"}, Tools: []string{"Read", "Glob", "Grep"}, Sessions: h.sessions, AllowedSessions: sessionIDs(h.sessions), Spawner: h.sp,
		RunMax: 10 * time.Second, Grace: 10 * time.Second, CloseWait: 10 * time.Second, MaxChildren: 4}
	if mod != nil {
		mod(&h.cfg, &sc)
	}
	var err error
	if h.hist, err = claudecode.OpenStore(h.histDir, sc); err != nil {
		t.Fatal(err)
	}
	if h.locks, err = AcquireLocks(h.lockDir, h.cfg.Sessions); err != nil {
		t.Fatal(err)
	}
	h.cfg.History, h.cfg.Locks = h.hist, h.locks
	if h.ex, err = New(h.cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Whatever a failing test left running is ended first, so a failure reads as a
		// failure and not as a hung Close.
		h.sp.mu.Lock()
		for _, c := range h.sp.byDir {
			c.finish(Exit{Code: 1})
		}
		h.sp.mu.Unlock()
		h.ex.Close()
		h.locks.Close()
		h.hist.Close()
	})
	return h
}

func (h *harness) send(n int, prompt, key string) (execution.Execution, bool, error) {
	return h.ex.Send(context.Background(), Request{Session: h.sessions[n-1].ID, Prompt: prompt, Key: key, Principal: "tester"})
}

func (h *harness) mustSend(n int, prompt, key string) execution.Execution {
	h.t.Helper()
	x, _, err := h.send(n, prompt, key)
	if err != nil {
		h.t.Fatalf("send: %v", err)
	}
	return x
}

func (h *harness) wait(id string) execution.Execution {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	x, err := h.ex.Wait(ctx, id)
	if err != nil {
		h.t.Fatalf("wait: %v (state %s)", err, x.State)
	}
	return x
}

func (h *harness) verdict(id string) claudecode.Verdict {
	h.t.Helper()
	v, err := h.ex.Verdict(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}

// next is the child of session n.
func (h *harness) next(n int) *fakeChild { h.t.Helper(); return h.sp.child(h.t, h.root(n)) }

func (h *harness) uuid(n int) string { return uuidN(n) }
func (h *harness) root(n int) string { return h.sessions[n-1].Root }

func errCode(err error) string {
	if e, ok := err.(*execution.Error); ok {
		return e.Code
	}
	if err == nil {
		return ""
	}
	return "other:" + err.Error()
}

// NDJSON lines of a compliant stream.
func js(m map[string]any) string { b, _ := json.Marshal(m); return string(b) }

func (h *harness) initLine(n int) string {
	return js(map[string]any{"type": "system", "subtype": "init", "cwd": h.root(n), "session_id": uuidN(n), "model": "m", "permissionMode": "default",
		"apiKeySource": "none", "claude_code_version": "2.1.322", "mcp_servers": []any{}, "plugins": []any{}, "tools": []any{"Read", "Glob", "Grep"}})
}

func (h *harness) resultLine(n int, mods map[string]any) string {
	m := map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": 1, "session_id": uuidN(n), "terminal_reason": "completed",
		"total_cost_usd": 0.01, "permission_denials": []any{}, "result": "ok"}
	for k, v := range mods {
		m[k] = v
	}
	return js(m)
}

func (h *harness) assistantLine(n int, text string) string {
	return js(map[string]any{"type": "assistant", "session_id": uuidN(n), "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
}

// historyKinds lists the record kinds of a session in order.
func (h *harness) historyKinds(n int) []string {
	h.t.Helper()
	res, err := h.hist.Read(uuidN(n))
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, r := range res.Records {
		out = append(out, r.Kind)
	}
	return out
}

var _ = path.Join

func ctxBG() context.Context { return context.Background() }

func sessionIDs(list []claudecode.Session) []string {
	var out []string
	for _, s := range list {
		out = append(out, s.ID)
	}
	return out
}

var errClosed = io.ErrClosedPipe

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

var io_ErrClosedPipe = io.ErrClosedPipe
