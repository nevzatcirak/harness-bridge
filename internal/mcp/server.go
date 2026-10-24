package mcp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// Server configuration and its policy must remain immutable while serving.
type Server struct {
	Client *opencode.Client
	Runs   *execution.Manager
	Token  string
	// ReadOnly denies mutations while a staged ingress shares a native session.
	ReadOnly bool
	// Remote binds one expiring credential to this server instance and ACL.
	Remote  *security.Remote
	Policy  *security.Policy
	once    sync.Once
	handler http.Handler
}
type args struct {
	SessionID      string `json:"session_id"`
	ExecutionID    string `json:"execution_id"`
	Prompt         string `json:"prompt"`
	IdempotencyKey string `json:"idempotency_key"`
}

func native(s string) (string, error) {
	id := strings.TrimPrefix(s, "opencode:")
	if !strings.HasPrefix(s, "opencode:") || !opencode.ValidID(id, "ses") {
		return "", execution.Fail("INVALID_INPUT", "invalid session_id")
	}
	return id, nil
}
func (s *Server) authorized(ctx context.Context, id string) (opencode.Session, *opencode.Client, error) {
	if s.Policy == nil || !s.Policy.Sessions[id] {
		return opencode.Session{}, nil, execution.Fail("FORBIDDEN", "session not authorized")
	}
	v, e := s.Client.Get(ctx, id)
	if e != nil {
		return v, nil, e
	}
	if v.ID != id || !s.Policy.Allows(id, v.Directory) {
		return v, nil, execution.Fail("FORBIDDEN", "workspace not authorized")
	}
	return v, s.Client.WithDirectory(v.Directory), nil
}
func sessionView(v opencode.Session) Session {
	return Session{SessionID: "opencode:" + v.ID, NativeID: v.ID, Harness: "opencode", Directory: v.Directory, Title: v.Title, State: "unknown"}
}
func (s *Server) call(ctx context.Context, name string, a args) (any, error) {
	if s.ReadOnly && (name == "send_prompt" || name == "interrupt_execution") {
		slog.Info("read-only mutation denied")
		return nil, execution.Fail("FORBIDDEN", "mutations disabled on this bridge")
	}
	switch name {
	case "list_sessions":
		list := []Session{}
		// Explicit ACL lookup also discovers sessions outside the server process cwd.
		if s.Policy != nil {
			for id := range s.Policy.Sessions {
				v, _, e := s.authorized(ctx, id)
				if e != nil {
					var denied *execution.Error
					if errors.As(e, &denied) && denied.Code == "FORBIDDEN" {
						continue
					}
					return nil, e
				}
				list = append(list, sessionView(v))
			}
		}
		return Sessions{Sessions: list}, nil
	case "get_session", "read_session_history", "send_prompt":
		id, e := native(a.SessionID)
		if e != nil {
			return nil, e
		}
		v, c, e := s.authorized(ctx, id)
		if e != nil {
			return nil, e
		}
		if name == "get_session" {
			return sessionView(v), nil
		}
		if name == "read_session_history" {
			msgs, e := c.History(ctx, id)
			return History{Messages: nonnil(msgs)}, e
		}
		x, e := s.Runs.SendWithRunner(ctx, c, id, a.Prompt, a.IdempotencyKey)
		slog.Info("prompt submission", "accepted", e == nil)
		return x, e
	case "get_execution_status", "get_execution_result", "interrupt_execution":
		x, e := s.Runs.Get(ctx, a.ExecutionID)
		if e != nil {
			return nil, e
		}
		id, e := native(x.SessionID)
		if e != nil {
			return nil, e
		}
		if _, _, e = s.authorized(ctx, id); e != nil {
			return nil, e
		}
		if name == "get_execution_status" {
			return x, nil
		}
		if name == "get_execution_result" {
			x, msgs, e := s.Runs.Result(ctx, a.ExecutionID)
			return Result{Execution: x, Messages: nonnil(msgs)}, e
		}
		x, e = s.Runs.Interrupt(ctx, a.ExecutionID)
		slog.Info("explicit interrupt", "accepted", e == nil)
		return x, e
	default:
		return nil, execution.Fail("INVALID_INPUT", "unknown tool")
	}
}
func envelope(err error) *execution.Error {
	var e *execution.Error
	if errors.As(err, &e) {
		return e
	}
	var h *opencode.HTTPError
	if errors.As(err, &h) {
		code := "UPSTREAM_FAILURE"
		switch h.Status {
		case 401:
			code = "UNAUTHORIZED"
		case 403:
			code = "FORBIDDEN"
		case 404:
			code = "NOT_FOUND"
		}
		return &execution.Error{Code: code, Message: h.Error()}
	}
	return &execution.Error{Code: "UPSTREAM_FAILURE", Message: "upstream unavailable or request canceled"}
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Remote != nil && !s.Remote.Check(w, r) {
		return
	}
	if s.Token == "" {
		http.Error(w, "server misconfigured", 500)
		return
	}
	header := r.Header.Get("Authorization")
	auth := strings.TrimPrefix(header, "Bearer ")
	if len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(header, "Bearer ") || len(auth) != len(s.Token) || subtle.ConstantTimeCompare([]byte(auth), []byte(s.Token)) != 1 {
		if s.Remote != nil {
			s.Remote.AuditAuth(false)
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="harness-bridge"`)
		http.Error(w, "unauthorized", 401)
		return
	}
	if s.Remote != nil {
		s.Remote.AuditAuth(true)
	}
	// Keep H1's strict single-JSON and body-size boundary. Protocol decoding,
	// dispatch, schemas and sessions are exclusively the official SDK's work.
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			} else {
				http.Error(w, "invalid body", http.StatusBadRequest)
			}
			return
		}
		if !json.Valid(body) {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	s.once.Do(func() {
		server := sdk.NewServer(&sdk.Implementation{Name: "harness-bridge", Version: "0.2.0"}, &sdk.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}})
		register[EmptyInput, Sessions](server, s, "list_sessions", "List authorized existing sessions", func(EmptyInput) args { return args{} })
		register[SessionInput, Session](server, s, "get_session", "Inspect an authorized session", func(a SessionInput) args { return args{SessionID: a.SessionID} })
		register[SessionInput, History](server, s, "read_session_history", "Read existing session history", func(a SessionInput) args { return args{SessionID: a.SessionID} })
		register[PromptInput, execution.Execution](server, s, "send_prompt", "Submit once to the same existing session", func(a PromptInput) args {
			return args{SessionID: a.SessionID, Prompt: a.Prompt, IdempotencyKey: a.IdempotencyKey}
		})
		for _, name := range []string{"get_execution_status", "interrupt_execution"} {
			register[ExecutionInput, execution.Execution](server, s, name, name, func(a ExecutionInput) args { return args{ExecutionID: a.ExecutionID} })
		}
		register[ExecutionInput, Result](server, s, "get_execution_result", "Read correlated execution output", func(a ExecutionInput) args { return args{ExecutionID: a.ExecutionID} })
		// Remote.Check enforces the exact public Host before replacing the SDK
		// loopback-only Host rule; proxy headers never grant an exception.
		transport := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{DisableLocalhostProtection: s.Remote != nil, JSONResponse: true, SessionTimeout: time.Minute, MaxRequestBodyBytes: 1 << 20})
		s.handler = http.NewCrossOriginProtection().Handler(transport)
	})
	s.handler.ServeHTTP(w, r)
}

func validateArgs(name string, a args) error {
	allowed := map[string]bool{}
	switch name {
	case "list_sessions":
	case "get_session", "read_session_history":
		allowed["session"] = true
	case "send_prompt":
		allowed["session"] = true
		allowed["prompt"] = true
		allowed["key"] = true
	case "get_execution_status", "get_execution_result", "interrupt_execution":
		allowed["execution"] = true
	default:
		return execution.Fail("INVALID_INPUT", "unknown tool")
	}
	values := map[string]string{"session": a.SessionID, "execution": a.ExecutionID, "prompt": a.Prompt, "key": a.IdempotencyKey}
	for field, value := range values {
		if allowed[field] && strings.TrimSpace(value) == "" || !allowed[field] && value != "" {
			return execution.Fail("INVALID_INPUT", "missing or unexpected tool argument")
		}
	}
	return nil
}
