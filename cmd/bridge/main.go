package main

import (
	"context"
	"crypto/tls"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/admin"
	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
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
		owner, err := acquireOpenCodeOwnership(os.Getenv("BRIDGE_WRITER_LOCK_DIR"), allSessions)
		if err != nil {
			log.Fatal(err)
		}
		if owner != nil {
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
	// ADR-022/ADR-025 lifecycle surface (D22): enabled by default, inert until BRIDGE_LIFECYCLE_CONFIG
	// (and, for archive, BRIDGE_OVERLAY_DIR) is set; an invalid value refuses to start.
	lc, err := openLifecycle(os.Getenv, os.Geteuid(), cfg.readOnly)
	if err != nil {
		log.Fatal(err)
	}
	defer lc.Close()
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
	// The one server builder (#291): a nil Claude adapter never reaches the interface.
	mcpServer := newMCPServer(client, runs, token, policy, cfg.remote, cfg.readOnly, claude)
	lc.apply(mcpServer, runs)
	log.Print(lc.summary(cfg.readOnly))
	// ADR-024 S5 (D15 Q3): reply_permission is registered in reject_only by default;
	// BRIDGE_PERMISSION_RELAY=off keeps the eleven-tool surface.
	relay, err := permissionRelayMode(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	incMode, err := permissionIncarnationMode(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	inc, stopIncarnation, err := startIncarnation(relay, incMode, client)
	if err != nil {
		log.Fatal(err)
	}
	defer stopIncarnation()
	if err := wirePermissionRelayWith(mcpServer, runs, client, relay, inc); err != nil {
		log.Fatal(err)
	}
	switch {
	case relay == "reject_only" && inc != nil:
		log.Print("permission relay: reject_only; incarnation=observed (ADR-024 A1 S7): requests are attributed only for runs admitted under the current Bridge-observed continuity epoch, which proves continuity of observation, not native identity")
	case relay == "reject_only":
		log.Print("permission relay: reject_only; no native incarnation source (BRIDGE_PERMISSION_INCARNATION is off), so permission requests stay unattributed and reply_permission refuses with not_verified (docs/CONTRACTS.md, ADR-024 S6)")
	case incMode == "observed":
		log.Print("BRIDGE_PERMISSION_INCARNATION=observed has no effect: BRIDGE_PERMISSION_RELAY=off starts no tracker")
	}
	dash, err := openDashboard(os.Getenv, func() dashboard.Source { return mcpServer.DashboardSource(nil) })
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpServer)
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
