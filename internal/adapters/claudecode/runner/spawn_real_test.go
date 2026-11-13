package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Direct tests of ExecSpawner with small shell wrappers (no sudo, no Claude CLI).

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "w.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func startScript(t *testing.T, sp ExecSpawner, path string) Child {
	t.Helper()
	c, err := sp.Start(Spec{Path: path, Env: []string{"PATH=/usr/bin:/bin"}, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.CloseStdin()
	return c
}

// #236 P3-3 (C-2): after a known Wait the group id could be reused, so a signal is refused.
func TestSignalAfterAKnownWaitIsRefused(t *testing.T) {
	// A group member outlives the wrapper, so kill(-pgid) would still succeed: only the
	// reaped flag stops the signal.
	c := startScript(t, ExecSpawner{}, writeScript(t, "sleep 2 >/dev/null 2>&1 &\nexit 0\n"))
	_, _ = io.Copy(io.Discard, c.Stdout())
	if x := c.Wait(); x.Unknown || x.Code != 0 {
		t.Fatalf("exit = %+v", x)
	}
	if err := c.Signal(SigInt); err == nil {
		t.Fatal("a signal after a confirmed reap must be refused (pid reuse)")
	}
	if err := c.Signal(Signal(99)); err == nil {
		t.Fatal("only SIGINT may ever be sent")
	}
}

// #236 P3-1: a descendant that keeps stderr open cannot make Wait unbounded; the expiry is
// an unknown wait, never a confirmed reap.
func TestWaitIsBoundedByWaitDelay(t *testing.T) {
	c := startScript(t, ExecSpawner{WaitDelay: 250 * time.Millisecond}, writeScript(t, "sleep 3 >/dev/null &\nexit 0\n"))
	_, _ = io.Copy(io.Discard, c.Stdout())
	start := time.Now()
	x := c.Wait()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Wait took %v with a descendant holding stderr (no WaitDelay)", d)
	}
	if !x.Unknown {
		t.Fatalf("a WaitDelay expiry is an unknown wait, got %+v", x)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("stderr sink failed") }

// #236 P3-2: cmd.Wait returned a copy error but already reaped the process (ProcessState
// set): that is a known status, and the child is marked reaped.
func TestCopyErrorAfterAReapIsAKnownStatus(t *testing.T) {
	c := startScript(t, ExecSpawner{stderr: failingWriter{}}, writeScript(t, "echo oops >&2\nexit 0\n"))
	_, _ = io.Copy(io.Discard, c.Stdout())
	x := c.Wait()
	if x.Unknown || x.Code != 0 {
		t.Fatalf("a reaped child with a stderr copy error must be a known status, got %+v", x)
	}
	if err := c.Signal(SigInt); err == nil {
		t.Fatal("the child was reaped: a later signal must be refused")
	}
}

// #236 P3-3 (C-5): the interrupt goes to the process GROUP: a member that is not the
// wrapper process gets it too.
func TestInterruptReachesTheWholeGroup(t *testing.T) {
	dir := t.TempDir()
	member := filepath.Join(dir, "member.sh")
	mark := filepath.Join(dir, "mark")
	body := "trap 'echo got > \"$1\"; exit 0' INT\ni=0\nwhile [ $i -lt 100 ]; do sleep 0.05; i=$((i+1)); done\necho timeout > \"$1\"\n"
	if err := os.WriteFile(member, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	// The wrapper is NOT the member: it waits for it in the foreground.
	c := startScript(t, ExecSpawner{}, writeScript(t, fmt.Sprintf("sh %s %s\n", member, mark)))
	time.Sleep(300 * time.Millisecond) // the member has installed its trap
	if err := c.Signal(SigInt); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, c.Stdout())
	c.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(mark); err == nil && len(b) > 0 {
			if string(b) != "got\n" {
				t.Fatalf("the group member was not interrupted: %q", b)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the group member never reported an interrupt")
}
