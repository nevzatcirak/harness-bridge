package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
)

// ADR-024 A1 S7 (D21 Q2): CIRAK_PERMISSION_INCARNATION, off by default.

func TestPermissionIncarnationModeIsOffByDefaultAndStrict(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "CIRAK_PERMISSION_INCARNATION" {
				return v
			}
			return ""
		}
	}
	for in, want := range map[string]string{"": "off", "off": "off", " off ": "off", "observed": "observed", " observed\n": "observed"} {
		if got, err := permissionIncarnationMode(env(in)); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"on", "true", "1", "Observed", "native", "b", "observed,off"} {
		if _, err := permissionIncarnationMode(env(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// A native that counts the requests of the global event stream and serves a heartbeat on it.
func eventNative(t *testing.T) (*opencode.Client, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/event" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "data: {\"payload\":{\"type\":\"server.connected\",\"properties\":{}}}\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: {\"payload\":{\"type\":\"server.heartbeat\",\"properties\":{}}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)
	c, err := opencode.New(ts.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c, &hits
}

func TestIncarnationTrackerStartsOnlyForObservedWithRejectOnly(t *testing.T) {
	for _, c := range []struct{ relay, inc string }{{"reject_only", "off"}, {"off", "observed"}, {"off", "off"}} {
		client, hits := eventNative(t)
		inc, stop, err := startIncarnation(c.relay, c.inc, client)
		if err != nil {
			t.Fatalf("relay=%s incarnation=%s: %v", c.relay, c.inc, err)
		}
		time.Sleep(60 * time.Millisecond)
		stop()
		if inc != nil || hits.Load() != 0 {
			t.Errorf("relay=%s incarnation=%s: started a tracker (source %v, %d requests)", c.relay, c.inc, inc, hits.Load())
		}
	}
	client, hits := eventNative(t)
	inc, stop, err := startIncarnation("reject_only", "observed", client)
	if err != nil || inc == nil {
		t.Fatal("observed with reject_only started no tracker")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if e, err := inc.NativeEpoch(context.Background()); err == nil && e != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tracker never armed against a real stream")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if hits.Load() < 1 {
		t.Fatal("no request reached the global event stream")
	}
	stop()
	if e, _ := inc.NativeEpoch(context.Background()); e != "" {
		t.Fatalf("armed after stop: %q", e)
	}
}

// A tracker that cannot be built is a startup error, never a silent "off" (#414 P3-2): observed with no usable
// client (no opener) is refused with an error that names the variable, and the off cases stay error-free.
func TestIncarnationObservedThatCannotStartIsAStartupError(t *testing.T) {
	inc, stop, err := startIncarnation("reject_only", "observed", nil)
	if err == nil || inc != nil || stop == nil {
		t.Fatalf("observed without a client started silently: inc=%v err=%v", inc, err)
	}
	if !strings.Contains(err.Error(), "CIRAK_PERMISSION_INCARNATION=observed") {
		t.Fatalf("the error does not name the variable: %v", err)
	}
	stop() // safe
	for _, c := range []struct{ relay, inc string }{{"reject_only", "off"}, {"off", "observed"}, {"off", "off"}} {
		if inc, _, err := startIncarnation(c.relay, c.inc, nil); err != nil || inc != nil {
			t.Errorf("relay=%s incarnation=%s: %v %v", c.relay, c.inc, inc, err)
		}
	}
}
