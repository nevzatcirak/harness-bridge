package execution

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

func TestDashboardSnapshotEmpty(t *testing.T) {
	m := setup(t, &fake{status: "idle"})
	v := m.DashboardSnapshot()
	if len(v.Executions) != 0 || len(v.Guards) != 0 || v.Guards == nil {
		t.Fatalf("empty manager: %+v", v)
	}
}

// The guard and the executions are visible without native I/O, in the same
// harness-qualified form the dashboard shows.
func TestDashboardSnapshotShowsExecutionsAndGuards(t *testing.T) {
	ctx := context.Background()
	t.Run("reservation without a published run", func(t *testing.T) {
		g := &gatedGet{fake: &fake{status: "idle"}, entered: make(chan struct{}), release: make(chan struct{})}
		m := NewWithTiming(g, time.Second, 2*time.Millisecond)
		done := make(chan error, 1)
		go func() { _, e := m.Send(ctx, "ses_test", "prompt", "reserved-key"); done <- e }()
		<-g.entered
		v := m.DashboardSnapshot()
		if len(v.Executions) != 0 {
			t.Fatalf("unpublished run listed: %+v", v.Executions)
		}
		if gv, ok := v.Guards["opencode:ses_test"]; !ok || gv.State != "submitting" || gv.ExecutionID == "" {
			t.Fatalf("guards: %+v", v.Guards)
		}
		close(g.release)
		if e := <-done; e != nil {
			t.Fatal(e)
		}
		m.Close()
	})
	t.Run("running then released", func(t *testing.T) {
		f := &fake{status: "idle"}
		m := setup(t, f)
		x, e := m.Send(ctx, "ses_test", "prompt", "running-key")
		if e != nil {
			t.Fatal(e)
		}
		waitState(t, m, x.ID, "running")
		v := m.DashboardSnapshot()
		if len(v.Executions) != 1 || v.Executions[0].ID != x.ID || v.Executions[0].SessionID != "opencode:ses_test" {
			t.Fatalf("executions: %+v", v.Executions)
		}
		if gv := v.Guards["opencode:ses_test"]; gv.ExecutionID != x.ID || gv.State != "running" {
			t.Fatalf("guard: %+v", v.Guards)
		}
		complete(f, x.MessageID, "stop", "")
		waitState(t, m, x.ID, "completed")
		v = m.DashboardSnapshot()
		if len(v.Executions) != 1 || v.Executions[0].State != "completed" {
			t.Fatalf("executions after completion: %+v", v.Executions)
		}
		if _, held := v.Guards["opencode:ses_test"]; held {
			t.Fatalf("guard still reported after release: %+v", v.Guards)
		}
	})
}

// The returned values are copies: mutating them, including every pointer field,
// leaves the manager's state untouched.
func TestDashboardSnapshotDoesNotAliasRunState(t *testing.T) {
	m := setup(t, &fake{status: "idle"})
	now := time.Now().UTC().Truncate(time.Second)
	later := now.Add(time.Minute)
	x := &run{view: Execution{
		ID: "exec_a", SessionID: "opencode:ses_a", State: "unknown", Started: now, Updated: now,
		Ended:        &later,
		Unresolved:   &Unresolved{Reason: "R", Since: now, LastCheckedAt: &later},
		LocalFailure: &LocalFailure{Code: "C", RecordedAt: now},
		Liveness:     &Liveness{},
	}, native: "ses_a", op: make(chan struct{}, 1)}
	m.mu.Lock()
	m.runs["exec_a"] = x
	m.busy["ses_a"] = "exec_a"
	m.mu.Unlock()

	v := m.DashboardSnapshot()
	if len(v.Executions) != 1 {
		t.Fatalf("executions: %+v", v.Executions)
	}
	e := &v.Executions[0]
	*e.Ended = now.Add(99 * time.Hour)
	e.Unresolved.Reason = "TAMPERED"
	*e.Unresolved.LastCheckedAt = now.Add(98 * time.Hour)
	e.LocalFailure.Code = "TAMPERED"
	e.State = "completed"
	v.Guards["opencode:ses_a"] = GuardView{ExecutionID: "tampered", State: "completed"}
	delete(v.Guards, "opencode:ses_a")

	x.mu.Lock()
	got := x.view
	x.mu.Unlock()
	if got.State != "unknown" || !got.Ended.Equal(later) || got.Unresolved.Reason != "R" || !got.Unresolved.LastCheckedAt.Equal(later) || got.LocalFailure.Code != "C" {
		t.Fatalf("manager state changed through the copy: %+v", got)
	}
	again := m.DashboardSnapshot()
	if len(again.Executions) != 1 || again.Executions[0].State != "unknown" || again.Guards["opencode:ses_a"].ExecutionID != "exec_a" {
		t.Fatalf("second snapshot: %+v", again)
	}
}

// A guard whose holder is already terminal is conservative, never "free".
func TestDashboardSnapshotTerminalHolderIsUnknown(t *testing.T) {
	m := setup(t, &fake{status: "idle"})
	x := &run{view: Execution{ID: "exec_t", SessionID: "opencode:ses_t", State: "completed", Started: time.Now(), Updated: time.Now()}, native: "ses_t", op: make(chan struct{}, 1)}
	m.mu.Lock()
	m.runs["exec_t"] = x
	m.busy["ses_t"] = "exec_t"
	m.mu.Unlock()
	if gv := m.DashboardSnapshot().Guards["opencode:ses_t"]; gv.State != "unknown" {
		t.Fatalf("guard of a terminal holder: %+v", gv)
	}
}

// T10 lock order. The race detector does not find lock-order inversions, so
// this test builds the interleaving: one goroutine holds a run mutex and then
// wants the manager mutex (the nested order finalizeUnsubmitted and
// commitRelease use) while the enumeration runs. An enumeration that holds m.mu
// while it takes x.mu would deadlock here and miss the 5 s deadline.
func TestDashboardSnapshotNeverHoldsManagerAndRunMutexTogether(t *testing.T) {
	for i := 0; i < 3; i++ {
		// No Close: on a deadlock the manager mutex is held forever, and Close
		// would hang the test instead of letting it fail at its deadline. No
		// goroutine of this manager runs (nothing was sent), so nothing leaks.
		m := NewWithTiming(&fake{status: "idle"}, time.Second, 2*time.Millisecond)
		x := &run{view: Execution{ID: "exec_l", SessionID: "opencode:ses_l", State: "running", Started: time.Now(), Updated: time.Now()}, native: "ses_l", op: make(chan struct{}, 1)}
		m.mu.Lock()
		m.runs["exec_l"] = x
		m.busy["ses_l"] = "exec_l"
		m.mu.Unlock()

		holding, nested, enumerated := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() { // the nested order: run mutex, then manager mutex
			x.mu.Lock()
			close(holding)
			time.Sleep(300 * time.Millisecond) // the enumeration is now past m.mu and waits on x.mu
			m.mu.Lock()
			m.busy["ses_other"] = "exec_other"
			delete(m.busy, "ses_other")
			m.mu.Unlock()
			x.mu.Unlock()
			close(nested)
		}()
		<-holding
		go func() { m.DashboardSnapshot(); close(enumerated) }()

		deadline := time.After(5 * time.Second)
		for _, c := range []chan struct{}{nested, enumerated} {
			select {
			case <-c:
			case <-deadline:
				t.Fatal("lock-order deadlock: DashboardSnapshot holds m.mu while taking a run mutex")
			}
		}
	}
}

// gatedStatus blocks exactly one native status read, armed after the run is
// published, so that run's probe holds its op while the enumeration runs.
type gatedStatus struct {
	*fake
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (g *gatedStatus) Status(ctx context.Context) (map[string]opencode.Status, error) {
	if g.armed.CompareAndSwap(true, false) {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.fake.Status(ctx)
}

// A native read held under a run's op must not block the enumeration, and
// neither may a concurrent send to another session be blocked by it.
func TestDashboardSnapshotIsNotBlockedByAHeldNativeRead(t *testing.T) {
	g := &gatedStatus{fake: &fake{status: "idle"}, entered: make(chan struct{}), release: make(chan struct{})}
	m := NewWithTiming(g, 10*time.Second, 2*time.Millisecond)
	t.Cleanup(func() { close(g.release); m.Close() })
	ctx := context.Background()
	x, e := m.Send(ctx, "ses_test", "prompt", "held-read-key")
	if e != nil {
		t.Fatal(e)
	}
	g.armed.Store(true)
	select {
	case <-g.entered: // a native status read is now in flight under this run's op
	case <-time.After(5 * time.Second):
		t.Fatal("test bug: no native read reached the gate")
	}
	got := make(chan DashboardView, 1)
	go func() { got <- m.DashboardSnapshot() }()
	select {
	case v := <-got:
		if len(v.Executions) != 1 || v.Executions[0].ID != x.ID {
			t.Fatalf("executions: %+v", v.Executions)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DashboardSnapshot blocked behind a held native read")
	}
	// A second session is admitted while the first run's native read is held.
	sent := make(chan error, 1)
	go func() { _, err := m.Send(ctx, "ses_other", "prompt", "other-key"); sent <- err }()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("send to another session blocked while a dashboard read ran")
	}
}

// Enumeration concurrent with sends and finalization, under -race and a 5 s
// deadline.
func TestDashboardSnapshotConcurrentWithSendAndFinalize(t *testing.T) {
	f := &fake{status: "idle"}
	m := setup(t, f)
	ctx := context.Background()
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 6; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					v := m.DashboardSnapshot()
					for _, e := range v.Executions {
						_ = e.State
					}
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, key := range []string{"conc-key-1", "conc-key-2", "conc-key-3"} {
			x, e := m.Send(ctx, "ses_test", "prompt", key)
			if e != nil {
				t.Error(e)
				return
			}
			waitState(t, m, x.ID, "running")
			complete(f, x.MessageID, "stop", "")
			waitState(t, m, x.ID, "completed")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("send/finalize stalled while the dashboard enumerated")
	}
	close(stop)
	readers.Wait()
	if n := len(m.DashboardSnapshot().Executions); n != 3 {
		t.Fatalf("executions: %d", n)
	}
}

// Review #223 P3-2, decided: DashboardSnapshot takes no context and applies no cap.
// It does no I/O, and the manager's own memory-only maps are not capped either, so a
// cap would hide executions instead of bounding anything. The cost is O(runs) under
// m.mu for the pointer copy only; this test pins that a large process stays far inside
// the 5 s request bound and that a send is not stalled while it enumerates.
func TestDashboardSnapshotWithManyRunsStaysBounded(t *testing.T) {
	m := setup(t, &fake{status: "idle"})
	const n = 20000
	m.mu.Lock()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("exec_%06d", i)
		m.runs[id] = &run{view: Execution{ID: id, SessionID: "opencode:ses_" + id, State: "completed", Started: time.Unix(int64(i), 0), Updated: time.Unix(int64(i), 0)}, native: "ses_" + id, op: make(chan struct{}, 1)}
	}
	m.mu.Unlock()
	start := time.Now()
	v := m.DashboardSnapshot()
	if len(v.Executions) != n || time.Since(start) > 5*time.Second {
		t.Fatalf("%d executions in %v", len(v.Executions), time.Since(start))
	}
	// A send is admitted while another goroutine enumerates in a loop.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				m.DashboardSnapshot()
			}
		}
	}()
	defer close(stop)
	sent := make(chan error, 1)
	go func() { _, err := m.Send(context.Background(), "ses_test", "prompt", "key-bound-0001"); sent <- err }()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a send stalled while 20000 executions were enumerated")
	}
}
