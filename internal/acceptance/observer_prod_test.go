//go:build linux

package acceptance

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode"
)

// ADR021-OBSERVER (#372): the production Observer and its use by the driver (item 1).

func TestProdObserverWithoutComponentsObservesNothing(t *testing.T) {
	for name, o := range map[string]*ProdObserver{"nil observer": nil, "no components": {}} {
		if rec, ok := o.History(context.Background()); ok {
			t.Errorf("%s: history observed: %q", name, rec)
		}
		if n, ok := o.FileCanaries(context.Background()); ok || n != 0 {
			t.Errorf("%s: file canaries observed: %d %v", name, n, ok)
		}
		if n, ok := o.SinkRequests(context.Background()); ok || n != 0 {
			t.Errorf("%s: sink observed: %d %v", name, n, ok)
		}
		if err := o.Close(); err != nil {
			t.Errorf("%s: Close: %v", name, err)
		}
	}
}

// uuidRunner rewrites the simulator's short session id into a valid UUID (the history store keys on it).
type uuidRunner struct{ inner Runner }

func (u uuidRunner) Run(ctx context.Context, inv Invocation) (Result, error) {
	res, err := u.inner.Run(ctx, inv)
	res.Stdout = bytes.ReplaceAll(res.Stdout, []byte(`"session_id":"s"`), []byte(`"session_id":"`+histUUID+`"`))
	return res, err
}

type observerFixture struct {
	obs  *ProdObserver
	home string
	cdir string
}

func fullObserver(t *testing.T) observerFixture {
	t.Helper()
	home := tHome // the fake driver's run home; the scan only compares path strings with it
	cdir := filepath.Join(realDir(t), "canaries")
	mkdirs(t, cdir)
	scan, err := NewFileCanaryScan(cdir, home)
	if err != nil {
		t.Fatal(err)
	}
	tap, err := NewHistoryTap(histDir(t))
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewLoopbackSink("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	o := &ProdObserver{Tap: tap, Scan: scan, Sink: sink}
	t.Cleanup(func() { _ = o.Close() })
	return observerFixture{obs: o, home: home, cdir: cdir}
}

// newObservedDriver builds the fake driver with the production observer and the recording runner.
func newObservedDriver(t *testing.T, s *sim, fx observerFixture) (*Driver, *bytes.Buffer) {
	t.Helper()
	d, buf, _ := newTestDriver(t, s)
	d.Runner = RecordingRunner{Inner: uuidRunner{inner: s}, Tap: fx.obs.Tap}
	d.Obs = fx.obs
	return d, buf
}

func TestItem1PassesWithEveryCheckObservedAndClean(t *testing.T) {
	fx := fullObserver(t)
	d, buf := newObservedDriver(t, &sim{}, fx)
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Pass {
		t.Fatalf("%+v\n%s", v, buf.String())
	}
	for _, want := range []string{"no_decoy_leak=true", "no_file_canary=true", "no_sink_request=true", "history_observed=true", "sink_observed=true", "file_canaries_observed=true"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("evidence lacks %q:\n%s", want, buf.String())
		}
	}
}

func TestItem1FailsWhenTheSinkReceivedARequest(t *testing.T) {
	fx := fullObserver(t)
	d, buf := newObservedDriver(t, &sim{}, fx)
	r, err := http.Get(fx.obs.Sink.URL() + "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Fail || !strings.Contains(buf.String(), "no_sink_request=false") {
		t.Fatalf("%+v\n%s", v, buf.String())
	}
}

func TestItem1FailsWhenACanaryFileAppeared(t *testing.T) {
	fx := fullObserver(t)
	d, buf := newObservedDriver(t, &sim{}, fx)
	if err := os.WriteFile(filepath.Join(fx.cdir, "hook-ran"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Fail || !strings.Contains(buf.String(), "no_file_canary=false") {
		t.Fatalf("%+v\n%s", v, buf.String())
	}
}

// The decoy marker in the history record fails item 1 on its own (the raw stream is clean here).
func TestItem1FailsWhenTheDecoyReachedTheHistoryRecord(t *testing.T) {
	fx := fullObserver(t)
	d, buf := newObservedDriver(t, &sim{}, fx)
	seed := make([]byte, 256) // distinct bytes: the decoy marker and the control marker must differ
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	d.Rand = bytes.NewReader(seed)
	marker, err := claudecode.NewCanaryMarker(bytes.NewReader(seed)) // the driver's first marker (the decoy's)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.obs.Tap.Record(deniedStream(histUUID, "leaked "+marker)); err != nil {
		t.Fatal(err)
	}
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Fail || !strings.Contains(buf.String(), "no_decoy_leak=false") {
		t.Fatalf("%+v\n%s", v, buf.String())
	}
	if strings.Contains(buf.String(), marker) {
		t.Fatal("the marker reached the evidence")
	}
}

// Every component missing from the flags leaves its check unknown and item 1 INCOMPLETE.
func TestItem1IsIncompleteForEachMissingComponent(t *testing.T) {
	cases := []struct {
		name    string
		drop    func(o *ProdObserver)
		unknown string
		missing string
	}{
		{"no history", func(o *ProdObserver) { o.Tap = nil }, "no_decoy_leak=unknown", "unobserved_history"},
		{"no file canary", func(o *ProdObserver) { o.Scan = nil }, "no_file_canary=unknown", "unobserved_file_canary"},
		{"no sink", func(o *ProdObserver) { o.Sink = nil }, "no_sink_request=unknown", "unobserved_sink"},
	}
	for _, c := range cases {
		fx := fullObserver(t)
		c.drop(fx.obs)
		d, buf := newObservedDriver(t, &sim{}, fx)
		v := verdict(d.RunItems(context.Background(), []int{1}), 1)
		if v.Result != Incomplete || !strings.Contains(v.Why, c.missing) {
			t.Errorf("%s: %+v\n%s", c.name, v, buf.String())
		}
		if !strings.Contains(buf.String(), c.unknown) || strings.Contains(buf.String(), "verdict=pass") {
			t.Errorf("%s: evidence:\n%s", c.name, buf.String())
		}
	}
}

// A failed observation component (here the history tap after a recording failure) makes item 1
// INCOMPLETE, never PASS.
func TestItem1IsIncompleteAfterARecordingFailure(t *testing.T) {
	fx := fullObserver(t)
	d, buf := newObservedDriver(t, &sim{}, fx)
	_ = fx.obs.Tap.Record(deniedStream("not-a-uuid", "x")) // marks the tap failed
	v := verdict(d.RunItems(context.Background(), []int{1}), 1)
	if v.Result != Incomplete || strings.Contains(buf.String(), "verdict=pass") {
		t.Fatalf("%+v\n%s", v, buf.String())
	}
}
