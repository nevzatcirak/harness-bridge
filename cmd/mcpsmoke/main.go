// smoke requires an explicitly selected existing session; it never creates servers or sessions.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == "session_id" || a.Key == "execution_id" {
			a.Value = slog.StringValue("[redacted]")
		}
		return a
	}})))
	id := os.Getenv("H2_SESSION_ID")
	directory := os.Getenv("H2_DIRECTORY")
	base := os.Getenv("OPENCODE_URL")
	if !opencode.ValidID(id, "ses") || directory == "" || base == "" {
		return fmt.Errorf("OPENCODE_URL, H2_SESSION_ID (existing native ID), H2_DIRECTORY required")
	}
	p, e := security.New([]string{directory}, []string{id})
	if e != nil {
		return e
	}
	c, e := opencode.New(base, os.Getenv("OPENCODE_SERVER_USERNAME"), os.Getenv("OPENCODE_SERVER_PASSWORD"))
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	session, e := c.Get(ctx, id)
	if e != nil {
		return e
	}
	if session.ID != id || session.Directory != directory || !p.Allows(id, session.Directory) {
		return fmt.Errorf("existing session identity/directory mismatch")
	}
	c = c.WithDirectory(session.Directory)
	discovered, e := c.List(ctx)
	if e != nil {
		return e
	}
	found := false
	for _, s := range discovered {
		if s.ID == id {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("target absent from discovery")
	}
	before, e := c.History(ctx, id)
	if e != nil {
		return e
	}
	if len(before) == 0 {
		return fmt.Errorf("existing session has no conversation history; seed a turn before running smoke")
	}
	statuses, e := c.Status(ctx)
	if e != nil {
		return e
	}
	if status, ok := statuses[id]; ok && status.Type != "idle" {
		return fmt.Errorf("existing session is busy")
	}
	permissions, e := c.Permissions(ctx)
	if e != nil {
		return e
	}
	for _, permission := range permissions {
		if permission.SessionID == id {
			return fmt.Errorf("existing session awaits permission")
		}
	}
	for _, msg := range before {
		if msg.Info.Role == "assistant" && msg.Info.Time.Completed == 0 {
			return fmt.Errorf("unfinished native assistant")
		}
	}
	fmt.Println("native idle preflight verified; no pending permissions or unfinished assistant")
	fmt.Printf("existing session selected; native ID and directory redacted; history before prompts: %d messages\n", len(before))
	var nonce [8]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return e
	}
	marker := "H2-" + hex.EncodeToString(nonce[:])
	endpoint := os.Getenv("H2_MCP_URL")
	token := os.Getenv("BRIDGE_TOKEN")
	if endpoint == "" {
		// Default: an ephemeral loopback bridge; never an OpenCode server.
		m := execution.New(c)
		defer m.Close()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		defer listener.Close()
		var secret [32]byte
		if _, err = rand.Read(secret[:]); err != nil {
			return err
		}
		token = hex.EncodeToString(secret[:])
		server := &http.Server{Handler: &mcp.Server{Client: c, Runs: m, Policy: p, Token: token}, ReadHeaderTimeout: 5 * time.Second}
		defer server.Close()
		go func() { _ = server.Serve(listener) }()
		endpoint = "http://" + listener.Addr().String() + "/mcp"
	}
	client, e := mcpclient.Connect(ctx, endpoint, token)
	if e != nil {
		return e
	}
	defer client.Close()
	if client.InitializeResult().ProtocolVersion != "2025-11-25" {
		return fmt.Errorf("unexpected protocol")
	}
	tools, e := client.ListTools(ctx, nil)
	if e != nil || len(tools.Tools) != 7 {
		return fmt.Errorf("seven tools not available: %v", e)
	}
	for _, tool := range tools.Tools {
		if tool.InputSchema == nil || tool.OutputSchema == nil {
			return fmt.Errorf("tool missing schema")
		}
	}
	sessions, e := mcpclient.Call[mcp.Sessions](ctx, client, "list_sessions", mcp.EmptyInput{})
	if e != nil || len(sessions.Sessions) != 1 || sessions.Sessions[0].SessionID != "opencode:"+id {
		return fmt.Errorf("MCP discovery mismatch: %v", e)
	}
	meta, e := mcpclient.Call[mcp.Session](ctx, client, "get_session", mcp.SessionInput{SessionID: "opencode:" + id})
	if e != nil || meta.Directory != directory {
		return fmt.Errorf("MCP metadata mismatch: %v", e)
	}
	prior, e := mcpclient.Call[mcp.History](ctx, client, "read_session_history", mcp.SessionInput{SessionID: "opencode:" + id})
	if e != nil || len(prior.Messages) != len(before) {
		return fmt.Errorf("MCP history mismatch: %v", e)
	}
	fmt.Println("official SDK initialize, tools/list, discovery, metadata and history verified")
	var executions []execution.Execution
	prompts := []string{"Continuity smoke test. Do not use tools or change files. Remember the exact marker " + marker + " and reply with only that marker.", "Continuity smoke test. Do not use tools or change files. Reply with only the exact marker from my immediately preceding prompt."}
	for turn, prompt := range prompts {
		x, e := mcpclient.Call[execution.Execution](ctx, client, "send_prompt", mcp.PromptInput{SessionID: "opencode:" + id, Prompt: prompt, IdempotencyKey: marker + fmt.Sprint(turn)})
		if e != nil {
			return e
		}
		duplicate, e := mcpclient.Call[execution.Execution](ctx, client, "send_prompt", mcp.PromptInput{SessionID: "opencode:" + id, Prompt: prompt, IdempotencyKey: marker + fmt.Sprint(turn)})
		if e != nil || duplicate.ID != x.ID {
			return fmt.Errorf("duplicate suppression failed")
		}
		if _, e = mcpclient.Call[execution.Execution](ctx, client, "send_prompt", mcp.PromptInput{SessionID: "opencode:" + id, Prompt: prompt + " changed", IdempotencyKey: marker + fmt.Sprint(turn)}); e == nil {
			return fmt.Errorf("idempotency conflict accepted")
		}
		for {
			x, e = mcpclient.Call[execution.Execution](ctx, client, "get_execution_status", mcp.ExecutionInput{ExecutionID: x.ID})
			if e != nil {
				return e
			}
			if x.State == "completed" {
				break
			}
			if x.State == "failed" || x.State == "interrupted" || x.State == "waiting_for_permission" {
				return fmt.Errorf("turn %d: %s", turn+1, x.State)
			}
			if time.Since(x.Started) > 5*time.Minute {
				return fmt.Errorf("turn %d unknown/timed out; do not resubmit blindly", turn+1)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
		result, e := mcpclient.Call[mcp.Result](ctx, client, "get_execution_result", mcp.ExecutionInput{ExecutionID: x.ID})
		msgs := result.Messages
		if e != nil {
			return e
		}
		output := ""
		for _, msg := range msgs {
			if msg.Info.Role == "assistant" {
				if msg.Info.ParentID != x.MessageID {
					return fmt.Errorf("wrong parent ID")
				}
				if msg.Info.Path.Cwd != directory {
					return fmt.Errorf("assistant cwd changed")
				}
				for _, raw := range msg.Parts {
					var part struct{ Type, Text string }
					_ = json.Unmarshal(raw, &part)
					if part.Type == "text" {
						output += part.Text
					}
				}
			}
		}
		if strings.TrimSpace(output) != marker {
			return fmt.Errorf("turn %d context marker mismatch", turn+1)
		}
		executions = append(executions, x)
		fmt.Printf("turn %d completed; assistant parentID, completion marker, context marker and cwd verified (IDs/content redacted)\n", turn+1)
	}
	finalHistory, e := mcpclient.Call[mcp.History](ctx, client, "read_session_history", mcp.SessionInput{SessionID: "opencode:" + id})
	after := finalHistory.Messages
	if e != nil {
		return e
	}
	ids := map[string]bool{}
	for _, msg := range after {
		ids[msg.Info.ID] = true
	}
	for _, msg := range before {
		if !ids[msg.Info.ID] {
			return fmt.Errorf("earlier history missing")
		}
	}
	for _, x := range executions {
		if !ids[x.MessageID] {
			return fmt.Errorf("prompt missing from final history")
		}
	}
	final, e := c.Get(ctx, id)
	if e != nil {
		return e
	}
	if final.ID != id || final.Directory != directory {
		return fmt.Errorf("session identity or directory changed")
	}
	if os.Getenv("H2_TEST_IDLE_EXPIRY") == "1" {
		fmt.Println("waiting 65 seconds without MCP requests to verify configured idle session expiry")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(65 * time.Second):
		}
		if _, err := client.ListTools(ctx, nil); err == nil {
			return fmt.Errorf("idle MCP session unexpectedly survived")
		}
		reconnected, err := mcpclient.Connect(ctx, endpoint, token)
		if err != nil {
			return err
		}
		defer reconnected.Close()
		history, err := mcpclient.Call[mcp.History](ctx, reconnected, "read_session_history", mcp.SessionInput{SessionID: "opencode:" + id})
		if err != nil {
			return err
		}
		if len(history.Messages) != len(after) {
			return fmt.Errorf("history changed during idle verification")
		}
		for i, msg := range after {
			if history.Messages[i].Info.ID != msg.Info.ID {
				return fmt.Errorf("history identity changed during idle verification")
			}
		}
		for _, x := range executions {
			snapshot, err := mcpclient.Call[execution.Execution](ctx, reconnected, "get_execution_status", mcp.ExecutionInput{ExecutionID: x.ID})
			if err != nil || snapshot.State != "completed" {
				return fmt.Errorf("execution lost after MCP idle expiry")
			}
		}
		fmt.Println("idle MCP session expired; official SDK reconnected; native history and completed executions retained; no prompt resubmission")
	}
	fmt.Printf("PASS: same existing native session; earlier history retained; both turns present; directory unchanged; final history: %d messages\n", len(after))
	return nil
}
