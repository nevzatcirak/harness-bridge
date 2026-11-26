package dashboard

// ADR-023 A1.6 JS-2, A1.9 S3b and A1.17 (D34): the light theme, theme.js and the toggle. The tests pin the file and
// its caps, the one place it may touch localStorage, where a page loads it, what the script does (with node), the
// frozen script namespace and the scheme detector the no-inline tests share.

import (
	"errors"
	"html"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

// maxThemeBytes is the cap of theme.js (A1.6 JS-2, D34): it is blocking in the head, so it stays tiny.
const maxThemeBytes = 1 << 10

// scriptCapFailures returns what is wrong with a set of script sizes (bytes by file name): a dashboard*.js file over
// 16 KiB, the dashboard*.js files together over 32 KiB, theme.js over 1 KiB (it has its own cap and is not part of
// the 32 KiB). The caps are tested over this helper with boundary values, and the real files are run through it.
func scriptCapFailures(sizes map[string]int) []string {
	var out []string
	total := 0
	for name, n := range sizes {
		if name == scriptTheme {
			if n > maxThemeBytes {
				out = append(out, name+" is over its 1 KiB cap")
			}
			continue
		}
		total += n
		if n > maxScriptBytes {
			out = append(out, name+" is over the 16 KiB cap of one script")
		}
	}
	if total > maxScriptsTotalBytes {
		out = append(out, "the dashboard scripts are over the 32 KiB cap of all of them")
	}
	return out
}

// #584 P3-1: the cap boundaries are committed tests, not only the sizes of today's files.
func TestS3bScriptCapBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		sizes map[string]int
		fails int
	}{
		{"one script at 16384", map[string]int{"dashboard.js": 16384}, 0},
		{"one script at 16385", map[string]int{"dashboard.js": 16385}, 1},
		{"two scripts, total 32768", map[string]int{"dashboard.js": 16384, "dashboard-list.js": 16384}, 0},
		{"four scripts, total 32769 with no file over its cap", map[string]int{"dashboard.js": 8192, "dashboard-list.js": 8192, "dashboard-detail.js": 8192, "dashboard-audit.js": 8193}, 1},
		{"theme.js at 1024", map[string]int{scriptTheme: 1024}, 0},
		{"theme.js at 1025", map[string]int{scriptTheme: 1025}, 1},
		{"theme.js is not part of the 32 KiB", map[string]int{"dashboard.js": 16384, "dashboard-list.js": 16384, scriptTheme: 1024}, 0},
		{"theme.js over its cap does not hide behind a small total", map[string]int{"dashboard.js": 100, scriptTheme: 4096}, 1},
	}
	for _, c := range cases {
		if got := scriptCapFailures(c.sizes); len(got) != c.fails {
			t.Errorf("%s: %d failures %v, want %d", c.name, len(got), got, c.fails)
		}
	}
}

// The real files satisfy the caps, theme.js is in the table of the served scripts and is not a dashboard*.js page script.
func TestS3bTheScriptTableAndTheRealSizes(t *testing.T) {
	sizes := map[string]int{}
	for _, name := range allScripts {
		raw, err := os.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sizes[name] = len(raw)
	}
	if got := scriptCapFailures(sizes); len(got) != 0 {
		t.Errorf("the real scripts break their caps: %v (sizes %v)", got, sizes)
	}
	if len(allScripts) != len(scriptFiles)+1 || allScripts[len(allScripts)-1] != scriptTheme {
		t.Errorf("allScripts = %v: the four dashboard scripts, then theme.js", allScripts)
	}
	for _, n := range scriptFiles {
		if n == scriptTheme {
			t.Error("theme.js is in the table of the dashboard*.js scripts")
		}
	}
}

// localStorageUse returns what is wrong with the use of localStorage in one script: it is forbidden everywhere except
// in theme.js, where it may only get, set and remove the theme key (A1.6: "localStorage for anything but the theme key").
func localStorageUse(name, code string) []string {
	n := strings.Count(code, "localStorage")
	if name != scriptTheme {
		if n > 0 {
			return []string{name + " uses localStorage (only theme.js may, for the theme key)"}
		}
		return nil
	}
	var out []string
	if n == 0 {
		return []string{"theme.js does not use localStorage"}
	}
	allowed := regexp.MustCompile(`window\.localStorage\.(getItem|setItem|removeItem)\(K[,)]`).FindAllString(code, -1)
	if len(allowed) != n {
		out = append(out, "theme.js touches localStorage other than get/set/remove of K")
	}
	if !regexp.MustCompile(`var K = 'cirak-theme'[,;]`).MatchString(code) && !regexp.MustCompile(`\bK = 'cirak-theme'`).MatchString(code) {
		out = append(out, "theme.js: K is not the theme key 'cirak-theme'")
	}
	return out
}

func TestS3bLocalStorageIsForThemeJSAndTheThemeKeyOnly(t *testing.T) {
	good := "(function () { var K = 'cirak-theme'; try { var v = window.localStorage.getItem(K); window.localStorage.setItem(K, v); window.localStorage.removeItem(K); } catch (e) {} })();"
	if got := localStorageUse(scriptTheme, good); len(got) != 0 {
		t.Errorf("the detector refuses the allowed use: %v", got)
	}
	for _, bad := range []string{
		strings.Replace(good, "getItem(K)", "getItem('other')", 1), strings.Replace(good, "setItem(K, v)", "setItem('x', v)", 1),
		good + " window.localStorage.clear();", good + " var s = window.localStorage;", strings.Replace(good, "'cirak-theme'", "'cirak'", 1),
		"window.localStorage.getItem(K)", "(function () {})();",
	} {
		if len(localStorageUse(scriptTheme, bad)) == 0 {
			t.Errorf("the detector accepts %q", bad)
		}
	}
	if len(localStorageUse(scriptCore, good)) == 0 || len(localStorageUse(scriptList, good)) == 0 {
		t.Error("the detector accepts localStorage in a dashboard script")
	}
	raw, err := os.ReadFile("assets/" + scriptTheme)
	if err != nil {
		t.Fatal(err)
	}
	if got := localStorageUse(scriptTheme, string(raw)); len(got) != 0 {
		t.Errorf("theme.js: %v", got)
	}
}

// A1.9 S3b, A1.3: every page of the dashboard carries the blocking theme script in its head and the toggle in the top
// bar, hidden until the script runs (no dead control without JS, A1.10 item 9); a page never has one without the other.
func TestS3bEveryPageCarriesTheThemeScriptInTheHeadAndTheHiddenToggle(t *testing.T) {
	head := regexp.MustCompile(`(?s)<head>.*?</head>`)
	tag := `<script src="/theme.js"></script>`
	button := regexp.MustCompile(`<button class="btn" type="button" id="theme"[^>]*\shidden[^>]*>Theme</button>`)
	n := 0
	for name, body := range goldenPages(t) {
		n++
		h := head.FindString(body)
		if strings.Count(h, tag) != 1 || strings.Count(body, tag) != 1 {
			t.Errorf("%s: the theme script is not once, in the head", name)
		}
		if strings.Contains(h, "async") || strings.Contains(h, "defer") {
			t.Errorf("%s: the theme script must block (no async, no defer)", name)
		}
		if strings.Index(h, tag) < strings.Index(h, `<link rel="stylesheet" href="/dashboard.css">`) {
			t.Errorf("%s: the theme script is before the stylesheet", name)
		}
		if got := len(button.FindAllString(body, -1)); got != 1 || strings.Count(body, `id="theme"`) != 1 {
			t.Errorf("%s: %d hidden theme toggles in the top bar, want exactly 1", name, got)
		}
		bar := regexp.MustCompile(`(?s)<header class="topbar">.*?</header>`).FindString(body)
		if !button.MatchString(bar) {
			t.Errorf("%s: the toggle is not in the top bar", name)
		}
		if strings.Contains(body, tag) != button.MatchString(body) {
			t.Errorf("%s: the theme script and the toggle markup do not go together", name)
		}
	}
	if n < 20 {
		t.Errorf("only %d pages were checked", n)
	}
}

// ---- behaviour, with node (A1.10 item 12: the stored theme value is ignored unless it is dark or light) ---------------

func themeDOM() map[string]any {
	return withID(node("main", nil, hiddenEl(withID(withText(node("button", nil), "Theme"), "theme"))), "content")
}

func themeRun(t *testing.T, extra map[string]any, steps ...any) jsResult {
	t.Helper()
	sc := map[string]any{"dom": themeDOM(), "scripts": []string{scriptTheme}, "steps": steps}
	for k, v := range extra {
		sc[k] = v
	}
	return runJS(t, sc)
}

func (r jsResult) themeOps() []string {
	var out []string
	for _, s := range r.Log.Storage {
		out = append(out, s.Op+" "+s.Key+" "+s.Value)
	}
	return out
}

func TestS3bThemeScriptAppliesOnlyADarkOrLightStoredValueBeforeFirstPaint(t *testing.T) {
	for _, v := range []string{"dark", "light"} {
		// noReady: the document is not parsed yet, as when a script of the head runs
		res := themeRun(t, map[string]any{"stored": map[string]string{"cirak-theme": v}, "noReady": true})
		if res.HTMLAttrs["data-theme"] != v {
			t.Errorf("stored %q: data-theme = %q before the document is ready", v, res.HTMLAttrs["data-theme"])
		}
		for _, op := range res.themeOps() {
			if strings.HasPrefix(op, "set") || strings.HasPrefix(op, "remove") {
				t.Errorf("stored %q: a valid value was rewritten: %s", v, op)
			}
		}
	}
	for _, bad := range []string{"", "Dark", "LIGHT", "dark ", "auto", "system", "<script>", "javascript:alert(1)", "dark;light", "0", "null", "undefined", "x\x00"} {
		res := themeRun(t, map[string]any{"stored": map[string]string{"cirak-theme": bad}})
		if v, ok := res.HTMLAttrs["data-theme"]; ok {
			t.Errorf("stored %q was used: data-theme = %q", bad, v)
		}
		if !strings.Contains(strings.Join(res.themeOps(), "|"), "remove cirak-theme") {
			t.Errorf("stored %q was not removed: %v", bad, res.themeOps())
		}
		if _, still := res.Store["cirak-theme"]; still {
			t.Errorf("stored %q is still in the store", bad)
		}
	}
	// nothing stored: the system preference applies (no attribute), nothing is written
	res := themeRun(t, nil)
	if _, ok := res.HTMLAttrs["data-theme"]; ok {
		t.Errorf("nothing stored but data-theme = %q", res.HTMLAttrs["data-theme"])
	}
	for _, op := range res.themeOps() {
		if !strings.HasPrefix(op, "get cirak-theme") {
			t.Errorf("an unexpected storage operation with nothing stored: %s", op)
		}
	}
}

func TestS3bThemeToggleIsHiddenUntilTheScriptRunsAndSwitchesAndRemembers(t *testing.T) {
	read := map[string]any{"read": []string{"theme"}}
	// light by the system preference: the first click chooses dark, the second light again
	res := themeRun(t, map[string]any{"prefersLight": true}, read, map[string]any{"click": "theme", "read": []string{"theme"}}, map[string]any{"click": "theme"})
	if e := res.el(t, 0, "theme"); e.Hidden {
		t.Error("the toggle stays hidden although the script ran")
	}
	if res.HTMLAttrs["data-theme"] != "light" {
		t.Errorf("after two clicks from a light system the theme is %q", res.HTMLAttrs["data-theme"])
	}
	ops := strings.Join(res.themeOps(), "|")
	if !strings.Contains(ops, "set cirak-theme dark") || !strings.Contains(ops, "set cirak-theme light") {
		t.Errorf("the choices were not stored: %v", res.themeOps())
	}
	// dark by default (no preference): the first click chooses light
	res = themeRun(t, nil, map[string]any{"click": "theme"})
	if res.HTMLAttrs["data-theme"] != "light" || res.Store["cirak-theme"] != "light" {
		t.Errorf("first click from the default: theme %q, stored %q", res.HTMLAttrs["data-theme"], res.Store["cirak-theme"])
	}
	// a stored choice wins over the system preference, and the click flips that choice
	res = themeRun(t, map[string]any{"prefersLight": true, "stored": map[string]string{"cirak-theme": "dark"}}, map[string]any{"click": "theme"})
	if res.HTMLAttrs["data-theme"] != "light" {
		t.Errorf("stored dark, click: theme %q, want light", res.HTMLAttrs["data-theme"])
	}
	// only the theme key is ever touched, and the script makes no request
	for _, op := range res.themeOps() {
		if !strings.Contains(op, " cirak-theme") {
			t.Errorf("a storage operation on another key: %s", op)
		}
	}
	if res.fetchCount() != 0 {
		t.Errorf("the theme script made %d requests", res.fetchCount())
	}
}

func TestS3bThemeScriptSurvivesBlockedFullAndAbsentStorage(t *testing.T) {
	for _, mode := range []string{"blocked", "full", "absent"} {
		res := themeRun(t, map[string]any{"storage": mode}, map[string]any{"click": "theme", "read": []string{"theme"}})
		if e := res.el(t, 0, "theme"); e.Hidden {
			t.Errorf("%s storage: the toggle stayed hidden", mode)
		}
		if res.HTMLAttrs["data-theme"] != "light" {
			t.Errorf("%s storage: the click did not switch the theme for this page (%q)", mode, res.HTMLAttrs["data-theme"])
		}
	}
}

// A script that is not there (no JS) leaves the toggle hidden: the page without the script is the no-JS render.
func TestS3bToggleStaysHiddenWithoutTheScript(t *testing.T) {
	res := runJS(t, map[string]any{"dom": themeDOM(), "scripts": []string{scriptCore}, "steps": []any{map[string]any{"read": []string{"theme"}}}})
	if e := res.el(t, 0, "theme"); !e.Hidden {
		t.Error("the core alone showed the theme toggle")
	}
}

// JS-2 and #585 P3-2: the page namespace is frozen, so no script can replace a helper another script uses.
func TestS3bTheScriptNamespaceIsFrozen(t *testing.T) {
	res := runJS(t, map[string]any{"dom": themeDOM(), "scripts": []string{scriptCore},
		"probes": []string{
			"Object.isFrozen(window.cirakDash)",
			"(function () { 'use strict'; try { window.cirakDash.el = null; return 'assigned'; } catch (e) { return 'refused'; } })()",
			"(function () { 'use strict'; try { window.cirakDash.extra = 1; return 'added'; } catch (e) { return 'refused'; } })()",
			"typeof window.cirakDash.poll",
		}})
	want := []any{true, "refused", "refused", "function"}
	for i, w := range want {
		if i >= len(res.Probes) || res.Probes[i] != w {
			t.Errorf("probe %d = %v, want %v (all %v)", i, res.Probes, w, res.Probes)
			break
		}
	}
	raw, _ := os.ReadFile("assets/" + scriptCore)
	if !strings.Contains(string(raw), "Object.freeze(") {
		t.Error("the core does not freeze its namespace")
	}
}

// ---- the scheme detector of the no-inline tests (#585 P3-1) -------------------------------------------------------

// urlScheme is the form of an attribute value a browser acts on: the HTML entities decoded, every ASCII tab, CR and LF
// removed (browsers strip them anywhere in a URL), leading and trailing C0 controls and spaces trimmed, lower case.
func urlScheme(v string) string {
	v = html.UnescapeString(v)
	v = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(v)
	v = strings.TrimFunc(v, func(r rune) bool { return r <= 0x20 })
	return strings.ToLower(v)
}

func runsScript(v string) bool { return strings.HasPrefix(urlScheme(v), "javascript:") }

func TestS3bTheJavascriptSchemeDetectorSeesTheFormsABrowserRuns(t *testing.T) {
	for _, bad := range []string{
		"javascript:alert(1)", " JaVaScRiPt:alert(1)", "jav&#x09;ascript:alert(1)", "jav&#9;ascript:alert(1)", "jav&#10;ascript:alert(1)",
		"java&#13;script:alert(1)", "j\ta\nv\ra\tscript:alert(1)", "\x01javascript:alert(1)", "&#x20;javascript:alert(1)", "javascript&colon;alert(1)x",
	} {
		// "javascript&colon;" is the one entity form html.UnescapeString turns into the colon
		if !runsScript(bad) {
			t.Errorf("the detector accepts %q", bad)
		}
	}
	for _, good := range []string{"/session/opencode/ses_x", "#content", "https://example.test/javascript:", "javascript", "java script:x", ""} {
		if runsScript(good) {
			t.Errorf("the detector refuses %q", good)
		}
	}
}

// A page whose markup carries a javascript: URL with a control character in the scheme is refused by the detectors the
// no-inline tests use (the real pages carry none).
func TestS3bNoInlineAttributeScanRefusesAControlCharacterInTheScheme(t *testing.T) {
	page := `<a href="jav&#x09;ascript:alert(1)">x</a>`
	bad := 0
	for _, a := range htmlAttrs(page) {
		if runsScript(a.value) {
			bad++
		}
	}
	if bad != 1 {
		t.Errorf("the attribute scan found %d javascript: URLs in %q, want 1", bad, page)
	}
}

// ---- #580 P3-1: the audit page parameter is digits only ---------------------------------------------------------

// With the full ring (1000 entries, 10 pages) the characters between '9' and 'z' must not select a page: a parser
// that accepts them would map ":" to page 10.
func TestS3bAuditPageParameterIsDigitsOnlyOnAFullRing(t *testing.T) {
	o := v2Opts()
	o.Audit = fakeAuditOf(auditEntries(AuditRingSize, "interrupt", "accepted")...)
	h := NewHandlerOptions(&fakeSource{}, nil, o)
	_, first, _ := getBody(t, h, "/audit")
	for _, v := range []string{":", ";", "<", "0:", "1:", "a", "z", "%3A", "٣", "+1", "%201", "1%20", "10x"} {
		code, body, _ := getBody(t, h, "/audit?page="+v)
		if code != http.StatusOK || body != first {
			t.Errorf("page=%q: %d, the page is not page 1", v, code)
		}
	}
	if _, body, _ := getBody(t, h, "/audit?page=10"); body == first {
		t.Error("page=10 of a full ring shows page 1")
	}
}

// ---- the P3 follow-ups of #576 ------------------------------------------------------------------------------------

// #576 P3-2: the label rules, one character at a time (a "return true for any one-byte value" mutation must fail).
func TestS3bLabelSingleCharacterCases(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == EnvLabel {
				return v
			}
			return ""
		}
	}
	for _, ok := range []string{"a", "Z", "0", "-", ".", "_"} {
		if !ValidLabel(ok) {
			t.Errorf("%q was refused", ok)
		}
		if _, err := ConfigFromEnv(env(ok)); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"<", ">", "&", "'", `"`, "=", ":", "\\", "(", ")", "/", "é", "\t", "\n", "\r", "\x00", ";", "`", "%", "#", "?", "{", "$"} {
		if ValidLabel(bad) {
			t.Errorf("%q was accepted", bad)
		}
		if _, err := ConfigFromEnv(env(bad)); err == nil || !strings.Contains(err.Error(), EnvLabel) || strings.Contains(err.Error(), bad) {
			t.Errorf("%q: error %v must name the variable and never echo the value", bad, err)
		}
	}
}

// #576 P3-4: a 503 page keeps the top bar: the label is process configuration and is known when the Source is down;
// the token badge comes from Health, which is memory only, so it stays too (unless Health itself fails).
func TestS3bUnavailablePagesKeepTheTopBar(t *testing.T) {
	for _, c := range []struct {
		target string
		set    func(*s3Src)
		badge  bool
	}{
		{"/", func(s *s3Src) { s.rowsErr = errors.New("down") }, true},
		{"/catalog", func(s *s3Src) { s.rowsErr = errors.New("down") }, true},
		{"/executions", func(s *s3Src) { s.execsErr = errors.New("down") }, true},
		{"/health", func(s *s3Src) { s.healthErr = errors.New("down") }, false},
	} {
		src := newS3Src()
		c.set(src)
		code, body, hdr := getBody(t, s3Handler(src, "bridge-a"), c.target)
		if code != http.StatusServiceUnavailable || hdr.Get("Retry-After") == "" {
			t.Fatalf("%s = %d, want 503 with Retry-After", c.target, code)
		}
		bar := between(t, body, `<header class="topbar">`, `</header>`)
		if !strings.Contains(bar, `<span class="label" id="instance">bridge-a</span>`) {
			t.Errorf("%s: the 503 page lost the instance label", c.target)
		}
		if got := strings.Contains(bar, "Token expires in"); got != c.badge {
			t.Errorf("%s: token badge shown = %v, want %v", c.target, got, c.badge)
		}
		if !strings.Contains(bar, `id="theme"`) {
			t.Errorf("%s: the 503 page lost the theme toggle", c.target)
		}
	}
}

// #576 P3-5: the build row shows the whole revision (the VCS revision is 40 hex characters), in the page and in the
// JSON twin, so an operator can compare it with a commit; it stays bounded.
func TestS3bHealthShowsTheWholeBuildRevision(t *testing.T) {
	src := newS3Src()
	rev := "0123456789abcdef0123456789abcdef01234567"
	_, body, _ := getBody(t, s3Handler(src, ""), "/health")
	if !strings.Contains(body, "<code>"+rev+"</code>") {
		t.Errorf("the page does not show the whole revision")
	}
	_, twin, _ := getBody(t, s3Handler(src, ""), "/api/state/health")
	if !strings.Contains(twin, rev) {
		t.Errorf("the twin does not carry the whole revision")
	}
	src.health.Build = strings.Repeat("a", 500)
	_, long, _ := getBody(t, s3Handler(src, ""), "/health")
	if strings.Contains(long, strings.Repeat("a", 65)) {
		t.Error("an over-long revision is not bounded")
	}
}

// #576 P3-6: the page says that the list is capped (the Source caps at 200, so the page can never count more).
func TestS3bExecutionsFootnoteSaysTheListIsCapped(t *testing.T) {
	_, body, _ := getBody(t, s3Handler(newS3Src(), ""), "/executions")
	if !strings.Contains(body, "at most the newest 200 executions") {
		t.Error("the executions page does not say that the list is capped at 200")
	}
}
