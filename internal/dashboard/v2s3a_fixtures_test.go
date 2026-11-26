package dashboard

// ADR-023 A1 slice S3a (audit, health, executions, catalog): the fixtures of every test of this slice. One page per
// screen and state, rendered with the fixed clock v2Now over a fake Source. Nothing here reads a real Bridge.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// s3Execs is the executions fixture: newest first, as the Source supplies them. The session IDs are harness-qualified.
func s3Execs() []Execution {
	end := func(from time.Time, d time.Duration) *time.Time { e := from.Add(d); return &e }
	a, b, c, dd := v2Now.Add(-8*time.Minute), v2Now.Add(-27*time.Minute), v2Now.Add(-46*time.Minute), v2Now.Add(-3*time.Hour)
	return []Execution{
		{ID: "7a2c91e0", State: "running", Started: a, Updated: v2Now, Current: true, SessionID: "opencode:ses_demo0001",
			Liveness: &Liveness{NativeStatus: "retry", RetryAttempt: ptrInt(4), RetryMax: ptrInt(5), RetryNextAt: ptrTime(v2Now.Add(-90 * time.Second)),
				RetryOverdue: true, DegradationReason: "PROVIDER_RETRY_OVERDUE", StallReason: "NO_PROGRESS", StallSuspected: true, ObservedAt: v2Now}},
		{ID: "b81f03d4", State: "waiting_for_permission", Started: b, Updated: v2Now, Current: true, SessionID: "opencode:ses_demo0002",
			Liveness: &Liveness{NativeStatus: "busy", ObservedAt: v2Now}},
		{ID: "5d03aa17", State: "completed", Started: c, Updated: v2Now, Ended: end(c, 6*time.Minute+2*time.Second), SessionID: "opencode:ses_demo0001"},
		{ID: "0c9be452", State: "failed", Started: dd, Updated: v2Now, Ended: end(dd, 75*time.Second), SessionID: "claudecode:cc-demo-1"},
		{ID: "e41d77b9", State: "interrupted", Started: dd.Add(-time.Hour), Updated: v2Now, Ended: end(dd.Add(-time.Hour), 3*time.Minute), SessionID: "not-a-qualified-id"},
	}
}

func s3Health(expires *time.Time) Health {
	return Health{Version: "v0.1.0", Build: "0123456789abcdef0123456789abcdef01234567", TokenExpires: expires,
		Features: []Feature{{"Lifecycle", true}, {"Permission relay", true}, {"Claude Code", false}},
		Reach:    []Reachability{{Harness: "opencode", State: "reachable", At: v2Now.Add(-12 * time.Second)}, {Harness: "claudecode", State: "not read"}}}
}

// s3Catalog is the sessions fixture of the catalog: two lifecycle roots, one session under none, one archived.
func s3Catalog() []Session {
	ago := func(d time.Duration) time.Time { return v2Now.Add(-d) }
	return []Session{
		{Harness: "opencode", ID: "ses_demo0001", Title: "Fix flaky retry test", Root: "/srv/demo/work/payments-api", LifecycleRoot: "/srv/demo/work", State: "running", LastActivity: ago(53 * time.Second)},
		{Harness: "opencode", ID: "ses_demo0002", Title: "Migrate config loader", Root: "/srv/demo/work/tools", LifecycleRoot: "/srv/demo/work", State: "waiting_for_permission", LastActivity: ago(6 * time.Minute)},
		{Harness: "opencode", ID: "ses_demo0009", Title: "Old prototype", Root: "/srv/demo/work/old", LifecycleRoot: "/srv/demo/work", Archived: true, State: "idle", LastActivity: ago(9 * 24 * time.Hour)},
		{Harness: "opencode", ID: "ses_lab0001", Title: "Lab notes", Root: "/srv/lab/notes", LifecycleRoot: "/srv/lab", State: "idle", LastActivity: ago(2 * time.Hour)},
		{Harness: "claudecode", ID: "cc-demo-1", Title: "Docs pass", Root: "/srv/other/docs", State: "unknown", LastActivity: ago(time.Hour)},
		{Harness: "opencode", ID: "ses_none0001", Title: "Spike: cache layer", Root: "/home/op/spikes/cache", State: "idle", LastActivity: ago(30 * time.Hour)},
	}
}

// s3Src is a Source for the new pages. archivedOnly rows are listed only when the request asks for archived sessions,
// as the production Source does (D17 Q7); the executions list obeys the same flag.
type s3Src struct {
	*fakeSource
	archivedExecs []Execution
	mu            sync.Mutex
	seenFlags     []bool
}

func (s *s3Src) note(ctx context.Context) {
	s.mu.Lock()
	s.seenFlags = append(s.seenFlags, ArchivedShown(ctx))
	s.mu.Unlock()
}

func (s *s3Src) Sessions(ctx context.Context) ([]Session, error) {
	s.note(ctx)
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

func (s *s3Src) Executions(ctx context.Context) ([]Execution, error) {
	s.note(ctx)
	out, err := s.fakeSource.Executions(ctx)
	if err == nil && ArchivedShown(ctx) {
		out = append(append([]Execution(nil), out...), s.archivedExecs...)
	}
	return out, err
}

func newS3Src() *s3Src {
	exp := v2Now.Add(52 * time.Minute)
	demo := s3Catalog()[0]
	return &s3Src{fakeSource: &fakeSource{rows: s3Catalog(), execs: s3Execs(), health: s3Health(&exp),
		details: map[string]SessionDetail{"opencode/ses_demo0001": {Session: demo}}},
		archivedExecs: []Execution{{ID: "old00001", State: "completed", Started: v2Now.Add(-9 * 24 * time.Hour), Ended: ptrTime(v2Now.Add(-9*24*time.Hour + time.Minute)), SessionID: "opencode:ses_demo0009"}}}
}

func s3Handler(src Source, label string) http.Handler {
	o := v2Opts()
	o.Label = label
	return NewHandlerOptions(src, nil, o)
}

// auditEntries builds n ring entries, newest first; entry i has the session ses_i and the given event/outcome.
func auditEntries(n int, event, outcome string) []AuditEntry {
	out := make([]AuditEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, AuditEntry{Time: v2Now.Add(-time.Duration(i) * time.Second), Message: event, Event: event,
			Attrs: []AuditAttr{{Key: "event", Value: event}, {Key: "outcome", Value: outcome}, {Key: "session_id", Value: fmt.Sprintf("ses_%04d", i)}, {Key: "principal", Value: "operator"}}})
	}
	return out
}

var (
	s3Mu    sync.Mutex
	s3Cache map[string]string
)

// s3Pages renders every S3a page and state once per process; each caller gets its own copy.
func s3Pages(t *testing.T) map[string]string {
	t.Helper()
	s3Mu.Lock()
	defer s3Mu.Unlock()
	if s3Cache == nil {
		s3Cache = renderS3Pages(t)
	}
	out := make(map[string]string, len(s3Cache))
	for k, v := range s3Cache {
		out[k] = v
	}
	return out
}

func renderS3Pages(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	get := func(name string, h http.Handler, target string, want int) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != want {
			t.Fatalf("%s: GET %s = %d, want %d", name, target, rec.Code, want)
		}
		out[name] = rec.Body.String()
	}
	full := s3Handler(newS3Src(), "bridge-a")
	get("executions", full, "/executions", 200)
	get("executions-archived", full, "/executions?archived=1", 200)
	get("catalog", full, "/catalog", 200)
	get("catalog-archived", full, "/catalog?archived=1", 200)
	get("health-remote", full, "/health", 200)
	local := newS3Src()
	local.health = s3Health(nil)
	get("health-local", s3Handler(local, ""), "/health", 200)
	get("list-labelled", full, "/", 200)

	empty := newS3Src()
	empty.execs, empty.rows = nil, nil
	get("executions-empty", s3Handler(empty, ""), "/executions", 200)
	get("catalog-empty", s3Handler(empty, ""), "/catalog", 200)
	down := newS3Src()
	down.execsErr, down.rowsErr, down.healthErr = context.DeadlineExceeded, context.DeadlineExceeded, errors.New("down")
	get("executions-unavailable", s3Handler(down, ""), "/executions", 503)
	get("catalog-unavailable", s3Handler(down, ""), "/catalog", 503)
	get("health-unavailable", s3Handler(down, ""), "/health", 503)
	get("executions-not-connected", v2Handler(NullSource{}), "/executions", 200)
	get("catalog-not-connected", v2Handler(NullSource{}), "/catalog", 200)
	get("health-not-connected", v2Handler(NullSource{}), "/health", 200)

	// the top bar's token badge in its three states
	for name, left := range map[string]time.Duration{"badge-ok": 5*time.Hour + 2*time.Minute, "badge-warn": 52 * time.Minute, "badge-expired": -3 * time.Minute} {
		s := newS3Src()
		exp := v2Now.Add(left)
		s.health = s3Health(&exp)
		get("detail-"+name, s3Handler(s, "bridge-a"), "/audit", 200)
	}

	// the audit page: chips from the fixed maps, an unknown word, three pages of a full ring
	mixed := append(auditEntries(2, "prompt_submission", "new"), auditEntries(1, "interrupt", "accepted")...)
	mixed = append(mixed, auditEntries(1, "guard_release", "refused")...)
	mixed = append(mixed, auditEntries(1, "session_create", "created")...)
	mixed = append(mixed, auditEntries(1, "future_event", "future_outcome")...)
	o := v2Opts()
	o.Audit = fakeAuditOf(mixed...)
	get("audit-chips", NewHandlerOptions(&fakeSource{}, nil, o), "/audit", 200)
	o.Audit = fakeAuditOf(auditEntries(250, "prompt_submission", "new")...)
	paged := NewHandlerOptions(&fakeSource{}, nil, o)
	get("audit-page1", paged, "/audit", 200)
	get("audit-page2", paged, "/audit?page=2", 200)
	get("audit-page3", paged, "/audit?page=3", 200)

	// hostile values in every new field
	evil := `<script>alert(1)</script>"><img src=x onerror=alert(2)>`
	hs := &s3Src{fakeSource: &fakeSource{
		rows: []Session{{Harness: "opencode", ID: "ses_evil0001", Title: evil, Root: "/tmp/" + evil, LifecycleRoot: evil, State: evil, LastActivity: v2Now}},
		execs: []Execution{{ID: evil, State: evil, Started: v2Now, Current: true, SessionID: "opencode:" + evil},
			{ID: "e2", State: "completed", Started: v2Now, SessionID: "opencode:ses_evil0001"}},
		health: Health{Version: evil, Build: evil, TokenExpires: ptrTime(v2Now.Add(time.Hour)), Features: []Feature{{Name: evil, On: true}},
			Reach: []Reachability{{Harness: evil, State: evil, At: v2Now}}}}}
	oh := v2Opts()
	oh.Label = "bridge a"
	oh.Audit = fakeAuditOf(AuditEntry{Time: v2Now, Message: evil, Event: evil, Attrs: []AuditAttr{{Key: "event", Value: evil}, {Key: "outcome", Value: evil}, {Key: "session_id", Value: evil}, {Key: "principal", Value: evil}, {Key: "reason", Value: evil}}})
	hh := NewHandlerOptions(hs, nil, oh)
	get("executions-hostile", hh, "/executions", 200)
	get("catalog-hostile", hh, "/catalog", 200)
	get("health-hostile", hh, "/health", 200)
	get("audit-hostile", hh, "/audit", 200)
	_ = strings.Contains
	return out
}
