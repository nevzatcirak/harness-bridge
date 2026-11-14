package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

const dashClaude = "claudecode:00000000-0000-4000-8000-000000000001"

// dashNative is a fake native OpenCode that records every request. Sessions live in
// root; ses_out is outside every allowed root (FORBIDDEN), ses_gone answers 404.
type dashNative struct {
	t        *testing.T
	root     string
	outside  string
	mu       sync.Mutex
	requests []string
	hold     chan struct{} // when non-nil, diff reads block until it closes or the request ends
	history  func(id string) string
}

func (n *dashNative) log() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.requests...)
}

func (n *dashNative) count(substr string) int {
	c := 0
	for _, r := range n.log() {
		if strings.Contains(r, substr) {
			c++
		}
	}
	return c
}

func (n *dashNative) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	n.requests = append(n.requests, r.Method+" "+r.URL.RequestURI())
	hold := n.hold
	n.mu.Unlock()
	if r.Method != http.MethodGet {
		n.t.Errorf("mutating native request: %s %s", r.Method, r.URL)
		w.WriteHeader(405)
		return
	}
	p := r.URL.Path
	switch {
	case p == "/session/status":
		_, _ = w.Write([]byte(`{"ses_b":{"type":"busy"}}`))
	case p == "/permission":
		_, _ = w.Write([]byte(`[]`))
	case p == "/vcs/diff":
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		if r.URL.Query().Get("mode") != "git" {
			n.t.Errorf("vcs mode %q", r.URL.Query().Get("mode"))
		}
		_, _ = w.Write([]byte(`[{"file":"a.go","patch":"@@ -1 +1 @@\n-x\n+y\n","additions":1,"deletions":1,"status":"modified"},{"additions":0,"deletions":0}]`))
	case strings.HasSuffix(p, "/diff"):
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write([]byte(`[{"file":"b.go","additions":2,"deletions":0}]`))
	case strings.HasSuffix(p, "/message"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/session/"), "/message")
		if n.history != nil {
			_, _ = w.Write([]byte(n.history(id)))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	case strings.HasPrefix(p, "/session/"):
		id := strings.TrimPrefix(p, "/session/")
		switch id {
		case "ses_a", "ses_b":
			_ = json.NewEncoder(w).Encode(opencode.Session{ID: id, Title: "title " + id, Directory: n.root, Time: json.RawMessage(`{"updated":1700000000000}`)})
		case "ses_out":
			_ = json.NewEncoder(w).Encode(opencode.Session{ID: id, Directory: n.outside})
		default:
			w.WriteHeader(404)
		}
	default:
		w.WriteHeader(404)
	}
}

func newDashServer(t *testing.T, acl []string, runs *execution.Manager) (*Server, *dashNative) {
	t.Helper()
	root := t.TempDir()
	n := &dashNative{t: t, root: root, outside: t.TempDir()}
	up := httptest.NewServer(n)
	t.Cleanup(up.Close)
	client, err := opencode.New(up.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := security.New([]string{root}, acl)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Client: client, Policy: policy, Runs: runs, ReadOnly: true}, n
}

func messages(parts ...string) string { return "[" + strings.Join(parts, ",") + "]" }

func userMsg(id string, summary string) string {
	s := ""
	if summary != "" {
		s = `,"summary":` + summary
	}
	return fmt.Sprintf(`{"info":{"id":%q,"role":"user","sessionID":"ses_a","time":{"created":1700000000000}%s},"parts":[]}`, id, s)
}

// The list equals list_sessions for the same ACL and native fake, including a FORBIDDEN
// entry, a NOT_FOUND entry and a claudecode: entry, which neither fails the list nor
// reaches the native client.
func TestDashboardSourceListEqualsListSessions(t *testing.T) {
	s, n := newDashServer(t, []string{"ses_a", "ses_b", "ses_out", "ses_gone", dashClaude}, nil)
	// The reference: list_sessions over the same ACL without the Claude entry (this
	// Server version's discovery does not serve a claudecode: ACL entry).
	ref := &Server{Client: s.Client, ReadOnly: true}
	p := *s.Policy
	p.Sessions = map[string]bool{"ses_a": true, "ses_b": true, "ses_out": true, "ses_gone": true}
	ref.Policy = &p
	want, err := ref.discover(context.Background())
	if err != nil || len(want) != 2 {
		t.Fatalf("reference: %v %v", want, err)
	}
	before := len(n.log())
	got, err := s.DashboardSource(nil).Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("rows: %+v", got)
	}
	for i, w := range want {
		if got[i].Harness != "opencode" || "opencode:"+got[i].ID != w.SessionID || got[i].State != w.State || got[i].Root != w.Directory || got[i].Title != w.Title {
			t.Errorf("row %d: dashboard %+v, list_sessions %+v", i, got[i], w)
		}
	}
	if got[0].LastActivity.IsZero() {
		t.Error("native time.updated was not carried")
	}
	if c := got[2]; c.Harness != "claudecode" || c.ID != "00000000-0000-4000-8000-000000000001" || c.State != "unknown" {
		t.Errorf("claude row: %+v", c)
	}
	if n.count("claudecode") != 0 || n.count("00000000-0000") != 0 {
		t.Fatalf("a claudecode: ID reached the native client: %v", n.log()[before:])
	}
	for _, r := range n.log() {
		if strings.Contains(r, "ses_out") && !strings.HasPrefix(r, "GET /session/ses_out") {
			t.Errorf("unexpected read of a non-ACL session: %s", r)
		}
	}
}

// A session that is not in the ACL is never listed and never read.
func TestDashboardSourceNeverListsOrReadsASessionOutsideTheACL(t *testing.T) {
	s, n := newDashServer(t, []string{"ses_a"}, nil)
	src := s.DashboardSource(nil)
	rows, err := src.Sessions(context.Background())
	if err != nil || len(rows) != 1 || rows[0].ID != "ses_a" {
		t.Fatalf("%+v %v", rows, err)
	}
	before := len(n.log())
	ctx := context.Background()
	if _, err := src.SessionDetail(ctx, "opencode", "ses_b"); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("detail: %v", err)
	}
	if _, err := src.SessionTurns(ctx, "opencode", "ses_b"); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("turns: %v", err)
	}
	if _, err := src.TurnDiff(ctx, "opencode", "ses_b", "msg_a"); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("turn diff: %v", err)
	}
	if _, err := src.WorkingTreeDiff(ctx, "opencode", "ses_b"); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("working tree: %v", err)
	}
	if _, err := src.SessionDetail(ctx, "opencode", "../x"); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("invalid id: %v", err)
	}
	if got := n.log()[before:]; len(got) != 0 {
		t.Fatalf("an unlisted session made native requests: %v", got)
	}
}

// A listed session that native refuses (FORBIDDEN by workspace, NOT_FOUND) is ErrNotFound.
func TestDashboardSourceMapsDeniedAndMissingToNotFound(t *testing.T) {
	s, _ := newDashServer(t, []string{"ses_a", "ses_out", "ses_gone"}, nil)
	src := s.DashboardSource(nil)
	for _, id := range []string{"ses_out", "ses_gone"} {
		if _, err := src.SessionDetail(context.Background(), "opencode", id); !errors.Is(err, dashboard.ErrNotFound) {
			t.Errorf("%s: %v", id, err)
		}
	}
}

// Claude Code sessions: the documented pending state, no native call, no turns, no diffs.
func TestDashboardSourceClaudeIsPendingAndNeverNative(t *testing.T) {
	s, n := newDashServer(t, []string{"ses_a", dashClaude}, nil)
	src := s.DashboardSource(nil)
	ctx := context.Background()
	uuid := "00000000-0000-4000-8000-000000000001"
	d, err := src.SessionDetail(ctx, "claudecode", uuid)
	if err != nil || d.Harness != "claudecode" || d.ID != uuid {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := src.SessionDetail(ctx, "claudecode", "00000000-0000-4000-8000-000000000002"); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("unlisted claude session: %v", err)
	}
	if _, err := src.SessionTurns(ctx, "claudecode", uuid); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("turns: %v", err)
	}
	if _, err := src.TurnDiff(ctx, "claudecode", uuid, "msg_a"); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("turn diff: %v", err)
	}
	if _, err := src.WorkingTreeDiff(ctx, "claudecode", uuid); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("working tree: %v", err)
	}
	if got := n.log(); len(got) != 0 {
		t.Fatalf("a claudecode: session made native requests: %v", got)
	}
}

// Detail, turns and diffs map the recorded fixture shapes; absent summary is never
// "computed, no changes".
func TestDashboardSourceMapsDetailTurnsAndDiffs(t *testing.T) {
	s, n := newDashServer(t, []string{"ses_a"}, nil)
	n.history = func(string) string {
		return messages(
			userMsg("msg_u1", ""),
			`{"info":{"id":"msg_asst","role":"assistant","time":{"created":1}},"parts":[]}`,
			userMsg("msg_u2", `{}`),
			userMsg("msg_u3", `{"diffs":null}`),
			userMsg("msg_u4", `{"diffs":[]}`),
			userMsg("msg_u5", `{"diffs":[{"file":"a"}]}`),
		)
	}
	src := s.DashboardSource(nil)
	ctx := context.Background()
	d, err := src.SessionDetail(ctx, "opencode", "ses_a")
	if err != nil || d.ID != "ses_a" || d.Title != "title ses_a" || d.Root != n.root || d.State == "" {
		t.Fatalf("%+v %v", d, err)
	}
	turns, err := src.SessionTurns(ctx, "opencode", "ses_a")
	if err != nil {
		t.Fatal(err)
	}
	want := []dashboard.TurnState{dashboard.TurnNotComputed, dashboard.TurnNotComputed, dashboard.TurnNotComputed, dashboard.TurnComputedEmpty, dashboard.TurnChanges}
	if len(turns) != len(want) {
		t.Fatalf("turns: %+v", turns)
	}
	for i, w := range want {
		if turns[i].State != w || turns[i].Created.IsZero() {
			t.Errorf("turn %d: %+v want %s", i, turns[i], w)
		}
	}
	if turns[4].MessageID != "msg_u5" {
		t.Errorf("order: %+v", turns)
	}
	diff, err := src.TurnDiff(ctx, "opencode", "ses_a", "msg_u5")
	if err != nil || len(diff.Files) != 1 || diff.Files[0].Path != "b.go" || !diff.Files[0].HasPath || diff.Files[0].HasPatch || diff.Files[0].Additions != 2 {
		t.Fatalf("turn diff: %+v %v", diff, err)
	}
	if n.count("GET /session/ses_a/diff?") != 1 || n.count("messageID=msg_u5") != 1 {
		t.Errorf("per-turn route: %v", n.log())
	}
	wt, err := src.WorkingTreeDiff(ctx, "opencode", "ses_a")
	if err != nil || len(wt.Files) != 2 || !wt.Files[0].HasPatch || wt.Files[0].Path != "a.go" || wt.Files[1].HasPath || wt.Files[1].HasPatch {
		t.Fatalf("working tree: %+v %v", wt, err)
	}
}

// Caps and timeouts: only the newest maxDashTurns turns are returned, and a native diff
// read that never answers ends with the caller's deadline.
func TestDashboardSourceCapsTurnsAndHonoursDeadlines(t *testing.T) {
	s, n := newDashServer(t, []string{"ses_a"}, nil)
	n.history = func(string) string {
		var b []string
		for i := 0; i < maxDashTurns+25; i++ {
			b = append(b, userMsg(fmt.Sprintf("msg_%05d", i), ""))
		}
		return messages(b...)
	}
	src := s.DashboardSource(nil)
	turns, err := src.SessionTurns(context.Background(), "opencode", "ses_a")
	if err != nil || len(turns) != maxDashTurns || turns[0].MessageID != "msg_00025" || turns[len(turns)-1].MessageID != fmt.Sprintf("msg_%05d", maxDashTurns+24) {
		t.Fatalf("turns: %d %v", len(turns), err)
	}
	n.mu.Lock()
	n.hold = make(chan struct{})
	n.mu.Unlock()
	defer close(n.hold)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := src.WorkingTreeDiff(ctx, "opencode", "ses_a"); err == nil || errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("a hung native read must be an error, not not-found: %v", err)
	}
	if _, err := src.TurnDiff(ctx, "opencode", "ses_a", "msg_a"); err == nil {
		t.Fatal("hung per-turn read returned no error")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("deadline not honoured: %v", time.Since(start))
	}
}

// countingRunner counts every mutating call of the execution manager's native runner.
type countingRunner struct {
	prompts, aborts atomic.Int32
}

func (c *countingRunner) Get(_ context.Context, id string) (opencode.Session, error) {
	return opencode.Session{ID: id, Directory: "/"}, nil
}
func (c *countingRunner) Permissions(context.Context) ([]opencode.Permission, error) { return nil, nil }
func (c *countingRunner) History(context.Context, string) ([]opencode.Message, error) {
	return nil, nil
}
func (c *countingRunner) Status(context.Context) (map[string]opencode.Status, error) {
	return map[string]opencode.Status{}, nil
}
func (c *countingRunner) Prompt(context.Context, string, string, string) error {
	c.prompts.Add(1)
	return nil
}
func (c *countingRunner) Abort(context.Context, string) error { c.aborts.Add(1); return nil }

// No mutation: all dashboard Source traffic is GET-only against native (the fake fails the
// test on any other method), makes no manager call that changes state, and writes no
// audit line (prompt_submission, interrupt, guard_release).
func TestDashboardSourceTrafficMutatesNothing(t *testing.T) {
	r := &countingRunner{}
	m := execution.NewWithTiming(r, time.Second, 2*time.Millisecond)
	t.Cleanup(m.Close)
	s, n := newDashServer(t, []string{"ses_a", "ses_b", dashClaude}, m)
	x, err := m.Send(context.Background(), "ses_b", "prompt", "key-dash-00001")
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); r.prompts.Load() < 1 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond) // the submission is asynchronous: take the baseline after it
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	prompts, aborts := r.prompts.Load(), r.aborts.Load()
	before := m.DashboardSnapshot()
	src := s.DashboardSource(nil)
	ctx := context.Background()
	rows, err := src.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		_, _ = src.SessionDetail(ctx, row.Harness, row.ID)
		_, _ = src.SessionTurns(ctx, row.Harness, row.ID)
		_, _ = src.TurnDiff(ctx, row.Harness, row.ID, "msg_a")
		_, _ = src.WorkingTreeDiff(ctx, row.Harness, row.ID)
	}
	if r.prompts.Load() != prompts || r.aborts.Load() != aborts {
		t.Fatalf("dashboard traffic made a mutating manager call: prompts %d->%d aborts %d->%d", prompts, r.prompts.Load(), aborts, r.aborts.Load())
	}
	after := m.DashboardSnapshot()
	if len(after.Executions) != len(before.Executions) || after.Executions[0].ID != x.ID || len(after.Guards) != len(before.Guards) {
		t.Fatalf("manager changed: %+v -> %+v", before, after)
	}
	for _, bad := range []string{"prompt_submission", "interrupt", "guard_release"} {
		if strings.Contains(buf.String(), bad) {
			t.Errorf("dashboard traffic wrote an audit line containing %q: %s", bad, buf.String())
		}
	}
	for _, req := range n.log() {
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("non-GET native request: %s", req)
		}
	}
}

// The row of the session whose execution holds the guard shows the guard and the current
// execution; a finished execution is not current.
func TestDashboardSourceShowsGuardAndCurrentExecution(t *testing.T) {
	r := &countingRunner{}
	m := execution.NewWithTiming(r, time.Second, 2*time.Millisecond)
	t.Cleanup(m.Close)
	s, _ := newDashServer(t, []string{"ses_a", "ses_b"}, m)
	x, err := m.Send(context.Background(), "ses_b", "prompt", "key-dash-00002")
	if err != nil {
		t.Fatal(err)
	}
	src := s.DashboardSource(nil)
	d, err := src.SessionDetail(context.Background(), "opencode", "ses_b")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Guard.Held || d.CurrentExecution != x.ID || len(d.Executions) != 1 || d.Executions[0].ID != x.ID || !d.Executions[0].Current {
		t.Fatalf("%+v", d)
	}
	a, err := src.SessionDetail(context.Background(), "opencode", "ses_a")
	if err != nil || a.Guard.Held || a.CurrentExecution != "" || len(a.Executions) != 0 {
		t.Fatalf("%+v %v", a, err)
	}
}

// Race and lock order: the source reads while sends are admitted and finalized. A lock
// inversion would miss the 5 s deadline (the race detector does not find those).
func TestDashboardSourceUnderConcurrentSends(t *testing.T) {
	r := &countingRunner{}
	m := execution.NewWithTiming(r, time.Second, time.Millisecond)
	t.Cleanup(m.Close)
	s, _ := newDashServer(t, []string{"ses_a", "ses_b", "ses_c"}, m)
	src := s.DashboardSource(nil)
	done := make(chan struct{})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = src.Sessions(context.Background())
				_, _ = src.SessionDetail(context.Background(), "opencode", "ses_b")
			}
		}()
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = m.Send(context.Background(), []string{"ses_a", "ses_b", "ses_c"}[i], "p", fmt.Sprintf("key-dash-conc-%d-%03d", i, j))
				time.Sleep(time.Millisecond)
			}
		}(i)
	}
	go func() {
		time.Sleep(600 * time.Millisecond)
		close(stop)
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the dashboard source and the manager deadlocked")
	}
}

// The source does not use the filesystem of the session roots beyond the ACL check that
// list_sessions already performs: it runs no git (ADR-023 section 1.3).
func TestDashboardSourceRunsNoGit(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("dashsource.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"os/exec", "exec.Command", "\"git\""} {
		if strings.Contains(string(src), bad) {
			t.Fatalf("dashsource.go contains %q", bad)
		}
	}
}
