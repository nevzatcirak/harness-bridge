package acceptance

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/cirak/internal/adapters/claudecode"
)

// R6 run 4 (#426): items 4 and 6 were undecidable. Both resumed the ONE bootstrap session (the model cited the
// earlier interrupted Glob and declined to call a tool) and their targets (RUN_HOME/canary-control/*, the
// decoy under RUN_HOME/.claude) lie outside the working directory without the --add-dir scope item 1 got in
// #411. Every stream below is FABRICATED by the sim of driver_test.go; the real CLI's behaviour in run 5
// (the scope under -p, whether the model calls the tool in a fresh session) is UNVERIFIED.

// run4Runs returns the invocations of item 4 (the two signal runs) and of item 6.
func run4Runs(s *sim) (signals, managed []Invocation) {
	for _, inv := range s.runs {
		switch {
		case strings.HasPrefix(inv.Label, "signal_"):
			signals = append(signals, inv)
		case inv.Label == "managed_only_read":
			managed = append(managed, inv)
		}
	}
	return
}

// recProcs records every token the driver asks about, and answers with alive.
type recProcs struct {
	mu     sync.Mutex
	tokens []string
	alive  bool
}

func (r *recProcs) Alive(token string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = append(r.tokens, token)
	return r.alive
}

// Items 4 and 6: every run is a NEW session of its own (--session-id, never --resume) with the one trailing
// --add-dir <run home> of item 1, so the argv is accepted by the probe guard and by nothing looser.
func TestItems4And6RunInFreshScopedSessions(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, buf, _ := newTestDriver(t, s)
	vs := d.RunItems(context.Background(), []int{4, 6})
	signals, managed := run4Runs(s)
	if len(signals) != 2 || len(managed) != 1 {
		t.Fatalf("%d signal runs, %d managed runs\n%s", len(signals), len(managed), buf.String())
	}
	seen := map[string]bool{}
	for _, inv := range append(append([]Invocation{}, signals...), managed...) {
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
		n := len(inv.Args)
		if n < 2 || inv.Args[n-2] != "--add-dir" || inv.Args[n-1] != tHome {
			t.Fatalf("%s: argv does not end with --add-dir <run home>: %v", inv.Label, inv.Args)
		}
		if err := ValidateProbeArgv(inv.Args, tHome); err != nil {
			t.Fatalf("%s: %v", inv.Label, err)
		}
		if inv.ProbeRunHome != tHome {
			t.Fatalf("%s: the runner would validate with the generic guard (ProbeRunHome %q)", inv.Label, inv.ProbeRunHome)
		}
	}
	for _, it := range []int{4, 6} {
		if v := verdict(vs, it); v.Result != Pass {
			t.Errorf("item %d: %+v\n%s", it, v, buf.String())
		}
	}
}

// Items 4 and 6 need no bootstrap: a run of exactly these two items costs 3 runs (SIGINT, SIGTERM, the read).
func TestItems4And6NeedNoBootstrap(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, _, _ := newTestDriver(t, s)
	d.RunItems(context.Background(), []int{4, 6})
	for _, inv := range s.runs {
		if inv.Label == "bootstrap" {
			t.Fatal("items 4 and 6 ran the bootstrap session")
		}
	}
	if d.Runs() != 3 {
		t.Fatalf("%d runs, want 3", d.Runs())
	}
}

// The deny of item 6 and the signals of item 4 still use the pinned hardening argv; the only addition is the
// trailing scope, and the inline settings of item 6 still carry no permissions block.
func TestItem6KeepsNoInlineRulesWithTheScope(t *testing.T) {
	s := &sim{scope: true}
	d, _, _ := newTestDriver(t, s)
	d.RunItems(context.Background(), []int{6})
	_, managed := run4Runs(s)
	if len(managed) != 1 {
		t.Fatalf("%d managed runs", len(managed))
	}
	settings := ""
	for i, a := range managed[0].Args {
		if a == "--settings" {
			settings = managed[0].Args[i+1]
		}
	}
	if settings == "" || strings.Contains(settings, "permissions") || strings.Contains(settings, "deny") {
		t.Fatalf("inline settings of the managed-only run: %q", settings)
	}
	if !contains(managed[0].Args, "--permission-mode") || !contains(managed[0].Args, "--strict-mcp-config") {
		t.Fatalf("hardening argv missing: %v", managed[0].Args)
	}
}

// The child check uses the run's OWN session uuid (the bootstrap uuid no longer exists for these items).
func TestItem4ChecksTheChildOfEachRunBySessionUUID(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, _, _ := newTestDriver(t, s)
	rp := &recProcs{}
	d.Procs = rp
	d.RunItems(context.Background(), []int{4})
	signals, _ := run4Runs(s)
	if len(signals) != 2 || len(rp.tokens) != 2 {
		t.Fatalf("%d signal runs, %d liveness checks", len(signals), len(rp.tokens))
	}
	for i, inv := range signals {
		uuid, _ := sessionOf(inv.Args)
		if rp.tokens[i] != uuid {
			t.Fatalf("run %s: liveness checked for %q, the run's session is %q", inv.Label, rp.tokens[i], uuid)
		}
	}
}

// Item 4 verdict table. FAIL stays for "a signal was sent but the wrapper did not exit or the child lives";
// a run in which no tool_use happened never sent its signal: INCOMPLETE, never FAIL, never PASS.
func TestItem4VerdictTable(t *testing.T) {
	both := map[string]bool{"signal_interrupt": true, "signal_terminated": true}
	for _, c := range []struct {
		name   string
		sim    *sim
		alive  bool
		result string
		why    string
	}{
		{"both signals sent, the child is gone", &sim{scope: true, resumeDeclines: true}, false, Pass, "both_signals_relayed_child_gone"},
		{"the model never called the tool in either run", &sim{declineLabels: both}, false, Incomplete, "signal_not_sent_no_tool_use"},
		{"the model declined only the SIGTERM run (run 4)", &sim{declineLabels: map[string]bool{"signal_terminated": true}}, false, Incomplete, "signal_not_sent_no_tool_use"},
		{"the model declined only the SIGINT run", &sim{declineLabels: map[string]bool{"signal_interrupt": true}}, false, Incomplete, "signal_not_sent_no_tool_use"},
		{"sent, but the child is still alive", &sim{scope: true}, true, Fail, "signal_not_relayed_or_child_alive"},
		{"sent, but the wrapper did not exit", &sim{scope: true, timeoutLabels: map[string]bool{"signal_terminated": true}}, false, Fail, "signal_not_relayed_or_child_alive"},
		{"a relay failure beats a run that was not exercised", &sim{declineLabels: map[string]bool{"signal_terminated": true}, timeoutLabels: map[string]bool{"signal_interrupt": true}}, false, Fail, "signal_not_relayed_or_child_alive"},
	} {
		d, buf, _ := newTestDriver(t, c.sim)
		d.Procs = noProcs{alive: c.alive}
		v := verdict(d.RunItems(context.Background(), []int{4}), 4)
		if v.Result != c.result || v.Why != c.why {
			t.Errorf("%s: %+v, want %s %s\n%s", c.name, v, c.result, c.why, buf.String())
		}
	}
}

// Item 4 keeps its SIGINT, 8 s, SIGTERM escalation in the SIGINT run and a lone SIGTERM in the other.
func TestItem4KeepsTheEscalation(t *testing.T) {
	s := &sim{scope: true}
	d, _, _ := newTestDriver(t, s)
	d.RunItems(context.Background(), []int{4})
	signals, _ := run4Runs(s)
	if len(signals) != 2 {
		t.Fatalf("%d signal runs", len(signals))
	}
	if got := len(signals[0].Signals); got != 2 || signals[0].Signals[0].Signal.String() != "interrupt" || signals[0].Signals[1].Signal.String() != "terminated" || signals[0].Signals[1].Delay.Seconds() != 8 {
		t.Fatalf("SIGINT run steps: %+v", signals[0].Signals)
	}
	if got := len(signals[1].Signals); got != 1 || signals[1].Signals[0].Signal.String() != "terminated" {
		t.Fatalf("SIGTERM run steps: %+v", signals[1].Signals)
	}
}

// Item 6 verdict table: marker shown is a FAIL; tool_use + a denial that is the rule is the only PASS;
// every other branch is INCOMPLETE, never FAIL, never PASS.
func TestItem6VerdictTable(t *testing.T) {
	for _, c := range []struct {
		name   string
		sim    *sim
		result string
		why    string
	}{
		{"tool_use, denial by the rule", &sim{scope: true}, Pass, "managed_deny_applied_no_inline_rules"},
		{"the marker is shown", &sim{scope: true, denied: func(string) bool { return false }}, Fail, "managed_deny_did_not_apply"},
		{"the model never called Read", &sim{declineLabels: map[string]bool{"managed_only_read": true}}, Incomplete, "no_tool_use"},
		{"tool_use, no denial, no marker", &sim{blind: true, denied: func(string) bool { return false }}, Incomplete, "no_denial_no_marker"},
		{"tool_use, denial for scope (not the rule)", &sim{scopeAll: true}, Incomplete, "denial_not_rule_scope"},
		{"tool_use, denial for another error", &sim{otherAll: true}, Incomplete, "denial_not_rule_other"},
	} {
		d, buf, _ := newTestDriver(t, c.sim)
		v := verdict(d.RunItems(context.Background(), []int{6}), 6)
		if v.Result != c.result || v.Why != c.why {
			t.Errorf("%s: %+v, want %s %s\n%s", c.name, v, c.result, c.why, buf.String())
		}
	}
}

// Item 6's evidence line carries the denial class (never the error text); PASS needs `rule`.
func TestItem6EvidenceCarriesTheDenialClass(t *testing.T) {
	for _, c := range []struct {
		sim   *sim
		class string
	}{
		{&sim{scope: true}, "rule"},
		{&sim{scopeAll: true}, "scope"},
		{&sim{otherAll: true}, "other"},
		{&sim{declineLabels: map[string]bool{"managed_only_read": true}}, "declined"},
	} {
		d, buf, _ := newTestDriver(t, c.sim)
		d.RunItems(context.Background(), []int{6})
		line := ""
		for _, l := range strings.Split(buf.String(), "\n") {
			if strings.HasPrefix(l, "managed ") {
				line = l
			}
		}
		if !strings.Contains(line, "denial_class="+c.class) {
			t.Errorf("class %s: managed line %q", c.class, line)
		}
		if strings.Contains(buf.String(), "could not be performed") || strings.Contains(buf.String(), "haven't granted") {
			t.Errorf("the evidence contains a tool error text:\n%s", buf.String())
		}
	}
}

// R6 run 4 reproduced: -items 2,4,5,6,7,8 with the sim of a CLI whose resumed session declines after a refusal
// or an interrupt. Items 2, 5, 7 still use the bootstrap; items 4 and 6 no longer do, so both PASS.
func TestRun4ScenarioNowDecides(t *testing.T) {
	s := &sim{scope: true, resumeDeclines: true}
	d, buf, _ := newTestDriver(t, s)
	vs := d.RunItems(context.Background(), []int{2, 4, 5, 6, 7, 8})
	if v := verdict(vs, 4); v.Result != Pass {
		t.Errorf("item 4: %+v\n%s", v, buf.String())
	}
	if v := verdict(vs, 6); v.Result != Pass {
		t.Errorf("item 6: %+v\n%s", v, buf.String())
	}
}
