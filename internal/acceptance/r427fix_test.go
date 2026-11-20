package acceptance

import (
	"context"
	"io/fs"
	"strings"
	"testing"
)

// R6 run 4 follow-up (#430, review #429 P2-1): items 4 and 6 hand the CLI the run home as a readable scope
// (--add-dir <run home>) exactly as item 1 does, so they must run the run-home pre-flight (a real directory,
// owned by the run user, not group- or world-writable) BEFORE any run starts. Every stream is FABRICATED by the
// sim of driver_test.go; the real filesystem is never read.

// badRunHomes are the run homes the pre-flight refuses, as the fake filesystem reports them.
var badRunHomes = []struct {
	name string
	mode fs.FileMode
	uid  uint32
}{
	{"world-writable (0777)", fs.ModeDir | 0o777, 0},
	{"group-writable (0770)", fs.ModeDir | 0o770, 0},
	{"world-writable only (0702)", fs.ModeDir | 0o702, 0},
	{"owned by another user", 0, 1700},
	{"not a directory", 0o600, 0},
	{"a symlink", fs.ModeSymlink | 0o777, 0},
}

// Items 4 and 6, each on its own: a refused run home is a FAIL `preflight_failed`, no run starts (so no
// --add-dir is ever given), nothing is written and nothing is left behind.
func TestItems4And6RefuseARunHomeThePreflightRefuses(t *testing.T) {
	for _, item := range []int{4, 6} {
		for _, c := range badRunHomes {
			s := &sim{scope: true}
			d, buf, fsys := newTestDriver(t, s)
			fsys.homeMode, fsys.homeUID = c.mode, c.uid
			v := verdict(d.RunItems(context.Background(), []int{item}), item)
			if v.Result != Fail || v.Why != "preflight_failed" {
				t.Errorf("item %d, %s: %+v\n%s", item, c.name, v, buf.String())
			}
			if d.Runs() != 0 || len(s.runs) != 0 {
				t.Errorf("item %d, %s: %d runs started (%d invocations)", item, c.name, d.Runs(), len(s.runs))
			}
			if len(fsys.writes) != 0 || len(fsys.content) != 0 || len(fsys.dirs) != 0 {
				t.Errorf("item %d, %s: the driver wrote before the pre-flight: writes %v dirs %v", item, c.name, fsys.writes, fsys.dirs)
			}
			if !strings.Contains(buf.String(), "preflight=fail") {
				t.Errorf("item %d, %s: no pre-flight line:\n%s", item, c.name, buf.String())
			}
		}
	}
}

// A run user that does not own the run home is refused through the configuration too (the other way round).
func TestItems4And6RefuseARunUserThatDoesNotOwnTheRunHome(t *testing.T) {
	for _, item := range []int{4, 6} {
		s := &sim{scope: true}
		d, buf, _ := newTestDriver(t, s)
		d.Cfg.RunUID = 1600 // the fake filesystem says the run home belongs to 1500
		v := verdict(d.RunItems(context.Background(), []int{item}), item)
		if v.Result != Fail || v.Why != "preflight_failed" || d.Runs() != 0 {
			t.Errorf("item %d: %+v runs %d\n%s", item, v, d.Runs(), buf.String())
		}
	}
}

// Items 1, 4 and 6 together: one bad run home stops all three, not one of them.
func TestABadRunHomeStopsItems1And4And6(t *testing.T) {
	s := &sim{scope: true}
	d, buf, fsys := newTestDriver(t, s)
	fsys.homeMode = fs.ModeDir | 0o777
	vs := d.RunItems(context.Background(), []int{1, 4, 6})
	for _, it := range []int{1, 4, 6} {
		if v := verdict(vs, it); v.Result != Fail || v.Why != "preflight_failed" {
			t.Errorf("item %d: %+v\n%s", it, v, buf.String())
		}
	}
	if d.Runs() != 0 {
		t.Fatalf("%d runs started with a refused run home", d.Runs())
	}
}

// A good run home changes nothing: both items still PASS and print the pre-flight line.
func TestItems4And6WithAGoodRunHomeAreUnchanged(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, buf, _ := newTestDriver(t, s)
	vs := d.RunItems(context.Background(), []int{4, 6})
	for _, it := range []int{4, 6} {
		if v := verdict(vs, it); v.Result != Pass {
			t.Errorf("item %d: %+v\n%s", it, v, buf.String())
		}
	}
	if d.Runs() != 3 {
		t.Errorf("%d runs, want 3", d.Runs())
	}
	for _, want := range []string{"run_home item=4 preflight=pass", "run_home item=6 preflight=pass"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("no %q line:\n%s", want, buf.String())
		}
	}
}

// #429 P3-1: the REAL argv of items 4 and 6 go through the production ExecRunner and start the wrapper
// (twin of TestExecRunnerStartsEveryItem1ProbeAndControlRun): without ProbeRunHome the runner would refuse
// every one of them and no test with the sim would notice.
func TestExecRunnerStartsEveryItem4And6Run(t *testing.T) {
	d, buf, _ := newTestDriver(t, &sim{})
	w, log := logWrapper(t)
	d.Runner = ExecRunner{Wrapper: w}
	d.RunItems(context.Background(), []int{4, 6})
	runs := loggedRuns(t, log)
	if len(runs) != 3 {
		t.Fatalf("the wrapper was started %d times, want 3 (SIGINT, SIGTERM, the read)\n%s", len(runs), buf.String())
	}
	for i, line := range runs {
		if !strings.HasSuffix(line, " --add-dir "+tHome) || !strings.Contains(line, " --session-id ") || strings.Contains(line, " --resume ") {
			t.Errorf("run %d argv: %s", i, line)
		}
	}
	if strings.Contains(buf.String(), "error=runner") {
		t.Errorf("a run failed in the runner:\n%s", buf.String())
	}
}
