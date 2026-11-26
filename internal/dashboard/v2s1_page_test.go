package dashboard

// ADR-023 Amendment A1, slice S1 (dashboard v2: tokens, shell, sessions table). The tests of this
// file only use the public behaviour of the handler (HTML, headers, routes), so they hold whatever
// the implementation of the helpers is. They are the RED-first part of the slice.

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// v2Now is the fixed clock of the S1 fixtures: the relative times below are computed against it.
var v2Now = time.Date(2026, 11, 21, 17, 48, 5, 0, time.UTC)

func v2Opts() Options { return Options{Now: func() time.Time { return v2Now }} }

func v2Handler(src Source) http.Handler { return NewHandlerOptions(src, nil, v2Opts()) }

// v2Rows is the fixture of the sessions screen (the rows of docs/design/dashboard-v2-mock.html).
func v2Rows() []Session {
	ago := func(d time.Duration) time.Time { return v2Now.Add(-d) }
	return []Session{
		{Harness: "opencode", ID: "ses_demo0001", Title: "Fix flaky retry test", Root: "/srv/demo/work/payments-api/services/billing",
			LifecycleRoot: "/srv/demo/work", State: "running", Guard: Guard{Held: true, State: "running"},
			CurrentExecution: "7a2c91e0", LastActivity: ago(53 * time.Second)},
		{Harness: "opencode", ID: "ses_demo0002", Title: "Migrate config loader", Root: "/srv/demo/work/tools/config-loader",
			LifecycleRoot: "/srv/demo/work", State: "waiting_for_permission", Guard: Guard{Held: true, State: "waiting_for_permission"},
			CurrentExecution: "b81f03d4", LastActivity: ago(6 * time.Minute)},
		{Harness: "claudecode", ID: "claudecode-4e59c1bd", Title: "Docs pass", Root: "/srv/demo/work/site/docs",
			LifecycleRoot: "/srv/demo/work", State: "unknown", LastActivity: ago(time.Hour + 2*time.Minute)},
		{Harness: "opencode", ID: "ses_demo0003", Title: "Spike: cache layer", Root: "/srv/demo/work/spikes/cache",
			State: "idle", LastActivity: ago(30 * time.Hour)},
		{Harness: "opencode", ID: "ses_demo0009", Title: "Old prototype", Root: "/srv/demo/work/prototypes/old",
			LifecycleRoot: "/srv/demo/work", Archived: true, State: "idle", LastActivity: ago(9 * 24 * time.Hour)},
	}
}

// v2Src lists archived sessions only when the request asks for them, as the production Source does (D17 Q7).
type v2Src struct{ *fakeSource }

func (s v2Src) Sessions(ctx context.Context) ([]Session, error) {
	rows, err := s.fakeSource.Sessions(ctx)
	if err != nil || ArchivedShown(ctx) {
		return rows, err
	}
	var out []Session
	for _, r := range rows {
		if !r.Archived {
			out = append(out, r)
		}
	}
	return out, nil
}

func v2Get(t *testing.T, h http.Handler, target string) (int, string, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, rec.Body.String(), rec.Header()
}

// v2Pages renders every page and state S1 serves, with a fixed clock: the list (with and without
// archived), the empty and unavailable states, the not-connected state, the detail page and the audit
// page. Every page of the dashboard goes through the same shell.
func v2Pages(t *testing.T) map[string]string {
	t.Helper()
	exec := time.Date(2026, 11, 21, 17, 40, 0, 0, time.UTC)
	rows := v2Rows()
	full := &fakeSource{rows: rows, details: map[string]SessionDetail{
		"opencode/ses_demo0001": {Session: rows[0], Executions: []Execution{{ID: "7a2c91e0", State: "running", Started: exec, Updated: exec, Current: true}}},
	}}
	out := map[string]string{}
	h := v2Handler(v2Src{full})
	for name, target := range map[string]string{
		"list": "/", "list-archived": "/?archived=1", "detail": "/session/opencode/ses_demo0001", "audit": "/audit",
	} {
		code, body, _ := v2Get(t, h, target)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, code)
		}
		out[name] = body
	}
	for name, body := range s2Pages(t) { // slice S2: the detail page, one per liveness reason, and its states
		out[name] = body
	}
	for name, body := range s3Pages(t) { // slice S3a: executions, catalog, health, the audit chips and pages, the top bar
		out[name] = body
	}
	_, out["empty"], _ = v2Get(t, v2Handler(&fakeSource{rows: []Session{}}), "/")
	code, body, _ := v2Get(t, v2Handler(&fakeSource{rowsErr: context.DeadlineExceeded}), "/")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable list = %d", code)
	}
	out["unavailable"] = body
	_, out["not-connected"], _ = v2Get(t, v2Handler(NullSource{}), "/")
	return out
}

// A1.10 item 3: the CSP string of section 3 on every response, and nothing inline in any page.
func TestV2S1CSPAndNoInlineOnEveryPage(t *testing.T) {
	styleElement := regexp.MustCompile(`(?i)<style`)
	inlineScript := regexp.MustCompile(`(?is)<script(\s[^>]*)?>`)
	srcOnly := regexp.MustCompile(`(?i)^<script src="/dashboard\.js"></script>$`)
	for name, body := range v2Pages(t) {
		// Attributes are parsed, not grepped: a hostile fixture legitimately shows "onerror=" or "javascript:" as
		// escaped TEXT or inside an escaped attribute VALUE, which is not an attribute.
		for _, a := range htmlAttrs(body) {
			switch {
			case a.name == "style":
				t.Errorf("%s: an inline style attribute", name)
			case strings.HasPrefix(a.name, "on"):
				t.Errorf("%s: an on* event-handler attribute: %s", name, a.name)
			case a.name == "href" || a.name == "src" || a.name == "action" || a.name == "formaction":
				v := urlScheme(a.value) // entities decoded, tab/CR/LF removed, as a browser reads it (#585 P3-1)
				if strings.HasPrefix(v, "javascript:") || strings.HasPrefix(v, "data:") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "http:") || strings.HasPrefix(v, "https:") {
					t.Errorf("%s: %s=%q points outside the dashboard or runs script", name, a.name, a.value)
				}
			}
		}
		if styleElement.MatchString(body) {
			t.Errorf("%s: an inline <style> element", name)
		}
		for _, s := range inlineScript.FindAllString(body, -1) {
			if !srcOnly.MatchString(s+"</script>") && !strings.HasPrefix(s, `<script src="/`) {
				t.Errorf("%s: a script tag that is not a same-origin file: %s", name, s)
			}
		}
		if strings.Contains(strings.ToLower(body), "<iframe") {
			t.Errorf("%s: active content", name)
		}
	}
	h := v2Handler(&fakeSource{rows: v2Rows()})
	for _, target := range []string{"/", "/audit", "/dashboard.css", "/dashboard.js", "/dashboard-list.js", "/dashboard-detail.js", "/dashboard-audit.js", "/theme.js", "/api/state", "/nope", "/session/opencode/x"} {
		_, _, hdr := v2Get(t, h, target)
		if got := hdr.Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Errorf("%s: CSP = %q", target, got)
		}
	}
	const section3 = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	if contentSecurityPolicy != section3 {
		t.Errorf("the CSP constant is not the string of ADR-023 section 3: %q", contentSecurityPolicy)
	}
}

// A1.10 item 10 and A1.5: the structure every page has.
func TestV2S1AccessibilityStructureOnEveryPage(t *testing.T) {
	for name, body := range v2Pages(t) {
		for _, want := range []string{
			`<html lang="en"`, `<meta name="viewport" content="width=device-width, initial-scale=1">`, `<meta name="description" content="`,
			`<a class="skip" href="#content">Skip to content</a>`, `<header class="topbar">`, `<nav aria-label="Main">`,
			`<main id="content" tabindex="-1" class="wrap"`, `<span class="brand">Cirak</span>`, `<link rel="stylesheet" href="/dashboard.css">`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: lacks %q", name, want)
			}
		}
		if n := strings.Count(body, "<h1"); n != 1 {
			t.Errorf("%s: %d h1 elements, want 1", name, n)
		}
		if n := strings.Count(body, `aria-current="page"`); n != 1 {
			t.Errorf("%s: %d aria-current links, want exactly 1", name, n)
		}
		if strings.Index(body, `class="skip"`) > strings.Index(body, `<header`) {
			t.Errorf("%s: the skip link is not the first thing in the body", name)
		}
		// Every table has a caption and column headers with a scope.
		tables := strings.Count(body, "<table")
		if c := strings.Count(body, "<caption"); c != tables {
			t.Errorf("%s: %d tables, %d captions", name, tables, c)
		}
		for _, th := range regexp.MustCompile(`<th[ >][^>]*>?`).FindAllString(body, -1) {
			if !strings.Contains(th, `scope="`) {
				t.Errorf("%s: a th without scope: %s", name, th)
			}
		}
		// A heading never skips a level (h1, then h2 at most one deeper, and so on).
		last := 0
		for _, m := range regexp.MustCompile(`<h([1-6])[ >]`).FindAllStringSubmatch(body, -1) {
			lvl := int(m[1][0] - '0')
			if lvl > last+1 {
				t.Errorf("%s: heading level jumps from %d to %d", name, last, lvl)
			}
			last = lvl
		}
		// Every input and select has a label.
		for _, ctl := range regexp.MustCompile(`<(?:input|select)[^>]*\sid="([^"]+)"`).FindAllStringSubmatch(body, -1) {
			if !strings.Contains(body, `for="`+ctl[1]+`"`) {
				t.Errorf("%s: control %s has no label", name, ctl[1])
			}
		}
	}
}

// A1.5: every table wrapper is a keyboard-reachable named region.
func TestV2S1TableWrappersAreNamedRegions(t *testing.T) {
	body := v2Pages(t)["list"]
	wrap := regexp.MustCompile(`<div class="table-wrap"[^>]*>`)
	ms := wrap.FindAllString(body, -1)
	if len(ms) == 0 {
		t.Fatal("no table wrapper on the sessions page")
	}
	for _, m := range ms {
		for _, want := range []string{`tabindex="0"`, `role="region"`, `aria-labelledby="`} {
			if !strings.Contains(m, want) {
				t.Errorf("wrapper %s lacks %s", m, want)
			}
		}
	}
	if n := strings.Count(body, "<table"); n != len(ms) {
		t.Errorf("%d tables, %d wrappers", n, len(ms))
	}
}

// A1.3: the destinations that exist, no dead link; the menu is open without script.
func TestV2S1ShellHasOnlyExistingDestinations(t *testing.T) {
	routes := map[string]bool{}
	for _, r := range Routes(NullSource{}) {
		routes[r.Path] = true
	}
	for name, body := range v2Pages(t) {
		start := strings.Index(body, `<nav aria-label="Main">`)
		if start < 0 {
			t.Errorf("%s: no main navigation", name)
			continue
		}
		nav := body[start:]
		nav = nav[:strings.Index(nav, "</nav>")]
		links := regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(nav, -1)
		if len(links) == 0 {
			t.Errorf("%s: the menu has no destination", name)
		}
		for _, l := range links {
			if !routes[l[1]] {
				t.Errorf("%s: menu link %s has no route", name, l[1])
			}
		}
		if !strings.Contains(body, `<details class="menu" id="menu" open>`) {
			t.Errorf("%s: the menu is not open without script", name)
		}
		// S3a: all five destinations of A1.3 exist, in this order, and nothing else is in the menu
		var got []string
		for _, l := range links {
			got = append(got, l[1])
		}
		if strings.Join(got, " ") != "/ /executions /catalog /audit /health" {
			t.Errorf("%s: the menu is %v", name, got)
		}
	}
	if body := v2Pages(t)["audit"]; !strings.Contains(body, `href="/audit" aria-current="page"`) {
		t.Error("the audit page does not mark its own destination")
	}
	if body := v2Pages(t)["detail"]; !strings.Contains(body, `href="/" aria-current="page"`) {
		t.Error("the session detail does not mark Sessions as the current destination")
	}
}

// A1.4 item 1 and A1.1: the sessions table.
func TestV2S1SessionsTableShape(t *testing.T) {
	body := v2Pages(t)["list"]
	for _, want := range []string{
		`<h1 id="h-main">Sessions</h1>`, `<caption class="visually-hidden">Sessions known to this Bridge</caption>`,
		`<th scope="col">Harness</th>`, `<th scope="col">Session</th>`, `<th scope="col">Repository and path</th>`,
		`<th scope="col">Lifecycle root</th>`, `<th scope="col">State</th>`, `<th scope="col">Guard</th>`,
		`<th scope="col">Current execution</th>`, `<th scope="col">Last activity</th>`,
		// harness badge with the harness name (D17 Q8)
		`<span class="badge">OpenCode</span>`, `<span class="badge">Claude Code</span>`,
		// the label is the word; the raw closed word stays in title
		`<span class="chip chip--info" title="running">Running</span>`,
		`<span class="chip chip--secondary" title="waiting_for_permission">Waiting for permission</span>`,
		`<span class="chip chip--warn" title="unknown">Unknown</span>`,
		`<span class="chip" title="idle">Idle</span>`,
		// path shortening: the last two segments, the full value in title
		`<span title="/srv/demo/work/payments-api/services/billing">…/services/billing</span>`,
		`<span title="/srv/demo/work/tools/config-loader">/srv/demo/work/tools/config-loader</span>`,
		// relative time with the absolute time in title and datetime
		`<time datetime="2026-11-21T17:47:12Z" title="2026-11-21 17:47:12Z">53 s ago</time>`,
		`<time datetime="2026-11-21T17:42:05Z" title="2026-11-21 17:42:05Z">6 min ago</time>`,
		`<time datetime="2026-11-21T16:46:05Z" title="2026-11-21 16:46:05Z">1 h ago</time>`,
		`1 d ago`,
		`Held, running`, `Held, waiting for permission`,
		`<code>ses_demo0001</code>`, `<code>7a2c91e0</code>`,
		`href="/session/opencode/ses_demo0001"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sessions page lacks %q", want)
		}
	}
	// The archived session is not in the default list; the filter link says how to show it.
	if strings.Contains(body, "ses_demo0009") {
		t.Error("an archived session is listed without the filter")
	}
	if !strings.Contains(body, `<a class="btn" href="/?archived=1">Show archived</a>`) {
		t.Error("no link to the archived filter")
	}
	if !strings.Contains(body, `<a class="btn btn--primary" href="/">Refresh</a>`) {
		t.Error("no Refresh link (the no-JS refresh of A1.6 JS-3)")
	}
	arch := v2Pages(t)["list-archived"]
	if !strings.Contains(arch, `<span class="chip chip--outline">Archived</span>`) || !strings.Contains(arch, "ses_demo0009") || !strings.Contains(arch, `href="/session/opencode/ses_demo0009?archived=1"`) {
		t.Error("the archived session is not shown, with its chip and the filter in its link, when asked for")
	}
	if !strings.Contains(arch, `<a class="btn" href="/">Hide archived</a>`) || !strings.Contains(arch, `<a class="btn btn--primary" href="/?archived=1">Refresh</a>`) {
		t.Error("the archived view keeps its filter in the Refresh and toggle links")
	}
	// No raw machine word as a chip label, no capitals for emphasis.
	for _, raw := range []string{">waiting_for_permission<", ">running<", ">idle<", ">unknown<", ">NO_PROGRESS<"} {
		if strings.Contains(body, raw) {
			t.Errorf("a raw state word is shown as visible text: %s", raw)
		}
	}
	if !strings.Contains(body, `5 sessions`) && !strings.Contains(body, `4 sessions`) {
		t.Error("the session count is not shown")
	}
}

// A1.6 JS-4 and A1.10 item 9: the filter controls exist only when script runs; the page has no
// form and no button that could submit anything.
func TestV2S1NoScriptRenderHasNoDeadControl(t *testing.T) {
	for name, body := range v2Pages(t) {
		if strings.Contains(body, "<form") {
			t.Errorf("%s: a form on a read-only page", name)
		}
		// The only buttons are the copy button of the detail page (JS-1) and the theme toggle of every top bar (JS-2): each is
		// hidden until its script runs, so neither is ever a dead control without script (A1.10 item 9).
		for _, b := range regexp.MustCompile(`<button[^>]*>`).FindAllString(body, -1) {
			if (!strings.Contains(b, ` id="copy"`) && !strings.Contains(b, ` id="theme"`)) || !regexp.MustCompile(`\shidden(\s|>)`).MatchString(b) {
				t.Errorf("%s: a button that is not the hidden copy button or the hidden theme toggle: %s", name, b)
			}
		}
	}
	body := v2Pages(t)["list"]
	i := strings.Index(body, `<div class="filters" id="filters" role="search" aria-label="Filter sessions" hidden>`)
	if i < 0 {
		t.Fatal("the filter controls are not in a hidden group (dead controls without script)")
	}
	group := body[i:]
	group = group[:strings.Index(group, "</div>")]
	for _, id := range []string{`id="f-q"`, `id="f-state"`, `id="f-harness"`} {
		if !strings.Contains(group, id) {
			t.Errorf("the hidden filter group lacks %s", id)
		}
	}
	for _, opt := range []string{`value="running"`, `value="waiting_for_permission"`, `value="idle"`, `value="unknown"`, `value="opencode"`, `value="claudecode"`} {
		if !strings.Contains(group, opt) {
			t.Errorf("the filter group lacks the option %s", opt)
		}
	}
	// Outside the group there is no input or select.
	rest := strings.Replace(body, group, "", 1)
	if strings.Contains(rest, "<input") || strings.Contains(rest, "<select") {
		t.Error("a control outside the hidden group")
	}
}

// A1.4 item 7: every data region has a heading, a sentence and the one action that fills it.
func TestV2S1StatesHaveHeadingSentenceAndAction(t *testing.T) {
	pages := v2Pages(t)
	for name, wants := range map[string][]string{
		"empty":         {"No sessions", `href="/?archived=1"`},
		"not-connected": {"No data source is connected"},
		"unavailable":   {"Data source unavailable", `href="/"`},
	} {
		for _, w := range wants {
			if !strings.Contains(pages[name], w) {
				t.Errorf("%s state lacks %q", name, w)
			}
		}
		if !strings.Contains(pages[name], `class="state`) {
			t.Errorf("%s state is not rendered as a state block", name)
		}
	}
	// 503 keeps Retry-After.
	rec := httptest.NewRecorder()
	v2Handler(&fakeSource{rowsErr: context.DeadlineExceeded}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("unavailable list = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// 404 stays a fixed text that never echoes the identifier.
	rec = httptest.NewRecorder()
	v2Handler(&fakeSource{rows: v2Rows()}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/session/opencode/ses_secret_unknown", nil))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "ses_secret_unknown") {
		t.Errorf("404 = %d %q", rec.Code, rec.Body.String())
	}
}

// A1.10 item 11: hostile values in every new field, and the query string.
func TestV2S1HostileValuesAreEscaped(t *testing.T) {
	evil := `<script>alert(1)</script>"><img src=x onerror=alert(2)>`
	rows := []Session{{Harness: "opencode", ID: "ses_x1", Title: evil, Root: "/tmp/" + evil + "/a/b/c/d/e/f/g/h/i/j/k",
		LifecycleRoot: evil, State: "running", Guard: Guard{Held: true, State: evil}, CurrentExecution: evil, LastActivity: v2Now}}
	for _, target := range []string{"/", "/?archived=1", "/?q=" + url.QueryEscape(evil) + "&state=" + url.QueryEscape(evil) + "&harness=" + url.QueryEscape(evil)} {
		_, body, _ := v2Get(t, v2Handler(&fakeSource{rows: rows}), target)
		for _, bad := range []string{"<script>alert", "<img src=x", `onerror=alert(2)>`} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: unescaped %q in the page", target, bad)
			}
		}
		if strings.Contains(target, "?q=") && strings.Contains(body, "alert(1)") && !strings.Contains(body, "&lt;script&gt;alert(1)") {
			t.Errorf("the query string is reflected unescaped")
		}
	}
	// The query string is never reflected by the server at all: filters are client-side state.
	_, body, _ := v2Get(t, v2Handler(&fakeSource{rows: sampleRows()}), "/?q=needle_xyz&state=running&harness=opencode")
	if strings.Contains(body, "needle_xyz") {
		t.Error("the server reflects the q parameter")
	}
}

// A1.0: every route is GET or HEAD, every other method is 405 with the Allow header.
func TestV2S1RouteAndMethodSweep(t *testing.T) {
	h := v2Handler(&fakeSource{rows: v2Rows()})
	paths := map[string]string{"/session/": "/session/opencode/ses_demo0001", "/api/state/session/": "/api/state/session/opencode/ses_demo0001"}
	for _, r := range Routes(&fakeSource{}) {
		for _, m := range r.Methods {
			if m != http.MethodGet && m != http.MethodHead {
				t.Errorf("route %s allows %s", r.Path, m)
			}
		}
		p := r.Path
		if alt, ok := paths[p]; ok {
			p = alt
		}
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(m, p, strings.NewReader("x")))
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s = %d Allow %q", m, p, rec.Code, rec.Header().Get("Allow"))
			}
		}
	}
}

// htmlAttr is one attribute of a start tag: its lower-case name and its value, unescaped as a browser reads it.
type htmlAttr struct{ name, value string }

var (
	startTag = regexp.MustCompile(`<[a-zA-Z][^\s/>]*((?:\s+[^\s"'<>/=]+(?:\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'=<>` + "`" + `]+))?)*)\s*/?>`)
	attrRE   = regexp.MustCompile(`\s+([^\s"'<>/=]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>` + "`" + `]+)))?`)
)

// htmlAttrs lists the attributes of every start tag of a page. Text content never has a literal "<" (html/template
// escapes it), so a "<" followed by a letter always opens a real tag.
func htmlAttrs(body string) []htmlAttr {
	var out []htmlAttr
	for _, tag := range startTag.FindAllStringSubmatch(body, -1) {
		for _, m := range attrRE.FindAllStringSubmatch(tag[1], -1) {
			v := m[2] + m[3] + m[4]
			out = append(out, htmlAttr{name: strings.ToLower(m[1]), value: html.UnescapeString(v)})
		}
	}
	return out
}
