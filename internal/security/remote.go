package security

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Remote supports one named operator, never a shared multi-user identity.
// Forwarded headers are ignored: a proxy must use verified TLS to the backend
// and preserve the configured public Host. No proxy can assert an identity.
type Remote struct {
	origin, host, principal string
	expires                 time.Time
	maxLifetime             time.Duration
}

// ADR-019 option (2), accepted 2026-11-06: the connector token's maximum
// lifetime is operator-configurable up to a compiled-in ceiling.
const (
	// DefaultTokenMaxLifetime applies when BRIDGE_TOKEN_MAX_LIFETIME is unset.
	DefaultTokenMaxLifetime = 24 * time.Hour
	// TokenMaxLifetimeCeiling is the hard upper bound for any configuration.
	TokenMaxLifetimeCeiling = 720 * time.Hour
	// MinTokenLength applies at or below DefaultTokenMaxLifetime.
	MinTokenLength = 32
	// MinLongLivedTokenLength (about 256 bits as base64url) applies above it.
	MinLongLivedTokenLength = 43
)

// ParseTokenMaxLifetime reads BRIDGE_TOKEN_MAX_LIFETIME. An empty value is the
// default. Anything that is not a Go duration in (0, ceiling] is rejected, so a
// typo never silently changes the bound.
func ParseTokenMaxLifetime(v string) (time.Duration, error) {
	if v == "" {
		return DefaultTokenMaxLifetime, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, errors.New("BRIDGE_TOKEN_MAX_LIFETIME must be a Go duration such as 168h")
	}
	if err := validMaxLifetime(d); err != nil {
		return 0, err
	}
	return d, nil
}

func validMaxLifetime(d time.Duration) error {
	if d <= 0 || d > TokenMaxLifetimeCeiling {
		return errors.New("BRIDGE_TOKEN_MAX_LIFETIME must be greater than 0 and at most 720h")
	}
	return nil
}

// TokenMinLength is the minimum bearer token length for a maximum lifetime.
func TokenMinLength(maxLifetime time.Duration) int {
	if maxLifetime > DefaultTokenMaxLifetime {
		return MinLongLivedTokenLength
	}
	return MinTokenLength
}

// NewRemote applies the default 24 h maximum token lifetime.
func NewRemote(origin, principal string, expires time.Time) (*Remote, error) {
	return NewRemoteWithMaxLifetime(origin, principal, expires, DefaultTokenMaxLifetime)
}

// NewRemoteWithMaxLifetime rejects an expiry that is not in the future or that
// is more than maxLifetime from now. This is a startup check; Check still
// refuses every request once the expiry has passed.
func NewRemoteWithMaxLifetime(origin, principal string, expires time.Time, maxLifetime time.Duration) (*Remote, error) {
	return newRemoteAt(origin, principal, expires, maxLifetime, time.Now())
}

// newRemoteAt is NewRemoteWithMaxLifetime at a given instant, read once, so
// the boundary is deterministic: an expiry exactly maxLifetime after now is
// accepted, one nanosecond later is not, and an expiry equal to now is not.
func newRemoteAt(origin, principal string, expires time.Time, maxLifetime time.Duration, now time.Time) (*Remote, error) {
	if err := validMaxLifetime(maxLifetime); err != nil {
		return nil, err
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery || strings.ContainsAny(u.Host, "\\\r\n") {
		return nil, errors.New("public origin must be an HTTPS origin without path or credentials")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(principal) || !expires.After(now) || expires.After(now.Add(maxLifetime)) {
		if maxLifetime == DefaultTokenMaxLifetime {
			return nil, errors.New("named principal and future token expiry within 24 hours required")
		}
		return nil, fmt.Errorf("named principal and future token expiry within BRIDGE_TOKEN_MAX_LIFETIME (%s) required", maxLifetime)
	}
	return &Remote{origin: origin, host: u.Host, principal: principal, expires: expires, maxLifetime: maxLifetime}, nil
}

// LogConfig records the configured expiry and maximum lifetime once at
// startup. Remote holds no token, so none can be logged here.
func (p *Remote) LogConfig() {
	slog.Info("remote token lifetime", "principal", p.principal, "token_expires", p.expires.UTC().Format(time.RFC3339), "token_max_lifetime", p.maxLifetime.String())
}

func (p *Remote) Check(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	status := 0
	switch {
	case r.TLS == nil:
		status = http.StatusForbidden
	case r.Host != p.host:
		status = http.StatusForbidden
	case len(r.Header.Values("Origin")) > 1 || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != p.origin):
		status = http.StatusForbidden
	case r.URL.Path != "/mcp" || r.URL.RawQuery != "":
		status = http.StatusBadRequest
	case !time.Now().Before(p.expires):
		w.Header().Set("WWW-Authenticate", `Bearer realm="harness-bridge", error="invalid_token"`)
		status = http.StatusUnauthorized
	}
	// Only configured identity and outcome; never log headers, URL or payload.
	slog.Info("remote boundary", "principal", p.principal, "boundary_allowed", status == 0)
	if status != 0 {
		http.Error(w, "remote request denied", status)
		return false
	}
	return true
}

// AuditAuth records only configured identity and authentication outcome.
func (p *Remote) AuditAuth(accepted bool) {
	slog.Info("remote authentication", "principal", p.principal, "accepted", accepted)
}

// Principal returns the configured operator name. It is an audit label, not a
// credential, and is already logged by the boundary and authentication lines.
func (p *Remote) Principal() string { return p.principal }
