package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// TEST-HARDENING-02 (R3-PR55-REVIEW P3 item 1): discover re-checks every
// authorized workspace before projecting sessions (discovery.go, "Revalidate
// all paths"). When ONLY that re-check cannot complete, after the first pass
// authorized the session, discovery must fail closed with the retryable
// UPSTREAM_TIMEOUT and return no session. Mutant C2 (keep the session when the
// re-check errors) must fail this test.
func TestDiscoveryRecheckFailsClosed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ws")
	if e := os.Mkdir(dir, 0o755); e != nil {
		t.Fatal(e)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			w.Write([]byte(`{}`))
		case "/permission":
			w.Write([]byte(`[]`))
		case "/session/ses_a":
			json.NewEncoder(w).Encode(opencode.Session{ID: "ses_a", Directory: dir})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(up.Close)
	client, e := opencode.New(up.URL, "", "")
	if e != nil {
		t.Fatal(e)
	}
	policy, e := security.New([]string{root}, []string{"ses_a"})
	if e != nil {
		t.Fatal(e)
	}
	s := &Server{Client: client, Policy: policy, ReadOnly: true}

	// Control: without blocking, discovery reports the session. Per session,
	// the workspace check of dir runs, in order, in the first pass
	// (authorized), in repository inspection (Allows) and in the re-check.
	var calls atomic.Int64
	restore := security.SetFSHookForTesting(func(path string) {
		if path == dir {
			calls.Add(1)
		}
	})
	out, e := s.discover(context.Background())
	restore()
	if e != nil || len(out) != 1 || out[0].NativeID != "ses_a" {
		t.Fatalf("control discovery: %+v %v", out, e)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("control: %d workspace checks of the directory, want 3 (first pass, inspection, re-check)", n)
	}

	// Block only the third check of dir, the re-check after the first pass and
	// the inspection have succeeded.
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	calls.Store(0)
	var blocked atomic.Int64
	restore = security.SetFSHookForTesting(func(path string) {
		if path != dir {
			return
		}
		if calls.Add(1) == 3 {
			blocked.Add(1)
			<-gate
		}
	})
	t.Cleanup(func() {
		release()
		restore()
		deadline := time.Now().Add(5 * time.Second)
		for security.AllowsInFlightForTesting() != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})
	// A test-scoped caller deadline keeps this short; the re-check honours it.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, e = s.discover(ctx)
	elapsed := time.Since(start)
	if blocked.Load() != 1 {
		t.Fatalf("the re-check was not the blocked call (blocked=%d, calls=%d)", blocked.Load(), calls.Load())
	}
	var failure *execution.Error
	if !errors.As(e, &failure) || failure.Code != "UPSTREAM_TIMEOUT" || !failure.Retryable {
		t.Fatalf("blocked re-check: got %+v %v, want retryable UPSTREAM_TIMEOUT", out, e)
	}
	if len(out) != 0 {
		t.Fatalf("blocked re-check projected sessions: %+v", out)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("discovery held the caller %v", elapsed)
	}
}
