package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// The production 300s deadline runs against Go's virtual clock, not a shorter
// production timeout. Every spawned goroutine must exit before the bubble ends.
func expired(t *testing.T, r Runner) (*Manager, Execution) {
	t.Helper()
	m := NewWithTiming(r, 300*time.Second, time.Second)
	x, err := m.Send(context.Background(), "ses_test", "controlled fixture", "late-key")
	if err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
	time.Sleep(300 * time.Second)
	synctest.Wait()
	v, err := m.Snapshot(context.Background(), x.ID)
	if err != nil || v.State != "unknown" || v.Unresolved == nil || v.Unresolved.Reason != "MONITOR_EXPIRED" || v.MonitorEndedAt == nil || v.Ended != nil {
		t.Fatalf("expiry: %+v %v", v, err)
	}
	return m, v
}
func lateProof(f *fake, mid, state string) {
	nativeError := ""
	finish := "stop"
	if state == "failed" {
		nativeError = `{"name":"APIError"}`
		finish = ""
	}
	if state == "interrupted" {
		nativeError = `{"name":"MessageAbortedError"}`
		finish = ""
	}
	complete(f, mid, finish, nativeError)
	f.mu.Lock()
	last := &f.messages[len(f.messages)-1]
	last.Info.Time.Completed = time.Now().UnixMilli()
	last.Info.Path.Cwd = "/"
	f.mu.Unlock()
}
func assertBusy(t *testing.T, m *Manager) {
	t.Helper()
	_, err := m.Send(context.Background(), "ses_test", "another", "another-key")
	var e *Error
	if !errors.As(err, &e) || e.Code != "SESSION_BUSY" {
		t.Fatalf("guard lost: %v", err)
	}
}
func assertReleased(t *testing.T, m *Manager) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy["ses_test"] != "" {
		t.Fatal("terminal guard retained")
	}
}

func TestLateCompletionAfterRealMonitorDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{status: "idle"}
		m, x := expired(t, f)
		defer m.Close()
		// Native still running AFTER the background monitor has ended.
		f.mu.Lock()
		f.status = "busy"
		f.mu.Unlock()
		v, err := m.Get(context.Background(), x.ID)
		if err != nil || v.State != "unknown" || v.Unresolved.LastCheckedAt == nil {
			t.Fatal(v, err)
		}
		assertBusy(t, m)
		replay, err := m.Send(context.Background(), "ses_test", "controlled fixture", "late-key")
		if err != nil || replay.ID != x.ID || replay.MessageID != x.MessageID || replay.State != "unknown" {
			t.Fatal(replay, err)
		}
		_, err = m.Send(context.Background(), "ses_test", "different", "late-key")
		var conflict *Error
		if !errors.As(err, &conflict) || conflict.Code != "IDEMPOTENCY_CONFLICT" {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Second)
		lateProof(f, x.MessageID, "completed")
		f.mu.Lock()
		f.status = "idle"
		f.mu.Unlock()
		done, msgs, err := m.Result(context.Background(), x.ID)
		if err != nil || done.State != "completed" || done.ID != x.ID || done.MessageID != x.MessageID || done.ReconciledAt == nil || done.Unresolved != nil || done.Ended == nil || len(msgs) != 2 {
			t.Fatal(done, len(msgs), err)
		}
		if done.Ended.Sub(x.Started) != 306*time.Second {
			t.Fatalf("native end timestamp lost: %v", done.Ended)
		}
		assertReleased(t, m)
		again, err := m.Get(context.Background(), x.ID)
		if err != nil || !again.Ended.Equal(*done.Ended) || !again.ReconciledAt.Equal(*done.ReconciledAt) {
			t.Fatal(again, err)
		}
		// Client-owned snapshots cannot mutate internal timestamp/reason records.
		*again.Ended = time.Time{}
		again, err = m.Get(context.Background(), x.ID)
		if err != nil || again.Ended.IsZero() {
			t.Fatal("snapshot alias")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sends != 1 || f.aborts != 0 {
			t.Fatal(f.sends, f.aborts)
		}
	})
}

func TestLateReconciliationTerminalErrors(t *testing.T) {
	for _, state := range []string{"failed", "interrupted"} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &fake{status: "idle"}
				m, x := expired(t, f)
				defer m.Close()
				lateProof(f, x.MessageID, state)
				got, err := m.Get(context.Background(), x.ID)
				if err != nil || got.State != state || got.ReconciledAt == nil {
					t.Fatal(got, err)
				}
				assertReleased(t, m)
			})
		})
	}
}

func TestLatePermissionBlocksEvenWithTerminalMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{status: "idle"}
		m, x := expired(t, f)
		defer m.Close()
		lateProof(f, x.MessageID, "completed")
		f.mu.Lock()
		f.permission = true
		f.mu.Unlock()
		got, err := m.Get(context.Background(), x.ID)
		if err != nil || got.State != "waiting_for_permission" || got.Ended != nil || got.Unresolved == nil {
			t.Fatal(got, err)
		}
		assertBusy(t, m)
		f.mu.Lock()
		f.permission = false
		f.mu.Unlock()
		time.Sleep(time.Second)
		got, err = m.Get(context.Background(), x.ID)
		if err != nil || got.State != "completed" {
			t.Fatal(got, err)
		}
		assertReleased(t, m)
	})
}

func TestLateWrongCorrelationCannotReleaseGuard(t *testing.T) {
	for _, kind := range []string{"parent", "session", "no-user", "unfinished", "negative-time", "malformed-error", "tool-finish", "busy"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &fake{status: "idle"}
				m, x := expired(t, f)
				defer m.Close()
				lateProof(f, x.MessageID, "completed")
				f.mu.Lock()
				last := &f.messages[len(f.messages)-1]
				switch kind {
				case "parent":
					last.Info.ParentID = "msg_other"
				case "session":
					last.Info.SessionID = "ses_other"
				case "no-user":
					f.messages = f.messages[1:]
				case "unfinished":
					last.Info.Time.Completed = 0
				case "negative-time":
					last.Info.Time.Completed = -1
				case "malformed-error":
					last.Info.Error = []byte(`{"unexpected":true}`)
				case "tool-finish":
					last.Info.Finish = "tool-calls"
				case "busy":
					f.status = "busy"
				}
				f.mu.Unlock()
				got, err := m.Get(context.Background(), x.ID)
				if err != nil || got.State != "unknown" || got.Ended != nil {
					t.Fatal(got, err)
				}
				assertBusy(t, m)
			})
		})
	}
}

type identityFixture struct {
	*fake
	id, dir string
}

func (f *identityFixture) Get(ctx context.Context, id string) (opencode.Session, error) {
	meta, err := f.fake.Get(ctx, id)
	if f.id != "" {
		meta.ID = f.id
	}
	if f.dir != "" {
		meta.Directory = f.dir
	}
	return meta, err
}
func TestLateIdentityAndWorkspaceMismatch(t *testing.T) {
	for _, kind := range []string{"session", "directory", "assistant-cwd"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &identityFixture{fake: &fake{status: "idle"}}
				m, x := expired(t, f)
				defer m.Close()
				lateProof(f.fake, x.MessageID, "completed")
				switch kind {
				case "session":
					f.id = "ses_other"
				case "directory":
					f.dir = "/tmp"
				case "assistant-cwd":
					f.mu.Lock()
					f.messages[len(f.messages)-1].Info.Path.Cwd = "/tmp"
					f.mu.Unlock()
				}
				_, _, err := m.Result(context.Background(), x.ID)
				var denied *Error
				if !errors.As(err, &denied) || denied.Code != "FORBIDDEN" {
					t.Fatal(err)
				}
				assertBusy(t, m)
				// Cooldown must retain a denial, not expose stale result messages.
				_, msgs, err := m.Result(context.Background(), x.ID)
				if err == nil || len(msgs) != 0 {
					t.Fatal("cached denial lost", err)
				}
			})
		})
	}
}

type gatedHistory struct {
	*fake
	gateMu            sync.Mutex
	block             <-chan struct{}
	entered           chan struct{}
	calls             atomic.Int64
	active, maxActive *atomic.Int64
}

func (f *gatedHistory) History(ctx context.Context, id string) ([]opencode.Message, error) {
	f.calls.Add(1)
	f.gateMu.Lock()
	block, entered := f.block, f.entered
	f.gateMu.Unlock()
	if block != nil {
		if f.active != nil {
			n := f.active.Add(1)
			defer f.active.Add(-1)
			for old := f.maxActive.Load(); n > old; old = f.maxActive.Load() {
				if f.maxActive.CompareAndSwap(old, n) {
					break
				}
			}
		}
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.fake.History(ctx, id)
}
func (f *gatedHistory) setGate(block <-chan struct{}, entered chan struct{}) {
	f.gateMu.Lock()
	f.block = block
	f.entered = entered
	f.gateMu.Unlock()
}

func TestConcurrentLateStatusResultAndInterrupt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &gatedHistory{fake: &fake{status: "idle"}}
		m, x := expired(t, f)
		defer m.Close()
		lateProof(f.fake, x.MessageID, "completed")
		block := make(chan struct{})
		entered := make(chan struct{}, 1)
		f.setGate(block, entered)
		before := f.calls.Load()
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if i%2 == 0 {
					got, err := m.Get(context.Background(), x.ID)
					if err != nil || got.State != "completed" {
						t.Error(got, err)
					}
				} else {
					got, msgs, err := m.Result(context.Background(), x.ID)
					if err != nil || got.State != "completed" || len(msgs) != 2 {
						t.Error(got, len(msgs), err)
					}
				}
			}(i)
		}
		<-entered
		synctest.Wait()
		close(block)
		wg.Wait()
		if f.calls.Load()-before != 1 {
			t.Fatal("pollers did not coalesce", f.calls.Load()-before)
		}
		// Once terminal proof is committed, explicit interrupt races cannot abort a
		// newly admitted run or demote/rewrite the terminal record.
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, err := m.Interrupt(context.Background(), x.ID)
				var typed *Error
				if v.State != "completed" || !errors.As(err, &typed) || typed.Code != "INVALID_INPUT" {
					t.Error(v, err)
				}
			}()
		}
		wg.Wait()
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sends != 1 || f.aborts != 0 {
			t.Fatal(f.sends, f.aborts)
		}
	})
}

func TestExplicitInterruptAndLateProbeShareOperationGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &gatedHistory{fake: &fake{status: "idle"}}
		m, x := expired(t, f)
		defer m.Close()
		lateProof(f.fake, x.MessageID, "completed")
		block := make(chan struct{})
		entered := make(chan struct{}, 1)
		f.setGate(block, entered)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			v, err := m.Get(context.Background(), x.ID)
			if err != nil || v.State != "completed" {
				t.Error(v, err)
			}
		}()
		<-entered
		go func() {
			defer wg.Done()
			v, err := m.Interrupt(context.Background(), x.ID)
			if err == nil || v.State != "completed" {
				t.Error(v, err)
			}
		}()
		synctest.Wait()
		close(block)
		wg.Wait()
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.aborts != 0 {
			t.Fatal("aborted proven completed execution")
		}
	})
}

func TestLateReadsBoundedCanceledAndNoBackgroundPolling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &gatedHistory{fake: &fake{status: "idle"}}
		m, x := expired(t, f)
		defer m.Close()
		forever := make(chan struct{})
		entered := make(chan struct{}, 1)
		f.setGate(forever, entered)
		done := make(chan Execution, 1)
		go func() {
			v, err := m.Get(context.Background(), x.ID)
			if err != nil {
				t.Error(err)
			}
			done <- v
		}()
		<-entered
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := m.Get(canceled, x.ID); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		v := <-done
		if v.State != "unknown" || v.Unresolved == nil || v.Unresolved.LastCheckedAt == nil {
			t.Fatal(v)
		}
		assertBusy(t, m)
		calls := f.calls.Load()
		time.Sleep(1000 * time.Second)
		synctest.Wait()
		if f.calls.Load() != calls {
			t.Fatal("polling continued without a caller")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sends != 1 || f.aborts != 0 {
			t.Fatal(f.sends, f.aborts)
		}
	})
}

func TestCloseCancelsBlockedReconciliation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &gatedHistory{fake: &fake{status: "idle"}}
		m, x := expired(t, f)
		entered := make(chan struct{}, 1)
		f.setGate(make(chan struct{}), entered)
		done := make(chan struct{})
		go func() { _, _ = m.Get(context.Background(), x.ID); close(done) }()
		<-entered
		m.Close()
		<-done
		synctest.Wait()
		got, err := m.Get(context.Background(), x.ID)
		if err != nil || got.State != "unknown" || got.Unresolved.Reason != "MANAGER_CLOSED" {
			t.Fatal(got, err)
		}
		// A new manager does not invent recovery of the old execution ID.
		fresh := New(f)
		defer fresh.Close()
		if _, err = fresh.Get(context.Background(), x.ID); err == nil {
			t.Fatal("restart fabricated execution")
		}
	})
}

func TestReconciliationGlobalConcurrencyBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewWithTiming(nil, 300*time.Second, time.Second)
		defer m.Close()
		var active, max atomic.Int64
		var fixtures []*gatedHistory
		var executions []Execution
		for i := 0; i < 5; i++ {
			f := &gatedHistory{fake: &fake{status: "idle"}, active: &active, maxActive: &max}
			fixtures = append(fixtures, f)
			x, err := m.SendWithRunner(context.Background(), f, fmt.Sprintf("ses_test%d", i), "fixture", fmt.Sprintf("bound-key-%d", i))
			if err != nil {
				t.Fatal(err)
			}
			executions = append(executions, x)
		}
		synctest.Wait()
		time.Sleep(300 * time.Second)
		synctest.Wait()
		for _, f := range fixtures {
			f.setGate(make(chan struct{}), nil)
		}
		var wg sync.WaitGroup
		for _, x := range executions {
			wg.Add(1)
			go func(id string) { defer wg.Done(); _, _ = m.Get(context.Background(), id) }(x.ID)
		}
		synctest.Wait()
		if active.Load() != 4 || max.Load() > 4 {
			t.Fatal("global native-read bound", active.Load(), max.Load())
		}
		m.Close()
		wg.Wait()
		synctest.Wait()
		if active.Load() != 0 {
			t.Fatal("native probes leaked")
		}
	})
}

func TestUnconfirmedSubmissionCanReconcileWithoutResubmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{status: "idle", fail: true}
		m := New(f)
		defer m.Close()
		ctx, cancel := context.WithCancel(context.Background())
		x, err := m.Send(ctx, "ses_test", "fixture", "uncertain-key")
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		synctest.Wait()
		got, err := m.Get(context.Background(), x.ID)
		if err != nil || got.State != "unknown" || got.Unresolved.Reason != "SUBMISSION_UNCONFIRMED" {
			t.Fatal(got, err)
		}
		lateProof(f, x.MessageID, "completed")
		time.Sleep(time.Second)
		got, err = m.Get(context.Background(), x.ID)
		if err != nil || got.State != "completed" {
			t.Fatal(got, err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sends != 1 || f.aborts != 0 {
			t.Fatal(f.sends, f.aborts)
		}
	})
}

func TestCanceledReconciliationCallerDoesNotAbortNative(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &gatedHistory{fake: &fake{status: "idle"}}
		m, x := expired(t, f)
		defer m.Close()
		block := make(chan struct{})
		entered := make(chan struct{}, 1)
		f.setGate(block, entered)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := m.Get(ctx, x.ID); done <- err }()
		<-entered
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		assertBusy(t, m)
		f.setGate(nil, nil)
		lateProof(f.fake, x.MessageID, "completed")
		time.Sleep(time.Second)
		got, err := m.Get(context.Background(), x.ID)
		if err != nil || got.State != "completed" {
			t.Fatal(got, err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.aborts != 0 || f.sends != 1 {
			t.Fatal(f.aborts, f.sends)
		}
	})
}

func TestInterruptRefusesUnavailableNativeEvidence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{status: "idle"}
		m, x := expired(t, f)
		defer m.Close()
		f.mu.Lock()
		f.disconnected = true
		f.mu.Unlock()
		_, err := m.Interrupt(context.Background(), x.ID)
		var unresolved *Error
		if !errors.As(err, &unresolved) || unresolved.Code != "UNKNOWN_OUTCOME" {
			t.Fatal(err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.aborts != 0 {
			t.Fatal("abort despite unavailable native state")
		}
	})
}

type unavailablePermissions struct {
	*fake
	failRead bool
}

func (f *unavailablePermissions) Permissions(ctx context.Context) ([]opencode.Permission, error) {
	if f.failRead {
		return nil, errors.New("permissions unavailable")
	}
	return f.fake.Permissions(ctx)
}
func TestUnavailablePermissionsCannotProveTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &unavailablePermissions{fake: &fake{status: "idle"}}
		m, x := expired(t, f)
		defer m.Close()
		lateProof(f.fake, x.MessageID, "completed")
		f.failRead = true
		got, err := m.Get(context.Background(), x.ID)
		if err != nil || got.State != "unknown" || got.Ended != nil {
			t.Fatal(got, err)
		}
		assertBusy(t, m)
		f.failRead = false
		time.Sleep(time.Second)
		got, err = m.Get(context.Background(), x.ID)
		if err != nil || got.State != "completed" {
			t.Fatal(got, err)
		}
	})
}
