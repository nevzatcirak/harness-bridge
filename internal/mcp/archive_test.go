package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/lifecycle"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/overlay"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// ADR-022 S6 / ADR-025 (D18) against a FAKE native. Nothing here is live: no Bridge, no OpenCode.

type archSession struct {
	dir, parent string
	busy        bool
	unfinished  bool
	msgs        []opencode.Message
}

type archNative struct {
	t    *testing.T
	mu   sync.Mutex
	reqs []nativeReq
	sess map[string]*archSession
	perm []string          // session IDs with a pending permission
	fail map[string]bool   // path -> answer 500
	hook func(path string) // called with the lock released, on every request
}

func (n *archNative) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	n.reqs = append(n.reqs, nativeReq{method: r.Method, path: r.URL.Path, directory: r.URL.Query().Get("directory")})
	hook, failed := n.hook, n.fail[r.URL.Path]
	n.mu.Unlock()
	if hook != nil {
		hook(r.URL.Path)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if failed {
		w.WriteHeader(500)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/session/status":
		out := map[string]opencode.Status{}
		for id, s := range n.sess {
			if s.busy {
				out[id] = opencode.Status{Type: "busy"}
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodGet && r.URL.Path == "/permission":
		out := []opencode.Permission{}
		for _, id := range n.perm {
			out = append(out, opencode.Permission{SessionID: id})
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodGet && r.URL.Path == "/session":
		out := []opencode.Session{}
		for id, s := range n.sess {
			out = append(out, opencode.Session{ID: id, Directory: s.dir, ParentID: s.parent})
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/message"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/session/"), "/message")
		s := n.sess[id]
		msgs := []opencode.Message{}
		if s != nil {
			msgs = append(msgs, s.msgs...)
			if s.unfinished {
				a := opencode.Message{Parts: []json.RawMessage{}}
				a.Info.ID, a.Info.Role, a.Info.SessionID = "msg_unfinished", "assistant", id
				msgs = append(msgs, a)
			}
		}
		_ = json.NewEncoder(w).Encode(msgs)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/session/"):
		id := strings.TrimPrefix(r.URL.Path, "/session/")
		s, ok := n.sess[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(opencode.Session{ID: id, Directory: s.dir, ParentID: s.parent})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompt_async"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/session/"), "/prompt_async")
		var in struct {
			MessageID string `json:"messageID"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		s := n.sess[id]
		user := opencode.Message{Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"p"}`)}}
		user.Info.ID, user.Info.SessionID, user.Info.Role = in.MessageID, id, "user"
		asst := opencode.Message{Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"r"}`)}}
		asst.Info.ID, asst.Info.ParentID, asst.Info.SessionID, asst.Info.Role = "msg_a"+in.MessageID, in.MessageID, id, "assistant"
		asst.Info.Path.Cwd = s.dir
		s.msgs = append(s.msgs, user, asst)
		s.busy = true
		w.WriteHeader(204)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/abort"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/session/"), "/abort")
		s := n.sess[id]
		if a := &s.msgs[len(s.msgs)-1]; a.Info.Role == "assistant" {
			a.Info.Time.Completed = 1
			a.Info.Error = json.RawMessage(`{"name":"MessageAbortedError"}`)
		}
		s.busy = false
		_, _ = w.Write([]byte(`true`))
	default:
		w.WriteHeader(404)
	}
}

func (n *archNative) writes() []nativeReq {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []nativeReq
	for _, r := range n.reqs {
		if r.method != http.MethodGet {
			out = append(out, r)
		}
	}
	return out
}

func (n *archNative) set(id string, f func(*archSession)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	f(n.sess[id])
}

type archEnv struct {
	t     *testing.T
	s     *Server
	nat   *archNative
	store *overlay.Store
	ac    *ArchiveConfig
	root  string
	clock time.Time
	logs  *syncBuffer
	dir   string
}

func newArchEnv(t *testing.T, mod func(*lifecycle.Config)) *archEnv {
	return newArchEnvTiming(t, mod, 5*time.Millisecond)
}

// newArchEnvTiming is newArchEnv with an injected manager reconcile interval. A long interval pins
// the monitor out of the test window: a run whose native state turns terminal is then NOT settled
// and its guard is NOT released until the interval elapses, so a test that needs the guard held at
// close time gets it deterministically (explicit timing injection, no sleeps, no changed timeouts).
func newArchEnvTiming(t *testing.T, mod func(*lifecycle.Config), reconcile time.Duration) *archEnv {
	t.Helper()
	canon := func(d string) string {
		r, err := filepath.EvalSymlinks(d)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	root, noArchive := canon(t.TempDir()), canon(t.TempDir())
	gitAt(t, root)
	gitAt(t, noArchive)
	nat := &archNative{t: t, fail: map[string]bool{}, sess: map[string]*archSession{
		"ses_arch1": {dir: root}, "ses_arch2": {dir: root}, "ses_arch3": {dir: root},
		"ses_child": {dir: root, parent: "ses_arch1"}, "ses_grand": {dir: root, parent: "ses_child"},
		"ses_nomode": {dir: noArchive},
	}}
	upstream := httptest.NewServer(nat)
	t.Cleanup(upstream.Close)
	client, err := opencode.New(upstream.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &lifecycle.Config{Roots: []lifecycle.Root{
		{Path: root, Modes: []string{lifecycle.ModeCreate, lifecycle.ModeArchive}, MaxSessions: 9},
		{Path: noArchive, Modes: []string{lifecycle.ModeCreate}, MaxSessions: 9},
	}, CreatePerHour: 5, ArchivePerHour: 5, MaxTotalSessions: 9}
	if mod != nil {
		mod(cfg)
	}
	dir := filepath.Join(t.TempDir(), "ov")
	_ = os.Mkdir(dir, 0o700)
	store, err := overlay.Open(dir, overlay.Options{Writer: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	ac, err := NewArchiveConfig(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	e := &archEnv{t: t, nat: nat, store: store, ac: ac, root: root, dir: dir, clock: time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)}
	ac.now = func() time.Time { return e.clock }
	policy, err := security.New([]string{root, noArchive}, []string{"ses_arch1", "ses_arch2", "ses_arch3", "ses_child", "ses_grand", "ses_nomode"})
	if err != nil {
		t.Fatal(err)
	}
	runs := execution.NewWithTiming(client, 3*time.Second, reconcile)
	t.Cleanup(runs.Close)
	ac.Install(runs)
	e.s = &Server{Client: client, Runs: runs, Policy: policy, Archive: ac}
	e.logs = captureLogs(t)
	t.Cleanup(func() { archiveHook = nil; archiveBound = 5 * time.Second })
	return e
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

func (e *archEnv) call(tool, session string) (ArchiveOutput, error) {
	e.t.Helper()
	a := args{SessionID: session}
	if err := validateArgs(tool, a); err != nil {
		return ArchiveOutput{}, err
	}
	v, err := e.s.call(context.Background(), tool, a)
	if err != nil {
		return ArchiveOutput{}, err
	}
	return v.(ArchiveOutput), nil
}

func (e *archEnv) archive(id string) (ArchiveOutput, error) {
	return e.call("archive_session", "opencode:"+id)
}

func (e *archEnv) archived(id string) bool {
	ok, err := e.store.Archived(context.Background(), "opencode:"+id)
	if err != nil {
		e.t.Fatal(err)
	}
	return ok
}

func (e *archEnv) wantRefusal(tool, id, code string) {
	e.t.Helper()
	gen, entries := e.store.Generation(), e.store.Entries()
	_, err := e.call(tool, "opencode:"+id)
	if codeOf(err) != code {
		e.t.Fatalf("%s %s: %v, want %s", tool, id, err, code)
	}
	if e.store.Generation() != gen || e.store.Entries() != entries {
		e.t.Fatalf("%s %s: a refused call changed the overlay", tool, id)
	}
	if w := e.nat.writes(); len(w) != 0 {
		e.t.Fatalf("native writes: %v", w)
	}
	if e.s.Runs.LifecycleGuard(id) {
		e.t.Fatalf("%s %s: the reservation was not released", tool, id)
	}
}

func TestArchiveHappyPathMakesNoNativeWrite(t *testing.T) {
	e := newArchEnv(t, nil)
	out, err := e.archive("ses_arch1")
	if err != nil || !out.Archived || !out.Changed || out.ArchivedAt == "" || out.SessionID != "opencode:ses_arch1" {
		t.Fatalf("archive: %+v %v", out, err)
	}
	again, err := e.archive("ses_arch1")
	if err != nil || !again.Archived || again.Changed {
		t.Fatalf("idempotent archive: %+v %v", again, err)
	}
	un, err := e.call("unarchive_session", "opencode:ses_arch1")
	if err != nil || un.Archived || !un.Changed {
		t.Fatalf("unarchive: %+v %v", un, err)
	}
	un2, err := e.call("unarchive_session", "opencode:ses_arch1")
	if err != nil || un2.Changed {
		t.Fatalf("idempotent unarchive: %+v %v", un2, err)
	}
	if w := e.nat.writes(); len(w) != 0 {
		t.Fatalf("a native write (no PATCH, DELETE or POST is allowed): %v", w)
	}
}

func TestArchivePreconditionsRefuseWithoutAnyChange(t *testing.T) {
	cases := map[string]struct {
		set  func(e *archEnv)
		code string
	}{
		"native status busy":       {func(e *archEnv) { e.nat.set("ses_arch1", func(s *archSession) { s.busy = true }) }, "SESSION_BUSY"},
		"pending permission":       {func(e *archEnv) { e.nat.mu.Lock(); e.nat.perm = []string{"ses_arch1"}; e.nat.mu.Unlock() }, "SESSION_BUSY"},
		"unfinished history":       {func(e *archEnv) { e.nat.set("ses_arch1", func(s *archSession) { s.unfinished = true }) }, "SESSION_BUSY"},
		"running child":            {func(e *archEnv) { e.nat.set("ses_child", func(s *archSession) { s.busy = true }) }, "SESSION_BUSY"},
		"running grandchild":       {func(e *archEnv) { e.nat.set("ses_grand", func(s *archSession) { s.busy = true }) }, "SESSION_BUSY"},
		"status unreadable":        {func(e *archEnv) { e.nat.mu.Lock(); e.nat.fail["/session/status"] = true; e.nat.mu.Unlock() }, "UPSTREAM_FAILURE"},
		"permissions unreadable":   {func(e *archEnv) { e.nat.mu.Lock(); e.nat.fail["/permission"] = true; e.nat.mu.Unlock() }, "UPSTREAM_FAILURE"},
		"history unreadable":       {func(e *archEnv) { e.nat.mu.Lock(); e.nat.fail["/session/ses_arch1/message"] = true; e.nat.mu.Unlock() }, "SESSION_BUSY"},
		"session tree unreadable":  {func(e *archEnv) { e.nat.mu.Lock(); e.nat.fail["/session"] = true; e.nat.mu.Unlock() }, "UPSTREAM_FAILURE"},
		"guard held by a reserved": {func(e *archEnv) { _, _ = e.s.Runs.ReserveLifecycle("ses_arch1") }, "SESSION_BUSY"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newArchEnv(t, nil)
			tc.set(e)
			gen := e.store.Generation()
			_, err := e.archive("ses_arch1")
			if codeOf(err) != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
			if e.store.Generation() != gen || e.archived("ses_arch1") {
				t.Fatal("a refused call changed the overlay")
			}
			if w := e.nat.writes(); len(w) != 0 {
				t.Fatalf("native writes: %v", w)
			}
		})
	}
}

func TestArchiveAuthorizationRefusals(t *testing.T) {
	e := newArchEnv(t, nil)
	e.wantRefusal("archive_session", "ses_nomode", "FORBIDDEN") // root without the archive mode
	_, err := e.call("archive_session", "opencode:ses_unknown")
	if codeOf(err) != "FORBIDDEN" && codeOf(err) != "NOT_FOUND" {
		t.Fatalf("unlisted session: %v", err)
	}
	for _, bad := range []string{"claudecode:" + strings.Repeat("a", 8) + "-aaaa-aaaa-aaaa-" + strings.Repeat("a", 12), "ses_arch1", "opencode:bad id"} {
		if _, err := e.s.call(context.Background(), "archive_session", args{SessionID: bad}); codeOf(err) != "INVALID_INPUT" {
			t.Fatalf("%q: %v, want INVALID_INPUT", bad, err)
		}
	}
	if e.store.Entries() != 0 {
		t.Fatal("a refused call wrote an entry")
	}
	e.s.ReadOnly = true
	e.wantRefusal("archive_session", "ses_arch1", "FORBIDDEN")
	e.wantRefusal("unarchive_session", "ses_arch1", "FORBIDDEN")
	e.wantRefusal("close_session", "ses_arch1", "FORBIDDEN")
}

// ADR-022 7.3: the re-check immediately before the overlay write (mutation: drop it).
func TestArchivePreWriteRecheck(t *testing.T) {
	e := newArchEnv(t, nil)
	archiveHook = func() { e.nat.set("ses_arch1", func(s *archSession) { s.busy = true }) }
	e.wantRefusal("archive_session", "ses_arch1", "SESSION_BUSY")
	if e.archived("ses_arch1") {
		t.Fatal("archived a session that became busy between the checks")
	}
}

func TestArchiveHourlyCap(t *testing.T) {
	e := newArchEnv(t, func(c *lifecycle.Config) { c.ArchivePerHour = 2 })
	for _, id := range []string{"ses_arch1", "ses_arch2"} {
		if _, err := e.archive(id); err != nil {
			t.Fatal(err)
		}
	}
	e.wantRefusal("archive_session", "ses_arch3", "SESSION_LIMIT")
	// un-archive shares the cap (Q4), and the idempotent repeat is charged too
	e.wantRefusal("unarchive_session", "ses_arch1", "SESSION_LIMIT")
	e.clock = e.clock.Add(61 * time.Minute)
	if _, err := e.archive("ses_arch3"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

// ADR-025 4.5 / Q1: send_prompt to an archived session is FORBIDDEN/session_archived with no I/O.
func TestSendPromptToAnArchivedSessionIsRefusedWithoutIO(t *testing.T) {
	e := newArchEnv(t, nil)
	if _, err := e.archive("ses_arch1"); err != nil {
		t.Fatal(err)
	}
	e.nat.mu.Lock()
	before := len(e.nat.reqs)
	e.nat.mu.Unlock()
	_, err := e.s.call(context.Background(), "send_prompt", args{SessionID: "opencode:ses_arch1", Prompt: "hi", IdempotencyKey: "key-archived-0001"})
	if codeOf(err) != "FORBIDDEN" || envelope(err).Reason != "session_archived" {
		t.Fatalf("send_prompt = %v, want FORBIDDEN/session_archived", err)
	}
	e.nat.mu.Lock()
	n := len(e.nat.reqs)
	e.nat.mu.Unlock()
	// authorized() reads the session once (the ACL path); the refusal itself costs no native call
	if n-before > 1 {
		t.Fatalf("the refusal made %d native requests, want at most the ACL read", n-before)
	}
	if _, err := e.call("unarchive_session", "opencode:ses_arch1"); err != nil {
		t.Fatal(err)
	}
	_, err = e.s.call(context.Background(), "send_prompt", args{SessionID: "opencode:ses_arch1", Prompt: "hi", IdempotencyKey: "key-archived-0002"})
	if err != nil {
		t.Fatalf("after un-archive: %v", err)
	}
	// an unrelated session is never affected
	if _, err := e.s.call(context.Background(), "send_prompt", args{SessionID: "opencode:ses_arch2", Prompt: "hi", IdempotencyKey: "key-archived-0003"}); err != nil {
		t.Fatalf("unrelated send_prompt: %v", err)
	}
}

// The reservation: SESSION_BUSY for a concurrent prompt, guard_reason on get_session, released on every exit.
func TestReservationWhileTheOverlayWriteIsInFlight(t *testing.T) {
	e := newArchEnv(t, nil)
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	e.store.Hook = func(step string) error {
		if step == "write" {
			entered <- struct{}{}
			<-gate
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := e.archive("ses_arch1"); done <- err }()
	<-entered
	_, err := e.s.call(context.Background(), "send_prompt", args{SessionID: "opencode:ses_arch1", Prompt: "hi", IdempotencyKey: "key-reserved-001"})
	if codeOf(err) != "SESSION_BUSY" {
		t.Fatalf("send_prompt during the change = %v, want SESSION_BUSY", err)
	}
	v, err := e.s.call(context.Background(), "get_session", args{SessionID: "opencode:ses_arch1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.(Session); got.GuardReason != "archive" || got.State == "idle" {
		t.Fatalf("get_session = state %q guard_reason %q, want a held session with guard_reason archive", got.State, got.GuardReason)
	}
	if _, err := e.archive("ses_arch1"); codeOf(err) != "SESSION_BUSY" {
		t.Fatalf("second change on the same session: %v", err)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if e.s.Runs.LifecycleGuard("ses_arch1") {
		t.Fatal("reservation held after the call")
	}
}

func TestReservationIsReleasedOnPanic(t *testing.T) {
	e := newArchEnv(t, nil)
	archiveHook = func() { panic("boom") }
	func() {
		defer func() { _ = recover() }()
		_, _ = e.archive("ses_arch1")
	}()
	if e.s.Runs.LifecycleGuard("ses_arch1") {
		t.Fatal("the reservation survived a panic")
	}
	lines := archLines(t, e.logs, "session_archive")
	if len(lines) != 1 || lines[0]["error_code"] != "INTERNAL" || lines[0]["accepted"] != false {
		t.Fatalf("panic audit = %v", lines)
	}
	archiveHook = nil
	if _, err := e.archive("ses_arch1"); err != nil {
		t.Fatalf("session not usable after a panic: %v", err)
	}
}

func archLines(t *testing.T, buf *syncBuffer, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) == nil && m["event"] == event {
			out = append(out, m)
		}
	}
	return out
}

// ADR-025 4.6 and test 10: exactly one line per call on every return path, closed fields only.
func TestArchiveAuditOneLinePerCall(t *testing.T) {
	e := newArchEnv(t, nil)
	_, _ = e.archive("ses_arch1") // archived
	_, _ = e.archive("ses_arch1") // already_archived
	e.nat.set("ses_arch2", func(s *archSession) { s.busy = true })
	_, _ = e.archive("ses_arch2")                            // rejected, SESSION_BUSY
	_, _ = e.call("unarchive_session", "opencode:ses_arch1") // unarchived
	_, _ = e.call("unarchive_session", "opencode:ses_arch1") // not_archived
	e.s.ReadOnly = true
	_, _ = e.archive("ses_arch3") // rejected, FORBIDDEN
	e.s.ReadOnly = false
	lines := append(archLines(t, e.logs, "session_archive"), archLines(t, e.logs, "session_unarchive")...)
	want := map[string]int{"archived": 1, "already_archived": 1, "rejected": 2, "unarchived": 1, "not_archived": 1}
	got := map[string]int{}
	for _, l := range lines {
		got[fmt.Sprint(l["outcome"])]++
		for _, k := range []string{"tool", "accepted", "principal", "overlay_generation", "overlay_entries"} {
			if _, ok := l[k]; !ok {
				t.Fatalf("line lacks %s: %v", k, l)
			}
		}
		for _, banned := range []string{"title", "path", "directory", "prompt", "message"} {
			if _, ok := l[banned]; ok {
				t.Fatalf("line carries %s: %v", banned, l)
			}
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("outcomes = %v, want %v", got, want)
		}
	}
	if len(lines) != 6 {
		t.Fatalf("%d lines for 6 calls", len(lines))
	}
	if strings.Contains(e.logs.String(), e.root) {
		t.Fatal("a path reached the journal")
	}
	if n := len(archLines(t, e.logs, "prompt_submission")) + len(archLines(t, e.logs, "interrupt")); n != 0 {
		t.Fatalf("the generic audit wrote %d extra lines", n)
	}
}

// close_session: interrupt first, archive only after it settled; exactly one session_close line.
func TestCloseSessionInterruptsThenArchivesWithOneAuditLine(t *testing.T) {
	e := newArchEnv(t, nil)
	x, err := e.s.call(context.Background(), "send_prompt", args{SessionID: "opencode:ses_arch1", Prompt: "work", IdempotencyKey: "key-close-0001"})
	if err != nil {
		t.Fatal(err)
	}
	_ = x
	waitRunning(t, e, x.(execution.Execution).ID)
	out, err := e.call("close_session", "opencode:ses_arch1")
	if err != nil || !out.Archived || out.Interrupt != "interrupted" {
		t.Fatalf("close: %+v %v", out, err)
	}
	if held, _ := e.s.Runs.SessionGuard("ses_arch1"); held {
		t.Fatal("the guard is still held after close")
	}
	var aborts int
	e.nat.mu.Lock()
	for _, r := range e.nat.reqs {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, "/abort") {
			aborts++
		}
	}
	e.nat.mu.Unlock()
	if aborts != 1 {
		t.Fatalf("%d aborts, want the one interrupt", aborts)
	}
	lines := archLines(t, e.logs, "session_close")
	if len(lines) != 1 || lines[0]["interrupt"] != "interrupted" || lines[0]["outcome"] != "closed" {
		t.Fatalf("session_close lines = %v", lines)
	}
	if n := len(archLines(t, e.logs, "interrupt")); n != 0 {
		t.Fatalf("close_session wrote %d interrupt lines, want none", n)
	}
	// no execution: interrupt "none"
	out2, err := e.call("close_session", "opencode:ses_arch2")
	if err != nil || out2.Interrupt != "none" || !out2.Archived {
		t.Fatalf("close without an execution: %+v %v", out2, err)
	}
}

func TestCloseSessionFailedInterruptLeavesTheSessionUnarchived(t *testing.T) {
	e := newArchEnv(t, nil)
	x, err := e.s.call(context.Background(), "send_prompt", args{SessionID: "opencode:ses_arch1", Prompt: "work", IdempotencyKey: "key-close-0002"})
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(t, e, x.(execution.Execution).ID)
	e.nat.mu.Lock()
	e.nat.fail["/session/ses_arch1/abort"] = true
	e.nat.mu.Unlock()
	archiveBound = 400 * time.Millisecond
	_, err = e.call("close_session", "opencode:ses_arch1")
	if err == nil {
		t.Fatal("close succeeded although the interrupt failed")
	}
	if e.archived("ses_arch1") || e.store.Entries() != 0 {
		t.Fatal("archived after a failed interrupt")
	}
	if e.s.Runs.LifecycleGuard("ses_arch1") {
		t.Fatal("reservation held")
	}
	lines := archLines(t, e.logs, "session_close")
	if len(lines) != 1 || lines[0]["interrupt"] != "failed" || lines[0]["accepted"] != false {
		t.Fatalf("audit = %v", lines)
	}
}

// ADR-025 Q3 / test 2: an unknown overlay state refuses the tools and fails lists, and never blocks
// an unrelated prompt (the prompt path keeps the last good snapshot).
func TestUnknownOverlayRefusesToolsButNotUnrelatedPrompts(t *testing.T) {
	e := newArchEnv(t, nil)
	if _, err := e.archive("ses_arch3"); err != nil {
		t.Fatal(err)
	}
	// corrupt the file under the writer, then let a re-read latch the unknown state
	e.store.Hook = func(step string) error {
		if step == "fsync_dir" {
			return fmt.Errorf("EIO")
		}
		return nil
	}
	if _, err := e.archive("ses_arch1"); codeOf(err) != "UNKNOWN_OUTCOME" || envelope(err).Reason != "overlay_uncertain" {
		t.Fatalf("uncertain write: %v, want UNKNOWN_OUTCOME/overlay_uncertain", err)
	}
	e.store.Hook = nil
	path := filepath.Join(e.storeDir(), overlay.FileName)
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.archive("ses_arch2"); codeOf(err) != "UPSTREAM_FAILURE" || envelope(err).Reason != "overlay_unavailable" {
		t.Fatalf("tool on an unknown overlay: %v, want UPSTREAM_FAILURE/overlay_unavailable", err)
	}
	if _, err := e.store.Archived(context.Background(), "opencode:ses_arch3"); err == nil {
		t.Fatal("lists must fail while unknown")
	}
	if _, err := e.s.call(context.Background(), "send_prompt", args{SessionID: "opencode:ses_arch2", Prompt: "hi", IdempotencyKey: "key-unknown-001"}); err != nil {
		t.Fatalf("an unrelated prompt was affected: %v", err)
	}
	if _, err := e.s.call(context.Background(), "send_prompt", args{SessionID: "opencode:ses_arch3", Prompt: "hi", IdempotencyKey: "key-unknown-002"}); codeOf(err) != "FORBIDDEN" {
		t.Fatalf("the last good snapshot must keep refusing the archived session: %v", err)
	}
}

func (e *archEnv) storeDir() string { return e.dir }

// Q5: the tools exist only when configured; with an overlay entry for every ACL session the existing
// tools' behaviour is unchanged (Q2: byte-identical).
func TestExistingToolsAreByteIdenticalWhateverTheOverlay(t *testing.T) {
	e := newArchEnv(t, nil)
	get := func() string {
		v, err := e.s.call(context.Background(), "get_session", args{SessionID: "opencode:ses_arch2"})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	list := func() string {
		v, err := e.s.call(context.Background(), "list_sessions", args{})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	g0, l0 := get(), list()
	for _, id := range []string{"ses_arch1", "ses_arch2", "ses_arch3"} {
		if _, err := e.archive(id); err != nil {
			t.Fatal(err)
		}
	}
	if get() != g0 || list() != l0 {
		t.Fatal("an overlay entry changed an existing tool's output")
	}
	if _, err := e.s.call(context.Background(), "get_session", args{SessionID: "opencode:ses_arch2"}); err != nil {
		t.Fatal(err)
	}
}

func TestToolsAreRegisteredOnlyWhenConfigured(t *testing.T) {
	for _, configured := range []bool{false, true} {
		e := newArchEnv(t, nil)
		if !configured {
			e.s.Archive = nil
		}
		ts := httptest.NewServer(e.s)
		e.s.Token = strings.Repeat("t", 32)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		client, err := mcpConnect(ctx, ts.URL, e.s.Token)
		if err != nil {
			t.Fatal(err)
		}
		tools, err := client.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		have := map[string]bool{}
		for _, tl := range tools.Tools {
			have[tl.Name] = true
		}
		for _, name := range []string{"archive_session", "unarchive_session", "close_session"} {
			if have[name] != configured {
				t.Fatalf("configured=%v: %s registered=%v", configured, name, have[name])
			}
		}
		_ = client.Close()
		ts.Close()
		cancel()
	}
}

func TestAuthorizationNeverConsultsTheOverlay(t *testing.T) {
	// (a) the dependency test: internal/security and internal/lifecycle do not import the overlay.
	for _, pkg := range []string{"../security", "../lifecycle"} {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, pkg, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pkgs {
			for name, f := range p.Files {
				for _, imp := range f.Imports {
					if strings.Contains(imp.Path.Value, "internal/overlay") {
						t.Fatalf("%s imports the overlay", name)
					}
				}
			}
		}
	}
	// (b) the call-graph test over internal/mcp: the named authorization functions neither reference
	// ArchiveOverlay/ArchiveStore nor call Archived, directly or through a helper they call.
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	funcs := map[string][]*ast.FuncDecl{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok {
					funcs[fd.Name.Name] = append(funcs[fd.Name.Name], fd)
				}
			}
		}
	}
	forbidden := map[string]bool{"Archived": true, "ArchivedKnown": true, "ArchiveOverlay": true, "ArchiveStore": true, "ArchiveConfig": true, "overlay": true}
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		for _, fd := range funcs[name] {
			ast.Inspect(fd, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					if forbidden[x.Name] {
						t.Errorf("%s (reached from an authorization function) uses %s", name, x.Name)
					}
				case *ast.SelectorExpr:
					if forbidden[x.Sel.Name] {
						t.Errorf("%s (reached from an authorization function) uses %s", name, x.Sel.Name)
					}
				case *ast.CallExpr:
					// follow calls only (a field named like a method is not a call)
					switch f := x.Fun.(type) {
					case *ast.Ident:
						walk(f.Name)
					case *ast.SelectorExpr:
						walk(f.Sel.Name)
					}
				}
				return true
			})
		}
	}
	for _, root := range []string{"authorized", "claudeSession", "native", "claudeID", "claudeCanonical"} {
		if len(funcs[root]) == 0 {
			t.Fatalf("authorization function %s not found: the test names are stale", root)
		}
		walk(root)
	}
}

func mcpConnect(ctx context.Context, url, token string) (*sdk.ClientSession, error) {
	return mcpclient.Connect(ctx, url, token)
}

func waitRunning(t *testing.T, e *archEnv, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		x, err := e.s.Runs.Get(context.Background(), id)
		if err == nil && x.State == "running" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the execution never reached running")
}

// ADR-025 test 9 and 2: the real store behind list_catalog. The labels survive a restart and an
// invalid file is the unknown state: the catalog fails with INTERNAL, never the empty overlay.
func TestCatalogWithTheRealOverlayAcrossARestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ov")
	_ = os.Mkdir(dir, 0o700)
	w, err := overlay.Open(dir, overlay.Options{Writer: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Archive(context.Background(), "opencode:ses_a", "op"); err != nil {
		t.Fatal(err)
	}
	e := newCatalogEnv(t, w)
	before := e.mustCall(args{})
	if got := strings.Join(ids(before), ","); strings.Contains(got, "ses_a") {
		t.Fatalf("an archived session is listed: %s", got)
	}
	shown := e.mustCall(args{IncludeArchived: true})
	if !strings.Contains(strings.Join(ids(shown), ","), "ses_a") {
		t.Fatal("include_archived does not list the archived session")
	}
	w.Close()
	r, err := overlay.Open(dir, overlay.Options{Writer: true})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	e2 := newCatalogEnv(t, r)
	after := e2.mustCall(args{})
	if strings.Join(ids(after), ",") != strings.Join(ids(before), ",") {
		t.Fatalf("catalog changed over a restart: %v vs %v", ids(before), ids(after))
	}
	if err := os.WriteFile(filepath.Join(dir, overlay.FileName), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad, err := overlay.Open(dir, overlay.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	e3 := newCatalogEnv(t, bad)
	if _, err := e3.call(args{}); codeOf(err) != "INTERNAL" {
		t.Fatalf("catalog over an unknown overlay: %v, want INTERNAL", err)
	}
}

// #308 P3-4: an interrupt that fails while the guard is free. The execution finished natively
// between the guard read and the interrupt, so Interrupt answers "already terminal": that is a failed
// interrupt and the overlay stays unchanged (the branch is pinned without the reservation's help,
// which would not refuse a free guard).
func TestCloseSessionInterruptFailsWhileTheGuardIsFree(t *testing.T) {
	// Determinism. Two facts pin the branch:
	//
	// (1) The long interval (10 s) keeps every interval-gated observe out of the window. The
	// monitor observes immediately once when the run starts (internal/execution/manager.go:509)
	// and then only when the 10 s ticker fires, so after its first observe completes nothing can
	// settle the run until far after this test has finished.
	//
	// (2) The barrier below waits for TWO arrivals of the last native read of an observation
	// (/permission), not one. The first /permission arrival is the send preflight's read (it runs
	// synchronously inside send_prompt, manager.go:365-396); the second is the monitor's first
	// observe. Only after the second arrival is the monitor's first observation guaranteed to hold
	// all its native reads (Get, status, history, permission, in that order, reconcile.go:242-266),
	// so the terminal flip in nat.set below is invisible to it: an observation that started before
	// the flip cannot settle the run afterwards. waitRunning's Gets perform no native reads while
	// the monitor is alive (refresh returns early on !monitorDone, reconcile.go:171), so no other
	// observation can run between the barrier and close_session's guard snapshot. The guard
	// assertion pins the held run, and the interrupt's own probe (manager.go:658) is therefore the
	// first observation that sees the terminal native: it settles the run and answers
	// "execution already terminal" deterministically.
	e := newArchEnvTiming(t, nil, 10*time.Second)
	probed := make(chan struct{}, 2)
	e.nat.mu.Lock()
	e.nat.hook = func(p string) {
		if p == "/permission" { // the last native read of one observation
			select {
			case probed <- struct{}{}:
			default:
			}
		}
	}
	e.nat.mu.Unlock()
	x, err := e.s.call(context.Background(), "send_prompt", args{SessionID: "opencode:ses_arch1", Prompt: "work", IdempotencyKey: "key-close-0003"})
	if err != nil {
		t.Fatal(err)
	}
	waitRunning(t, e, x.(execution.Execution).ID)
	// The first arrival is the send preflight's /permission read (its single Permissions call; the
	// arrival count below depends on it, so a change to the preflight's native reads must update
	// this count); the second is the monitor's first observe. Waiting for the second guarantees that observation has finished reading the
	// native (with the still-busy session) before the session is made terminal below.
	for i := 0; i < 2; i++ {
		select {
		case <-probed:
		case <-time.After(2 * time.Second):
			t.Fatalf("native observation %d/2 did not reach its permission read", i+1)
		}
	}
	e.nat.set("ses_arch1", func(s *archSession) {
		s.busy = false
		a := &s.msgs[len(s.msgs)-1]
		a.Info.Time.Completed, a.Info.Finish = 1, "stop"
	})
	if held, st := e.s.Runs.SessionGuard("ses_arch1"); !held || st != "running" {
		t.Fatalf("test premise failed: guard held=%v state=%q before close", held, st)
	}
	_, err = e.call("close_session", "opencode:ses_arch1")
	if err == nil {
		t.Fatal("close succeeded although the interrupt failed")
	}
	if codeOf(err) != "INVALID_INPUT" {
		t.Fatalf("close error = %v, want INVALID_INPUT (execution already terminal)", err)
	}
	if e.archived("ses_arch1") || e.store.Entries() != 0 {
		t.Fatal("archived after a failed interrupt")
	}
	if held, _ := e.s.Runs.SessionGuard("ses_arch1"); held {
		t.Fatalf("the test premise failed: the guard is still held (%v)", err)
	}
	lines := archLines(t, e.logs, "session_close")
	if len(lines) != 1 || lines[0]["interrupt"] != "failed" || lines[0]["accepted"] != false {
		t.Fatalf("audit = %v", lines)
	}
}
