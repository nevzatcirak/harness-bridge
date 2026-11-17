package acceptance

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ADR021-OBSERVER (#372): the loopback sink that counts the requests a hostile root's ANTHROPIC_BASE_URL
// would send (ADR-021 §8.3: "the sink received no request").

func TestSinkRefusesNonLoopbackBinds(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0", ":0", "10.1.2.3:0", "192.168.0.7:80", "example.com:80", "localhost:0", "", "127.0.0.1", "[::ffff:10.0.0.1]:0"} {
		s, err := NewLoopbackSink(addr)
		if err == nil {
			_ = s.Close()
			t.Errorf("bound %q", addr)
		}
	}
}

func TestSinkCountsRequestsAndDiscardsBodies(t *testing.T) {
	s, err := NewLoopbackSink("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !strings.HasPrefix(s.URL(), "http://127.0.0.1:") {
		t.Fatalf("url %q", s.URL())
	}
	if n, ok := s.Count(); n != 0 || !ok {
		t.Fatalf("fresh sink: %d %v", n, ok)
	}
	resp, err := http.Post(s.URL()+"/v1/messages", "application/json", strings.NewReader(`{"secret":"BODY-MARKER-123"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "BODY-MARKER-123") {
		t.Fatal("the sink echoed a request body")
	}
	r2, err := http.Get(s.URL() + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if n, ok := s.Count(); n != 2 || !ok {
		t.Fatalf("after two requests: %d %v", n, ok)
	}
}

func TestSinkBoundsABigBodyAndSurvives(t *testing.T) {
	s, err := NewLoopbackSink("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	big := strings.NewReader(strings.Repeat("x", 4<<20))
	resp, err := http.Post(s.URL(), "application/octet-stream", big)
	if err == nil {
		resp.Body.Close()
	}
	r2, err := http.Get(s.URL())
	if err != nil {
		t.Fatalf("the sink did not survive an oversized body: %v", err)
	}
	r2.Body.Close()
	if n, ok := s.Count(); n < 2 || !ok {
		t.Fatalf("the oversized request was not counted: %d %v", n, ok)
	}
}

func TestSinkCloseStopsAcceptingAndKeepsTheCount(t *testing.T) {
	s, err := NewLoopbackSink("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := s.URL()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get(url); err == nil {
		t.Fatal("the closed sink still accepted a request")
	}
	if n, ok := s.Count(); n != 1 || !ok {
		t.Fatalf("after Close: %d %v", n, ok)
	}
}

func TestSinkLogsNothingAndIsHardened(t *testing.T) {
	s, err := NewLoopbackSink("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.srv.ErrorLog == nil || s.srv.ErrorLog.Writer() != io.Discard {
		t.Fatal("the sink's server error log is not discarded")
	}
	if s.srv.ReadHeaderTimeout <= 0 || s.srv.ReadTimeout <= 0 || s.srv.MaxHeaderBytes <= 0 || s.srv.MaxHeaderBytes > 64<<10 {
		t.Fatalf("server limits: %v %v %d", s.srv.ReadHeaderTimeout, s.srv.ReadTimeout, s.srv.MaxHeaderBytes)
	}
}

// If the listener dies under the sink, "no request" proves nothing: the count is unobserved.
func TestSinkIsUnobservedWhenTheListenerFails(t *testing.T) {
	s, err := NewLoopbackSink("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.ln.Close() // the listener dies; Serve returns an error that is not ErrServerClosed
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := s.Count(); !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a failed sink still reports an observation")
}
