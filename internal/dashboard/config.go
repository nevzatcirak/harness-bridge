package dashboard

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Config selects the dashboard transport. The zero value is "disabled".
type Config struct {
	// Socket is the path of a unix socket in a 0700 directory
	// (CIRAK_DASHBOARD_SOCKET). It is the recommended transport (ADR-023 (a')).
	Socket string
	// Addr is host:port (CIRAK_DASHBOARD_ADDR); the host must be exactly the
	// IP literal 127.0.0.1 or ::1.
	Addr string
	// Label is the optional instance label of the top bar (CIRAK_DASHBOARD_LABEL, ADR-023 A1.8, Q3): at most 40
	// bytes of [A-Za-z0-9 ._-], default none. It is display text only and never an authority.
	Label string
}

// EnvLabel is the instance-label variable (ADR-026 prefix; ADR-023 A1.8).
const EnvLabel = "CIRAK_DASHBOARD_LABEL"

// maxLabelBytes bounds the instance label.
const maxLabelBytes = 40

// ValidLabel reports whether s is an acceptable instance label: empty (none), or at most 40 bytes of letters, digits,
// space, dot, underscore and hyphen. Anything else (a control character, markup, a path separator) is refused at
// startup like the redaction variable, so a label can never carry more than plain text.
func ValidLabel(s string) bool {
	if len(s) > maxLabelBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ' ' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// Enabled reports whether any transport is configured. The dashboard is
// disabled by default.
func (c Config) Enabled() bool { return c.Socket != "" || c.Addr != "" }

// ConfigFromEnv reads CIRAK_DASHBOARD_SOCKET and CIRAK_DASHBOARD_ADDR. Setting
// both is refused: one dashboard has one transport.
func ConfigFromEnv(env func(string) string) (Config, error) {
	c := Config{Socket: env("CIRAK_DASHBOARD_SOCKET"), Addr: env("CIRAK_DASHBOARD_ADDR"), Label: env(EnvLabel)}
	if !ValidLabel(c.Label) {
		return Config{}, errors.New(EnvLabel + " must be at most 40 bytes of letters, digits, space, dot, underscore and hyphen")
	}
	if c.Socket != "" && c.Addr != "" {
		return Config{}, errors.New("set only one of CIRAK_DASHBOARD_SOCKET and CIRAK_DASHBOARD_ADDR")
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
		return fmt.Errorf("CIRAK_DASHBOARD_ADDR must be host:port with a loopback IP: %w", err)
	}
	if host == "" {
		return errors.New("CIRAK_DASHBOARD_ADDR needs an explicit loopback host (127.0.0.1 or ::1)")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("CIRAK_DASHBOARD_ADDR host %q must be a loopback IP literal, not a name", host)
	}
	if ip.IsUnspecified() || !ip.IsLoopback() {
		return fmt.Errorf("CIRAK_DASHBOARD_ADDR host %q is not loopback: the dashboard never binds a non-loopback address", host)
	}
	// An IPv4-mapped literal (::ffff:127.0.0.1, ::ffff:7f00:1) is the same address as 127.0.0.1 to net.IP.Equal, but
	// the Host allowlist does not name it: it would bind and then answer 400 to every request (review #199 P3-1).
	if strings.Contains(host, ":") && ip.To4() != nil {
		return fmt.Errorf("CIRAK_DASHBOARD_ADDR host %q is an IPv4-mapped literal: use 127.0.0.1 or ::1", host)
	}
	if !ip.Equal(net.IPv4(127, 0, 0, 1)) && !ip.Equal(net.IPv6loopback) {
		return fmt.Errorf("CIRAK_DASHBOARD_ADDR host %q must be exactly 127.0.0.1 or ::1 (the Host allowlist names only those)", host)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("CIRAK_DASHBOARD_ADDR port %q is invalid", port)
	}
	return nil
}
