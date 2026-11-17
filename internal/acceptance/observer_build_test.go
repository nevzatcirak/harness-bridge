//go:build linux

package acceptance

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

// ADR021-OBSERVER (#372): the assembly of the Observer from the live command's flags.

func TestBuildObserverWithNothingConfiguredChangesNothing(t *testing.T) {
	inner := &cannedRunner{}
	obs, r, err := BuildObserver(ObserverOptions{RunHome: tHome}, inner)
	if err != nil || obs != nil || r != Runner(inner) {
		t.Fatalf("%v %v %v", obs, r, err)
	}
}

func TestBuildObserverBuildsOnlyWhatWasAskedFor(t *testing.T) {
	home := tHome
	cdir := filepath.Join(realDir(t), "canaries")
	mkdirs(t, cdir)
	inner := &cannedRunner{}
	// only the canary directory: no history, no sink; the runner is not wrapped
	obs, r, err := BuildObserver(ObserverOptions{CanaryDir: cdir, RunHome: home}, inner)
	if err != nil {
		t.Fatal(err)
	}
	defer obs.Close()
	if obs.Scan == nil || obs.Tap != nil || obs.Sink != nil || r != Runner(inner) {
		t.Fatalf("%+v %v", obs, r)
	}
	if _, ok := obs.History(context.Background()); ok {
		t.Fatal("an unconfigured history reported an observation")
	}
	if _, ok := obs.SinkRequests(context.Background()); ok {
		t.Fatal("an unconfigured sink reported an observation")
	}
	// everything: the runner is wrapped, the sink listens on loopback
	hd := histDir(t)
	cdir2 := filepath.Join(realDir(t), "canaries")
	mkdirs(t, cdir2)
	obs2, r2, err := BuildObserver(ObserverOptions{HistoryDir: hd, CanaryDir: cdir2, SinkAddr: "127.0.0.1:0", RunHome: home}, inner)
	if err != nil {
		t.Fatal(err)
	}
	defer obs2.Close()
	if _, ok := r2.(RecordingRunner); !ok || obs2.Tap == nil || obs2.Scan == nil || obs2.Sink == nil {
		t.Fatalf("%+v %T", obs2, r2)
	}
	ta := obs2.Sink.ln.Addr().(*net.TCPAddr)
	if !ta.IP.IsLoopback() {
		t.Fatalf("the sink is not on loopback: %v", ta)
	}
	pairs := obs2.Describe()
	joined := ""
	for _, p := range pairs {
		joined += p + " "
	}
	for _, want := range []string{"history configured", "file_canaries configured", "sink configured", "sink_port "} {
		if !strings.Contains(joined, want) {
			t.Errorf("Describe lacks %q: %v", want, pairs)
		}
	}
}

// A refused component closes everything opened before it (no leaked listener) and reports an error.
func TestBuildObserverClosesWhatItOpenedOnError(t *testing.T) {
	hd := histDir(t)
	if _, _, err := BuildObserver(ObserverOptions{HistoryDir: hd, CanaryDir: "relative/dir", RunHome: tHome}, &cannedRunner{}); err == nil {
		t.Fatal("a relative canary directory was accepted")
	}
	// the history directory is released again: it can be opened afresh
	tap, err := NewHistoryTap(hd)
	if err != nil {
		t.Fatalf("the history directory stayed locked after a failed build: %v", err)
	}
	tap.Close()
	if _, _, err := BuildObserver(ObserverOptions{SinkAddr: "0.0.0.0:0"}, &cannedRunner{}); err == nil {
		t.Fatal("a non-loopback sink address was accepted")
	}
	if _, _, err := BuildObserver(ObserverOptions{HistoryDir: "/nonexistent-history-dir"}, &cannedRunner{}); err == nil {
		t.Fatal("a missing history directory was accepted")
	}
}
