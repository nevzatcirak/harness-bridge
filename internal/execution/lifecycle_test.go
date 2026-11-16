package execution

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func envelopeCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func envelopeReason(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ""
}

// lifecycleToken returns the stored reservation token (test only).
func (m *Manager) lifecycleToken(session string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lifecycle[session]
}

// ADR-022 5.1 / ADR-025 4.3, 4.5 (S6): the lifecycle reservation kind and the archive refusal.

func TestLifecycleReservationExcludesSendAndIsReleased(t *testing.T) {
	ctx := context.Background()
	m := setup(t, &fake{status: "idle"})
	release, err := m.ReserveLifecycle("ses_test")
	if err != nil {
		t.Fatal(err)
	}
	if !m.LifecycleGuard("ses_test") {
		t.Fatal("reservation not reported held")
	}
	if held, state := m.SessionGuard("ses_test"); !held || state != "submitting" {
		t.Fatalf("SessionGuard = (%v,%q), want (true,submitting)", held, state)
	}
	if _, e := m.Send(ctx, "ses_test", "prompt", "key-lifecycle-1"); envelopeCode(e) != "SESSION_BUSY" {
		t.Fatalf("Send during reservation = %v, want SESSION_BUSY", e)
	}
	if _, e := m.ReserveLifecycle("ses_test"); envelopeCode(e) != "SESSION_BUSY" {
		t.Fatalf("second reservation = %v, want SESSION_BUSY", e)
	}
	release()
	release() // idempotent
	if m.LifecycleGuard("ses_test") {
		t.Fatal("reservation still held after release")
	}
	if _, e := m.Send(ctx, "ses_test", "prompt", "key-lifecycle-2"); e != nil {
		t.Fatalf("Send after release: %v", e)
	}
}

func TestLifecycleReservationRefusedWhileAnExecutionHoldsTheGuard(t *testing.T) {
	ctx := context.Background()
	f := &fake{status: "idle"}
	m := setup(t, f)
	x, e := m.Send(ctx, "ses_test", "prompt", "key-lifecycle-3")
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, m, x.ID, "running")
	if _, e := m.ReserveLifecycle("ses_test"); envelopeCode(e) != "SESSION_BUSY" {
		t.Fatalf("reservation over a held guard = %v, want SESSION_BUSY", e)
	}
}

// The reservation is a separate kind: no execution ID can match it (ADR-020 release compares IDs).
func TestLifecycleReservationIsNotAnExecutionID(t *testing.T) {
	m := setup(t, &fake{status: "idle"})
	release, err := m.ReserveLifecycle("ses_test")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	m.mu.Lock()
	busy := m.busy["ses_test"]
	m.mu.Unlock()
	if busy != "" {
		t.Fatalf("reservation stored in busy as %q", busy)
	}
	if _, e := m.ReleaseGuard(context.Background(), "ses_test", m.lifecycleToken("ses_test"), ReleaseOptions{}); e == nil {
		t.Fatal("ADR-020 release acted on a lifecycle reservation")
	}
	if !m.LifecycleGuard("ses_test") {
		t.Fatal("release removed the reservation")
	}
	view := m.DashboardSnapshot()
	g, ok := view.Guards["opencode:ses_test"]
	if !ok || g.State != "submitting" {
		t.Fatalf("dashboard guard = %+v ok=%v, want held submitting", g, ok)
	}
}

func TestSendRefusesAnArchivedSessionWithoutIO(t *testing.T) {
	ctx := context.Background()
	f := &fake{status: "idle"}
	m := setup(t, f)
	archived := true
	m.SetArchiveCheck(func(s string) bool { return archived && s == "ses_test" })
	_, e := m.Send(ctx, "ses_test", "prompt", "key-archived-1")
	if envelopeCode(e) != "FORBIDDEN" || envelopeReason(e) != "session_archived" {
		t.Fatalf("Send to archived = %v, want FORBIDDEN/session_archived", e)
	}
	if r := f.reads(); r != [4]int{} {
		t.Fatalf("archived refusal made native reads %v, want none", r)
	}
	archived = false
	if _, e := m.Send(ctx, "ses_test", "prompt", "key-archived-1"); e != nil {
		t.Fatalf("Send after un-archive: %v", e)
	}
}

// ADR-025 4.5: the check sits after the idempotent-replay branch: a replay of an execution
// accepted before the archive returns it and submits nothing.
func TestArchivedCheckComesAfterTheReplayBranch(t *testing.T) {
	ctx := context.Background()
	m := setup(t, &fake{status: "idle"})
	x, e := m.Send(ctx, "ses_test", "prompt", "key-archived-2")
	if e != nil {
		t.Fatal(e)
	}
	m.SetArchiveCheck(func(string) bool { return true })
	y, replay, e := m.SendWithRunnerOutcome(ctx, m.r, "ses_test", "prompt", "key-archived-2")
	if e != nil || !replay || y.ID != x.ID {
		t.Fatalf("replay after archive = (%v,%v,%v), want the original execution", y.ID, replay, e)
	}
	if _, e := m.Send(ctx, "ses_test", "other", "key-archived-3"); envelopeCode(e) != "FORBIDDEN" {
		t.Fatalf("new send after archive = %v, want FORBIDDEN", e)
	}
}

// The interleaving of ADR-025 test 6: the archive check and the busy check are one critical section.
func TestArchivedCheckAndBusyCheckShareTheCriticalSection(t *testing.T) {
	m := setup(t, &fake{status: "idle"})
	var mu sync.Mutex
	state := false
	m.SetArchiveCheck(func(string) bool { mu.Lock(); defer mu.Unlock(); return state })
	release, err := m.ReserveLifecycle("ses_test")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	state = true // the archive commits and publishes while it still holds the reservation
	mu.Unlock()
	release()
	if _, e := m.Send(context.Background(), "ses_test", "p", "key-archived-4"); envelopeCode(e) != "FORBIDDEN" {
		t.Fatalf("send after commit = %v, want FORBIDDEN", e)
	}
}
