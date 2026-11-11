package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const wantCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

func serve(h http.Handler, method, target, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func checkSecurityHeaders(t *testing.T, what string, h http.Header) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy": wantCSP,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s: header %s = %q, want %q", what, k, got, v)
		}
	}
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Errorf("%s: CORS header %s must never be set", what, k)
		}
	}
}

func TestIndexIsHTMLWithCSP(t *testing.T) {
	h := NewHandler(NullSource{}, nil)
	rec := serve(h, http.MethodGet, "/", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
	checkSecurityHeaders(t, "GET /", rec.Header())
	body := rec.Body.String()
	for _, bad := range []string{"<script>", "onclick=", "onerror=", "javascript:"} {
		if strings.Contains(strings.ToLower(body), bad) {
			t.Errorf("index contains %q: inline script is forbidden", bad)
		}
	}
	if !strings.Contains(body, `src="/dashboard.js"`) || !strings.Contains(body, `href="/dashboard.css"`) {
		t.Errorf("index must reference the two embedded assets: %s", body)
	}
	if !strings.Contains(strings.ToLower(body), "read-only") {
		t.Errorf("index must say read-only")
	}
}

func TestAssetsAreServedWithSecurityHeaders(t *testing.T) {
	h := NewHandler(NullSource{}, nil)
	for path, ct := range map[string]string{"/dashboard.css": "text/css", "/dashboard.js": "text/javascript"} {
		rec := serve(h, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, ct) {
			t.Errorf("%s Content-Type = %q, want %s", path, got, ct)
		}
		checkSecurityHeaders(t, path, rec.Header())
	}
}

// The route table is the single source of truth for routing and may contain
// only GET (and HEAD). This test fails if any other method is ever registered.
func TestRouteTableHasOnlyGetAndHead(t *testing.T) {
	routes := Routes(NullSource{})
	if len(routes) == 0 {
		t.Fatal("empty route table")
	}
	for _, r := range routes {
		if len(r.Methods) == 0 {
			t.Errorf("route %s has no methods", r.Path)
		}
		for _, m := range r.Methods {
			if m != http.MethodGet && m != http.MethodHead {
				t.Errorf("route %s registers method %s: the dashboard is read-only", r.Path, m)
			}
		}
	}
}

// Every mutating method on every registered route, and on an unknown path, is
// 405 with an Allow header and the security headers.
func TestMutatingMethodsAreRefused405(t *testing.T) {
	h := NewHandler(NullSource{}, nil)
	paths := []string{"/unknown", "/api/state", "/session/opencode/ses_x"}
	for _, r := range Routes(NullSource{}) {
		paths = append(paths, r.Path)
	}
	for _, p := range paths {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace, http.MethodConnect, "PROPFIND"} {
			rec := serve(h, m, p, "")
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", m, p, rec.Code)
				continue
			}
			if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("%s %s Allow = %q", m, p, got)
			}
			checkSecurityHeaders(t, m+" "+p, rec.Header())
		}
	}
}

func TestHeadAndUnknownGet(t *testing.T) {
	h := NewHandler(NullSource{}, nil)
	// (net/http drops the body of a HEAD response on a real connection; the
	// recorder does not, so only the status and headers are asserted here.)
	rec := serve(h, http.MethodHead, "/", "")
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD / = %d", rec.Code)
	}
	checkSecurityHeaders(t, "HEAD /", rec.Header())
	rec = serve(h, http.MethodGet, "/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope = %d", rec.Code)
	}
	checkSecurityHeaders(t, "404", rec.Header())
}

// For a TCP listener the Host header is allowlisted (DNS rebinding defence).
func TestHostAllowlist(t *testing.T) {
	h := NewHandler(NullSource{}, DefaultHosts)
	for _, host := range []string{"evil.example", "evil.example:8080", "192.168.0.5", "127.0.0.1.evil.example", "localhost.evil.example", ""} {
		rec := serve(h, http.MethodGet, "/", host)
		if host == "" {
			// httptest sets example.com when empty: still not allowed.
			rec = serve(h, http.MethodGet, "/", "example.com")
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("Host %q = %d, want 400", host, rec.Code)
		}
		checkSecurityHeaders(t, "bad host", rec.Header())
	}
	for _, host := range []string{"localhost", "localhost:8080", "127.0.0.1", "127.0.0.1:9", "[::1]", "[::1]:9", "LOCALHOST:80"} {
		if rec := serve(h, http.MethodGet, "/", host); rec.Code != http.StatusOK {
			t.Errorf("Host %q = %d, want 200", host, rec.Code)
		}
	}
	// A refused host is refused for every method too, before the method check.
	if rec := serve(h, http.MethodPost, "/", "evil.example"); rec.Code != http.StatusBadRequest {
		t.Errorf("POST with a bad Host = %d, want 400", rec.Code)
	}
}

func TestNoHostCheckWithoutAllowlist(t *testing.T) {
	h := NewHandler(NullSource{}, nil) // unix socket: the directory mode is the fence
	if rec := serve(h, http.MethodGet, "/", "whatever.example"); rec.Code != http.StatusOK {
		t.Errorf("unix-socket handler must not apply a Host allowlist, got %d", rec.Code)
	}
}
