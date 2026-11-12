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
// both settings at once) is a startup error. Slice 1 connects no data source.
func openDashboard(env func(string) string) (*dashboard.Server, error) {
	cfg, err := dashboard.ConfigFromEnv(env)
	if err != nil {
		return nil, err
	}
	srv, err := dashboard.Open(cfg, dashboard.NullSource{}, os.Geteuid())
	if err != nil {
		return nil, err
	}
	if srv != nil {
		log.Printf("dashboard (read-only) listening on %s", srv.Addr())
	}
	return srv, nil
}
