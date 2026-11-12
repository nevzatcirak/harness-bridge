package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Server is a running dashboard listener.
type Server struct {
	ln      net.Listener
	srv     *http.Server
	closing atomic.Bool
}

// Open starts the dashboard if cfg enables it and returns nil, nil otherwise
// (disabled by default: no listener, no port, no socket). It validates the
// transport before binding anything: a unix socket must sit in a directory that
// uid owns with mode exactly 0700; a TCP address must be a loopback IP literal.
// uid is the service user that must own the socket directory.
func Open(cfg Config, src Source, uid int) (*Server, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	if cfg.Socket != "" && cfg.Addr != "" {
		return nil, fmt.Errorf("set only one of BRIDGE_DASHBOARD_SOCKET and BRIDGE_DASHBOARD_ADDR")
	}
	var (
		ln    net.Listener
		hosts []string
		err   error
	)
	if cfg.Socket != "" {
		if ln, err = listenUnix(cfg.Socket, uid); err != nil {
			return nil, err
		}
	} else {
		if err = ValidateAddr(cfg.Addr); err != nil {
			return nil, err
		}
		if ln, err = net.Listen("tcp", cfg.Addr); err != nil {
			return nil, fmt.Errorf("listen on BRIDGE_DASHBOARD_ADDR: %w", err)
		}
		// Defence in depth: the bound address must still be loopback.
		if ta, ok := ln.Addr().(*net.TCPAddr); !ok || !ta.IP.IsLoopback() || ta.IP.IsUnspecified() {
			_ = ln.Close()
			return nil, fmt.Errorf("BRIDGE_DASHBOARD_ADDR bound a non-loopback address %s", ln.Addr())
		}
		hosts = DefaultHosts
	}
	s := &Server{ln: ln, srv: &http.Server{
		Handler:           NewHandler(src, hosts),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}}
	go func() {
		// A Serve error caused by our own Shutdown (the listener closed under it)
		// is expected and not logged.
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed && !(s.closing.Load() && errors.Is(err, net.ErrClosed)) {
			log.Printf("dashboard stopped: %v", err)
		}
	}()
	return s, nil
}

// Addr returns the listener address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Shutdown stops accepting and waits for in-flight requests until ctx ends. It
// closes the listener itself, first, so the listener is closed when Shutdown
// returns even if it runs before the Serve goroutine has handed the listener to
// the http.Server (review #194 P2-1; http.Server.Shutdown can only close a
// listener it already tracks). A unix socket file is removed by the close. It
// is safe to call more than once.
func (s *Server) Shutdown(ctx context.Context) error {
	s.closing.Store(true)
	if err := s.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	// http.Server.Shutdown closes the listeners it tracks too and reports the
	// error of closing the one we already closed: that is expected, not a failure.
	if err := s.srv.Shutdown(ctx); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}
