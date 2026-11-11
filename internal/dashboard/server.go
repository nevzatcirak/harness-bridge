package dashboard

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

// Server is a running dashboard listener.
type Server struct {
	ln  net.Listener
	srv *http.Server
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
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("dashboard stopped: %v", err)
		}
	}()
	return s, nil
}

// Addr returns the listener address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Shutdown stops accepting and waits for in-flight requests until ctx ends.
func (s *Server) Shutdown(ctx context.Context) error { return s.srv.Shutdown(ctx) }
