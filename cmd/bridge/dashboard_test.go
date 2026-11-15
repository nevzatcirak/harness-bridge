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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
)

func TestOpenDashboardDisabledByDefault(t *testing.T) {
	srv, err := openDashboard(func(string) string { return "" }, nil)
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
		srv, err := openDashboard(func(k string) string { return env[k] }, nil)
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
	}, nil)
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
	if srv, err := openDashboard(func(string) string { return "" }, nil); err != nil || srv != nil {
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
	}, nil); err == nil {
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
	}, nil)
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

// A disabled dashboard builds no source, starts no goroutine and makes no native request.
func TestDisabledDashboardNeverBuildsTheSource(t *testing.T) {
	built := 0
	before := runtime.NumGoroutine()
	srv, err := openDashboard(func(string) string { return "" }, func() dashboard.Source { built++; return dashboard.NullSource{} })
	if err != nil || srv != nil || built != 0 {
		t.Fatalf("srv %v err %v built %d", srv, err, built)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines %d -> %d", before, after)
	}
}

// An enabled dashboard builds the source exactly once.
func TestEnabledDashboardBuildsTheSourceOnce(t *testing.T) {
	built := 0
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv, err := openDashboard(func(k string) string {
		if k == "BRIDGE_DASHBOARD_SOCKET" {
			return filepath.Join(dir, "d.sock")
		}
		return ""
	}, func() dashboard.Source { built++; return dashboard.NullSource{} })
	if err != nil || srv == nil {
		t.Fatalf("%v %v", srv, err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	if built != 1 {
		t.Fatalf("built %d", built)
	}
}

// An invalid operator redaction pattern is a startup error that opens no listener and
// does not log the pattern.
func TestInvalidRedactionPatternFailsClosedWithoutLogging(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "d.sock")
	srv, err := openDashboard(func(k string) string {
		switch k {
		case "BRIDGE_DASHBOARD_SOCKET":
			return sock
		case dashboard.EnvRedactExtra:
			return `["SECRETPATTERN("]`
		}
		return ""
	}, nil)
	if err == nil || srv != nil {
		t.Fatalf("accepted: %v %v", srv, err)
	}
	if _, statErr := os.Stat(sock); statErr == nil {
		t.Fatal("a socket was created for an invalid configuration")
	}
	if strings.Contains(buf.String(), "SECRETPATTERN") || strings.Contains(err.Error(), "SECRETPATTERN") {
		t.Fatal("the pattern text was logged or echoed")
	}
}

// #272 P3-1: the operator redaction patterns also apply to the audit ring, so /audit and
// /api/audit never show a value the operator asked to redact, while the journal line is
// byte-identical (redaction is display-only).
func TestOperatorRedactionReachesTheAuditRingButNotTheJournal(t *testing.T) {
	buf := withLogState(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "dash.sock")
	srv, err := openDashboard(func(k string) string {
		switch k {
		case "BRIDGE_DASHBOARD_SOCKET":
			return sock
		case dashboard.EnvRedactExtra:
			return `["corp-[0-9a-f]{8}"]`
		}
		return ""
	}, nil)
	if err != nil || srv == nil {
		t.Fatalf("openDashboard: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	slog.Info("explicit interrupt", "event", "interrupt", "tool", "interrupt_execution corp-0123abcd", "outcome", "accepted", "principal", "local", "execution_id", "0123456789abcdef0123456789abcdef")
	if !strings.Contains(buf.String(), "corp-0123abcd") {
		t.Fatalf("the journal must keep the value unchanged: %q", buf.String())
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}, Timeout: 5 * time.Second}
	for _, path := range []string{"/api/audit", "/audit"} {
		resp, err := client.Get("http://dashboard" + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), "interrupt_execution") {
			t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
		}
		if strings.Contains(string(body), "corp-0123abcd") || !strings.Contains(string(body), "[redacted]") {
			t.Errorf("GET %s shows the operator-redacted value: %s", path, body)
		}
	}
}
