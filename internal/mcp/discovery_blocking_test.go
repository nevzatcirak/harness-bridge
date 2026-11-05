package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/repository"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// H4-A F-3: a repository storage call that blocks (hung NFS/FUSE) must not
// stall discovery past its deadline. Repository and workspace fields are then
// absent (fail closed); once storage recovers the next discovery reports them.
func TestDiscoveryNotStalledByBlockedRepositoryStorage(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"init"}, {"-c", "user.name=Test", "-c", "user.email=t@example.invalid", "commit", "--allow-empty", "-m", "seed"}} {
		if b, e := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); e != nil {
			t.Fatalf("%v %s", e, b)
		}
	}
	sessions := map[string]opencode.Session{"ses_a": {ID: "ses_a", Directory: root}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			json.NewEncoder(w).Encode(map[string]opencode.Status{})
		case "/permission":
			json.NewEncoder(w).Encode([]opencode.Permission{})
		default:
			v, ok := sessions[strings.TrimPrefix(r.URL.Path, "/session/")]
			if !ok {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(v)
		}
	}))
	defer upstream.Close()
	client, _ := opencode.New(upstream.URL, "", "")
	policy, _ := security.New([]string{root}, []string{"ses_a"})
	s := &Server{Client: client, Policy: policy, ReadOnly: true}

	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	restore := repository.SetFSHookForTesting(func(path string) {
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			<-gate
		}
	})
	defer restore()
	defer release()

	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		start := time.Now()
		out, e := s.discover(context.Background())
		elapsed := time.Since(start)
		if e != nil {
			t.Fatalf("discovery %d: %v", i, e)
		}
		if elapsed > 5*time.Second {
			t.Fatalf("discovery %d took %v while storage was blocked", i, elapsed)
		}
		if len(out) != 1 || out[0].Repository != nil || out[0].Workspace != nil {
			t.Fatalf("discovery %d while blocked: %+v", i, out)
		}
	}
	// Poll: discovery's own worker goroutines may still be exiting right after
	// discover returns; blocked inspections never exit, so a leak still fails.
	settle := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine()-before > 4 && time.Now().Before(settle) {
		time.Sleep(5 * time.Millisecond)
	}
	if grown := runtime.NumGoroutine() - before; grown > 4 {
		t.Fatalf("goroutines grew by %d after 5 discoveries over one blocked directory", grown)
	}

	release()
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, e := s.discover(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if len(out) == 1 && out[0].Repository != nil && out[0].Workspace != nil && out[0].Workspace.Root == root {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery after recovery still lacks repository fields: %+v", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
