package mcp

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

type nativeFixture struct {
	mu              sync.Mutex
	root            string
	messages        []opencode.Message
	posts, aborts   int
	finish          bool
	loseReply       bool
	block, canceled chan struct{}
}

func (f *nativeFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/session/ses_blocked" {
		close(f.block)
		<-r.Context().Done()
		close(f.canceled)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/session/ses_existing" && r.URL.Query().Get("directory") != f.root {
		http.Error(w, "directory lost", 400)
		return
	}
	switch r.URL.Path {
	case "/session/ses_existing":
		_ = json.NewEncoder(w).Encode(opencode.Session{ID: "ses_existing", Directory: f.root, Title: "existing fixture"})
	case "/session/status":
		status := "idle"
		if len(f.messages) > 1 && !f.finish {
			status = "busy"
		}
		_ = json.NewEncoder(w).Encode(map[string]opencode.Status{"ses_existing": {Type: status}})
	case "/permission":
		_, _ = w.Write([]byte(`[]`))
	case "/session/ses_existing/message":
		_ = json.NewEncoder(w).Encode(f.messages)
	case "/session/ses_existing/prompt_async":
		var in struct {
			MessageID string `json:"messageID"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, "bad prompt", 400)
			return
		}
		f.posts++
		user := opencode.Message{Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"fixture prompt"}`)}}
		user.Info.ID = in.MessageID
		user.Info.SessionID = "ses_existing"
		user.Info.Role = "user"
		assistant := opencode.Message{Parts: []json.RawMessage{json.RawMessage(`{"type":"text","text":"fixture response"}`)}}
		assistant.Info.ID = fmt.Sprint("msg_assistant", f.posts)
		assistant.Info.ParentID = in.MessageID
		assistant.Info.SessionID = "ses_existing"
		assistant.Info.Role = "assistant"
		assistant.Info.Path.Cwd = f.root
		if f.finish {
			assistant.Info.Time.Completed = 1
			assistant.Info.Finish = "stop"
		}
		f.messages = append(f.messages, user, assistant)
		if f.loseReply {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.WriteHeader(204)
	case "/session/ses_existing/abort":
		f.aborts++
		f.finish = true
		a := &f.messages[len(f.messages)-1]
		a.Info.Time.Completed = 1
		a.Info.Error = json.RawMessage(`{"name":"MessageAbortedError"}`)
		_, _ = w.Write([]byte(`true`))
	default:
		http.Error(w, "unexpected endpoint", 404)
	}
}
func integration(t *testing.T, finish bool) (*sdk.ClientSession, *nativeFixture, *Server, string) {
	return integrationMode(t, finish, false)
}
func integrationMode(t *testing.T, finish, remote bool) (*sdk.ClientSession, *nativeFixture, *Server, string) {
	t.Helper()
	f := &nativeFixture{root: t.TempDir(), finish: finish, block: make(chan struct{}), canceled: make(chan struct{}), messages: []opencode.Message{{Parts: []json.RawMessage{}}}}
	f.messages[0].Info.ID = "msg_seed"
	f.messages[0].Info.Role = "user"
	f.messages[0].Info.SessionID = "ses_existing"
	upstream := httptest.NewServer(f)
	t.Cleanup(upstream.Close)
	c, err := opencode.New(upstream.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := security.New([]string{f.root}, []string{"ses_existing", "ses_blocked"})
	if err != nil {
		t.Fatal(err)
	}
	runs := execution.NewWithTiming(c, time.Second*3, time.Millisecond*5)
	t.Cleanup(runs.Close)
	bridge := &Server{Client: c, Runs: runs, Policy: p, Token: strings.Repeat("t", 32)}
	httpServer := httptest.NewUnstartedServer(bridge)
	if remote {
		httpServer.StartTLS()
		bridge.Remote, err = security.NewRemote("https://example.com", "test-operator", time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	} else {
		httpServer.Start()
	}
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	var client *sdk.ClientSession
	if remote {
		caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: httpServer.Certificate().Raw})
		client, err = mcpclient.ConnectPrivateTLS(ctx, httpServer.URL+"/mcp", "https://example.com", bridge.Token, caPEM)
	} else {
		client, err = mcpclient.Connect(ctx, httpServer.URL, bridge.Token)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, f, bridge, httpServer.URL
}
func invoke(t *testing.T, c *sdk.ClientSession, name string, args any, wantError bool) *sdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()
	r, e := c.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		t.Fatal(e)
	}
	if r.IsError != wantError {
		t.Fatalf("%s error=%v content=%v", name, r.IsError, r.Content)
	}
	return r
}
func decode[T any](t *testing.T, r *sdk.CallToolResult) T {
	t.Helper()
	var v T
	b, e := json.Marshal(r.StructuredContent)
	if e == nil {
		e = json.Unmarshal(b, &v)
	}
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func awaitExecution(t *testing.T, c *sdk.ClientSession, id, state string) execution.Execution {
	t.Helper()
	deadline := time.Now().Add(time.Second * 2)
	for time.Now().Before(deadline) {
		x := decode[execution.Execution](t, invoke(t, c, "get_execution_status", ExecutionInput{id}, false))
		if x.State == state {
			return x
		}
		time.Sleep(time.Millisecond * 5)
	}
	t.Fatalf("execution did not reach %s", state)
	return execution.Execution{}
}

func TestSDKLocalIntegration(t *testing.T) { testSDKContinuation(t, false) }
func TestSDKHTTPSIntegration(t *testing.T) { testSDKContinuation(t, true) }
func testSDKContinuation(t *testing.T, remote bool) {
	client, f, bridge, _ := integrationMode(t, true, remote)
	if v := client.InitializeResult(); v.ProtocolVersion != "2025-11-25" || v.ServerInfo.Name != "harness-bridge" {
		t.Fatal(v)
	}
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 7 {
		t.Fatal(tools, err)
	}
	schemas := map[string]*jsonschema.Resolved{}
	for _, tool := range tools.Tools {
		b, _ := json.Marshal(tool.InputSchema)
		var input jsonschema.Schema
		_ = json.Unmarshal(b, &input)
		if input.AdditionalProperties == nil || input.AdditionalProperties.Not == nil {
			t.Fatal("non-strict input", tool.Name)
		}
		b, _ = json.Marshal(tool.OutputSchema)
		var output jsonschema.Schema
		if e := json.Unmarshal(b, &output); e != nil {
			t.Fatal(e)
		}
		resolved, e := output.Resolve(nil)
		if e != nil {
			t.Fatal(e)
		}
		schemas[tool.Name] = resolved
	}
	check := func(name string, args any) *sdk.CallToolResult {
		r := invoke(t, client, name, args, false)
		b, _ := json.Marshal(r.StructuredContent)
		var v any
		_ = json.Unmarshal(b, &v)
		if e := schemas[name].Validate(v); e != nil {
			t.Fatal(name, e)
		}
		if len(r.Content) == 0 {
			t.Fatal("missing compatibility text")
		}
		return r
	}
	// Avoid the deliberately blocked ACL entry during discovery in this test.
	delete(bridge.Policy.Sessions, "ses_blocked")
	sessions := decode[Sessions](t, check("list_sessions", EmptyInput{}))
	if len(sessions.Sessions) != 1 {
		t.Fatal(sessions)
	}
	check("get_session", SessionInput{"opencode:ses_existing"})
	for turn := 0; turn < 2; turn++ {
		before := decode[History](t, check("read_session_history", SessionInput{"opencode:ses_existing"}))
		if len(before.Messages) != 1+turn*2 {
			t.Fatal("seed/history lost")
		}
		input := PromptInput{"opencode:ses_existing", "fixture prompt", fmt.Sprint("test-key-", turn)}
		x := decode[execution.Execution](t, check("send_prompt", input))
		duplicate := decode[execution.Execution](t, check("send_prompt", input))
		if duplicate.ID != x.ID {
			t.Fatal("duplicate")
		}
		input.Prompt = "changed"
		conflict := decode[execution.Error](t, invoke(t, client, "send_prompt", input, true))
		if conflict.Code != "IDEMPOTENCY_CONFLICT" {
			t.Fatal(conflict)
		}
		awaitExecution(t, client, x.ID, "completed")
		result := decode[Result](t, check("get_execution_result", ExecutionInput{x.ID}))
		if len(result.Messages) != 2 || result.Messages[1].Info.ParentID != x.MessageID || result.Messages[1].Info.Path.Cwd != f.root {
			t.Fatal("correlation/directory lost")
		}
	}
	f.mu.Lock()
	posts := f.posts
	f.mu.Unlock()
	if posts != 2 {
		t.Fatal(posts)
	}
	for _, name := range []string{"get_session", "read_session_history", "send_prompt"} {
		a := map[string]any{"session_id": "opencode:ses_denied"}
		if name == "send_prompt" {
			a["prompt"] = "test"
			a["idempotency_key"] = "denied-key"
		}
		failure := invoke(t, client, name, a, true)
		b, _ := json.Marshal(failure.StructuredContent)
		var v any
		_ = json.Unmarshal(b, &v)
		if err := schemas[name].Validate(v); err != nil {
			t.Fatal(err)
		}
		e := decode[execution.Error](t, failure)
		if e.Code != "FORBIDDEN" {
			t.Fatal(e)
		}
	}
	e := decode[execution.Error](t, invoke(t, client, "get_execution_result", ExecutionInput{"missing"}, true))
	if e.Code != "NOT_FOUND" {
		t.Fatal(e)
	}
	for _, a := range []any{map[string]any{}, map[string]any{"extra": true}, map[string]any{"session_id": 5}, map[string]any{"session_id": "ses_missing_prefix"}, map[string]any{"session_id": "opencode:ses_existing", "prompt": "test", "idempotency_key": "short"}, map[string]any{"session_id": "opencode:ses_existing", "prompt": strings.Repeat("x", 32769), "idempotency_key": "valid-key"}} {
		invoke(t, client, "send_prompt", a, true)
	}
	_, err = client.CallTool(context.Background(), &sdk.CallToolParams{Name: "unknown_tool", Arguments: EmptyInput{}})
	if err == nil {
		t.Fatal("unknown tool accepted")
	}
}
func TestSDKCancellationAndInterrupt(t *testing.T) {
	client, f, _, _ := integration(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, e := client.CallTool(ctx, &sdk.CallToolParams{Name: "send_prompt", Arguments: PromptInput{"opencode:ses_blocked", "test", "blocked-key"}})
		done <- e
	}()
	select {
	case <-f.block:
	case <-time.After(time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancellation ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("client blocked")
	}
	select {
	case <-f.canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream context not canceled")
	}
	f.mu.Lock()
	posts := f.posts
	f.mu.Unlock()
	if posts != 0 {
		t.Fatal("canceled preflight submitted prompt")
	}
	ctx, cancel = context.WithCancel(context.Background())
	x, e := mcpclient.Call[execution.Execution](ctx, client, "send_prompt", PromptInput{"opencode:ses_existing", "test", "cancel-key"})
	if e != nil {
		t.Fatal(e)
	}
	cancel() // accepted work must survive its caller context
	awaitExecution(t, client, x.ID, "running")
	invoke(t, client, "send_prompt", PromptInput{"opencode:ses_existing", "test", "other-key"}, true)
	invoke(t, client, "interrupt_execution", ExecutionInput{x.ID}, false)
	awaitExecution(t, client, x.ID, "interrupted")
	result := decode[Result](t, invoke(t, client, "get_execution_result", ExecutionInput{x.ID}, false))
	if len(result.Messages) != 2 {
		t.Fatal(result)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 || f.aborts != 1 {
		t.Fatal(f.posts, f.aborts)
	}
}

func TestSDKHTTPGuards(t *testing.T) {
	client, _, bridge, endpoint := integration(t, true)
	for _, tc := range []struct {
		name, body, host, origin, auth string
		status                         int
	}{
		{name: "missing auth", body: `{}`, status: 401},
		{name: "malformed JSON", body: `{`, auth: bridge.Token, status: 400},
		{name: "trailing JSON", body: `{"jsonrpc":"2.0","id":9,"method":"ping"} {}`, auth: bridge.Token, status: 400},
		{name: "oversized", body: strings.Repeat(" ", 1<<20) + `{}`, auth: bridge.Token, status: 413},
		{name: "DNS rebinding", body: `{}`, host: "attacker.example", auth: bridge.Token, status: 403},
		{name: "cross origin", body: `{}`, origin: "http://attacker.example", auth: bridge.Token, status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", endpoint, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Mcp-Session-Id", client.ID())
			req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
			if tc.auth != "" {
				req.Header.Set("Authorization", "Bearer "+tc.auth)
			}
			if tc.host != "" {
				req.Host = tc.host
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
				req.Header.Set("Sec-Fetch-Site", "cross-site")
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.status {
				t.Fatalf("got %d want %d", res.StatusCode, tc.status)
			}
		})
	}
	// A valid MCP session ID is never a substitute for bearer authentication.
	for _, method := range []string{"GET", "DELETE"} {
		req, _ := http.NewRequest(method, endpoint, nil)
		req.Header.Set("Mcp-Session-Id", client.ID())
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 401 {
			t.Fatal(method, res.StatusCode)
		}
	}
}

func TestSDKExecutionAuthorization(t *testing.T) {
	client, _, bridge, _ := integration(t, false)
	x := decode[execution.Execution](t, invoke(t, client, "send_prompt", PromptInput{"opencode:ses_existing", "test", "guard-key"}, false))
	awaitExecution(t, client, x.ID, "running")
	// A second principal with a different root cannot read or abort a known run.
	policy, err := security.New([]string{t.TempDir()}, []string{"ses_existing"})
	if err != nil {
		t.Fatal(err)
	}
	deniedServer := httptest.NewServer(&Server{Client: bridge.Client, Runs: bridge.Runs, Token: bridge.Token, Policy: policy})
	defer deniedServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	denied, err := mcpclient.Connect(ctx, deniedServer.URL, bridge.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer denied.Close()
	for _, name := range []string{"get_execution_status", "get_execution_result", "interrupt_execution"} {
		failure := decode[execution.Error](t, invoke(t, denied, name, ExecutionInput{x.ID}, true))
		if failure.Code != "FORBIDDEN" {
			t.Fatal(failure)
		}
	}
	invoke(t, client, "interrupt_execution", ExecutionInput{x.ID}, false)
	awaitExecution(t, client, x.ID, "interrupted")
}

func TestSDKReadOnlyStaging(t *testing.T) {
	_, f, bridge, _ := integration(t, true)
	policy, err := security.New([]string{f.root}, []string{"ses_existing"})
	if err != nil {
		t.Fatal(err)
	}
	staged := &Server{Client: bridge.Client, Runs: bridge.Runs, Token: bridge.Token, Policy: policy, ReadOnly: true}
	ts := httptest.NewServer(staged)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mcpclient.Connect(ctx, ts.URL, staged.Token)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 7 {
		t.Fatal("tool surface changed", err)
	}
	for _, name := range []string{"get_session", "read_session_history"} {
		invoke(t, client, name, SessionInput{"opencode:ses_existing"}, false)
	}
	invoke(t, client, "list_sessions", EmptyInput{}, false)
	for _, tc := range []struct {
		name  string
		input any
	}{
		{"send_prompt", PromptInput{"opencode:ses_existing", "must not execute", "staged-denial"}},
		{"interrupt_execution", ExecutionInput{"exe_staged_denial"}},
	} {
		failure := decode[execution.Error](t, invoke(t, client, tc.name, tc.input, true))
		if failure.Code != "FORBIDDEN" {
			t.Fatal("mutation was not forbidden", tc.name)
		}
	}
	invoke(t, client, "get_session", SessionInput{"opencode:ses_unauthorized"}, true)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 || f.aborts != 0 {
		t.Fatal("read-only staging touched native execution")
	}
}

func TestSDKConcurrentSingleWriter(t *testing.T) {
	client, f, _, _ := integration(t, false)
	input := PromptInput{"opencode:ses_existing", "fixture prompt", "concurrent-key"}
	start := make(chan struct{})
	results := make(chan *sdk.CallToolResult, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() {
			<-start
			r, err := client.CallTool(context.Background(), &sdk.CallToolParams{Name: "send_prompt", Arguments: input})
			results <- r
			errs <- err
		}()
	}
	close(start)
	id := ""
	for i := 0; i < 12; i++ {
		r := <-results
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if r.IsError {
			t.Fatal(r)
		}
		x := decode[execution.Execution](t, r)
		if id != "" && x.ID != id {
			t.Fatal("duplicate execution")
		}
		id = x.ID
	}
	different := input
	different.IdempotencyKey = "another-key"
	if failure := decode[execution.Error](t, invoke(t, client, "send_prompt", different, true)); failure.Code != "SESSION_BUSY" {
		t.Fatal(failure)
	}
	different = input
	different.Prompt = "changed"
	if failure := decode[execution.Error](t, invoke(t, client, "send_prompt", different, true)); failure.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(failure)
	}
	awaitExecution(t, client, id, "running")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 {
		t.Fatal(f.posts)
	}
}

func TestSDKLostNativeReplyNeverResubmits(t *testing.T) {
	client, f, _, _ := integration(t, true)
	f.mu.Lock()
	f.loseReply = true
	f.mu.Unlock()
	input := PromptInput{"opencode:ses_existing", "fixture prompt", "lost-reply-key"}
	x := decode[execution.Execution](t, invoke(t, client, "send_prompt", input, false))
	awaitExecution(t, client, x.ID, "unknown")
	replay := decode[execution.Execution](t, invoke(t, client, "send_prompt", input, false))
	if replay.ID != x.ID || replay.State != "unknown" {
		t.Fatal(replay)
	}
	input.IdempotencyKey = "unsafe-new-key"
	failure := decode[execution.Error](t, invoke(t, client, "send_prompt", input, true))
	if failure.Code != "SESSION_BUSY" {
		t.Fatal(failure)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 {
		t.Fatal("native POST replayed", f.posts)
	}
}
