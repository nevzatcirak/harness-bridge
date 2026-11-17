package acceptance

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// maxSinkBody is the most of a request body the sink reads; the rest is dropped with the connection.
const maxSinkBody = 64 << 10

// LoopbackSink is the ADR-021 §8.3 sink: a hostile root's `env` block sets ANTHROPIC_BASE_URL to a
// local address, and the acceptance requires that the sink received no request. It listens on a
// LOOPBACK address only (an IP literal, 127.0.0.0/8 or ::1; any other bind is refused), counts the
// requests, reads and discards at most 64 KiB of each body, never logs, never echoes a request and
// answers every request with an empty JSON object. It is started and stopped by the driver command.
type LoopbackSink struct {
	srv  *http.Server
	ln   net.Listener
	n    atomic.Int64
	done chan struct{}

	mu     sync.Mutex
	failed bool // the listener died for a reason other than Close: "no request" proves nothing
}

// loopbackAddr checks that addr is "IP-literal:port" with a loopback IP and a numeric port.
func loopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("acceptance: the sink address must be host:port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("acceptance: the sink binds loopback IP literals only (127.0.0.0/8 or ::1)")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 0 || p > 65535 {
		return errors.New("acceptance: the sink port must be a number")
	}
	return nil
}

// NewLoopbackSink listens on addr (for example "127.0.0.1:0", an ephemeral port) and starts serving.
func NewLoopbackSink(addr string) (*LoopbackSink, error) {
	if err := loopbackAddr(addr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("acceptance: the sink cannot listen: %w", err)
	}
	if ta, ok := ln.Addr().(*net.TCPAddr); !ok || !ta.IP.IsLoopback() { // defence in depth
		_ = ln.Close()
		return nil, errors.New("acceptance: the sink listener is not on a loopback address")
	}
	s := &LoopbackSink{ln: ln, done: make(chan struct{})}
	s.srv = &http.Server{
		Handler:           http.HandlerFunc(s.handle),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		defer close(s.done)
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			s.failed = true
			s.mu.Unlock()
		}
	}()
	return s, nil
}

func (s *LoopbackSink) handle(w http.ResponseWriter, r *http.Request) {
	s.n.Add(1) // counted before the body is touched
	_, _ = io.CopyN(io.Discard, r.Body, maxSinkBody)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Connection", "close")
	_, _ = io.WriteString(w, "{}")
}

// URL is the base URL a hostile root would be pointed at.
func (s *LoopbackSink) URL() string { return "http://" + s.ln.Addr().String() }

// Count returns the requests seen and whether that is an observation: false when the listener died
// for any reason other than Close.
func (s *LoopbackSink) Count() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int(s.n.Load()), !s.failed
}

// Close stops the sink; the count stays readable.
func (s *LoopbackSink) Close() error {
	_ = s.srv.Close()
	<-s.done
	return nil
}
