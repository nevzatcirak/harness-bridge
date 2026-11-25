package main

import (
	"fmt"
	"time"

	"github.com/nevzatcirak/cirak/internal/security"
)

// loadIngress resolves the ingress configuration and applies the bearer token
// length rule for the configured maximum token lifetime (ADR-019): 32
// characters up to 24h, 43 above it. In remote mode it logs the configured
// expiry and maximum lifetime once. The token itself, or any part or hash of
// it, is never logged.
func loadIngress(env func(string) string, token string) (ingress, error) {
	cfg, err := ingressWithToken(env, token, time.Now())
	if err == nil && cfg.remote != nil {
		cfg.remote.LogConfig()
	}
	return cfg, err
}

// ingressWithToken is loadIngress without its log line and at a given instant: `cirak config check` calls
// it, so the check and the start apply the same token-length rule to the same ingress settings.
func ingressWithToken(env func(string) string, token string, now time.Time) (ingress, error) {
	// A missing or short token is reported before any ingress setting, as before
	// ADR-019 (#102 nit 1); the 43-character rule needs the lifetime and follows.
	if len(token) < security.MinTokenLength {
		return ingress{}, fmt.Errorf("CIRAK_TOKEN must be at least %d characters", security.MinTokenLength)
	}
	cfg, err := readIngressAt(env, now)
	if err != nil {
		return cfg, err
	}
	if minimum := security.TokenMinLength(cfg.maxLifetime); len(token) < minimum {
		return cfg, fmt.Errorf("CIRAK_TOKEN must be at least %d characters", minimum)
	}
	return cfg, nil
}

// loadStartup reads CIRAK_TOKEN and resolves the ingress configuration before
// any other secret is read or a native client is built. Startup errors are
// therefore reported in this order: CIRAK_TOKEN / CIRAK_TOKEN_FILE access; a
// token shorter than 32 characters (a missing token included); the ingress
// settings (CIRAK_READ_ONLY, CIRAK_TOKEN_MAX_LIFETIME, remote mode); the
// 43-character rule above 24h; only then OPENCODE_SERVER_PASSWORD and the rest.
func loadStartup(env func(string) string) (string, ingress, error) {
	token, err := readSecret(env, "CIRAK_TOKEN")
	if err != nil {
		return "", ingress{}, err
	}
	cfg, err := loadIngress(env, token)
	if err != nil {
		return "", cfg, err
	}
	return token, cfg, nil
}
