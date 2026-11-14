package main

import (
	"log"
	"os"

	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
)

// openDashboard starts the ADR-023 read-only dashboard when
// BRIDGE_DASHBOARD_SOCKET or BRIDGE_DASHBOARD_ADDR is set, and returns nil, nil
// when neither is (the default: no listener, no port, no socket). A bad
// configuration (a non-loopback address, a socket directory that is not 0700,
// both settings at once) is a startup error. No data source is connected yet (NullSource); the audit feed is.
func openDashboard(env func(string) string) (*dashboard.Server, error) {
	cfg, err := dashboard.ConfigFromEnv(env)
	if err != nil {
		return nil, err
	}
	// The audit ring is fed by a tee over the process logger, installed only when the
	// dashboard is enabled (ADR-023 section 1.1): the journal keeps receiving every
	// line unchanged, and the admin server, which logs through slog.Default() at call
	// time, is covered by the same tee.
	ring := dashboard.NewAuditRing()
	srv, err := dashboard.OpenWithOptions(cfg, dashboard.NullSource{}, os.Geteuid(), dashboard.Options{Audit: ring})
	if err != nil {
		return nil, err
	}
	if srv != nil {
		srv.AtShutdown(dashboard.InstallAuditTee(ring))
		log.Printf("dashboard (read-only) listening on %s", srv.Addr())
	}
	return srv, nil
}
