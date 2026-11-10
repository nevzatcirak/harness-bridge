package main

import (
	"strings"
	"sync"
	"testing"
)

// #102 nit 1: a missing or short token is reported before any ingress setting,
// including an invalid BRIDGE_TOKEN_MAX_LIFETIME, as it was before ADR-019.
func TestStartupTokenErrorBeforeIngress(t *testing.T) {
	env := func(k string) string {
		switch k {
		case "BRIDGE_TOKEN_MAX_LIFETIME":
			return "garbage"
		case "BRIDGE_READ_ONLY":
			return "2"
		}
		return ""
	}
	for _, token := range []string{"", "short", token32[:31]} {
		_, err := loadIngress(env, token)
		if err == nil || err.Error() != "BRIDGE_TOKEN must be at least 32 characters" {
			t.Fatalf("token %d chars: got %v, want the token error first", len(token), err)
		}
	}
	// With a valid-length token the ingress errors appear, in their own order.
	if _, err := loadIngress(env, token32); err == nil || err.Error() != "BRIDGE_READ_ONLY must be 0 or 1" {
		t.Fatalf("ingress error: %v", err)
	}
	lifetimeOnly := func(k string) string {
		if k == "BRIDGE_TOKEN_MAX_LIFETIME" {
			return "garbage"
		}
		return ""
	}
	if _, err := loadIngress(lifetimeOnly, token32); err == nil || !strings.Contains(err.Error(), "BRIDGE_TOKEN_MAX_LIFETIME") {
		t.Fatalf("lifetime error: %v", err)
	}
	// Above 24h a 32..42 character token gets the 43-character error after the lifetime is known.
	long := func(k string) string {
		if k == "BRIDGE_TOKEN_MAX_LIFETIME" {
			return "168h"
		}
		return ""
	}
	if _, err := loadIngress(long, token32); err == nil || err.Error() != "BRIDGE_TOKEN must be at least 43 characters" {
		t.Fatalf("long-lived rule: %v", err)
	}
}

// loadStartup checks the token and the ingress before the native password is
// read, so a short token with a missing password reports the token.
func TestLoadStartupOrder(t *testing.T) {
	var mu sync.Mutex
	read := map[string]bool{}
	env := func(values map[string]string) func(string) string {
		return func(k string) string {
			mu.Lock()
			read[k] = true
			mu.Unlock()
			return values[k]
		}
	}
	_, _, err := loadStartup(env(map[string]string{"BRIDGE_TOKEN_MAX_LIFETIME": "garbage"}))
	if err == nil || err.Error() != "BRIDGE_TOKEN must be at least 32 characters" {
		t.Fatalf("missing token: %v", err)
	}
	_, _, err = loadStartup(env(map[string]string{"BRIDGE_TOKEN": token32, "BRIDGE_TOKEN_MAX_LIFETIME": "garbage"}))
	if err == nil || !strings.Contains(err.Error(), "BRIDGE_TOKEN_MAX_LIFETIME") {
		t.Fatalf("lifetime: %v", err)
	}
	_, _, err = loadStartup(env(map[string]string{"BRIDGE_TOKEN": "a", "BRIDGE_TOKEN_FILE": "/x"}))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("token source: %v", err)
	}
	token, cfg, err := loadStartup(env(map[string]string{"BRIDGE_TOKEN": token32}))
	if err != nil || token != token32 || cfg.addr == "" {
		t.Fatalf("valid: %q %+v %v", token, cfg, err)
	}
	for k := range read {
		if strings.HasPrefix(k, "OPENCODE_") {
			t.Fatalf("loadStartup read %s before the token and ingress checks finished", k)
		}
	}
}
