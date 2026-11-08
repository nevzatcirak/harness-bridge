//go:build linux

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// ADR-020 (b) test plan groups 9 (admin socket) and 11 (audit line).

const (
	testSession   = "opencode:ses_example"
	testExecution = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

type stubReleaser struct {
	mu    sync.Mutex
	calls []string
	opts  []execution.ReleaseOptions
	ev    execution.ReleaseEvidence
	err   error
}

func (s *stubReleaser) ReleaseGuard(_ context.Context, session, id string, opt execution.ReleaseOptions) (execution.ReleaseEvidence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, session+" "+id)
	s.opts = append(s.opts, opt)
	return s.ev, s.err
}

func (s *stubReleaser) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (b *syncBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("audit line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hbadm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err = os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

type harness struct {
	socket string
	stub   *stubReleaser
	logs   *syncBuffer
	srv    *Server
}

func start(t *testing.T, uid int, stub *stubReleaser) *harness {
	t.Helper()
	path := filepath.Join(privateDir(t), "admin.sock")
	ln, err := Listen(path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	srv := &Server{Runs: stub, UID: uid, Logger: slog.New(slog.NewJSONHandler(logs, nil)), ReadTimeout: time.Second, HandleTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	t.Cleanup(func() {
		srv.Close()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return &harness{socket: path, stub: stub, logs: logs, srv: srv}
}

func validRequest() Request {
	return Request{Op: OpReleaseGuard, SessionID: testSession, ExecutionID: testExecution, Ticket: "OPS-1234"}
}

func call(t *testing.T, h *harness, req Request) Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := Call(ctx, h.socket, req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func raw(t *testing.T, h *harness, payload []byte) Response {
	t.Helper()
	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err = conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	var r Response
	if err = json.NewDecoder(conn).Decode(&r); err != nil {
		t.Fatalf("no response: %v", err)
	}
	return r
}

func onlyLine(t *testing.T, h *harness) map[string]any {
	t.Helper()
	lines := h.logs.lines(t)
	if len(lines) != 1 {
		t.Fatalf("want exactly one audit line, got %d: %v", len(lines), lines)
	}
	return lines[0]
}

func wantFields(t *testing.T, line map[string]any, fields map[string]any) {
	t.Helper()
	for k, v := range fields {
		got, present := line[k]
		if v == nil {
			if present {
				t.Errorf("%s = %v, want absent", k, got)
			}
			continue
		}
		if !present || got != v {
			t.Errorf("%s = %v, want %v (%v)", k, got, v, line)
		}
	}
}

func refused(t *testing.T, r Response, code string) {
	t.Helper()
	if r.Outcome != "refused" || r.Error == nil || r.Error.Code != code {
		t.Fatalf("response %+v, want refused %s", r, code)
	}
}

var principal = "admin:uid=" + strconv.Itoa(os.Geteuid())

func TestServiceUIDReleases(t *testing.T) {
	released := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	stub := &stubReleaser{ev: execution.ReleaseEvidence{NativeRead: true, NativeState: "absent", CorrelatedUser: "found", SettleMS: 5000, GuardReleasedAt: &released}}
	h := start(t, os.Geteuid(), stub)
	r := call(t, h, validRequest())
	if r.Outcome != "released" || r.Error != nil || r.Evidence == nil || r.Evidence.GuardReleasedAt == nil || r.SessionID != testSession || r.ExecutionID != testExecution {
		t.Fatalf("response %+v", r)
	}
	if got := stub.calls; len(got) != 1 || got[0] != "ses_example "+testExecution || stub.opts[0].AllowMissingUserMessage {
		t.Fatalf("calls %v %+v", got, stub.opts)
	}
	wantFields(t, onlyLine(t, h), map[string]any{
		"msg": "guard release", "event": "guard_release", "tool": "admin", "outcome": "released", "accepted": true, "error_code": nil,
		"session_id": testSession, "execution_id": testExecution, "native_state": "absent", "unfinished": false, "permission_pending": false,
		"correlated_user": "found", "settle_ms": float64(5000), "ticket": "OPS-1234", "principal": principal, "allow_missing_user_message": nil,
	})
}

// Group 9 and the planner's extra case: a non-service UID is refused before
// the request is read, with one audit line.
func TestWrongPeerUIDRefused(t *testing.T) {
	stub := &stubReleaser{}
	h := start(t, os.Geteuid()+1, stub)
	r := call(t, h, validRequest())
	refused(t, r, "FORBIDDEN")
	if stub.count() != 0 {
		t.Fatal("release attempted for a foreign peer")
	}
	wantFields(t, onlyLine(t, h), map[string]any{"event": "guard_release", "outcome": "refused", "accepted": false, "error_code": "FORBIDDEN", "principal": principal, "ticket": nil, "session_id": nil})
}

func TestMalformedAndOversizedRefused(t *testing.T) {
	cases := map[string][]byte{
		"not_json":       []byte("release please\n"),
		"no_newline":     []byte(`{"op":"release_guard"}`),
		"two_objects":    []byte(`{"op":"release_guard"} {"op":"release_guard"}` + "\n"),
		"oversized":      append(bytes.Repeat([]byte(" "), MaxRequestBytes+10), '\n'),
		"free_text":      []byte(`{"op":"release_guard","session_id":"opencode:ses_example","execution_id":"0f1e2d3c4b5a69788796a5b4c3d2e1f0","ticket":"OPS-1","reason":"FREE TEXT SECRET"}` + "\n"),
		"wrong_op":       []byte(`{"op":"delete_everything","session_id":"opencode:ses_example","execution_id":"0f1e2d3c4b5a69788796a5b4c3d2e1f0","ticket":"OPS-1"}` + "\n"),
		"bare_session":   []byte(`{"op":"release_guard","session_id":"ses_example","execution_id":"0f1e2d3c4b5a69788796a5b4c3d2e1f0","ticket":"OPS-1"}` + "\n"),
		"bad_execution":  []byte(`{"op":"release_guard","session_id":"opencode:ses_example","execution_id":"EXEC-INJECT","ticket":"OPS-1"}` + "\n"),
		"missing_ticket": []byte(`{"op":"release_guard","session_id":"opencode:ses_example","execution_id":"0f1e2d3c4b5a69788796a5b4c3d2e1f0"}` + "\n"),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			stub := &stubReleaser{}
			h := start(t, os.Geteuid(), stub)
			refused(t, raw(t, h, payload), "INVALID_INPUT")
			if stub.count() != 0 {
				t.Fatal("release attempted for an invalid request")
			}
			line := onlyLine(t, h)
			wantFields(t, line, map[string]any{"event": "guard_release", "outcome": "refused", "error_code": "INVALID_INPUT", "principal": principal})
			for _, leaked := range []string{"FREE TEXT SECRET", "EXEC-INJECT", "release please", "delete_everything"} {
				if strings.Contains(h.logs.String(), leaked) {
					t.Fatalf("untrusted text %q logged: %s", leaked, h.logs.String())
				}
			}
		})
	}
}

// Group 11 and the planner's extra case: the ticket charset and length are
// enforced, and an invalid ticket is never logged.
func TestTicketBounded(t *testing.T) {
	for name, ticket := range map[string]string{
		"too_long": strings.Repeat("A", 65), "space": "OPS 1234", "slash": "OPS/1", "non_ascii": "OPS-ö", "newline": "OPS\nINJECT", "equals": "a=b", "empty": "",
	} {
		t.Run(name, func(t *testing.T) {
			stub := &stubReleaser{}
			h := start(t, os.Geteuid(), stub)
			req := validRequest()
			req.Ticket = ticket
			refused(t, call(t, h, req), "INVALID_INPUT")
			if stub.count() != 0 {
				t.Fatal("release attempted with an invalid ticket")
			}
			line := onlyLine(t, h)
			wantFields(t, line, map[string]any{"ticket": nil, "outcome": "refused", "error_code": "INVALID_INPUT"})
			if ticket != "" && strings.Contains(h.logs.String(), ticket) {
				t.Fatalf("invalid ticket logged: %s", h.logs.String())
			}
		})
	}
	stub := &stubReleaser{}
	h := start(t, os.Geteuid(), stub)
	req := validRequest()
	req.Ticket = strings.Repeat("a", 60) + "._-9"
	if r := call(t, h, req); r.Outcome != "released" {
		t.Fatalf("64-character ticket refused: %+v", r)
	}
	wantFields(t, onlyLine(t, h), map[string]any{"ticket": req.Ticket})
}

// Group 4 (audit part): the override is audited, and correlated_user=absent
// appears with released.
func TestOverrideAudited(t *testing.T) {
	stub := &stubReleaser{ev: execution.ReleaseEvidence{NativeRead: true, NativeState: "idle", CorrelatedUser: "absent", SettleMS: 5000}}
	h := start(t, os.Geteuid(), stub)
	req := validRequest()
	req.AllowMissingUserMessage = true
	if r := call(t, h, req); r.Outcome != "released" {
		t.Fatalf("response %+v", r)
	}
	if !stub.opts[0].AllowMissingUserMessage {
		t.Fatal("override not passed to the manager")
	}
	wantFields(t, onlyLine(t, h), map[string]any{"outcome": "released", "correlated_user": "absent", "allow_missing_user_message": true})
}

func TestManagerRefusalAudited(t *testing.T) {
	stub := &stubReleaser{ev: execution.ReleaseEvidence{NativeRead: true, NativeState: "busy", CorrelatedUser: "found"}, err: execution.Fail("SESSION_BUSY", "native session not idle")}
	h := start(t, os.Geteuid(), stub)
	r := call(t, h, validRequest())
	refused(t, r, "SESSION_BUSY")
	if r.Evidence == nil || r.Evidence.NativeState != "busy" {
		t.Fatalf("refusal evidence: %+v", r)
	}
	wantFields(t, onlyLine(t, h), map[string]any{"outcome": "refused", "accepted": false, "error_code": "SESSION_BUSY", "native_state": "busy", "settle_ms": nil, "ticket": "OPS-1234"})
	// No read: no native keys.
	stub2 := &stubReleaser{err: execution.Fail("INVALID_INPUT", "guard is not held by this execution")}
	h2 := start(t, os.Geteuid(), stub2)
	refused(t, call(t, h2, validRequest()), "INVALID_INPUT")
	wantFields(t, onlyLine(t, h2), map[string]any{"native_state": nil, "unfinished": nil, "permission_pending": nil, "correlated_user": nil})
}

func TestListenSocketMode(t *testing.T) {
	h := start(t, os.Geteuid(), &stubReleaser{})
	var st syscall.Stat_t
	if err := syscall.Lstat(h.socket, &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFSOCK || st.Mode&0o777 != 0o600 {
		t.Fatalf("socket mode %o", st.Mode)
	}
}

// Group 9: wrong directory mode or owner fails startup.
func TestListenRefusesUnsafeDirectory(t *testing.T) {
	dir := privateDir(t)
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if ln, err := Listen(filepath.Join(dir, "a.sock"), os.Geteuid()); err == nil {
		ln.Close()
		t.Fatal("0750 directory accepted")
	}
	dir = privateDir(t)
	if ln, err := Listen(filepath.Join(dir, "a.sock"), os.Geteuid()+1); err == nil {
		ln.Close()
		t.Fatal("directory owned by another user accepted")
	}
	link := filepath.Join(privateDir(t), "link")
	if err := os.Symlink(privateDir(t), link); err != nil {
		t.Fatal(err)
	}
	if ln, err := Listen(filepath.Join(link, "a.sock"), os.Geteuid()); err == nil {
		ln.Close()
		t.Fatal("symlinked directory accepted")
	}
	if _, err := Listen("relative/a.sock", os.Geteuid()); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestListenStaleSocket(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "a.sock")
	first, err := Listen(path, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	first.(*net.UnixListener).SetUnlinkOnClose(false)
	first.Close()
	second, err := Listen(path, os.Geteuid())
	if err != nil {
		t.Fatalf("stale own socket not replaced: %v", err)
	}
	second.Close()
	regular := filepath.Join(dir, "b.sock")
	if err := os.WriteFile(regular, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ln, err := Listen(regular, os.Geteuid()); err == nil {
		ln.Close()
		t.Fatal("regular file replaced")
	}
	if b, _ := os.ReadFile(regular); string(b) != "keep" {
		t.Fatal("regular file changed")
	}
}
