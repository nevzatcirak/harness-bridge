package execution

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// gatedGet blocks the first native metadata read so a session guard can be
// observed while it is reserved but no run has been published yet.
type gatedGet struct {
	*fake
	once             sync.Once
	entered, release chan struct{}
}

func (g *gatedGet) Get(ctx context.Context, id string) (opencode.Session, error) {
	first := false
	g.once.Do(func() { first = true })
	if first {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
			return opencode.Session{}, ctx.Err()
		}
	}
	return g.fake.Get(ctx, id)
}

func wantGuard(t *testing.T, m *Manager, held bool, state string) {
	t.Helper()
	gotHeld, gotState := m.SessionGuard("ses_test")
	if gotHeld != held || gotState != state {
		t.Fatalf("SessionGuard = (%v, %q), want (%v, %q)", gotHeld, gotState, held, state)
	}
}

// R3: the guard and the held execution state are visible without native I/O.
func TestSessionGuard(t *testing.T) {
	ctx := context.Background()
	t.Run("none", func(t *testing.T) {
		m := setup(t, &fake{status: "idle"})
		wantGuard(t, m, false, "")
	})
	t.Run("reserved_without_run", func(t *testing.T) {
		g := &gatedGet{fake: &fake{status: "idle"}, entered: make(chan struct{}), release: make(chan struct{})}
		m := NewWithTiming(g, time.Second, 2*time.Millisecond)
		done := make(chan error, 1)
		go func() { _, e := m.Send(ctx, "ses_test", "prompt", "reserved-key"); done <- e }()
		<-g.entered
		wantGuard(t, m, true, "submitting")
		close(g.release)
		if e := <-done; e != nil {
			t.Fatal(e)
		}
		m.Close()
	})
	t.Run("submitting", func(t *testing.T) {
		f := &fake{status: "idle", entered: make(chan struct{}), release: make(chan struct{})}
		m := setup(t, f)
		if _, e := m.Send(ctx, "ses_test", "prompt", "submitting-key"); e != nil {
			t.Fatal(e)
		}
		<-f.entered
		wantGuard(t, m, true, "submitting")
		close(f.release)
	})
	t.Run("running_then_released", func(t *testing.T) {
		f := &fake{status: "idle"}
		m := setup(t, f)
		x, e := m.Send(ctx, "ses_test", "prompt", "running-key")
		if e != nil {
			t.Fatal(e)
		}
		waitState(t, m, x.ID, "running")
		wantGuard(t, m, true, "running")
		complete(f, x.MessageID, "stop", "")
		waitState(t, m, x.ID, "completed")
		wantGuard(t, m, false, "")
	})
	t.Run("waiting_for_permission", func(t *testing.T) {
		f := &fake{status: "idle"}
		m := setup(t, f)
		x, e := m.Send(ctx, "ses_test", "prompt", "permission-key")
		if e != nil {
			t.Fatal(e)
		}
		f.mu.Lock()
		f.permission = true
		f.mu.Unlock()
		waitState(t, m, x.ID, "waiting_for_permission")
		wantGuard(t, m, true, "waiting_for_permission")
	})
	t.Run("unknown", func(t *testing.T) {
		f := &fake{status: "idle", fail: true}
		m := setup(t, f)
		x, e := m.Send(ctx, "ses_test", "prompt", "unknown-key")
		if e != nil {
			t.Fatal(e)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			v, _ := m.Snapshot(ctx, x.ID)
			if v.State == "unknown" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("want unknown got %+v", v)
			}
			time.Sleep(time.Millisecond)
		}
		wantGuard(t, m, true, "unknown")
	})
	t.Run("terminal_while_guard_points_at_it", func(t *testing.T) {
		// A terminal run is always released in the same critical section. If the
		// guard still names one, the reader cannot prove release: stay conservative.
		m := setup(t, &fake{status: "idle"})
		m.mu.Lock()
		m.busy["ses_test"] = "exec_stale"
		m.runs["exec_stale"] = &run{native: "ses_test", view: Execution{ID: "exec_stale", State: "completed"}}
		m.mu.Unlock()
		wantGuard(t, m, true, "unknown")
	})
}

// G4: a reader never reports "no guard" while the same reservation is observed
// held immediately before and after it, and never reports a terminal state.
func TestSessionGuardConcurrent(t *testing.T) {
	f := &fake{status: "idle"}
	m := setup(t, f)
	ctx := context.Background()
	stop := make(chan struct{})
	var readers sync.WaitGroup
	errs := make(chan string, 8)
	busy := func() string {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.busy["ses_test"]
	}
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				before := busy()
				held, state := m.SessionGuard("ses_test")
				after := busy()
				switch {
				case before != "" && before == after && !held:
					errs <- "guard held at both edges but reported released"
					return
				case held && terminal(state):
					errs <- "terminal state reported while held: " + state
					return
				case !held && state != "":
					errs <- "state without guard: " + state
					return
				case held && state != "submitting" && state != "running" && state != "waiting_for_permission" && state != "unknown":
					errs <- "unexpected held state: " + state
					return
				}
			}
		}()
	}
	for i := 0; i < 25; i++ {
		x, e := m.Send(ctx, "ses_test", "prompt", fmt.Sprintf("churn-key-%02d", i))
		if e != nil {
			t.Fatal(e)
		}
		waitState(t, m, x.ID, "running")
		complete(f, x.MessageID, "stop", "")
		waitState(t, m, x.ID, "completed")
	}
	close(stop)
	readers.Wait()
	select {
	case e := <-errs:
		t.Fatal(e)
	default:
	}
}
