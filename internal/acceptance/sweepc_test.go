package acceptance

import (
	"context"
	"testing"
)

// Cleanup sweep C (#434), review #428 P3-3: item 4 printed `signal_not_sent_no_tool_use` whenever no signal was
// sent, also when a tool_use HAD happened and the wrapper exited before the signal fired (ExecRunner appends to
// Sent only when the signal call succeeds). The verdict (INCOMPLETE, exit 3) was right; the label named a cause
// that was not the cause. Every stream is FABRICATED by the sim of driver_test.go.
func TestItem4NotSentLabelNamesTheCause(t *testing.T) {
	both := map[string]bool{"signal_interrupt": true, "signal_terminated": true}
	for _, c := range []struct {
		name string
		sim  *sim
		why  string
	}{
		{"no tool_use in either run", &sim{declineLabels: both}, "signal_not_sent_no_tool_use"},
		{"a tool_use in both runs but nothing was sent", &sim{scope: true, exitEarlyLabels: both}, "signal_not_sent_after_tool_use"},
		{"a tool_use but nothing sent in the SIGINT run only", &sim{scope: true, exitEarlyLabels: map[string]bool{"signal_interrupt": true}}, "signal_not_sent_after_tool_use"},
		{"one run without a tool_use, the other exited early (the missing tool_use is named)", &sim{declineLabels: map[string]bool{"signal_terminated": true}, exitEarlyLabels: map[string]bool{"signal_interrupt": true}}, "signal_not_sent_no_tool_use"},
	} {
		d, buf, _ := newTestDriver(t, c.sim)
		d.Procs = noProcs{}
		v := verdict(d.RunItems(context.Background(), []int{4}), 4)
		if v.Result != Incomplete || v.Why != c.why {
			t.Errorf("%s: %+v, want INCOMPLETE %s\n%s", c.name, v, c.why, buf.String())
		}
	}
}

// A Grep/Glob probe that the CLI answers with the Read text is not refused by the rule (review #424 P3-1): the
// sim default is the Grep/Glob text of runs 2 and 3; `grepGlobReadText` gives the Read text to those probes.
func TestItem1GrepGlobProbesAnsweredWithTheReadTextAreNotTheRule(t *testing.T) {
	s := &sim{scope: true, grepGlobReadText: true}
	d, buf, _ := newTestDriver(t, s)
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Incomplete || v.Why != "probe_not_rule_denied_other" {
		t.Fatalf("verdict %+v\n%s", v, buf.String())
	}
	// and the default sim (the real texts) still PASSes
	d2, buf2, _ := newTestDriver(t, &sim{scope: true, resumeDeclines: true})
	if v2 := verdict(d2.RunItems(context.Background(), []int{1}), 1); v2.Result != Pass {
		t.Fatalf("default sim: %+v\n%s", v2, buf2.String())
	}
}
