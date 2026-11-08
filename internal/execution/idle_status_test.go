package execution

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// #86 finding 3 (P3): while the monitor runs, an explicit native "idle" entry
// without terminal proof is treated exactly like an absent entry: unknown
// OBSERVATION_UNAVAILABLE, never "running". busy and retry still report
// running, and terminal proof with idle still completes.
func TestExplicitIdleWithoutProofIsLikeAbsent(t *testing.T) {
	observe := func(t *testing.T, set func(*fake)) Execution {
		t.Helper()
		var got Execution
		synctest.Test(t, func(t *testing.T) {
			f := &fake{status: "idle"}
			m := NewWithTiming(f, 300*time.Second, time.Second)
			defer m.Close()
			x, err := m.Send(context.Background(), "ses_test", "prompt", "idle-key-1")
			if err != nil {
				t.Fatal(err)
			}
			locked(f, func() { set(f) })
			synctest.Wait()
			time.Sleep(time.Second)
			synctest.Wait()
			if got, err = m.Get(context.Background(), x.ID); err != nil {
				t.Fatal(err)
			}
			guardHeld(t, m)
		})
		return got
	}
	idle := observe(t, func(f *fake) { f.nativeStatus = &opencode.Status{Type: "idle"} })
	absent := observe(t, func(f *fake) { f.missing = true })
	for name, v := range map[string]Execution{"idle": idle, "absent": absent} {
		if v.State != "unknown" || v.Unresolved == nil || v.Unresolved.Reason != "OBSERVATION_UNAVAILABLE" || v.Ended != nil {
			t.Fatalf("%s without terminal proof: %+v", name, v)
		}
	}
	if idle.State != absent.State || idle.Error != absent.Error || idle.Unresolved.Reason != absent.Unresolved.Reason {
		t.Fatalf("explicit idle differs from absent:\nidle   %+v\nabsent %+v", idle, absent)
	}
	for _, s := range []string{"busy", "retry"} {
		v := observe(t, func(f *fake) { f.nativeStatus = &opencode.Status{Type: s} })
		if v.State != "running" || v.Unresolved != nil {
			t.Fatalf("%s: %+v", s, v)
		}
	}
	unsupported := observe(t, func(f *fake) { f.nativeStatus = &opencode.Status{Type: "compacting"} })
	if unsupported.State != "unknown" || unsupported.Unresolved == nil || unsupported.Unresolved.Reason != "OBSERVATION_UNAVAILABLE" {
		t.Fatalf("unsupported status: %+v", unsupported)
	}
}

// Explicit idle followed by native busy returns to running, and explicit idle
// with terminal proof completes and releases the guard.
func TestExplicitIdleRecovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{status: "idle"}
		m := NewWithTiming(f, 300*time.Second, time.Second)
		defer m.Close()
		x, err := m.Send(context.Background(), "ses_test", "prompt", "idle-key-2")
		if err != nil {
			t.Fatal(err)
		}
		step := func(set func(*fake)) Execution {
			locked(f, func() { set(f) })
			synctest.Wait()
			time.Sleep(time.Second)
			synctest.Wait()
			v, err := m.Get(context.Background(), x.ID)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		if v := step(func(f *fake) { f.nativeStatus = &opencode.Status{Type: "idle"} }); v.State != "unknown" {
			t.Fatalf("idle: %+v", v)
		}
		if v := step(func(f *fake) { f.nativeStatus = &opencode.Status{Type: "busy"} }); v.State != "running" || v.Unresolved != nil || v.Error != "" {
			t.Fatalf("busy after idle: %+v", v)
		}
		complete(f, x.MessageID, "stop", "")
		if v := step(func(f *fake) { f.nativeStatus = &opencode.Status{Type: "idle"} }); v.State != "completed" || v.Ended == nil {
			t.Fatalf("idle with proof: %+v", v)
		}
		assertReleased(t, m)
	})
}
