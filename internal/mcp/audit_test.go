package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// AUDIT-01: every mutating call outcome writes exactly one structured line with
// stable keys, and no prompt text or raw idempotency key ever reaches the log.

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLog routes the default slog logger to a JSON buffer for this test.
// Tests in this package do not run in parallel.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

var auditMessages = map[string]bool{"prompt submission": true, "explicit interrupt": true, "read-only mutation denied": true}

// auditLines returns the audit records written so far, in order.
func auditLines(t *testing.T, buf *syncBuffer) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		m := map[string]any{}
		if e := json.Unmarshal([]byte(line), &m); e != nil {
			t.Fatalf("non-JSON log line %q: %v", line, e)
		}
		if auditMessages[m["msg"].(string)] {
			out = append(out, m)
		}
	}
	return out
}

func idemOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// lastAudit asserts that exactly one new audit line appeared since seen and
// returns it.
func lastAudit(t *testing.T, buf *syncBuffer, seen *int) map[string]any {
	t.Helper()
	lines := auditLines(t, buf)
	if len(lines) != *seen+1 {
		t.Fatalf("want exactly one new audit line, got %d new: %v", len(lines)-*seen, lines[*seen:])
	}
	*seen = len(lines)
	return lines[len(lines)-1]
}

// want checks exact values; "" means the key must be absent.
func want(t *testing.T, line map[string]any, fields map[string]any) {
	t.Helper()
	for k, v := range fields {
		got, present := line[k]
		if v == "" {
			if present {
				t.Errorf("%s present = %v, want absent (%v)", k, got, line)
			}
			continue
		}
		if !present || got != v {
			t.Errorf("%s = %v, want %v (%v)", k, got, v, line)
		}
	}
}

func assertNoSecrets(t *testing.T, buf *syncBuffer, secrets ...string) {
	t.Helper()
	log := buf.String()
	for _, s := range secrets {
		if strings.Contains(log, s) {
			t.Fatalf("log contains forbidden value %q:\n%s", s, log)
		}
	}
}

func TestAuditPromptOutcomes(t *testing.T) {
	buf := captureLog(t)
	client, _, bridge, _ := integration(t, true)
	seen := 0
	const prompt = "AUDIT-SECRET-PROMPT-7f2c reply with the marker"
	const key = "audit-raw-key-3b9d1e"
	ctx := context.Background()

	x, e := mcpclient.Call[execution.Execution](ctx, client, "send_prompt", PromptInput{"opencode:ses_existing", prompt, key})
	if e != nil {
		t.Fatal(e)
	}
	want(t, lastAudit(t, buf, &seen), map[string]any{"msg": "prompt submission", "event": "prompt_submission", "tool": "send_prompt", "outcome": "new", "accepted": true, "session_id": "opencode:ses_existing", "execution_id": x.ID, "idem": idemOf(key), "principal": "local", "error_code": ""})
	awaitExecution(t, client, x.ID, "completed")

	// Same key and payload: the manager returns the existing execution.
	again, e := mcpclient.Call[execution.Execution](ctx, client, "send_prompt", PromptInput{"opencode:ses_existing", prompt, key})
	if e != nil || again.ID != x.ID {
		t.Fatal(again, e)
	}
	want(t, lastAudit(t, buf, &seen), map[string]any{"outcome": "replay", "accepted": true, "execution_id": x.ID, "idem": idemOf(key), "error_code": ""})

	// Same key, different payload.
	invoke(t, client, "send_prompt", PromptInput{"opencode:ses_existing", prompt + " changed", key}, true)
	want(t, lastAudit(t, buf, &seen), map[string]any{"outcome": "rejected", "accepted": false, "error_code": "IDEMPOTENCY_CONFLICT", "session_id": "opencode:ses_existing", "idem": idemOf(key), "execution_id": ""})

	// Unauthorized session.
	invoke(t, client, "send_prompt", PromptInput{"opencode:ses_unauthorized", prompt, "audit-raw-key-unauth"}, true)
	want(t, lastAudit(t, buf, &seen), map[string]any{"outcome": "rejected", "error_code": "FORBIDDEN", "session_id": "opencode:ses_unauthorized", "execution_id": ""})

	// Schema-valid but rejected arguments are audited too.
	invoke(t, client, "send_prompt", PromptInput{"opencode:ses_existing", "   ", "audit-raw-key-blank"}, true)
	want(t, lastAudit(t, buf, &seen), map[string]any{"outcome": "rejected", "error_code": "INVALID_INPUT"})

	assertNoSecrets(t, buf, prompt, "AUDIT-SECRET-PROMPT", key, "audit-raw-key-unauth", "audit-raw-key-blank", "fixture response", bridge.Token)
}

func TestAuditBusyAndInterrupt(t *testing.T) {
	buf := captureLog(t)
	client, _, _, _ := integration(t, false)
	seen := 0
	ctx := context.Background()
	x, e := mcpclient.Call[execution.Execution](ctx, client, "send_prompt", PromptInput{"opencode:ses_existing", "first", "audit-busy-key-1"})
	if e != nil {
		t.Fatal(e)
	}
	want(t, lastAudit(t, buf, &seen), map[string]any{"outcome": "new", "execution_id": x.ID})
	awaitExecution(t, client, x.ID, "running")

	invoke(t, client, "send_prompt", PromptInput{"opencode:ses_existing", "second", "audit-busy-key-2"}, true)
	want(t, lastAudit(t, buf, &seen), map[string]any{"event": "prompt_submission", "outcome": "rejected", "error_code": "SESSION_BUSY", "session_id": "opencode:ses_existing", "idem": idemOf("audit-busy-key-2"), "execution_id": ""})

	invoke(t, client, "interrupt_execution", ExecutionInput{x.ID}, false)
	want(t, lastAudit(t, buf, &seen), map[string]any{"msg": "explicit interrupt", "event": "interrupt", "tool": "interrupt_execution", "outcome": "accepted", "accepted": true, "execution_id": x.ID, "session_id": "opencode:ses_existing", "principal": "local", "error_code": "", "idem": ""})
	awaitExecution(t, client, x.ID, "interrupted")

	invoke(t, client, "interrupt_execution", ExecutionInput{x.ID}, true)
	want(t, lastAudit(t, buf, &seen), map[string]any{"event": "interrupt", "outcome": "rejected", "accepted": false, "error_code": "INVALID_INPUT", "execution_id": x.ID})

	unknown := strings.Repeat("ab", 16)
	invoke(t, client, "interrupt_execution", ExecutionInput{unknown}, true)
	want(t, lastAudit(t, buf, &seen), map[string]any{"event": "interrupt", "outcome": "rejected", "error_code": "NOT_FOUND", "execution_id": unknown, "session_id": ""})

	// Arbitrary client input is never echoed as an execution ID.
	invoke(t, client, "interrupt_execution", ExecutionInput{"INJECT\nfake=line"}, true)
	want(t, lastAudit(t, buf, &seen), map[string]any{"event": "interrupt", "outcome": "rejected", "error_code": "NOT_FOUND", "execution_id": ""})
	assertNoSecrets(t, buf, "INJECT", "audit-busy-key-1", "audit-busy-key-2")
}

func TestAuditReadOnlyDenial(t *testing.T) {
	buf := captureLog(t)
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
	seen := len(auditLines(t, buf))
	invoke(t, client, "send_prompt", PromptInput{"opencode:ses_existing", "AUDIT-RO-PROMPT", "audit-ro-key-1"}, true)
	want(t, lastAudit(t, buf, &seen), map[string]any{"msg": "read-only mutation denied", "tool": "send_prompt", "event": "prompt_submission", "outcome": "rejected", "error_code": "FORBIDDEN", "session_id": "opencode:ses_existing", "idem": idemOf("audit-ro-key-1"), "principal": "local"})
	invoke(t, client, "interrupt_execution", ExecutionInput{strings.Repeat("cd", 16)}, true)
	want(t, lastAudit(t, buf, &seen), map[string]any{"msg": "read-only mutation denied", "tool": "interrupt_execution", "event": "interrupt", "outcome": "rejected", "error_code": "FORBIDDEN"})
	assertNoSecrets(t, buf, "AUDIT-RO-PROMPT", "audit-ro-key-1")
}

func TestAuditRemotePrincipal(t *testing.T) {
	buf := captureLog(t)
	client, _, _, _ := integrationMode(t, true, true)
	seen := len(auditLines(t, buf))
	x, e := mcpclient.Call[execution.Execution](context.Background(), client, "send_prompt", PromptInput{"opencode:ses_existing", "remote", "audit-remote-key"})
	if e != nil {
		t.Fatal(e)
	}
	want(t, lastAudit(t, buf, &seen), map[string]any{"outcome": "new", "execution_id": x.ID, "principal": "test-operator"})
}

// Read-only tools never write audit lines.
func TestAuditReadToolsSilent(t *testing.T) {
	buf := captureLog(t)
	client, _, _, _ := integration(t, true)
	invoke(t, client, "get_session", SessionInput{"opencode:ses_existing"}, false)
	invoke(t, client, "read_session_history", SessionInput{"opencode:ses_existing"}, false)
	invoke(t, client, "get_execution_status", ExecutionInput{strings.Repeat("ef", 16)}, true)
	if lines := auditLines(t, buf); len(lines) != 0 {
		t.Fatal(lines)
	}
}

// AUDIT-01b (R4-PR23-REVIEW P2-1): calls the SDK rejects against the input
// schema never reach the handler; the receiving middleware writes exactly one
// rejected line, logging only format-valid IDs and the hashed string key.
func TestAuditSchemaRejectedMutations(t *testing.T) {
	buf := captureLog(t)
	client, f, _, _ := integration(t, true)
	seen := 0
	const prompt = "AUDIT-SCHEMA-SECRET-PROMPT-91d"
	for _, tc := range []struct {
		name   string
		tool   string
		args   map[string]any
		fields map[string]any
	}{
		{"session_id_pattern", "send_prompt", map[string]any{"session_id": "ses_existing", "prompt": prompt, "idempotency_key": "audit-schema-key-1"},
			map[string]any{"session_id": "", "idem": idemOf("audit-schema-key-1")}},
		{"idempotency_key_min_length", "send_prompt", map[string]any{"session_id": "opencode:ses_existing", "prompt": prompt, "idempotency_key": "k7shrt"},
			map[string]any{"session_id": "opencode:ses_existing", "idem": idemOf("k7shrt")}},
		{"prompt_min_length", "send_prompt", map[string]any{"session_id": "opencode:ses_existing", "prompt": "", "idempotency_key": "audit-schema-key-3"},
			map[string]any{"session_id": "opencode:ses_existing", "idem": idemOf("audit-schema-key-3")}},
		{"execution_id_min_length", "interrupt_execution", map[string]any{"execution_id": ""},
			map[string]any{"event": "interrupt", "execution_id": "", "session_id": "", "idem": ""}},
		{"non_string_key_not_hashed", "send_prompt", map[string]any{"session_id": "opencode:ses_existing", "prompt": prompt, "idempotency_key": 12345678},
			map[string]any{"idem": ""}},
		{"injected_execution_id", "interrupt_execution", map[string]any{"execution_id": "INJECT\nfake=line", "extra": true},
			map[string]any{"execution_id": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invoke(t, client, tc.tool, tc.args, true)
			line := lastAudit(t, buf, &seen)
			want(t, line, map[string]any{"tool": tc.tool, "outcome": "rejected", "accepted": false, "error_code": "INVALID_INPUT", "principal": "local"})
			want(t, line, tc.fields)
		})
	}
	assertNoSecrets(t, buf, prompt, "audit-schema-key-1", "k7shrt", "audit-schema-key-3", "12345678", "INJECT")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 || f.aborts != 0 {
		t.Fatal("schema-rejected call reached native execution")
	}
}
