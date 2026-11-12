package dashboard

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

// Config selects the dashboard transport. The zero value is "disabled".
type Config struct {
	// Socket is the path of a unix socket in a 0700 directory
	// (BRIDGE_DASHBOARD_SOCKET). It is the recommended transport (ADR-023 (a')).
	Socket string
	// Addr is host:port (BRIDGE_DASHBOARD_ADDR); the host must be exactly the
	// IP literal 127.0.0.1 or ::1.
	Addr string
}

// Enabled reports whether any transport is configured. The dashboard is
// disabled by default.
func (c Config) Enabled() bool { return c.Socket != "" || c.Addr != "" }

// ConfigFromEnv reads BRIDGE_DASHBOARD_SOCKET and BRIDGE_DASHBOARD_ADDR. Setting
// both is refused: one dashboard has one transport.
func ConfigFromEnv(env func(string) string) (Config, error) {
	c := Config{Socket: env("BRIDGE_DASHBOARD_SOCKET"), Addr: env("BRIDGE_DASHBOARD_ADDR")}
	if c.Socket != "" && c.Addr != "" {
		return Config{}, errors.New("set only one of BRIDGE_DASHBOARD_SOCKET and BRIDGE_DASHBOARD_ADDR")
	}
	if c.Addr != "" {
		if err := ValidateAddr(c.Addr); err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

// ValidateAddr accepts only host:port where host is exactly 127.0.0.1 or ::1.
// Those are the two literals on the Host allowlist (DefaultHosts), so every
// address that can be bound can also be served; another 127.0.0.0/8 address
// would bind and then answer 400 to every request (review #194 P3-1).
// Wildcards (0.0.0.0, ::), any other interface, an empty host and host names are
// refused, so no name resolution can widen the bind.
func ValidateAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("BRIDGE_DASHBOARD_ADDR must be host:port with a loopback IP: %w", err)
	}
	if host == "" {
		return errors.New("BRIDGE_DASHBOARD_ADDR needs an explicit loopback host (127.0.0.1 or ::1)")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("BRIDGE_DASHBOARD_ADDR host %q must be a loopback IP literal, not a name", host)
	}
	if ip.IsUnspecified() || !ip.IsLoopback() {
		return fmt.Errorf("BRIDGE_DASHBOARD_ADDR host %q is not loopback: the dashboard never binds a non-loopback address", host)
	}
	if !ip.Equal(net.IPv4(127, 0, 0, 1)) && !ip.Equal(net.IPv6loopback) {
		return fmt.Errorf("BRIDGE_DASHBOARD_ADDR host %q must be exactly 127.0.0.1 or ::1 (the Host allowlist names only those)", host)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("BRIDGE_DASHBOARD_ADDR port %q is invalid", port)
	}
	return nil
}
