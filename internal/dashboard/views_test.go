package dashboard

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSource is an in-memory read-only Source that records how it was asked.
type fakeSource struct {
	mu           sync.Mutex // guards the counters: requests run concurrently in some tests
	rows         []Session
	rowsErr      error
	details      map[string]SessionDetail // key harness/id
	detailErr    error
	detailCalls  int
	listCalls    int
	lastDeadline bool
	override     *SessionDetail // returned whatever was asked (a misbehaving source)

	// Diff reads (slice 3).
	turns         []Turn
	turnsErr      error
	turnDiffs     map[string]DiffResult
	turnDiffErr   error
	workDiff      DiffResult
	workErr       error
	turnsCalls    int
	turnDiffCall  int
	workCalls     int
	block         func(ctx context.Context) // a slow native read: runs first in every diff read
	blockTurnDiff func(ctx context.Context) // a slow per-turn read only
}

func (f *fakeSource) SessionTurns(ctx context.Context, harness, id string) ([]Turn, error) {
	f.mu.Lock()
	f.turnsCalls++
	f.mu.Unlock()
	if f.block != nil {
		f.block(ctx)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return f.turns, f.turnsErr
}

func (f *fakeSource) TurnDiff(ctx context.Context, harness, id, messageID string) (DiffResult, error) {
	f.mu.Lock()
	f.turnDiffCall++
	f.mu.Unlock()
	if f.blockTurnDiff != nil {
		f.blockTurnDiff(ctx)
	}
	if f.block != nil {
		f.block(ctx)
		if err := ctx.Err(); err != nil {
			return DiffResult{}, err
		}
	}
	if f.turnDiffErr != nil {
		return DiffResult{}, f.turnDiffErr
	}
	r, ok := f.turnDiffs[messageID]
	if !ok {
		return DiffResult{}, ErrNotFound
	}
	return r, nil
}

func (f *fakeSource) WorkingTreeDiff(ctx context.Context, harness, id string) (DiffResult, error) {
	f.mu.Lock()
	f.workCalls++
	f.mu.Unlock()
	if f.block != nil {
		f.block(ctx)
		if err := ctx.Err(); err != nil {
			return DiffResult{}, err
		}
	}
	return f.workDiff, f.workErr
}

func (f *fakeSource) Sessions(ctx context.Context) ([]Session, error) {
	f.mu.Lock()
	f.listCalls++
	_, f.lastDeadline = ctx.Deadline()
	f.mu.Unlock()
	return f.rows, f.rowsErr
}

func (f *fakeSource) SessionDetail(ctx context.Context, harness, id string) (SessionDetail, error) {
	f.mu.Lock()
	f.detailCalls++
	_, f.lastDeadline = ctx.Deadline()
	f.mu.Unlock()
	if f.detailErr != nil {
		return SessionDetail{}, f.detailErr
	}
	if f.override != nil {
		return *f.override, nil
	}
	d, ok := f.details[harness+"/"+id]
	if !ok {
		return SessionDetail{}, ErrNotFound
	}
	return d, nil
}

var t0 = time.Date(2026, 11, 12, 12, 4, 11, 0, time.UTC)

func sampleRows() []Session {
	return []Session{
		{Harness: "opencode", ID: "ses_aaa", Title: "fix the build", Root: "/opt/proj-x", State: "running",
			Guard: Guard{Held: true, State: "running"}, CurrentExecution: "exec_1", LastActivity: t0},
		{Harness: "claudecode", ID: "4e59c1bd-4333-4a71-875d-cba9301bd4a9", Title: "review", Root: "/opt/proj-y", State: "idle", LastActivity: t0},
	}
}

func fetch(h http.Handler, target string) (int, string, http.Header) {
	rec := serve(h, http.MethodGet, target, "")
	return rec.Code, rec.Body.String(), rec.Header()
}

func TestListRendersRowsWithBadgeLinkGuardAndState(t *testing.T) {
	src := &fakeSource{rows: sampleRows()}
	code, body, hdr := fetch(NewHandler(src, nil), "/")
	if code != http.StatusOK {
		t.Fatalf("GET / = %d", code)
	}
	checkSecurityHeaders(t, "GET /", hdr)
	for _, want := range []string{
		`class="badge harness-opencode">opencode<`, `class="badge harness-claudecode">claudecode<`,
		`href="/session/opencode/ses_aaa"`, `href="/session/claudecode/4e59c1bd-4333-4a71-875d-cba9301bd4a9"`,
		"fix the build", "/opt/proj-x", "held (running)", "exec_1", "2026-11-12 12:04:11Z", "2 sessions.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("list lacks %q:\n%s", want, body)
		}
	}
	if !src.lastDeadline {
		t.Error("the Source context carries no deadline (5 s bound)")
	}
}

func TestStateValuesAreNormalised(t *testing.T) {
	rows := []Session{{Harness: "opencode", ID: "ses_x", State: "completed", Guard: Guard{Held: true, State: "<b>x</b>"}}}
	_, body, _ := fetch(NewHandler(&fakeSource{rows: rows}, nil), "/")
	if strings.Contains(body, "<b>") {
		t.Errorf("a guard state is rendered raw: %s", body)
	}
	if !strings.Contains(body, "<td>unknown</td>") {
		t.Errorf("an execution state shown as a session state must read unknown: %s", body)
	}
	if !strings.Contains(body, "held (unknown)") {
		t.Errorf("an unrecognised guard state must read unknown: %s", body)
	}
}

func TestEmptyAndFailingSources(t *testing.T) {
	code, body, _ := fetch(NewHandler(&fakeSource{rows: []Session{}}, nil), "/")
	if code != http.StatusOK || !strings.Contains(body, "0 sessions.") || strings.Contains(body, "<table") {
		t.Errorf("empty list = %d %s", code, body)
	}
	// Review #223 P3-1: a failing data source is 503 with Retry-After for the list as
	// for the detail (one semantics); a configured-absent source is a 200 page.
	code, body, hdr0 := fetch(NewHandler(&fakeSource{rowsErr: errors.New("boom <b>")}, nil), "/")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "Data source unavailable.") || strings.Contains(body, "boom") || hdr0.Get("Retry-After") == "" {
		t.Errorf("failing list = %d %s", code, body)
	}
	checkSecurityHeaders(t, "503 list", hdr0)
	code, body, hdr := fetch(NewHandler(&fakeSource{detailErr: errors.New("boom <b>")}, nil), "/session/opencode/ses_aaa")
	if code != http.StatusServiceUnavailable || strings.Contains(body, "boom") {
		t.Errorf("failing detail = %d %s", code, body)
	}
	checkSecurityHeaders(t, "503", hdr)
	code, body, _ = fetch(NewHandler(NullSource{}, nil), "/")
	if code != http.StatusOK || !strings.Contains(body, "No data source is connected.") {
		t.Errorf("null source = %d %s", code, body)
	}
}

// XSS fixtures (ADR-023 T1): everything the Source supplies is escaped.
func hostile() (title, root, id string) {
	return `<script>alert(1)</script>`, `"><img src=x onerror=alert(1)>`, `</title><script>alert(2)</script>`
}

func assertNoActiveContent(t *testing.T, what, body string) {
	t.Helper()
	// The page itself has exactly one script element: the embedded asset.
	if n := strings.Count(strings.ToLower(body), "<script"); n != 1 {
		t.Errorf("%s: %d <script elements, want exactly the one embedded asset:\n%s", what, n, body)
	}
	for _, bad := range []string{`"><img`, "<img", "</title><script", "<b>"} {
		if strings.Contains(body, bad) {
			t.Errorf("%s: raw %q in output:\n%s", what, bad, body)
		}
	}
}

func TestListEscapesHostileValues(t *testing.T) {
	title, root, id := hostile()
	rows := []Session{{Harness: "opencode", ID: id, Title: title, Root: root, State: "idle", CurrentExecution: `<b>exec</b>`}}
	_, body, _ := fetch(NewHandler(&fakeSource{rows: rows}, nil), "/")
	assertNoActiveContent(t, "list", body)
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("title is not shown escaped:\n%s", body)
	}
	if strings.Contains(body, `href="/session/opencode/`) {
		t.Errorf("a session whose ID the detail route would refuse must not get a link:\n%s", body)
	}
}

func TestDetailEscapesHostileValues(t *testing.T) {
	title, root, _ := hostile()
	d := SessionDetail{
		Session: Session{Harness: "opencode", ID: "ses_x", Title: title, Root: root, State: "idle", LastActivity: t0,
			Guard: Guard{Held: true, State: "<b>g</b>"}, CurrentExecution: "<b>c</b>"},
		Executions: []Execution{{ID: "</td><script>alert(3)</script>", State: "<b>s</b>", Started: t0, Updated: t0}},
	}
	h := NewHandler(&fakeSource{details: map[string]SessionDetail{"opencode/ses_x": d}}, nil)
	code, body, hdr := fetch(h, "/session/opencode/ses_x")
	if code != http.StatusOK {
		t.Fatalf("detail = %d", code)
	}
	checkSecurityHeaders(t, "detail", hdr)
	assertNoActiveContent(t, "detail", body)
	if strings.Contains(body, "alert(3)</script>") {
		t.Errorf("an execution ID is rendered raw:\n%s", body)
	}
}

func TestDetailShowsStateGuardCurrentExecutionAndTimeline(t *testing.T) {
	end := t0.Add(time.Minute)
	d := SessionDetail{
		Session: Session{Harness: "opencode", ID: "ses_aaa", Title: "fix", Root: "/opt/proj-x", State: "running",
			Guard: Guard{Held: true, State: "running"}, CurrentExecution: "exec_2", LastActivity: t0},
		Executions: []Execution{
			{ID: "exec_2", State: "running", Started: t0, Updated: t0, Current: true},
			{ID: "exec_1", State: "completed", Started: t0.Add(-time.Hour), Updated: t0.Add(-time.Hour), Ended: &end},
		},
	}
	h := NewHandler(&fakeSource{details: map[string]SessionDetail{"opencode/ses_aaa": d}}, nil)
	_, body, _ := fetch(h, "/session/opencode/ses_aaa")
	for _, want := range []string{"/opt/proj-x", "held (running)", "exec_2 (current)", "exec_1", "completed", "2026-11-12 12:05:11Z", "2026-11-12 11:04:11Z", "Executions"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "ADR-021 pending") {
		t.Errorf("an OpenCode session must not show the Claude Code pending note")
	}
}

func TestClaudeCodeSessionShowsTheDocumentedPendingState(t *testing.T) {
	id := "4e59c1bd-4333-4a71-875d-cba9301bd4a9"
	d := SessionDetail{Session: Session{Harness: "claudecode", ID: id, State: "idle"}}
	h := NewHandler(&fakeSource{details: map[string]SessionDetail{"claudecode/" + id: d}}, nil)
	code, body, _ := fetch(h, "/session/claudecode/"+id)
	if code != http.StatusOK || !strings.Contains(body, "ADR-021 pending") || !strings.Contains(body, "No executions recorded") {
		t.Errorf("claude detail = %d %s", code, body)
	}
}

// T4: an unknown or invalid session is 404 with a fixed text; the request is
// never echoed, and an invalid identifier never reaches the Source.
func TestDetailRefusalsAreFixedTextAnd404(t *testing.T) {
	src := &fakeSource{details: map[string]SessionDetail{}}
	h := NewHandler(src, nil)
	invalid := []string{
		"/session/opencode/", "/session/opencode", "/session/", "/session//ses_x", "/session/opencode/ses_x/extra",
		"/session/opencode/..", "/session/opencode/../claudecode/x", "/session/../opencode/ses_x", "/session/opencode/.%2e/x",
		"/session/opencode/%3Cscript%3Ealert(1)%3C%2Fscript%3E", "/session/opencode/ses%2Fx", "/session/opencode/ses%00x",
		"/session/OPENCODE/ses_x", "/session/other/ses_x", "/session/opencode/" + strings.Repeat("a", 129),
		"/session/%3Cimg%20src=x%20onerror=alert(1)%3E/ses_x",
	}
	for _, p := range invalid {
		code, body, hdr := fetch(h, p)
		if code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, code)
		}
		if body != notFoundText+"\n" {
			t.Errorf("GET %s body %q: must be the fixed text", p, body)
		}
		checkSecurityHeaders(t, p, hdr)
	}
	if src.detailCalls != 0 {
		t.Errorf("%d invalid identifiers reached the Source", src.detailCalls)
	}
	// A well-formed but unlisted session asks the Source once and is 404 too.
	code, body, _ := fetch(h, "/session/opencode/ses_unlisted")
	if code != http.StatusNotFound || body != notFoundText+"\n" || src.detailCalls != 1 {
		t.Errorf("unlisted = %d %q (calls %d)", code, body, src.detailCalls)
	}
	// The raw request text never appears in any refusal.
	_, body, _ = fetch(h, "/session/opencode/"+url.PathEscape("x<script>y")+"z")
	if strings.Contains(body, "script") {
		t.Errorf("request echoed: %q", body)
	}
}

func TestDetailNeverRendersADifferentSessionThanRequested(t *testing.T) {
	other := SessionDetail{Session: Session{Harness: "opencode", ID: "ses_other", Title: "secret", State: "idle"}}
	h := NewHandler(&fakeSource{override: &other}, nil)
	code, body, _ := fetch(h, "/session/opencode/ses_asked")
	if code != http.StatusNotFound || strings.Contains(body, "secret") {
		t.Errorf("mismatched session = %d %s", code, body)
	}
}

func TestRenderingCaps(t *testing.T) {
	var rows []Session
	for i := 0; i < maxListRows+50; i++ {
		rows = append(rows, Session{Harness: "opencode", ID: fmt.Sprintf("ses_%04d", i), State: "idle"})
	}
	_, body, _ := fetch(NewHandler(&fakeSource{rows: rows}, nil), "/")
	if n := strings.Count(body, "<tr>") - 1; n != maxListRows { // minus the header row
		t.Errorf("rendered %d rows, want %d", n, maxListRows)
	}
	if !strings.Contains(body, fmt.Sprintf("Showing the first %d of %d sessions.", maxListRows, maxListRows+50)) {
		t.Errorf("a truncated list must say so:\n%s", body[:min(len(body), 400)])
	}

	long := strings.Repeat("é", 400) // multi-byte: a byte cut would break UTF-8
	_, body, _ = fetch(NewHandler(&fakeSource{rows: []Session{{Harness: "opencode", ID: "ses_l", Title: long, Root: long, State: "idle"}}}, nil), "/")
	if strings.Contains(body, long) || !strings.Contains(body, strings.Repeat("é", maxTitleRunes)+"…") {
		t.Errorf("long title not cut on a rune boundary")
	}
	if strings.Contains(body, "\uFFFD") {
		t.Errorf("replacement character: a rune was cut")
	}

	d := SessionDetail{Session: Session{Harness: "opencode", ID: "ses_d", State: "idle"}}
	for i := 0; i < maxDetailRows+30; i++ {
		d.Executions = append(d.Executions, Execution{ID: fmt.Sprintf("exec_%04d", i), State: "completed"})
	}
	_, body, _ = fetch(NewHandler(&fakeSource{details: map[string]SessionDetail{"opencode/ses_d": d}}, nil), "/session/opencode/ses_d")
	if n := strings.Count(body, "<tr><td>exec_"); n != maxDetailRows {
		t.Errorf("rendered %d executions, want %d", n, maxDetailRows)
	}
	if !strings.Contains(body, fmt.Sprintf("Showing the first %d of %d executions.", maxDetailRows, maxDetailRows+30)) {
		t.Errorf("a truncated execution list must say so")
	}
}

func TestHeadOnDetailAndMutatingMethodsOnEverySessionPath(t *testing.T) {
	h := NewHandler(&fakeSource{details: map[string]SessionDetail{"opencode/ses_a": {Session: Session{Harness: "opencode", ID: "ses_a", State: "idle"}}}}, nil)
	if rec := serve(h, http.MethodHead, "/session/opencode/ses_a", ""); rec.Code != http.StatusOK {
		t.Errorf("HEAD detail = %d", rec.Code)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, p := range []string{"/session/opencode/ses_a", "/session/opencode/nope", "/session/"} {
			if rec := serve(h, m, p, ""); rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", m, p, rec.Code)
			}
		}
	}
}

func TestSessionPrefixRouteIsGetOnlyAndExactRoutesWin(t *testing.T) {
	var found bool
	for _, r := range Routes(NullSource{}) {
		if r.Path == sessionPrefix {
			found = true
			if !r.Prefix || len(r.Methods) != 2 {
				t.Errorf("session route: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("no /session/ route")
	}
	// The assets and the index are exact routes and are not shadowed.
	h := NewHandler(NullSource{}, nil)
	for _, p := range []string{"/", "/dashboard.css", "/dashboard.js"} {
		if code, _, _ := fetch(h, p); code != http.StatusOK {
			t.Errorf("GET %s = %d", p, code)
		}
	}
	if code, _, _ := fetch(h, "/sessions"); code != http.StatusNotFound {
		t.Errorf("/sessions = %d: only /session/ is a prefix route", code)
	}
}

// ADR-023 section 3: no template.HTML, template.JS, template.URL, template.CSS
// or template.HTMLAttr on data, and html/template (not text/template).
func TestTemplatesAreHTMLTemplateWithoutTrustedTypes(t *testing.T) {
	for _, file := range []string{"views.go", "handler.go", "diffs.go", "redact.go", "source.go"} {
		af, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			if p := strings.Trim(imp.Path.Value, `"`); p == "text/template" {
				t.Errorf("%s imports text/template: output would not be escaped", file)
			}
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "template" {
					switch sel.Sel.Name {
					case "HTML", "JS", "URL", "CSS", "HTMLAttr", "JSStr", "Srcset":
						t.Errorf("%s uses template.%s: a trusted type on data", file, sel.Sel.Name)
					}
				}
			}
			return true
		})
	}
	raw, err := os.ReadFile("views.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`\{\{[^}]*\}\}`).FindAllString(string(raw), -1) {
		if regexp.MustCompile(`\|\s*(html|js|urlquery)\b|safeHTML|noescape`).MatchString(m) {
			t.Errorf("template action disables or bypasses escaping: %s", m)
		}
	}
}
