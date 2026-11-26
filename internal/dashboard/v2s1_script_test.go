package dashboard

// ADR-023 A1.10 items 5 and 12 (slice S1): the shipped script is scanned for forbidden APIs and run, unmodified,
// in a Node vm with a fake DOM, a fake clock and a scripted fetch (testdata/js_harness.js). The behaviour tests
// need the node binary: without it they are SKIPPED, which is never a pass (the CI runner has node).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var jsLineComment = regexp.MustCompile(`(?m)//[^\n]*`)

// A1.6, D34 and S3b: the forbidden APIs are scanned in EVERY script (the table the server serves), on the RAW source (a
// comment stripper that mistakes a "//" inside a string for a comment would hide the rest of the line, #585 P3-2, so
// the scripts keep these words out of their comments too), and each script, their sum and theme.js have a size cap
// (checked over scriptCapFailures, which the boundary test also runs).
func TestV2S1ScriptHygiene(t *testing.T) {
	sizes := map[string]int{}
	var all strings.Builder
	for _, name := range allScripts {
		raw, err := os.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sizes[name] = len(raw)
		code := string(raw)
		all.WriteString(code)
		for _, bad := range forbiddenScriptAPIs {
			if regexp.MustCompile(bad.re).MatchString(code) {
				t.Errorf("%s uses %s", name, bad.what)
			}
		}
		for _, f := range localStorageUse(name, code) {
			t.Error(f)
		}
	}
	for _, f := range scriptCapFailures(sizes) {
		t.Error(f)
	}
	for _, want := range []string{"textContent", "AbortController", "visibilitychange", "replaceState", "matchMedia", "If-None-Match", "Retry-After"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("the scripts lack %q", want)
		}
	}
}

// D34: the size caps of one script and of the four dashboard*.js together (ADR-023 A1.6, as amended); theme.js has its
// own cap (maxThemeBytes, s3b_test.go).
const (
	maxScriptBytes       = 16 << 10
	maxScriptsTotalBytes = 32 << 10
)

// forbiddenScriptAPIs is the A1.6 list; the scan runs on every script file. localStorage is not in it: it is allowed
// in theme.js for the theme key only, and refused everywhere else (localStorageUse).
var forbiddenScriptAPIs = []struct{ re, what string }{
	{`\.innerHTML`, "innerHTML"}, {`\.outerHTML`, "outerHTML"}, {`insertAdjacentHTML`, "insertAdjacentHTML"},
	{`document\.write`, "document.write"}, {`\beval\s*\(`, "eval"}, {`new\s+Function`, "new Function"},
	{`set(Timeout|Interval)\s*\(\s*['"` + "`" + `]`, "a string argument to setTimeout/setInterval"}, {`\bimport\s*\(`, "dynamic import()"},
	{`https?:`, "an http(s) literal"}, {`sessionStorage`, "sessionStorage"}, {`indexedDB`, "indexedDB"},
	{`XMLHttpRequest`, "XMLHttpRequest"}, {`\.cookie`, "document.cookie"}, {`method:\s*'(POST|PUT|PATCH|DELETE)'`, "a write method"},
}

// ---- the harness ---------------------------------------------------------------------------------------------

type jsSnap struct {
	Fetches int                        `json:"fetches"`
	Pending []int64                    `json:"pending"`
	Aborted int                        `json:"aborted"`
	Els     map[string]json.RawMessage `json:"-"`
}

type jsEl struct {
	Text   string `json:"text"`
	Hidden bool   `json:"hidden"`
	Open   bool   `json:"open"`
	Value  string `json:"value"`
	HTML   string `json:"html"`
}

type jsResult struct {
	Steps []map[string]json.RawMessage `json:"steps"`
	Log   struct {
		Fetches []struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
			At      int64             `json:"at"`
		} `json:"fetches"`
		ReplaceState []string `json:"replaceState"`
		Notes        []string `json:"notes"`
		Reloads      int      `json:"reloads"`
		Copied       []string `json:"copied"`
		Storage      []struct {
			Op    string `json:"op"`
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"log"`
	HTMLAttrs map[string]string `json:"htmlAttrs"`
	Store     map[string]string `json:"store"`
	Probes    []any             `json:"probes"`
	Times     []struct {
		Text     string `json:"text"`
		Datetime string `json:"datetime"`
	} `json:"times"`
	Live        string `json:"live"`
	StatusInner string `json:"statusInner"`
}

func (r jsResult) step(t *testing.T, i int) (fetches int, pending []int64, aborted int) {
	t.Helper()
	if i >= len(r.Steps) {
		t.Fatalf("no step %d", i)
	}
	_ = json.Unmarshal(r.Steps[i]["fetches"], &fetches)
	_ = json.Unmarshal(r.Steps[i]["pending"], &pending)
	_ = json.Unmarshal(r.Steps[i]["aborted"], &aborted)
	return
}

func (r jsResult) el(t *testing.T, i int, id string) jsEl {
	t.Helper()
	var e jsEl
	if raw, ok := r.Steps[i][id]; !ok || json.Unmarshal(raw, &e) != nil {
		t.Fatalf("step %d has no element %q", i, id)
	}
	return e
}

// scriptPathsFor returns the files the harness runs, in load order: the scenario's own "scripts" (file names), or
// those the page of the scenario's DOM loads (the core, plus the one for its data-poll or its audit filter).
func scriptPathsFor(scenario map[string]any) []string {
	names := []string{scriptCore}
	if own, ok := scenario["scripts"].([]string); ok {
		names = own
	} else if dom, ok := scenario["dom"].(map[string]any); ok {
		switch {
		case domHasAttr(dom, "data-poll", "list"):
			names = append(names, scriptList)
		case domHasAttr(dom, "data-poll", "detail"):
			names = append(names, scriptDetail)
		case domHasID(dom, "audit-filter"):
			names = append(names, scriptAudit)
		}
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "assets/" + n
	}
	return out
}

func domWalk(n map[string]any, f func(map[string]any) bool) bool {
	if f(n) {
		return true
	}
	kids, _ := n["children"].([]map[string]any)
	for _, c := range kids {
		if domWalk(c, f) {
			return true
		}
	}
	return false
}

func domHasAttr(n map[string]any, k, v string) bool {
	return domWalk(n, func(m map[string]any) bool { a, _ := m["attrs"].(map[string]string); return a != nil && a[k] == v })
}

func domHasID(n map[string]any, id string) bool {
	return domWalk(n, func(m map[string]any) bool { s, _ := m["id"].(string); return s == id })
}

func runJS(t *testing.T, scenario map[string]any) jsResult {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: the dashboard script behaviour tests did NOT run (a skip is not a pass)")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "scenario.json")
	b, _ := json.Marshal(scenario)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "testdata/js_harness.js", strings.Join(scriptPathsFor(scenario), ","), path)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("node: %v\n%s", err, errb.String())
	}
	var res jsResult
	if err := json.Unmarshal([]byte(out.String()), &res); err != nil {
		t.Fatalf("harness output: %v\n%s", err, out.String())
	}
	return res
}

func node(tag string, attrs map[string]string, children ...map[string]any) map[string]any {
	m := map[string]any{"tag": tag}
	if attrs != nil {
		m["attrs"] = attrs
	}
	if len(children) > 0 {
		m["children"] = children
	}
	return m
}

func withID(m map[string]any, id string) map[string]any { m["id"] = id; return m }
func hiddenEl(m map[string]any) map[string]any          { m["hidden"] = true; return m }
func withText(m map[string]any, s string) map[string]any {
	m["text"] = s
	return m
}

// listDOM is the markup of the sessions page that the script looks up (shell.go): the same ids.
func listDOM(rows ...map[string]any) map[string]any {
	opt := func(v string) map[string]any { m := node("option", nil); m["value"] = v; return m }
	sel := func(id string, vals ...string) map[string]any {
		var os []map[string]any
		for _, v := range vals {
			os = append(os, opt(v))
		}
		return withID(node("select", nil, os...), id)
	}
	tbody := node("tbody", nil, rows...)
	live := withID(node("div", nil, node("div", map[string]string{"class": "table-wrap"}, node("table", nil, tbody))), "live")
	status := withID(node("span", nil, node("time", map[string]string{"datetime": "2026-11-21T17:48:05Z", "title": "2026-11-21 17:48:05Z"})), "status")
	return withID(node("main", map[string]string{"data-poll": "list"},
		hiddenEl(withID(node("p", nil), "stale")),
		func() map[string]any { m := withID(node("details", nil), "menu"); m["open"] = true; return m }(),
		hiddenEl(withID(node("div", nil, withID(node("input", nil), "f-q"), sel("f-state", "", "running", "waiting_for_permission", "idle", "unknown"), sel("f-harness", "", "opencode", "claudecode")), "filters")),
		status, live,
	), "content")
}

func sessionTR(harness, state, text string) map[string]any {
	return withText(node("tr", map[string]string{"data-harness": harness, "data-state": state}), text)
}

func okBody() map[string]any {
	return map[string]any{"kind": "list", "connected": true, "count": 0, "count_text": "0 sessions", "sessions": []any{}}
}

// ---- A1.7: pause when hidden, one refresh when visible again ---------------------------------------------------

func TestV2S1ScriptPausesWhileHiddenAndRefreshesOnReturn(t *testing.T) {
	res := runJS(t, map[string]any{
		"dom": listDOM(), "responses": []any{
			map[string]any{"status": 200, "body": okBody(), "headers": map[string]string{"ETag": `"e1"`}},
			map[string]any{"hang": true},
			map[string]any{"status": 200, "body": okBody()},
		},
		"steps": []any{
			map[string]any{"advance": 4999},                  // 0: nothing yet
			map[string]any{"advance": 1},                     // 1: the first poll at 5 s
			map[string]any{"advance": 5000},                  // 2: the second poll (hangs)
			map[string]any{"hidden": true},                   // 3: the tab is hidden: the request is aborted, no timer
			map[string]any{"advance": 600000},                // 4: ten minutes hidden: no request at all
			map[string]any{"hidden": false},                  // 5: visible again: one immediate refresh, no waiting
			map[string]any{"advance": 0, "read": []string{}}, // 6
		},
	})
	f0, _, _ := res.step(t, 0)
	f1, p1, _ := res.step(t, 1)
	f2, _, _ := res.step(t, 2)
	f3, p3, ab3 := res.step(t, 3)
	f4, p4, _ := res.step(t, 4)
	f5, p5, _ := res.step(t, 5)
	if f0 != 0 || f1 != 1 || f2 != 2 {
		t.Errorf("fetches before hiding = %d, %d, %d; want 0, 1, 2 (poll every 5 s)", f0, f1, f2)
	}
	if !hasDelay(p1, 5000) {
		t.Errorf("after a successful poll the pending timers are %v, want a poll in 5000", p1)
	}
	if ab3 != 1 || f3 != 2 {
		t.Errorf("hiding the tab: aborted %d (want 1), fetches %d (want 2)", ab3, f3)
	}
	for _, p := range [][]int64{p3, p4} {
		if len(p) != 0 {
			t.Errorf("a timer is pending while the tab is hidden: %v", p)
		}
	}
	if f4 != 2 {
		t.Errorf("%d requests were sent while hidden", f4-2)
	}
	if f5 != 3 {
		t.Errorf("on becoming visible %d requests were sent, want exactly one immediate refresh", f5-2)
	}
	if len(p5) == 0 {
		t.Error("polling did not resume after the tab became visible")
	}
	// The conditional request carries the ETag of the last answer (A1.7: 304 when unchanged).
	if got := res.Log.Fetches[1].Headers["If-None-Match"]; got != `"e1"` {
		t.Errorf("the second request sent If-None-Match %q, want the ETag of the first answer", got)
	}
	for _, f := range res.Log.Fetches {
		if f.Method != "GET" {
			t.Errorf("a %s request", f.Method)
		}
	}
}

// An answer that arrives after the tab was hidden never re-arms the poll timer (the pause is held by the scheduler,
// not only by the event handler).
func TestV2S1ScriptNeverSchedulesWhileHidden(t *testing.T) {
	res := runJS(t, map[string]any{"dom": listDOM(), "responses": []any{map[string]any{"hold": true}},
		"steps": []any{
			map[string]any{"advance": 5000},
			map[string]any{"hiddenSilent": true},
			map[string]any{"release": map[string]any{"status": 200, "body": okBody()}},
			map[string]any{"advance": 600000},
		}})
	// Step 2: the event has not been delivered yet, so the relative-time tick is still armed; the poll must not be.
	if _, p, _ := res.step(t, 2); hasDelay(p, 5000) {
		t.Errorf("an answer that arrived while hidden re-armed the poll timer: %v", p)
	}
	if _, p, _ := res.step(t, 3); len(p) != 0 {
		t.Errorf("timers are pending while the tab is hidden: %v", p)
	}
	if f, _, _ := res.step(t, 3); f != 1 {
		t.Errorf("%d requests, want only the first one", f)
	}
}

// A1.7 errors: max(the page's own interval, Retry-After), doubled on each further failure, at most 60 s, reset on success.
func TestV2S1ScriptBackoffFloorAndRetryAfter(t *testing.T) {
	fail := func(ra string) map[string]any {
		h := map[string]string{}
		if ra != "" {
			h["Retry-After"] = ra
		}
		return map[string]any{"status": 503, "body": map[string]any{}, "headers": h}
	}
	run := func(ra string, n int) []int64 {
		var resp []any
		for i := 0; i < n; i++ {
			resp = append(resp, fail(ra))
		}
		res := runJS(t, map[string]any{"dom": listDOM(), "responses": resp, "steps": func() []any {
			s := []any{map[string]any{"advance": 5000}}
			for i := 1; i < n; i++ {
				s = append(s, map[string]any{"advance": 0})
			}
			return s
		}()})
		_, p, _ := res.step(t, 0)
		return p
	}
	// The first failure: the delay is max(5000, Retry-After): Retry-After: 1 must NOT make the retry faster than 5 s.
	if p := run("1", 1); !hasDelay(p, 5000) || hasDelay(p, 1000) {
		t.Errorf("after a 503 with Retry-After: 1 the pending timers are %v, want a poll in 5000 and none in 1000 (the floor is the page's own interval)", p)
	}
	if p := run("", 1); !hasDelay(p, 5000) {
		t.Errorf("after a 503 without Retry-After the pending timers are %v, want a poll in 5000", p)
	}
	if p := run("30", 1); !hasDelay(p, 30000) || hasDelay(p, 5000) {
		t.Errorf("after a 503 with Retry-After: 30 the pending timers are %v, want a poll in 30000", p)
	}
	// A sequence of failures doubles, up to 60 s.
	var resp []any
	var steps []any
	for i := 0; i < 7; i++ {
		resp = append(resp, fail("1"))
	}
	steps = append(steps, map[string]any{"advance": 5000})
	for _, d := range []int{5000, 10000, 20000, 40000, 60000, 60000} {
		steps = append(steps, map[string]any{"advance": d})
	}
	res := runJS(t, map[string]any{"dom": listDOM(), "responses": resp, "steps": steps})
	want := []int64{5000, 10000, 20000, 40000, 60000, 60000, 60000}
	for i, w := range want {
		_, p, _ := res.step(t, i)
		if !hasDelay(p, w) {
			t.Errorf("after failure %d the pending timers are %v, want a poll in %d", i+1, p, w)
		}
	}
	// The stale banner says which data is shown (the page's own render time), and a success resets everything.
	res = runJS(t, map[string]any{"dom": listDOM(), "responses": []any{fail("1"), fail("1"), map[string]any{"status": 200, "body": okBody()}},
		"steps": []any{
			map[string]any{"advance": 5000, "read": []string{"stale"}},
			map[string]any{"advance": 5000},
			map[string]any{"advance": 10000, "read": []string{"stale"}},
		}})
	if b := res.el(t, 0, "stale"); b.Hidden || b.Text != "Showing the data from 17:48:05Z; the last refresh failed" {
		t.Errorf("stale banner = %+v", b)
	}
	if b := res.el(t, 2, "stale"); !b.Hidden || b.Text != "" {
		t.Errorf("the banner is not cleared by a successful poll: %+v", b)
	}
	if _, p, _ := res.step(t, 2); !hasDelay(p, 5000) {
		t.Errorf("after a success the delay is back to 5000: %v", p)
	}
	// A network error (a rejected fetch) is a failure too.
	res = runJS(t, map[string]any{"dom": listDOM(), "responses": []any{map[string]any{"reject": true}}, "steps": []any{map[string]any{"advance": 5000, "read": []string{"stale"}}}})
	if b := res.el(t, 0, "stale"); b.Hidden {
		t.Error("a rejected fetch does not show the stale banner")
	}
}

// A 304 keeps what is shown and sends the validator.
func TestV2S1ScriptNotModifiedKeepsTheData(t *testing.T) {
	res := runJS(t, map[string]any{"dom": listDOM(), "responses": []any{
		map[string]any{"status": 200, "body": okBody(), "headers": map[string]string{"ETag": `"v1"`}},
		map[string]any{"status": 304, "body": nil, "headers": map[string]string{}},
		map[string]any{"status": 304, "body": nil, "headers": map[string]string{}},
	}, "steps": []any{map[string]any{"advance": 5000}, map[string]any{"advance": 5000, "read": []string{"stale"}}, map[string]any{"advance": 5000}}})
	if res.Log.Fetches[1].Headers["If-None-Match"] != `"v1"` || res.Log.Fetches[2].Headers["If-None-Match"] != `"v1"` {
		t.Errorf("validators sent: %+v", res.Log.Fetches)
	}
	if b := res.el(t, 1, "stale"); !b.Hidden {
		t.Error("a 304 is a healthy answer: no stale banner")
	}
	if _, p, _ := res.step(t, 2); !hasDelay(p, 5000) {
		t.Errorf("next poll after a 304: %v", p)
	}
}

// ---- JS-4: filters with URL state, values only through .value ---------------------------------------------------

func TestV2S1ScriptFiltersRoundTripThroughTheURL(t *testing.T) {
	rows := []map[string]any{
		sessionTR("opencode", "running", "Fix flaky retry test ses_demo0001 /srv/work/payments-api/services/billing"),
		sessionTR("opencode", "idle", "Spike cache ses_demo0003 /srv/work/spikes/cache"),
		sessionTR("claudecode", "running", "Docs pass claudecode-4e59c1bd /srv/work/site/docs"),
	}
	res := runJS(t, map[string]any{
		"dom": listDOM(rows...), "pathname": "/", "search": "?archived=1&q=BILLING&state=running&harness=opencode",
		"steps": []any{
			map[string]any{"read": []string{"f-q", "f-state", "f-harness", "filters"}},
			map[string]any{"input": map[string]string{"id": "f-q", "value": ""}},
			map[string]any{"input": map[string]string{"id": "f-state", "value": "idle"}},
		},
	})
	if e := res.el(t, 0, "f-q"); e.Value != "BILLING" {
		t.Errorf("q was not restored from the URL: %+v", e)
	}
	if e := res.el(t, 0, "f-state"); e.Value != "running" {
		t.Errorf("state: %+v", e)
	}
	if e := res.el(t, 0, "f-harness"); e.Value != "opencode" {
		t.Errorf("harness: %+v", e)
	}
	if e := res.el(t, 0, "filters"); e.Hidden {
		t.Error("the filter group is still hidden after the script ran")
	}
	// Only the first row matches q=billing, state=running, harness=opencode: the others are hidden.
	if len(res.Log.ReplaceState) == 0 || res.Log.ReplaceState[0] != "/?archived=1&q=BILLING&state=running&harness=opencode" {
		t.Errorf("replaceState = %v", res.Log.ReplaceState)
	}
	last := res.Log.ReplaceState[len(res.Log.ReplaceState)-1]
	if last != "/?archived=1&state=idle&harness=opencode" {
		t.Errorf("after clearing q and choosing idle the URL is %q", last)
	}
}

func TestV2S1ScriptFilterValuesAreCheckedAndNeverMarkup(t *testing.T) {
	evil := "<img src=x onerror=alert(1)>"
	long := strings.Repeat("a", 200)
	res := runJS(t, map[string]any{
		"dom": listDOM(sessionTR("opencode", "idle", "one")), "pathname": "/",
		"search": "?q=" + strings.ReplaceAll(strings.ReplaceAll(evil, "<", "%3C"), ">", "%3E") + "&state=" + strings.ReplaceAll(evil, "<", "%3C") + "&harness=nope&x=" + long,
		"steps": []any{map[string]any{"read": []string{"f-q", "f-state", "f-harness", "live"}},
			map[string]any{"input": map[string]string{"id": "f-q", "value": long}}},
	})
	if e := res.el(t, 0, "f-q"); e.Value != evil {
		t.Errorf("q = %q", e.Value) // the value is shown in the control as text, nothing else
	}
	if e := res.el(t, 0, "f-state"); e.Value != "" {
		t.Errorf("an unknown state was accepted: %q", e.Value)
	}
	if e := res.el(t, 0, "f-harness"); e.Value != "" {
		t.Errorf("an unknown harness was accepted: %q", e.Value)
	}
	if e := res.el(t, 0, "live"); strings.Contains(e.HTML, "<img") || strings.Contains(e.HTML, "onerror") {
		t.Errorf("markup built from the query: %s", e.HTML)
	}
	if last := res.Log.ReplaceState[len(res.Log.ReplaceState)-1]; len(last) > 200 {
		t.Errorf("the URL state is not bounded: %d bytes", len(last))
	}
}

// ---- JS-5 and JS-6 -----------------------------------------------------------------------------------------

func TestV2S1ScriptRelativeTimesAndMenu(t *testing.T) {
	timeEl := func(iso, text string) map[string]any {
		return withText(node("time", map[string]string{"datetime": iso}), text)
	}
	dom := listDOM()
	dom["children"] = append(dom["children"].([]map[string]any), timeEl("2026-11-21T17:47:12Z", "stale text"))
	res := runJS(t, map[string]any{"dom": dom, "narrow": true, "steps": []any{
		map[string]any{"read": []string{"menu"}},
		map[string]any{"mq": false, "read": []string{"menu"}},
		map[string]any{"advance": 30000},
		map[string]any{"hidden": true},
	}})
	if m := res.el(t, 0, "menu"); m.Open {
		t.Error("the menu is open on a narrow screen after the script ran")
	}
	if m := res.el(t, 1, "menu"); !m.Open {
		t.Error("the menu is closed on a wide screen")
	}
	var got []string
	for _, tm := range res.Times {
		got = append(got, tm.Text)
	}
	if !containsStr(got, "1 min ago") { // 17:47:12 to 17:48:35 is 83 s
		t.Errorf("times after 30 s: %v (want the stale one re-rendered as 1 min ago)", got)
	}
	if _, p, _ := res.step(t, 3); len(p) != 0 {
		t.Errorf("timers while hidden: %v", p)
	}
}

// hasDelay reports whether a timer is pending with the given remaining time. The 30 s relative-time tick (JS-5) is
// always pending while the tab is visible, so the poll timer is looked for among the pending ones.
func hasDelay(p []int64, d int64) bool {
	for _, x := range p {
		if x == d {
			return true
		}
	}
	return false
}

func containsStr(a []string, s string) bool {
	for _, x := range a {
		if x == s {
			return true
		}
	}
	return false
}

// ---- the script renders the same markup as the server -----------------------------------------------------------

func normHTML(s string) string {
	return strings.TrimSpace(regexp.MustCompile(`>\s+<`).ReplaceAllString(s, "><"))
}

func serverLive(t *testing.T, h http.Handler, target string) (live, status string, model []byte, etag string) {
	t.Helper()
	_, body, _ := v2Get(t, h, target)
	m := regexp.MustCompile(`(?s)<div id="live">\n(.*?)\n</div>\n</div>\n</section>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no live region in:\n%s", body)
	}
	s := regexp.MustCompile(`(?s)<span class="label" id="status">(.*?)</span>`).FindStringSubmatch(body)
	if s == nil {
		t.Fatal("no status region")
	}
	api := "/api/state"
	if strings.Contains(target, "archived=1") {
		api += "?archived=1"
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, api, nil))
	return normHTML(m[1]), normHTML(s[1]), rec.Body.Bytes(), rec.Header().Get("ETag")
}

func TestV2S1ScriptRendersTheMarkupTheServerRenders(t *testing.T) {
	long := func(p string) string { return p + "/extra" }
	rows := append(v2Rows(),
		Session{Harness: "opencode", ID: "ses_untitled", Root: "/srv/other", State: "idle", LastActivity: v2Now.Add(-time.Second)},
		Session{Harness: "opencode", ID: "ses_p1", Title: "p1", Root: long("/srv/one/project-alpha/services/billing"), State: "idle", LastActivity: v2Now},
		Session{Harness: "opencode", ID: "ses_p2", Title: "p2", Root: long("/srv/two/project-beta/services/billing"), State: "idle"},
		Session{Harness: "weird", ID: "x<y>", Title: "<b>t</b>", Root: "/r", State: "bogus", Guard: Guard{Held: true, State: "failed"}},
	)
	for _, tc := range []struct {
		name, target string
		src          Source
		want         string // a fragment that must be in the live region
	}{
		{"full", "/", v2Src{&fakeSource{rows: rows}}, "ses_demo0001"},
		{"archived", "/?archived=1", v2Src{&fakeSource{rows: rows}}, "Archived"},
		{"empty", "/", &fakeSource{rows: []Session{}}, "No sessions"},
		{"empty-archived", "/?archived=1", &fakeSource{rows: []Session{}}, "Hide archived"},
		{"not-connected", "/", NullSource{}, "No data source is connected."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := v2Handler(tc.src)
			live, status, model, etag := serverLive(t, h, tc.target)
			if !strings.Contains(live, tc.want) {
				t.Fatalf("server live region lacks %q: %s", tc.want, live)
			}
			var body any
			if err := json.Unmarshal(model, &body); err != nil {
				t.Fatal(err)
			}
			search := ""
			if strings.Contains(tc.target, "archived=1") {
				search = "?archived=1"
			}
			res := runJS(t, map[string]any{"dom": listDOM(), "now": v2Now.Add(-5 * time.Second).Format(time.RFC3339), "search": search, // the poll runs 5 s later, at v2Now
				"responses": []any{map[string]any{"status": 200, "body": body, "headers": map[string]string{"ETag": etag}}},
				"steps":     []any{map[string]any{"advance": 5000}}})
			if normHTML(res.Live) != live {
				t.Errorf("the script's live region differs from the server's.\nscript: %s\nserver: %s", normHTML(res.Live), live)
			}
			if tc.name != "not-connected" && normHTML(res.StatusInner) != status {
				t.Errorf("the script's status differs.\nscript: %s\nserver: %s", normHTML(res.StatusInner), status)
			}
		})
	}
}

// The JSON model must not move every second (its ETag drives the 304 of A1.7): no relative text in it.
func TestV2S1JSONModelHasNoRelativeTime(t *testing.T) {
	src := v2Src{&fakeSource{rows: v2Rows()}}
	get := func(now time.Time) (string, string) {
		h := NewHandlerOptions(src, nil, Options{Now: func() time.Time { return now }})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
		return rec.Header().Get("ETag"), rec.Body.String()
	}
	e1, b1 := get(v2Now)
	e2, b2 := get(v2Now.Add(37 * time.Second))
	if e1 != e2 || b1 != b2 {
		t.Errorf("the model changed with the clock: %s vs %s", e1, e2)
	}
	for _, want := range []string{`"harness_name":"OpenCode"`, `"state_label":"Running"`, `"state_tone":"info"`, `"guard_text":"Held, running"`,
		`"path_short":"…/services/billing"`, `"last_activity_iso":"2026-11-21T17:47:12Z"`, `"count":4`, `"count_text":"4 sessions"`, `"message":"4 sessions."`} {
		if !strings.Contains(b1, want) {
			t.Errorf("JSON model lacks %s: %s", want, b1)
		}
	}
	if strings.Contains(b1, " ago") {
		t.Error("a relative time is in the JSON model")
	}
}
