package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenDashboardDisabledByDefault(t *testing.T) {
	srv, err := openDashboard(func(string) string { return "" })
	if err != nil || srv != nil {
		t.Fatalf("no dashboard configuration must start nothing: srv=%v err=%v", srv, err)
	}
}

func TestOpenDashboardRefusesBadConfiguration(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"wildcard":  {"BRIDGE_DASHBOARD_ADDR": "0.0.0.0:8080"},
		"ipv6-any":  {"BRIDGE_DASHBOARD_ADDR": "[::]:8080"},
		"non-local": {"BRIDGE_DASHBOARD_ADDR": "192.0.2.1:8080"},
		"name":      {"BRIDGE_DASHBOARD_ADDR": "localhost:8080"},
		"both":      {"BRIDGE_DASHBOARD_ADDR": "127.0.0.1:0", "BRIDGE_DASHBOARD_SOCKET": "/run/x/d.sock"},
	} {
		srv, err := openDashboard(func(k string) string { return env[k] })
		if err == nil || srv != nil {
			t.Errorf("%s: want a startup error, got srv=%v err=%v", name, srv, err)
		}
	}
}

func TestOpenDashboardOnA0700Socket(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "dash.sock")
	srv, err := openDashboard(func(k string) string {
		if k == "BRIDGE_DASHBOARD_SOCKET" {
			return sock
		}
		return ""
	})
	if err != nil || srv == nil {
		t.Fatalf("openDashboard: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
