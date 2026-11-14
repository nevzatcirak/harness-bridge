package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/security"
)

// ADR-022 section 6.1 to 6.3 and slice S3: the read-only list_catalog tool.
//
// Which sessions it lists: exactly the sessions the existing authorization lists
// (the immutable operator ACL, through the same bounded discovery as find_sessions).
// The lifecycle roots add the "root" label and filter; they widen nothing, and the
// per-root existing_tools flag (O2) still grants nothing (slice S9). Claude Code
// entries are omitted, not guessed: the ADR-021 adapter is not wired.

// catalogDeadline is the one total deadline of a list_catalog call (ADR-022 6.1). A
// variable only so a test can shorten it; nothing else changes it.
var catalogDeadline = 10 * time.Second

// ArchiveOverlay is the READ-ONLY view of the Bridge archive overlay (ADR-022 D16 Q1
// option B, Q5). Nothing in this package can write it. The production implementation
// is EmptyArchiveOverlay: no persistence exists yet (slice S8, then S6), so nothing is
// overlay-archived. An error means the answer is unknown and fails the whole call: a
// session whose archive state is unknown is never listed or hidden by a guess.
type ArchiveOverlay interface {
	Archived(ctx context.Context, sessionID string) (bool, error)
}

// EmptyArchiveOverlay is the production overlay of slice S3: nothing is archived.
type EmptyArchiveOverlay struct{}

// Archived implements ArchiveOverlay.
func (EmptyArchiveOverlay) Archived(context.Context, string) (bool, error) { return false, nil }

// CatalogConfig enables list_catalog. A Server whose Catalog is nil does not
// register the tool (ADR-022 3.1: absent lifecycle configuration, no lifecycle
// tools), so the tool set of an existing deployment is unchanged.
type CatalogConfig struct {
	// Roots are the validated lifecycle roots; an entry's "root" is the one that
	// lexically contains its directory (the directory is already authorized by the
	// ACL, whose check resolves symlinks). Required.
	Roots *security.LifecycleRoots
	// Overlay is read-only. Nil means EmptyArchiveOverlay.
	Overlay ArchiveOverlay
}

func (c *CatalogConfig) overlay() ArchiveOverlay {
	if c == nil || c.Overlay == nil {
		return EmptyArchiveOverlay{}
	}
	return c.Overlay
}

// CatalogInput is the list_catalog request. Every filter is optional and an exact match.
type CatalogInput struct {
	Root            string `json:"root,omitempty"`
	State           string `json:"state,omitempty"`
	UpdatedSince    string `json:"updated_since,omitempty"`
	CreatedByBridge *bool  `json:"created_by_bridge,omitempty"`
	HarnessName     string `json:"harness_name,omitempty"`
	IncludeArchived bool   `json:"include_archived,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	Offset          int    `json:"offset,omitempty"`
}

// SessionRef is the first step toward an instance-qualified session identity (ADR-022
// 6.2). It is not the ADR-012 versioned ID: Harness is the "harness:<hash>" identity of the
// native instance and SessionID the existing "opencode:<id>" form.
type SessionRef struct {
	Harness   string `json:"harness"`
	SessionID string `json:"session_id"`
}

// CatalogEntry is one listed session.
type CatalogEntry struct {
	SessionRef  SessionRef `json:"session_ref"`
	HarnessName string     `json:"harness_name"`
	Directory   string     `json:"directory"`
	// Root is the lifecycle root that contains Directory; omitted when none does.
	Root  string `json:"root,omitempty"`
	Title string `json:"title"`
	State string `json:"state"`
	// UpdatedAt is the native time.updated; omitted when the session carries none.
	UpdatedAt string `json:"updated_at,omitempty"`
	// CreatedByBridge is a best-effort provenance LABEL (metadata.hb_origin), never an authority.
	CreatedByBridge bool `json:"created_by_bridge"`
	// Archived is true only for an overlay-archived session, which is listed only
	// with include_archived.
	Archived bool `json:"archived,omitempty"`
}

// CatalogWindow is the page the aggregates describe.
type CatalogWindow struct {
	Offset   int `json:"offset"`
	Limit    int `json:"limit"`
	Returned int `json:"returned"`
}

// CatalogAggregates are computed from the returned page only and say so; they are not
// whole-fleet totals (ADR-022 6.2).
type CatalogAggregates struct {
	Scope   string         `json:"scope"`
	Window  CatalogWindow  `json:"window"`
	Total   int            `json:"total"`
	ByState map[string]int `json:"by_state"`
	ByRoot  map[string]int `json:"by_root"`
}

// Catalog is the list_catalog result.
type Catalog struct {
	// Harnesses are the harness kinds this catalog can list. A kind that is not here (Claude
	// Code, until ADR-021 is wired) is omitted, not guessed.
	Harnesses  []string          `json:"harnesses"`
	Sessions   []CatalogEntry    `json:"sessions"`
	NextOffset *int              `json:"next_offset,omitempty"`
	Aggregates CatalogAggregates `json:"aggregates"`
}

var catalogStates = []string{"idle", "running", "waiting_for_permission", "unknown"}

var harnessNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

const maxCatalogRootBytes = 4096

// catalogFilter is a validated list_catalog request.
type catalogFilter struct {
	root         string
	state        string
	since        time.Time
	hasSince     bool
	bridge       *bool
	harness      string
	includeArch  bool
	limit, start int
}

// parseCatalogFilter validates the catalog arguments. Every refusal is INVALID_INPUT
// before any native read.
func parseCatalogFilter(a args) (catalogFilter, error) {
	f := catalogFilter{root: a.Root, state: a.State, bridge: a.CreatedByBridge, harness: a.HarnessName, includeArch: a.IncludeArchived, limit: a.Limit, start: a.Offset}
	bad := func(msg string) (catalogFilter, error) { return catalogFilter{}, execution.Fail("INVALID_INPUT", msg) }
	if f.root != "" && (len(f.root) > maxCatalogRootBytes || !utf8.ValidString(f.root) || !filepath.IsAbs(f.root) || filepath.Clean(f.root) != f.root) {
		return bad("root must be an absolute, clean path")
	}
	if f.state != "" {
		ok := false
		for _, s := range catalogStates {
			ok = ok || s == f.state
		}
		if !ok {
			return bad("state must be one of idle, running, waiting_for_permission, unknown")
		}
	}
	if a.UpdatedSince != "" {
		t, err := time.Parse(time.RFC3339, a.UpdatedSince)
		if err != nil || len(a.UpdatedSince) > 40 {
			return bad("updated_since must be an RFC 3339 time")
		}
		f.since, f.hasSince = t, true
	}
	if f.harness != "" && !harnessNamePattern.MatchString(f.harness) {
		return bad("invalid harness_name")
	}
	if f.limit == 0 {
		f.limit = 50
	}
	return f, nil
}

// catalogUnavailable is the typed result of a catalog read that did not complete: no
// partial page is ever returned in its place.
func catalogUnavailable() error {
	return &execution.Error{Code: "UPSTREAM_TIMEOUT", Message: "catalog read did not complete within its deadline; no partial result is returned", Retryable: true}
}

func (s *Server) catalogCall(ctx context.Context, a args) (any, error) {
	if s.Catalog == nil || s.Catalog.Roots == nil {
		return nil, execution.Fail("INVALID_INPUT", "unknown tool")
	}
	f, err := parseCatalogFilter(a)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, catalogDeadline)
	defer cancel()
	timedOut := func(e error) bool {
		return errors.Is(e, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded
	}
	all, err := s.discoverNative(ctx)
	if err != nil {
		if timedOut(err) {
			return nil, catalogUnavailable()
		}
		return nil, err
	}
	overlay := s.Catalog.overlay()
	matched := []CatalogEntry{}
	for _, d := range all {
		v := d.view
		if f.harness != "" && f.harness != v.Harness {
			continue
		}
		if f.state != "" && f.state != v.State {
			continue
		}
		root := s.Catalog.Roots.RootOf(v.Directory)
		if f.root != "" && f.root != root {
			continue
		}
		if f.bridge != nil && *f.bridge != d.native.CreatedByBridge() {
			continue
		}
		updated, hasUpdated := d.native.UpdatedAt()
		if f.hasSince && (!hasUpdated || updated.Before(f.since)) {
			continue // a session with no usable update time cannot satisfy the filter
		}
		archived, e := overlay.Archived(ctx, v.SessionID)
		if e != nil {
			if timedOut(e) {
				return nil, catalogUnavailable()
			}
			return nil, execution.Fail("INTERNAL", "archive overlay unavailable; the catalog is not returned without it")
		}
		if archived && !f.includeArch {
			continue
		}
		entry := CatalogEntry{
			SessionRef:  SessionRef{Harness: v.HarnessInstanceID, SessionID: v.SessionID},
			HarnessName: v.Harness, Directory: v.Directory, Root: root, Title: v.Title, State: v.State,
			CreatedByBridge: d.native.CreatedByBridge(), Archived: archived,
		}
		if hasUpdated {
			entry.UpdatedAt = updated.Format(time.RFC3339)
		}
		matched = append(matched, entry)
	}
	if ctx.Err() != nil {
		return nil, catalogUnavailable()
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].SessionRef.SessionID < matched[j].SessionRef.SessionID })
	start := f.start
	if start > len(matched) {
		start = len(matched)
	}
	end := start + f.limit
	if end > len(matched) {
		end = len(matched)
	}
	page := matched[start:end]
	out := Catalog{Harnesses: []string{"opencode"}, Sessions: nonnil(page)}
	if end < len(matched) {
		out.NextOffset = &end
	}
	out.Aggregates = CatalogAggregates{Scope: "page", Window: CatalogWindow{Offset: start, Limit: f.limit, Returned: len(page)}, Total: len(page),
		ByState: map[string]int{}, ByRoot: map[string]int{}}
	for _, st := range catalogStates {
		out.Aggregates.ByState[st] = 0
	}
	for _, e := range page {
		out.Aggregates.ByState[e.State]++
		if e.Root != "" {
			out.Aggregates.ByRoot[e.Root]++
		}
	}
	return out, nil
}
