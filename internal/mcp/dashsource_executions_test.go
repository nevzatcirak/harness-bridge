package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/adapters/opencode"
	"github.com/nevzatcirak/cirak/internal/dashboard"
	"github.com/nevzatcirak/cirak/internal/execution"
	"github.com/nevzatcirak/cirak/internal/security"
)

// ADR-023 A1.8 and A1.10 item 11 (slice S3a): Source.Executions lists the executions of exactly the sessions the
// Sessions call lists for the request, before the Sessions page's display cap, capped after the filter; Source.Health
// reports the Bridge's own facts and the outcome of the last session-list read. No new native read: the fake native
// of these tests counts what it is asked.

// dashRunner is a runner whose sessions always exist and never finish, so every Send leaves one live execution.
type dashRunner struct{ root string }

func (r dashRunner) Get(_ context.Context, id string) (opencode.Session, error) {
	return opencode.Session{ID: id, Directory: r.root}, nil
}
func (dashRunner) Permissions(context.Context) ([]opencode.Permission, error) { return nil, nil }
func (dashRunner) History(context.Context, string) ([]opencode.Message, error) {
	return []opencode.Message{}, nil
}
func (dashRunner) Status(context.Context) (map[string]opencode.Status, error) {
	return map[string]opencode.Status{}, nil
}
func (dashRunner) Prompt(context.Context, string, string, string) error { return nil }
func (dashRunner) Abort(context.Context, string) error                  { return nil }

// execFixture is a Server over the fake native plus a Manager holding one execution per given session.
func execFixture(t *testing.T, acl []string, execSessions []string) (*Server, *dashNative) {
	t.Helper()
	root := t.TempDir()
	runs := execution.NewWithTiming(dashRunner{root: root}, time.Hour, time.Hour)
	t.Cleanup(runs.Close)
	s, n := newDashServer(t, acl, runs)
	// the fake native serves the sessions under ITS root: send through the same one
	for i, id := range execSessions {
		if _, err := runs.Send(context.Background(), id, "prompt", fmt.Sprintf("key-%d-%s", i, id)); err != nil {
			t.Fatalf("send on %s: %v", id, err)
		}
		time.Sleep(2 * time.Millisecond) // distinct start times: the list is ordered by them
	}
	return s, n
}

func execSessionIDs(execs []dashboard.Execution) []string {
	var out []string
	for _, e := range execs {
		out = append(out, e.SessionID)
	}
	return out
}

func has(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// An execution of an archived session is absent without ?archived=1 and present with it; one of a session outside the
// ACL is never listed, with or without the flag.
func TestDashboardSourceExecutionsFollowTheScopeAndTheArchivedFlag(t *testing.T) {
	s, _ := execFixture(t, []string{"ses_a", "ses_b"}, []string{"ses_a", "ses_b", "ses_outsider"})
	src := s.DashboardSource(archiveFake{archived: map[string]bool{"opencode:ses_b": true}})
	ctx := context.Background()
	plain, err := src.Executions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := execSessionIDs(plain); len(got) != 1 || got[0] != "opencode:ses_a" {
		t.Fatalf("without the flag: %v, want only ses_a", got)
	}
	flagged, err := src.Executions(dashboard.WithArchived(ctx))
	if err != nil {
		t.Fatal(err)
	}
	got := execSessionIDs(flagged)
	if len(got) != 2 || !has(got, "opencode:ses_a") || !has(got, "opencode:ses_b") {
		t.Fatalf("with the flag: %v, want ses_a and ses_b", got)
	}
	for _, g := range append(execSessionIDs(plain), got...) {
		if strings.Contains(g, "outsider") {
			t.Errorf("an execution of a session outside the ACL was listed: %s", g)
		}
	}
}

// The subset property: every listed execution belongs to a session the Sessions list names for the same request.
func TestDashboardSourceExecutionsAreASubsetOfTheSessionScope(t *testing.T) {
	s, _ := execFixture(t, []string{"ses_a", "ses_b", "ses_life"}, []string{"ses_a", "ses_b", "ses_life", "ses_x1", "ses_x2", "ses_x3"})
	src := s.DashboardSource(archiveFake{archived: map[string]bool{"opencode:ses_life": true}})
	for _, ctx := range []context.Context{context.Background(), dashboard.WithArchived(context.Background())} {
		rows, err := src.Sessions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		scope := map[string]bool{}
		for _, r := range rows {
			scope[r.Harness+":"+r.ID] = true
		}
		execs, err := src.Executions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range execs {
			if !scope[e.SessionID] {
				t.Errorf("the execution %s of %s is not in the scope set %v", e.ID, e.SessionID, scope)
			}
		}
		if len(execs) == 0 {
			t.Error("no executions at all: the property is vacuous")
		}
	}
}

// The cap of 200 is applied AFTER the filter: executions of sessions that are not in scope can neither appear nor
// push a visible one out.
func TestDashboardSourceExecutionsCapComesAfterTheFilter(t *testing.T) {
	hidden := make([]string, 0, 230)
	for i := 0; i < 230; i++ {
		hidden = append(hidden, fmt.Sprintf("ses_h%04d", i))
	}
	// the visible ones are the OLDEST: if the cap came first, 200 newer hidden executions would crowd them out
	s, _ := execFixture(t, []string{"ses_a", "ses_b"}, append([]string{"ses_a", "ses_b"}, hidden...))
	execs, err := s.DashboardSource(nil).Executions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := execSessionIDs(execs)
	if len(got) != 2 || !has(got, "opencode:ses_a") || !has(got, "opencode:ses_b") {
		t.Fatalf("%d executions %v: the hidden ones crowded the visible ones out, or appeared", len(got), got)
	}
}

// More than 200 in-scope sessions: the join set is the scope set BEFORE the Sessions page's display cap, so an
// execution of the 201st session is listed on /executions while the Sessions page shows 200 rows and says so; and the
// list itself is capped at 200, newest first.
func TestDashboardSourceExecutionsJoinTheFullScopeSetNotTheDisplayCap(t *testing.T) {
	var acl []string
	for i := 0; i < 205; i++ {
		acl = append(acl, fmt.Sprintf("ses_n%03d", i))
	}
	s, n := execFixture(t, acl, []string{"ses_n000", "ses_n200", "ses_n204"})
	src := s.DashboardSource(nil)
	execs, err := src.Executions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := execSessionIDs(execs)
	if len(got) != 3 || got[0] != "opencode:ses_n204" || got[1] != "opencode:ses_n200" || got[2] != "opencode:ses_n000" {
		t.Fatalf("executions %v: want newest first, with the sessions beyond the first 200 rows included", got)
	}
	// through the handler: the Sessions page shows 200 rows and the truncation note; /executions lists the 201st
	h := dashboard.NewHandler(src, nil)
	list := httptest.NewRecorder()
	h.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(list.Body.String(), "Showing the first 200 of 205 sessions.") || strings.Contains(list.Body.String(), "ses_n200") {
		t.Error("the Sessions page must show 200 rows and the truncation note, without the 201st session")
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/executions", nil))
	if !strings.Contains(page.Body.String(), "ses_n200") || !strings.Contains(page.Body.String(), `href="/session/opencode/ses_n200"`) {
		t.Error("the execution of the 201st session is not listed on /executions")
	}
	_ = n
	// the cap: 230 in-scope executions are cut to the newest 200
	var acl2, send []string
	for i := 0; i < 230; i++ {
		id := fmt.Sprintf("ses_n%03d", i)
		acl2, send = append(acl2, id), append(send, id)
	}
	s2, _ := execFixture(t, acl2[:230], send)
	execs2, err := s2.DashboardSource(nil).Executions(context.Background())
	if err != nil || len(execs2) != 200 {
		t.Fatalf("%d executions (%v), want the cap of 200", len(execs2), err)
	}
	if got := execSessionIDs(execs2); got[0] != "opencode:ses_n229" || has(got, "opencode:ses_n000") {
		t.Errorf("the cap kept the wrong executions: first %s", got[0])
	}
}

// T10: the executions are copies; mutating them leaves the Manager and the next call untouched.
func TestDashboardSourceExecutionsAreCopies(t *testing.T) {
	s, _ := execFixture(t, []string{"ses_a"}, []string{"ses_a"})
	src := s.DashboardSource(nil)
	a, err := src.Executions(context.Background())
	if err != nil || len(a) != 1 {
		t.Fatalf("%v %v", a, err)
	}
	id := a[0].ID
	a[0].ID, a[0].SessionID, a[0].State = "mutated", "opencode:other", "failed"
	b, _ := src.Executions(context.Background())
	if len(b) != 1 || b[0].ID != id || b[0].SessionID != "opencode:ses_a" || b[0].State == "failed" {
		t.Errorf("a mutation of the result reached the next call: %+v", b)
	}
	if !b[0].Current {
		t.Error("the live execution of the session is not marked current")
	}
}

// No new native read beyond the discovery the Sessions list makes: Executions asks the native exactly as Sessions does.
func TestDashboardSourceExecutionsMakeNoNativeReadOfTheirOwn(t *testing.T) {
	s, n := execFixture(t, []string{"ses_a", "ses_b"}, []string{"ses_a"})
	src := s.DashboardSource(nil)
	before := len(n.log())
	if _, err := src.Sessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessionsReads := len(n.log()) - before
	before = len(n.log())
	if _, err := src.Executions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(n.log()) - before; got != sessionsReads {
		t.Errorf("Executions made %d native reads, the Sessions list makes %d", got, sessionsReads)
	}
	before = len(n.log())
	if _, err := src.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(n.log()) - before; got != 0 {
		t.Errorf("Health made %d native reads, want none", got)
	}
}

// ---- Health ----------------------------------------------------------------------------------------------------

func TestDashboardSourceHealthReachabilityIsTheLastListRead(t *testing.T) {
	s, n := execFixture(t, []string{"ses_a"}, nil)
	src := s.DashboardSource(nil)
	reach := func() dashboard.Reachability {
		h, err := src.Health(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Reach) != 2 || h.Reach[0].Harness != "opencode" || h.Reach[1].Harness != "claudecode" || h.Reach[1].State != "not read" {
			t.Fatalf("reach: %+v", h.Reach)
		}
		return h.Reach[0]
	}
	if r := reach(); r.State != "not read" || !r.At.IsZero() {
		t.Errorf("before any read: %+v", r)
	}
	if _, err := src.Sessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := reach(); r.State != "reachable" || r.At.IsZero() {
		t.Errorf("after a good read: %+v", r)
	}
	n.down.Store(true)
	_, _ = src.Sessions(context.Background())
	if r := reach(); r.State != "unreachable" {
		t.Errorf("after a failed read: %+v", r)
	}
	n.down.Store(false)
	if _, err := src.Sessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := reach(); r.State != "reachable" {
		t.Errorf("after the harness came back: %+v", r)
	}
}

func TestDashboardSourceHealthReportsTheBridgesOwnFacts(t *testing.T) {
	s, _ := execFixture(t, []string{"ses_a"}, nil)
	exp := time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)
	remote, err := security.NewRemoteWithMaxLifetime("https://bridge.example", "operator", time.Now().Add(time.Hour), time.Hour*2)
	if err != nil {
		t.Fatal(err)
	}
	_ = exp
	s.Remote = remote
	s.Catalog = &CatalogConfig{}
	h, err := s.DashboardSource(nil).Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.Version == "" {
		t.Error("no version")
	}
	if h.TokenExpires == nil || !h.TokenExpires.Equal(remote.Expires()) {
		t.Errorf("token expiry %v, want %v", h.TokenExpires, remote.Expires())
	}
	want := map[string]bool{"Lifecycle": true, "Permission relay": false, "Claude Code": false}
	for _, f := range h.Features {
		if on, ok := want[f.Name]; !ok || on != f.On {
			t.Errorf("feature %+v", f)
		}
		delete(want, f.Name)
	}
	if len(want) != 0 {
		t.Errorf("missing features %v", want)
	}
	// local mode: no expiry at all
	s2, _ := execFixture(t, []string{"ses_a"}, nil)
	h2, _ := s2.DashboardSource(nil).Health(context.Background())
	if h2.TokenExpires != nil {
		t.Errorf("local mode reports an expiry: %v", h2.TokenExpires)
	}
	// the read-only getter of the remote settings returns only a timestamp
	if got := fmt.Sprint(remote.Expires()); strings.Contains(got, "operator") || strings.Contains(got, "bridge.example") {
		t.Errorf("the getter leaks %q", got)
	}
}

// Health is safe against a concurrent list read (-race).
func TestDashboardSourceHealthRacesWithTheListRead(t *testing.T) {
	s, _ := execFixture(t, []string{"ses_a", "ses_b"}, []string{"ses_a"})
	src := s.DashboardSource(nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = src.Sessions(context.Background()) }()
		go func() {
			defer wg.Done()
			_, _ = src.Health(context.Background())
			_, _ = src.Executions(context.Background())
		}()
	}
	wg.Wait()
}
