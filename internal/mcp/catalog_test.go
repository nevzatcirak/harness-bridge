package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// ADR-022 slice S3: list_catalog. These tests use a fake native server; no live
// OpenCode, Bridge or host service is involved.

type catalogEnv struct {
	t        *testing.T
	s        *Server
	rootA    string
	rootB    string
	outside  string
	mu       sync.Mutex
	requests []string // "METHOD path"
	// hook may answer a request itself; it returns true when it did.
	hook func(w http.ResponseWriter, r *http.Request) bool
}

func (e *catalogEnv) requestLog() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.requests...)
}

func ms(t string) int64 {
	v, err := time.Parse(time.RFC3339, t)
	if err != nil {
		panic(err)
	}
	return v.UnixMilli()
}

func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

type fakeOverlay struct {
	archived map[string]bool
	err      error
	calls    []string
}

func (f *fakeOverlay) Archived(_ context.Context, id string) (bool, error) {
	f.calls = append(f.calls, id)
	return f.archived[id], f.err
}

// newCatalogEnv serves five authorized sessions:
//
//	ses_a idle,    rootA/p1, updated 2026-01-10, hb_origin bridge
//	ses_b running, rootA/p2, updated 2026-02-10
//	ses_c waiting, rootB/p3, updated 2026-03-10, hb_origin other
//	ses_d idle,    outside every root, no time
//	ses_e idle,    rootB/p4, no time, metadata is an array (an unexpected shape)
func newCatalogEnv(t *testing.T, overlay ArchiveOverlay) *catalogEnv {
	t.Helper()
	canon := func(d string) string {
		r, err := filepath.EvalSymlinks(d)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	e := &catalogEnv{t: t, rootA: canon(t.TempDir()), rootB: canon(t.TempDir()), outside: canon(t.TempDir())}
	mk := func(parts ...string) string {
		p := filepath.Join(parts...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dirs := map[string]string{"ses_a": mk(e.rootA, "p1"), "ses_b": mk(e.rootA, "p2"), "ses_c": mk(e.rootB, "p3"), "ses_d": e.outside, "ses_e": mk(e.rootB, "p4")}
	sessions := map[string]opencode.Session{
		"ses_a": {ID: "ses_a", Title: "alpha", Directory: dirs["ses_a"], Time: rawJSON(map[string]any{"created": ms("2026-01-01T00:00:00Z"), "updated": ms("2026-01-10T00:00:00Z")}), Metadata: rawJSON(map[string]any{"hb_origin": "bridge", "hb_key": "abc"})},
		"ses_b": {ID: "ses_b", Title: "beta", Directory: dirs["ses_b"], Time: rawJSON(map[string]any{"updated": ms("2026-02-10T00:00:00Z")})},
		"ses_c": {ID: "ses_c", Title: "gamma", Directory: dirs["ses_c"], Time: rawJSON(map[string]any{"updated": ms("2026-03-10T00:00:00Z")}), Metadata: rawJSON(map[string]any{"hb_origin": "other"})},
		"ses_d": {ID: "ses_d", Title: "delta", Directory: dirs["ses_d"]},
		"ses_e": {ID: "ses_e", Title: "eps", Directory: dirs["ses_e"], Metadata: json.RawMessage(`[1,2]`)},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.requests = append(e.requests, r.Method+" "+r.URL.Path)
		e.mu.Unlock()
		if e.hook != nil && e.hook(w, r) {
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("a native write: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(405)
			return
		}
		switch {
		case r.URL.Path == "/session/status":
			_ = json.NewEncoder(w).Encode(map[string]opencode.Status{"ses_b": {Type: "busy"}})
		case r.URL.Path == "/permission":
			_ = json.NewEncoder(w).Encode([]opencode.Permission{{SessionID: "ses_c"}})
		case strings.HasSuffix(r.URL.Path, "/message"):
			_, _ = w.Write([]byte(`[]`)) // the F-5 history check of an idle session
		default:
			v, ok := sessions[strings.TrimPrefix(r.URL.Path, "/session/")]
			if !ok {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(v)
		}
	}))
	t.Cleanup(upstream.Close)
	client, err := opencode.New(upstream.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := security.New([]string{e.rootA, e.rootB, e.outside}, []string{"ses_a", "ses_b", "ses_c", "ses_d", "ses_e", "ses_gone"})
	if err != nil {
		t.Fatal(err)
	}
	lr, err := security.NewLifecycleRoots([]string{e.rootA, e.rootB})
	if err != nil {
		t.Fatal(err)
	}
	e.s = &Server{Client: client, Policy: policy, ReadOnly: true, Catalog: &CatalogConfig{Roots: lr, Overlay: overlay}}
	return e
}

func (e *catalogEnv) call(a args) (Catalog, error) {
	e.t.Helper()
	v, err := e.s.call(context.Background(), "list_catalog", a)
	if err != nil {
		return Catalog{}, err
	}
	return v.(Catalog), nil
}

func (e *catalogEnv) mustCall(a args) Catalog {
	e.t.Helper()
	if err := validateArgs("list_catalog", a); err != nil {
		e.t.Fatalf("validate: %v", err)
	}
	c, err := e.call(a)
	if err != nil {
		e.t.Fatalf("list_catalog: %v", err)
	}
	return c
}

func ids(c Catalog) []string {
	out := []string{}
	for _, s := range c.Sessions {
		out = append(out, strings.TrimPrefix(s.SessionRef.SessionID, "opencode:"))
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestCatalogEntriesAndInstanceQualifiedSessionRef(t *testing.T) {
	e := newCatalogEnv(t, nil)
	c := e.mustCall(args{})
	if !eq(ids(c), []string{"ses_a", "ses_b", "ses_c", "ses_d", "ses_e"}) {
		t.Fatalf("ids %v", ids(c))
	}
	inst := e.s.Client.InstanceID()
	if !strings.HasPrefix(inst, "harness:") {
		t.Fatalf("instance id %q", inst)
	}
	byID := map[string]CatalogEntry{}
	for _, s := range c.Sessions {
		byID[s.SessionRef.SessionID] = s
		if s.SessionRef.Harness != inst || s.HarnessName != "opencode" {
			t.Errorf("session_ref %+v harness_name %q", s.SessionRef, s.HarnessName)
		}
	}
	want := map[string]struct {
		state, root, updated string
		bridge               bool
	}{
		"opencode:ses_a": {"idle", e.rootA, "2026-01-10T00:00:00Z", true},
		"opencode:ses_b": {"running", e.rootA, "2026-02-10T00:00:00Z", false},
		"opencode:ses_c": {"waiting_for_permission", e.rootB, "2026-03-10T00:00:00Z", false},
		"opencode:ses_d": {"idle", "", "", false},
		"opencode:ses_e": {"idle", e.rootB, "", false}, // metadata of an unexpected shape: no label, no failure
	}
	for id, w := range want {
		g := byID[id]
		if g.State != w.state || g.Root != w.root || g.UpdatedAt != w.updated || g.CreatedByBridge != w.bridge || g.Archived {
			t.Errorf("%s: %+v, want %+v", id, g, w)
		}
	}
	if !eq(c.Harnesses, []string{"opencode"}) {
		t.Errorf("harnesses %v: a harness that is not wired must not be listed", c.Harnesses)
	}
	// The ref is stable: same instance, same ID, on every call.
	again := e.mustCall(args{})
	for i := range c.Sessions {
		if c.Sessions[i].SessionRef != again.Sessions[i].SessionRef {
			t.Errorf("session_ref changed: %+v -> %+v", c.Sessions[i].SessionRef, again.Sessions[i].SessionRef)
		}
	}
	// A different native instance gives a different harness identity.
	other, err := opencode.New("http://127.0.0.1:1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if other.InstanceID() == inst {
		t.Error("two instances share an identity")
	}
	// The existing tools keep their shapes: list_sessions still returns the opencode:<id> form.
	v, err := e.s.call(context.Background(), "list_sessions", args{})
	if err != nil || len(v.(Sessions).Sessions) != 5 || v.(Sessions).Sessions[0].SessionID != "opencode:ses_a" {
		t.Fatalf("list_sessions changed: %+v %v", v, err)
	}
}

func TestCatalogFilters(t *testing.T) {
	yes, no := true, false
	e := newCatalogEnv(t, nil)
	for name, tc := range map[string]struct {
		a    args
		want []string
	}{
		"root A":               {args{Root: e.rootA}, []string{"ses_a", "ses_b"}},
		"root B":               {args{Root: e.rootB}, []string{"ses_c", "ses_e"}},
		"root with no session": {args{Root: filepath.Join(e.rootA, "nope")}, []string{}},
		"state idle":           {args{State: "idle"}, []string{"ses_a", "ses_d", "ses_e"}},
		"state running":        {args{State: "running"}, []string{"ses_b"}},
		"state waiting":        {args{State: "waiting_for_permission"}, []string{"ses_c"}},
		"state unknown":        {args{State: "unknown"}, []string{}},
		"updated_since feb":    {args{UpdatedSince: "2026-02-01T00:00:00Z"}, []string{"ses_b", "ses_c"}},
		"updated_since exact":  {args{UpdatedSince: "2026-02-10T00:00:00Z"}, []string{"ses_b", "ses_c"}},
		"updated_since offset": {args{UpdatedSince: "2026-02-10T02:00:00+02:00"}, []string{"ses_b", "ses_c"}},
		"updated_since late":   {args{UpdatedSince: "2027-01-01T00:00:00Z"}, []string{}},
		"bridge true":          {args{CreatedByBridge: &yes}, []string{"ses_a"}},
		"bridge false":         {args{CreatedByBridge: &no}, []string{"ses_b", "ses_c", "ses_d", "ses_e"}},
		"harness opencode":     {args{HarnessName: "opencode"}, []string{"ses_a", "ses_b", "ses_c", "ses_d", "ses_e"}},
		"harness claudecode":   {args{HarnessName: "claudecode"}, []string{}}, // omitted, not guessed
		"combined":             {args{Root: e.rootB, State: "idle", CreatedByBridge: &no}, []string{"ses_e"}},
		"combined empty":       {args{Root: e.rootA, State: "waiting_for_permission"}, []string{}},
	} {
		if got := ids(e.mustCall(tc.a)); !eq(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// A session with no usable update time cannot satisfy updated_since (it is never guessed).
func TestCatalogUpdatedSinceNeverGuessesATime(t *testing.T) {
	e := newCatalogEnv(t, nil)
	got := ids(e.mustCall(args{UpdatedSince: "1970-01-02T00:00:00Z"}))
	if !eq(got, []string{"ses_a", "ses_b", "ses_c"}) {
		t.Fatalf("%v: ses_d and ses_e carry no time and must not match", got)
	}
}

func TestCatalogPaginationAndWindowLabelledAggregates(t *testing.T) {
	e := newCatalogEnv(t, nil)
	p1 := e.mustCall(args{Limit: 2})
	if !eq(ids(p1), []string{"ses_a", "ses_b"}) || p1.NextOffset == nil || *p1.NextOffset != 2 {
		t.Fatalf("page 1: %v %v", ids(p1), p1.NextOffset)
	}
	p2 := e.mustCall(args{Limit: 2, Offset: 2})
	if !eq(ids(p2), []string{"ses_c", "ses_d"}) || p2.NextOffset == nil || *p2.NextOffset != 4 {
		t.Fatalf("page 2: %v %v", ids(p2), p2.NextOffset)
	}
	p3 := e.mustCall(args{Limit: 2, Offset: 4})
	if !eq(ids(p3), []string{"ses_e"}) || p3.NextOffset != nil {
		t.Fatalf("page 3: %v %v", ids(p3), p3.NextOffset)
	}
	if past := e.mustCall(args{Offset: 1000}); len(past.Sessions) != 0 || past.Sessions == nil {
		t.Fatalf("offset past the end: %+v", past)
	}
	// Aggregates describe the returned page only, and say so.
	a := p1.Aggregates
	if a.Scope != "page" || a.Window != (CatalogWindow{Offset: 0, Limit: 2, Returned: 2}) || a.Total != 2 {
		t.Fatalf("window: %+v", a)
	}
	if a.ByState["idle"] != 1 || a.ByState["running"] != 1 || a.ByState["waiting_for_permission"] != 0 || a.ByState["unknown"] != 0 || a.ByRoot[e.rootA] != 2 || len(a.ByRoot) != 1 {
		t.Fatalf("page aggregates: %+v", a)
	}
	b := p2.Aggregates
	if b.Window != (CatalogWindow{Offset: 2, Limit: 2, Returned: 2}) || b.ByState["waiting_for_permission"] != 1 || b.ByState["idle"] != 1 || b.ByRoot[e.rootB] != 1 || len(b.ByRoot) != 1 {
		t.Fatalf("page 2 aggregates: %+v", b)
	}
	if def := e.mustCall(args{}); def.Aggregates.Window.Limit != 50 || def.Aggregates.Total != 5 {
		t.Fatalf("default window: %+v", def.Aggregates)
	}
	// Bounds as find_sessions.
	for _, bad := range []args{{Limit: 201}, {Limit: -1}, {Offset: 1001}, {Offset: -1}} {
		if err := validateArgs("list_catalog", bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestCatalogRefusesBadFiltersBeforeAnyNativeRead(t *testing.T) {
	e := newCatalogEnv(t, nil)
	for name, a := range map[string]args{
		"relative root":      {Root: "rel/dir"},
		"unclean root":       {Root: e.rootA + "/../" + filepath.Base(e.rootA)},
		"trailing slash":     {Root: e.rootA + "/"},
		"archived as state":  {State: "archived"},
		"unknown state":      {State: "busy"},
		"bad time":           {UpdatedSince: "yesterday"},
		"date only":          {UpdatedSince: "2026-01-01"},
		"bad harness":        {HarnessName: "Open Code"},
		"harness with slash": {HarnessName: "a/b"},
	} {
		err := validateArgs("list_catalog", a)
		var ee *execution.Error
		if !errors.As(err, &ee) || ee.Code != "INVALID_INPUT" {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := e.call(a); err == nil {
			t.Errorf("%s: call accepted", name)
		}
	}
	if n := len(e.requestLog()); n != 0 {
		t.Errorf("a refused filter made %d native requests: %v", n, e.requestLog())
	}
}

// ADR-022 6.1: one total deadline; an error is returned, never a short page.
func TestCatalogDeadlineReturnsAnErrorNotAShortPage(t *testing.T) {
	old := catalogDeadline
	catalogDeadline = 300 * time.Millisecond
	t.Cleanup(func() { catalogDeadline = old })
	e := newCatalogEnv(t, nil)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	e.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/session/ses_c" { // one session never answers
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return true
		}
		return false
	}
	type result struct {
		c   Catalog
		err error
	}
	done := make(chan result, 1)
	go func() { c, err := e.call(args{}); done <- result{c, err} }()
	select {
	case r := <-done:
		var ee *execution.Error
		if !errors.As(r.err, &ee) || ee.Code != "UPSTREAM_TIMEOUT" || !ee.Retryable || len(r.c.Sessions) != 0 {
			t.Fatalf("want a typed retryable timeout and no sessions, got %+v / %v", r.c, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("list_catalog has no deadline: a hung native read held the call past 3 s")
	}
}

func TestCatalogNativeErrorMidPageIsAnErrorNotAShortPage(t *testing.T) {
	e := newCatalogEnv(t, nil)
	e.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/session/ses_c" {
			w.WriteHeader(500)
			return true
		}
		return false
	}
	c, err := e.call(args{})
	if err == nil || envelope(err).Code != "UPSTREAM_FAILURE" || len(c.Sessions) != 0 {
		t.Fatalf("want UPSTREAM_FAILURE and no partial page, got %+v / %v", c, err)
	}
}

// A session that is in the ACL but no longer exists natively (404) is skipped, exactly
// as find_sessions does: it is not an error and not a partial page. ses_gone is in the
// ACL of this fixture and has no native record.
func TestCatalogSkipsASessionTheNativeServerNoLongerHas(t *testing.T) {
	e := newCatalogEnv(t, nil)
	c := e.mustCall(args{})
	for _, id := range ids(c) {
		if id == "ses_gone" {
			t.Fatal("a session with no native record was listed")
		}
	}
	if len(c.Sessions) != 5 {
		t.Fatalf("%v", ids(c))
	}
}

// A failed status read does not drop sessions: they are listed with state unknown.
func TestCatalogStatusFailureLeavesStateUnknownNotMissing(t *testing.T) {
	e := newCatalogEnv(t, nil)
	e.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/session/status" {
			w.WriteHeader(500)
			return true
		}
		return false
	}
	c := e.mustCall(args{})
	if len(c.Sessions) != 5 {
		t.Fatalf("sessions dropped: %v", ids(c))
	}
	for _, s := range c.Sessions {
		if s.State == "idle" || s.State == "running" {
			t.Errorf("%s: state %q without a status read", s.SessionRef.SessionID, s.State)
		}
	}
}

// D16 Q5: overlay-archived sessions are hidden by default and listed only with include_archived.
func TestCatalogOverlayArchivedHiddenByDefault(t *testing.T) {
	ov := &fakeOverlay{archived: map[string]bool{"opencode:ses_b": true, "opencode:ses_d": true}}
	e := newCatalogEnv(t, ov)
	if got := ids(e.mustCall(args{})); !eq(got, []string{"ses_a", "ses_c", "ses_e"}) {
		t.Fatalf("default: %v", got)
	}
	c := e.mustCall(args{IncludeArchived: true})
	if !eq(ids(c), []string{"ses_a", "ses_b", "ses_c", "ses_d", "ses_e"}) {
		t.Fatalf("include_archived: %v", ids(c))
	}
	for _, s := range c.Sessions {
		wantArchived := s.SessionRef.SessionID == "opencode:ses_b" || s.SessionRef.SessionID == "opencode:ses_d"
		if s.Archived != wantArchived {
			t.Errorf("%s archived=%v", s.SessionRef.SessionID, s.Archived)
		}
	}
	// Filters apply to archived sessions as well, and the aggregates follow the page.
	only := e.mustCall(args{IncludeArchived: true, State: "running"})
	if !eq(ids(only), []string{"ses_b"}) || !only.Sessions[0].Archived || only.Aggregates.Total != 1 {
		t.Fatalf("%+v", only)
	}
	if got := ids(e.mustCall(args{State: "running"})); len(got) != 0 {
		t.Fatalf("an archived session leaked into a filtered default listing: %v", got)
	}
}

// Production: the empty overlay hides nothing, with or without include_archived.
func TestCatalogEmptyOverlayHidesNothing(t *testing.T) {
	for _, ov := range []ArchiveOverlay{nil, EmptyArchiveOverlay{}} {
		e := newCatalogEnv(t, ov)
		for _, inc := range []bool{false, true} {
			c := e.mustCall(args{IncludeArchived: inc})
			if len(c.Sessions) != 5 {
				t.Fatalf("overlay %T include=%v: %v", ov, inc, ids(c))
			}
			for _, s := range c.Sessions {
				if s.Archived {
					t.Fatalf("empty overlay marked %s archived", s.SessionRef.SessionID)
				}
			}
		}
	}
}

// An overlay that cannot answer fails the call; a session is never listed or hidden by a guess.
func TestCatalogOverlayErrorIsAnErrorNotAGuess(t *testing.T) {
	ov := &fakeOverlay{err: errors.New("boom-secret-detail")}
	e := newCatalogEnv(t, ov)
	c, err := e.call(args{})
	var ee *execution.Error
	if !errors.As(err, &ee) || ee.Code != "INTERNAL" || len(c.Sessions) != 0 {
		t.Fatalf("want INTERNAL and no sessions, got %+v / %v", c, err)
	}
	if strings.Contains(ee.Message, "boom-secret-detail") {
		t.Errorf("the overlay's own error text leaked: %q", ee.Message)
	}
}

// The tool never writes natively and is not a mutating tool: no audit record is needed
// and BRIDGE_READ_ONLY (set in this fixture) does not refuse it.
func TestCatalogIsReadOnly(t *testing.T) {
	e := newCatalogEnv(t, &fakeOverlay{})
	if mutating("list_catalog") {
		t.Fatal("list_catalog is classified as mutating")
	}
	e.mustCall(args{})
	e.mustCall(args{IncludeArchived: true, State: "idle"})
	for _, r := range e.requestLog() {
		if !strings.HasPrefix(r, "GET ") {
			t.Errorf("native request %q is not a read", r)
		}
	}
	if len(e.requestLog()) == 0 {
		t.Fatal("no native request was recorded")
	}
	if !e.s.ReadOnly {
		t.Fatal("fixture must run with BRIDGE_READ_ONLY")
	}
}

// A Server without a Catalog does not have the tool: an existing deployment's tool set is unchanged.
func TestCatalogToolIsAbsentWithoutConfiguration(t *testing.T) {
	e := newCatalogEnv(t, nil)
	e.s.Catalog = nil
	if _, err := e.call(args{}); err == nil {
		t.Fatal("list_catalog served without a Catalog")
	}
	e.s.Catalog = &CatalogConfig{}
	if _, err := e.call(args{}); err == nil {
		t.Fatal("list_catalog served without lifecycle roots")
	}
}

// Through the real MCP transport: the tool is listed (12 tools), its schemas validate
// the request and the result, BRIDGE_READ_ONLY keeps it available, and the existing
// eleven are unchanged.
func TestCatalogOverTheMCPTransport(t *testing.T) {
	e := newCatalogEnv(t, &fakeOverlay{archived: map[string]bool{"opencode:ses_b": true}})
	e.s.Token = strings.Repeat("t", 32)
	ts := httptest.NewServer(e.s)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mcpclient.Connect(ctx, ts.URL, e.s.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	tools, err := client.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 12 {
		t.Fatalf("tools: %d %v", len(tools.Tools), err)
	}
	var found bool
	for _, tool := range tools.Tools {
		if tool.Name == "list_catalog" {
			found = true
		}
	}
	if !found {
		t.Fatal("list_catalog is not listed")
	}
	got := decode[Catalog](t, invoke(t, client, "list_catalog", map[string]any{"limit": 2, "state": "idle"}, false))
	if !eq(ids(got), []string{"ses_a", "ses_d"}) || got.NextOffset == nil || got.Aggregates.Scope != "page" {
		t.Fatalf("%+v", got)
	}
	all := decode[Catalog](t, invoke(t, client, "list_catalog", map[string]any{"include_archived": true}, false))
	if len(all.Sessions) != 5 {
		t.Fatalf("%v", ids(all))
	}
	// A request the schema rejects is an error, with no native read for it.
	before := len(e.requestLog())
	for _, bad := range []map[string]any{{"state": "archived"}, {"limit": 500}, {"root": "relative"}, {"unknown_field": 1}, {"include_archived": "yes"}} {
		ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		r, err := client.CallTool(ctx2, &sdk.CallToolParams{Name: "list_catalog", Arguments: bad})
		cancel2()
		if err == nil && (r == nil || !r.IsError) {
			t.Errorf("%v accepted", bad)
		}
	}
	if n := len(e.requestLog()); n != before {
		t.Errorf("rejected requests made %d native reads", n-before)
	}
	// An existing tool still has its shape.
	sess := decode[Sessions](t, invoke(t, client, "list_sessions", EmptyInput{}, false))
	if len(sess.Sessions) != 5 || sess.Sessions[0].SessionID != "opencode:ses_a" {
		t.Fatalf("%+v", sess)
	}
}
