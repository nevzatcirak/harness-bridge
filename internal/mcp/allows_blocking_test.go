package mcp

import (
	"context"
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

	// invoke's 2 s client timeout would race the check's own 2 s bound.
	call := func(name string, args any, wantError bool) *sdk.CallToolResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		r, e := client.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		if r.IsError != wantError {
			t.Fatalf("%s error=%v content=%v", name, r.IsError, r.Content)
		}
		return r
	}
	wantTimeout := func(name string, start time.Time, failure execution.Error) {
		t.Helper()
		if elapsed := time.Since(start); elapsed > 4*time.Second {
			t.Fatalf("%s took %v while storage was blocked", name, elapsed)
		}
		if failure.Code != "UPSTREAM_TIMEOUT" || !failure.Retryable {
			t.Fatalf("%s: got %+v, want retryable UPSTREAM_TIMEOUT", name, failure)
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
		start := time.Now()
		r := call("get_session", SessionInput{"opencode:ses_existing"}, true)
		wantTimeout("get_session", start, decode[execution.Error](t, r))

		start = time.Now()
		r = call("send_prompt", PromptInput{"opencode:ses_existing", "blocked storage prompt", "allows-block-key-" + string(rune('a'+i))}, true)
		wantTimeout("send_prompt", start, decode[execution.Error](t, r))

		start = time.Now()
		_, e := discovery.discover(context.Background())
		if e == nil {
			t.Fatal("discovery succeeded while the workspace check was blocked")
		}
		wantTimeout("discovery", start, *envelope(e))
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
