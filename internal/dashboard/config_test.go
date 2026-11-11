package dashboard

import (
	"net"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// The dashboard is disabled by default: no configuration means no listener.
func TestDisabledByDefault(t *testing.T) {
	cfg, err := ConfigFromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled() {
		t.Fatalf("config enabled with no environment: %+v", cfg)
	}
	srv, err := Open(cfg, nil, 0)
	if err != nil || srv != nil {
		t.Fatalf("Open with a disabled config must start nothing: srv=%v err=%v", srv, err)
	}
}

func TestSocketAndAddrAreMutuallyExclusive(t *testing.T) {
	_, err := ConfigFromEnv(env(map[string]string{"BRIDGE_DASHBOARD_SOCKET": "/run/x/d.sock", "BRIDGE_DASHBOARD_ADDR": "127.0.0.1:9"}))
	if err == nil {
		t.Fatal("both BRIDGE_DASHBOARD_SOCKET and BRIDGE_DASHBOARD_ADDR must be refused")
	}
}

// Only a loopback IP literal is accepted. Wildcards, other interfaces, empty
// hosts and host names (which would need resolution) are refused at startup.
func TestValidateAddrRefusals(t *testing.T) {
	bad := []string{
		"0.0.0.0:8080", "[::]:8080", ":8080", "0.0.0.0:0", "[::]:0",
		"192.168.1.10:8080", "10.0.0.1:80", "8.8.8.8:80", "[2001:db8::1]:80",
		"localhost:8080", "example.com:80", "127.0.0.1", "127.0.0.1:99999", "127.0.0.1:abc", "",
		"[::ffff:0.0.0.0]:80",
	}
	for _, a := range bad {
		if err := ValidateAddr(a); err == nil {
			t.Errorf("ValidateAddr(%q) = nil, want an error", a)
		}
	}
	for _, a := range []string{"127.0.0.1:8080", "127.0.0.1:0", "[::1]:8080", "[::1]:0"} {
		if err := ValidateAddr(a); err != nil {
			t.Errorf("ValidateAddr(%q) = %v, want nil", a, err)
		}
	}
}

// Only 127.0.0.1 and ::1 are accepted: another 127.0.0.0/8 literal would bind
// and then answer 400 to every browser request, because the Host allowlist
// names only localhost, 127.0.0.1 and [::1] (review #194 P3-1).
func TestOnlyTheHostsOnTheAllowlistCanBeBound(t *testing.T) {
	for _, a := range []string{"127.5.5.5:80", "127.0.0.2:80", "127.255.255.254:80", "[::ffff:127.5.5.5]:80"} {
		if err := ValidateAddr(a); err == nil {
			t.Errorf("ValidateAddr(%q) = nil: it would bind and then fail every Host check", a)
		}
	}
	// Property: every accepted address has a host that the default Host
	// allowlist accepts, so a bind that succeeds is a bind that can be served.
	allowed := map[string]bool{}
	for _, h := range DefaultHosts {
		allowed[h] = true
	}
	for _, a := range []string{"127.0.0.1:1", "[::1]:1", "127.0.0.1:65535", "[::1]:65535"} {
		if err := ValidateAddr(a); err != nil {
			t.Fatalf("ValidateAddr(%q) = %v", a, err)
		}
		host, _, _ := net.SplitHostPort(a)
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if !allowed[normalizeHost(host)] {
			t.Errorf("accepted address %q has host %q which is not on DefaultHosts", a, host)
		}
	}
}

// Open refuses a non-loopback bind before any listener exists.
func TestOpenRefusesWildcardBind(t *testing.T) {
	for _, a := range []string{"0.0.0.0:0", "[::]:0", "192.0.2.1:0"} {
		srv, err := Open(Config{Addr: a}, nil, 0)
		if err == nil || srv != nil {
			t.Fatalf("Open(%q) must refuse: srv=%v err=%v", a, srv, err)
		}
		if !strings.Contains(err.Error(), "BRIDGE_DASHBOARD_ADDR") {
			t.Errorf("error does not name the setting: %v", err)
		}
	}
}
