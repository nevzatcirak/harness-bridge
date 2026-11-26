package main

import (
	"strings"
	"testing"
	"time"
)

// ADR-027 S1 (P2-1): the remote-mode trigger is a public origin, a principal or a TLS certificate or key, NOT
// CIRAK_TOKEN_EXPIRES. Local mode records the deadline (ADR-019: no runtime expiry); the boundary of the
// authentication and network exposure does not move.

var t0 = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestTokenExpiresAloneStaysLocal(t *testing.T) {
	cfg, err := readIngressAt(env(map[string]string{"CIRAK_TOKEN_EXPIRES": t0.Add(7 * 24 * time.Hour).Format(time.RFC3339)}), t0)
	if err != nil || cfg.remote != nil || cfg.addr != "127.0.0.1:8080" || len(cfg.warnings) != 0 {
		t.Fatalf("TOKEN_EXPIRES alone must stay local with no warning: %+v %v", cfg, err)
	}
	// ADR-019: the local deadline is NOT bounded by the maximum token lifetime and enforces nothing; a deadline
	// beyond 24h is the D30 7-day rotation deadline and is accepted.
	if _, err := readIngressAt(env(map[string]string{"CIRAK_TOKEN_EXPIRES": t0.Add(30 * 24 * time.Hour).Format(time.RFC3339)}), t0); err != nil {
		t.Fatalf("a 30-day local deadline: %v", err)
	}
}

// TOKEN_EXPIRES alone on a non-loopback address is still a startup refusal: local mode never leaves loopback.
func TestTokenExpiresAloneOnANonLoopbackAddressIsRefused(t *testing.T) {
	for _, addr := range []string{"172.18.0.1:8443", "91.99.201.115:8443", "0.0.0.0:8443", "[::]:8443", ":8443", "bridge.example:8443", "[fe80::1]:8443"} {
		_, err := readIngressAt(env(map[string]string{"CIRAK_LISTEN": addr, "CIRAK_TOKEN_EXPIRES": t0.Add(time.Hour).Format(time.RFC3339)}), t0)
		if err == nil {
			t.Errorf("TOKEN_EXPIRES alone started on %s", addr)
		}
	}
}

// Each of the four remote settings alone still forces remote mode, and therefore its own checks.
func TestEachRemoteSettingAloneStillForcesRemote(t *testing.T) {
	for _, name := range []string{"CIRAK_PUBLIC_ORIGIN", "CIRAK_PRINCIPAL", "CIRAK_TLS_CERT", "CIRAK_TLS_KEY"} {
		val := "operator"
		switch name {
		case "CIRAK_PUBLIC_ORIGIN":
			val = "https://bridge.example"
		case "CIRAK_TLS_CERT", "CIRAK_TLS_KEY":
			val = "/not-opened/file"
		}
		cfg, err := readIngressAt(env(map[string]string{name: val}), t0)
		if err == nil || cfg.remote != nil {
			t.Errorf("%s alone did not force the remote checks: %+v %v", name, cfg, err)
		}
		// and the expiry does not replace the missing pieces
		_, err = readIngressAt(env(map[string]string{name: val, "CIRAK_TOKEN_EXPIRES": t0.Add(time.Hour).Format(time.RFC3339)}), t0)
		if err == nil {
			t.Errorf("%s with a token expiry started without the rest of remote mode", name)
		}
	}
	// the complete set is remote; the remote expiry rule is unchanged
	full := map[string]string{
		"CIRAK_LISTEN": "172.18.0.1:8443", "CIRAK_PUBLIC_ORIGIN": "https://bridge.example", "CIRAK_PRINCIPAL": "operator",
		"CIRAK_TLS_CERT": "/not-opened/c", "CIRAK_TLS_KEY": "/not-opened/k", "CIRAK_TOKEN_EXPIRES": time.Now().Add(time.Hour).Format(time.RFC3339),
	}
	if cfg, err := readIngress(env(full)); err != nil || cfg.remote == nil || len(cfg.warnings) != 0 {
		t.Fatalf("remote mode: %+v %v", cfg, err)
	}
	delete(full, "CIRAK_TOKEN_EXPIRES")
	if _, err := readIngress(env(full)); err == nil {
		t.Fatal("remote mode without an expiry started")
	}
	// a TLS pair alone (no origin/principal) is remote and refused for the rest; a partial pair is refused
	if _, err := readIngressAt(env(map[string]string{"CIRAK_TLS_CERT": "/a", "CIRAK_TLS_KEY": "/b", "CIRAK_TOKEN_EXPIRES": t0.Add(time.Hour).Format(time.RFC3339)}), t0); err == nil {
		t.Fatal("TLS alone is not the whole remote configuration")
	}
}

func TestLocalExpiryIsParsedAndWarnsOnce(t *testing.T) {
	for _, bad := range []string{"tomorrow", "2026-11-26", "2026-11-26 12:00:00", "1764072000", " 2026-11-26T12:00:00Z", "2026-11-26T12:00:00Z "} {
		if _, err := readIngressAt(env(map[string]string{"CIRAK_TOKEN_EXPIRES": bad}), t0); err == nil || !strings.Contains(err.Error(), "RFC3339") {
			t.Errorf("unparseable local expiry %q: %v", bad, err)
		}
	}
	for name, at := range map[string]time.Time{"passed": t0.Add(-time.Hour), "now": t0} {
		cfg, err := readIngressAt(env(map[string]string{"CIRAK_TOKEN_EXPIRES": at.Format(time.RFC3339)}), t0)
		if err != nil {
			t.Fatalf("a %s local deadline is a warning, not a refusal: %v", name, err)
		}
		if len(cfg.warnings) != 1 || !strings.Contains(cfg.warnings[0], "CIRAK_TOKEN_EXPIRES has passed") {
			t.Fatalf("%s: warnings %q", name, cfg.warnings)
		}
		if strings.Contains(cfg.warnings[0], at.Format(time.RFC3339)) || strings.Contains(cfg.warnings[0], "2026") {
			t.Fatalf("the warning contains the value: %q", cfg.warnings[0])
		}
	}
	// one nanosecond in the future is not passed
	if cfg, err := readIngressAt(env(map[string]string{"CIRAK_TOKEN_EXPIRES": t0.Add(time.Second).Format(time.RFC3339)}), t0); err != nil || len(cfg.warnings) != 0 {
		t.Fatalf("%+v %v", cfg, err)
	}
	// the passed-deadline warning comes out of the real start path exactly once (loadStartup does not duplicate it)
	if cfg, err := readIngressAt(env(map[string]string{"CIRAK_TOKEN_EXPIRES": t0.Add(-time.Hour).Format(time.RFC3339), "CIRAK_READ_ONLY": "1"}), t0); err != nil || len(cfg.warnings) != 1 || !cfg.readOnly {
		t.Fatalf("%+v %v", cfg, err)
	}
}
