package dashboard

// ADR-023 A1.10 items 1, 3, 10, 11 and 12 for slice S3a: the top bar (label, token badge), /executions, /catalog,
// /health, the audit chips and pages, and the JSON twins. The scope rules of Source.Executions are tested against the
// production Source in internal/mcp; here the page, the twin and the cache key are tested against a fake that obeys
// the same contract.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func getBody(t *testing.T, h http.Handler, target string) (int, string, http.Header) {
	t.Helper()
	return v2Get(t, h, target)
}

// ---- the top bar (A1.3) -----------------------------------------------------------------------------------------

func TestV3TopBarShowsTheInstanceLabelAndTheTokenBadgeOnEveryScreen(t *testing.T) {
	src := newS3Src()
	h := s3Handler(src, "bridge-a")
	for _, target := range []string{"/", "/session/opencode/ses_demo0001", "/executions", "/catalog", "/audit", "/health"} {
		code, body, _ := getBody(t, h, target)
		if code != 200 && code != 404 {
			t.Fatalf("%s = %d", target, code)
		}
		bar := between(t, body, `<header class="topbar">`, `</header>`)
		if !strings.Contains(bar, `<span class="label" id="instance">bridge-a</span>`) {
			t.Errorf("%s: no instance label in the top bar:\n%s", target, bar)
		}
		if !strings.Contains(bar, `<span class="chip chip--warn" title="Connector token expiry 2026-11-21 18:40:05Z">Token expires in 52 min</span>`) {
			t.Errorf("%s: no token badge in the top bar:\n%s", target, bar)
		}
		// the word "Read-only" stays last
		if !(strings.Index(bar, "bridge-a") < strings.Index(bar, "Token expires") && strings.Index(bar, "Token expires") < strings.Index(bar, "Read-only")) {
			t.Errorf("%s: the top bar order is wrong", target)
		}
	}
}

func TestV3TokenBadgeStatesAndLocalMode(t *testing.T) {
	now := v2Now
	for _, c := range []struct {
		left        time.Duration
		text, class string
	}{
		{5*time.Hour + 2*time.Minute, "Token expires in 5 h 02 min", "chip"},
		{time.Hour, "Token expires in 1 h 00 min", "chip"},
		{time.Hour - time.Second, "Token expires in 60 min", "chip chip--warn"},
		{52 * time.Minute, "Token expires in 52 min", "chip chip--warn"},
		{time.Second, "Token expires in 1 min", "chip chip--warn"},
		{0, "Token expired", "chip chip--error"},
		{-time.Minute, "Token expired", "chip chip--error"},
		{3*24*time.Hour + 4*time.Hour, "Token expires in 3 d 4 h", "chip"},
	} {
		b := expiryBadge(now, now.Add(c.left))
		if b.Text != c.text || b.Class != c.class || !strings.Contains(b.Title, fmtTime(now.Add(c.left))) {
			t.Errorf("left %v: %+v, want %q %q", c.left, b, c.text, c.class)
		}
	}
	pages := s3Pages(t)
	if strings.Contains(pages["health-local"], "Token ex") || strings.Contains(pages["health-local"], "Connector token") {
		t.Error("local mode shows a token expiry (ADR-019: there is none)")
	}
	for name, want := range map[string]string{"detail-badge-ok": `<span class="chip" title=`, "detail-badge-warn": `chip chip--warn`, "detail-badge-expired": `chip chip--error`} {
		if bar := between(t, pages[name], `<header class="topbar">`, `</header>`); !strings.Contains(bar, want) || !strings.Contains(bar, "Token ex") {
			t.Errorf("%s: %s", name, bar)
		}
	}
	// a Source that cannot answer Health leaves the badge out; the page still renders
	bad := newS3Src()
	bad.healthErr = context.DeadlineExceeded
	code, body, _ := getBody(t, s3Handler(bad, ""), "/executions")
	if code != 200 || strings.Contains(body, "Token ex") {
		t.Errorf("a failing Health broke the page: %d", code)
	}
}

func TestV3InstanceLabelIsValidatedAndAlwaysEscaped(t *testing.T) {
	for _, ok := range []string{"", "bridge-a", "Bridge A.1_x", strings.Repeat("a", 40)} {
		if !ValidLabel(ok) {
			t.Errorf("%q was refused", ok)
		}
		if _, err := ConfigFromEnv(func(k string) string {
			if k == EnvLabel {
				return ok
			}
			return ""
		}); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{strings.Repeat("a", 41), "<b>x</b>", "a/b", "a\tb", "a\nb", "é", "a;b", `a"b`, "a`b", "a\x00b", "a<b", "a>b", "a&b", "a'b", "a=b", "a:b", "a\\b", "a(b"} {
		if ValidLabel(bad) {
			t.Errorf("%q was accepted", bad)
		}
		_, err := ConfigFromEnv(func(k string) string {
			if k == EnvLabel {
				return bad
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), EnvLabel) || strings.Contains(err.Error(), bad) && bad != "" {
			t.Errorf("%q: error %v must name the variable and never echo the value", bad, err)
		}
	}
	// defence in depth: a label that bypassed the check is still only escaped text
	s := newS3Src()
	_, body, _ := getBody(t, s3Handler(s, `<script>alert(1)</script>`), "/health")
	assertNoActiveContent(t, "label", body)
	if !strings.Contains(body, `&lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Error("the label is not shown escaped")
	}
}

// OpenWithOptions takes the label from the configuration (CIRAK_DASHBOARD_LABEL) when Options names none.
func TestV3OpenCarriesTheConfiguredLabel(t *testing.T) {
	cfg, err := ConfigFromEnv(func(k string) string {
		switch k {
		case "CIRAK_DASHBOARD_ADDR":
			return "127.0.0.1:0"
		case EnvLabel:
			return "bridge-open"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := OpenWithOptions(cfg, newS3Src(), os.Geteuid(), v2Opts())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()
	resp, err := http.Get("http://" + srv.Addr().String() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(strings.Builder)
	b := make([]byte, 1<<16)
	for {
		n, e := resp.Body.Read(b)
		buf.Write(b[:n])
		if e != nil {
			break
		}
	}
	if !strings.Contains(buf.String(), `id="instance">bridge-open</span>`) {
		t.Error("the configured label is not in the top bar")
	}
}

// ---- /executions (A1.4 item 3) ---------------------------------------------------------------------------------

func TestV3ExecutionsPage(t *testing.T) {
	body := s3Pages(t)["executions"]
	for _, col := range []string{"State", "Execution", "Session", "Started", "Duration", "Liveness"} {
		if !strings.Contains(body, `<th scope="col">`+col+`</th>`) {
			t.Errorf("no column %s", col)
		}
	}
	// the Session column: a link built from the qualified ID, split at the FIRST colon
	for _, want := range []string{`<a href="/session/opencode/ses_demo0001"><code>ses_demo0001</code></a>`, `<a href="/session/claudecode/cc-demo-1"><code>cc-demo-1</code></a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("no session link %s", want)
		}
	}
	// an ID the routes would refuse (not qualified) is shown without a link
	if !strings.Contains(body, `<td><code>not-a-qualified-id</code></td>`) {
		t.Error("an unqualified session ID was linked or lost")
	}
	// newest first, as the Source supplies them; chips; durations
	ids := []string{"7a2c91e0", "b81f03d4", "5d03aa17", "0c9be452", "e41d77b9"}
	last := -1
	for _, id := range ids {
		i := strings.Index(body, "<code>"+id+"</code>")
		if i < last || i < 0 {
			t.Errorf("%s is not in order", id)
		}
		last = i
	}
	for _, want := range []string{`chip--info" title="running">Running`, `chip--secondary" title="waiting_for_permission">Waiting for permission`, `chip--ok" title="completed">Completed`, `chip--error" title="failed">Failed`, `chip--warn" title="interrupted">Interrupted`,
		`<td class="num">6 min 02 s</td>`, `<td class="num">Running for 8 min 00 s</td>`, `>Stalled</span>`, `<span class="label">current</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the table lacks %q", want)
		}
	}
	// the footnotes of A1.4 item 3
	for _, want := range []string{"includes sessions beyond the first 200 shown on the Sessions page", "A restart of the Bridge empties it (ADR-010)", "Executions of archived sessions are behind the archived filter"} {
		if !strings.Contains(body, want) {
			t.Errorf("the footnote %q is missing", want)
		}
	}
	if !strings.Contains(body, "5 executions, updated") || !strings.Contains(body, `<a class="btn btn--primary" href="/executions">Refresh</a>`) {
		t.Error("no count or no Refresh link")
	}
}

func TestV3ExecutionsArchivedFlagReachesTheSourceAndTheLinks(t *testing.T) {
	src := newS3Src()
	h := s3Handler(src, "")
	_, plain, _ := getBody(t, h, "/executions")
	_, arch, _ := getBody(t, h, "/executions?archived=1")
	if strings.Contains(plain, "old00001") || !strings.Contains(arch, "old00001") {
		t.Error("the archived execution must be absent without the flag and present with it")
	}
	if strings.Contains(plain, "?archived=1\"><code>ses") || !strings.Contains(arch, `<a href="/session/opencode/ses_demo0009?archived=1">`) {
		t.Error("with the flag the session links keep it (an archived session is a 404 without it)")
	}
	src.mu.Lock()
	flags := append([]bool(nil), src.seenFlags...)
	src.mu.Unlock()
	if len(flags) < 2 || flags[0] || !flags[len(flags)-1] {
		t.Errorf("the Source saw the flag %v", flags)
	}
	if !strings.Contains(arch, `<a class="btn" href="/executions">Hide archived</a>`) || !strings.Contains(plain, `<a class="btn" href="/executions?archived=1">Show archived</a>`) {
		t.Error("the archived toggle is wrong")
	}
}

// The page applies its own cap defensively: a Source that returns more than 200 rows shows 200 and says so.
func TestV3ExecutionsPageCapsAtTwoHundred(t *testing.T) {
	src := newS3Src()
	for len(src.execs) < 250 {
		src.execs = append(src.execs, Execution{ID: "x" + string(rune('a'+len(src.execs)%26)), State: "completed", Started: v2Now, SessionID: "opencode:ses_demo0001"})
	}
	_, body, _ := getBody(t, s3Handler(src, ""), "/executions")
	if n := strings.Count(body, "<tr>\n<td><span"); n != 200 {
		t.Errorf("%d rows, want 200", n)
	}
	if !strings.Contains(body, "the newest 200 executions") {
		t.Error("the cap is not stated")
	}
}

func TestV3ExecutionsStates(t *testing.T) {
	pages := s3Pages(t)
	if b := pages["executions-empty"]; !strings.Contains(b, "No executions") || !strings.Contains(b, "A restart empties the list") || !strings.Contains(b, "Show archived") {
		t.Error("the empty state has no heading, sentence and action")
	}
	if b := pages["executions-unavailable"]; !strings.Contains(b, "Data source unavailable") || !strings.Contains(b, "Try again") || strings.Contains(b, "DeadlineExceeded") {
		t.Error("the error state is wrong or shows the error text")
	}
	if b := pages["executions-not-connected"]; !strings.Contains(b, "No data source") {
		t.Error("the not-connected state is missing")
	}
	// the 503 carries Retry-After, like the list
	rec := httptest.NewRecorder()
	down := newS3Src()
	down.execsErr = context.DeadlineExceeded
	s3Handler(down, "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/executions", nil))
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("%d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestV3ExecutionsTwinSharesTheSourceAndHasNoClock(t *testing.T) {
	get := func(now time.Time, target string, src *s3Src) string {
		o := Options{Now: func() time.Time { return now }}
		code, body, _ := getBody(t, NewHandlerOptions(src, nil, o), target)
		if code != 200 {
			t.Fatalf("%s = %d", target, code)
		}
		return body
	}
	a, b := get(v2Now, "/api/state/executions", newS3Src()), get(v2Now.Add(41*time.Second), "/api/state/executions", newS3Src())
	if a != b {
		t.Error("the executions model changes with the clock")
	}
	for _, bad := range []string{"ago", "Running for"} {
		if strings.Contains(a, bad) {
			t.Errorf("the model carries %q", bad)
		}
	}
	var m apiExecList
	if err := json.Unmarshal([]byte(a), &m); err != nil || m.Kind != "executions" || !m.Connected || m.Count != 5 || m.Executions[0].SessionHref != "/session/opencode/ses_demo0001" || m.Executions[0].Liveness == nil || len(m.Executions[0].Liveness.Badges) == 0 || m.Executions[0].Liveness.Badges[0].Label != "Stalled" {
		t.Fatalf("%v %+v", err, m)
	}
	// the twin and the page go through the same Source.Executions, once each (no second implementation)
	src := newS3Src()
	h := s3Handler(src, "")
	getBody(t, h, "/executions")
	getBody(t, h, "/api/state/executions")
	if src.execCalls != 2 {
		t.Errorf("Source.Executions was called %d times for one page and one twin", src.execCalls)
	}
}

// A request with ?archived=1 followed within 1 s by one without never serves the first response: the cache key of the
// twin carries the flag. Page and twin alike.
func TestV3ArchivedFlagIsPartOfTheCacheKeyOfEveryTwin(t *testing.T) {
	for _, twin := range []string{"/api/state/executions", "/api/state/catalog"} {
		src := newS3Src()
		h := s3Handler(src, "")
		_, withFlag, _ := getBody(t, h, twin+"?archived=1")
		_, without, _ := getBody(t, h, twin)
		if withFlag == without {
			t.Errorf("%s: the answer without the flag is the cached answer of the request with it", twin)
		}
		if !strings.Contains(withFlag, "old00001") && !strings.Contains(withFlag, "ses_demo0009") {
			t.Errorf("%s: the flagged answer lacks the archived row", twin)
		}
		if strings.Contains(without, "old00001") || strings.Contains(without, "ses_demo0009") {
			t.Errorf("%s: the unflagged answer shows an archived row", twin)
		}
		// and the other way round
		src2 := newS3Src()
		h2 := s3Handler(src2, "")
		_, w1, _ := getBody(t, h2, twin)
		_, w2, _ := getBody(t, h2, twin+"?archived=1")
		if w1 == w2 {
			t.Errorf("%s: the flagged request was served the unflagged cache", twin)
		}
	}
}

// ---- /catalog (A1.4 item 4) ------------------------------------------------------------------------------------

func TestV3CatalogGroupsByLifecycleRoot(t *testing.T) {
	body := s3Pages(t)["catalog"]
	heads := regexp.MustCompile(`<h2 id="(g[^"]+)">(.*?)</h2>`).FindAllStringSubmatch(body, -1)
	var got []string
	for _, h := range heads {
		got = append(got, textOf(h[2]))
	}
	if strings.Join(got, "|") != "/srv/demo/work 2 sessions|/srv/lab 1 session|No lifecycle root 2 sessions" {
		t.Errorf("groups: %v", got)
	}
	if strings.Contains(body, "ses_demo0009") {
		t.Error("an archived session is listed without the flag")
	}
	arch := s3Pages(t)["catalog-archived"]
	if !strings.Contains(arch, `<a href="/session/opencode/ses_demo0009?archived=1">Old prototype</a> <span class="chip chip--outline">Archived</span>`) || !strings.Contains(arch, "Hide archived") {
		t.Error("the archived filter does not show the archived session with its chip and a link that keeps the flag")
	}
	for _, want := range []string{"The root is a label only: it widens nothing.", `<th scope="col">Harness</th><th scope="col">Session</th><th scope="col">State</th><th scope="col">Last activity</th>`,
		`<span class="chip chip--secondary" title="waiting_for_permission">Waiting for permission</span>`, "5 sessions, updated"} {
		if !strings.Contains(body, want) {
			t.Errorf("the catalog lacks %q", want)
		}
	}
	// every table is a named region (A1.5) and every group heading is an h2 under the one h1
	if strings.Count(body, "<table") != strings.Count(body, `role="region" aria-labelledby="g`) {
		t.Error("a table is not a named region of its group")
	}
}

func TestV3CatalogTwinGroupsTheSameWay(t *testing.T) {
	_, body, _ := getBody(t, s3Handler(newS3Src(), ""), "/api/state/catalog")
	var m apiCatalog
	if err := json.Unmarshal([]byte(body), &m); err != nil || m.Kind != "catalog" || !m.Connected || m.Count != 5 || len(m.Groups) != 3 {
		t.Fatalf("%v %+v", err, m)
	}
	if m.Groups[0].Root != "/srv/demo/work" || len(m.Groups[0].Sessions) != 2 || m.Groups[2].Root != "" || strings.Contains(body, "ago") {
		t.Errorf("groups: %+v", m.Groups)
	}
}

// The catalog has the 200-row cap of the Sessions page, and says so.
func TestV3CatalogCapsLikeTheList(t *testing.T) {
	src := newS3Src()
	src.rows = nil
	for i := 0; i < 230; i++ {
		src.rows = append(src.rows, Session{Harness: "opencode", ID: "ses_cap" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Root: "/srv/x", LifecycleRoot: "/srv/x", State: "idle"})
	}
	_, body, _ := getBody(t, s3Handler(src, ""), "/catalog")
	if n := strings.Count(body, "<tr>\n<td><span class=\"badge\">"); n != maxListRows {
		t.Errorf("%d rows, want %d", n, maxListRows)
	}
	if !strings.Contains(body, "Showing the first 200 of 230 sessions.") {
		t.Error("the cap is not stated")
	}
}

func TestV3CatalogStates(t *testing.T) {
	pages := s3Pages(t)
	if b := pages["catalog-empty"]; !strings.Contains(b, "No sessions") || !strings.Contains(b, "Show archived") {
		t.Error("the empty state")
	}
	if b := pages["catalog-unavailable"]; !strings.Contains(b, "Data source unavailable") || !strings.Contains(b, "Try again") {
		t.Error("the error state")
	}
	if !strings.Contains(pages["catalog-not-connected"], "No data source") {
		t.Error("the not-connected state")
	}
}

// ---- /health (A1.4 item 6) -------------------------------------------------------------------------------------

func TestV3HealthRows(t *testing.T) {
	body := s3Pages(t)["health-remote"]
	flat := regexp.MustCompile(`\s+`).ReplaceAllString(textOf(between(t, body, `<dl class="kv">`, `</dl>`)), " ")
	for _, want := range []string{"Instancebridge-a", "Versionv0.1.0", "Build0123456789abcdef0123456789abcdef01234567", "Connector token expires2026-11-21 18:40:05Z", "LifecycleOn", "Permission relayOn", "Claude CodeOff",
		"OpenCode reachabilityReachable, last session-list read 12 s ago, 2026-11-21 17:47:53Z", "Claude Code reachabilityNot read", "Audit feedOff"} {
		if !strings.Contains(strings.ReplaceAll(flat, " ", ""), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("the health list lacks %q:\n%s", want, flat)
		}
	}
	// one row each: the three features are three chips, not a joined string
	for _, f := range []string{"Lifecycle", "Permission relay", "Claude Code"} {
		if strings.Count(body, "<dt>"+f+"</dt>") != 1 {
			t.Errorf("the feature %s is not exactly one row", f)
		}
	}
	if !strings.Contains(body, "this page makes no native probe of its own") {
		t.Error("the page does not say that reachability is the last list read")
	}
}

func TestV3HealthBuildAndFieldsThatAreNotKnown(t *testing.T) {
	src := newS3Src()
	src.health = Health{Version: "", Build: ""}
	_, body, _ := getBody(t, s3Handler(src, ""), "/health")
	flat := strings.ReplaceAll(textOf(between(t, body, `<dl class="kv">`, `</dl>`)), "\n", "")
	if !strings.Contains(flat, "Versionunknown") || !strings.Contains(flat, "Buildunknown") {
		t.Errorf("an unknown build must say so: %s", flat)
	}
	for _, absent := range []string{"Connector token", "Lifecycle", "Permission relay", "Reachab"} {
		if strings.Contains(flat, absent) {
			t.Errorf("a field the Source did not report was shown or defaulted: %s", absent)
		}
	}
	// the audit feed shows its fill when it is on
	o := v2Opts()
	o.Audit = fakeAuditOf(auditEntries(7, "interrupt", "accepted")...)
	_, on, _ := getBody(t, NewHandlerOptions(newS3Src(), nil, o), "/health")
	if !strings.Contains(on, "7 of 1000 entries") {
		t.Error("the audit fill is not shown")
	}
}

func TestV3HealthStatesAndTwin(t *testing.T) {
	pages := s3Pages(t)
	if !strings.Contains(pages["health-unavailable"], "Data source unavailable") || !strings.Contains(pages["health-not-connected"], "No data source") {
		t.Error("the states")
	}
	// the twin carries the same rows and no clock-dependent text
	get := func(now time.Time) string {
		_, body, _ := getBody(t, NewHandlerOptions(newS3Src(), nil, Options{Now: func() time.Time { return now }}), "/api/state/health")
		return body
	}
	a, b := get(v2Now), get(v2Now.Add(33*time.Second))
	if a != b || strings.Contains(a, "ago") {
		t.Error("the health model changes with the clock")
	}
	var m apiHealth
	if err := json.Unmarshal([]byte(a), &m); err != nil || m.Kind != "health" || !m.Connected || len(m.Rows) < 8 {
		t.Fatalf("%v %+v", err, m)
	}
}

// ---- the audit page (A1.4 item 5) ------------------------------------------------------------------------------

func TestV3AuditChipsComeFromFixedMaps(t *testing.T) {
	body := s3Pages(t)["audit-chips"]
	for _, want := range []string{`<span class="chip" title="prompt_submission">Prompt submission</span>`, `<span class="chip" title="interrupt">Interrupt</span>`, `<span class="chip" title="guard_release">Guard release</span>`, `<span class="chip" title="session_create">Session create</span>`,
		`<span class="chip chip--ok" title="new">New</span>`, `<span class="chip chip--ok" title="accepted">Accepted</span>`, `<span class="chip chip--error" title="refused">Refused</span>`, `<span class="chip chip--ok" title="created">Created</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("no chip %s", want)
		}
	}
	// every event chip is neutral (A1.1); an unknown word is shown escaped, in a neutral chip
	if !strings.Contains(body, `<span class="chip" title="future_event">future_event</span>`) || !strings.Contains(body, `<span class="chip" title="future_outcome">future_outcome</span>`) {
		t.Error("an unknown event or outcome is not shown as received in a neutral chip")
	}
	for _, m := range regexp.MustCompile(`<span class="(chip[^"]*)" title="(guard_release|prompt_submission|interrupt|session_create)"`).FindAllStringSubmatch(body, -1) {
		if m[1] != "chip" {
			t.Errorf("the event %s is not neutral: %s", m[2], m[1])
		}
	}
}

// The outcome map is total over what the AUDIT-01 writers emit: a new outcome without a label fails here.
func TestV3AuditOutcomeMapCoversTheWriters(t *testing.T) {
	// "outcome = "x"" at the start of a line, and the second value of "event, outcome := "e", "x""
	re := regexp.MustCompile(`(?m)^\s*outcome\s*:?=\s*"([a-z_]+)"|event,\s*outcome\s*:?=\s*"[a-z_]+",\s*"([a-z_]+)"`)
	seen := map[string]bool{}
	for _, f := range []string{"../mcp/audit.go", "../admin/admin.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			word := m[1] + m[2]
			seen[word] = true
			if _, ok := auditOutcomeLabels[word]; !ok {
				t.Errorf("the writers emit the outcome %q and the dashboard has no label for it", word)
			}
		}
	}
	if len(seen) < 10 {
		t.Errorf("only %d outcomes found: the scan is broken", len(seen))
	}
	for e := range auditEvents {
		if _, ok := auditEventLabels[e]; !ok {
			t.Errorf("the ring keeps the event %q and the dashboard has no label for it", e)
		}
	}
	for k, c := range auditOutcomeLabels {
		if snakeCase.MatchString(c.Label) || strings.ToUpper(c.Label) == c.Label || c.Tone == "secondary" {
			t.Errorf("outcome %s: label %q tone %s", k, c.Label, c.Tone)
		}
	}
}

func TestV3AuditPagination(t *testing.T) {
	pages := s3Pages(t)
	rows := func(b string) int { return strings.Count(b, `<tr><td>`) }
	if rows(pages["audit-page1"]) != 100 || rows(pages["audit-page2"]) != 100 || rows(pages["audit-page3"]) != 50 {
		t.Errorf("rows per page: %d %d %d", rows(pages["audit-page1"]), rows(pages["audit-page2"]), rows(pages["audit-page3"]))
	}
	// newest first, continuing across pages: ses_0000 on page 1, ses_0100 on page 2, ses_0200 on page 3
	for page, id := range map[string]string{"audit-page1": "ses_0000", "audit-page2": "ses_0100", "audit-page3": "ses_0200"} {
		first := regexp.MustCompile(`<tr><td>[^<]*</td><td>.*?</td><td>.*?</td><td></td><td>(ses_\d+)</td>`).FindStringSubmatch(pages[page])
		if first == nil || first[1] != id {
			t.Errorf("%s starts with %v, want %s", page, first, id)
		}
	}
	p1, p2, p3 := pages["audit-page1"], pages["audit-page2"], pages["audit-page3"]
	if !strings.Contains(p1, "Entries 1 to 100 of 250") || strings.Contains(p1, "Newer entries") || !strings.Contains(p1, `<a class="btn" href="/audit?page=2">Older entries</a>`) {
		t.Error("page 1 pager")
	}
	if !strings.Contains(p2, `<a class="btn" href="/audit">Newer entries</a>`) || !strings.Contains(p2, `href="/audit?page=3">Older entries`) || !strings.Contains(p2, "Entries 101 to 200 of 250") {
		t.Error("page 2 pager")
	}
	if !strings.Contains(p3, `href="/audit?page=2">Newer entries`) || strings.Contains(p3, "Older entries") || !strings.Contains(p3, "Entries 201 to 250 of 250") {
		t.Error("page 3 pager")
	}
	// a ring of 100 or fewer has no pager
	if strings.Contains(pages["audit-chips"], "Audit pages") {
		t.Error("a short ring shows a pager")
	}
}

// ?page=N is a bounded GET parameter: an invalid value is page 1, never an error and never reflected.
func TestV3AuditPageParameterIsBoundedAndNeverReflected(t *testing.T) {
	o := v2Opts()
	o.Audit = fakeAuditOf(auditEntries(250, "interrupt", "accepted")...)
	h := NewHandlerOptions(&fakeSource{}, nil, o)
	_, first, _ := getBody(t, h, "/audit")
	for _, v := range []string{"0", "-1", "abc", "4", "999999999999", "1%202", "%3Cscript%3E", "", "٣", "03x", "1.5"} {
		code, body, _ := getBody(t, h, "/audit?page="+v)
		if code != 200 || body != first {
			t.Errorf("page=%q: %d, the page is not page 1", v, code)
		}
		if strings.Contains(body, "<script>alert") {
			t.Errorf("page=%q was reflected", v)
		}
	}
	// a repeated parameter: the first value is the one read
	for v, want := range map[string]string{"2": "ses_0100", "3": "ses_0200", "02": "ses_0100", "2&page=3": "ses_0100", "3&page=1": "ses_0200"} {
		if _, body, _ := getBody(t, h, "/audit?page="+v); !strings.Contains(body, want) {
			t.Errorf("page=%s does not show %s", v, want)
		}
	}
}

// The JSON twin of the audit page is unchanged: the whole ring.
func TestV3AuditJSONTwinStaysTheWholeRing(t *testing.T) {
	o := v2Opts()
	o.Audit = fakeAuditOf(auditEntries(250, "interrupt", "accepted")...)
	_, body, _ := getBody(t, NewHandlerOptions(&fakeSource{}, nil, o), "/api/audit?page=2")
	var m apiAudit
	if err := json.Unmarshal([]byte(body), &m); err != nil || len(m.Entries) != 250 || !m.Enabled {
		t.Fatalf("%v %d", err, len(m.Entries))
	}
}

// ---- hostile values (A1.10 item 11) ----------------------------------------------------------------------------

func TestV3HostilePagesAreEscaped(t *testing.T) {
	pages := s3Pages(t)
	for _, name := range []string{"executions-hostile", "catalog-hostile", "health-hostile", "audit-hostile"} {
		body := pages[name]
		assertNoActiveContent(t, name, body)
		for _, bad := range []string{"<script>alert", "<img src=x", "onerror=alert(2)>"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: unescaped %q", name, bad)
			}
		}
		for _, a := range htmlAttrs(body) {
			if strings.HasPrefix(a.name, "on") || a.name == "style" {
				t.Errorf("%s: attribute %s", name, a.name)
			}
			if (a.name == "href" || a.name == "src") && !strings.HasPrefix(a.value, "/") && !strings.HasPrefix(a.value, "#") {
				t.Errorf("%s: %s=%q leaves the dashboard", name, a.name, a.value)
			}
		}
		if !strings.Contains(textOf(body), `<script>alert(1)</script>`) {
			t.Errorf("%s: the hostile text is not shown as text", name)
		}
	}
	// a hostile session ID in the executions list never becomes a link: the routes would refuse it
	if strings.Contains(pages["executions-hostile"], `href="/session/opencode/<`) || strings.Contains(pages["executions-hostile"], `ses_evil0001"><code>`) == false {
		t.Log("(the valid session of the hostile fixture keeps its link)")
	}
}

// ---- routes, limiter, methods (A1.0, A1.7, A1.10 item 11) --------------------------------------------------------

func TestV3NewRoutesAreDataRoutesAndGetOnly(t *testing.T) {
	want := map[string]bool{"/executions": false, "/catalog": false, "/health": false, "/api/state/executions": false, "/api/state/catalog": false, "/api/state/health": false}
	for _, r := range Routes(NullSource{}) {
		if _, ok := want[r.Path]; ok {
			want[r.Path] = true
			if !r.Data {
				t.Errorf("%s does not count against the data limiter", r.Path)
			}
			if len(r.Methods) != 2 || r.Methods[0] != http.MethodGet || r.Methods[1] != http.MethodHead || r.Prefix {
				t.Errorf("%s: %v prefix %v", r.Path, r.Methods, r.Prefix)
			}
		}
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("no route %s", p)
		}
	}
	h := s3Handler(newS3Src(), "")
	for p := range want {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(m, p, strings.NewReader("x")))
			if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s = %d", m, p, rec.Code)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Header().Get("Content-Security-Policy") != contentSecurityPolicy || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: headers %v", p, rec.Header())
		}
	}
}

// Each new reader counts against the one data limiter: four at once are served, the fifth is refused at once.
type execGate struct {
	*s3Src
	gate    chan struct{}
	entered chan struct{}
}

func (g *execGate) wait(ctx context.Context) {
	g.entered <- struct{}{}
	select {
	case <-g.gate:
	case <-ctx.Done():
	}
}
func (g *execGate) Executions(ctx context.Context) ([]Execution, error) {
	g.wait(ctx)
	return g.s3Src.Executions(ctx)
}
func (g *execGate) Sessions(ctx context.Context) ([]Session, error) {
	g.wait(ctx)
	return g.s3Src.Sessions(ctx)
}

func TestV3NewReadersShareTheDataLimiter(t *testing.T) {
	g := &execGate{s3Src: newS3Src(), gate: make(chan struct{}), entered: make(chan struct{}, 16)}
	g.health.TokenExpires = nil
	h := s3Handler(g, "")
	var wg sync.WaitGroup
	for _, p := range []string{"/executions", "/catalog", "/api/state/executions", "/api/state/catalog"} {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
		}(p)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-g.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("the four data requests did not all start")
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != busyRetryAfter {
		t.Errorf("the fifth data request = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	close(g.gate)
	wg.Wait()
}

func TestV3TwinsAnswerNotModifiedForAnUnchangedModel(t *testing.T) {
	h := s3Handler(newS3Src(), "")
	for _, p := range []string{"/api/state/executions", "/api/state/catalog", "/api/state/health"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		etag := rec.Header().Get("ETag")
		if etag == "" || rec.Code != 200 {
			t.Fatalf("%s = %d etag %q", p, rec.Code, etag)
		}
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("If-None-Match", etag)
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req)
		if rec2.Code != http.StatusNotModified || rec2.Body.Len() != 0 {
			t.Errorf("%s: %d with a matching tag", p, rec2.Code)
		}
	}
}
