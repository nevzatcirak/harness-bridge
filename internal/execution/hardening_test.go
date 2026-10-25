package execution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

type workspaceRunner struct {
	*fake
	directory     string
	gets          atomic.Int32
	onGet         func(int32)
	onPermissions func()
}

func (f *workspaceRunner) Get(ctx context.Context, id string) (opencode.Session, error) {
	if err := ctx.Err(); err != nil {
		return opencode.Session{}, err
	}
	n := f.gets.Add(1)
	if f.onGet != nil {
		f.onGet(n)
	}
	return opencode.Session{ID: id, Directory: f.directory}, nil
}
func (f *workspaceRunner) Permissions(ctx context.Context) ([]opencode.Permission, error) {
	if f.onPermissions != nil {
		f.onPermissions()
	}
	return f.fake.Permissions(ctx)
}
func replaceWorkspace(t *testing.T, directory string, symlink bool) {
	t.Helper()
	if err := os.Rename(directory, directory+"-original"); err != nil {
		t.Fatal(err)
	}
	if symlink {
		if err := os.Symlink(t.TempDir(), directory); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
}
func denied(t *testing.T, err error) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != "FORBIDDEN" {
		t.Fatalf("expected workspace denial: %v", err)
	}
}
func fixtureWorkspace(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestReplacedWorkspaceCannotReconcileOrReplay(t *testing.T) {
	for _, link := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-path-new-inode", true: "symlink"}[link], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &workspaceRunner{fake: &fake{status: "idle"}, directory: fixtureWorkspace(t)}
				m, x := expired(t, f)
				defer m.Close()
				complete(f.fake, x.MessageID, "stop", "")
				replaceWorkspace(t, f.directory, link)
				_, msgs, err := m.Result(context.Background(), x.ID)
				denied(t, err)
				if len(msgs) != 0 {
					t.Fatal("replacement exposed result")
				}
				_, err = m.Send(context.Background(), "ses_test", "controlled fixture", "late-key")
				denied(t, err)
				v, _ := m.Snapshot(context.Background(), x.ID)
				if v.State != "unknown" || v.Ended != nil {
					t.Fatal("replacement proved terminal", v)
				}
				assertBusy(t, m)
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.sends != 1 || f.aborts != 0 {
					t.Fatal("unexpected mutation", f.sends, f.aborts)
				}
			})
		})
	}
}
func TestTerminalCacheRejectsReplacedWorkspace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &workspaceRunner{fake: &fake{status: "idle"}, directory: fixtureWorkspace(t)}
		m, x := expired(t, f)
		defer m.Close()
		complete(f.fake, x.MessageID, "stop", "")
		v, msgs, err := m.Result(context.Background(), x.ID)
		if err != nil || v.State != "completed" || len(msgs) != 2 {
			t.Fatal(v, err)
		}
		replaceWorkspace(t, f.directory, false)
		denied(t, m.ValidateSession(x.ID, opencode.Session{ID: "ses_test", Directory: f.directory}))
		_, msgs, err = m.Result(context.Background(), x.ID)
		denied(t, err)
		if len(msgs) != 0 {
			t.Fatal("terminal result leaked")
		}
		// Denial does not rewrite the immutable terminal record.
		v, _ = m.Snapshot(context.Background(), x.ID)
		if v.State != "completed" {
			t.Fatal(v)
		}
	})
}
func TestReplacementDuringObservationPreventsTerminalAndAbort(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "result", true: "interrupt"}[interrupt], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &workspaceRunner{fake: &fake{status: "idle"}, directory: fixtureWorkspace(t)}
				m, x := expired(t, f)
				defer m.Close()
				if !interrupt {
					complete(f.fake, x.MessageID, "stop", "")
				}
				f.onPermissions = func() { replaceWorkspace(t, f.directory, false) }
				var err error
				if interrupt {
					_, err = m.Interrupt(context.Background(), x.ID)
				} else {
					_, _, err = m.Result(context.Background(), x.ID)
				}
				denied(t, err)
				assertBusy(t, m)
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.aborts != 0 {
					t.Fatal("aborted changed workspace")
				}
			})
		})
	}
}
func TestReplacementImmediatelyBeforeSubmissionPreventsPrompt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &workspaceRunner{fake: &fake{status: "idle"}, directory: fixtureWorkspace(t)}
		var mutationErr error
		f.onGet = func(n int32) {
			if n == 2 {
				mutationErr = os.Rename(f.directory, f.directory+"-original")
				if mutationErr == nil {
					mutationErr = os.Mkdir(f.directory, 0700)
				}
			}
		}
		m := New(f)
		defer m.Close()
		x, err := m.Send(context.Background(), "ses_test", "fixture", "pre-submit-key")
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if mutationErr != nil {
			t.Fatal(mutationErr)
		}
		v, err := m.Snapshot(context.Background(), x.ID)
		if err != nil || v.State != "unknown" || v.Unresolved == nil || v.Unresolved.Reason != "PRE_SUBMISSION_UNAVAILABLE" {
			t.Fatal(v, err)
		}
		assertBusy(t, m)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.sends != 0 {
			t.Fatal("submitted into replacement")
		}
	})
}

// The native preflight intentionally waits for cancellation, then pauses cleanup.
// Close must not return until that cleanup and the reservation have been joined.
type closingPreflight struct {
	*fake
	entered, canceled, release chan struct{}
}

func (f *closingPreflight) Get(ctx context.Context, id string) (opencode.Session, error) {
	close(f.entered)
	<-ctx.Done()
	close(f.canceled)
	<-f.release
	return opencode.Session{}, ctx.Err()
}
func TestCloseCancelsAndJoinsAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &closingPreflight{fake: &fake{status: "idle"}, entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
		m := New(f)
		sent := make(chan error, 1)
		go func() { _, err := m.Send(context.Background(), "ses_test", "fixture", "closing-key"); sent <- err }()
		<-f.entered
		closed := make(chan struct{})
		go func() { m.Close(); close(closed) }()
		<-f.canceled
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("Close returned with admission still running")
		default:
		}
		close(f.release)
		<-closed
		if err := <-sent; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.busy)+len(m.pending)+len(m.keys)+len(m.fingerprints) != 0 {
			t.Fatal("admission state leaked")
		}
		if f.sends != 0 {
			t.Fatal("closed manager submitted prompt")
		}
	})
}
func TestAdmissionHasBoundedDeadlineAndReleasesReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		close(release)
		f := &closingPreflight{fake: &fake{status: "idle"}, entered: make(chan struct{}), canceled: make(chan struct{}), release: release}
		m := New(f)
		defer m.Close()
		started := time.Now()
		_, err := m.Send(context.Background(), "ses_test", "fixture", "deadline-key")
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != 5*time.Second {
			t.Fatal("unbounded admission", err, time.Since(started))
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.busy)+len(m.pending)+len(m.keys)+len(m.fingerprints) != 0 {
			t.Fatal("failed admission retained reservations")
		}
	})
}

type admissionLoad struct {
	*fake
	active, max atomic.Int32
}

func (f *admissionLoad) Get(ctx context.Context, id string) (opencode.Session, error) {
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for previous := f.max.Load(); n > previous; previous = f.max.Load() {
		if f.max.CompareAndSwap(previous, n) {
			break
		}
	}
	<-ctx.Done()
	return opencode.Session{}, ctx.Err()
}
func TestAdmissionConcurrencyAndQueueDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &admissionLoad{fake: &fake{status: "idle"}}
		m := New(f)
		defer m.Close()
		done := make(chan error, 20)
		for i := 0; i < 20; i++ {
			go func(i int) {
				_, err := m.Send(context.Background(), fmt.Sprintf("ses_%d", i), "fixture", "load-key")
				done <- err
			}(i)
		}
		synctest.Wait()
		if f.active.Load() != 4 || f.max.Load() != 4 {
			t.Fatal("admission not bounded", f.active.Load(), f.max.Load())
		}
		time.Sleep(5 * time.Second)
		for i := 0; i < 20; i++ {
			if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		}
		if f.active.Load() != 0 || f.max.Load() != 4 {
			t.Fatal("admission leaked native reads")
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.busy)+len(m.pending)+len(m.fingerprints)+len(m.keys) != 0 {
			t.Fatal("queue timeout retained reservations")
		}
	})
}
