package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ADR-023 Amendment A1, slice S3a: the top bar's instance label and token-expiry badge, /executions, /catalog,
// /health and their JSON twins. Every page is server-rendered and needs no script (the dashboard.js size cap is not
// touched); the Refresh link is the no-JS refresh of A1.7. Everything that comes from a Source is escaped by
// html/template, and the JSON twins carry no clock-dependent text, so their ETags stay stable.

// ---- the top bar (A1.3, A1.8) ---------------------------------------------------------------------------------

// topBadge is the connector-token expiry badge of the top bar: remote mode only, neutral, warn under 60 minutes,
// error once expired (A1.3). The absolute time is in the title.
type topBadge struct {
	Text, Class, Title string
}

// expiryBadge turns the token's expiry into the badge text.
func expiryBadge(now, expires time.Time) topBadge {
	left := expires.Sub(now)
	b := topBadge{Title: "Connector token expiry " + fmtTime(expires)}
	switch {
	case left <= 0:
		b.Text, b.Class = "Token expired", chipClass("error")
	case left < time.Hour:
		b.Text, b.Class = "Token expires in "+strconv.Itoa(int((left+time.Minute-1)/time.Minute))+" min", chipClass("warn")
	default:
		b.Text, b.Class = "Token expires in "+tokenLeft(left), chipClass("neutral")
	}
	return b
}

// tokenLeft is a remaining lifetime of at least one hour: "5 h 02 min" or "3 d 4 h".
func tokenLeft(d time.Duration) string {
	s := int64(d / time.Second)
	if s >= 86400 {
		return fmt.Sprintf("%d d %d h", s/86400, (s%86400)/3600)
	}
	return fmt.Sprintf("%d h %02d min", s/3600, (s%3600)/60)
}

// healthCache keeps the last Source.Health answer for one second: the top bar asks for it on every page, and
// Health is memory only, so this only keeps the page cost flat under a refresh storm.
type healthCache struct {
	mu  sync.Mutex
	at  time.Time
	h   Health
	err error
	ok  bool
}

func (d *dash) health(ctx context.Context) (Health, error) {
	if _, none := d.src.(NullSource); none {
		return Health{}, nil
	}
	d.hc.mu.Lock()
	defer d.hc.mu.Unlock()
	if d.hc.ok && d.now().Sub(d.hc.at) < modelTTL {
		return d.hc.h, d.hc.err
	}
	d.hc.h, d.hc.err = d.src.Health(ctx)
	d.hc.at, d.hc.ok = d.now(), true
	return d.hc.h, d.hc.err
}

// decorate sets what the top bar shows on every page: the instance label (a plain-text variable) and, in remote
// mode, the token-expiry badge. A Source that cannot answer just leaves the badge out: the page never fails for it.
func (d *dash) decorate(ctx context.Context, p *page) {
	p.TopLabel = d.label
	if h, err := d.health(ctx); err == nil && h.TokenExpires != nil {
		b := expiryBadge(d.now().UTC(), *h.TokenExpires)
		p.TopBadge = &b
	}
}

// page decorates and renders. Every handler of the dashboard renders through it.
func (d *dash) render(ctx context.Context, w http.ResponseWriter, status int, p page) {
	d.decorate(ctx, &p)
	renderPage(w, status, p)
}

// ---- /executions (A1.4 item 3) --------------------------------------------------------------------------------

// maxExecListRows is the cap of the /executions page and its twin. The Source already caps after its scope filter;
// this bounds a Source that does not.
const maxExecListRows = 200

type execListRow struct {
	ID, State, StateLabel, StateClass string
	Started, StartedISO, StartedRel   string
	Duration                          string
	SessionID, SessionText            string // the harness-qualified ID, and the part after the first colon
	SessionHref                       string
	Current                           bool
	Live                              livenessCell
	Liveness                          *Liveness
	UnavailableSince                  *time.Time
	startedAt                         time.Time
	endedAt                           *time.Time
}

// splitSessionID splits a harness-qualified session ID at its FIRST colon ("opencode:ses_x" is harness "opencode"
// and ID "ses_x"; an ID may itself hold a colon). Nothing else about it is trusted.
func splitSessionID(q string) (harness, id string, ok bool) {
	return strings.Cut(q, ":")
}

func (d *dash) execListRows(ctx context.Context) (rows []execListRow, capped bool, err error) {
	execs, err := d.src.Executions(ctx)
	if err != nil {
		return nil, false, err
	}
	if len(execs) > maxExecListRows {
		execs, capped = execs[:maxExecListRows], true
	}
	suffix := ""
	if ArchivedShown(ctx) {
		suffix = "?archived=1" // an archived session's page is a 404 without the filter (D17 Q7)
	}
	for _, e := range execs {
		state := normExecutionState(e.State)
		c := stateChip(state)
		r := execListRow{ID: truncRunes(e.ID, 128), State: state, StateLabel: c.Label, StateClass: chipClass(c.Tone), Current: e.Current,
			Started: fmtTime(e.Started), StartedISO: isoTime(e.Started), Live: cellFor(e), startedAt: e.Started, endedAt: e.Ended,
			SessionID: truncRunes(e.SessionID, 160), Liveness: e.Liveness, UnavailableSince: e.UnavailableSince}
		if h, id, ok := splitSessionID(e.SessionID); ok {
			r.SessionText = truncRunes(id, 128)
			if href := sessionHref(h, id); href != "" {
				r.SessionHref = href + suffix
			}
		} else {
			r.SessionText = truncRunes(e.SessionID, 128) // not a qualified ID: shown, never linked
		}
		rows = append(rows, r)
	}
	return rows, capped, nil
}

// clockExecRows sets the clock-dependent text of the HTML table (relative start, duration); never the JSON.
func clockExecRows(rows []execListRow, now time.Time) {
	for i := range rows {
		r := &rows[i]
		r.StartedRel = relTime(now, r.startedAt)
		switch {
		case r.startedAt.IsZero():
			r.Duration = "-"
		case r.endedAt != nil:
			r.Duration = durationText(r.endedAt.Sub(r.startedAt))
		case terminalState(r.State):
			r.Duration = "-"
		default:
			r.Duration = "Running for " + durationText(now.Sub(r.startedAt))
		}
	}
}

func (d *dash) executionsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		p := page{Title: "Executions - Cirak", Active: "executions", ExecPage: true}
		p.RefreshHref, p.ToggleHref, p.ToggleText = "/executions", "/executions?archived=1", "Show archived"
		if ArchivedShown(ctx) {
			p.RefreshHref, p.ToggleHref, p.ToggleText = "/executions?archived=1", "/executions", "Hide archived"
		}
		if _, none := d.src.(NullSource); none {
			p.State = &stateBlock{Class: "state", Heading: "No data source", Text: "No data source is connected."}
			d.render(ctx, w, http.StatusOK, p)
			return
		}
		rows, capped, err := d.execListRows(ctx)
		if err != nil {
			p.State = &stateBlock{Class: "state state--error", Heading: "Data source unavailable",
				Text: "The Bridge could not read its executions just now. Nothing was changed. Try again in a few seconds.", LinkHref: p.RefreshHref, LinkText: "Try again"}
			d.unavailablePage(ctx, w, p)
			return
		}
		now := d.now().UTC()
		clockExecRows(rows, now)
		p.ExecRows = rows
		p.StatusISO, p.StatusAbs, p.StatusRel = isoTime(now), fmtTime(now), relTime(now, now)
		p.CountText = executionsCountText(len(rows), capped)
		if len(rows) == 0 {
			p.State = &stateBlock{Class: "state", Heading: "No executions", Text: "This Bridge process has recorded no execution for a session you may see. A restart empties the list. Executions of archived sessions are shown only when you ask for them.",
				LinkHref: p.ToggleHref, LinkText: p.ToggleText}
		}
		d.render(ctx, w, http.StatusOK, p)
	})
}

func executionsCountText(n int, capped bool) string {
	if capped {
		return fmt.Sprintf("the newest %d executions", n)
	}
	if n == 1 {
		return "1 execution"
	}
	return fmt.Sprintf("%d executions", n)
}

type apiExecRow struct {
	ID          string       `json:"id"`
	State       string       `json:"state"`
	StateLabel  string       `json:"state_label"`
	StateTone   string       `json:"state_tone"`
	Started     string       `json:"started"`
	StartedISO  string       `json:"started_iso,omitempty"`
	Ended       string       `json:"ended"`
	SessionID   string       `json:"session_id"`
	SessionHref string       `json:"session_href,omitempty"`
	Current     bool         `json:"current,omitempty"`
	Liveness    *apiLiveness `json:"liveness,omitempty"`
}

type apiExecList struct {
	Kind       string       `json:"kind"`
	Connected  bool         `json:"connected"`
	Count      int          `json:"count"`
	Capped     bool         `json:"capped,omitempty"`
	Executions []apiExecRow `json:"executions"`
}

func (d *dash) buildExecutions(ctx context.Context) ([]byte, error) {
	m := apiExecList{Kind: "executions", Executions: []apiExecRow{}}
	if _, none := d.src.(NullSource); none {
		return json.Marshal(m)
	}
	rows, capped, err := d.execListRows(ctx)
	if err != nil {
		return nil, err
	}
	m.Connected, m.Count, m.Capped = true, len(rows), capped
	for _, r := range rows {
		x := apiExecRow{ID: r.ID, State: r.State, StateLabel: r.StateLabel, StateTone: toneOfClass(r.StateClass), Started: r.Started, StartedISO: r.StartedISO,
			SessionID: r.SessionID, SessionHref: r.SessionHref, Current: r.Current}
		x.Ended = fmtTimePtr(r.endedAt)
		x.Liveness = apiLivenessFor(r.Liveness, r.UnavailableSince, r.Live)
		m.Executions = append(m.Executions, x)
	}
	return json.Marshal(m)
}

// toneOfClass is the tone of a chip class: "chip chip--ok" is "ok", "chip" is "neutral".
func toneOfClass(class string) string {
	if t, ok := strings.CutPrefix(class, "chip chip--"); ok {
		return t
	}
	return "neutral"
}

func (d *dash) executionsJSONHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := d.model(r.Context(), "state\x00executions"+archivedCacheKey(r.Context()), d.buildExecutions)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeModel(w, r, m)
	})
}

// ---- /catalog (A1.4 item 4) -----------------------------------------------------------------------------------

// catalogGroup is the sessions of one lifecycle root. The root is a LABEL only: it widens nothing (ADR-022 6.1).
type catalogGroup struct {
	Root     string // "" for sessions under no lifecycle root
	Rows     []sessionRow
	Anchor   string
	Heading  string
	CountTxt string
}

// groupCatalog groups the rows by lifecycle root: the roots in alphabetical order, then the sessions with none. The
// 200-row cap of the Sessions page applies to the whole catalog, so a hidden session never appears and the page stays
// as small as the list.
func groupCatalog(rows []Session) (groups []catalogGroup, truncated string) {
	shown, trunc := listRows(rows)
	byRoot := map[string][]sessionRow{}
	var roots []string
	for _, r := range shown {
		if _, seen := byRoot[r.LifecycleRoot]; !seen && r.LifecycleRoot != "" {
			roots = append(roots, r.LifecycleRoot)
		}
		byRoot[r.LifecycleRoot] = append(byRoot[r.LifecycleRoot], r)
	}
	sort.Strings(roots)
	for i, root := range roots {
		groups = append(groups, catalogGroup{Root: root, Rows: byRoot[root], Anchor: "g" + strconv.Itoa(i), CountTxt: sessionsCountText(len(byRoot[root]))})
	}
	if none := byRoot[""]; len(none) > 0 {
		groups = append(groups, catalogGroup{Rows: none, Anchor: "g-none", CountTxt: sessionsCountText(len(none))})
	}
	return groups, trunc
}

func (d *dash) catalogHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		now := d.now().UTC()
		p := page{Title: "Catalog and archived - Cirak", Active: "catalog", CatalogPage: true}
		p.RefreshHref, p.ToggleHref, p.ToggleText = "/catalog", "/catalog?archived=1", "Show archived"
		if ArchivedShown(ctx) {
			p.RefreshHref, p.ToggleHref, p.ToggleText = "/catalog?archived=1", "/catalog", "Hide archived"
		}
		if _, none := d.src.(NullSource); none {
			p.State = &stateBlock{Class: "state", Heading: "No data source", Text: "No data source is connected."}
			d.render(ctx, w, http.StatusOK, p)
			return
		}
		rows, err := d.src.Sessions(ctx)
		if err != nil {
			p.State = &stateBlock{Class: "state state--error", Heading: "Data source unavailable",
				Text: "The Bridge could not read its sessions just now. Nothing was changed. Try again in a few seconds.", LinkHref: p.RefreshHref, LinkText: "Try again"}
			d.unavailablePage(ctx, w, p)
			return
		}
		p.Groups, p.Truncate = groupCatalog(rows)
		for gi := range p.Groups {
			for i := range p.Groups[gi].Rows {
				p.Groups[gi].Rows[i].LastRel = relTimeISO(now, p.Groups[gi].Rows[i].LastISO)
			}
		}
		p.CountText = sessionsCountText(len(rows))
		p.StatusISO, p.StatusAbs, p.StatusRel = isoTime(now), fmtTime(now), relTime(now, now)
		if len(p.Groups) == 0 {
			p.State = &stateBlock{Class: "state", Heading: "No sessions", Text: "This Bridge lists no session you may see. Archived sessions are hidden unless you ask for them.",
				LinkHref: p.ToggleHref, LinkText: p.ToggleText}
		}
		d.render(ctx, w, http.StatusOK, p)
	})
}

type apiCatalogGroup struct {
	Root     string       `json:"lifecycle_root"`
	Sessions []apiSession `json:"sessions"`
}

type apiCatalog struct {
	Kind      string            `json:"kind"`
	Connected bool              `json:"connected"`
	Count     int               `json:"count"`
	Truncated string            `json:"truncated,omitempty"`
	Groups    []apiCatalogGroup `json:"groups"`
}

func (d *dash) buildCatalog(ctx context.Context) ([]byte, error) {
	m := apiCatalog{Kind: "catalog", Groups: []apiCatalogGroup{}}
	if _, none := d.src.(NullSource); none {
		return json.Marshal(m)
	}
	rows, err := d.src.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	m.Connected, m.Count = true, len(rows)
	groups, trunc := groupCatalog(rows)
	m.Truncated = trunc
	for _, g := range groups {
		ag := apiCatalogGroup{Root: g.Root, Sessions: []apiSession{}}
		for _, r := range g.Rows {
			ag.Sessions = append(ag.Sessions, toAPISession(r))
		}
		m.Groups = append(m.Groups, ag)
	}
	return json.Marshal(m)
}

func (d *dash) catalogJSONHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := d.model(r.Context(), "state\x00catalog"+archivedCacheKey(r.Context()), d.buildCatalog)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeModel(w, r, m)
	})
}

// ---- /health (A1.4 item 6) ------------------------------------------------------------------------------------

// healthRow is one key/value row of the About and health page: a label and parts (the same part kinds the detail
// page renders).
type healthRow struct {
	Label string `json:"label"`
	Parts []part `json:"parts"`
}

var onOff = map[bool]chip{true: {Label: "On", Tone: "ok", Raw: "on"}, false: {Label: "Off", Tone: "neutral", Raw: "off"}}

var reachChips = map[string]chip{
	"reachable":   {Label: "Reachable", Tone: "ok", Raw: "reachable"},
	"unreachable": {Label: "Unreachable", Tone: "error", Raw: "unreachable"},
	"not read":    {Label: "Not read", Tone: "neutral", Raw: "not_read"},
}

var harnessNames = map[string]string{"opencode": "OpenCode", "claudecode": "Claude Code"}

// buildHealthRows turns the Source's Health, the instance label and the audit feed into the rows of the page. A field
// the Source does not know is left out, never defaulted (A1.8): the build row is the one exception, which says
// "unknown" because the ADR says so.
func buildHealthRows(h Health, label string, audit AuditSource) []healthRow {
	var rows []healthRow
	text := func(l, v string) { rows = append(rows, healthRow{Label: l, Parts: []part{textPart(v)}}) }
	if label != "" {
		rows = append(rows, healthRow{Label: "Instance", Parts: []part{textPart(label)}})
	}
	version := truncRunes(h.Version, 64)
	if version == "" {
		version = "unknown"
	}
	text("Version", version)
	if h.Build != "" { // the whole VCS revision (40 hex characters), bounded (#576 P3-5)
		rows = append(rows, healthRow{Label: "Build", Parts: []part{codePart(truncRunes(h.Build, 64))}})
	} else {
		text("Build", "unknown")
	}
	if h.TokenExpires != nil {
		rows = append(rows, healthRow{Label: "Connector token expires", Parts: []part{absTimePart(*h.TokenExpires)}})
	}
	for _, f := range h.Features {
		rows = append(rows, healthRow{Label: truncRunes(f.Name, 40), Parts: []part{chipPart(onOff[f.On])}})
	}
	for _, r := range h.Reach {
		c, ok := reachChips[r.State]
		if !ok {
			c = unknownChip
		}
		name := harnessNames[r.Harness]
		if name == "" {
			name = "Other harness"
		}
		parts := []part{chipPart(c)}
		if !r.At.IsZero() {
			parts = append(parts, textPart(", last session-list read "), timePart(r.At))
		}
		rows = append(rows, healthRow{Label: name + " reachability", Parts: parts})
	}
	if audit == nil {
		rows = append(rows, healthRow{Label: "Audit feed", Parts: []part{chipPart(onOff[false])}})
	} else {
		rows = append(rows, healthRow{Label: "Audit feed", Parts: []part{chipPart(onOff[true]), textPart(fmt.Sprintf(", %d of %d entries", len(audit.Entries()), AuditRingSize))}})
	}
	return rows
}

func (d *dash) healthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		p := page{Title: "About and health - Cirak", Active: "health", HealthPage: true}
		p.RefreshHref = "/health"
		if _, none := d.src.(NullSource); none {
			p.State = &stateBlock{Class: "state", Heading: "No data source", Text: "No data source is connected."}
			d.render(ctx, w, http.StatusOK, p)
			return
		}
		h, err := d.health(ctx)
		if err != nil {
			p.State = &stateBlock{Class: "state state--error", Heading: "Data source unavailable",
				Text: "The Bridge could not read its own state just now. Nothing was changed. Try again in a few seconds.", LinkHref: p.RefreshHref, LinkText: "Try again"}
			d.unavailablePage(ctx, w, p)
			return
		}
		now := d.now().UTC()
		p.HealthRows = buildHealthRows(h, d.label, d.audit)
		for i := range p.HealthRows {
			for j := range p.HealthRows[i].Parts {
				stampPart(&p.HealthRows[i].Parts[j], now)
			}
		}
		p.StatusISO, p.StatusAbs, p.StatusRel = isoTime(now), fmtTime(now), relTime(now, now)
		d.render(ctx, w, http.StatusOK, p)
	})
}

type apiHealth struct {
	Kind      string      `json:"kind"`
	Connected bool        `json:"connected"`
	Rows      []healthRow `json:"rows"`
}

func (d *dash) buildHealth(ctx context.Context) ([]byte, error) {
	m := apiHealth{Kind: "health", Rows: []healthRow{}}
	if _, none := d.src.(NullSource); none {
		return json.Marshal(m)
	}
	h, err := d.health(ctx)
	if err != nil {
		return nil, err
	}
	m.Connected = true
	m.Rows = buildHealthRows(h, d.label, d.audit)
	return json.Marshal(m)
}

func (d *dash) healthJSONHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := d.model(r.Context(), "state\x00health", d.buildHealth)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeModel(w, r, m)
	})
}
