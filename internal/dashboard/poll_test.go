package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func newGet(target string) *http.Request { return httptest.NewRequest(http.MethodGet, target, nil) }

func serveReq(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func detailSource() *fakeSource {
	d := session("opencode", sesID)
	d.Guard = Guard{Held: true, State: "running"}
	end := t0.Add(time.Minute)
	d.Executions = []Execution{{ID: "exec_1", State: "completed", Started: t0, Updated: t0, Ended: &end}, {ID: "exec_2", State: "running", Started: t0, Updated: t0, Current: true}}
	return &fakeSource{rows: sampleRows(), details: map[string]SessionDetail{"opencode/" + sesID: d}}
}

func TestStateListJSONShapeAndEscaping(t *testing.T) {
	src := &fakeSource{rows: sampleRows()}
	src.rows[0].Title = `<script>alert(1)</script>`
	h := NewHandler(src, nil)
	rec := serve(h, http.MethodGet, "/api/state", "")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("GET /api/state = %d %v", rec.Code, rec.Header())
	}
	checkSecurityHeaders(t, "GET /api/state", rec.Header())
	var m apiList
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Kind != "list" || !m.Connected || m.Message != "2 sessions." || len(m.Sessions) != 2 {
		t.Fatalf("%+v", m)
	}
	s := m.Sessions[0]
	if s.Harness != "opencode" || s.ID != "ses_aaa" || s.State != "running" || !s.Guard.Held || s.Guard.State != "running" || s.Current != "exec_1" || s.Href != "/session/opencode/ses_aaa" || s.Title != `<script>alert(1)</script>` {
		t.Errorf("%+v", s)
	}
	if strings.Contains(rec.Body.String(), "<script") {
		t.Errorf("a raw <script reached the JSON body: %s", rec.Body.String())
	}
	// The same normalization as the HTML: an unknown state is "unknown", never echoed.
	src.rows[1].State = "<b>x</b>"
	rec = serve(NewHandler(src, nil), http.MethodGet, "/api/state", "")
	if strings.Contains(rec.Body.String(), "<b>") || !strings.Contains(rec.Body.String(), `"state":"unknown"`) {
		t.Errorf("state not normalized: %s", rec.Body.String())
	}
	// NullSource: connected false.
	rec = serve(NewHandler(NullSource{}, nil), http.MethodGet, "/api/state", "")
	if !strings.Contains(rec.Body.String(), `"connected":false`) || !strings.Contains(rec.Body.String(), `"sessions":[]`) {
		t.Errorf("null source: %s", rec.Body.String())
	}
}

func TestStateDetailJSON(t *testing.T) {
	h := NewHandler(detailSource(), nil)
	rec := serve(h, http.MethodGet, "/api/state/session/opencode/"+sesID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var m apiDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Kind != "detail" || m.Session.ID != sesID || !m.Session.Guard.Held || len(m.Executions) != 2 || !m.Executions[1].Current || m.Executions[0].Ended == "-" || m.Executions[1].Ended != "-" {
		t.Fatalf("%+v", m)
	}
	// No diff text in the polling model: diffs have their own pages.
	if strings.Contains(rec.Body.String(), "patch") {
		t.Errorf("the detail model carries diff content: %s", rec.Body.String())
	}
}

func TestStateConditionalRequests(t *testing.T) {
	for _, target := range []string{"/api/state", "/api/state/session/opencode/" + sesID, "/api/audit"} {
		h := NewHandlerOptions(detailSource(), nil, Options{Audit: &fakeAudit{entries: sampleAudit()}})
		first := serveReq(h, newGet(target))
		etag, lm := first.Header().Get("ETag"), first.Header().Get("Last-Modified")
		if first.Code != 200 || !regexp.MustCompile(`^"[0-9a-f]{32}"$`).MatchString(etag) || lm == "" {
			t.Fatalf("%s first = %d etag=%q lm=%q", target, first.Code, etag, lm)
		}
		cond := func(k, v string) *httptest.ResponseRecorder {
			r := newGet(target)
			r.Header.Set(k, v)
			return serveReq(h, r)
		}
		for name, hv := range map[string]string{"exact": etag, "weak": "W/" + etag, "star": "*", "in a list": `"nope", ` + etag} {
			rec := cond("If-None-Match", hv)
			if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag || rec.Header().Get("Last-Modified") != lm {
				t.Errorf("%s %s: %d body=%q hdr=%v", target, name, rec.Code, rec.Body.String(), rec.Header())
			}
			checkSecurityHeaders(t, target+" 304", rec.Header())
		}
		if rec := cond("If-None-Match", `"0000"`); rec.Code != 200 || rec.Body.Len() == 0 {
			t.Errorf("%s mismatching ETag = %d", target, rec.Code)
		}
		if rec := cond("If-None-Match", strings.Repeat("a", maxINMHeader+1)); rec.Code != 200 {
			t.Errorf("%s an oversize header = %d", target, rec.Code)
		}
		if rec := cond("If-Modified-Since", lm); rec.Code != http.StatusNotModified {
			t.Errorf("%s If-Modified-Since = %d", target, rec.Code)
		}
		old := time.Unix(0, 0).UTC().Format(http.TimeFormat)
		if rec := cond("If-Modified-Since", old); rec.Code != 200 {
			t.Errorf("%s old If-Modified-Since = %d", target, rec.Code)
		}
		if rec := cond("If-Modified-Since", "garbage"); rec.Code != 200 {
			t.Errorf("%s bad If-Modified-Since = %d", target, rec.Code)
		}
		// If-None-Match wins over If-Modified-Since.
		r := newGet(target)
		r.Header.Set("If-None-Match", `"0000"`)
		r.Header.Set("If-Modified-Since", lm)
		if rec := serveReq(h, r); rec.Code != 200 {
			t.Errorf("%s INM mismatch must not be overridden by IMS: %d", target, rec.Code)
		}
	}
}

// The view model is cached for one second, and the ETag follows the model.
func TestStateModelCacheOneSecondAndETagFollowsTheModel(t *testing.T) {
	ck := &clock{t: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)}
	src := &fakeSource{rows: sampleRows()}
	h := NewHandlerOptions(src, nil, Options{Now: ck.now})
	get := func() *httptest.ResponseRecorder { return serveReq(h, newGet("/api/state")) }
	a := get()
	ck.add(900 * time.Millisecond)
	src.mu.Lock()
	src.rows = append([]Session{}, src.rows...)
	src.rows[0].State = "idle" // the model changes at the source...
	src.mu.Unlock()
	b := get()
	if src.listCalls != 1 || b.Header().Get("ETag") != a.Header().Get("ETag") {
		t.Fatalf("inside one second the model must come from the cache: calls=%d", src.listCalls)
	}
	ck.add(200 * time.Millisecond) // 1.1 s after the first build
	c := get()
	if src.listCalls != 2 {
		t.Fatalf("after the TTL the model must be rebuilt: calls=%d", src.listCalls)
	}
	if c.Header().Get("ETag") == a.Header().Get("ETag") || !strings.Contains(c.Body.String(), `"state":"idle"`) {
		t.Fatalf("a changed model kept its ETag %s", c.Header().Get("ETag"))
	}
	if c.Header().Get("Last-Modified") == a.Header().Get("Last-Modified") {
		t.Errorf("Last-Modified did not move with the model: %s", c.Header().Get("Last-Modified"))
	}
	// A rebuild of an unchanged model keeps the ETag and the Last-Modified.
	ck.add(1100 * time.Millisecond)
	d := get()
	if src.listCalls != 3 || d.Header().Get("ETag") != c.Header().Get("ETag") || d.Header().Get("Last-Modified") != c.Header().Get("Last-Modified") {
		t.Fatalf("an unchanged model changed its validators: calls=%d %s/%s vs %s/%s", src.listCalls, d.Header().Get("ETag"), d.Header().Get("Last-Modified"), c.Header().Get("ETag"), c.Header().Get("Last-Modified"))
	}
	if get().Code != 200 || src.listCalls != 3 {
		t.Errorf("a second request inside the TTL must not read the source again")
	}
}

// Single-flight: concurrent polls of one key share one read.
func TestStateModelIsSingleFlight(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 8)
	src := &gateSource{fakeSource: &fakeSource{rows: sampleRows()}, gate: gate, entered: entered}
	h := NewHandler(src, nil)
	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := range codes {
		wg.Add(1)
		go func(i int) { defer wg.Done(); codes[i] = serveReq(h, newGet("/api/state")).Code }(i)
	}
	<-entered
	time.Sleep(50 * time.Millisecond) // let the other two reach the shared read
	close(gate)
	wg.Wait()
	if src.listCalls != 1 {
		t.Fatalf("%d source reads for 3 concurrent polls, want 1", src.listCalls)
	}
	for _, c := range codes {
		if c != 200 {
			t.Fatalf("codes %v", codes)
		}
	}
}

// The build is detached from the caller, so one client that goes away cannot hand its
// cancellation to the polls that share its read.
func TestStateBuildIsNotCancelledByTheFirstCaller(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{}, 8)
	src := &gateSource{fakeSource: &fakeSource{rows: sampleRows()}, gate: gate, entered: entered, ignoreCancel: true}
	h := NewHandler(src, nil)
	ctx1, cancel1 := newCancelable()
	first := make(chan int, 1)
	go func() {
		r := newGet("/api/state").WithContext(ctx1)
		first <- serveReq(h, r).Code
	}()
	<-entered
	second := make(chan int, 1)
	go func() { second <- serveReq(h, newGet("/api/state")).Code }()
	time.Sleep(50 * time.Millisecond)
	cancel1() // the first client disconnects
	time.Sleep(50 * time.Millisecond)
	close(gate)
	if c := <-second; c != 200 {
		t.Fatalf("the second poll got %d after the first client left", c)
	}
	<-first
}

func TestStateErrors(t *testing.T) {
	failing := NewHandler(&fakeSource{rowsErr: errors.New("boom <b>"), detailErr: errors.New("boom <b>")}, nil)
	for _, target := range []string{"/api/state", "/api/state/session/opencode/ses_aaa"} {
		rec := serveReq(failing, newGet(target))
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" || strings.Contains(rec.Body.String(), "boom") || !strings.Contains(rec.Body.String(), "data source unavailable") {
			t.Errorf("%s failing = %d %v %s", target, rec.Code, rec.Header(), rec.Body.String())
		}
		checkSecurityHeaders(t, target+" 503", rec.Header())
	}
	src := detailSource()
	h := NewHandler(src, nil)
	for _, target := range []string{
		"/api/state/session/opencode/ses_unknown", "/api/state/session/other/" + sesID, "/api/state/session/opencode",
		"/api/state/session/opencode/../etc", "/api/state/session/opencode/" + sesID + "/x", "/api/state/session/", "/api/state/session/opencode/%2e%2e",
	} {
		rec := serveReq(h, newGet(target))
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "ses_unknown") || !strings.Contains(rec.Body.String(), "session not found") {
			t.Errorf("%s = %d %s", target, rec.Code, rec.Body.String())
		}
	}
	// A source that answers for another session is never rendered as the requested one.
	other := session("opencode", "ses_other")
	src.override = &other
	if rec := serveReq(NewHandler(src, nil), newGet("/api/state/session/opencode/"+sesID)); rec.Code != http.StatusNotFound {
		t.Errorf("a mismatched detail = %d", rec.Code)
	}
}

func TestStateHeadHasHeadersAndNoBody(t *testing.T) {
	h := NewHandler(detailSource(), nil)
	for _, target := range []string{"/api/state", "/api/state/session/opencode/" + sesID} {
		get := serveReq(h, newGet(target))
		req := httptest.NewRequest(http.MethodHead, target, nil)
		head := serveReq(h, req)
		if head.Code != 200 || head.Header().Get("ETag") != get.Header().Get("ETag") || head.Body.Len() != 0 {
			t.Errorf("HEAD %s = %d etag=%q body=%d", target, head.Code, head.Header().Get("ETag"), head.Body.Len())
		}
	}
}

func TestNewRoutesAreGetAndHeadOnly(t *testing.T) {
	want := map[string]bool{"/": false, "/session/": false, "/api/state": false, "/api/state/session/": false, "/audit": false, "/api/audit": false, "/dashboard.css": false, "/dashboard.js": false}
	for _, r := range Routes(NullSource{}) {
		if _, ok := want[r.Path]; !ok {
			t.Errorf("unexpected route %s", r.Path)
		}
		want[r.Path] = true
		if len(r.Methods) != 2 || r.Methods[0] != http.MethodGet || r.Methods[1] != http.MethodHead {
			t.Errorf("route %s methods %v", r.Path, r.Methods)
		}
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("route %s missing", p)
		}
	}
	h := NewHandlerOptions(NullSource{}, nil, Options{Audit: NewAuditRing()})
	for _, p := range []string{"/api/state", "/api/state/session/opencode/ses_x", "/audit", "/api/audit"} {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
			if rec := serve(h, m, p, ""); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s = %d allow=%q", m, p, rec.Code, rec.Header().Get("Allow"))
			}
		}
	}
}

// The page script: no markup from data, no evaluation, GET only, one fetch, same origin,
// the documented intervals.
func TestPollingScriptIsSafeAndUsesTheDocumentedIntervals(t *testing.T) {
	b, err := os.ReadFile("assets/dashboard.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, banned := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "XMLHttpRequest", "WebSocket", "EventSource", "sendBeacon", "importScripts", "localStorage", "sessionStorage", "document.cookie", "http://", "https://", "'POST'", "'PUT'", "'PATCH'", "'DELETE'", `"POST"`, "srcdoc", "javascript:"} {
		if strings.Contains(js, banned) {
			t.Errorf("dashboard.js contains %q", banned)
		}
	}
	if n := strings.Count(js, "fetch("); n != 1 {
		t.Errorf("%d fetch calls, want 1", n)
	}
	if !strings.Contains(js, "method: 'GET'") || !strings.Contains(js, "'If-None-Match'") || !strings.Contains(js, "r.status === 304") {
		t.Error("the script does not poll with GET and If-None-Match")
	}
	for _, want := range []string{"visibleMs = 5000; hiddenMs = 5000", "visibleMs = 2000; hiddenMs = 10000", "document.hidden", "'/api/state'", "'/api/state/session/'"} {
		if !strings.Contains(js, want) {
			t.Errorf("dashboard.js lacks %q", want)
		}
	}
	if strings.Count(js, "setTimeout(") != 1 || strings.Contains(js, "setInterval") {
		t.Error("the script must use one self-rescheduling timer")
	}
}

func TestPagesCarryThePollingHooks(t *testing.T) {
	src := diffSource(t)
	src.rows = sampleRows()
	h := NewHandler(src, nil)
	_, list, _ := fetch(h, "/")
	if !strings.Contains(list, `data-poll="list"`) || !strings.Contains(list, `id="live"`) {
		t.Errorf("list page lacks its polling hooks")
	}
	_, detail, _ := fetch(h, "/session/opencode/"+sesID)
	if !strings.Contains(detail, `data-poll="detail" data-harness="opencode" data-session="`+sesID+`"`) {
		t.Errorf("detail page lacks its polling hooks")
	}
	_, turn, _ := fetch(h, "/session/opencode/"+sesID+"/turn/"+turnX)
	if strings.Contains(turn, "data-poll") {
		t.Errorf("a turn page polls")
	}
	for _, body := range []string{list, detail, turn} {
		if strings.Count(body, "<script") != 1 || !strings.Contains(body, `<script src="/dashboard.js"></script>`) {
			t.Errorf("a page does not carry exactly the one embedded script")
		}
	}
}

// Review #243 P3-2: the escaped page can exceed the 2 MiB bound; it is degraded with a
// written notice, never a 500.
func TestPageOverTheBoundAfterEscapingIsDegradedNotAnError(t *testing.T) {
	src := diffSource(t)
	var files []DiffFile
	hunk := "@@ -1 +1 @@\n" + strings.Repeat("<", maxPatchPerFile-20) // 4 bytes each once escaped
	for i := 0; i < 4; i++ {
		files = append(files, DiffFile{Path: fmt.Sprintf("esc%d.txt", i), HasPath: true, Patch: hunk, HasPatch: true, Additions: 1, Status: "modified"})
	}
	src.workDiff = DiffResult{Files: files}
	src.turnDiffs[turnX] = src.workDiff
	h := NewHandler(src, nil)
	for _, target := range []string{"/session/opencode/" + sesID, "/session/opencode/" + sesID + "/turn/" + turnX} {
		code, body, hdr := fetch(h, target)
		if code != http.StatusOK || len(body) > maxBodyBytes {
			t.Fatalf("%s = %d (%d bytes)", target, code, len(body))
		}
		checkSecurityHeaders(t, target, hdr)
		hidden := strings.Count(body, patchHiddenNote)
		shown := strings.Count(body, "<pre>")
		if hidden == 0 || shown == 0 || hidden+shown != 4 {
			t.Errorf("%s: %d patches shown, %d hidden: the page must keep what fits and say what it hid", target, shown, hidden)
		}
		for i := 0; i < 4; i++ {
			if !strings.Contains(body, fmt.Sprintf("esc%d.txt", i)) {
				t.Errorf("%s: file esc%d.txt is not listed", target, i)
			}
		}
		assertNoActiveContent(t, target, body)
	}
	// Content that does not expand is untouched: no note, every patch shown.
	src2 := diffSource(t)
	_, body, _ := fetch(NewHandler(src2, nil), "/session/opencode/"+sesID)
	if strings.Contains(body, patchHiddenNote) {
		t.Error("a normal page was degraded")
	}
}

// When even the budgeted pass does not fit (extreme chrome), every patch is hidden and
// the page is still served.
func TestPageDegradesToNoPatchesBeforeFailing(t *testing.T) {
	p := page{Title: "t", Diff: &diffSection{Working: &diffView{Label: LabelChanges, Scope: "s", Files: []fileView{
		{Path: "a", Patch: strings.Repeat("<", 900<<10), ShowPatch: true},
		{Path: "b", Patch: strings.Repeat("&", 900<<10), ShowPatch: true},
	}}}}
	rec := httptest.NewRecorder()
	renderPage(rec, http.StatusOK, p)
	if rec.Code != http.StatusOK || rec.Body.Len() > maxBodyBytes || !strings.Contains(rec.Body.String(), patchHiddenNote) {
		t.Fatalf("%d %d", rec.Code, rec.Body.Len())
	}
	// A page with no patch to hide that is still over the bound is refused, not truncated.
	big := page{Title: "t", Message: strings.Repeat("<", maxBodyBytes)}
	rec = httptest.NewRecorder()
	renderPage(rec, http.StatusOK, big)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("an oversize page without patches = %d", rec.Code)
	}
}

// Review #243 P3-3: the turn list of the detail page pins each label (a not-computed
// turn is never rendered as "no changes").
func TestDetailTurnListLabelsArePinned(t *testing.T) {
	_, body, _ := fetch(NewHandler(diffSource(t), nil), "/session/opencode/"+sesID)
	row := func(id string) string {
		m := regexp.MustCompile(`<tr><td>(?:<a[^>]*>)?` + regexp.QuoteMeta(id) + `(?:</a>)?</td><td>[^<]*</td><td>([^<]*)</td></tr>`).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no turn row for %s in\n%s", id, body)
		}
		return m[1]
	}
	for id, want := range map[string]string{
		"msg_user_notcomputed": LabelNotComputed,
		"msg_user_nodiffskey":  LabelNotComputed,
		"msg_user_empty":       LabelComputedEmpty,
		turnX:                  LabelChanges,
	} {
		if got := row(id); got != want {
			t.Errorf("turn %s is labelled %q, want %q", id, got, want)
		}
	}
	if LabelNotComputed == LabelComputedEmpty || strings.Contains(strings.ToLower(LabelNotComputed), "no changes") {
		t.Error("the not-computed label reads as no changes")
	}
}
