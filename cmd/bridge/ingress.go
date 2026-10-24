package main

import (
	"errors"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/security"
)

type ingress struct {
	addr, cert, key string
	remote          *security.Remote
}

// Resolve the complete ingress configuration before any listener is opened.
// Any remote setting opts into remote validation; partial TLS never falls back
// to plaintext, including when a private address is requested.
func readIngress(env func(string) string) (ingress, error) {
	c := ingress{addr: env("BRIDGE_LISTEN"), cert: env("BRIDGE_TLS_CERT"), key: env("BRIDGE_TLS_KEY")}
	if c.addr == "" {
		c.addr = "127.0.0.1:8080"
	}
	if env("BRIDGE_PUBLIC_ORIGIN") != "" || env("BRIDGE_PRINCIPAL") != "" || env("BRIDGE_TOKEN_EXPIRES") != "" || c.cert != "" || c.key != "" {
		if c.cert == "" || c.key == "" {
			return c, errors.New("remote mode requires TLS certificate and key")
		}
		expires, err := time.Parse(time.RFC3339, env("BRIDGE_TOKEN_EXPIRES"))
		if err != nil {
			return c, errors.New("BRIDGE_TOKEN_EXPIRES must be RFC3339")
		}
		c.remote, err = security.NewRemote(env("BRIDGE_PUBLIC_ORIGIN"), env("BRIDGE_PRINCIPAL"), expires)
		if err != nil {
			return c, err
		}
	}
	return c, security.ValidateListen(c.addr, c.remote != nil)
}
