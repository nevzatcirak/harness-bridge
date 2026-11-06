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
