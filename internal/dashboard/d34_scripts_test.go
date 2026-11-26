package dashboard

// D34 (2026-11-26): dashboard.js is split into a core that every page loads and one script per page kind. These
// tests pin the split: the files, their sizes, what each page loads and why, the CSP and the absence of inline
// script, the page scripts' dependence on the core, and (with node) that a page script acts only on its own page.

import (
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// scriptTagRe matches one script element with its body, or a start tag that is never closed.
var scriptTagRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<script\b[^>]*>`)

// embeddedScriptRe is the one form of script element a page may carry: a same-origin file of the dashboard.
var embeddedScriptRe = regexp.MustCompile(`^<script src="(/(?:dashboard[a-z-]*|theme)\.js)"></script>$`)

func knownScript(src string) bool {
	for _, n := range allScripts {
		if src == "/"+n {
			return true
		}
	}
	return false
}

// scriptSrcs returns the sources of the embedded script elements of a page, in document order.
func scriptSrcs(body string) []string {
	var out []string
	for _, tag := range scriptTagRe.FindAllString(body, -1) {
		if m := embeddedScriptRe.FindStringSubmatch(tag); m != nil && knownScript(m[1]) {
			out = append(out, m[1])
		}
	}
	return out
}

// foreignScripts returns every script element that is not an embedded dashboard file: an inline script, another
// source, an attribute besides src, or a start tag with no end. A raw "<script" that the pattern misses counts too.
func foreignScripts(body string) []string {
	var out []string
	tags := scriptTagRe.FindAllString(body, -1)
	for _, tag := range tags {
		if m := embeddedScriptRe.FindStringSubmatch(tag); m == nil || !knownScript(m[1]) {
			out = append(out, tag)
		}
	}
	if n := strings.Count(strings.ToLower(body), "<script"); n != len(tags) {
		out = append(out, "a <script that is not an element")
	}
	return out
}

// The assets are exactly the stylesheet and the scripts of the table: no stray file, and no script the server does
// not serve (theme.js is added by S3b with its own 1 KiB cap).
func TestD34AssetsAreTheStylesheetAndTheScriptTable(t *testing.T) {
	entries, err := os.ReadDir("assets")
	if err != nil {
		t.Fatal(err)
	}
	var have []string
	for _, e := range entries {
		have = append(have, e.Name())
	}
	want := append([]string{"dashboard.css"}, allScripts...)
	sort.Strings(have)
	sort.Strings(want)
	if strings.Join(have, " ") != strings.Join(want, " ") {
		t.Errorf("assets = %v, want %v", have, want)
	}
	if len(scriptFiles) != 4 || scriptFiles[0] != "dashboard.js" {
		t.Errorf("the script table is %v: the core first, then the three page scripts", scriptFiles)
	}
	h := NewHandlerOptions(NullSource{}, nil, Options{Audit: NewAuditRing()})
	for _, name := range allScripts {
		raw, err := os.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		rec := serve(h, http.MethodGet, "/"+name, "")
		if rec.Code != http.StatusOK || rec.Body.String() != string(raw) || rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
			t.Errorf("GET /%s = %d, type %q, %d bytes (file has %d)", name, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len(), len(raw))
		}
		checkSecurityHeaders(t, "GET /"+name, rec.Header())
		if head := serve(h, http.MethodHead, "/"+name, ""); head.Code != http.StatusOK {
			t.Errorf("HEAD /%s = %d", name, head.Code)
		}
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			if rec := serve(h, m, "/"+name, ""); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s /%s = %d allow=%q", m, name, rec.Code, rec.Header().Get("Allow"))
			}
		}
	}
}

// Every page references scripts that exist, each once, the core first, and exactly the scripts its markup needs:
// the list script for the polled sessions list, the detail script for the polled session, the audit script for an
// audit page that has a filter and rows. The needs are read from the HTML hooks, not from the server's own table.
func TestD34EachPageLoadsOnlyTheScriptsItNeeds(t *testing.T) {
	seen := map[string]int{}
	for name, body := range goldenPages(t) {
		want := []string{"/" + scriptTheme, "/" + scriptCore} // theme.js is blocking in the head, the scripts of the body follow
		switch {
		case strings.Contains(body, `data-poll="list"`):
			want = append(want, "/"+scriptList)
			seen["list"]++
		case strings.Contains(body, `data-poll="detail"`):
			want = append(want, "/"+scriptDetail)
			seen["detail"]++
		case strings.Contains(body, `id="audit-filter"`) && strings.Contains(body, `id="audit-rows"`):
			want = append(want, "/"+scriptAudit)
			seen["audit"]++
		default:
			seen["core only"]++
		}
		got := scriptSrcs(body)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s loads %v, want %v", name, got, want)
		}
		if bad := foreignScripts(body); len(bad) > 0 {
			t.Errorf("%s has script elements that are not the embedded files: %q", name, bad)
		}
		dup := map[string]bool{}
		for _, s := range got {
			if dup[s] || !knownScript(s) {
				t.Errorf("%s: script %s twice or unknown", name, s)
			}
			dup[s] = true
			if _, err := os.Stat("assets/" + strings.TrimPrefix(s, "/")); err != nil {
				t.Errorf("%s references %s, which does not exist: %v", name, s, err)
			}
		}
	}
	for _, kind := range []string{"list", "detail", "audit", "core only"} {
		if seen[kind] == 0 {
			t.Errorf("no page of the kind %q was checked", kind)
		}
	}
	// The pages that need the core alone: no poll, no filter.
	for _, name := range []string{"executions", "catalog", "health-remote"} {
		if got := scriptSrcs(s3Pages(t)[name]); strings.Join(got, " ") != "/theme.js /dashboard.js" {
			t.Errorf("%s loads %v, want the core alone", name, got)
		}
	}
	// An audit page without rows has no filter to drive, so no audit script.
	o := v2Opts()
	o.Audit = fakeAuditOf()
	_, body, _ := v2Get(t, NewHandlerOptions(&fakeSource{}, nil, o), "/audit")
	if got := scriptSrcs(body); strings.Join(got, " ") != "/theme.js /dashboard.js" {
		t.Errorf("an empty audit page loads %v, want the core alone", got)
	}
}

// The CSP is the string of ADR-023 section 3, on the pages and on every script; script-src is 'self' only.
func TestD34CSPOnPagesAndScripts(t *testing.T) {
	if !strings.Contains(contentSecurityPolicy, "script-src 'self';") {
		t.Errorf("script-src is not 'self': %q", contentSecurityPolicy)
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "http:", "https:", "data:", "blob:", "*", "nonce-", "strict-dynamic"} {
		if strings.Contains(contentSecurityPolicy, bad) {
			t.Errorf("the CSP contains %q", bad)
		}
	}
	h := NewHandlerOptions(&fakeSource{rows: v2Rows()}, nil, Options{Audit: NewAuditRing()})
	targets := []string{"/", "/audit", "/executions", "/catalog", "/health", "/session/opencode/ses_demo0001", "/nope"}
	for _, n := range allScripts {
		targets = append(targets, "/"+n)
	}
	for _, target := range targets {
		rec := serve(h, http.MethodGet, target, "")
		if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
			t.Errorf("GET %s: CSP = %q", target, got)
		}
	}
}

// No page has an inline script: every script element is an empty <script src="/dashboard*.js"> of the table, and
// no tag carries an event-handler attribute or a javascript: URL.
func TestD34NoInlineScriptOnAnyPage(t *testing.T) {
	for name, body := range goldenPages(t) {
		for _, tag := range scriptTagRe.FindAllString(body, -1) {
			if !embeddedScriptRe.MatchString(tag) {
				t.Errorf("%s: a script element that is not an embedded file: %q", name, tag)
			}
		}
		for _, a := range htmlAttrs(body) {
			if strings.HasPrefix(a.name, "on") || runsScript(a.value) {
				t.Errorf("%s: attribute %s=%q", name, a.name, a.value)
			}
		}
	}
	// The detector itself: the forms it must refuse.
	for _, bad := range []string{
		`<script>alert(1)</script>`, `<script src="/dashboard.js">x</script>`, `<script src="https://x/y.js"></script>`,
		`<script src="/dashboard.js" async></script>`, `<script src="/other.js"></script>`, `<SCRIPT SRC=/dashboard.js></SCRIPT>`, `<script src="/dashboard.js">`,
	} {
		if len(foreignScripts(bad)) == 0 {
			t.Errorf("the scan accepts %q", bad)
		}
	}
	if got := foreignScripts(`<script src="/dashboard.js"></script>
<script src="/dashboard-list.js"></script>`); len(got) != 0 {
		t.Errorf("the scan refuses the embedded files: %q", got)
	}
}

// The page scripts use only what the core exports on window.cirakDash, and the core exports nothing a page script
// does not need (no dead surface).
func TestD34PageScriptsUseOnlyTheCoreExports(t *testing.T) {
	core, err := os.ReadFile("assets/" + scriptCore)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)window\.cirakDash = Object\.freeze\(\{(.*?)\n  \}\);`).FindStringSubmatch(string(core))
	if m == nil {
		t.Fatal("the core does not export window.cirakDash")
	}
	exported := map[string]bool{}
	for _, k := range regexp.MustCompile(`(\w+):`).FindAllStringSubmatch(m[1], -1) {
		exported[k[1]] = true
	}
	used := map[string]bool{}
	for _, name := range scriptFiles[1:] {
		raw, err := os.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range regexp.MustCompile(`\bD\.(\w+)`).FindAllStringSubmatch(string(raw), -1) {
			used[u[1]] = true
			if !exported[u[1]] {
				t.Errorf("%s uses D.%s, which the core does not export", name, u[1])
			}
		}
		if !strings.Contains(string(raw), "var D = window.cirakDash;\n  if (!D) { return; }") {
			t.Errorf("%s does not stop quietly when the core is missing", name)
		}
	}
	for k := range exported {
		if !used[k] {
			t.Errorf("the core exports %q, which no page script uses", k)
		}
	}
	// No script defines a global other than the one namespace.
	for _, name := range allScripts {
		raw, _ := os.ReadFile("assets/" + name)
		for _, g := range regexp.MustCompile(`window\.(\w+)\s*=[^=]`).FindAllStringSubmatch(string(raw), -1) {
			if g[1] != "cirakDash" {
				t.Errorf("%s assigns window.%s", name, g[1])
			}
		}
	}
}

// ---- behaviour, with node --------------------------------------------------------------------------------------

func auditDOM(rows ...map[string]any) map[string]any {
	return withID(node("main", nil,
		hiddenEl(withID(node("div", nil, withID(node("input", nil), "audit-filter")), "filters")),
		withID(node("tbody", nil, rows...), "audit-rows"),
	), "content")
}

func auditTR(text string) map[string]any { return withText(node("tr", nil), text) }

func (r jsResult) fetchCount() int { return len(r.Log.Fetches) }

// The audit filter is its own script: it filters the rows, keeps the state in the URL and makes no request.
func TestD34AuditScriptFiltersRowsAndKeepsTheStateInTheURL(t *testing.T) {
	rows := []map[string]any{auditTR("interrupt accepted ses_a"), auditTR("guard_release refused ses_b"), auditTR("prompt_submission new ses_c")}
	res := runJS(t, map[string]any{"dom": auditDOM(rows...), "pathname": "/audit", "search": "?q=GUARD&page=2",
		"steps": []any{
			map[string]any{"read": []string{"audit-filter", "filters", "audit-rows"}},
			map[string]any{"input": map[string]string{"id": "audit-filter", "value": ""}, "read": []string{"audit-rows"}},
			map[string]any{"advance": 20000},
		}})
	if e := res.el(t, 0, "audit-filter"); e.Value != "GUARD" {
		t.Errorf("q was not restored from the URL: %+v", e)
	}
	if e := res.el(t, 0, "filters"); e.Hidden {
		t.Error("the filter group is still hidden")
	}
	if len(res.Log.ReplaceState) == 0 || res.Log.ReplaceState[0] != "/audit?q=GUARD" {
		t.Errorf("replaceState = %v", res.Log.ReplaceState)
	}
	if last := res.Log.ReplaceState[len(res.Log.ReplaceState)-1]; last != "/audit" {
		t.Errorf("after clearing the filter the URL is %q", last)
	}
	if res.fetchCount() != 0 {
		t.Errorf("the audit page made %d requests", res.fetchCount())
	}
	// Without the audit script the filter is not driven: the group stays hidden and the value untouched.
	res = runJS(t, map[string]any{"dom": auditDOM(rows...), "pathname": "/audit", "search": "?q=GUARD", "scripts": []string{scriptCore},
		"steps": []any{map[string]any{"read": []string{"audit-filter", "filters"}}}})
	if e := res.el(t, 0, "filters"); !e.Hidden {
		t.Error("the core alone drove the audit filter")
	}
	if len(res.Log.ReplaceState) != 0 {
		t.Errorf("the core alone wrote the URL: %v", res.Log.ReplaceState)
	}
}

// A page script acts only on its own page, and the core alone polls nothing: the split is real.
func TestD34PageScriptsActOnlyOnTheirOwnPage(t *testing.T) {
	steps := []any{map[string]any{"advance": 20000}}
	fetches := func(dom map[string]any, scripts ...string) (int, string) {
		res := runJS(t, map[string]any{"dom": dom, "scripts": scripts, "steps": steps, "responses": []any{
			map[string]any{"status": 200, "body": okBody(), "headers": map[string]string{"ETag": `"e1"`}}}})
		url := ""
		if res.fetchCount() > 0 {
			url = res.Log.Fetches[0].URL
		}
		return res.fetchCount(), url
	}
	if n, url := fetches(listDOM(), scriptCore, scriptList); n == 0 || url != "/api/state" {
		t.Errorf("core + list: %d requests, first %q", n, url)
	}
	if n, _ := fetches(listDOM(), scriptCore); n != 0 {
		t.Errorf("the core alone polled the list (%d requests)", n)
	}
	if n, _ := fetches(listDOM(), scriptCore, scriptDetail, scriptAudit); n != 0 {
		t.Errorf("the detail and audit scripts polled the list (%d requests)", n)
	}
	if n, url := fetches(detailDOM("x"), scriptCore, scriptDetail); n == 0 || url != "/api/state/session/opencode/ses_demo0001" {
		t.Errorf("core + detail: %d requests, first %q", n, url)
	}
	if n, _ := fetches(detailDOM("x"), scriptCore, scriptList, scriptAudit); n != 0 {
		t.Errorf("the list and audit scripts polled the session (%d requests)", n)
	}
	// A page script without the core stops quietly: no error, no request, no timer.
	for _, s := range scriptFiles[1:] {
		for _, dom := range []map[string]any{listDOM(), detailDOM("x"), auditDOM(auditTR("x"))} {
			if n, _ := fetches(dom, s); n != 0 {
				t.Errorf("%s without the core made %d requests", s, n)
			}
		}
	}
}

// The core serves the pages that have no page script: the menu, the relative times and the copy button (the turn
// page has a copy button and no poll).
func TestD34CoreAloneKeepsMenuTimesAndCopyWorking(t *testing.T) {
	timeEl := withText(node("time", map[string]string{"datetime": "2026-11-21T17:47:12Z"}), "stale text")
	dom := withID(node("main", nil,
		func() map[string]any { m := withID(node("details", nil), "menu"); m["open"] = true; return m }(),
		hiddenEl(withID(withText(node("button", map[string]string{"data-copy": "ses_demo0001"}), "Copy ID"), "copy")),
		timeEl), "content")
	res := runJS(t, map[string]any{"dom": dom, "narrow": true, "clipboard": "ok", "scripts": []string{scriptCore}, "steps": []any{
		map[string]any{"read": []string{"menu", "copy"}},
		map[string]any{"advance": 30000},
		map[string]any{"click": "copy", "read": []string{"copy"}},
	}})
	if m := res.el(t, 0, "menu"); m.Open {
		t.Error("the menu is open on a narrow screen")
	}
	if c := res.el(t, 0, "copy"); c.Hidden {
		t.Error("the copy button stayed hidden although the clipboard can be written")
	}
	if len(res.Times) != 1 || res.Times[0].Text == "stale text" {
		t.Errorf("the relative time was not refreshed: %+v", res.Times)
	}
	if len(res.Log.Copied) != 1 || res.Log.Copied[0] != "ses_demo0001" {
		t.Errorf("copied %v", res.Log.Copied)
	}
	if res.fetchCount() != 0 {
		t.Errorf("a page with no page script made %d requests", res.fetchCount())
	}
}

// The poll function holds the pause itself: a timer that fires after the tab became hidden but before the
// visibilitychange event was delivered sends nothing (the scheduler alone does not hold it; found by mutation B4,
// which also survived on the unsplit script).
func TestD34PollTimerFiringWhileHiddenSendsNothing(t *testing.T) {
	res := runJS(t, map[string]any{"dom": listDOM(), "responses": []any{map[string]any{"status": 200, "body": okBody()}},
		"steps": []any{
			map[string]any{"advance": 1000},
			map[string]any{"hiddenSilent": true},
			map[string]any{"advance": 5000},
		}})
	if res.fetchCount() != 0 {
		t.Errorf("%d requests were sent although the tab was hidden when the timer fired", res.fetchCount())
	}
}
