package dashboard

// ADR-023 A1.10 item 1 (slice S1): golden HTML snapshots of the pages and states this slice serves, with a fixed
// clock and a fixture source. A change to the markup shows up as a diff of a committed file. To accept an
// intended change run: go test ./internal/dashboard -run TestV2S1Golden -update-golden

import (
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the golden HTML snapshots under testdata/golden")

func goldenPages(t *testing.T) map[string]string {
	t.Helper()
	pages := v2Pages(t)
	// The audit page with the feed on: one row of every kind of cell.
	opts := v2Opts()
	opts.Audit = fakeAuditOf(AuditEntry{Time: time.Date(2026, 11, 21, 17, 40, 0, 0, time.UTC), Event: "interrupt", Message: "interrupt",
		Attrs: []AuditAttr{{Key: "event", Value: "interrupt"}, {Key: "outcome", Value: "accepted"}, {Key: "session_id", Value: "ses_demo0001"}, {Key: "principal", Value: "operator"}, {Key: "reason", Value: "user"}}})
	_, body, _ := v2Get(t, NewHandlerOptions(&fakeSource{}, nil, opts), "/audit")
	pages["audit-on"] = body
	return pages
}

type fakeAuditEntries []AuditEntry

func (f fakeAuditEntries) Entries() []AuditEntry { return f }

func fakeAuditOf(e ...AuditEntry) AuditSource { return fakeAuditEntries(e) }

func TestV2S1GoldenSnapshots(t *testing.T) {
	dir := filepath.Join("testdata", "golden")
	pages := goldenPages(t)
	if *updateGolden {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range pages {
			if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no golden snapshots (run with -update-golden): %v", err)
	}
	if len(entries) != len(pages) {
		t.Errorf("%d golden files for %d pages", len(entries), len(pages))
	}
	for name, body := range pages {
		want, err := os.ReadFile(filepath.Join(dir, name+".html"))
		if err != nil {
			t.Errorf("%s: no golden file", name)
			continue
		}
		if string(want) != body {
			t.Errorf("%s: the rendered page differs from testdata/golden/%s.html (run with -update-golden if the change is intended)", name, name)
		}
	}
}

// The snapshots are produced with a fixed clock: two renders of one page are byte-identical.
func TestV2S1RenderIsDeterministic(t *testing.T) {
	a, b := goldenPages(t), goldenPages(t)
	for name := range a {
		if a[name] != b[name] {
			t.Errorf("%s: two renders differ", name)
		}
	}
	_, _, hdr := v2Get(t, v2Handler(&fakeSource{rows: v2Rows()}), "/")
	if hdr.Get("Content-Type") != "text/html; charset=utf-8" || hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", hdr)
	}
	_ = http.StatusOK
}
