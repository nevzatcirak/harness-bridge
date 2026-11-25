package acceptance

import (
	"context"
	"strings"
	"testing"

	"github.com/nevzatcirak/cirak/internal/adapters/claudecode"
)

// R6 run 1 (#409): item 1 was undecidable. Every run resumed ONE session (the model cited earlier
// refusals and declined to call the tool) and the control decoys sat outside the run's working directory
// (a refusal that is not the deny rule). The streams are FABRICATED by the sim of driver_test.go; the
// scope text and the behaviour of --add-dir under the real CLI are UNVERIFIED.

// item1Runs returns the invocations of the canary item (everything but the bootstrap).
func item1Runs(s *sim) []Invocation {
	var out []Invocation
	for _, inv := range s.runs {
		if strings.HasPrefix(inv.Label, "canary_") {
			out = append(out, inv)
		}
	}
	return out
}

func argvWithout(args []string, uuid string) []string {
	out := append([]string(nil), args...)
	for i, a := range out {
		if a == uuid {
			out[i] = "<uuid>"
		}
	}
	return out
}

// With the realistic sim (scope refusals outside the cwd, a resumed session that declines after a refusal)
// the fixed driver PASSes: probes are denied by the rule, controls read their decoy.
func TestCanaryItemPassesWhenEveryRunHasItsOwnSessionAndTheControlIsReadable(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, buf, _ := newTestDriver(t, s)
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Pass {
		t.Fatalf("verdict %+v\n%s", v, buf.String())
	}
}

// D2: no probe and no control resumes the bootstrap session or another run's session.
func TestNoCanaryRunResumesASession(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, _, _ := newTestDriver(t, s)
	d.RunItems(context.Background(), []int{1})
	runs := item1Runs(s)
	if len(runs) != 12 {
		t.Fatalf("%d canary runs, want 12 (6 probes + 6 controls)", len(runs))
	}
	seen := map[string]bool{}
	for _, inv := range runs {
		for _, a := range inv.Args {
			if a == "--resume" {
				t.Fatalf("%s resumes a session: %v", inv.Label, inv.Args)
			}
		}
		uuid, resumed := sessionOf(inv.Args)
		if resumed || !claudecode.ValidUUID(uuid) {
			t.Fatalf("%s: session %q resumed=%v, want a new --session-id uuid", inv.Label, uuid, resumed)
		}
		if seen[uuid] {
			t.Fatalf("%s reuses the session %s of another run", inv.Label, uuid)
		}
		seen[uuid] = true
	}
}

// Item 1 no longer needs the bootstrap: its runs create their own sessions, so it costs 12 runs, not 13.
func TestCanaryItemNeedsNoBootstrapAndCostsTwelveRuns(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, _, _ := newTestDriver(t, s)
	d.RunItems(context.Background(), []int{1})
	for _, inv := range s.runs {
		if inv.Label == "bootstrap" {
			t.Fatal("item 1 ran the bootstrap session")
		}
	}
	if d.Runs() != 12 {
		t.Fatalf("%d runs, want 12", d.Runs())
	}
	// A budget of exactly 12 is enough; one less breaches.
	for _, c := range []struct {
		max  int
		want string
	}{{12, Pass}, {11, NotRun}} {
		s2 := &sim{scope: true, resumeDeclines: true}
		d2, _, _ := newTestDriver(t, s2)
		d2.budget, _ = NewBudget(c.max)
		d2.Cfg.MaxRuns = c.max
		vs := d2.RunItems(context.Background(), []int{1})
		if got := verdict(vs, 1).Result; (c.want == Pass && got != Pass) || (c.want == NotRun && got == Pass) {
			t.Errorf("budget %d: verdict %q", c.max, got)
		}
	}
}

// The only difference between a probe and its control is the prompt (its target): same argv (up to the
// session uuid), the same readable scope.
func TestProbeAndControlArgvDifferOnlyInThePrompt(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, _, _ := newTestDriver(t, s)
	d.RunItems(context.Background(), []int{1})
	runs := item1Runs(s)
	if len(runs) != 12 {
		t.Fatalf("%d canary runs", len(runs))
	}
	probes, controls := runs[:6], runs[6:]
	for i := range probes {
		p, c := probes[i], controls[i]
		if !strings.HasPrefix(p.Label, "canary_denied_") || !strings.HasPrefix(c.Label, "canary_control_") {
			t.Fatalf("order: %s, %s", p.Label, c.Label)
		}
		pu, _ := sessionOf(p.Args)
		cu, _ := sessionOf(c.Args)
		pa, ca := argvWithout(p.Args, pu), argvWithout(c.Args, cu)
		if strings.Join(pa, "\x00") != strings.Join(ca, "\x00") {
			t.Errorf("probe %s and control %s argv differ:\n%v\n%v", p.Label, c.Label, pa, ca)
		}
		if p.Stdin == c.Stdin || p.Stdin == "" || c.Stdin == "" {
			t.Errorf("probe %s and control %s prompts: %q / %q", p.Label, c.Label, p.Stdin, c.Stdin)
		}
		if addDirOf(p.Args) != tHome || addDirOf(c.Args) != tHome {
			t.Errorf("%s / %s: --add-dir %q / %q, want the run home %q", p.Label, c.Label, addDirOf(p.Args), addDirOf(c.Args), tHome)
		}
		if err := ValidateProbeArgv(p.Args, tHome); err != nil {
			t.Errorf("%s: %v", p.Label, err)
		}
	}
}

// stripAddDir hides the readable scope from the sim: it models a harness whose control is out of scope.
// With only set, it strips the scope from the runs whose label starts with it.
type stripAddDir struct {
	inner Runner
	only  string
}

func (r stripAddDir) Run(ctx context.Context, inv Invocation) (Result, error) {
	if r.only != "" && !strings.HasPrefix(inv.Label, r.only) {
		return r.inner.Run(ctx, inv)
	}
	var args []string
	for i := 0; i < len(inv.Args); i++ {
		if inv.Args[i] == "--add-dir" {
			i++
			continue
		}
		args = append(args, inv.Args[i])
	}
	inv.Args = args
	return r.inner.Run(ctx, inv)
}

// A control refused for a reason that is not the deny rule (here: out of scope) is a harness defect: the
// item is INCOMPLETE with why=control_refused_scope, never a FAIL of the product.
func TestControlRefusedForScopeIsAHarnessDefect(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, buf, _ := newTestDriver(t, s)
	d.Runner = stripAddDir{inner: s}
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Incomplete || v.Why != "control_refused_scope" {
		t.Fatalf("verdict %+v\n%s", v, buf.String())
	}
	if !strings.Contains(buf.String(), "kind=control") || !strings.Contains(buf.String(), "denial_class=scope") {
		t.Fatalf("the evidence lacks the control's denial class:\n%s", buf.String())
	}
}

// A control refused by the deny rule itself is the same defect (the rule must not cover the control).
func TestControlRefusedByTheRuleIsAHarnessDefect(t *testing.T) {
	s := &sim{ruleAll: true}
	d, buf, _ := newTestDriver(t, s)
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Incomplete || v.Why != "control_refused_rule" {
		t.Fatalf("verdict %+v\n%s", v, buf.String())
	}
}

// A control the model declined (no tool_use) is no evidence either: INCOMPLETE, not a FAIL of the product.
func TestControlDeclinedIsAHarnessDefect(t *testing.T) {
	s := &sim{scope: true, declineControls: true}
	d, _, _ := newTestDriver(t, s)
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Incomplete || v.Why != "control_refused_declined" {
		t.Fatalf("verdict %+v", v)
	}
}

// A real security failure is never hidden behind a control defect: when the marker is shown the verdict is
// FAIL, even if the controls were also refused.
func TestSecurityFailureWinsOverAControlDefect(t *testing.T) {
	s := &sim{scope: true, denied: func(string) bool { return false }} // the deny rule does not work: the probes show the marker
	d, _, _ := newTestDriver(t, s)
	d.Runner = stripAddDir{inner: s, only: "canary_control"} // and the controls are out of scope as well
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Fail {
		t.Fatalf("verdict %+v: a leak of the marker must FAIL", v)
	}
}

// The evidence carries one closed-set denial_class per run (probes: rule; controls: none) and never the tool
// error text or a marker.
func TestEvidenceHasADenialClassPerRunAndNoErrorText(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, buf, _ := newTestDriver(t, s)
	d.RunItems(context.Background(), []int{1})
	out := buf.String()
	lines := 0
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "canary_run ") {
			continue
		}
		lines++
		switch {
		case strings.Contains(l, "kind=probe"):
			if !strings.Contains(l, "denial_class=rule") {
				t.Errorf("probe line: %s", l)
			}
		case strings.Contains(l, "kind=control"):
			if !strings.Contains(l, "denial_class=none") {
				t.Errorf("control line: %s", l)
			}
		default:
			t.Errorf("neither probe nor control: %s", l)
		}
	}
	if lines != 12 {
		t.Fatalf("%d canary_run lines, want 12:\n%s", lines, out)
	}
	for _, bad := range []string{"permission settings", "haven't granted", "hbcanary-"} {
		if strings.Contains(out, bad) {
			t.Errorf("the evidence contains %q", bad)
		}
	}
}

// ValidateProbeArgv: the only extra flag item 1 may add is one trailing --add-dir with exactly the run home.
func TestValidateProbeArgv(t *testing.T) {
	s := &sim{}
	d, _, _ := newTestDriver(t, s)
	u, err := newUUID(d.Rand)
	if err != nil {
		t.Fatal(err)
	}
	base, err := d.argvUUID(u, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }
	if err := ValidateProbeArgv(with("--add-dir", tHome), tHome); err != nil {
		t.Fatalf("the allowed shape was refused: %v", err)
	}
	bad := map[string][]string{
		"no add-dir":                  with(),
		"another directory":           with("--add-dir", "/etc"),
		"the parent of the run home":  with("--add-dir", "/srv"),
		"two add-dir":                 with("--add-dir", tHome, "--add-dir", tHome),
		"add-dir not last":            append(with("--add-dir", tHome), "--verbose"),
		"add-dir without a value":     with("--add-dir"),
		"a relative path":             with("--add-dir", "srv/runhome"),
		"the run home plus extra dir": with("--add-dir", tHome, "/etc"),
	}
	for name, a := range bad {
		if ValidateProbeArgv(a, tHome) == nil {
			t.Errorf("%s: accepted %v", name, a[len(base):])
		}
	}
	if ValidateArgv(with("--add-dir", tHome)) == nil {
		t.Error("the generic ValidateArgv accepts --add-dir: only the probe path may")
	}
	if ValidateProbeArgv(with("--add-dir", tHome, "--dangerously-skip-permissions"), tHome) == nil {
		t.Error("a permission-skipping flag after --add-dir was accepted")
	}
	// the run home itself must be an absolute clean path
	if ValidateProbeArgv(with("--add-dir", tHome), "") == nil || ValidateProbeArgv(with("--add-dir", tHome+"/"), tHome+"/") == nil {
		t.Error("an empty or unclean run home was accepted")
	}
}

// R6 run 2 (#422): the live CLI refuses a Grep/Glob under the deny rule with
// "Permission to read <dir> has been denied." (<dir> = the probe's own target). The classifier takes it as the
// rule for a target inside a denied directory, so the item PASSes. The stream is FABRICATED by the sim; the
// real CLI's behaviour in run 3 is UNVERIFIED.
func TestGrepGlobDirectoryRefusalOfTheDeniedDirIsTheRule(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true, grepGlobText: 1}
	d, buf, _ := newTestDriver(t, s)
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Pass {
		t.Fatalf("verdict %+v\n%s", v, buf.String())
	}
	probes := 0
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(l, "canary_run ") && strings.Contains(l, "kind=probe") {
			probes++
			if !strings.Contains(l, "denial_class=rule") {
				t.Errorf("probe line: %s", l)
			}
		}
	}
	if probes != 6 {
		t.Fatalf("%d probe lines, want 6", probes)
	}
	if strings.Contains(buf.String(), "has been denied") {
		t.Errorf("the evidence contains the tool error text")
	}
}

// The same text naming another path is no evidence about the rule: INCOMPLETE, never PASS.
func TestGrepGlobRefusalNamingAnotherPathIsNotTheRule(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true, grepGlobText: 2}
	d, buf, _ := newTestDriver(t, s)
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Incomplete || v.Why != "probe_not_rule_denied_other" {
		t.Fatalf("verdict %+v\n%s", v, buf.String())
	}
}

// Item 1 already needs EVERY control clean (#412): a control that was not read makes the item INCOMPLETE
// whatever the probes show, so a probe's `rule` is never counted next to an unclean control.
func TestAnUncleanControlStillBlocksThePassWhenProbesAreTheGrepGlobRule(t *testing.T) {
	s := &sim{ruleAll: true, grepGlobText: 1}
	d, buf, _ := newTestDriver(t, s)
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Incomplete || !strings.HasPrefix(v.Why, "control_refused_") {
		t.Fatalf("verdict %+v\n%s", v, buf.String())
	}
}
