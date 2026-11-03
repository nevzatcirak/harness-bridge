package mcp

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// G4 (MCP side): concurrent discovery reads while executions are admitted,
// completed and re-admitted on the same session. Native status is always idle.
// A read whose single guard sample is held must not be idle; with a guard held
// throughout (fixed phase) no read may be idle. Run with -race.
func TestStateGuardConcurrentDiscovery(t *testing.T) {
	root := t.TempDir()
	t.Run("held_throughout", func(t *testing.T) {
		s := guardServer(t, root, heldManager(t, root, "running"), `{"ses_a":{"type":"idle"}}`, `[]`)
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 25; j++ {
					for _, state := range sessionStatesNoFatal(s) {
						if state != "running" {
							errs <- fmt.Errorf("held session reported %q", state)
							return
						}
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
	})
	t.Run("churn", func(t *testing.T) {
		g := &guardRunner{directory: root, mode: "idle", entered: make(chan struct{}), release: make(chan struct{})}
		runs := execution.NewWithTiming(g, 30*time.Second, time.Millisecond)
		t.Cleanup(runs.Close)
		s := guardServer(t, root, runs, `{"ses_a":{"type":"idle"}}`, `[]`)
		stop := make(chan struct{})
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					for _, state := range sessionStatesNoFatal(s) {
						if state != "idle" && state != "running" && state != "unknown" {
							errs <- fmt.Errorf("unexpected state %q", state)
							return
						}
					}
				}
			}()
		}
		ctx := context.Background()
		for i := 0; i < 15; i++ {
			x, e := runs.Send(ctx, "ses_a", "prompt", fmt.Sprintf("churn-key-%02d", i))
			if e != nil {
				t.Fatal(e)
			}
			if held, _ := runs.SessionGuard("ses_a"); held {
				for _, state := range sessionStatesNoFatal(s) {
					if state == "idle" {
						if still, _ := runs.SessionGuard("ses_a"); still {
							t.Fatal("idle reported while guard held before and after")
						}
					}
				}
			}
			waitExecution(t, runs, x.ID, "running")
			g.complete(x.MessageID)
			waitExecution(t, runs, x.ID, "completed")
		}
		close(stop)
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
	})
}

func sessionStatesNoFatal(s *Server) []string {
	ctx := context.Background()
	out := []string{}
	if v, e := s.call(ctx, "get_session", args{SessionID: "opencode:ses_a"}); e == nil {
		out = append(out, v.(Session).State)
	}
	if v, e := s.call(ctx, "list_sessions", args{}); e == nil {
		for _, x := range v.(Sessions).Sessions {
			out = append(out, x.State)
		}
	}
	return out
}

func waitExecution(t *testing.T, m *execution.Manager, id, state string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		v, e := m.Get(context.Background(), id)
		if e == nil && v.State == state {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s: want %s got %+v %v", id, state, v, e)
		}
		time.Sleep(time.Millisecond)
	}
}
