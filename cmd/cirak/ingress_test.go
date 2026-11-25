package main

import (
	"testing"
	"time"
)

func TestIngressConfiguration(t *testing.T) {
	valid := map[string]string{
		"CIRAK_LISTEN":        "172.18.0.1:8443",
		"CIRAK_PUBLIC_ORIGIN": "https://bridge.nevzatcirak.com",
		"CIRAK_PRINCIPAL":     "operator",
		"CIRAK_TOKEN_EXPIRES": time.Now().Add(time.Hour).Format(time.RFC3339),
		"CIRAK_TLS_CERT":      "/not-opened/server.crt", "CIRAK_TLS_KEY": "/not-opened/server.key",
	}
	for _, missing := range []string{"", "CIRAK_TLS_CERT", "CIRAK_TLS_KEY", "CIRAK_PUBLIC_ORIGIN", "CIRAK_PRINCIPAL", "CIRAK_TOKEN_EXPIRES"} {
		t.Run("missing-"+missing, func(t *testing.T) {
			cfg, err := readIngress(func(key string) string {
				if key == missing {
					return ""
				}
				return valid[key]
			})
			if missing == "" {
				if err != nil || cfg.remote == nil || cfg.addr != valid["CIRAK_LISTEN"] {
					t.Fatal(cfg, err)
				}
			} else if err == nil {
				t.Fatal("partial remote configuration accepted", missing)
			}
		})
	}
	for _, addr := range []string{"172.18.0.1:8443", "91.99.201.115:8443", "0.0.0.0:8443"} {
		if _, err := readIngress(func(key string) string {
			if key == "CIRAK_LISTEN" {
				return addr
			}
			return ""
		}); err == nil {
			t.Fatal("nonlocal plaintext accepted", addr)
		}
	}
	for _, addr := range []string{"91.99.201.115:8443", "0.0.0.0:8443", "[::]:8443"} {
		if _, err := readIngress(func(key string) string {
			if key == "CIRAK_LISTEN" {
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

func TestReadOnlyConfiguration(t *testing.T) {
	for _, value := range []string{"", "0", "1", "true", "typo"} {
		cfg, err := readIngress(func(k string) string {
			if k == "CIRAK_READ_ONLY" {
				return value
			}
			return ""
		})
		if value == "true" || value == "typo" {
			if err == nil {
				t.Fatal("invalid read-only setting accepted")
			}
			continue
		}
		if err != nil || cfg.readOnly != (value == "1") {
			t.Fatal("incorrect read-only setting", err)
		}
	}
}
