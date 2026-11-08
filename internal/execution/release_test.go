package execution

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// ADR-020 (b): operator-only fenced guard release. The test numbers in the
// comments follow the ADR-020 test plan.

var fastRelease = ReleaseOptions{Settle: time.Millisecond}

func wantCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
	return e
}

func guardOf(m *Manager) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy["ses_test"]
}

// injectRun publishes a run in the given state that holds the session guard,
// so the state checks are exercised independently of the guard bookkeeping.
func injectRun(t *testing.T, m *Manager, r Runner, state string, monitorDone bool) *run {
	t.Helper()
	info, err := os.Stat("/")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	x := &run{native: "ses_test", runner: r, op: make(chan struct{}, 1), directory: "/", canonicalDirectory: "/", workspace: info, monitorDone: monitorDone, submissionAttempted: true,
		view: Execution{ID: token(), SessionID: "opencode:ses_test", MessageID: messageID(), State: state, Started: now, Updated: now}}
	m.mu.Lock()
	m.runs[x.view.ID] = x
	m.busy["ses_test"] = x.view.ID
	m.mu.Unlock()
	return x
}

// releaseFixtureWith returns an unknown execution whose monitor has ended and
// whose correlated user message exists natively, submitted through r.
func releaseFixtureWith(t *testing.T, r Runner) (*Manager, Execution) {
	t.Helper()
	m := NewWithTiming(r, 50*time.Millisecond, 2*time.Millisecond)
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "prompt", "monitor-key")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		v, err := m.Snapshot(context.Background(), x.ID)
		if err == nil && v.MonitorEndedAt != nil && v.State == "unknown" {
			return m, v
		}
		if time.Now().After(deadline) {
			t.Fatalf("monitor did not end: %+v %v", v, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// wantKept checks that a refused release left the guard and the record alone.
func wantKept(t *testing.T, m *Manager, before Execution) {
	t.Helper()
	if g := guardOf(m); g != before.ID {
		t.Fatalf("guard = %q, want kept %q", g, before.ID)
	}
	v, err := m.Snapshot(context.Background(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != before.State || v.Error != before.Error || v.GuardReleasedAt != nil {
		t.Fatalf("refused release changed the record: before %+v after %+v", before, v)
	}
}

// Test 1: a newer execution holds the guard.
func TestReleaseHolderMismatch(t *testing.T) {
	f := &fake{status: "idle"}
	m, x := releaseFixtureWith(t, f)
	newer := injectRun(t, m, f, "running", false)
	_, err := m.ReleaseGuard(context.Background(), "ses_test", x.ID, fastRelease)
	wantCode(t, err, "INVALID_INPUT")
	if g := guardOf(m); g != newer.view.ID {
		t.Fatalf("newer guard changed: %q, want %q", g, newer.view.ID)
	}
	v, _ := m.Snapshot(context.Background(), x.ID)
	if v.State != "unknown" || v.GuardReleasedAt != nil {
		t.Fatalf("old record changed: %+v", v)
	}
}

func TestReleaseInputAndUnknownExecution(t *testing.T) {
	f := &fake{status: "idle"}
	m, x := releaseFixtureWith(t, f)
	for _, c := range []struct{ session, id string }{
		{"opencode:ses_test", x.ID}, {"ses_test", "not-an-id"}, {"ses_test", "0f1e2d3c4b5a69788796a5b4c3d2e1f0"}, {"ses_other", x.ID}, {"", x.ID},
	} {
		_, err := m.ReleaseGuard(context.Background(), c.session, c.id, fastRelease)
		wantCode(t, err, "INVALID_INPUT")
	}
	wantKept(t, m, x)
}

// Test 2: only unknown with an ended monitor may release.
func TestReleaseRefusesStates(t *testing.T) {
	cases := []struct {
		state       string
		monitorDone bool
	}{
		{"submitting", false}, {"running", false}, {"waiting_for_permission", true},
		{"completed", true}, {"failed", true}, {"interrupted", true}, {"unknown", false},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			f := &fake{status: "idle"}
			m := setup(t, f)
			x := injectRun(t, m, f, c.state, c.monitorDone)
			_, err := m.ReleaseGuard(context.Background(), "ses_test", x.view.ID, fastRelease)
			wantCode(t, err, "INVALID_INPUT")
			if g := guardOf(m); g != x.view.ID {
				t.Fatalf("guard released for state %s", c.state)
			}
			x.mu.Lock()
			defer x.mu.Unlock()
			if x.view.State != c.state || x.view.GuardReleasedAt != nil {
				t.Fatalf("record changed: %+v", x.view)
			}
		})
	}
}

// releaseRunner adds identity, blocking and overlap probes to the fake.
type releaseRunner struct {
	*fake
	mu          sync.Mutex
	directory   string
	blockStatus bool
	inflight    atomic.Int32
	overlap     atomic.Bool
	pause       time.Duration
}

func (r *releaseRunner) enter() func() {
	if r.inflight.Add(1) > 1 {
		r.overlap.Store(true)
	}
	if r.pause > 0 {
		time.Sleep(r.pause)
	}
	return func() { r.inflight.Add(-1) }
}
func (r *releaseRunner) Get(ctx context.Context, id string) (opencode.Session, error) {
	defer r.enter()()
	s, err := r.fake.Get(ctx, id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.directory != "" {
		s.Directory = r.directory
	}
	return s, err
}
func (r *releaseRunner) Status(ctx context.Context) (map[string]opencode.Status, error) {
	defer r.enter()()
	r.mu.Lock()
	block := r.blockStatus
	r.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.fake.Status(ctx)
}
func (r *releaseRunner) History(ctx context.Context, id string) ([]opencode.Message, error) {
	defer r.enter()()
	return r.fake.History(ctx, id)
}
func (r *releaseRunner) Permissions(ctx context.Context) ([]opencode.Permission, error) {
	defer r.enter()()
	return r.fake.Permissions(ctx)
}

func unfinishedElsewhere(f *fake) {
	msg := opencode.Message{}
	msg.Info.ID = "msg_orphan"
	msg.Info.Role = "assistant"
	msg.Info.SessionID = "ses_test"
	msg.Info.ParentID = "msg_somebody_else"
	f.messages = append(f.messages, msg)
}

// Test 3: native evidence that is not quiescent, or not readable, refuses.
func TestReleaseRefusesNativeEvidence(t *testing.T) {
	cases := []struct {
		name  string
		code  string
		setup func(*Manager, *releaseRunner)
	}{
		{"native_busy", "SESSION_BUSY", func(_ *Manager, r *releaseRunner) { locked(r.fake, func() { r.fake.status = "busy" }) }},
		{"native_retry", "SESSION_BUSY", func(_ *Manager, r *releaseRunner) { locked(r.fake, func() { r.fake.status = "retry" }) }},
		{"unsupported_status", "SESSION_BUSY", func(_ *Manager, r *releaseRunner) { locked(r.fake, func() { r.fake.status = "compacting" }) }},
		{"unfinished_assistant_anywhere", "SESSION_BUSY", func(_ *Manager, r *releaseRunner) { locked(r.fake, func() { unfinishedElsewhere(r.fake) }) }},
		{"pending_permission", "SESSION_BUSY", func(_ *Manager, r *releaseRunner) { locked(r.fake, func() { r.fake.permission = true }) }},
		{"identity_mismatch", "FORBIDDEN", func(_ *Manager, r *releaseRunner) { r.mu.Lock(); r.directory = "/tmp"; r.mu.Unlock() }},
		{"read_error", "UPSTREAM_FAILURE", func(_ *Manager, r *releaseRunner) { locked(r.fake, func() { r.fake.disconnected = true }) }},
		{"bounded_read_timeout", "UPSTREAM_TIMEOUT", func(m *Manager, r *releaseRunner) {
			m.reconcileTimeout = 100 * time.Millisecond
			r.mu.Lock()
			r.blockStatus = true
			r.mu.Unlock()
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &releaseRunner{fake: &fake{status: "idle"}}
			m, x := releaseFixtureWith(t, r)
			c.setup(m, r)
			start := time.Now()
			_, err := m.ReleaseGuard(context.Background(), "ses_test", x.ID, fastRelease)
			wantCode(t, err, c.code)
			if took := time.Since(start); took > 3*time.Second {
				t.Fatalf("refusal took %v", took)
			}
			wantKept(t, m, x)
		})
	}
}

// Test 4: a missing correlated user message needs the explicit override.
func TestReleaseMissingUserMessage(t *testing.T) {
	f := &fake{status: "idle"}
	m, x := releaseFixtureWith(t, f)
	locked(f, func() { f.messages = nil })
	ev, err := m.ReleaseGuard(context.Background(), "ses_test", x.ID, fastRelease)
	wantCode(t, err, "INVALID_INPUT")
	if ev.CorrelatedUser != "absent" {
		t.Fatalf("evidence correlated_user = %q, want absent", ev.CorrelatedUser)
	}
	wantKept(t, m, x)
	ev, err = m.ReleaseGuard(context.Background(), "ses_test", x.ID, ReleaseOptions{Settle: time.Millisecond, AllowMissingUserMessage: true})
	if err != nil {
		t.Fatal(err)
	}
	if ev.CorrelatedUser != "absent" || ev.GuardReleasedAt == nil {
		t.Fatalf("override evidence: %+v", ev)
	}
	if g := guardOf(m); g != "" {
		t.Fatalf("guard still held: %q", g)
	}
}

func sends(f *fake) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends
}

// Test 5: success keeps the record, the key and the fingerprint.
func TestReleaseSuccess(t *testing.T) {
	f := &fake{status: "idle"}
	m, before := releaseFixtureWith(t, f)
	ev, err := m.ReleaseGuard(context.Background(), "ses_test", before.ID, fastRelease)
	if err != nil {
		t.Fatal(err)
	}
	if !ev.NativeRead || ev.NativeState != "idle" || ev.Unfinished || ev.PermissionPending || ev.CorrelatedUser != "found" || ev.SettleMS != 1 || ev.GuardReleasedAt == nil {
		t.Fatalf("evidence: %+v", ev)
	}
	wantGuard(t, m, false, "")
	after, err := m.Snapshot(context.Background(), before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "unknown" || after.Error != before.Error || after.Unresolved == nil || before.Unresolved == nil ||
		after.Unresolved.Reason != before.Unresolved.Reason || after.Ended != nil || !after.Updated.Equal(before.Updated) ||
		after.GuardReleasedAt == nil || !after.GuardReleasedAt.Equal(*ev.GuardReleasedAt) {
		t.Fatalf("release changed the record: before %+v after %+v", before, after)
	}
	if n := sends(f); n != 1 {
		t.Fatalf("Prompt calls before replay = %d", n)
	}
	again, replay, err := m.SendWithRunnerOutcome(context.Background(), f, "ses_test", "prompt", "monitor-key")
	if err != nil || !replay || again.ID != before.ID || again.State != "unknown" {
		t.Fatalf("old key did not replay: %+v replay=%v err=%v", again, replay, err)
	}
	if _, err := m.Send(context.Background(), "ses_test", "changed", "monitor-key"); err == nil {
		t.Fatal("fingerprint dropped: old key accepted a different payload")
	}
	if n := sends(f); n != 1 {
		t.Fatalf("replay submitted again: %d Prompt calls", n)
	}
	newer, replay, err := m.SendWithRunnerOutcome(context.Background(), f, "ses_test", "next", "fresh-key-1")
	if err != nil || replay || newer.ID == before.ID {
		t.Fatalf("new key not admitted: %+v replay=%v err=%v", newer, replay, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for sends(f) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if n := sends(f); n != 2 {
		t.Fatalf("new key: %d Prompt calls in total, want exactly 2", n)
	}
	// A second release of the old execution is refused: it no longer holds the guard.
	_, err = m.ReleaseGuard(context.Background(), "ses_test", before.ID, fastRelease)
	wantCode(t, err, "INVALID_INPUT")
	if g := guardOf(m); g != newer.ID {
		t.Fatalf("newer guard changed: %q", g)
	}
}

// Test 6: ADR-011 promotion still applies after a release and never removes a
// newer execution's guard.
func TestReleaseLateCompletionKeepsNewerGuard(t *testing.T) {
	f := &fake{status: "idle"}
	m, old := releaseFixtureWith(t, f)
	if _, err := m.ReleaseGuard(context.Background(), "ses_test", old.ID, fastRelease); err != nil {
		t.Fatal(err)
	}
	newer, err := m.Send(context.Background(), "ses_test", "next", "newer-key-1")
	if err != nil {
		t.Fatal(err)
	}
	complete(f, old.MessageID, "stop", "")
	done := waitState(t, m, old.ID, "completed")
	if done.Ended == nil || done.GuardReleasedAt == nil {
		t.Fatalf("promoted record: %+v", done)
	}
	if g := guardOf(m); g != newer.ID {
		t.Fatalf("late promotion removed the newer guard: %q, want %q", g, newer.ID)
	}
	if held, _ := m.SessionGuard("ses_test"); !held {
		t.Fatal("SessionGuard reports released while the newer execution holds it")
	}
}

// Test 7: native evidence that changes between the two settle reads refuses.
func TestReleaseSettleChangeRefused(t *testing.T) {
	cases := []struct {
		name   string
		change func(*fake)
	}{
		{"history", func(f *fake) {
			msg := opencode.Message{}
			msg.Info.ID = "msg_zz_new"
			msg.Info.Role = "user"
			msg.Info.SessionID = "ses_test"
			f.messages = append(f.messages, msg)
		}},
		{"status", func(f *fake) { f.missing = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{status: "idle"}
			m, x := releaseFixtureWith(t, f)
			m.releaseSleep = func(context.Context, time.Duration) error {
				locked(f, func() { c.change(f) })
				return nil
			}
			_, err := m.ReleaseGuard(context.Background(), "ses_test", x.ID, fastRelease)
			wantCode(t, err, "SESSION_BUSY")
			wantKept(t, m, x)
		})
	}
}

// Test 8: release, refresh and Interrupt on the same run serialize through
// x.op, so their native reads never overlap. Run with -race -count=50.
func TestReleaseSerializesWithRefreshAndInterrupt(t *testing.T) {
	r := &releaseRunner{fake: &fake{status: "idle"}}
	m, x := releaseFixtureWith(t, r)
	r.pause = 200 * time.Microsecond
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if i%2 == 0 {
					_, _ = m.Get(context.Background(), x.ID)
				} else {
					_, _ = m.Interrupt(context.Background(), x.ID)
				}
			}
		}(i)
	}
	_, err := m.ReleaseGuard(context.Background(), "ses_test", x.ID, ReleaseOptions{Settle: 2 * time.Millisecond})
	close(stop)
	wg.Wait()
	if r.overlap.Load() {
		t.Fatal("native reads of one run overlapped: release did not serialize through x.op")
	}
	v, _ := m.Snapshot(context.Background(), x.ID)
	if v.State != "unknown" {
		t.Fatalf("state changed: %+v", v)
	}
	switch {
	case err == nil:
		if guardOf(m) != "" || v.GuardReleasedAt == nil {
			t.Fatalf("released without effect: guard=%q %+v", guardOf(m), v)
		}
	default:
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("untyped error: %v", err)
		}
		if guardOf(m) != x.ID || v.GuardReleasedAt != nil {
			t.Fatalf("refused (%v) but guard=%q %+v", err, guardOf(m), v)
		}
	}
}

// Test 12: the settle sleep does not hold x.op, so a poll returns normally.
func TestReleaseSettleDoesNotBlockPollers(t *testing.T) {
	f := &fake{status: "idle"}
	m, x := releaseFixtureWith(t, f)
	m.reconcileTimeout = 300 * time.Millisecond
	inSleep, resume := make(chan struct{}), make(chan struct{})
	m.releaseSleep = func(context.Context, time.Duration) error {
		close(inSleep)
		<-resume
		return nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.ReleaseGuard(context.Background(), "ses_test", x.ID, fastRelease)
		result <- err
	}()
	select {
	case <-inSleep:
	case err := <-result:
		t.Fatalf("release returned before its settle sleep: %v", err)
	}
	start := time.Now()
	_, err := m.Get(context.Background(), x.ID)
	took := time.Since(start)
	close(resume)
	if err != nil {
		t.Fatalf("status poll during the settle sleep failed after %v: %v", took, err)
	}
	if took >= m.reconcileTimeout {
		t.Fatalf("status poll took %v, the reconcile bound", took)
	}
	if err := <-result; err != nil {
		t.Fatalf("release after the poll: %v", err)
	}
}

// Test 13: a promotion during the settle sleep, followed by a newer execution,
// makes the re-check after re-taking x.op refuse before any second read.
func TestReleasePromotionDuringSettle(t *testing.T) {
	f := &fake{status: "idle"}
	m, x := releaseFixtureWith(t, f)
	var newer Execution
	m.releaseSleep = func(context.Context, time.Duration) error {
		complete(f, x.MessageID, "stop", "")
		waitState(t, m, x.ID, "completed")
		var err error
		if newer, err = m.Send(context.Background(), "ses_test", "next", "newer-key-1"); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	_, err := m.ReleaseGuard(context.Background(), "ses_test", x.ID, fastRelease)
	e := wantCode(t, err, "INVALID_INPUT")
	if e.Message != "guard is not held by this execution" {
		t.Fatalf("refused for the wrong reason: %q", e.Message)
	}
	if g := guardOf(m); g != newer.ID {
		t.Fatalf("newer guard = %q, want %q", g, newer.ID)
	}
	v, _ := m.Snapshot(context.Background(), x.ID)
	if v.State != "completed" || v.GuardReleasedAt != nil {
		t.Fatalf("promoted record: %+v", v)
	}
}

func TestReleaseClosedManager(t *testing.T) {
	f := &fake{status: "idle"}
	m, x := releaseFixtureWith(t, f)
	m.Close()
	_, err := m.ReleaseGuard(context.Background(), "ses_test", x.ID, fastRelease)
	wantCode(t, err, "UNKNOWN_OUTCOME")
}

func TestReleaseSnapshotCopiesReleaseTime(t *testing.T) {
	now := time.Now()
	v := snapshot(Execution{GuardReleasedAt: &now})
	if v.GuardReleasedAt == &now {
		t.Fatal("snapshot shares guard_released_at")
	}
}
