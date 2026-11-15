package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nevzatcirak/harness-bridge/internal/dashboard"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// ADR-023 slice 5b (D17 Q6, Q7): lifecycle roots are a LABEL on the sessions the existing
// authorization lists (the rule of list_catalog, ADR-022 6.1), and archived sessions are hidden
// by default with a filter. Nothing here lists a session the ACL does not.

type archiveFake struct {
	archived map[string]bool
	err      error
}

func (a archiveFake) Archived(_ context.Context, id string) (bool, error) {
	return a.archived[id], a.err
}

// lifecycleServer builds the dash fixture with a lifecycle root <root>/life and an ACL of
// ses_a and ses_b (outside it) and ses_life (inside it).
func lifecycleServer(t *testing.T, ov ArchiveOverlay) (*Server, *dashNative, string) {
	t.Helper()
	s, n := newDashServer(t, []string{"ses_a", "ses_b", "ses_life"}, nil)
	life := filepath.Join(n.root, "life")
	if err := os.MkdirAll(filepath.Join(life, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots, err := security.NewLifecycleRoots([]string{life})
	if err != nil {
		t.Fatal(err)
	}
	s.Catalog = &CatalogConfig{Roots: roots, Overlay: ov}
	return s, n, life
}

func rowsByID(rows []dashboard.Session) map[string]dashboard.Session {
	m := map[string]dashboard.Session{}
	for _, r := range rows {
		m[r.ID] = r
	}
	return m
}

// The dashboard lists exactly the sessions list_catalog lists for the same configuration, and
// its lifecycle-root label is the catalog's root.
func TestDashboardSourceLifecycleRootLabelEqualsListCatalog(t *testing.T) {
	s, _, life := lifecycleServer(t, nil)
	out, err := s.call(context.Background(), "list_catalog", args{})
	if err != nil {
		t.Fatal(err)
	}
	cat := out.(Catalog)
	want := map[string]string{}
	for _, e := range cat.Sessions {
		want[strings.TrimPrefix(e.SessionRef.SessionID, "opencode:")] = e.Root
	}
	if len(want) != 3 || want["ses_life"] != life || want["ses_a"] != "" {
		t.Fatalf("catalog: %v", want)
	}
	rows, err := s.DashboardSource(nil).Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) {
		t.Fatalf("dashboard rows %d, catalog sessions %d", len(rows), len(want))
	}
	for _, r := range rows {
		if label, ok := want[r.ID]; !ok || r.LifecycleRoot != label {
			t.Errorf("%s: lifecycle root %q, catalog root %q (listed %v)", r.ID, r.LifecycleRoot, label, ok)
		}
	}
	// ordering is unchanged: OpenCode rows by native ID.
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("rows not sorted by ID: %v", ids)
	}
}

// A session under a lifecycle root that the ACL does not list is NOT shown: the label widens
// nothing (the same rule as list_catalog; reading it would need the O2 flag, D16 Q7).
func TestDashboardSourceLifecycleRootsWidenNothing(t *testing.T) {
	s, n := newDashServer(t, []string{"ses_a"}, nil) // ses_life exists natively but is not in the ACL
	life := filepath.Join(n.root, "life")
	if err := os.MkdirAll(filepath.Join(life, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	roots, err := security.NewLifecycleRoots([]string{life})
	if err != nil {
		t.Fatal(err)
	}
	s.Catalog = &CatalogConfig{Roots: roots}
	src := s.DashboardSource(nil)
	rows, err := src.Sessions(context.Background())
	if err != nil || len(rows) != 1 || rows[0].ID != "ses_a" {
		t.Fatalf("%+v %v", rows, err)
	}
	if _, err := src.SessionDetail(context.Background(), "opencode", "ses_life"); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("a lifecycle-root session outside the ACL: %v", err)
	}
	for _, r := range n.log() {
		if strings.Contains(r, "ses_life") {
			t.Errorf("a native read of a session the ACL does not list: %s", r)
		}
	}
}

// D17 Q7: archived sessions are hidden by default, shown with the filter, and equal to
// list_catalog with include_archived.
func TestDashboardSourceArchivedFilter(t *testing.T) {
	s, _, _ := lifecycleServer(t, archiveFake{archived: map[string]bool{"opencode:ses_b": true}})
	ctx := context.Background()
	src := s.DashboardSource(nil)
	rows, err := src.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rowsByID(rows)["ses_b"]; ok || len(rows) != 2 {
		t.Fatalf("an archived session is listed by default: %+v", rows)
	}
	rows, err = src.Sessions(dashboard.WithArchived(ctx))
	if err != nil {
		t.Fatal(err)
	}
	b, ok := rowsByID(rows)["ses_b"]
	if !ok || !b.Archived || len(rows) != 3 || rowsByID(rows)["ses_a"].Archived {
		t.Fatalf("with the filter: %+v", rows)
	}
	cat, err := s.call(ctx, "list_catalog", args{IncludeArchived: true})
	if err != nil || len(cat.(Catalog).Sessions) != 3 {
		t.Fatalf("catalog with include_archived: %v %v", cat, err)
	}
	// the detail follows the same rule.
	if _, err := src.SessionDetail(ctx, "opencode", "ses_b"); !errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("archived detail without the filter: %v", err)
	}
	d, err := src.SessionDetail(dashboard.WithArchived(ctx), "opencode", "ses_b")
	if err != nil || !d.Archived {
		t.Fatalf("archived detail with the filter: %+v %v", d, err)
	}
}

// An overlay whose answer is unknown fails the list (never listed or hidden by a guess), like
// list_catalog; the detail fails too.
func TestDashboardSourceUnknownArchiveStateFailsClosed(t *testing.T) {
	s, _, _ := lifecycleServer(t, archiveFake{err: errors.New("overlay unavailable")})
	src := s.DashboardSource(nil)
	if rows, err := src.Sessions(context.Background()); err == nil || errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("an unknown archive state listed %d rows: %v", len(rows), err)
	}
	if _, err := src.SessionDetail(context.Background(), "opencode", "ses_a"); err == nil || errors.Is(err, dashboard.ErrNotFound) {
		t.Fatalf("detail with an unknown archive state: %v", err)
	}
	if _, err := s.call(context.Background(), "list_catalog", args{}); err == nil {
		t.Fatal("the catalog does not fail on an unknown archive state: the premise of this test is gone")
	}
}
