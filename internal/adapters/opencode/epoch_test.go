package opencode

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ADR-024 A1 S7 (D21): the Bridge-observed continuity epoch. Fake SSE only; no OpenCode contact.

// fakeClock is a manual clock: After(d) fires when Advance moves Now to or past now+d.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}
type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var keep []*fakeTimer
	var fire []*fakeTimer
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			fire = append(fire, t)
		} else {
			keep = append(keep, t)
		}
	}
	c.timers = keep
	now := c.now
	c.mu.Unlock()
	for _, t := range fire {
		t.ch <- now
	}
}

// pending reports how many timers wait (the tracker is in a backoff or gap wait).
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// hasTimer reports whether a timer is due exactly d from now (a backoff of that length is waiting).
func (c *fakeClock) hasTimer(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.timers {
		if t.at.Sub(c.now) == d {
			return true
		}
	}
	return false
}

// fakeStream is one connection: Send writes SSE bytes, End closes it.
type fakeStream struct {
	pr *io.PipeReader
	pw *io.PipeWriter
}

func (s *fakeStream) Read(p []byte) (int, error) { return s.pr.Read(p) }
func (s *fakeStream) Close() error               { return s.pr.Close() }
func (s *fakeStream) Send(t *testing.T, frame string) {
	t.Helper()
	if _, err := io.WriteString(s.pw, frame); err != nil {
		t.Fatalf("send: %v", err)
	}
}
func (s *fakeStream) End() { _ = s.pw.Close() }

func frame(typ string) string {
	return "data: {\"payload\":{\"type\":\"" + typ + "\",\"properties\":{}}}\n\n"
}

// fakeServer hands out one fakeStream per Open, or an error.
type fakeServer struct {
	mu      sync.Mutex
	opens   int
	streams chan *fakeStream
	failNow error
}

func newFakeServer() *fakeServer { return &fakeServer{streams: make(chan *fakeStream, 16)} }
func (f *fakeServer) open(ctx context.Context) (io.ReadCloser, error) {
	f.mu.Lock()
	f.opens++
	err := f.failNow
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	s := &fakeStream{pr: pr, pw: pw}
	select {
	case f.streams <- s:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s, nil
}
func (f *fakeServer) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}
func (f *fakeServer) next(t *testing.T) *fakeStream {
	t.Helper()
	select {
	case s := <-f.streams:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("the tracker did not open a stream")
		return nil
	}
}

func newTracker(t *testing.T, srv *fakeServer, clk *fakeClock) *EpochTracker {
	t.Helper()
	tr, err := NewEpochTracker(EpochTrackerConfig{Open: srv.open, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	tr.Start()
	t.Cleanup(tr.Stop)
	return tr
}

// waitFor polls cond for up to 3 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func armed(t *testing.T, tr *EpochTracker) string {
	t.Helper()
	waitFor(t, "the epoch to arm", func() bool { _, ok := tr.Epoch(); return ok })
	tok, _ := tr.Epoch()
	return tok
}

func TestEpochIsNotArmedBeforeTheFirstHeartbeat(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr := newTracker(t, srv, clk)
	s := srv.next(t)
	if tok, ok := tr.Epoch(); ok || tok != "" {
		t.Fatalf("armed before any event: %q %v", tok, ok)
	}
	s.Send(t, frame("server.connected")) // the hello does not arm
	s.Send(t, frame("message.updated"))
	time.Sleep(30 * time.Millisecond)
	if _, ok := tr.Epoch(); ok {
		t.Fatal("armed at the hello or on an unrelated event")
	}
	s.Send(t, frame("server.heartbeat"))
	tok := armed(t, tr)
	if len(tok) != 32 {
		t.Fatalf("the token is not 128 random bits as hex: %q", tok)
	}
	if e, err := tr.NativeEpoch(context.Background()); err != nil || e != tok {
		t.Fatalf("seam adapter: %q %v", e, err)
	}
}

func TestEpochSeamReturnsEmptyAndNoErrorWhileUnarmed(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr := newTracker(t, srv, clk)
	if e, err := tr.NativeEpoch(context.Background()); e != "" || err != nil {
		t.Fatalf("unarmed seam: %q %v (an error would fail the scope read)", e, err)
	}
	var nilTracker *EpochTracker
	if e, err := nilTracker.NativeEpoch(context.Background()); e != "" || err != nil {
		t.Fatalf("nil tracker seam: %q %v", e, err)
	}
}

func replaced(t *testing.T, tr *EpochTracker, old string) {
	t.Helper()
	waitFor(t, "the epoch to be replaced", func() bool { tok, _ := tr.Epoch(); return tok != old })
}

func TestEpochIsReplacedByAnyInstanceDisposedEvent(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr := newTracker(t, srv, clk)
	s := srv.next(t)
	s.Send(t, frame("server.heartbeat"))
	e1 := armed(t, tr)
	// a dispose of a directory the run never used still replaces the epoch
	s.Send(t, "data: {\"directory\":\"/some/other/dir\",\"payload\":{\"type\":\"server.instance.disposed\",\"properties\":{\"directory\":\"/some/other/dir\"}}}\n\n")
	replaced(t, tr, e1)
	e2, ok := tr.Epoch()
	if !ok || e2 == e1 || e2 == "" {
		t.Fatalf("after a dispose: %q %v (was %q)", e2, ok, e1)
	}
}

func TestEpochIsReplacedByGlobalDisposedAlone(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr := newTracker(t, srv, clk)
	s := srv.next(t)
	s.Send(t, frame("server.heartbeat"))
	e1 := armed(t, tr)
	// global.disposed arrives alone (no per-instance event before it, as after a failed disposeAll)
	s.Send(t, "data: {\"directory\":\"global\",\"payload\":{\"type\":\"global.disposed\",\"properties\":{}}}\n\n")
	replaced(t, tr, e1)
}

func TestEpochIsReplacedByTheTopLevelTypeShapeToo(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr := newTracker(t, srv, clk)
	s := srv.next(t)
	s.Send(t, "data: {\"type\":\"server.heartbeat\",\"properties\":{}}\n\n")
	e1 := armed(t, tr)
	s.Send(t, "data: {\"type\":\"server.instance.disposed\",\"properties\":{}}\n\n")
	replaced(t, tr, e1)
}

// A disposed-like event of a type this version does not know is treated as a disposal (fail closed).
func TestEpochIsReplacedByAnUnknownDisposedLikeEvent(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr := newTracker(t, srv, clk)
	s := srv.next(t)
	s.Send(t, frame("server.heartbeat"))
	e1 := armed(t, tr)
	s.Send(t, frame("project.instance.Disposed"))
	replaced(t, tr, e1)
}

func TestEpochIgnoresUnrelatedEventsAndOversizedFrames(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr := newTracker(t, srv, clk)
	s := srv.next(t)
	s.Send(t, frame("server.heartbeat"))
	e1 := armed(t, tr)
	for i := 0; i < 50; i++ {
		s.Send(t, frame("message.part.updated"))
	}
	// an oversized frame is bounded and ignored (a disposal event is tiny)
	s.Send(t, "data: {\"payload\":{\"type\":\"message.part.updated\",\"properties\":{\"x\":\""+strings.Repeat("a", 3*epochMaxEvent)+"\"}}}\n\n")
	s.Send(t, ": a comment line\nretry: 1000\nid: 7\n\n")
	s.Send(t, frame("server.heartbeat"))
	time.Sleep(50 * time.Millisecond)
	if tok, ok := tr.Epoch(); !ok || tok != e1 {
		t.Fatalf("unrelated or oversized events changed the epoch: %q %v (want %q)", tok, ok, e1)
	}
}

func TestEpochIsInvalidatedByAMalformedEvent(t *testing.T) {
	for name, bad := range map[string]string{
		"not JSON":               "data: this is not json\n\n",
		"JSON that is no object": "data: [1,2,3]\n\n",
		"no type at all":         "data: {\"properties\":{}}\n\n",
		"type too long":          "data: {\"type\":\"" + strings.Repeat("x", 200) + "\"}\n\n",
	} {
		srv, clk := newFakeServer(), newFakeClock()
		tr := newTracker(t, srv, clk)
		s := srv.next(t)
		s.Send(t, frame("server.heartbeat"))
		e1 := armed(t, tr)
		s.Send(t, bad)
		waitFor(t, name+": the epoch to be invalidated", func() bool { tok, ok := tr.Epoch(); return !ok || tok != e1 })
		if _, ok := tr.Epoch(); ok {
			t.Fatalf("%s: still armed after a malformed event", name)
		}
		// the next heartbeat of the same connection re-arms with a NEW token
		s.Send(t, frame("server.heartbeat"))
		e2 := armed(t, tr)
		if e2 == e1 {
			t.Fatalf("%s: the old token came back", name)
		}
		tr.Stop()
	}
}

func TestEpochIsReplacedWhenTheStreamEndsAndNeverReusedAcrossReconnects(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr := newTracker(t, srv, clk)
	s := srv.next(t)
	s.Send(t, frame("server.heartbeat"))
	e1 := armed(t, tr)
	s.End()
	waitFor(t, "disarm after the stream ended", func() bool { _, ok := tr.Epoch(); return !ok })
	waitFor(t, "the reconnect timer", func() bool { return clk.hasTimer(time.Second) })
	clk.Advance(time.Second) // the first backoff
	s2 := srv.next(t)
	s2.Send(t, frame("server.connected"))
	time.Sleep(20 * time.Millisecond)
	if _, ok := tr.Epoch(); ok {
		t.Fatal("armed again before the first heartbeat of the new connection")
	}
	s2.Send(t, frame("server.heartbeat"))
	e2 := armed(t, tr)
	if e2 == e1 {
		t.Fatal("the epoch survived a reconnect")
	}
}

func TestEpochHeartbeatGapBoundary(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr := newTracker(t, srv, clk)
	s := srv.next(t)
	s.Send(t, frame("server.heartbeat"))
	e1 := armed(t, tr)
	waitFor(t, "the gap timer", func() bool { return clk.pending() > 0 })
	clk.Advance(25*time.Second - time.Nanosecond)
	time.Sleep(30 * time.Millisecond)
	if tok, ok := tr.Epoch(); !ok || tok != e1 {
		t.Fatalf("replaced 1 ns before the 25 s gap: %q %v", tok, ok)
	}
	clk.Advance(time.Nanosecond) // exactly 25 s without an event
	waitFor(t, "the gap rule", func() bool { _, ok := tr.Epoch(); return !ok })
	// the connection is dropped and re-opened after the backoff; the old token never returns
	waitFor(t, "the reconnect timer", func() bool { return clk.hasTimer(time.Second) })
	clk.Advance(time.Second)
	s2 := srv.next(t)
	s2.Send(t, frame("server.heartbeat"))
	if e2 := armed(t, tr); e2 == e1 {
		t.Fatal("the epoch survived a heartbeat gap")
	}
}

// Any event, not only a heartbeat, resets the gap.
func TestEpochAnyEventResetsTheGap(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr := newTracker(t, srv, clk)
	s := srv.next(t)
	s.Send(t, frame("server.heartbeat"))
	e1 := armed(t, tr)
	waitFor(t, "the gap timer", func() bool { return clk.pending() > 0 })
	clk.Advance(20 * time.Second)
	s.Send(t, frame("message.updated"))
	time.Sleep(30 * time.Millisecond)
	clk.Advance(20 * time.Second) // 40 s since the heartbeat, 20 s since the last event
	time.Sleep(30 * time.Millisecond)
	if tok, ok := tr.Epoch(); !ok || tok != e1 {
		t.Fatalf("an event did not reset the gap: %q %v", tok, ok)
	}
	clk.Advance(5 * time.Second) // 25 s since the last event
	waitFor(t, "the gap rule after the last event", func() bool { _, ok := tr.Epoch(); return !ok })
}

func TestEpochBackoffDoublesToTheCapAndResetsAfterAnArmedConnection(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	srv.failNow = errors.New("401")
	tr := newTracker(t, srv, clk)
	_ = tr
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	opens := 0
	for i, d := range want {
		waitFor(t, "an open attempt", func() bool { return srv.openCount() > opens })
		opens = srv.openCount()
		waitFor(t, "the backoff timer", func() bool { return clk.hasTimer(d) })
		clk.Advance(d - time.Nanosecond)
		time.Sleep(15 * time.Millisecond)
		if srv.openCount() != opens {
			t.Fatalf("attempt %d reconnected before its %v backoff", i, d)
		}
		if i == len(want)-1 {
			srv.mu.Lock() // the next attempt (after the last, capped wait) succeeds
			srv.failNow = nil
			srv.mu.Unlock()
		}
		clk.Advance(time.Nanosecond)
	}
	// a connection that armed resets the backoff to 1 s
	s := srv.next(t)
	s.Send(t, frame("server.heartbeat"))
	armed(t, tr)
	s.End()
	waitFor(t, "the reconnect timer", func() bool { return clk.hasTimer(time.Second) })
	clk.Advance(time.Second)
	srv.next(t) // reopened after 1 s again, not 30 s
}

func TestEpochNeverArmsOnARefusedOrMalformedStream(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	srv.failNow = errors.New("opencode HTTP 401")
	tr := newTracker(t, srv, clk)
	waitFor(t, "an attempt", func() bool { return srv.openCount() > 0 })
	if _, ok := tr.Epoch(); ok {
		t.Fatal("armed on a refused stream")
	}
	// a stream of garbage never arms either
	srv2, clk2 := newFakeServer(), newFakeClock()
	tr2 := newTracker(t, srv2, clk2)
	s := srv2.next(t)
	s.Send(t, "data: <html>login</html>\n\n")
	time.Sleep(40 * time.Millisecond)
	if _, ok := tr2.Epoch(); ok {
		t.Fatal("armed on a malformed stream")
	}
}

// The token is the Bridge's own random value: the same native events give different tokens.
func TestEpochTokenIsUnrelatedToNativeValues(t *testing.T) {
	var toks []string
	for i := 0; i < 2; i++ {
		srv, clk := newFakeServer(), newFakeClock()
		tr := newTracker(t, srv, clk)
		s := srv.next(t)
		s.Send(t, "data: {\"id\":\"evt_0000000000000000000000000001\",\"payload\":{\"type\":\"server.heartbeat\",\"properties\":{}}}\n\n")
		tok := armed(t, tr)
		if strings.Contains(tok, "evt_") || strings.Contains(tok, "0000000001") {
			t.Fatalf("the token is derived from an event ID: %q", tok)
		}
		toks = append(toks, tok)
		tr.Stop()
	}
	if toks[0] == toks[1] {
		t.Fatal("two servers with identical events gave the same token")
	}
}

func TestEpochRandomnessFailureNeverArms(t *testing.T) {
	srv, clk := newFakeServer(), newFakeClock()
	tr, err := NewEpochTracker(EpochTrackerConfig{Open: srv.open, Clock: clk, Rand: bytes.NewReader(nil)})
	if err != nil {
		t.Fatal(err)
	}
	tr.Start()
	defer tr.Stop()
	time.Sleep(40 * time.Millisecond)
	if _, ok := tr.Epoch(); ok {
		t.Fatal("armed without a random token")
	}
}

func TestEpochStopLeavesNoGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		srv, clk := newFakeServer(), newFakeClock()
		tr, err := NewEpochTracker(EpochTrackerConfig{Open: srv.open, Clock: clk})
		if err != nil {
			t.Fatal(err)
		}
		tr.Start()
		s := srv.next(t)
		s.Send(t, frame("server.heartbeat"))
		armed(t, tr)
		tr.Stop()
		tr.Stop() // idempotent
		if _, ok := tr.Epoch(); ok {
			t.Fatal("armed after Stop")
		}
	}
	waitFor(t, "the goroutines to end", func() bool { return runtime.NumGoroutine() <= before })
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	if strings.Contains(string(buf[:n]), "opencode.(*EpochTracker)") {
		t.Fatalf("a tracker goroutine is still running:\n%s", buf[:n])
	}
}

func TestEpochTrackerNeedsAnOpener(t *testing.T) {
	if _, err := NewEpochTracker(EpochTrackerConfig{}); err == nil {
		t.Fatal("a tracker without an opener was accepted")
	}
}

// ---- the real opener against an httptest server (loopback) ----

func TestGlobalEventOpenerAuthenticatesAndArmsOnARealStream(t *testing.T) {
	var gotAuth, gotPath, gotAccept string
	srv := httptestServer(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotAccept = r.Header.Get("Authorization"), r.URL.Path, r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, frame("server.connected"))
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, frame("server.heartbeat"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	defer srv.Close()
	c, err := New(srv.URL, "u", "secret")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewEpochTracker(EpochTrackerConfig{Open: c.GlobalEventOpener()})
	if err != nil {
		t.Fatal(err)
	}
	tr.Start()
	defer tr.Stop()
	armed(t, tr)
	if gotPath != "/global/event" || !strings.HasPrefix(gotAuth, "Basic ") || gotAccept != "text/event-stream" {
		t.Fatalf("request: path %q auth %q accept %q", gotPath, gotAuth, gotAccept)
	}
}

func TestGlobalEventOpenerRefusesEverythingButAnEventStream(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"401": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) },
		"500": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
		"html page": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>")
		},
		"json": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{}")
		},
		"redirect (ignored)": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) },
	} {
		srv := httptestServer(h)
		c, err := New(srv.URL, "", "")
		if err != nil {
			t.Fatal(err)
		}
		rc, err := c.GlobalEventOpener()(context.Background())
		if err == nil {
			rc.Close()
			t.Errorf("%s: opened", name)
		}
		srv.Close()
	}
	var nilClient *Client
	if nilClient.GlobalEventOpener() != nil {
		t.Error("a nil client returned an opener")
	}
}

func httptestServer(h http.HandlerFunc) *httptest.Server { return httptest.NewServer(h) }
