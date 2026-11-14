package dashboard

import (
	"context"
	"embed"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed assets/dashboard.css assets/dashboard.js
var assets embed.FS

// contentSecurityPolicy is the ADR-023 section 3 policy, verbatim.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// DefaultHosts is the Host allowlist for the TCP transport (ADR-023 T3, T9).
var DefaultHosts = []string{"localhost", "127.0.0.1", "[::1]"}

// requestBound is the per-request context bound of ADR-023 section 4.
const requestBound = 5 * time.Second

// Route is one entry of the route table. The table is the single source of
// truth for routing: a path that is not in it is 404, and a method that is not
// in Methods is 405.
type Route struct {
	Path    string
	Methods []string
	Handler http.Handler
	// Data marks a route that reads the data source: it counts against the limit of
	// concurrent data requests (limits.go). Every route counts against the in-flight limit.
	Data bool
	// Prefix routes every path under Path (which ends in "/") to Handler. The
	// handler validates the remainder itself. An exact route always wins.
	Prefix bool
}

// sessionPrefix is the detail route: GET /session/<harness>/<id>.
const sessionPrefix = "/session/"

var getHead = []string{http.MethodGet, http.MethodHead}

// Routes returns the route table. Slice 2 has the session list, the session
// detail and the two embedded assets; there is no other route and no method but
// GET and HEAD.
func Routes(src Source) []Route { return routes(src, Options{}) }

// Options are the optional parts of the handler. The zero value is the shipped
// behavior: the default redaction list is ON (D17 Q5) and the clock is time.Now.
type Options struct {
	// Redactor replaces the default redactor (the default list plus operator
	// patterns). Nil means the default list.
	Redactor *Redactor
	// Now is the cache clock; tests replace it.
	Now func() time.Time
	// Audit is the read-only audit feed (the ring of the tee handler). Nil means the
	// feed is not enabled: the audit pages say so and show nothing.
	Audit AuditSource
}

func routes(src Source, opts Options) []Route {
	d := newDash(src, opts)
	return []Route{
		{Path: "/", Methods: getHead, Handler: d.indexHandler(), Data: true},
		{Path: sessionPrefix, Methods: getHead, Handler: d.detailHandler(), Prefix: true, Data: true},
		{Path: "/api/state", Methods: getHead, Handler: d.stateListHandler(), Data: true},
		{Path: statePrefix, Methods: getHead, Handler: d.stateDetailHandler(), Prefix: true, Data: true},
		{Path: "/audit", Methods: getHead, Handler: d.auditPageHandler()},
		{Path: "/api/audit", Methods: getHead, Handler: d.auditJSONHandler()},
		{Path: "/dashboard.css", Methods: getHead, Handler: assetHandler("assets/dashboard.css", "text/css; charset=utf-8")},
		{Path: "/dashboard.js", Methods: getHead, Handler: assetHandler("assets/dashboard.js", "text/javascript; charset=utf-8")},
	}
}

func assetHandler(name, contentType string) http.Handler {
	data, err := assets.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("dashboard: embedded asset %s missing: %v", name, err))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	})
}

// NewHandler returns the dashboard handler. allowedHosts is the Host header
// allowlist for the TCP transport; nil or empty applies none (unix socket: the
// directory mode is the fence). Every response, including errors, carries the
// security headers; there are no CORS headers.
func NewHandler(src Source, allowedHosts []string) http.Handler {
	return NewHandlerOptions(src, allowedHosts, Options{})
}

// NewHandlerOptions is NewHandler with Options.
func NewHandlerOptions(src Source, allowedHosts []string, opts Options) http.Handler {
	if src == nil {
		src = NullSource{}
	}
	table := map[string]Route{}
	var prefixes []Route
	for _, r := range routes(src, opts) {
		if r.Prefix {
			prefixes = append(prefixes, r)
			continue
		}
		table[r.Path] = r
	}
	lim := &limiter{}
	hosts := map[string]bool{}
	for _, h := range allowedHosts {
		hosts[strings.ToLower(h)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		if len(hosts) > 0 && !hosts[normalizeHost(r.Host)] {
			http.Error(w, "bad host", http.StatusBadRequest)
			return
		}
		// Read-only by construction: any method but GET/HEAD is refused before
		// routing, whatever the path.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		route, ok := table[r.URL.Path]
		if !ok {
			for _, p := range prefixes {
				if strings.HasPrefix(r.URL.Path, p.Path) {
					route, ok = p, true
					break
				}
			}
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !methodIn(route.Methods, r.Method) {
			h.Set("Allow", strings.Join(route.Methods, ", "))
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Limits (ADR-023 section 4): never a queue. A request over a limit is refused at
		// once; the others run with the 5 s request bound.
		release, ok := lim.acquire(route.Data)
		if !ok {
			refuseBusy(w)
			return
		}
		defer release()
		ctx, cancel := context.WithTimeout(r.Context(), requestBound)
		defer cancel()
		route.Handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

func methodIn(methods []string, m string) bool {
	for _, x := range methods {
		if x == m {
			return true
		}
	}
	return false
}

// normalizeHost lower-cases a Host header and strips the port, keeping the
// brackets of an IPv6 literal so it compares with "[::1]".
func normalizeHost(host string) string {
	host = strings.ToLower(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		if strings.Contains(h, ":") {
			return "[" + h + "]"
		}
		return h
	}
	return host
}
