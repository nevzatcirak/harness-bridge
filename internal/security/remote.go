package security

import (
	"errors"
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
}

func NewRemote(origin, principal string, expires time.Time) (*Remote, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.ForceQuery || strings.ContainsAny(u.Host, "\\\r\n") {
		return nil, errors.New("public origin must be an HTTPS origin without path or credentials")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(principal) || !expires.After(time.Now()) || expires.After(time.Now().Add(24*time.Hour)) {
		return nil, errors.New("named principal and future token expiry within 24 hours required")
	}
	return &Remote{origin: origin, host: u.Host, principal: principal, expires: expires}, nil
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
