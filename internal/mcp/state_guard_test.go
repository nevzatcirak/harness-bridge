package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// guardRunner is the execution manager's native view. It is deliberately separate
// from the upstream that session discovery reads, so each test can hold a Bridge
// guard in a chosen execution state while discovery observes any native state.
type guardRunner struct {
	mu        sync.Mutex
	directory string
	mode      string // reserved, submitting, running, waiting_for_permission, unknown, idle
	prompted  bool
	messages  []opencode.Message
	once      sync.Once
	entered   chan struct{}
	release   chan struct{}
}

func (g *guardRunner) gate(ctx context.Context) error {
	first := false
	g.once.Do(func() { first = true })
	if !first {
		return nil
	}
	close(g.entered)
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g *guardRunner) Get(ctx context.Context, id string) (opencode.Session, error) {
	if g.mode == "reserved" {
		if e := g.gate(ctx); e != nil {
			return opencode.Session{}, e
		}
	}
	return opencode.Session{ID: id, Directory: g.directory}, nil
}
func (g *guardRunner) Status(context.Context) (map[string]opencode.Status, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.prompted {
		return map[string]opencode.Status{}, nil
	}
	if g.mode == "idle" {
		return map[string]opencode.Status{"ses_a": {Type: "idle"}}, nil
	}
	return map[string]opencode.Status{"ses_a": {Type: "busy"}}, nil
}
func (g *guardRunner) Permissions(context.Context) ([]opencode.Permission, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.prompted && g.mode == "waiting_for_permission" {
		return []opencode.Permission{{SessionID: "ses_a"}}, nil
	}
	return nil, nil
}
func (g *guardRunner) History(context.Context, string) ([]opencode.Message, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]opencode.Message(nil), g.messages...), nil
}
func (g *guardRunner) Prompt(ctx context.Context, session, _, mid string) error {
	if g.mode == "submitting" {
		if e := g.gate(ctx); e != nil {
			return e
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.mode == "unknown" {
		return errors.New("lost reply")
	}
	user := opencode.Message{}
	user.Info.ID, user.Info.Role, user.Info.SessionID = mid, "user", session
	g.messages = append(g.messages, user)
	g.prompted = true
	return nil
}
func (g *guardRunner) Abort(context.Context, string) error { return nil }
func (g *guardRunner) complete(mid string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	msg := opencode.Message{}
	msg.Info.ID, msg.Info.Role, msg.Info.SessionID, msg.Info.ParentID, msg.Info.Finish = "msg_done"+mid, "assistant", "ses_a", mid, "stop"
	msg.Info.Time.Completed = 1
	g.messages = append(g.messages, msg)
}

// heldManager returns a Manager whose guard for ses_a is held in the requested
// execution state ("" means no execution and therefore no guard).
func heldManager(t *testing.T, root, mode string) *execution.Manager {
	t.Helper()
	g := &guardRunner{directory: root, mode: mode, entered: make(chan struct{}), release: make(chan struct{})}
	m := execution.NewWithTiming(g, 30*time.Second, 2*time.Millisecond)
	t.Cleanup(func() {
		select {
		case <-g.release:
		default:
			close(g.release)
		}
		m.Close()
	})
	if mode == "" {
		return m
	}
	ctx := context.Background()
	if mode == "reserved" {
		go m.Send(ctx, "ses_a", "prompt", "guard-key-"+mode)
		<-g.entered
		return m
	}
	x, e := m.Send(ctx, "ses_a", "prompt", "guard-key-"+mode)
	if e != nil {
		t.Fatal(e)
	}
	if mode == "submitting" {
		<-g.entered
	}
	want := mode
	deadline := time.Now().Add(3 * time.Second)
	for {
		v, e := m.Snapshot(ctx, x.ID)
		if e == nil && v.State == want {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution state: want %s got %+v %v", want, v, e)
		}
		time.Sleep(time.Millisecond)
	}
}

// discoveryUpstream serves the native evidence that session discovery reads.
func discoveryUpstream(t *testing.T, root, status, permissions string) *opencode.Client {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			if status == "error" {
				w.WriteHeader(500)
				return
			}
			w.Write([]byte(status))
		case "/permission":
			if permissions == "error" {
				w.WriteHeader(500)
				return
			}
			w.Write([]byte(permissions))
		default:
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: root})
		}
	}))
	t.Cleanup(up.Close)
	c, e := opencode.New(up.URL, "", "")
	if e != nil {
		t.Fatal(e)
	}
	return c
}

// sessionStates returns the state reported by get_session, list_sessions and find_sessions.
func sessionStates(t *testing.T, s *Server) []string {
	t.Helper()
	ctx := context.Background()
	got, e := s.call(ctx, "get_session", args{SessionID: "opencode:ses_a"})
	if e != nil {
		t.Fatal(e)
	}
	listed, e := s.call(ctx, "list_sessions", args{})
	if e != nil || len(listed.(Sessions).Sessions) != 1 {
		t.Fatal(listed, e)
	}
	found, e := s.call(ctx, "find_sessions", args{})
	if e != nil || len(found.(FoundSessions).Sessions) != 1 {
		t.Fatal(found, e)
	}
	return []string{got.(Session).State, listed.(Sessions).Sessions[0].State, found.(FoundSessions).Sessions[0].State}
}

func guardServer(t *testing.T, root string, runs *execution.Manager, status, permissions string) *Server {
	t.Helper()
	p, e := security.New([]string{root}, []string{"ses_a"})
	if e != nil {
		t.Fatal(e)
	}
	return &Server{Client: discoveryUpstream(t, root, status, permissions), Runs: runs, Policy: p}
}

func wantStates(t *testing.T, s *Server, want string) {
	t.Helper()
	for i, got := range sessionStates(t, s) {
		if got != want {
			t.Fatalf("%s state = %q, want %q", []string{"get_session", "list_sessions", "find_sessions"}[i], got, want)
		}
	}
}

// R1: a running Bridge execution must not be reported idle because native status is idle.
func TestStateGuardHeldNativeIdle(t *testing.T) {
	root := t.TempDir()
	wantStates(t, guardServer(t, root, heldManager(t, root, "running"), `{"ses_a":{"type":"idle"}}`, `[]`), "running")
}

// R2: the R3a case. An unconfirmed submission keeps the guard; never idle.
func TestStateGuardHeldUnknown(t *testing.T) {
	root := t.TempDir()
	wantStates(t, guardServer(t, root, heldManager(t, root, "unknown"), `{"ses_a":{"type":"idle"}}`, `[]`), "unknown")
}

// G1: every held cell (native status ignored) and every no-guard cell (ADR-012).
func TestStateGuardTable(t *testing.T) {
	natives := map[string]string{"idle": `{"ses_a":{"type":"idle"}}`, "busy": `{"ses_a":{"type":"busy"}}`, "retry": `{"ses_a":{"type":"retry"}}`, "absent": `{}`}
	permissions := map[string]string{"pending": `[{"sessionID":"ses_a"}]`, "none": `[]`, "failed": "error"}
	held := map[string]map[string]string{
		"reserved":               {"pending": "waiting_for_permission", "none": "running", "failed": "unknown"},
		"submitting":             {"pending": "waiting_for_permission", "none": "running", "failed": "unknown"},
		"running":                {"pending": "waiting_for_permission", "none": "running", "failed": "unknown"},
		"waiting_for_permission": {"pending": "waiting_for_permission", "none": "waiting_for_permission", "failed": "unknown"},
		"unknown":                {"pending": "waiting_for_permission", "none": "unknown", "failed": "unknown"},
	}
	unguarded := map[string]map[string]string{
		"idle":   {"pending": "waiting_for_permission", "none": "idle", "failed": "unknown"},
		"busy":   {"pending": "waiting_for_permission", "none": "running", "failed": "unknown"},
		"retry":  {"pending": "waiting_for_permission", "none": "running", "failed": "unknown"},
		"absent": {"pending": "waiting_for_permission", "none": "idle", "failed": "unknown"}, // F-4
	}
	for mode, row := range held {
		t.Run("held_"+mode, func(t *testing.T) {
			root := t.TempDir()
			runs := heldManager(t, root, mode)
			for native, status := range natives {
				for permission, want := range row {
					t.Run(native+"_"+permission, func(t *testing.T) {
						wantStates(t, guardServer(t, root, runs, status, permissions[permission]), want)
					})
				}
			}
		})
	}
	// G3: no Manager, and a Manager without any execution, keep ADR-012 exactly.
	for _, withManager := range []bool{false, true} {
		name := "unguarded_nil_runs"
		if withManager {
			name = "unguarded_idle_manager"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			var runs *execution.Manager
			if withManager {
				runs = heldManager(t, root, "")
			}
			for native, row := range unguarded {
				for permission, want := range row {
					t.Run(native+"_"+permission, func(t *testing.T) {
						wantStates(t, guardServer(t, root, runs, natives[native], permissions[permission]), want)
					})
				}
			}
		})
	}
}
