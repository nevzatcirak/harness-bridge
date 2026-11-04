package execution

import (
	"context"
	"sync"
	"testing"
)

// AUDIT-01: SendWithRunnerOutcome reports the manager's own replay decision and
// is otherwise identical to SendWithRunner.
func TestSendOutcomeReplayIsManagerDecision(t *testing.T) {
	f := &fake{status: "idle", entered: make(chan struct{}), release: make(chan struct{})}
	m := setup(t, f)
	ctx := context.Background()
	x, replay, e := m.SendWithRunnerOutcome(ctx, f, "ses_test", "test", "outcome-key")
	if e != nil || replay {
		t.Fatalf("first submission: replay=%v err=%v", replay, e)
	}
	<-f.entered
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			y, replay, e := m.SendWithRunnerOutcome(ctx, f, "ses_test", "test", "outcome-key")
			if e != nil || !replay || y.ID != x.ID {
				t.Errorf("same key and payload: id=%s replay=%v err=%v", y.ID, replay, e)
			}
		}()
	}
	wg.Wait()
	if _, replay, e := m.SendWithRunnerOutcome(ctx, f, "ses_test", "changed", "outcome-key"); e == nil || replay {
		t.Fatalf("conflict: replay=%v err=%v", replay, e)
	}
	if _, replay, e := m.SendWithRunnerOutcome(ctx, f, "ses_test", "test", "outcome-other"); e == nil || replay {
		t.Fatalf("busy: replay=%v err=%v", replay, e)
	}
	close(f.release)
	waitState(t, m, x.ID, "running")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends != 1 {
		t.Fatal(f.sends)
	}
}
