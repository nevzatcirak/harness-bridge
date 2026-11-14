package main

import (
	"context"
	"crypto/tls"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/admin"
	"github.com/nevzatcirak/harness-bridge/internal/claudewire"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/security"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		os.Exit(runAdmin(os.Args[2:], os.Getenv, os.Stdout, os.Stderr))
	}
	token, cfg, err := loadStartup(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	base := os.Getenv("OPENCODE_URL")
	if base == "" {
		base = "http://127.0.0.1:4096"
	}
	password, err := readSecret(os.Getenv, "OPENCODE_SERVER_PASSWORD")
	if err != nil {
		log.Fatal(err)
	}
	client, err := opencode.New(base, os.Getenv("OPENCODE_SERVER_USERNAME"), password)
	if err != nil {
		log.Fatal(err)
	}
	policy, err := security.New(strings.Split(os.Getenv("BRIDGE_ALLOWED_ROOTS"), string(os.PathListSeparator)), strings.Split(os.Getenv("BRIDGE_ALLOWED_SESSIONS"), ","))
	if err != nil {
		log.Fatal(err)
	}
	// Read-only ingress cannot mutate and does not acquire writer ownership.
	// Every cooperating writer on this host must share this protected directory.
	allSessions := make([]string, 0, len(policy.Sessions))
	for id := range policy.Sessions {
		allSessions = append(allSessions, id)
	}
	if !cfg.readOnly {
		// One owner per session (#236 P3-4): the claudecode: IDs belong to the Claude Code
		// adapter's own locks (same directory, same hashed names); taking them here too would
		// make the second flock of one name in this process fail.
		_, opencodeSessions := claudewire.ClaudeSessionIDs(allSessions)
		if len(opencodeSessions) > 0 {
			owner, err := execution.AcquireOwnership(os.Getenv("BRIDGE_WRITER_LOCK_DIR"), opencodeSessions)
			if err != nil {
				log.Fatal(err)
			}
			defer owner.Close()
		}
	}
	claude, err := openClaude(os.Getenv, splitList(os.Getenv("BRIDGE_ALLOWED_ROOTS"), string(os.PathListSeparator)), allSessions, cfg.readOnly)
	if err != nil {
		log.Fatal(err)
	}
	if claude != nil {
		defer claude.Close()
	}
	runs := execution.New(client)
	defer runs.Close()
	var claudeRelease admin.ClaudeReleaser // stays a nil interface unless the adapter runs
	if claude != nil {
		claudeRelease = claude
	}
	adminServer, err := openAdmin(os.Getenv, cfg.readOnly, runs, claudeRelease)
	if err != nil {
		log.Fatal(err)
	}
	if adminServer != nil {
		defer adminServer.Close()
	}
	// ADR-023 read-only dashboard: disabled unless BRIDGE_DASHBOARD_SOCKET or
	// BRIDGE_DASHBOARD_ADDR is set; local transports only.
	dash, err := openDashboard(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	bridge := &mcp.Server{Client: client, Runs: runs, Token: token, Policy: policy, Remote: cfg.remote, ReadOnly: cfg.readOnly}
	if claude != nil {
		bridge.Claude = claude // only when enabled: a nil *Harness must never reach the interface
	}
	mux.Handle("/mcp", bridge)
	srv := &http.Server{Addr: cfg.addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
		if dash != nil {
			_ = dash.Shutdown(c)
		}
	}()
	log.Printf("Bridge listening on %s", cfg.addr)
	var serveErr error
	if cfg.remote != nil {
		serveErr = srv.ListenAndServeTLS(cfg.cert, cfg.key)
	} else {
		serveErr = srv.ListenAndServe()
	}
	if err := serveErr; err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
