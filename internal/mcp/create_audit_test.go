package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/lifecycle"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

func callParams(name string, a map[string]any) *sdk.CallToolParams {
	return &sdk.CallToolParams{Name: name, Arguments: a}
}

// connect serves the env's Server over the real MCP transport.
func (e *createEnv) connect(t *testing.T) *sdk.ClientSession {
	t.Helper()
	pol, err := security.New([]string{e.rootA}, []string{"ses_none"})
	if err != nil {
		t.Fatal(err)
	}
	e.s.Policy = pol
	e.s.Token = strings.Repeat("t", 32)
	ts := httptest.NewServer(e.s)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := mcpclient.Connect(ctx, ts.URL, e.s.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// ADR-022 slice S5 (section 4.3): AUDIT-01 gets event=session_create, exactly one line per
// create_session call on every return path, with closed-shape fields and never the title, the
// subdirectory text, the raw key, the free root path or any native response text.

func keyHash16(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

// createLines returns the session_create lines written so far.
func createLines(t *testing.T, buf *syncBuffer) []map[string]any {
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
		if m["event"] == "session_create" {
			out = append(out, m)
		}
	}
	return out
}

// oneLine asserts that exactly one new session_create line appeared since *seen and returns it.
func oneLine(t *testing.T, buf *syncBuffer, seen *int) map[string]any {
	t.Helper()
	all := createLines(t, buf)
	if len(all) != *seen+1 {
		t.Fatalf("session_create lines: %d, want %d: %s", len(all), *seen+1, buf.String())
	}
	*seen = len(all)
	return all[len(all)-1]
}

func wantKV(t *testing.T, line map[string]any, kv map[string]any) {
	t.Helper()
	for k, v := range kv {
		if got, ok := line[k]; !ok || fmt.Sprint(got) != fmt.Sprint(v) {
			t.Errorf("%s = %v (present %v), want %v; line %v", k, got, ok, v, line)
		}
	}
	if line["tool"] != "create_session" || line["event"] != "session_create" || line["principal"] != "local" {
		t.Errorf("fixed fields: %v", line)
	}
}

func absentKeys(t *testing.T, line map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := line[k]; ok {
			t.Errorf("%s must be absent: %v", k, line)
		}
	}
}

// Every return path writes exactly one line, with the fields §4.3 names.
func TestCreateAuditOneLinePerReturnPath(t *testing.T) {
	buf := captureLog(t)
	e := newCreateEnv(t, nil)
	seen := 0
	const k1 = "key-audit-00001"

	// 1. input refusals (no state touched, no root resolved).
	_, err := e.s.call(context.Background(), "create_session", args{Root: e.rootA, Title: "bad\ttitle", IdempotencyKey: k1})
	if codeOf(err) != "INVALID_INPUT" {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"outcome": "rejected", "accepted": false, "error_code": "INVALID_INPUT", "key_hash": keyHash16(k1)})
	_, err = e.s.call(context.Background(), "create_session", args{Root: e.rootA, IdempotencyKey: "short"})
	if codeOf(err) != "INVALID_INPUT" {
		t.Fatal(err)
	}
	line := oneLine(t, buf, &seen)
	wantKV(t, line, map[string]any{"outcome": "rejected", "accepted": false, "error_code": "INVALID_INPUT"})
	absentKeys(t, line, "key_hash") // a short key is not hashed: its hash would reveal it

	// 2. root not configured (the free path is never logged), 3. subdirectory refusals.
	_, err = e.s.call(context.Background(), "create_session", args{Root: "/etc/not-a-root", IdempotencyKey: "key-audit-00002"})
	if codeOf(err) != "FORBIDDEN" {
		t.Fatal(err)
	}
	line = oneLine(t, buf, &seen)
	wantKV(t, line, map[string]any{"error_code": "FORBIDDEN", "accepted": false})
	absentKeys(t, line, "root_id")
	_, err = e.s.call(context.Background(), "create_session", args{Root: e.rootA, Subdirectory: "../up", IdempotencyKey: "key-audit-00003"})
	if codeOf(err) != "INVALID_INPUT" {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"error_code": "INVALID_INPUT"})
	_, err = e.s.call(context.Background(), "create_session", args{Root: e.rootA, Subdirectory: "deeper", IdempotencyKey: "key-audit-00004"})
	if codeOf(err) != "FORBIDDEN" { // rootA has max_depth 0
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"error_code": "FORBIDDEN", "root_id": 0})
	_, err = e.s.call(context.Background(), "create_session", args{Root: e.rootB, Subdirectory: "missing", IdempotencyKey: "key-audit-00005"})
	if codeOf(err) != "NOT_FOUND" {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"error_code": "NOT_FOUND", "root_id": 1, "accepted": false})

	// 4. success: counts after the call include the new session and this POST.
	const k2 = "key-audit-00006"
	out, err := e.s.call(context.Background(), "create_session", args{Root: e.rootA, Title: "first", IdempotencyKey: k2})
	if err != nil {
		t.Fatal(err)
	}
	created := out.(CreateOutput)
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"outcome": "created", "accepted": true, "root_id": 0, "session_id": created.SessionID,
		"key_hash": keyHash16(k2), "limit_state": "hour:1/5,root:1/3,total:1/5"})

	// 5. idempotent replay of the success, and a conflict.
	if _, err = e.s.call(context.Background(), "create_session", args{Root: e.rootA, Title: "first", IdempotencyKey: k2}); err != nil {
		t.Fatal(err)
	}
	line = oneLine(t, buf, &seen)
	wantKV(t, line, map[string]any{"outcome": "replay", "accepted": true, "session_id": created.SessionID, "key_hash": keyHash16(k2)})
	if _, err = e.s.call(context.Background(), "create_session", args{Root: e.rootA, Title: "other", IdempotencyKey: k2}); codeOf(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"outcome": "rejected", "accepted": false, "error_code": "IDEMPOTENCY_CONFLICT", "root_id": 0})

	// 6. read-only.
	e.s.ReadOnly = true
	if _, err = e.s.call(context.Background(), "create_session", args{Root: e.rootA, IdempotencyKey: "key-audit-00007"}); codeOf(err) != "FORBIDDEN" {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"error_code": "FORBIDDEN", "accepted": false})
	e.s.ReadOnly = false
}

// Caps, counts and native failures.
func TestCreateAuditCapsAndNativeFailures(t *testing.T) {
	buf := captureLog(t)
	e := newCreateEnv(t, nil)
	seen := 0
	ctx := context.Background()

	// max_sessions of the root reached: the line carries the counts it saw.
	e.listing[e.rootA] = []opencode.Session{{ID: "ses_a1", Directory: e.rootA}, {ID: "ses_a2", Directory: e.rootA}, {ID: "ses_a3", Directory: e.rootA}}
	if _, err := e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: "key-audit-10001"}); codeOf(err) != "SESSION_LIMIT" {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"error_code": "SESSION_LIMIT", "accepted": false, "root_id": 0, "limit_state": "hour:0/5,root:3/3,total:3/5"})
	e.listing[e.rootA] = nil

	// count that cannot be established (a failing page): cap state unknown.
	e.setListHook(func(w http.ResponseWriter, r *http.Request) bool { w.WriteHeader(500); return true })
	if _, err := e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: "key-audit-10002"}); codeOf(err) != "SESSION_LIMIT" {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"error_code": "SESSION_LIMIT", "limit_state": "hour:0/5,root:?/3,total:?/5"})

	// count timeout.
	createBound.Store(int64(200 * time.Millisecond))
	e.setListHook(func(w http.ResponseWriter, r *http.Request) bool {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		return true
	})
	if _, err := e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: "key-audit-10003"}); codeOf(err) != "UPSTREAM_TIMEOUT" {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"error_code": "UPSTREAM_TIMEOUT", "accepted": false, "limit_state": "hour:0/5,root:?/3,total:?/5"})
	e.setListHook(nil)
	createBound.Store(int64(5 * time.Second))

	// native 5xx on the POST: UPSTREAM_FAILURE (the POST was counted).
	e.setPostStatus(500)
	if _, err := e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: "key-audit-10004"}); codeOf(err) != "UPSTREAM_FAILURE" {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"error_code": "UPSTREAM_FAILURE", "accepted": false, "root_id": 0})
	e.setPostStatus(0)

	// hourly cap.
	e2 := newCreateEnv(t, func(c *lifecycle.Config) { c.CreatePerHour = 1 })
	seen2 := len(createLines(t, buf))
	e2.mustCreate(args{Root: e2.rootA, IdempotencyKey: "key-audit-10005"})
	seen2++
	if _, err := e2.s.call(ctx, "create_session", args{Root: e2.rootA, IdempotencyKey: "key-audit-10006"}); codeOf(err) != "SESSION_LIMIT" {
		t.Fatal(err)
	}
	if got := createLines(t, buf); len(got) != seen2+1 || got[seen2]["error_code"] != "SESSION_LIMIT" || got[seen2]["limit_state"] != "hour:1/1,root:?/3,total:?/5" {
		t.Fatalf("hourly cap line: %v", got)
	}
}

// Unknown outcomes, the bootstrap flag and the replay of a recorded error.
func TestCreateAuditUnknownOutcomeAndReplayedError(t *testing.T) {
	buf := captureLog(t)
	e := newCreateEnv(t, nil)
	seen := 0
	ctx := context.Background()

	// timeout on the POST: UNKNOWN_OUTCOME (never retried).
	createBound.Store(int64(200 * time.Millisecond))
	e.setPostHook(func(w http.ResponseWriter, r *http.Request, dir string) bool {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		return true
	})
	const k = "key-audit-20001"
	if _, err := e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: k}); codeOf(err) != "UNKNOWN_OUTCOME" {
		t.Fatal(err)
	}
	line := oneLine(t, buf, &seen)
	wantKV(t, line, map[string]any{"outcome": "rejected", "accepted": false, "error_code": "UNKNOWN_OUTCOME", "root_id": 0, "key_hash": keyHash16(k),
		"limit_state": "hour:1/5,root:?/3,total:?/5"}) // the POST may have created a session: the counts are not final
	absentKeys(t, line, "bootstrap_may_be_outside_root", "replay", "session_id")
	// the recorded outcome is returned for the same key: one more line, marked replay.
	if _, err := e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: k}); codeOf(err) != "UNKNOWN_OUTCOME" {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"outcome": "rejected", "error_code": "UNKNOWN_OUTCOME", "replay": true, "key_hash": keyHash16(k)})
	e.setPostHook(nil)
	createBound.Store(int64(5 * time.Second))

	// the native answer names another directory: bootstrap_may_be_outside_root=true, and the
	// (well formed) session ID is logged, the path is not.
	e.setPostHook(func(w http.ResponseWriter, r *http.Request, dir string) bool {
		_ = json.NewEncoder(w).Encode(opencode.Session{ID: "ses_elsewhere", Directory: "/somewhere/else/entirely"})
		return true
	})
	if _, err := e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: "key-audit-20002"}); codeOf(err) != "UNKNOWN_OUTCOME" {
		t.Fatal(err)
	}
	line = oneLine(t, buf, &seen)
	wantKV(t, line, map[string]any{"error_code": "UNKNOWN_OUTCOME", "bootstrap_may_be_outside_root": true, "session_id": "opencode:ses_elsewhere",
		"limit_state": "hour:2/5,root:?/3,total:?/5"}) // a session exists somewhere: the pre-POST counts are not claimed
	if strings.Contains(buf.String(), "/somewhere/else") {
		t.Fatal("the native directory reached the journal")
	}

	// an answer without a valid session ID: no session_id on the line.
	e.setPostHook(func(w http.ResponseWriter, r *http.Request, dir string) bool {
		_ = json.NewEncoder(w).Encode(opencode.Session{ID: "not a valid id\nevent=forged", Directory: dir})
		return true
	})
	if _, err := e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: "key-audit-20003"}); codeOf(err) != "UNKNOWN_OUTCOME" {
		t.Fatal(err)
	}
	line = oneLine(t, buf, &seen)
	absentKeys(t, line, "session_id")
	if strings.Contains(buf.String(), "forged") {
		t.Fatal("native text reached the journal")
	}
}

// The journal never holds the title, the subdirectory text, the raw key, the free root path
// or native response text, on success or on failure.
func TestCreateAuditNeverLogsFreeText(t *testing.T) {
	buf := captureLog(t)
	e := newCreateEnv(t, nil)
	sub := e.mkdirs(e.rootB, "secret-subdir-name")
	_ = sub
	const rawKey = "RAW-KEY-DO-NOT-LOG-1234"
	e.setPostHook(func(w http.ResponseWriter, r *http.Request, dir string) bool {
		_ = json.NewEncoder(w).Encode(opencode.Session{ID: "ses_ok1", Title: "NATIVE-TITLE-TEXT", Directory: dir})
		return true
	})
	for _, a := range []args{
		{Root: e.rootB, Subdirectory: "secret-subdir-name", Title: "SECRET-TITLE-xyz", IdempotencyKey: rawKey},
		{Root: e.rootB, Subdirectory: "secret-subdir-name", Title: "SECRET-TITLE-other", IdempotencyKey: rawKey}, // conflict
		{Root: e.rootB, Subdirectory: "secret-missing-sub", Title: "SECRET-TITLE-xyz", IdempotencyKey: rawKey + "b"},
	} {
		_, _ = e.s.call(context.Background(), "create_session", a)
	}
	if got := len(createLines(t, buf)); got != 3 {
		t.Fatalf("lines %d: %s", got, buf.String())
	}
	for _, bad := range []string{"SECRET-TITLE", "NATIVE-TITLE", "secret-subdir", "secret-missing", rawKey, e.rootA, e.rootB} {
		if strings.Contains(buf.String(), bad) {
			t.Errorf("the journal contains %q: %s", bad, buf.String())
		}
	}
}

// A panic is audited as INTERNAL, still exactly one line, the panic continues, and the
// idempotency record does not stay pending: a retry of the same key is a fresh request.
func TestCreateAuditPanicWritesOneLineAndReleasesTheRecord(t *testing.T) {
	buf := captureLog(t)
	e := newCreateEnv(t, nil)
	ctx := context.Background()
	const k = "key-audit-30001"
	good := e.cc.now
	e.cc.now = func() time.Time { panic("boom") }
	func() {
		defer func() {
			if p := recover(); p == nil {
				t.Error("the panic was swallowed")
			}
		}()
		_, _ = e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: k})
	}()
	seen := 0
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"outcome": "rejected", "accepted": false, "error_code": "INTERNAL", "key_hash": keyHash16(k)})
	// the same key is not stuck: it is a new request that succeeds.
	e.cc.now = good
	done := make(chan error, 1)
	go func() { _, err := e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: k}); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("retry after a panic: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the retry hangs on a pending record left by the panic")
	}
	if e.count("POST") != 1 {
		t.Fatalf("POSTs %d", e.count("POST"))
	}
}

// A panic after the POST was sent records the outcome as unknown: the same key is never
// POSTed again.
func TestCreateAuditPanicAfterThePostRecordsAnUnknownOutcome(t *testing.T) {
	buf := captureLog(t)
	e := newCreateEnv(t, nil)
	ctx := context.Background()
	const k = "key-audit-30002"
	createAfterPostHook = func() { panic("after the post") }
	defer func() { createAfterPostHook = nil }()
	func() {
		defer func() { _ = recover() }()
		_, _ = e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: k})
	}()
	seen := 0
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"error_code": "INTERNAL", "accepted": false})
	createAfterPostHook = nil
	if _, err := e.s.call(ctx, "create_session", args{Root: e.rootA, IdempotencyKey: k}); codeOf(err) != "UNKNOWN_OUTCOME" {
		t.Fatalf("a retry after a panic past the POST: %v", err)
	}
	if e.count("POST") != 1 {
		t.Fatalf("the key was POSTed again: %d", e.count("POST"))
	}
}

// Calls the SDK rejects against the input schema never reach the handler: the middleware
// writes their one line, and a call that reaches the handler is audited once (not twice).
func TestCreateAuditOverTheMCPTransport(t *testing.T) {
	buf := captureLog(t)
	e := newCreateEnv(t, nil)
	client := e.connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	seen := 0
	// schema-rejected: a title longer than 200 characters.
	r, err := client.CallTool(ctx, callParams("create_session", map[string]any{"root": e.rootA, "title": strings.Repeat("x", 201), "idempotency_key": "key-audit-40001"}))
	if err == nil && (r == nil || !r.IsError) {
		t.Fatal("accepted")
	}
	line := oneLine(t, buf, &seen)
	wantKV(t, line, map[string]any{"outcome": "rejected", "accepted": false, "error_code": "INVALID_INPUT"})
	if strings.Contains(buf.String(), "xxxxxxxx") {
		t.Fatal("the title reached the journal")
	}
	// handled by the tool: exactly one line.
	invoke(t, client, "create_session", map[string]any{"root": e.rootA, "title": "via mcp", "idempotency_key": "key-audit-40002"}, false)
	line = oneLine(t, buf, &seen)
	wantKV(t, line, map[string]any{"outcome": "created", "accepted": true, "root_id": 0})
}

// #281 P3-1: a same-key waiter whose context expires gets UPSTREAM_TIMEOUT and is NOT journaled
// replay=true: no recorded outcome was returned to it. The owner's own line is created.
func TestCreateAuditWaiterTimeoutIsNotAReplay(t *testing.T) {
	buf := captureLog(t)
	e := newCreateEnv(t, nil)
	release := make(chan struct{})
	e.setPostHook(func(w http.ResponseWriter, r *http.Request, dir string) bool {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return false // then the fake answers normally
	})
	const k = "key-audit-50001"
	ownerDone := make(chan error, 1)
	go func() {
		_, err := e.s.call(context.Background(), "create_session", args{Root: e.rootA, IdempotencyKey: k})
		ownerDone <- err
	}()
	for deadline := time.Now().Add(3 * time.Second); e.count("POST") == 0 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if e.count("POST") != 1 {
		t.Fatal("the owner's POST was not sent")
	}
	wctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := e.s.call(wctx, "create_session", args{Root: e.rootA, IdempotencyKey: k}); codeOf(err) != "UPSTREAM_TIMEOUT" {
		t.Fatalf("waiter: %v", err)
	}
	seen := 0
	line := oneLine(t, buf, &seen)
	wantKV(t, line, map[string]any{"outcome": "rejected", "accepted": false, "error_code": "UPSTREAM_TIMEOUT", "key_hash": keyHash16(k)})
	absentKeys(t, line, "replay")
	close(release)
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	wantKV(t, oneLine(t, buf, &seen), map[string]any{"outcome": "created", "accepted": true})
}
