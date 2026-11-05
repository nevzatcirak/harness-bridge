package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// H4-A F-3 (second half): the workspace authorization check that every
// session-scoped tool performs must not block past its deadline on a hung
// NFS/FUSE mount. It fails closed with a retryable UPSTREAM_TIMEOUT, submits
// nothing to the native harness, keeps goroutines bounded and recovers.
//
// Every blocked call waits the check's own 2 s bound, so the three calls of a
// round run concurrently (TEST-HARDENING-02): one run takes about 8 s instead
// of 24 s. `go test -race -count=20 ./internal/mcp` then fits the default
// 10 m test timeout; pass -timeout explicitly for larger -count values.
func TestWorkspaceCheckNotStalledByBlockedStorage(t *testing.T) {
	client, f, bridge, _ := integration(t, true)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	restore := security.SetFSHookForTesting(func(path string) {
		if path == f.root || strings.HasPrefix(path, f.root+string(filepath.Separator)) {
			<-gate
		}
	})
	defer restore()
	defer release()

	// invoke's 2 s client timeout would race the check's own 2 s bound. These
	// helpers run in goroutines, so they report with t.Errorf, not t.Fatalf.
	callTool := func(name string, args any, wantError bool) (*sdk.CallToolResult, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		r, e := client.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if e != nil {
			t.Errorf("%s: %v", name, e)
			return nil, false
		}
		if r.IsError != wantError {
			t.Errorf("%s error=%v content=%v", name, r.IsError, r.Content)
			return nil, false
		}
		return r, true
	}
	call := func(name string, args any, wantError bool) *sdk.CallToolResult {
		t.Helper()
		r, ok := callTool(name, args, wantError)
		if !ok {
			t.FailNow()
		}
		return r
	}
	wantTimeout := func(name string, start time.Time, failure execution.Error) {
		if elapsed := time.Since(start); elapsed > 4*time.Second {
			t.Errorf("%s took %v while storage was blocked", name, elapsed)
		}
		if failure.Code != "UPSTREAM_TIMEOUT" || !failure.Retryable {
			t.Errorf("%s: got %+v, want retryable UPSTREAM_TIMEOUT", name, failure)
		}
	}

	// The shared fixture's ACL also lists ses_blocked, whose native lookup
	// hangs by design; discovery here uses only the fixture's real session.
	discoveryPolicy, e := security.New([]string{f.root}, []string{"ses_existing"})
	if e != nil {
		t.Fatal(e)
	}
	discovery := &Server{Client: bridge.Client, Runs: bridge.Runs, Token: bridge.Token, Policy: discoveryPolicy, ReadOnly: true}

	before := 0
	for i := 0; i < 4; i++ {
		if i == 1 {
			// Baseline after the first round: it opens the MCP/HTTP connections
			// and starts the one blocked check per key that later rounds share.
			before = runtime.NumGoroutine()
		}
		// The three calls of a round run concurrently; each one still has to
		// return within its own bound with the fail-closed error.
		var wg sync.WaitGroup
		wg.Add(3)
		go func() {
			defer wg.Done()
			start := time.Now()
			if r, ok := callTool("get_session", SessionInput{"opencode:ses_existing"}, true); ok {
				wantTimeout("get_session", start, toolError(t, "get_session", r))
			}
		}()
		go func() {
			defer wg.Done()
			start := time.Now()
			if r, ok := callTool("send_prompt", PromptInput{"opencode:ses_existing", "blocked storage prompt", "allows-block-key-" + string(rune('a'+i))}, true); ok {
				wantTimeout("send_prompt", start, toolError(t, "send_prompt", r))
			}
		}()
		go func() {
			defer wg.Done()
			start := time.Now()
			_, e := discovery.discover(context.Background())
			if e == nil {
				t.Error("discovery succeeded while the workspace check was blocked")
				return
			}
			wantTimeout("discovery", start, *envelope(e))
		}()
		// Watchdog: a call that never returns fails the test instead of
		// hanging it until the go test timeout.
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: a call did not return within 10 s while storage was blocked", i)
		}
		if t.Failed() {
			t.FailNow()
		}
	}
	f.mu.Lock()
	posts := f.posts
	f.mu.Unlock()
	if posts != 0 {
		t.Fatalf("%d native prompts submitted while the workspace check was unavailable", posts)
	}
	settle := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine()-before > 4 && time.Now().Before(settle) {
		time.Sleep(5 * time.Millisecond)
	}
	if grown := runtime.NumGoroutine() - before; grown > 4 {
		t.Fatalf("goroutines grew by %d after repeated calls over one blocked workspace", grown)
	}
	// One blocked check per (policy, directory) key: the bridge's policy and
	// the discovery server's policy.
	if n := security.AllowsInFlightForTesting(); n > 2 {
		t.Fatalf("%d workspace checks in flight for 2 keys", n)
	}

	release()
	deadline := time.Now().Add(5 * time.Second)
	for security.AllowsInFlightForTesting() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	r := call("get_session", SessionInput{"opencode:ses_existing"}, false)
	if got := decode[Session](t, r); got.NativeID != "ses_existing" || got.Directory != f.root {
		t.Fatalf("get_session after recovery: %+v", got)
	}
}

// toolError decodes a tool's structured error without t.Fatal, so it can run
// in the goroutines above.
func toolError(t *testing.T, name string, r *sdk.CallToolResult) execution.Error {
	var v execution.Error
	b, e := json.Marshal(r.StructuredContent)
	if e == nil {
		e = json.Unmarshal(b, &v)
	}
	if e != nil {
		t.Errorf("%s: undecodable error result: %v", name, e)
	}
	return v
}
