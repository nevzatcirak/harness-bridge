package dashboard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
)

// slowSource knows one session and never answers a diff read: every native read
// blocks until its context ends (ADR-023 T7, T10).
type slowSource struct {
	dashboard.NullSource
	entered chan struct{}
}

func (s slowSource) SessionDetail(_ context.Context, harness, id string) (dashboard.SessionDetail, error) {
	return dashboard.SessionDetail{Session: dashboard.Session{Harness: harness, ID: id, State: "running"}}, nil
}

func (s slowSource) block(ctx context.Context) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

func (s slowSource) SessionTurns(ctx context.Context, _, _ string) ([]dashboard.Turn, error) {
	return nil, s.block(ctx)
}

func (s slowSource) TurnDiff(ctx context.Context, _, _, _ string) (dashboard.DiffResult, error) {
	return dashboard.DiffResult{}, s.block(ctx)
}

func (s slowSource) WorkingTreeDiff(ctx context.Context, _, _ string) (dashboard.DiffResult, error) {
	return dashboard.DiffResult{}, s.block(ctx)
}

// stubRunner is the smallest native the execution manager needs to admit a send.
type stubRunner struct{}

func (stubRunner) Get(_ context.Context, id string) (opencode.Session, error) {
	return opencode.Session{ID: id, Directory: "/"}, nil
}
func (stubRunner) Permissions(context.Context) ([]opencode.Permission, error) { return nil, nil }
func (stubRunner) History(context.Context, string) ([]opencode.Message, error) {
	return nil, nil
}
func (stubRunner) Status(context.Context) (map[string]opencode.Status, error) {
	return map[string]opencode.Status{}, nil
}
func (stubRunner) Prompt(context.Context, string, string, string) error { return nil }
func (stubRunner) Abort(context.Context, string) error                  { return nil }

// T10 (slice 3): a native diff read that never returns holds only its own request,
// which ends at the 5 s bound as "unavailable"; a send_prompt admitted meanwhile is
// not delayed, because the dashboard shares no lock and no slot with the manager.
func TestSlowNativeDiffReadDoesNotBlockASend(t *testing.T) {
	src := slowSource{entered: make(chan struct{}, 8)}
	h := dashboard.NewHandler(src, nil)
	m := execution.NewWithTiming(stubRunner{}, time.Second, 2*time.Millisecond)
	t.Cleanup(m.Close)

	type result struct {
		code int
		body string
		took time.Duration
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/session/opencode/ses_slow1", nil))
		done <- result{rec.Code, rec.Body.String(), time.Since(start)}
	}()
	select {
	case <-src.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the dashboard never started its native diff read")
	}

	sendDone := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := m.Send(context.Background(), "ses_busy1", "prompt", "key-t10-00001")
		sendDone <- err
	}()
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("send took %v while a dashboard diff read was stuck", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send blocked behind a slow dashboard diff read (5 s deadline)")
	}

	select {
	case r := <-done:
		if r.code != http.StatusOK || !strings.Contains(r.body, `>unavailable</span>`) || !strings.Contains(r.body, "(timeout)") {
			t.Fatalf("slow page = %d\n%s", r.code, r.body)
		}
		if r.took > 5*time.Second+2*time.Second {
			t.Fatalf("the page took %v, past the 5 s bound", r.took)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the dashboard request outlived its bound")
	}
}

// ADR-023 section 4 (third test) and T11: dashboard traffic, whatever the method and
// route, creates no execution, takes or frees no guard and changes no execution the
// manager holds (the dashboard has no path to the manager's writers at all). The
// execution may finish on its own meanwhile, so only what traffic could add is compared.
func TestDashboardTrafficDoesNotChangeTheManager(t *testing.T) {
	m := execution.NewWithTiming(stubRunner{}, time.Second, 2*time.Millisecond)
	t.Cleanup(m.Close)
	x, err := m.Send(context.Background(), "ses_busy2", "prompt", "key-t11-00001")
	if err != nil {
		t.Fatal(err)
	}
	before := m.DashboardSnapshot()
	if len(before.Executions) != 1 || before.Executions[0].ID != x.ID {
		t.Fatalf("setup: %+v", before)
	}
	h := dashboard.NewHandlerOptions(dashboard.NullSource{}, nil, dashboard.Options{Audit: dashboard.NewAuditRing()})
	for _, p := range []string{"/", "/audit", "/api/audit", "/api/state", "/api/state/session/opencode/ses_busy2", "/session/opencode/ses_busy2", "/session/opencode/ses_busy2/turn/msg_a", "/dashboard.js", "/execution/" + x.ID, "/admin", "/send_prompt"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, p, strings.NewReader("prompt=x")))
		}
	}
	after := m.DashboardSnapshot()
	if len(after.Executions) != 1 || after.Executions[0].ID != x.ID || after.Executions[0].SessionID != before.Executions[0].SessionID {
		t.Fatalf("dashboard traffic changed the manager: %+v -> %+v", before, after)
	}
}
