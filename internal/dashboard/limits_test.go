package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// gateSource is a fakeSource whose list read blocks until the gate is closed (or, unless
// ignoreCancel, its context ends).
type gateSource struct {
	*fakeSource
	gate         chan struct{}
	entered      chan struct{}
	ignoreCancel bool
}

func (g *gateSource) Sessions(ctx context.Context) ([]Session, error) {
	g.mu.Lock()
	g.listCalls++
	g.mu.Unlock()
	select {
	case g.entered <- struct{}{}:
	default:
	}
	if g.ignoreCancel {
		<-g.gate
	} else {
		select {
		case <-g.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.rows, nil
}

func newCancelable() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

type held struct {
	wg    sync.WaitGroup
	codes []*httptest.ResponseRecorder
}

// hold starts n requests for path and returns once every one has entered its handler.
func hold(t *testing.T, h http.Handler, path string, n int, entered chan struct{}) *held {
	t.Helper()
	x := &held{}
	for i := 0; i < n; i++ {
		rec := httptest.NewRecorder()
		x.codes = append(x.codes, rec)
		x.wg.Add(1)
		go func() { defer x.wg.Done(); h.ServeHTTP(rec, newGet(path)) }()
	}
	for i := 0; i < n; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of %d requests for %s entered their handler", i, n, path)
		}
	}
	return x
}

func (x *held) release(t *testing.T, gate chan struct{}) {
	t.Helper()
	close(gate)
	x.wg.Wait()
}

func refused(t *testing.T, what string, h http.Handler, path string) {
	t.Helper()
	start := time.Now()
	rec := serveReq(h, newGet(path))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || strings.TrimSpace(rec.Body.String()) != busyText {
		t.Errorf("%s: %s = %d retry=%q body=%q, want 503 with Retry-After", what, path, rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	checkSecurityHeaders(t, what+" 503", rec.Header())
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("%s: a refused request waited %v: the limit must never queue", what, took)
	}
}

// The documented numbers are pinned as literals (a test that reads the constants would
// follow a mutation of them): ADR-023 section 4 says 4 concurrent and 8 in flight.
func TestLimitsAreTheDocumentedFourAndEight(t *testing.T) {
	if maxDataRequests != 4 || maxInFlight != 8 {
		t.Fatalf("limits are %d and %d, the ADR says 4 and 8", maxDataRequests, maxInFlight)
	}
}

// ADR-023 section 4: at most 4 data requests at once; the 5th is refused at once.
func TestFifthConcurrentDataRequestIsRefusedWithRetryAfterAndNeverQueued(t *testing.T) {
	gate, entered := make(chan struct{}), make(chan struct{}, 16)
	src := &gateSource{fakeSource: &fakeSource{rows: sampleRows(), details: map[string]SessionDetail{"opencode/" + sesID: session("opencode", sesID)}}, gate: gate, entered: entered}
	h := NewHandlerOptions(src, nil, Options{Audit: NewAuditRing()})
	x := hold(t, h, "/", 4, entered) // exactly 4 are admitted
	for _, p := range []string{"/", "/api/state", "/session/opencode/" + sesID, "/api/state/session/opencode/" + sesID} {
		refused(t, "5th data request", h, p)
	}
	// A request that does not read the data source is still served: its limit is the in-flight one.
	for _, p := range []string{"/dashboard.css", "/dashboard.js", "/audit", "/api/audit"} {
		if rec := serveReq(h, newGet(p)); rec.Code != http.StatusOK {
			t.Errorf("%s = %d while 4 data requests are in flight", p, rec.Code)
		}
	}
	if rec := serveReq(h, newGet("/nope")); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown path = %d", rec.Code)
	}
	x.release(t, gate)
	for i, rec := range x.codes {
		if rec.Code != http.StatusOK {
			t.Errorf("held request %d = %d: the four admitted requests must all succeed", i, rec.Code)
		}
	}
	// The slots are free again, and refused requests leaked none.
	if rec := serveReq(h, newGet("/")); rec.Code != http.StatusOK {
		t.Errorf("after release = %d", rec.Code)
	}
}

// At most 8 requests of any kind are in flight; the 9th is refused at once.
func TestNinthInFlightRequestIsRefusedWithRetryAfter(t *testing.T) {
	gate, entered := make(chan struct{}), make(chan struct{}, 32)
	src := &gateSource{fakeSource: &fakeSource{rows: sampleRows()}, gate: gate, entered: entered}
	audit := &fakeAudit{entries: sampleAudit(), block: gate, entered: entered}
	h := NewHandlerOptions(src, nil, Options{Audit: audit})
	data := hold(t, h, "/", 4, entered)
	other := hold(t, h, "/audit", (8 - 4), entered) // 8 in flight in all
	for _, p := range []string{"/dashboard.css", "/dashboard.js", "/audit", "/api/audit", "/", "/api/state", "/session/opencode/x"} {
		refused(t, "9th in flight", h, p)
	}
	close(gate)
	data.wg.Wait()
	other.wg.Wait()
	for i, rec := range append(data.codes, other.codes...) {
		if rec.Code != http.StatusOK {
			t.Errorf("admitted request %d = %d: all eight must succeed", i, rec.Code)
		}
	}
	if rec := serveReq(h, newGet("/dashboard.css")); rec.Code != http.StatusOK {
		t.Errorf("after release = %d", rec.Code)
	}
}

// Many refused requests and many finished ones leak no slot; HEAD counts like GET.
func TestLimiterLeaksNoSlotsAndCountsHead(t *testing.T) {
	gate, entered := make(chan struct{}), make(chan struct{}, 16)
	src := &gateSource{fakeSource: &fakeSource{rows: sampleRows()}, gate: gate, entered: entered}
	h := NewHandler(src, nil)
	x := &held{}
	for i := 0; i < 4; i++ { // HEAD requests hold data slots too
		rec := httptest.NewRecorder()
		x.codes = append(x.codes, rec)
		x.wg.Add(1)
		go func() { defer x.wg.Done(); h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil)) }()
	}
	for i := 0; i < 4; i++ {
		<-entered
	}
	for i := 0; i < 200; i++ {
		if rec := serveReq(h, newGet("/")); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("request %d = %d", i, rec.Code)
		}
	}
	x.release(t, gate)
	for round := 0; round < 3; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if rec := serveReq(h, newGet("/")); rec.Code != http.StatusOK {
					t.Errorf("round %d = %d", round, rec.Code)
				}
			}()
		}
		wg.Wait()
	}
}

// The limits never queue under a burst: every request is answered 200 or 503 at once.
func TestBurstIsAnsweredAtOnceNeverQueued(t *testing.T) {
	gate, entered := make(chan struct{}), make(chan struct{}, 64)
	src := &gateSource{fakeSource: &fakeSource{rows: sampleRows()}, gate: gate, entered: entered}
	h := NewHandler(src, nil)
	x := hold(t, h, "/", 4, entered)
	var wg sync.WaitGroup
	start := time.Now()
	var mu sync.Mutex
	counts := map[int]int{}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := serveReq(h, newGet("/")).Code
			mu.Lock()
			counts[c]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if took := time.Since(start); took > 2*time.Second || counts[503] != 100 {
		t.Fatalf("burst of 100 took %v: %v (all must be refused at once)", took, counts)
	}
	x.release(t, gate)
}

// Every request that reaches a handler carries the 5 s bound, whatever the route.
func TestEveryRoutedRequestCarriesTheFiveSecondBound(t *testing.T) {
	seen := map[string]time.Duration{}
	var mu sync.Mutex
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dl, ok := r.Context().Deadline()
		mu.Lock()
		defer mu.Unlock()
		if ok {
			seen[r.URL.Path] = time.Until(dl)
		}
	})
	_ = probe
	// Through the real handler: the data routes record the deadline their source sees.
	src := &deadlineSource{fakeSource: &fakeSource{rows: sampleRows(), details: map[string]SessionDetail{"opencode/" + sesID: session("opencode", sesID)}}}
	h := NewHandler(src, nil)
	for _, p := range []string{"/", "/api/state", "/session/opencode/" + sesID, "/api/state/session/opencode/" + sesID} {
		serveReq(h, newGet(p))
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.remaining) < 4 {
		t.Fatalf("%d reads recorded", len(src.remaining))
	}
	for i, d := range src.remaining {
		if d <= 0 || d > requestBound {
			t.Errorf("read %d saw a remaining bound of %v, want within (0, %v]", i, d, requestBound)
		}
	}
	if requestBound != 5*time.Second {
		t.Errorf("requestBound = %v", requestBound)
	}
}

type deadlineSource struct {
	*fakeSource
	remaining []time.Duration
}

func (d *deadlineSource) note(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		d.remaining = append(d.remaining, time.Until(dl))
	} else {
		d.remaining = append(d.remaining, -1)
	}
}

func (d *deadlineSource) Sessions(ctx context.Context) ([]Session, error) {
	d.note(ctx)
	return d.rows, nil
}

func (d *deadlineSource) SessionDetail(ctx context.Context, harness, id string) (SessionDetail, error) {
	d.note(ctx)
	return d.fakeSource.details[harness+"/"+id], nil
}
