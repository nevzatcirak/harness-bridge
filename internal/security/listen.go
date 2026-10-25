package security

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
)

// ValidateListen allows legacy loopback binds, or an explicit RFC1918/ULA
// literal in remote TLS mode. No DNS lookup, wildcard, zone or mapped address
// can expand the bind. The OS must also own the exact address when listening.
func ValidateListen(addr string, remoteTLS bool) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("BRIDGE_LISTEN requires an explicit host and port")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return errors.New("BRIDGE_LISTEN requires a numeric port in 1..65535")
	}
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		return nil
	}
	ip, err := netip.ParseAddr(host)
	if !remoteTLS || err != nil || ip.Zone() != "" || ip.Is4In6() || !ip.IsPrivate() {
		return errors.New("BRIDGE_LISTEN must use loopback, or an explicit private IP in remote TLS mode")
	}
	return nil
}
