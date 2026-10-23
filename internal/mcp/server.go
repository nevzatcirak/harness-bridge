package mcp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

type Server struct {
	Client *opencode.Client
	Runs   *execution.Manager
	Token  string
	Policy *security.Policy
}
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}
type args struct {
	SessionID      string `json:"session_id"`
	ExecutionID    string `json:"execution_id"`
	Prompt         string `json:"prompt"`
	IdempotencyKey string `json:"idempotency_key"`
}

func prop() map[string]any { return map[string]any{"type": "string"} }
func tool(name, desc string, required []string, properties map[string]any) map[string]any {
	return map[string]any{"name": name, "description": desc, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}
}
func tools() []map[string]any {
	return []map[string]any{
		tool("list_sessions", "List existing sessions", nil, map[string]any{}),
		tool("get_session", "Inspect session", []string{"session_id"}, map[string]any{"session_id": prop()}),
		tool("read_session_history", "Read existing session messages", []string{"session_id"}, map[string]any{"session_id": prop()}),
		tool("send_prompt", "Submit prompt to the same existing session", []string{"session_id", "prompt", "idempotency_key"}, map[string]any{"session_id": prop(), "prompt": prop(), "idempotency_key": prop()}),
		tool("get_execution_status", "Read execution state", []string{"execution_id"}, map[string]any{"execution_id": prop()}),
		tool("get_execution_result", "Read execution output", []string{"execution_id"}, map[string]any{"execution_id": prop()}),
		tool("interrupt_execution", "Request interruption", []string{"execution_id"}, map[string]any{"execution_id": prop()}),
	}
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
func sessionView(v opencode.Session) any {
	return map[string]any{"session_id": "opencode:" + v.ID, "native_id": v.ID, "harness": "opencode", "directory": v.Directory, "title": v.Title, "state": "unknown"}
}
func (s *Server) call(ctx context.Context, name string, a args) (any, error) {
	switch name {
	case "list_sessions":
		list := []any{}
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
		return map[string]any{"sessions": list}, nil
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
			return map[string]any{"messages": msgs}, e
		}
		x, e := s.Runs.SendWithRunner(ctx, c, id, a.Prompt, a.IdempotencyKey)
		slog.Info("prompt submission", "session_id", a.SessionID, "execution_id", x.ID, "accepted", e == nil)
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
			return map[string]any{"execution": x, "messages": msgs}, e
		}
		x, e = s.Runs.Interrupt(ctx, a.ExecutionID)
		slog.Info("explicit interrupt", "execution_id", x.ID, "accepted", e == nil)
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
	if s.Token == "" {
		http.Error(w, "server misconfigured", 500)
		return
	}
	header := r.Header.Get("Authorization")
	auth := strings.TrimPrefix(header, "Bearer ")
	if !strings.HasPrefix(header, "Bearer ") || len(auth) != len(s.Token) || subtle.ConstantTimeCompare([]byte(auth), []byte(s.Token)) != 1 {
		http.Error(w, "unauthorized", 401)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var q request
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	if q.JSONRPC != "2.0" {
		http.Error(w, "invalid jsonrpc", 400)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "trailing JSON", 400)
		return
	}
	out := response{JSONRPC: "2.0", ID: q.ID}
	switch q.Method {
	case "initialize":
		out.Result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "harness-bridge", "version": "0.1.0"}}
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	case "ping":
		out.Result = map[string]any{}
	case "tools/list":
		out.Result = map[string]any{"tools": tools()}
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments args   `json:"arguments"`
		}
		decoder := json.NewDecoder(bytes.NewReader(q.Params))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			out.Error = map[string]any{"code": -32602, "message": "invalid arguments"}
			break
		}
		if err := validateArgs(p.Name, p.Arguments); err != nil {
			out.Result = map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": errorJSON(err)}}}
			break
		}
		result, err := s.call(r.Context(), p.Name, p.Arguments)
		if err != nil {
			out.Result = map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": errorJSON(err)}}}
		} else {
			b, _ := json.Marshal(result)
			out.Result = map[string]any{"content": []any{map[string]string{"type": "text", "text": string(b)}}}
		}
	default:
		out.Error = map[string]any{"code": -32601, "message": "method not found"}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
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

func errorJSON(err error) string { b, _ := json.Marshal(envelope(err)); return string(b) }
