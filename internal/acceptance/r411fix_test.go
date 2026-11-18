package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R6 run 1 fix, round 2 (#416, reviews #412/#413): the production runner must accept the item 1 probe argv,
// the glued flag forms are refused, a probe that was not refused by the rule is never a PASS. Every stream
// is FABRICATED by the sim of driver_test.go; the wrapper below is a temp shell script, never the host's.

// logWrapper is a fake wrapper that records its argv (one line per run) and answers with a minimal stream.
func logWrapper(t *testing.T) (wrapper, logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "argv.log")
	body := "echo \"$*\" >> '" + logPath + "'\ncat >/dev/null\necho '" + initJSON + "'\necho '" + resultJSON + "'\n"
	return fakeWrapper(t, body), logPath
}

func loggedRuns(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// P1-1: the driver's REAL probe and control argv go through the production ExecRunner and start the wrapper.
func TestExecRunnerStartsEveryItem1ProbeAndControlRun(t *testing.T) {
	d, buf, _ := newTestDriver(t, &sim{})
	w, log := logWrapper(t)
	d.Runner = ExecRunner{Wrapper: w}
	vs := d.RunItems(context.Background(), []int{1})
	runs := loggedRuns(t, log)
	if len(runs) != 12 {
		t.Fatalf("the wrapper was started %d times, want 12 (6 probes + 6 controls); verdict %+v\n%s", len(runs), verdict(vs, 1), buf.String())
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

// The generic check is not weakened: only a run that carries the validated probe run home may carry --add-dir.
func TestExecRunnerStillRefusesAddDirOnAnOrdinaryRun(t *testing.T) {
	d, _, _ := newTestDriver(t, &sim{})
	u, err := newUUID(d.Rand)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := d.probeArgv(u)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := d.argvUUID(u, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	with := func(a []string, extra ...string) []string { return append(append([]string(nil), a...), extra...) }
	cases := []struct {
		name    string
		inv     Invocation
		started bool
	}{
		{"the probe argv with the probe run home", Invocation{Args: probe, Stdin: "x", ProbeRunHome: tHome}, true},
		{"an ordinary argv", Invocation{Args: plain, Stdin: "x"}, true},
		{"the probe argv on an ordinary run", Invocation{Args: probe, Stdin: "x"}, false},
		{"another directory with the probe run home", Invocation{Args: with(plain, "--add-dir", "/etc"), Stdin: "x", ProbeRunHome: tHome}, false},
		{"the probe run home of another run home", Invocation{Args: probe, Stdin: "x", ProbeRunHome: "/srv/other"}, false},
		{"an extra flag after the pair", Invocation{Args: with(probe, "--verbose"), Stdin: "x", ProbeRunHome: tHome}, false},
		{"a glued add-dir on a probe run", Invocation{Args: with(plain, "--add-dir="+tHome), Stdin: "x", ProbeRunHome: tHome}, false},
		{"a relative probe run home", Invocation{Args: with(plain, "--add-dir", "srv/runhome"), Stdin: "x", ProbeRunHome: "srv/runhome"}, false},
	}
	for _, c := range cases {
		w, log := logWrapper(t)
		_, err := ExecRunner{Wrapper: w}.Run(context.Background(), c.inv)
		started := len(loggedRuns(t, log)) == 1
		if started != c.started || (err == nil) != c.started {
			t.Errorf("%s: started=%v err=%v, want started=%v", c.name, started, err, c.started)
		}
	}
}

// P2-1 (#413) / P3-1 (#412): the glued forms of the guarded flags are refused by both validators.
func TestGluedFlagFormsAreRefused(t *testing.T) {
	d, _, _ := newTestDriver(t, &sim{})
	u, err := newUUID(d.Rand)
	if err != nil {
		t.Fatal(err)
	}
	base, err := d.argvUUID(u, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	pair := []string{"--add-dir", tHome}
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }
	if err := ValidateArgv(base); err != nil {
		t.Fatalf("the plain argv was refused: %v", err)
	}
	if err := ValidateProbeArgv(with(pair...), tHome); err != nil {
		t.Fatalf("the allowed probe shape was refused: %v", err)
	}
	for _, f := range []string{
		"--add-dir=" + tHome, "--add-dir=/etc", "--add-dirx", "--permission-mode=default", "--permission-mode=acceptEdits",
		"--permission-prompts=none", "--permission-prompts=allow", "--allowedTools=Bash", "--allowed-tools=Bash", "--permission-prompt-tool=x",
		"--dangerously-skip-permissions=true",
	} {
		if ValidateArgv(with(f)) == nil {
			t.Errorf("ValidateArgv accepted %s", f)
		}
		if ValidateProbeArgv(with(f), tHome) == nil {
			t.Errorf("ValidateProbeArgv accepted %s as the last argument", f)
		}
		if ValidateProbeArgv(with(append([]string{f}, pair...)...), tHome) == nil {
			t.Errorf("ValidateProbeArgv accepted %s before the trailing pair", f)
		}
	}
}

// rewriteProbes changes the rule's refusal text of the probe runs into another text (a probe refused for a
// reason that is not the deny rule), leaving the permission_denials entries as they are.
type rewriteProbes struct {
	inner    Runner
	from, to string
}

func (r rewriteProbes) Run(ctx context.Context, inv Invocation) (Result, error) {
	res, err := r.inner.Run(ctx, inv)
	if err == nil && strings.HasPrefix(inv.Label, "canary_denied") {
		res.Stdout = []byte(strings.ReplaceAll(string(res.Stdout), r.from, r.to))
	}
	return res, err
}

// #412 P2-1: a probe that was not refused by the deny rule is never a PASS.
func TestProbeNotRuleDeniedIsIncompleteNeverPass(t *testing.T) {
	for _, c := range []struct {
		name string
		sim  *sim
		wrap func(*sim) Runner
		why  string
	}{
		{"probes refused for scope", &sim{scope: true}, func(s *sim) Runner { return rewriteProbes{s, textRuleDenied, textScopeDenied} }, "probe_not_rule_denied_scope"},
		{"probes refused with another error", &sim{scope: true}, func(s *sim) Runner { return rewriteProbes{s, textRuleDenied, "ENOENT: no such file"} }, "probe_not_rule_denied_other"},
		{"probes the model declined", &sim{scope: true, declineProbes: true}, func(s *sim) Runner { return s }, "probe_not_rule_denied_declined"},
	} {
		d, buf, _ := newTestDriver(t, c.sim)
		d.Runner = c.wrap(c.sim)
		v := verdict(d.RunItems(context.Background(), []int{1}), 1)
		if v.Result != Incomplete || v.Why != c.why {
			t.Errorf("%s: verdict %+v\n%s", c.name, v, buf.String())
		}
	}
}

// The order of the verdict: a control defect comes before the probe class, a security failure before both,
// and a probe that ran undenied without showing the marker stays the FAIL it was.
func TestProbeClassKeepsTheVerdictOrder(t *testing.T) {
	// control defect (controls out of scope) before the probe class (probes rewritten to scope)
	s := &sim{scope: true}
	d, _, _ := newTestDriver(t, s)
	d.Runner = stripAddDir{inner: rewriteProbes{s, textRuleDenied, textScopeDenied}, only: "canary_control"}
	if v := verdict(d.RunItems(context.Background(), []int{1}), 1); v.Result != Incomplete || v.Why != "control_refused_scope" {
		t.Errorf("control defect first: %+v", v)
	}
	// a security failure (one probe shows the marker) before the probe class of the others
	s2 := &sim{scope: true, denied: func(target string) bool { return target != tHome+"/.claude/.credentials.decoy.json" }}
	d2, _, _ := newTestDriver(t, s2)
	d2.Runner = rewriteProbes{s2, textRuleDenied, textScopeDenied}
	if v := verdict(d2.RunItems(context.Background(), []int{1}), 1); v.Result != Fail || v.Why != "canary_failed" {
		t.Errorf("security failure first: %+v", v)
	}
	// an undenied probe that shows nothing is not a probe class defect: it stays a FAIL
	s3 := &sim{denied: func(string) bool { return false }, blind: true}
	d3, _, _ := newTestDriver(t, s3)
	if v := verdict(d3.RunItems(context.Background(), []int{1}), 1); v.Result != Fail || v.Why != "canary_failed" {
		t.Errorf("undenied probe: %+v", v)
	}
}

// The production chain is RecordingRunner{ExecRunner}: it must hand the probe run home on unchanged.
func TestRecordingRunnerPassesTheProbeRunHomeThrough(t *testing.T) {
	d, buf, _ := newTestDriver(t, &sim{})
	w, log := logWrapper(t)
	d.Runner = RecordingRunner{Inner: ExecRunner{Wrapper: w}}
	d.RunItems(context.Background(), []int{1})
	if n := len(loggedRuns(t, log)); n != 12 {
		t.Fatalf("the wrapper was started %d times through RecordingRunner, want 12\n%s", n, buf.String())
	}
}
