package execution

// ADR-017 A2 S3 (D32): the native-read failure class, HARNESS_UNREACHABLE with
// unavailable_since, and the operator thresholds. Fixtures only; no live
// OpenCode and no host action.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
)

func s3Env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// A2.6 / test plan item 20: absent uses the default, a valid in-range integer is
// applied, and a non-integer or an out-of-range value is an error naming the
// variable. The CIRAK_* names are the only names read (ADR-026 S1 merged).
func TestS3ParseLivenessEnv(t *testing.T) {
	defaults := LivenessConfig{NoProgressS: DefaultNoProgressS, ToolLongS: DefaultToolLongS}
	cases := []struct {
		name   string
		env    map[string]string
		want   LivenessConfig
		errVar string
	}{
		{"absent", nil, defaults, ""},
		{"low bounds", map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "30", "CIRAK_LIVENESS_TOOL_LONG_S": "60"}, LivenessConfig{30, 60}, ""},
		{"high bounds", map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "900", "CIRAK_LIVENESS_TOOL_LONG_S": "3600"}, LivenessConfig{900, 3600}, ""},
		{"in range", map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "45", "CIRAK_LIVENESS_TOOL_LONG_S": "90"}, LivenessConfig{45, 90}, ""},
		{"no progress not an integer", map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "soon"}, LivenessConfig{}, "CIRAK_LIVENESS_NO_PROGRESS_S"},
		{"no progress too low", map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "29"}, LivenessConfig{}, "CIRAK_LIVENESS_NO_PROGRESS_S"},
		{"no progress too high", map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "901"}, LivenessConfig{}, "CIRAK_LIVENESS_NO_PROGRESS_S"},
		{"no progress negative", map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "-1"}, LivenessConfig{}, "CIRAK_LIVENESS_NO_PROGRESS_S"},
		{"no progress float", map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "1.5"}, LivenessConfig{}, "CIRAK_LIVENESS_NO_PROGRESS_S"},
		{"tool long not an integer", map[string]string{"CIRAK_LIVENESS_TOOL_LONG_S": "soon"}, LivenessConfig{}, "CIRAK_LIVENESS_TOOL_LONG_S"},
		{"tool long too low", map[string]string{"CIRAK_LIVENESS_TOOL_LONG_S": "59"}, LivenessConfig{}, "CIRAK_LIVENESS_TOOL_LONG_S"},
		{"tool long too high", map[string]string{"CIRAK_LIVENESS_TOOL_LONG_S": "3601"}, LivenessConfig{}, "CIRAK_LIVENESS_TOOL_LONG_S"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseLivenessEnv(s3Env(c.env))
			if c.errVar != "" {
				if err == nil || !strings.Contains(err.Error(), c.errVar) {
					t.Fatalf("error %v, want it to name %s", err, c.errVar)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %+v / %v, want %+v", got, err, c.want)
			}
		})
	}
	// The CIRAK_* names are the real names.
	got, err := ParseLivenessEnv(s3Env(map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "30", "CIRAK_LIVENESS_TOOL_LONG_S": "90"}))
	if err != nil || got != (LivenessConfig{30, 90}) {
		t.Fatalf("CIRAK_* not read: %+v %v", got, err)
	}
	// No legacy twin: the pre-rename names are ignored, never accepted.
	got, err = ParseLivenessEnv(s3Env(map[string]string{"BRIDGE_LIVENESS_NO_PROGRESS_S": "30", "BRIDGE_LIVENESS_TOOL_LONG_S": "90"}))
	if err != nil || got != defaults {
		t.Fatalf("a legacy name was accepted: %+v %v", got, err)
	}
}

func TestS3NewWithLivenessApplies(t *testing.T) {
	m := NewWithLiveness(&fake{}, LivenessConfig{NoProgressS: 30, ToolLongS: 90})
	t.Cleanup(m.Close)
	if m.stallThreshold != 30*time.Second || m.toolLongThreshold != 90*time.Second {
		t.Fatalf("thresholds %v / %v", m.stallThreshold, m.toolLongThreshold)
	}
	th := m.thresholdsObject()
	if th == nil || th.NoProgressS != 30 || th.ToolLongS != 90 {
		t.Fatalf("thresholds object %+v", th)
	}
}

// A2.4 item 1 / test plan item 9: the classification table. Only a transport
// error, a read failure/timeout or a 5xx counts; 3xx, 4xx, decode, over-size,
// local filesystem, cancellation and anything unrecognized never do.
func TestS3ClassifyNativeRead(t *testing.T) {
	live := context.Background()
	canceled, cancel := context.WithCancel(live)
	cancel()
	cases := []struct {
		name string
		ctx  context.Context
		mctx context.Context
		err  error
		want nativeFailure
	}{
		{"transport", live, live, opencode.ErrTransport, nativeUnavailable},
		{"response read", live, live, opencode.ErrResponseRead, nativeUnavailable},
		{"500", live, live, &opencode.HTTPError{Status: 500}, nativeUnavailable},
		{"502", live, live, &opencode.HTTPError{Status: 502}, nativeUnavailable},
		{"503", live, live, &opencode.HTTPError{Status: 503}, nativeUnavailable},
		{"504", live, live, &opencode.HTTPError{Status: 504}, nativeUnavailable},
		{"529", live, live, &opencode.HTTPError{Status: 529}, nativeUnavailable},
		{"408", live, live, &opencode.HTTPError{Status: 408}, nativeUnavailable},
		{"401", live, live, &opencode.HTTPError{Status: 401}, nativeRefused},
		{"403", live, live, &opencode.HTTPError{Status: 403}, nativeRefused},
		{"404", live, live, &opencode.HTTPError{Status: 404}, nativeRefused},
		{"429", live, live, &opencode.HTTPError{Status: 429}, nativeRefused},
		{"400", live, live, &opencode.HTTPError{Status: 400}, nativeRefused},
		{"302", live, live, &opencode.HTTPError{Status: 302}, nativeRefused},
		{"304", live, live, &opencode.HTTPError{Status: 304}, nativeRefused},
		{"decode", live, live, opencode.ErrInvalidResponse, nativeRefused},
		{"over-size", live, live, opencode.ErrResponseTooLarge, nativeRefused},
		{"unknown error", live, live, errors.New("offline"), nativeRefused},
		{"local filesystem", live, live, errFSTimeout, nativeLocalFS},
		{"canceled caller context", canceled, live, opencode.ErrTransport, nativeCanceled},
		{"canceled manager context", live, canceled, opencode.ErrTransport, nativeCanceled},
		{"context.Canceled with a live ctx", live, live, context.Canceled, nativeRefused},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyNativeRead(c.ctx, c.mctx, c.err); got != c.want {
				t.Fatalf("class %v, want %v", got, c.want)
			}
		})
	}
}

// A local filesystem timeout and an identity mismatch are their own states and
// never start or extend the native-unavailable window, whatever their age.
func TestS3LocalFSAndIdentityNeverCount(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	clock := &fakeClock{t: now}
	m := NewWithTiming(&fake{}, time.Minute, time.Millisecond)
	m.clock = clock.Now
	m.stallThreshold = time.Minute
	defer m.Close()
	x := &run{}
	for _, c := range []struct {
		name   string
		reason string
		err    error
	}{
		{"local fs", "OBSERVATION_UNAVAILABLE", errFSTimeout},
		{"identity", "IDENTITY_MISMATCH", Fail("FORBIDDEN", "workspace changed")},
	} {
		t.Run(c.name, func(t *testing.T) {
			clock.Advance(5 * time.Minute)
			l := m.nativeFailureLiveness(x, context.Background(), c.reason, c.err)
			if l.StallSuspected || l.StallReason != "" {
				t.Fatalf("non-counting failure became a stall: %+v", l)
			}
			if !x.unavailableSince.IsZero() || x.view.UnavailableSince != nil {
				t.Fatalf("window set by a non-counting failure: %v %v", x.unavailableSince, x.view.UnavailableSince)
			}
		})
	}
}

// A native read that keeps failing with a counting class for 120 s is
// HARNESS_UNREACHABLE with unavailable_since on the run; 60 s is not; a
// successful probe clears both. No native mutation and no state/guard change.
func TestS3UnreachableWindowAndClear(t *testing.T) {
	f := &fake{status: "idle"}
	start := time.Now()
	clock := &fakeClock{t: start}
	m := NewWithTiming(f, time.Minute, 2*time.Millisecond)
	m.clock = clock.Now
	m.stallThreshold = time.Minute
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "fixture", "s3-unreachable-key")
	if err != nil {
		t.Fatal(err)
	}
	locked(f, func() { f.readErrHistory = opencode.ErrTransport })
	got := waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.UnavailableSince != nil && x.Liveness != nil &&
			x.Liveness.NativeStatus == "unavailable" && x.Liveness.StallReason == ""
	})
	if !got.UnavailableSince.Equal(start) {
		t.Fatalf("unavailable_since %v, want %v", got.UnavailableSince, start)
	}
	if got.Liveness.StallSuspected {
		t.Fatalf("suspected before the window: %+v", got.Liveness)
	}
	if th := got.Liveness.Thresholds; th == nil || th.NoProgressS != 60 || th.ToolLongS != 600 {
		t.Fatalf("thresholds %+v", th)
	}
	since := *got.UnavailableSince

	clock.Advance(60 * time.Second)
	got = waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && !x.Liveness.ObservedAt.Before(clock.Now()) &&
			x.Liveness.StallReason == "" && x.UnavailableSince != nil && x.UnavailableSince.Equal(since)
	})
	if got.Liveness.StallSuspected {
		t.Fatalf("suspected at 60 s: %+v", got.Liveness)
	}

	clock.Advance(60 * time.Second)
	got = waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.Liveness != nil && x.Liveness.StallReason == "HARNESS_UNREACHABLE" &&
			x.Liveness.StallSuspected && x.UnavailableSince != nil && x.UnavailableSince.Equal(since)
	})
	if got.Liveness.NativeStatus != "unavailable" {
		t.Fatalf("native status %q", got.Liveness.NativeStatus)
	}

	locked(f, func() { f.readErrHistory = nil })
	got = waitLiveness(t, m, x.ID, func(x Execution) bool {
		return x.UnavailableSince == nil && x.Liveness != nil && x.Liveness.NativeStatus != "unavailable"
	})
	if got.Liveness.StallReason == "HARNESS_UNREACHABLE" {
		t.Fatalf("success kept the reason: %+v", got.Liveness)
	}
	if got.State != "running" {
		t.Fatalf("state %+v", got.State)
	}
	guardHeld(t, m)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sends != 1 || f.aborts != 0 {
		t.Fatalf("native mutation: sends=%d aborts=%d", f.sends, f.aborts)
	}
}

// A non-counting failure (3xx, 4xx, decode, over-size, unrecognized) never
// produces HARNESS_UNREACHABLE, however long it persists.
func TestS3NonCountingFailureNeverUnreachable(t *testing.T) {
	cases := map[string]error{
		"401":       &opencode.HTTPError{Status: 401},
		"404":       &opencode.HTTPError{Status: 404},
		"429":       &opencode.HTTPError{Status: 429},
		"302":       &opencode.HTTPError{Status: 302},
		"decode":    opencode.ErrInvalidResponse,
		"over-size": opencode.ErrResponseTooLarge,
		"unknown":   errors.New("offline"),
	}
	for name, readErr := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fake{status: "idle"}
			start := time.Now()
			clock := &fakeClock{t: start}
			m := NewWithTiming(f, time.Minute, 2*time.Millisecond)
			m.clock = clock.Now
			m.stallThreshold = time.Minute
			t.Cleanup(m.Close)
			x, err := m.Send(context.Background(), "ses_test", "fixture", "s3-refused-"+name)
			if err != nil {
				t.Fatal(err)
			}
			locked(f, func() { f.readErrHistory = readErr })
			got := waitLiveness(t, m, x.ID, func(x Execution) bool {
				return x.Liveness != nil && x.Liveness.NativeStatus == "unavailable" && x.Liveness.StallReason == ""
			})
			if got.UnavailableSince != nil || got.Liveness.StallSuspected {
				t.Fatalf("non-counting failure set a window/stall: %+v %+v", got.UnavailableSince, got.Liveness)
			}
			clock.Advance(5 * time.Minute)
			got = waitLiveness(t, m, x.ID, func(x Execution) bool {
				return x.Liveness != nil && !x.Liveness.ObservedAt.Before(clock.Now())
			})
			if got.Liveness.StallSuspected || got.Liveness.StallReason != "" || got.UnavailableSince != nil {
				t.Fatalf("persistent non-counting failure became a stall: %+v %+v", got.UnavailableSince, got.Liveness)
			}
		})
	}
}

// The applied thresholds are echoed in the liveness block and stay optional for
// the old JSON shape.
func TestS3ThresholdsJSON(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	th := Thresholds{NoProgressS: 120, ToolLongS: 600}
	e := a2Evidence(now, opencode.Status{Type: "busy"}, true)
	e.thresholds = th
	l := assessLiveness(now, a2Threshold, e)
	if l.Thresholds == nil || *l.Thresholds != th {
		t.Fatalf("assessLiveness thresholds %+v", l.Thresholds)
	}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var got Thresholds
	if err := json.Unmarshal(m["thresholds"], &got); err != nil || got != th {
		t.Fatalf("thresholds JSON %s: %+v %v", m["thresholds"], got, err)
	}
	// A block without thresholds omits the object.
	plain, _ := json.Marshal(&Liveness{NativeStatus: "busy", ObservedAt: now})
	var pm map[string]json.RawMessage
	_ = json.Unmarshal(plain, &pm)
	if _, ok := pm["thresholds"]; ok {
		t.Fatalf("empty thresholds exported: %s", plain)
	}
}

// unavailable_since is report-only memory on the execution record: copied on a
// snapshot and additive in JSON.
func TestS3UnavailableSinceSnapshotAndJSON(t *testing.T) {
	now := time.Date(2026, 11, 21, 17, 48, 5, 0, time.UTC)
	x := Execution{ID: "e", Started: now, Updated: now, UnavailableSince: &now}
	c := snapshot(x)
	if c.UnavailableSince == x.UnavailableSince || !c.UnavailableSince.Equal(now) {
		t.Fatalf("snapshot aliased unavailable_since: %v", c.UnavailableSince)
	}
	b, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"unavailable_since":"2026-11-21T17:48:05Z"`) {
		t.Fatalf("unavailable_since JSON: %s", b)
	}
}

// copyLiveness does not alias the thresholds object.
func TestS3CopyLivenessThresholds(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000).UTC()
	th := Thresholds{NoProgressS: 120, ToolLongS: 600}
	l := &Liveness{NativeStatus: "busy", Thresholds: &th, ObservedAt: now}
	c := copyLiveness(l)
	if c.Thresholds == l.Thresholds {
		t.Fatal("copyLiveness aliased the thresholds")
	}
	c.Thresholds.NoProgressS = 1
	if l.Thresholds.NoProgressS != 120 {
		t.Fatalf("mutating the copy changed the source: %+v", l.Thresholds)
	}
}

// With the report switched off, the failure path writes neither liveness nor
// unavailable_since (the same rule as the retry memory).
func TestS3NoLivenessWritesNoWindow(t *testing.T) {
	f := &fake{status: "idle"}
	m := NewWithTiming(f, time.Minute, 2*time.Millisecond)
	m.noLiveness = true
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_test", "fixture", "s3-off-key")
	if err != nil {
		t.Fatal(err)
	}
	locked(f, func() { f.readErrHistory = opencode.ErrTransport })
	time.Sleep(50 * time.Millisecond)
	got, err := m.Snapshot(context.Background(), x.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Liveness != nil || got.UnavailableSince != nil {
		t.Fatalf("report off but A2 S3 wrote liveness=%+v since=%v", got.Liveness, got.UnavailableSince)
	}
}

// The environment parser feeds the manager and the manager's applied values
// reach the JSON block (the "echoed in thresholds" half of item 20).
func TestS3ConfigReachesTheManager(t *testing.T) {
	cfg, err := ParseLivenessEnv(s3Env(map[string]string{"CIRAK_LIVENESS_NO_PROGRESS_S": "45", "CIRAK_LIVENESS_TOOL_LONG_S": "90"}))
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{status: "idle"}
	m := NewWithLiveness(f, cfg)
	t.Cleanup(m.Close)
	m.clock = func() time.Time { return time.UnixMilli(1_700_000_000_000).UTC() }
	now := m.clock()
	l := assessLiveness(now, m.stallThreshold, livenessEvidence{
		status: opencode.Status{Type: "busy"}, statusExists: true, userFound: true,
		started: now.Add(-time.Second), thresholds: *m.thresholdsObject(),
	})
	if l.Thresholds == nil || l.Thresholds.NoProgressS != 45 || l.Thresholds.ToolLongS != 90 {
		t.Fatalf("liveness thresholds %+v", l.Thresholds)
	}
}
