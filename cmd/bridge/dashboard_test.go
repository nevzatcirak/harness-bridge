package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func withLogState(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevW, prevF, prevD := log.Writer(), log.Flags(), slog.Default()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevW); log.SetFlags(prevF); slog.SetDefault(prevD) })
	return &buf
}

// ADR-023 section 1.1: the tee is installed only when the dashboard is enabled.
func TestAuditTeeIsNotInstalledWhenTheDashboardIsOff(t *testing.T) {
	withLogState(t)
	before := slog.Default()
	if srv, err := openDashboard(func(string) string { return "" }); err != nil || srv != nil {
		t.Fatalf("%v %v", srv, err)
	}
	if slog.Default() != before {
		t.Fatal("the process logger was replaced although the dashboard is disabled")
	}
	// A refused configuration installs nothing either.
	if _, err := openDashboard(func(k string) string {
		if k == "BRIDGE_DASHBOARD_ADDR" {
			return "0.0.0.0:8080"
		}
		return ""
	}); err == nil {
		t.Fatal("a wildcard address was accepted")
	}
	if slog.Default() != before {
		t.Fatal("a refused dashboard replaced the process logger")
	}
}

func TestAuditTeeFeedsTheDashboardAndShutdownRestoresTheLogger(t *testing.T) {
	buf := withLogState(t)
	before := slog.Default()
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
	if slog.Default() == before {
		t.Fatal("the tee was not installed")
	}
	// An AUDIT-01 style line reaches the journal branch once and the dashboard's ring.
	slog.Info("explicit interrupt", "event", "interrupt", "tool", "interrupt_execution", "outcome", "accepted", "accepted", true, "execution_id", "0123456789abcdef0123456789abcdef", "principal", "local", "prompt", "SECRET")
	slog.Info("remote boundary", "principal", "p", "boundary_allowed", true)
	// Three lines: the startup log line (the log package, unchanged) and the two slog lines.
	if strings.Count(buf.String(), "\n") != 3 || strings.Count(buf.String(), "event=interrupt") != 1 || !strings.Contains(buf.String(), "boundary_allowed=true") {
		t.Fatalf("journal output: %q", buf.String())
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}, Timeout: 5 * time.Second}
	resp, err := client.Get("http://dashboard/api/audit")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"enabled":true`) || !strings.Contains(string(body), `"value":"interrupt_execution"`) || strings.Contains(string(body), "SECRET") || strings.Contains(string(body), "boundary") {
		t.Fatalf("GET /api/audit = %d %s", resp.StatusCode, body)
	}
	resp, err = client.Get("http://dashboard/audit")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "interrupt_execution") || !strings.Contains(string(page), "1 entries") {
		t.Fatalf("GET /audit = %d %s", resp.StatusCode, page)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if slog.Default() != before {
		t.Fatal("Shutdown did not put the original logger back")
	}
}
