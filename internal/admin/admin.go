// Package admin is the ADR-020 (b) operator-only administrative channel: a
// private Unix socket opened only by the writer, peer-checked with SO_PEERCRED.
// It is not an MCP tool, has no TCP or HTTP listener and is not reachable
// through the remote connector.
package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

const (
	// OpReleaseGuard is the only operation.
	OpReleaseGuard = "release_guard"
	// MaxRequestBytes bounds one newline-delimited JSON request.
	MaxRequestBytes = 4096
	// MaxConns is the number of connections served at once. A connection over
	// the cap is refused at once with TOO_MANY_CONNECTIONS; nothing is queued.
	MaxConns        = 4
	maxResponseSize = 64 << 10
)

var (
	ticketPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	executionPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// ValidTicket reports whether a ticket matches [A-Za-z0-9._-]{1,64}.
func ValidTicket(ticket string) bool { return ticketPattern.MatchString(ticket) }

// Request is one admin request. Unknown fields are refused, so no free-text
// reason can be sent.
type Request struct {
	Op                      string `json:"op"`
	SessionID               string `json:"session_id"`
	ExecutionID             string `json:"execution_id"`
	Ticket                  string `json:"ticket"`
	AllowMissingUserMessage bool   `json:"allow_missing_user_message,omitempty"`
}

// Response is the JSON evidence returned to the operator.
type Response struct {
	Outcome     string                     `json:"outcome"`
	SessionID   string                     `json:"session_id,omitempty"`
	ExecutionID string                     `json:"execution_id,omitempty"`
	Evidence    *execution.ReleaseEvidence `json:"evidence,omitempty"`
	// ClaudeEvidence is the Claude harness release evidence (ADR-021 slice 4): there is no
	// native read in it.
	ClaudeEvidence *claudecode.ReleaseEvidence `json:"claude_evidence,omitempty"`
	Error          *execution.Error            `json:"error,omitempty"`
}

// Releaser is the execution manager's release operation.
type Releaser interface {
	ReleaseGuard(context.Context, string, string, execution.ReleaseOptions) (execution.ReleaseEvidence, error)
}

// ClaudeReleaser is the Claude Code release (ADR-021 §3 last bullet, D14 Q8): the same admin
// operation with Claude-specific preconditions in place of native reads. It is nil until the
// Claude Executor is constructed in production, which it is not: then a claudecode: session is
// refused with a clear reason (default off).
type ClaudeReleaser interface {
	ReleaseGuard(context.Context, string, string, claudecode.ReleaseOptions) (claudecode.ReleaseEvidence, error)
}

// Server serves one request per connection.
type Server struct {
	Runs Releaser
	// Claude releases claudecode: guards; nil refuses them ("not enabled").
	Claude ClaudeReleaser
	// UID is the only peer UID accepted (the service user's effective UID).
	UID int
	// Settle overrides the release settle interval (0: the manager default).
	Settle time.Duration
	// Logger receives the AUDIT-01 guard_release lines (nil: slog.Default()).
	Logger *slog.Logger
	// ReadTimeout bounds reading the request (0: 5 s); HandleTimeout bounds
	// the whole connection (0: 60 s).
	ReadTimeout, HandleTimeout time.Duration

	mu     sync.Mutex
	conns  int
	ln     net.Listener
	wg     sync.WaitGroup
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
}

// Serve accepts connections on ln until Close; it returns nil after Close.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	s.ln = ln
	s.ctx, s.cancel = context.WithCancel(context.Background())
	ctx := s.ctx
	s.mu.Unlock()
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return err
		}
		if !s.admit() {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.refuseOverCap(conn)
			}()
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.release()
			s.serveConn(ctx, conn)
		}()
	}
}

// admit takes a connection slot, or reports that all MaxConns are in use.
func (s *Server) admit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns >= MaxConns {
		return false
	}
	s.conns++
	return true
}

// refuseOverCap answers a connection over the cap without reading from it and
// closes it. The peer UID is still recorded in the one audit line.
func (s *Server) refuseOverCap(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	principal := "admin:uid=unknown"
	if uid, err := peerUID(conn); err == nil {
		principal = "admin:uid=" + strconv.Itoa(uid)
	}
	e := &execution.Error{Code: "TOO_MANY_CONNECTIONS", Message: "too many admin connections; retry", Retryable: true}
	s.reply(conn, s.refuse(principal, Request{}, nil, e))
}

func (s *Server) release() {
	s.mu.Lock()
	s.conns--
	s.mu.Unlock()
}

// active reports the number of connections currently being served.
func (s *Server) active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

// Close stops accepting, cancels in-flight requests and waits for them. The
// socket file is removed by the listener.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	ln, cancel := s.ln, s.cancel
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	handle := orDefault(s.HandleTimeout, 60*time.Second)
	_ = conn.SetDeadline(time.Now().Add(handle))
	ctx, cancel := context.WithTimeout(ctx, handle)
	defer cancel()
	uid, err := peerUID(conn)
	principal := "admin:uid=unknown"
	if err == nil {
		principal = "admin:uid=" + strconv.Itoa(uid)
	}
	if err != nil || uid != s.UID {
		s.reply(conn, s.refuse(principal, Request{}, nil, execution.Fail("FORBIDDEN", "peer is not the service user")))
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(orDefault(s.ReadTimeout, 5*time.Second)))
	req, err := readRequest(conn)
	if err != nil {
		s.reply(conn, s.refuse(principal, Request{}, nil, err))
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	s.reply(conn, s.Handle(ctx, req, principal))
}

func (s *Server) reply(conn net.Conn, r Response) {
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(b, '\n'))
}

// readRequest reads exactly one bounded JSON object terminated by a newline.
func readRequest(r io.Reader) (Request, error) {
	var req Request
	line, err := bufio.NewReaderSize(io.LimitReader(r, MaxRequestBytes+1), MaxRequestBytes+1).ReadBytes('\n')
	if len(line) > MaxRequestBytes {
		return req, execution.Fail("INVALID_INPUT", "request too large")
	}
	if err != nil {
		return req, execution.Fail("INVALID_INPUT", "request must be one newline-terminated JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&req); err != nil {
		return Request{}, execution.Fail("INVALID_INPUT", "malformed request")
	}
	if dec.More() {
		return Request{}, execution.Fail("INVALID_INPUT", "malformed request")
	}
	return req, nil
}

// Handle validates and executes one decoded request and writes exactly one
// audit line for it.
func (s *Server) Handle(ctx context.Context, req Request, principal string) Response {
	if req.Op != OpReleaseGuard {
		return s.refuse(principal, req, nil, execution.Fail("INVALID_INPUT", "unsupported operation"))
	}
	if !ValidTicket(req.Ticket) {
		return s.refuse(principal, req, nil, execution.Fail("INVALID_INPUT", "ticket must match [A-Za-z0-9._-]{1,64}"))
	}
	if strings.HasPrefix(req.SessionID, claudecode.SessionPrefix) {
		return s.handleClaude(ctx, req, principal)
	}
	session, ok := nativeSession(req.SessionID)
	if !ok || !executionPattern.MatchString(req.ExecutionID) {
		return s.refuse(principal, req, nil, execution.Fail("INVALID_INPUT", "invalid session or execution ID"))
	}
	ev, err := s.Runs.ReleaseGuard(ctx, session, req.ExecutionID, execution.ReleaseOptions{AllowMissingUserMessage: req.AllowMissingUserMessage, Settle: s.Settle})
	if err != nil {
		return s.refuse(principal, req, &ev, err)
	}
	s.audit(principal, req, &ev, nil)
	return Response{Outcome: "released", SessionID: req.SessionID, ExecutionID: req.ExecutionID, Evidence: &ev}
}

// claudeSession reports whether id is a canonical claudecode:<uuid> session ID.
func claudeSession(id string) bool {
	uuid, err := claudecode.ParseSessionID(id)
	return err == nil && id == claudecode.SessionPrefix+uuid
}

// handleClaude is the Claude harness release: validation, the nil-releaser refusal, one call,
// exactly one audit line.
func (s *Server) handleClaude(ctx context.Context, req Request, principal string) Response {
	switch {
	case !claudeSession(req.SessionID) || !executionPattern.MatchString(req.ExecutionID):
		return s.refuseClaude(principal, req, nil, execution.Fail("INVALID_INPUT", "invalid session or execution ID"))
	case req.AllowMissingUserMessage:
		return s.refuseClaude(principal, req, nil, execution.Fail("INVALID_INPUT", "allow_missing_user_message does not apply to a claudecode session"))
	case s.Claude == nil:
		return s.refuseClaude(principal, req, nil, execution.Fail("INVALID_INPUT", "claudecode sessions are not enabled: no Claude Code adapter is running"))
	}
	ev, err := s.Claude.ReleaseGuard(ctx, req.SessionID, req.ExecutionID, claudecode.ReleaseOptions{Settle: s.Settle})
	if err != nil {
		return s.refuseClaude(principal, req, &ev, err)
	}
	s.auditClaude(principal, req, &ev, nil)
	return Response{Outcome: "released", SessionID: req.SessionID, ExecutionID: req.ExecutionID, ClaudeEvidence: &ev}
}

func (s *Server) refuseClaude(principal string, req Request, ev *claudecode.ReleaseEvidence, err error) Response {
	e := typed(err)
	s.auditClaude(principal, req, ev, e)
	r := Response{Outcome: "refused", Error: e}
	if claudeSession(req.SessionID) {
		r.SessionID = req.SessionID
	}
	if executionPattern.MatchString(req.ExecutionID) {
		r.ExecutionID = req.ExecutionID
	}
	if ev != nil && ev.Harness != "" {
		r.ClaudeEvidence = ev
	}
	return r
}

// auditClaude writes the one AUDIT-01 guard_release line of a Claude release: validated values
// only, the harness, the child and group facts, no native-read keys, no free text.
func (s *Server) auditClaude(principal string, req Request, ev *claudecode.ReleaseEvidence, e *execution.Error) {
	outcome := "released"
	if e != nil {
		outcome = "refused"
	}
	attrs := []any{"event", "guard_release", "tool", "admin", "harness", "claudecode", "outcome", outcome, "accepted", e == nil}
	if e != nil {
		attrs = append(attrs, "error_code", e.Code)
	}
	if claudeSession(req.SessionID) {
		attrs = append(attrs, "session_id", req.SessionID)
	}
	if executionPattern.MatchString(req.ExecutionID) {
		attrs = append(attrs, "execution_id", req.ExecutionID)
	}
	if ev != nil && ev.Harness != "" {
		attrs = append(attrs, "child_exited", ev.ChildExited, "group_empty", ev.GroupEmpty, "rechecked", ev.Rechecked)
		if ev.Row != "" {
			attrs = append(attrs, "row", ev.Row)
		}
		if ev.ExitClass != "" {
			attrs = append(attrs, "exit_class", ev.ExitClass)
		}
		if ev.SettleMS > 0 {
			attrs = append(attrs, "settle_ms", ev.SettleMS)
		}
	}
	if ValidTicket(req.Ticket) {
		attrs = append(attrs, "ticket", req.Ticket)
	}
	attrs = append(attrs, "principal", principal)
	s.logger().Info("guard release", attrs...)
}

func nativeSession(id string) (string, bool) {
	native, ok := strings.CutPrefix(id, "opencode:")
	return native, ok && opencode.ValidID(native, "ses")
}

func typed(err error) *execution.Error {
	var e *execution.Error
	if errors.As(err, &e) {
		return e
	}
	return &execution.Error{Code: "INTERNAL", Message: "internal error"}
}

func (s *Server) refuse(principal string, req Request, ev *execution.ReleaseEvidence, err error) Response {
	e := typed(err)
	s.audit(principal, req, ev, e)
	r := Response{Outcome: "refused", Error: e}
	if _, ok := nativeSession(req.SessionID); ok {
		r.SessionID = req.SessionID
	}
	if executionPattern.MatchString(req.ExecutionID) {
		r.ExecutionID = req.ExecutionID
	}
	if ev != nil && ev.NativeRead {
		r.Evidence = ev
	}
	return r
}

// audit writes one AUDIT-01 event=guard_release line. Only validated values are
// logged; there is no free text and no prompt content.
func (s *Server) audit(principal string, req Request, ev *execution.ReleaseEvidence, e *execution.Error) {
	outcome := "released"
	if e != nil {
		outcome = "refused"
	}
	attrs := []any{"event", "guard_release", "tool", "admin", "outcome", outcome, "accepted", e == nil}
	if e != nil {
		attrs = append(attrs, "error_code", e.Code)
	}
	if _, ok := nativeSession(req.SessionID); ok {
		attrs = append(attrs, "session_id", req.SessionID)
	}
	if executionPattern.MatchString(req.ExecutionID) {
		attrs = append(attrs, "execution_id", req.ExecutionID)
	}
	if ev != nil && ev.NativeRead {
		attrs = append(attrs, "native_state", ev.NativeState, "unfinished", ev.Unfinished, "permission_pending", ev.PermissionPending, "correlated_user", ev.CorrelatedUser)
	}
	if ev != nil && ev.SettleMS > 0 {
		attrs = append(attrs, "settle_ms", ev.SettleMS)
	}
	if req.AllowMissingUserMessage {
		attrs = append(attrs, "allow_missing_user_message", true)
	}
	if ValidTicket(req.Ticket) {
		attrs = append(attrs, "ticket", req.Ticket)
	}
	attrs = append(attrs, "principal", principal)
	s.logger().Info("guard release", attrs...)
}

// Call sends one request to the admin socket and returns the decoded response.
func Call(ctx context.Context, socket string, req Request) (Response, error) {
	var resp Response
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return resp, fmt.Errorf("connect admin socket: %w", err)
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(90 * time.Second)
	}
	_ = conn.SetDeadline(deadline)
	b, err := json.Marshal(req)
	if err != nil {
		return resp, err
	}
	// The server may refuse a peer before reading its request and close the
	// connection; the refusal is still readable after a failed write.
	_, writeErr := conn.Write(append(b, '\n'))
	line, err := bufio.NewReader(io.LimitReader(conn, maxResponseSize)).ReadBytes('\n')
	if err != nil {
		if writeErr != nil {
			return resp, fmt.Errorf("send admin request: %w", writeErr)
		}
		return resp, fmt.Errorf("read admin response: %w", err)
	}
	if err = json.Unmarshal(line, &resp); err != nil {
		return resp, fmt.Errorf("decode admin response: %w", err)
	}
	return resp, nil
}
