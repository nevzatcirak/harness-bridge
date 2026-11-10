package main

import (
	"fmt"

	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// loadIngress resolves the ingress configuration and applies the bearer token
// length rule for the configured maximum token lifetime (ADR-019): 32
// characters up to 24h, 43 above it. In remote mode it logs the configured
// expiry and maximum lifetime once. The token itself, or any part or hash of
// it, is never logged.
func loadIngress(env func(string) string, token string) (ingress, error) {
	// A missing or short token is reported before any ingress setting, as before
	// ADR-019 (#102 nit 1); the 43-character rule needs the lifetime and follows.
	if len(token) < security.MinTokenLength {
		return ingress{}, fmt.Errorf("BRIDGE_TOKEN must be at least %d characters", security.MinTokenLength)
	}
	cfg, err := readIngress(env)
	if err != nil {
		return cfg, err
	}
	if minimum := security.TokenMinLength(cfg.maxLifetime); len(token) < minimum {
		return cfg, fmt.Errorf("BRIDGE_TOKEN must be at least %d characters", minimum)
	}
	if cfg.remote != nil {
		cfg.remote.LogConfig()
	}
	return cfg, nil
}

// loadStartup reads BRIDGE_TOKEN and resolves the ingress configuration before
// any other secret is read or a native client is built. Startup errors are
// therefore reported in this order: BRIDGE_TOKEN / BRIDGE_TOKEN_FILE access; a
// token shorter than 32 characters (a missing token included); the ingress
// settings (BRIDGE_READ_ONLY, BRIDGE_TOKEN_MAX_LIFETIME, remote mode); the
// 43-character rule above 24h; only then OPENCODE_SERVER_PASSWORD and the rest.
func loadStartup(env func(string) string) (string, ingress, error) {
	token, err := readSecret(env, "BRIDGE_TOKEN")
	if err != nil {
		return "", ingress{}, err
	}
	cfg, err := loadIngress(env, token)
	if err != nil {
		return "", cfg, err
	}
	return token, cfg, nil
}
