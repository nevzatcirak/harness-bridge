package main

import (
	"testing"
	"time"
)

func TestIngressConfiguration(t *testing.T) {
	valid := map[string]string{
		"BRIDGE_LISTEN":        "172.18.0.1:8443",
		"BRIDGE_PUBLIC_ORIGIN": "https://bridge.nevzatcirak.com",
		"BRIDGE_PRINCIPAL":     "operator",
		"BRIDGE_TOKEN_EXPIRES": time.Now().Add(time.Hour).Format(time.RFC3339),
		"BRIDGE_TLS_CERT":      "/not-opened/server.crt", "BRIDGE_TLS_KEY": "/not-opened/server.key",
	}
	for _, missing := range []string{"", "BRIDGE_TLS_CERT", "BRIDGE_TLS_KEY", "BRIDGE_PUBLIC_ORIGIN", "BRIDGE_PRINCIPAL", "BRIDGE_TOKEN_EXPIRES"} {
		t.Run("missing-"+missing, func(t *testing.T) {
			cfg, err := readIngress(func(key string) string {
				if key == missing {
					return ""
				}
				return valid[key]
			})
			if missing == "" {
				if err != nil || cfg.remote == nil || cfg.addr != valid["BRIDGE_LISTEN"] {
					t.Fatal(cfg, err)
				}
			} else if err == nil {
				t.Fatal("partial remote configuration accepted", missing)
			}
		})
	}
	for _, addr := range []string{"172.18.0.1:8443", "91.99.201.115:8443", "0.0.0.0:8443"} {
		if _, err := readIngress(func(key string) string {
			if key == "BRIDGE_LISTEN" {
				return addr
			}
			return ""
		}); err == nil {
			t.Fatal("nonlocal plaintext accepted", addr)
		}
	}
	for _, addr := range []string{"91.99.201.115:8443", "0.0.0.0:8443", "[::]:8443"} {
		if _, err := readIngress(func(key string) string {
			if key == "BRIDGE_LISTEN" {
				return addr
			}
			return valid[key]
		}); err == nil {
			t.Fatal("public TLS bind accepted", addr)
		}
	}
	cfg, err := readIngress(func(string) string { return "" })
	if err != nil || cfg.addr != "127.0.0.1:8080" || cfg.remote != nil {
		t.Fatal("local default changed", cfg, err)
	}
}
