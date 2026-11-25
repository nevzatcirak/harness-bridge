package main

import (
	"errors"
	"time"

	"github.com/nevzatcirak/cirak/internal/security"
)

type ingress struct {
	addr, cert, key string
	remote          *security.Remote
	readOnly        bool
	// maxLifetime is CIRAK_TOKEN_MAX_LIFETIME (ADR-019), default 24h. It is
	// validated in every mode and sets the minimum token length.
	maxLifetime time.Duration
	// warnings are the startup warnings of the ingress settings: one line each, never a value.
	warnings []string
}

// Resolve the complete ingress configuration before any listener is opened.
// A public origin, a principal or a TLS certificate or key opts into remote
// validation; partial TLS never falls back to plaintext, including when a private
// address is requested. CIRAK_TOKEN_EXPIRES alone does NOT (ADR-027 S1, P2-1): in
// local mode it is a rotation deadline that is parsed and recorded but never
// enforced (ADR-019), and ValidateListen below still refuses a non-loopback
// address without remote mode.
func readIngress(env func(string) string) (ingress, error) {
	return readIngressAt(env, time.Now())
}

// readIngressAt is readIngress at a given instant (the injected clock of the local expiry warning).
func readIngressAt(env func(string) string, now time.Time) (ingress, error) {
	c := ingress{addr: env("CIRAK_LISTEN"), cert: env("CIRAK_TLS_CERT"), key: env("CIRAK_TLS_KEY")}
	switch env("CIRAK_READ_ONLY") {
	case "", "0":
	case "1":
		c.readOnly = true
	default:
		return c, errors.New("CIRAK_READ_ONLY must be 0 or 1")
	}
	if c.addr == "" {
		c.addr = "127.0.0.1:8080"
	}
	var err error
	if c.maxLifetime, err = security.ParseTokenMaxLifetime(env("CIRAK_TOKEN_MAX_LIFETIME")); err != nil {
		return c, err
	}
	if env("CIRAK_PUBLIC_ORIGIN") != "" || env("CIRAK_PRINCIPAL") != "" || c.cert != "" || c.key != "" {
		if c.cert == "" || c.key == "" {
			return c, errors.New("remote mode requires TLS certificate and key")
		}
		expires, err := time.Parse(time.RFC3339, env("CIRAK_TOKEN_EXPIRES"))
		if err != nil {
			return c, errors.New("CIRAK_TOKEN_EXPIRES must be RFC3339")
		}
		c.remote, err = security.NewRemoteWithMaxLifetime(env("CIRAK_PUBLIC_ORIGIN"), env("CIRAK_PRINCIPAL"), expires, c.maxLifetime)
		if err != nil {
			return c, err
		}
	}
	if c.remote == nil && env("CIRAK_TOKEN_EXPIRES") != "" {
		// Local mode: the deadline must be well formed, and a passed one is one warning without a value.
		expires, err := time.Parse(time.RFC3339, env("CIRAK_TOKEN_EXPIRES"))
		if err != nil {
			return c, errors.New("CIRAK_TOKEN_EXPIRES must be RFC3339")
		}
		if !expires.After(now) {
			c.warnings = append(c.warnings, "CIRAK_TOKEN_EXPIRES has passed: rotate the token (local mode records the deadline and enforces no expiry)")
		}
	}
	return c, security.ValidateListen(c.addr, c.remote != nil)
}
