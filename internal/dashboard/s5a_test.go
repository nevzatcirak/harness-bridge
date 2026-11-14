package dashboard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// Redaction stays on with no configuration (the shipped list), operator patterns are
// additive, and an invalid configuration fails closed without echoing the pattern.
func TestRedactorFromEnv(t *testing.T) {
	const ant = "key sk-ant-abcdefgh12345678 end"
	r, err := dashboard.RedactorFromEnv(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if out, n := r.Redact(ant); n != 1 || strings.Contains(out, "sk-ant-") {
		t.Fatalf("default list is not on with no configuration: %q %d", out, n)
	}
	r, err = dashboard.RedactorFromEnv(envOf(map[string]string{dashboard.EnvRedactExtra: `["corp-[0-9a-f]{8}"]`}))
	if err != nil {
		t.Fatal(err)
	}
	out, n := r.Redact(ant + " corp-0123abcd")
	if n != 2 || strings.Contains(out, "sk-ant-") || strings.Contains(out, "corp-0123abcd") {
		t.Fatalf("operator patterns must be additive to the shipped list: %q %d", out, n)
	}
	secretish := "SUPERSECRETPATTERN("
	for name, v := range map[string]string{
		"not json":      secretish,
		"not an array":  `{"a":"b"}`,
		"null":          `null`,
		"bad regex":     `["` + secretish + `"]`,
		"empty pattern": `[""]`,
		"too long":      `["` + strings.Repeat("a", 257) + `"]`,
		"too many":      `["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q"]`,
	} {
		_, err := dashboard.RedactorFromEnv(envOf(map[string]string{dashboard.EnvRedactExtra: v}))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "SUPERSECRET") || strings.Contains(err.Error(), "aaaa") {
			t.Errorf("%s: the error echoes the configuration: %v", name, err)
		}
	}
}

// fixedSource serves one session and one diff so the route sweep reaches the data paths.
type fixedSource struct{ dashboard.NullSource }

func (fixedSource) Sessions(context.Context) ([]dashboard.Session, error) {
	return []dashboard.Session{{Harness: "opencode", ID: "ses_a", State: "idle"}}, nil
}
func (fixedSource) SessionDetail(_ context.Context, h, id string) (dashboard.SessionDetail, error) {
	return dashboard.SessionDetail{Session: dashboard.Session{Harness: h, ID: id, State: "idle"}}, nil
}
func (fixedSource) SessionTurns(context.Context, string, string) ([]dashboard.Turn, error) {
	return []dashboard.Turn{{MessageID: "msg_a", State: dashboard.TurnChanges}}, nil
}
func (fixedSource) TurnDiff(context.Context, string, string, string) (dashboard.DiffResult, error) {
	return dashboard.DiffResult{Files: []dashboard.DiffFile{{Path: "a", HasPath: true}}}, nil
}
func (fixedSource) WorkingTreeDiff(context.Context, string, string) (dashboard.DiffResult, error) {
	return dashboard.DiffResult{}, nil
}

// Route sweep: every route, including the data paths, enforces Host and Origin and only
// answers GET and HEAD, whatever the Source behind it.
func TestRouteSweepHostOriginAndMethods(t *testing.T) {
	paths := []string{"/", "/api/state", "/api/state/session/opencode/ses_a", "/session/opencode/ses_a", "/session/opencode/ses_a/turn/msg_a",
		"/session/claudecode/00000000-0000-4000-8000-000000000001", "/audit", "/api/audit", "/dashboard.css", "/dashboard.js", "/nope"}
	h := dashboard.NewHandlerOptions(fixedSource{}, dashboard.DefaultHosts, dashboard.Options{Audit: dashboard.NewAuditRing()})
	do := func(method, path, host, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, p := range paths {
		if c := do(http.MethodGet, p, "127.0.0.1:8080", "").Code; c == http.StatusBadRequest || c == http.StatusForbidden || c == http.StatusMethodNotAllowed {
			t.Errorf("GET %s with a good Host: %d", p, c)
		}
		if c := do(http.MethodGet, p, "127.0.0.1:8080", "http://127.0.0.1:8080").Code; c == http.StatusForbidden || c == http.StatusBadRequest {
			t.Errorf("GET %s with the dashboard's own Origin: %d", p, c)
		}
		if c := do(http.MethodGet, p, "evil.example", "").Code; c != http.StatusBadRequest {
			t.Errorf("GET %s with a foreign Host: %d, want 400", p, c)
		}
		for _, o := range []string{"http://evil.example", "https://evil.example:8080", "null", "http://127.0.0.1.evil.example", "http://user@127.0.0.1:8080", "ftp://127.0.0.1", "::bad"} {
			if c := do(http.MethodGet, p, "127.0.0.1:8080", o).Code; c != http.StatusForbidden {
				t.Errorf("GET %s with Origin %q: %d, want 403", p, o, c)
			}
		}
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
			if c := do(m, p, "127.0.0.1:8080", "").Code; c != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: %d, want 405", m, p, c)
			}
		}
	}
}

// The unix socket has no browser origin: any Origin header is refused there.
func TestAnyOriginIsRefusedWithoutAHostAllowlist(t *testing.T) {
	h := dashboard.NewHandlerOptions(fixedSource{}, nil, dashboard.Options{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("no Origin: %d", rec.Code)
	}
}

// The default redaction list is applied to a served diff with no configuration.
func TestServedDiffIsRedactedByDefault(t *testing.T) {
	src := redactingSource{}
	h := dashboard.NewHandlerOptions(src, nil, dashboard.Options{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/session/opencode/ses_a", nil))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "sk-ant-abcdefgh12345678") || !strings.Contains(rec.Body.String(), "[redacted]") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

type redactingSource struct{ fixedSource }

func (redactingSource) WorkingTreeDiff(context.Context, string, string) (dashboard.DiffResult, error) {
	return dashboard.DiffResult{Files: []dashboard.DiffFile{{Path: "a.env", HasPath: true, Patch: "+KEY=sk-ant-abcdefgh12345678\n", HasPatch: true}}}, nil
}
