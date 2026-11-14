package runner

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// #242 residual (wiring slice): the wrapper's known exit is not proof that the CLI behind
// `sudo` is gone. A known exit with the child's process group still populated keeps the guard
// held and the verdict `unknown`; it never completes the run and never frees the session.

func groupBusyHarness(t *testing.T) *harness {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.GroupWait = 80 * time.Millisecond })
	h.sp.tweak = func(c *fakeChild) { c.groupBusy = true }
	return h
}

func TestKnownWrapperExitWithALiveGroupMemberKeepsTheGuard(t *testing.T) {
	h := groupBusyHarness(t)
	x := h.mustSend(1, "p", "key-grp-00000001")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Code: 0}) // a clean, known exit with a success result: row 3 without the group check
	x = h.wait(x.ID)
	v := h.verdict(x.ID)
	if x.State != "unknown" || v.State != "unknown" || v.GuardReleased || v.Row == "3" {
		t.Fatalf("state %s verdict %+v: a live group member must not let the run complete", x.State, v)
	}
	if !h.guardHeld(1) {
		t.Fatal("the guard was released while a descendant may still run")
	}
	if _, _, err := h.send(1, "second", "key-grp-00000002"); errCode(err) != "SESSION_BUSY" || h.sp.count() != 1 {
		t.Fatalf("second send: %v (children %d)", err, h.sp.count())
	}
}

func TestKnownWrapperExitWithoutAResultAndALiveGroupIsUnknown(t *testing.T) {
	h := groupBusyHarness(t)
	x := h.mustSend(1, "p", "key-grp-00000003")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.finish(Exit{Code: 0})
	h.wait(x.ID)
	if v := h.verdict(x.ID); v.State != "unknown" || v.GuardReleased || !h.guardHeld(1) {
		t.Fatalf("verdict %+v", v)
	}
}

// Once the group is empty the operator can release it (S4): the facts the release tests are
// the known exit and the empty group.
func TestAGroupThatEmptiesLaterCanBeReleasedByTheOperator(t *testing.T) {
	h := groupBusyHarness(t)
	x := h.mustSend(1, "p", "key-grp-00000004")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.finish(Exit{Code: 0})
	h.wait(x.ID)
	if _, err := h.release(1, x.ID); errCode(err) != "SESSION_BUSY" || !h.guardHeld(1) {
		t.Fatalf("with a member left: %v", err)
	}
	c.mu.Lock()
	c.groupBusy = false
	c.mu.Unlock()
	if _, err := h.release(1, x.ID); err != nil || h.guardHeld(1) {
		t.Fatalf("after the group emptied: %v", err)
	}
}

// A group that empties within GroupWait is the normal case (the wrapper's own cleanup): the
// run completes as before.
func TestAGroupThatEmptiesWithinGroupWaitCompletesTheRun(t *testing.T) {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.GroupWait = 2 * time.Second })
	h.sp.tweak = func(c *fakeChild) { c.groupBusy = true }
	x := h.mustSend(1, "p", "key-grp-00000005")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	go func() {
		time.Sleep(120 * time.Millisecond)
		c.mu.Lock()
		c.groupBusy = false
		c.mu.Unlock()
	}()
	c.finish(Exit{Code: 0})
	x = h.wait(x.ID)
	if v := h.verdict(x.ID); x.State != "completed" || v.Row != "3" || !v.GuardReleased {
		t.Fatalf("state %s verdict %+v", x.State, v)
	}
}

// A failing group check is "not empty": fail closed.
func TestAFailingGroupCheckAfterAKnownExitFailsClosed(t *testing.T) {
	h := newHarness(t, func(c *Config, sc *claudecode.StoreConfig) { s3(c, sc); c.GroupWait = 50 * time.Millisecond })
	h.sp.tweak = func(c *fakeChild) { c.groupErr = io.ErrUnexpectedEOF }
	x := h.mustSend(1, "p", "key-grp-00000006")
	c := h.next(1)
	c.emit(h.initLine(1))
	c.emit(h.resultLine(1, nil))
	c.finish(Exit{Code: 0})
	h.wait(x.ID)
	if v := h.verdict(x.ID); v.State != "unknown" || v.GuardReleased || !h.guardHeld(1) {
		t.Fatalf("verdict %+v", v)
	}
}

// #257 P3-2 / #259: the real ExecSpawner GroupEmpty. A member that outlives the wrapper keeps
// the group populated (false); once it is gone the group is empty (true).
func TestRealGroupEmptyFollowsTheGroupMembers(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "member.pid")
	c := startScript(t, ExecSpawner{}, writeScript(t, "sleep 30 >/dev/null 2>&1 &\necho $! > "+pidfile+"\nexit 0\n"))
	_, _ = io.Copy(io.Discard, c.Stdout())
	if x := c.Wait(); x.Unknown || x.Code != 0 {
		t.Fatalf("exit = %+v", x)
	}
	var pid int
	until(t, "the member pid", func() bool {
		b, err := os.ReadFile(pidfile)
		n, e2 := strconv.Atoi(strings.TrimSpace(string(b)))
		pid = n
		return err == nil && e2 == nil
	})
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	empty, err := c.GroupEmpty()
	if err != nil || empty {
		t.Fatalf("a member of the group is alive after the wrapper exited: empty=%v err=%v", empty, err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil { // the test, as an operator, removes it
		t.Fatal(err)
	}
	until(t, "an empty group", func() bool {
		e, err := c.GroupEmpty()
		return err == nil && e
	})
}
