package main

import (
	"testing"
	"time"
)

// ADR-019 option (2): BRIDGE_TOKEN_MAX_LIFETIME bounds BRIDGE_TOKEN_EXPIRES.
// These tests use only readIngress, so they run unchanged against the code
// before the change.

func remoteEnv(expires time.Time, maxLifetime string, set bool) func(string) string {
	v := map[string]string{
		"BRIDGE_LISTEN":        "172.18.0.1:8443",
		"BRIDGE_PUBLIC_ORIGIN": "https://bridge.example",
		"BRIDGE_PRINCIPAL":     "operator",
		"BRIDGE_TOKEN_EXPIRES": expires.UTC().Format(time.RFC3339),
		"BRIDGE_TLS_CERT":      "/not-opened/server.crt",
		"BRIDGE_TLS_KEY":       "/not-opened/server.key",
	}
	if set {
		v["BRIDGE_TOKEN_MAX_LIFETIME"] = maxLifetime
	}
	return func(k string) string { return v[k] }
}

func TestTokenMaxLifetimeDefaultUnchanged(t *testing.T) {
	now := time.Now()
	if _, err := readIngress(remoteEnv(now.Add(23*time.Hour), "", false)); err != nil {
		t.Fatalf("unset: 23 h expiry rejected: %v", err)
	}
	for _, expiry := range []time.Duration{24*time.Hour + time.Minute, 25 * time.Hour, 168 * time.Hour} {
		if _, err := readIngress(remoteEnv(now.Add(expiry), "", false)); err == nil {
			t.Fatalf("unset: expiry %s accepted, want the 24 h cap", expiry)
		}
	}
	// The empty value is the same as unset.
	if _, err := readIngress(remoteEnv(now.Add(25*time.Hour), "", true)); err == nil {
		t.Fatal("empty BRIDGE_TOKEN_MAX_LIFETIME: 25 h expiry accepted")
	}
}

func TestTokenMaxLifetimeWeek(t *testing.T) {
	now := time.Now()
	if _, err := readIngress(remoteEnv(now.Add(7*24*time.Hour), "168h", true)); err != nil {
		t.Fatalf("168h: 7-day expiry rejected: %v", err)
	}
	if _, err := readIngress(remoteEnv(now.Add(7*24*time.Hour+time.Minute), "168h", true)); err == nil {
		t.Fatal("168h: 7 days + 1 minute accepted")
	}
}

func TestTokenMaxLifetimeCeiling(t *testing.T) {
	now := time.Now()
	if _, err := readIngress(remoteEnv(now.Add(720*time.Hour), "720h", true)); err != nil {
		t.Fatalf("720h: 30-day expiry rejected: %v", err)
	}
	for _, v := range []string{"721h", "720h1s", "8760h"} {
		// Even a short expiry: the setting itself is out of range.
		if _, err := readIngress(remoteEnv(now.Add(time.Hour), v, true)); err == nil {
			t.Fatalf("%s above the 720h ceiling accepted at startup", v)
		}
	}
}

func TestTokenMaxLifetimeInvalid(t *testing.T) {
	now := time.Now()
	for _, v := range []string{"0", "0s", "-1h", "-24h", "garbage", "1d", "24", " 24h", "24h "} {
		if _, err := readIngress(remoteEnv(now.Add(time.Hour), v, true)); err == nil {
			t.Fatalf("invalid BRIDGE_TOKEN_MAX_LIFETIME %q accepted", v)
		}
		// Fail closed in local mode too: a typo is never silently ignored.
		local := func(k string) string {
			if k == "BRIDGE_TOKEN_MAX_LIFETIME" {
				return v
			}
			return ""
		}
		if _, err := readIngress(local); err == nil {
			t.Fatalf("local mode: invalid BRIDGE_TOKEN_MAX_LIFETIME %q accepted", v)
		}
	}
}
