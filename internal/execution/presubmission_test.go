package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

type prePromptGate struct {
	*fake
	directory        string
	gets             atomic.Int32
	entered, release chan struct{}
}

func (f *prePromptGate) Get(ctx context.Context, id string) (opencode.Session, error) {
	if f.gets.Add(1) == 2 {
		close(f.entered)
		select {
		case <-f.release:
			return opencode.Session{}, errors.New("fixture pre-Prompt metadata unavailable")
		case <-ctx.Done():
			return opencode.Session{}, ctx.Err()
		}
	}
	return opencode.Session{ID: id, Directory: f.directory}, ctx.Err()
}

func assertLocalFailure(t *testing.T, v Execution) {
	t.Helper()
	if v.State != "failed" || v.LocalFailure == nil || v.LocalFailure.Code != "PRE_SUBMISSION_UNAVAILABLE" || v.LocalFailure.RecordedAt.IsZero() || v.Unresolved != nil || v.Ended != nil || v.ReconciledAt != nil {
		t.Fatalf("not an explicit local failure: %+v", v)
	}
}

func TestAcceptedNeverSubmittedTransition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &prePromptGate{fake: &fake{status: "idle"}, directory: fixtureWorkspace(t), entered: make(chan struct{}), release: make(chan struct{})}
		m := New(f)
		defer m.Close()
		x, err := m.Send(context.Background(), "ses_test", "fixture", "transition-key")
		if err != nil {
			t.Fatal(err)
		}
		<-f.entered
		r, _ := m.find(x.ID)
		r.mu.Lock()
		beforeInvocation := !r.submissionAttempted && r.view.State == "submitting"
		r.mu.Unlock()
		if !beforeInvocation {
			t.Fatal("invocation boundary crossed before metadata validation")
		}
		m.mu.Lock()
		reserved := m.busy["ses_test"] == x.ID && m.keys["ses_test\x00transition-key"] == x.ID
		m.mu.Unlock()
		if !reserved {
			t.Fatal("accepted reservation/key missing")
		}
		if _, err := m.Interrupt(context.Background(), x.ID); err == nil {
			t.Fatal("interrupt accepted before Prompt")
		}
		close(f.release)
		synctest.Wait()
		v, err := m.Snapshot(context.Background(), x.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertLocalFailure(t, v)
		assertReleased(t, m)
		m.mu.Lock()
		keyRetained := m.keys["ses_test\x00transition-key"] == x.ID
		m.mu.Unlock()
		if !keyRetained {
			t.Fatal("accepted key lost on local finalization")
		}
		r.mu.Lock()
		locallyFinal := !r.submissionAttempted && r.monitorDone
		r.mu.Unlock()
		if !locallyFinal {
			t.Fatal("incorrect local finalization boundary")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sends != 0 || f.aborts != 0 {
			t.Fatal(f.sends, f.aborts)
		}
	})
}

func TestLocalFailureImmutableAcrossReadersAndNextRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, f, x := finalReviewNeverSubmitted(t)
		defer m.Close()
		initial, err := m.Snapshot(context.Background(), x.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertLocalFailure(t, initial)
		original, _ := json.Marshal(initial)
		// Correlated-looking late fixture history must never revive this local run.
		user := opencode.Message{}
		user.Info.ID, user.Info.SessionID, user.Info.Role = x.MessageID, "ses_test", "user"
		f.mu.Lock()
		f.messages = append(f.messages, user)
		f.mu.Unlock()
		complete(f.fake, x.MessageID, "stop", "")
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				var v Execution
				var e error
				switch i % 4 {
				case 0:
					v, e = m.Get(context.Background(), x.ID)
				case 1:
					var messages []opencode.Message
					v, messages, e = m.Result(context.Background(), x.ID)
					if len(messages) != 0 {
						t.Error("invented local result messages")
					}
				case 2:
					v, e = m.Interrupt(context.Background(), x.ID)
					var domain *Error
					if !errors.As(e, &domain) || domain.Code != "INVALID_INPUT" {
						t.Errorf("local interrupt not rejected: %v", e)
					}
					e = nil
				case 3:
					v, e = m.Send(context.Background(), "ses_test", "fixture", "final-review-key")
				}
				if e != nil {
					t.Error(e)
					return
				}
				encoded, _ := json.Marshal(v)
				if string(encoded) != string(original) {
					t.Errorf("local outcome changed: %+v", v)
				}
			}(i)
		}
		wg.Wait()
		// Snapshot consumers cannot mutate the stored failure evidence.
		initial.LocalFailure.Code = "caller mutation"
		initial.LocalFailure.RecordedAt = time.Time{}
		v, _ := m.Snapshot(context.Background(), x.ID)
		assertLocalFailure(t, v)
		if _, err := m.Send(context.Background(), "ses_test", "different payload", "final-review-key"); err == nil {
			t.Fatal("local failure key lost fingerprint conflict")
		}
		next, err := m.Send(context.Background(), "ses_test", "next fixture", "next-local-key")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		r, _ := m.find(x.ID)
		// Delayed observation/finalization for the old execution cannot release next's guard.
		m.observe(context.Background(), r)
		m.finalizeUnsubmitted(r)
		m.mu.Lock()
		nextOwnsGuard := m.busy["ses_test"] == next.ID
		m.mu.Unlock()
		if !nextOwnsGuard {
			t.Fatal("old execution released next guard")
		}
		complete(f.fake, next.MessageID, "stop", "")
		finished := waitState(t, m, next.ID, "completed")
		if finished.LocalFailure != nil || finished.Ended == nil {
			t.Fatal("native result confused with local failure")
		}
		m.Close()
		v, _ = m.Snapshot(context.Background(), x.ID)
		encoded, _ := json.Marshal(v)
		if string(encoded) != string(original) {
			t.Fatal("Close changed local terminal result")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sends != 1 || f.aborts != 0 {
			t.Fatal(f.sends, f.aborts)
		}
	})
}

func TestPossiblySubmittedCannotBeLocallyFinalized(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{status: "idle", fail: true}
		m := New(f)
		defer m.Close()
		x, err := m.Send(context.Background(), "ses_test", "fixture", "possibly-key")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		r, _ := m.find(x.ID)
		r.mu.Lock()
		attempted := r.submissionAttempted
		if r.view.Unresolved != nil {
			// A stale reason must not erase internal invocation evidence.
			r.view.Unresolved.Reason = "PRE_SUBMISSION_UNAVAILABLE"
		}
		r.mu.Unlock()
		if !attempted {
			t.Fatal("Prompt invocation not tracked")
		}
		m.finalizeUnsubmitted(r)
		f.mu.Lock()
		user := f.messages[0]
		f.messages = nil
		f.mu.Unlock()
		for i := 0; i < 3; i++ {
			time.Sleep(time.Second)
			v, err := m.Get(context.Background(), x.ID)
			if err != nil || v.State != "unknown" || v.LocalFailure != nil || v.ID != x.ID || v.MessageID != x.MessageID {
				t.Fatal(v, err)
			}
			assertBusy(t, m)
		}
		replay, err := m.Send(context.Background(), "ses_test", "fixture", "possibly-key")
		if err != nil || replay.ID != x.ID {
			t.Fatal(replay, err)
		}
		f.mu.Lock()
		sends, aborts := f.sends, f.aborts
		f.messages = []opencode.Message{user}
		f.mu.Unlock()
		if sends != 1 || aborts != 0 {
			t.Fatal(sends, aborts)
		}
		complete(f, x.MessageID, "stop", "")
		time.Sleep(time.Second)
		v, err := m.Get(context.Background(), x.ID)
		if err != nil || v.State != "completed" || v.LocalFailure != nil {
			t.Fatal(v, err)
		}
	})
}

func TestClosePreservesSubmissionDisposition(t *testing.T) {
	t.Run("before-invocation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := &prePromptGate{fake: &fake{status: "idle"}, directory: fixtureWorkspace(t), entered: make(chan struct{}), release: make(chan struct{})}
			m := New(f)
			x, err := m.Send(context.Background(), "ses_test", "fixture", "close-local-key")
			if err != nil {
				t.Fatal(err)
			}
			<-f.entered
			m.Close()
			v, _ := m.Snapshot(context.Background(), x.ID)
			assertLocalFailure(t, v)
			assertReleased(t, m)
			if f.sends != 0 || f.aborts != 0 {
				t.Fatal(f.sends, f.aborts)
			}
		})
	})
	t.Run("invocation-in-flight", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := &fake{status: "idle", entered: make(chan struct{}), release: make(chan struct{})}
			m := New(f)
			x, err := m.Send(context.Background(), "ses_test", "fixture", "close-attempted-key")
			if err != nil {
				t.Fatal(err)
			}
			<-f.entered
			r, _ := m.find(x.ID)
			r.mu.Lock()
			attempted := r.submissionAttempted
			r.mu.Unlock()
			if !attempted {
				t.Fatal("invocation boundary was not marked before entering Prompt")
			}
			m.Close()
			v, _ := m.Snapshot(context.Background(), x.ID)
			if v.State != "unknown" || v.LocalFailure != nil || v.Unresolved == nil || v.Unresolved.Reason != "MANAGER_CLOSED" {
				t.Fatal(v)
			}
			m.mu.Lock()
			retained := m.busy["ses_test"] == x.ID
			m.mu.Unlock()
			if !retained {
				t.Fatal("possibly submitted Close released guard")
			}
			if f.sends != 1 || f.aborts != 0 {
				t.Fatal(f.sends, f.aborts)
			}
		})
	})
}

type finalReviewMetadata struct {
	*fake
	directory string
	gets      atomic.Int32
}

func (f *finalReviewMetadata) Get(ctx context.Context, id string) (opencode.Session, error) {
	if err := ctx.Err(); err != nil {
		return opencode.Session{}, err
	}
	if f.gets.Add(1) == 2 {
		return opencode.Session{}, errors.New("fixture transient metadata failure before Prompt")
	}
	return opencode.Session{ID: id, Directory: f.directory}, nil
}
func finalReviewNeverSubmitted(t *testing.T) (*Manager, *finalReviewMetadata, Execution) {
	t.Helper()
	f := &finalReviewMetadata{fake: &fake{status: "idle"}, directory: fixtureWorkspace(t)}
	m := New(f)
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "fixture", "final-review-key")
	if err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
	v, err := m.Snapshot(context.Background(), x.ID)
	if err != nil || (v.State != "unknown" && v.State != "failed") {
		t.Fatalf("unexpected setup: %+v %v", v, err)
	}
	f.mu.Lock()
	sends := f.sends
	f.mu.Unlock()
	if sends != 0 {
		t.Fatalf("fixture was submitted: %d", sends)
	}
	return m, f, x
}
func TestFinalReviewPreSubmissionFailureRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, f, x := finalReviewNeverSubmitted(t)
		for i := 0; i < 10; i++ {
			time.Sleep(time.Second)
			if _, err := m.Get(context.Background(), x.ID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.Result(context.Background(), x.ID); err != nil {
				t.Fatal(err)
			}
		}
		replay, err := m.Send(context.Background(), "ses_test", "fixture", "final-review-key")
		if err != nil || replay.ID != x.ID || replay.MessageID != x.MessageID {
			t.Fatalf("replay mapping changed: %+v %v", replay, err)
		}
		f.mu.Lock()
		sends := f.sends
		f.mu.Unlock()
		if sends != 0 {
			t.Fatalf("read/replay submitted native work: %d", sends)
		}
		_, err = m.Send(context.Background(), "ses_test", "next authorized fixture", "final-next-key")
		var domain *Error
		if errors.As(err, &domain) && domain.Code == "SESSION_BUSY" {
			t.Fatalf("known never-submitted run still locks healthy unchanged session after recovery reads; original state=%s unresolved=%v sends=%d", replay.State, replay.Unresolved, sends)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
}
func TestFinalReviewNeverSubmittedInterrupt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, f, x := finalReviewNeverSubmitted(t)
		_, err := m.Interrupt(context.Background(), x.ID)
		f.mu.Lock()
		sends, aborts := f.sends, f.aborts
		f.mu.Unlock()
		if sends != 0 || aborts != 0 {
			t.Fatalf("interrupt must not Abort a known never-submitted execution: sends=%d aborts=%d returned_error=%v", sends, aborts, err)
		}
		if err == nil {
			t.Fatal("interrupt of never-submitted run must be rejected")
		}
	})
}
