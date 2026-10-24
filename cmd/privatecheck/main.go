// privatecheck performs read-only SDK inspection. Token input is stdin only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	bridge "github.com/nevzatcirak/harness-bridge/internal/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "private SDK verification failed:", err)
		os.Exit(1)
	}
}
func run() error {
	endpoint := flag.String("endpoint", "https://172.18.0.1:8443/mcp", "private HTTPS endpoint")
	origin := flag.String("origin", "https://bridge.nevzatcirak.com", "exact public origin")
	ca := flag.String("ca", "/etc/harness-bridge/tls/ca.crt", "public CA certificate file")
	id := flag.String("session", "", "expected native session ID")
	workspace := flag.String("workspace", "/opt/harness-bridge-test/workspace", "expected session workspace")
	flag.Parse()
	if *id == "" {
		return fmt.Errorf("explicit existing session required")
	}
	token, err := io.ReadAll(io.LimitReader(os.Stdin, 16385))
	if err != nil || len(token) > 16384 {
		return fmt.Errorf("invalid token input")
	}
	secret := strings.TrimSuffix(strings.TrimSuffix(string(token), "\n"), "\r")
	if len(secret) < 32 || strings.ContainsAny(secret, "\r\n\x00") {
		return fmt.Errorf("invalid token input")
	}
	caPEM, err := os.ReadFile(*ca)
	if err != nil {
		return fmt.Errorf("CA certificate unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := mcpclient.ConnectPrivateTLS(ctx, *endpoint, *origin, secret, caPEM)
	if err != nil {
		return fmt.Errorf("TLS/MCP initialize failed (details withheld)")
	}
	defer client.Close()
	if client.InitializeResult().ProtocolVersion != "2025-11-25" {
		return fmt.Errorf("unexpected protocol")
	}
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 7 {
		return fmt.Errorf("seven tools unavailable")
	}
	sessions, err := mcpclient.Call[bridge.Sessions](ctx, client, "list_sessions", bridge.EmptyInput{})
	if err != nil || len(sessions.Sessions) != 1 || sessions.Sessions[0].SessionID != "opencode:"+*id {
		return fmt.Errorf("session allowlist mismatch")
	}
	meta, err := mcpclient.Call[bridge.Session](ctx, client, "get_session", bridge.SessionInput{SessionID: "opencode:" + *id})
	if err != nil || meta.Directory != *workspace {
		return fmt.Errorf("workspace mismatch")
	}
	history, err := mcpclient.Call[bridge.History](ctx, client, "read_session_history", bridge.SessionInput{SessionID: "opencode:" + *id})
	if err != nil {
		return fmt.Errorf("history unavailable")
	}
	for _, name := range []string{"get_session", "read_session_history"} {
		result, e := client.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: bridge.SessionInput{SessionID: "opencode:ses_unauthorized_probe"}})
		if e != nil || !result.IsError {
			return fmt.Errorf("unauthorized session not denied")
		}
		b, e := json.Marshal(result.StructuredContent)
		if e != nil {
			return e
		}
		var denied execution.Error
		if json.Unmarshal(b, &denied) != nil || denied.Code != "FORBIDDEN" {
			return fmt.Errorf("unexpected authorization result")
		}
	}
	fmt.Printf("PASS: verified TLS/name, SDK initialize, seven tools, exact session/workspace, history count=%d, unauthorized reads denied; no mutation submitted\n", len(history.Messages))
	return nil
}
