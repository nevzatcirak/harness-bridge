package dashboard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/dashboard"
)

// ADR-023 slice 5b (D17 Q6, Q7): the lifecycle-root label and the archived filter reach the
// views, and the filter is a query flag read by one place of the handler.

type flagSource struct {
	dashboard.NullSource
	mu   sync.Mutex
	seen []bool // ArchivedShown(ctx) of every Source call
}

func (f *flagSource) note(ctx context.Context) {
	f.mu.Lock()
	f.seen = append(f.seen, dashboard.ArchivedShown(ctx))
	f.mu.Unlock()
}

func (f *flagSource) last() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) == 0 {
		return false
	}
	return f.seen[len(f.seen)-1]
}

func (f *flagSource) Sessions(ctx context.Context) ([]dashboard.Session, error) {
	f.note(ctx)
	rows := []dashboard.Session{
		{Harness: "opencode", ID: "ses_a", Title: "plain", Root: "/srv/a", State: "idle"},
		{Harness: "opencode", ID: "ses_life", Title: "labelled", Root: "/srv/life/proj", LifecycleRoot: "/srv/life", State: "idle"},
	}
	if dashboard.ArchivedShown(ctx) {
		rows = append(rows, dashboard.Session{Harness: "opencode", ID: "ses_old", Title: "old", Root: "/srv/life/old", LifecycleRoot: "/srv/life", State: "idle", Archived: true})
	}
	return rows, nil
}

func (f *flagSource) SessionDetail(ctx context.Context, h, id string) (dashboard.SessionDetail, error) {
	f.note(ctx)
	return dashboard.SessionDetail{Session: dashboard.Session{Harness: h, ID: id, State: "idle", LifecycleRoot: "/srv/life", Archived: id == "ses_old"}}, nil
}

func (f *flagSource) SessionTurns(ctx context.Context, _, _ string) ([]dashboard.Turn, error) {
	f.note(ctx)
	return nil, nil
}

func (f *flagSource) WorkingTreeDiff(ctx context.Context, _, _ string) (dashboard.DiffResult, error) {
	f.note(ctx)
	return dashboard.DiffResult{}, nil
}

func get(t *testing.T, h http.Handler, target string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, rec.Body.String()
}

// The flag is exactly "archived=1" and is read for every data route.
func TestArchivedFlagIsReadFromTheQueryOnEveryDataRoute(t *testing.T) {
	src := &flagSource{}
	// A clock that moves 2 s per reading, so no JSON model is served from the cache here (the
	// cache has its own test below).
	var tick int64
	var tmu sync.Mutex
	now := func() time.Time {
		tmu.Lock()
		defer tmu.Unlock()
		tick++
		return time.Unix(1700000000+2*tick, 0)
	}
	h := dashboard.NewHandlerOptions(src, nil, dashboard.Options{Now: now})
	for _, p := range []string{"/", "/api/state", "/session/opencode/ses_old", "/api/state/session/opencode/ses_old"} {
		for target, want := range map[string]bool{p: false, p + "?archived=1": true, p + "?archived=0": false, p + "?archived=yes": false, p + "?archived=": false} {
			if code, _ := get(t, h, target); code != 200 {
				t.Fatalf("GET %s: %d", target, code)
			}
			if got := src.last(); got != want {
				t.Errorf("GET %s: ArchivedShown = %v, want %v", target, got, want)
			}
		}
	}
}

// The list shows the lifecycle-root label, the archived badge, and a link that keeps the filter.
func TestListShowsTheLifecycleRootAndArchivedRows(t *testing.T) {
	h := dashboard.NewHandlerOptions(&flagSource{}, nil, dashboard.Options{})
	_, plain := get(t, h, "/")
	if !strings.Contains(plain, "<code>/srv/life</code>") || strings.Contains(plain, "ses_old") || strings.Contains(plain, "archived</span>") {
		t.Fatalf("default list: %s", plain)
	}
	if !strings.Contains(plain, `href="/?archived=1"`) {
		t.Errorf("no link to show archived sessions: %s", plain)
	}
	_, all := get(t, h, "/?archived=1")
	if !strings.Contains(all, "ses_old") || !strings.Contains(all, `badge archived">archived</span>`) || !strings.Contains(all, `href="/session/opencode/ses_old?archived=1"`) {
		t.Fatalf("list with the filter: %s", all)
	}
	if !strings.Contains(all, `href="/"`) {
		t.Errorf("no link back to hide archived sessions")
	}
	_, js := get(t, h, "/api/state?archived=1")
	for _, want := range []string{`"lifecycle_root":"/srv/life"`, `"archived":true`} {
		if !strings.Contains(js, want) {
			t.Errorf("JSON model lacks %s: %s", want, js)
		}
	}
	_, js = get(t, h, "/api/state")
	if strings.Contains(js, "ses_old") || strings.Contains(js, `"archived":true`) {
		t.Errorf("default JSON model shows archived: %s", js)
	}
}

// The detail page shows the label too.
func TestDetailShowsTheLifecycleRoot(t *testing.T) {
	h := dashboard.NewHandlerOptions(&flagSource{}, nil, dashboard.Options{})
	_, body := get(t, h, "/session/opencode/ses_a")
	if !strings.Contains(body, "Lifecycle root") || !strings.Contains(body, "<code>/srv/life</code>") {
		t.Fatalf("%s", body)
	}
	_, js := get(t, h, "/api/state/session/opencode/ses_a")
	if !strings.Contains(js, `"lifecycle_root":"/srv/life"`) {
		t.Fatalf("%s", js)
	}
}

// Sweep: the filter query does not change any security check on any route.
func TestRouteSweepWithTheArchivedFilter(t *testing.T) {
	h := dashboard.NewHandlerOptions(&flagSource{}, dashboard.DefaultHosts, dashboard.Options{Audit: dashboard.NewAuditRing()})
	for _, p := range []string{"/?archived=1", "/api/state?archived=1", "/session/opencode/ses_a?archived=1", "/api/state/session/opencode/ses_a?archived=1", "/audit?archived=1"} {
		do := func(method, host, origin string) int {
			req := httptest.NewRequest(method, p, nil)
			req.Host = host
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec.Code
		}
		if c := do(http.MethodGet, "127.0.0.1:8080", ""); c != 200 {
			t.Errorf("GET %s: %d", p, c)
		}
		if c := do(http.MethodGet, "evil.example", ""); c != http.StatusBadRequest {
			t.Errorf("GET %s foreign Host: %d", p, c)
		}
		if c := do(http.MethodGet, "127.0.0.1:8080", "http://evil.example"); c != http.StatusForbidden {
			t.Errorf("GET %s foreign Origin: %d", p, c)
		}
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			if c := do(m, "127.0.0.1:8080", ""); c != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: %d", m, p, c)
			}
		}
	}
}

// The cached JSON model of a request with the filter is never served to one without it, and
// the other way round (the cache key carries the flag).
func TestArchivedModelIsNotSharedAcrossTheFilter(t *testing.T) {
	h := dashboard.NewHandlerOptions(&flagSource{}, nil, dashboard.Options{}) // the real clock: both requests are inside the 1 s cache window
	_, with := get(t, h, "/api/state?archived=1")
	_, without := get(t, h, "/api/state")
	if !strings.Contains(with, "ses_old") {
		t.Fatalf("the filtered model lacks the archived session: %s", with)
	}
	if strings.Contains(without, "ses_old") {
		t.Fatalf("an archived session leaked into the unfiltered model through the cache: %s", without)
	}
	_, with2 := get(t, h, "/api/state?archived=1")
	if !strings.Contains(with2, "ses_old") {
		t.Fatalf("the filtered model was replaced by the unfiltered one: %s", with2)
	}
}
