package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Real processes, real signals (the fake CLI is this test binary behind a wrapper script):
// the §4 escalation, timing and the absence of SIGKILL.

func realS3(c *Config) { c.IntWait, c.TermWait = 400*time.Millisecond, 400*time.Millisecond }

// runningWithInit starts a run and waits until the real child has installed its signal
// handlers and written its init line (it then drops a "ready" file in the root).
func (i *integ) runningWithInit(key string) string {
	i.t.Helper()
	id := i.run(key)
	until(i.t, "the fake CLI to be ready", func() bool {
		_, err := os.Stat(filepath.Join(i.root(1), "ready"))
		return err == nil
	})
	time.Sleep(60 * time.Millisecond) // the runner has read the init line
	return id
}

func TestRealUserInterruptAckedByAbortedIsInterrupted(t *testing.T) {
	// IntWait is long on purpose: a -race build of the fake CLI can take a while to exit,
	// and an acknowledged interrupt must not be escalated into the SIGTERM path.
	i := newInteg(t, "init-abort", "-", func(c *Config) { c.IntWait, c.TermWait = 5*time.Second, 5*time.Second })
	id := i.runningWithInit("key-real-int-0001")
	if _, err := i.ex.Interrupt(id); err != nil {
		t.Fatal(err)
	}
	x := i.wait(id)
	v := i.verdict(id)
	if x.State != "interrupted" || v.Row != "5" || !v.GuardReleased || v.ExitClass != "zero" {
		t.Fatalf("state %s verdict %+v", x.State, v)
	}
}

// SIGINT ignored, SIGTERM delivered after IntWait to the real process group: the child dies
// of the signal, there is no result, and a user interrupt makes that row 6 / ABORT_UNCONFIRMED.
func TestRealSigtermFollowsSigintAfterIntWait(t *testing.T) {
	i := newInteg(t, "init-ignoreint", "-", realS3)
	id := i.runningWithInit("key-real-int-0002")
	start := time.Now()
	if _, err := i.ex.Interrupt(id); err != nil {
		t.Fatal(err)
	}
	x := i.wait(id)
	took := time.Since(start)
	v := i.verdict(id)
	if x.State != "unknown" || v.Row != "6" || v.Reason != "ABORT_UNCONFIRMED" || v.ExitClass != "signal" || v.ChildUnconfirmed {
		t.Fatalf("state %s verdict %+v", x.State, v)
	}
	if took < 400*time.Millisecond-30*time.Millisecond {
		t.Fatalf("SIGTERM arrived after %v, before IntWait", took)
	}
	if err := syscall.Kill(atoi(i.record()["pid"]), 0); err == nil {
		t.Fatal("the child is still alive after SIGTERM")
	}
}

// Both ignored: no SIGKILL, ever. The child is still alive when the escalation is exhausted,
// the verdict is row 6b with child_unconfirmed, the guard stays held. The test (the operator)
// then removes the child.
func TestRealEscalationExhaustedNeverKills(t *testing.T) {
	i := newInteg(t, "init-ignoreboth", "-", realS3)
	id := i.runningWithInit("key-real-int-0003")
	pid := atoi(i.record()["pid"])
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	if _, err := i.ex.Interrupt(id); err != nil {
		t.Fatal(err)
	}
	until(t, "row 6b", func() bool { return i.verdict(id).Row == "6b" })
	time.Sleep(900 * time.Millisecond) // well past IntWait+TermWait: still nothing stronger
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the runner killed a child that ignores SIGINT and SIGTERM: %v", err)
	}
	v := i.verdict(id)
	if v.Reason != "ABORT_UNCONFIRMED" || !v.ChildUnconfirmed || v.GuardReleased || v.State != "unknown" {
		t.Fatalf("verdict %+v", v)
	}
	if held, _ := i.ex.SessionGuard("claudecode:" + uuidN(1)); !held {
		t.Fatal("the guard must stay held while the child lives")
	}
	if _, _, err := i.send(1, "second", "key-real-int-0004"); errCode(err) != "SESSION_BUSY" {
		t.Fatalf("second send: %v", err)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL) // operator action
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := i.ex.Wait(ctx, id); err != nil {
		t.Fatalf("the run did not end after the operator removed the child: %v", err)
	}
	if v := i.verdict(id); v.Row != "6b" || v.GuardReleased {
		t.Fatalf("the operator's kill changed the verdict: %+v", v)
	}
}

// Bridge shutdown escalates the same way against real processes and abandons, never kills,
// a child that ignores both signals.
func TestRealCloseEscalatesAndAbandons(t *testing.T) {
	i := newInteg(t, "init-ignoreboth", "-", func(c *Config) {
		c.IntWait, c.TermWait, c.CloseWait = 200*time.Millisecond, 200*time.Millisecond, 700*time.Millisecond
	})
	id := i.runningWithInit("key-real-int-0005")
	pid := atoi(i.record()["pid"])
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	abandoned := i.ex.Close()
	if len(abandoned) != 1 || abandoned[0] != id {
		t.Fatalf("abandoned = %v", abandoned)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("Close killed a child: %v", err)
	}
	if v := i.verdict(id); v.Row != "9s" || v.Reason != "MANAGER_CLOSED" || v.GuardReleased {
		t.Fatalf("verdict %+v", v)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// #242 P3-2 (C-10): the refusal of every signal but SIGINT and SIGTERM is pinned on a LIVE
// child: nothing is sent, the child is neither signalled nor killed, and the two allowed
// signals do arrive.
func TestOnlySigintAndSigtermReachALiveChild(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "mark")
	body := "trap 'echo INT >> " + mark + "; exit 0' INT\ntrap 'echo TERM >> " + mark + "; exit 0' TERM\n" +
		"trap 'echo HUP >> " + mark + "' HUP\ntrap 'echo QUIT >> " + mark + "' QUIT\ni=0\nwhile [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done\n"
	c := startScript(t, ExecSpawner{}, writeScript(t, body))
	time.Sleep(300 * time.Millisecond) // the traps are installed
	for _, bad := range []Signal{0, 3, 9, 99, -1} {
		if err := c.Signal(bad); err == nil {
			t.Fatalf("Signal(%d) was accepted on a live child", bad)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if b, err := os.ReadFile(mark); err == nil && len(b) > 0 {
		t.Fatalf("a refused signal reached the child: %q", b)
	}
	if err := c.Signal(SigTerm); err != nil {
		t.Fatalf("SIGTERM refused on a live child: %v", err)
	}
	_, _ = io.Copy(io.Discard, c.Stdout())
	if x := c.Wait(); x.Unknown || x.Code != 0 {
		t.Fatalf("exit = %+v", x)
	}
	if b, _ := os.ReadFile(mark); string(b) != "TERM\n" {
		t.Fatalf("the child saw %q, want TERM only", b)
	}
}
