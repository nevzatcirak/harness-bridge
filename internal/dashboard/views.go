package dashboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Rendering bounds (ADR-023 section 3).
const (
	maxListRows       = 200
	maxDetailRows     = 100
	maxTitleRunes     = 120
	maxRootRunes      = 256
	maxBodyBytes      = 2 << 20
	unavailableText   = "data source unavailable"
	notFoundText      = "session not found"
	harnessOpenCode   = "opencode"
	harnessClaudeCode = "claudecode"
)

// idPattern is the conservative shape of a session ID in a path (it starts
// with an alphanumeric, so "." and ".." are refused). It is a
// request-side filter only: nothing built from it ever touches the filesystem
// or a native query (T4).
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

var knownHarness = map[string]bool{harnessOpenCode: true, harnessClaudeCode: true}

// Session state is one of idle|running|waiting_for_permission|unknown;
// "completed" is an execution state and never a session state (section 5.1).
var sessionStates = map[string]bool{"idle": true, "running": true, "waiting_for_permission": true, "unknown": true}

var executionStates = map[string]bool{"submitting": true, "running": true, "waiting_for_permission": true,
	"completed": true, "failed": true, "interrupted": true, "unknown": true}

func normSessionState(s string) string {
	if sessionStates[s] {
		return s
	}
	return "unknown"
}

func normExecutionState(s string) string {
	if executionStates[s] {
		return s
	}
	return "unknown"
}

// truncRunes cuts s to at most n runes on a rune boundary, marking the cut.
func truncRunes(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05Z")
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return fmtTime(*t)
}

// sessionHref is the detail link, built from escaped path segments, and only
// for an identifier the detail route would accept. Anything else is shown
// without a link.
func sessionHref(harness, id string) string {
	if !knownHarness[harness] || !idPattern.MatchString(id) {
		return ""
	}
	return "/session/" + url.PathEscape(harness) + "/" + url.PathEscape(id)
}

// rowHref is the detail link; an archived session's link keeps the filter, or its detail page
// would be a 404 (D17 Q7).
func rowHref(s Session) string {
	h := sessionHref(s.Harness, s.ID)
	if h != "" && s.Archived {
		h += "?archived=1"
	}
	return h
}

type guardRow struct {
	Held  bool
	State string
}

type sessionRow struct {
	Harness, HarnessClass string
	HarnessName           string // the badge text: OpenCode, Claude Code, Other
	ID, Title, Root       string
	PathShort             string // the shortened path of the Repository and path column (A1.4 item 1)
	LifecycleRoot         string // the ADR-022 lifecycle root label ("" when none)
	Archived              bool
	State                 string // the closed session state word (also the title of the chip and the JSON)
	StateLabel            string // the sentence-case label of the chip
	StateTone             string
	StateClass            string // the class attribute of the chip
	Guard                 guardRow
	GuardText             string
	Current               string
	LastActivity          string // the absolute time ("-" when unknown)
	LastISO               string // the datetime attribute ("" when unknown)
	LastRel               string // set for the HTML only: the JSON must not change every second (its ETag)
	Href                  string
	// Live is the liveness cell of the current execution (A2.8); the list shows its badges only.
	Live livenessCell
	// Liveness and UnavailableSince are the raw optional fields of the current execution for the JSON twin.
	Liveness         *Liveness
	UnavailableSince *time.Time
}

func harnessLabel(h string) (label, class string) {
	if knownHarness[h] {
		return h, "harness-" + h
	}
	return "other", "harness-other"
}

func toRow(s Session) sessionRow {
	label, class := harnessLabel(s.Harness)
	g := guardRow{Held: s.Guard.Held}
	if s.Guard.Held {
		g.State = normExecutionState(s.Guard.State)
	}
	state := normSessionState(s.State)
	c := stateChip(state)
	root := truncRunes(s.Root, maxRootRunes)
	return sessionRow{
		Harness: label, HarnessClass: class, HarnessName: harnessName(label),
		ID: truncRunes(s.ID, 128), Title: truncRunes(s.Title, maxTitleRunes), Root: root, PathShort: shortPath(root),
		LifecycleRoot: truncRunes(s.LifecycleRoot, maxRootRunes), Archived: s.Archived,
		State: state, StateLabel: c.Label, StateTone: c.Tone, StateClass: chipClass(c.Tone), Guard: g, GuardText: guardText(g),
		Current:      truncRunes(s.CurrentExecution, 128),
		LastActivity: fmtTime(s.LastActivity), LastISO: isoTime(s.LastActivity),
		Href: rowHref(s), Live: cellForLiveness(s.Liveness, s.UnavailableSince),
		Liveness: s.Liveness, UnavailableSince: s.UnavailableSince,
	}
}

type execRow struct {
	ID, State, Started, Updated, Ended string
	Current                            bool
	// The timeline row (A1.4 item 2): the state chip, the start as a datetime, the liveness cell, and, for the
	// current execution only, the liveness report and the pending-permission row. Duration and StartedRel depend on
	// the clock, so they are set for the HTML only and never reach the JSON (its ETag).
	StateLabel, StateClass string
	StartedISO             string
	StartedRel, Duration   string
	startedAt              time.Time // the clock-dependent fields below are derived from these (clockRows)
	endedAt                *time.Time
	Live                   livenessCell
	Report                 *reportView
	Perm                   *permView
	Liveness               *Liveness
	UnavailableSince       *time.Time
}

// stateBlock is an empty, error or not-connected state of a data region (A1.4 item 7): a heading, a sentence
// and the one action that fills it.
type stateBlock struct {
	Class, Heading, Text, LinkHref, LinkText string
}

type page struct {
	Title    string
	Active   string // the current destination of the top bar: sessions or audit
	Truncate string
	Rows     []sessionRow
	// The sessions screen (A1.4 item 1).
	ListPage                            bool
	State                               *stateBlock
	CountText                           string
	StatusISO, StatusAbs, StatusRel     string
	ToggleHref, ToggleText, RefreshHref string
	// Detail view
	Detail     *sessionRow
	Executions []execRow
	Pending    bool // Claude Code: history and diffs pending ADR-021
	Diff       *diffSection
	BackHref   string
	// Polling: the client script polls the JSON model of this view (data-poll). Sig is the signature of the
	// timeline (see execSignature): when the polled model's signature differs, the page is reloaded.
	Poll, PollHarness, PollID, Sig string
	// Audit feed
	AuditPage  bool
	AuditOn    bool
	AuditMsg   string
	Audit      []auditRow
	AuditPager auditPager
	// The top bar of every page (A1.3): the instance label and, in remote mode, the token-expiry badge.
	TopLabel string
	TopBadge *topBadge
	// The executions, catalog and health screens (A1.4 items 3, 4 and 6).
	ExecPage    bool
	ExecRows    []execListRow
	CatalogPage bool
	Groups      []catalogGroup
	HealthPage  bool
	HealthRows  []healthRow
}

// renderPage executes the template into a buffer and never serves a partial page. The
// 2 MiB bound applies to the escaped body: html/template can expand a capped diff
// (a '<' becomes four bytes), so a page over the bound is not an error (review #243
// P3-2). It is degraded with a written notice instead: the patches that would not fit
// are hidden, in order from the last file, and the page says so. Only a page that is
// still over the bound without any patch is refused.
func renderPage(w http.ResponseWriter, status int, p page) {
	var buf bytes.Buffer
	if pageTemplate.Execute(&buf, p) != nil {
		http.Error(w, "page unavailable", http.StatusInternalServerError)
		return
	}
	if buf.Len() > maxBodyBytes {
		for _, budget := range []int{maxBodyBytes - pageChromeBytes, 0} {
			fitPatches(&p, budget)
			buf.Reset()
			if pageTemplate.Execute(&buf, p) != nil {
				http.Error(w, "page unavailable", http.StatusInternalServerError)
				return
			}
			if buf.Len() <= maxBodyBytes {
				break
			}
		}
		if buf.Len() > maxBodyBytes {
			http.Error(w, "page unavailable", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// pageChromeBytes is the headroom kept for everything that is not patch text.
const pageChromeBytes = 256 << 10

// lineMarkupBytes bounds the markup one diff line adds around its text: <span class="ln hunk"></span> and a margin.
const lineMarkupBytes = 40

const patchHiddenNote = "patch not shown: the escaped page would exceed the 2 MiB page bound"

// fitPatches hides every shown patch that does not fit in budget bytes (measured
// escaped), keeping the earlier files, and marks each hidden patch.
func fitPatches(p *page, budget int) {
	if p.Diff == nil {
		return
	}
	used := 0
	for _, v := range []*diffView{p.Diff.Working, p.Diff.Single} {
		if v == nil {
			continue
		}
		for i := range v.Files {
			f := &v.Files[i]
			if !f.ShowPatch {
				continue
			}
			// the escaped text, a margin for escapes the string form leaves out, and the span markup of every line
			size := len(template.HTMLEscapeString(f.Patch)) + len(f.Patch)/16 + len(f.Lines)*lineMarkupBytes
			if used+size > budget {
				f.ShowPatch, f.Patch, f.Lines = false, "", nil
				if f.Note != "" {
					f.Note += "; "
				}
				f.Note += patchHiddenNote
				continue
			}
			used += size
		}
	}
}

// dash carries what the handlers share: the read-only Source, the redactor and
// the diff cache. It holds no state of a session and writes nothing.
type dash struct {
	src    Source
	audit  AuditSource // read-only; nil when the audit feed is not enabled
	red    *Redactor
	cache  *diffCache
	models *flightCache[modelResult]
	now    func() time.Time
	modMu  sync.Mutex
	mod    map[string]modStamp
	label  string // the instance label of the top bar
	hc     healthCache
}

func newDash(src Source, opts Options) *dash {
	red := opts.Redactor
	if red == nil {
		red = DefaultRedactor()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &dash{src: src, audit: opts.Audit, red: red, cache: newDiffCache(opts.Now), now: now, mod: map[string]modStamp{}, label: opts.Label,
		models: newFlightCache[modelResult](opts.Now, modelTTL, modelMax)}
}

// Scripts returns the script files of this page, in load order (ADR-023 A1.6, D34): the core on every page, then
// only the script of the page kind. The sessions page polls and filters; the session page polls; the audit page
// filters its rows (only when there is a filter and rows to filter). The executions, catalog, health and turn pages
// need the core alone. theme.js is not in this list: it is blocking in the head of every page (the template).
func (p page) Scripts() []string {
	out := []string{"/" + scriptCore}
	switch {
	case p.ListPage:
		out = append(out, "/"+scriptList)
	case p.Poll == "detail":
		out = append(out, "/"+scriptDetail)
	case p.AuditPage && p.AuditOn && len(p.Audit) > 0:
		out = append(out, "/"+scriptAudit)
	}
	return out
}

// unavailablePage answers a data source that failed: HTTP 503 with Retry-After,
// for the list and the detail alike (review #223 P3-1: one semantics). The page
// states it; the error text is never shown. The top bar stays as on every page: the label is process
// configuration and the token badge is memory only (#576 P3-4).
func (d *dash) unavailablePage(ctx context.Context, w http.ResponseWriter, p page) {
	d.decorate(ctx, &p)
	w.Header().Set("Retry-After", "5")
	renderPage(w, http.StatusServiceUnavailable, p)
}

func (d *dash) indexHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestBound)
		defer cancel()
		now := d.now().UTC()
		p := page{Title: "Sessions - Cirak", Active: "sessions", ListPage: true, Poll: "list"}
		p.RefreshHref, p.ToggleHref, p.ToggleText = "/", "/?archived=1", "Show archived"
		if ArchivedShown(ctx) {
			p.RefreshHref, p.ToggleHref, p.ToggleText = "/?archived=1", "/", "Hide archived"
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
		p.Rows, p.Truncate = listRows(rows)
		p.CountText = sessionsCountText(len(rows))
		p.StatusISO, p.StatusAbs, p.StatusRel = isoTime(now), fmtTime(now), relTime(now, now)
		for i := range p.Rows {
			p.Rows[i].LastRel = relTimeISO(now, p.Rows[i].LastISO)
		}
		if len(p.Rows) == 0 {
			p.State = &stateBlock{Class: "state", Heading: "No sessions", Text: "This Bridge lists no session you may see. Archived sessions are hidden unless you ask for them.",
				LinkHref: p.ToggleHref, LinkText: p.ToggleText}
		}
		d.render(ctx, w, http.StatusOK, p)
	})
}

// diffSection is the "Changes" part of a detail or turn page.
type diffSection struct {
	Pending   bool
	Working   *diffView
	Single    *diffView
	Turns     []turnRow
	TurnsNote string
}

type turnRow struct {
	ID, Time, Label, Href string
}

var labelClass = map[string]string{LabelChanges: "changes", LabelComputedEmpty: "computed-empty", LabelNotComputed: "not-computed", LabelUnavailable: "unavailable"}

func finishView(v diffView) *diffView {
	v.LabelClass = labelClass[v.Label]
	return &v
}

const workingScope = "working tree (not per turn)"

// workingView reads the default diff (native /vcs/diff, D17 Q2 and Q9) through the
// cache. An empty answer is NOT "no changes": OpenCode returns [] for a non-git
// project and for the default branch too (ADR-023 section 1.2).
func (d *dash) workingView(ctx context.Context, harness, id string) *diffView {
	res, err := d.cache.get(ctx, "wt\x00"+harness+"\x00"+id, func(c context.Context) (DiffResult, error) {
		return d.src.WorkingTreeDiff(c, harness, id)
	})
	switch {
	case err != nil:
		return finishView(unavailableView(workingScope, err))
	case len(res.Files) == 0:
		return finishView(diffView{Label: LabelComputedEmpty, Scope: workingScope,
			Reason: "an empty answer is also what OpenCode returns for a non-git project or the default branch"})
	}
	return finishView(buildDiffView(LabelChanges, workingScope, res, d.red))
}

func turnLabel(s TurnState) string {
	switch s {
	case TurnComputedEmpty:
		return LabelComputedEmpty
	case TurnChanges:
		return LabelChanges
	}
	return LabelNotComputed // an unknown state is never "no changes"
}

func (d *dash) detailSection(ctx context.Context, harness, id string) *diffSection {
	if harness == harnessClaudeCode {
		return &diffSection{Pending: true}
	}
	sec := &diffSection{}
	var wg sync.WaitGroup
	var turns []Turn
	var terr error
	wg.Add(2)
	go func() { defer wg.Done(); sec.Working = d.workingView(ctx, harness, id) }()
	go func() { defer wg.Done(); turns, terr = d.src.SessionTurns(ctx, harness, id) }()
	wg.Wait()
	if terr != nil {
		sec.TurnsNote = "turn list unavailable (" + unavailableReason(terr) + ")"
		return sec
	}
	if len(turns) > maxTurnsShown {
		sec.TurnsNote = fmt.Sprintf("Showing the last %d of %d turns.", maxTurnsShown, len(turns))
		turns = turns[len(turns)-maxTurnsShown:]
	}
	for _, t := range turns {
		row := turnRow{ID: truncRunes(t.MessageID, 128), Time: fmtTime(t.Created), Label: turnLabel(t.State)}
		if t.State == TurnChanges && idPattern.MatchString(t.MessageID) {
			row.Href = sessionHref(harness, id) + "/turn/" + url.PathEscape(t.MessageID)
		}
		sec.Turns = append(sec.Turns, row)
	}
	return sec
}

// detailHandler serves GET /session/<harness>/<id> and the per-turn diff
// GET /session/<harness>/<id>/turn/<messageID>. Every identifier is validated
// before the Source is asked; an invalid or unknown one is 404 with a fixed text
// that never echoes the request.
func (d *dash) detailHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, sessionPrefix), "/")
		turn := len(parts) == 4 && parts[2] == "turn"
		if (len(parts) != 2 && !turn) || !knownHarness[parts[0]] || !idPattern.MatchString(parts[1]) || (turn && !idPattern.MatchString(parts[3])) {
			http.Error(w, notFoundText, http.StatusNotFound)
			return
		}
		harness, id := parts[0], parts[1]
		ctx, cancel := context.WithTimeout(r.Context(), requestBound)
		defer cancel()
		det, err := d.src.SessionDetail(ctx, harness, id)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, notFoundText, http.StatusNotFound)
			return
		case err != nil:
			// Same semantics as the list (#223 P3-1, #243 P3-1): 503 with Retry-After.
			w.Header().Set("Retry-After", "5")
			http.Error(w, unavailableText, http.StatusServiceUnavailable)
			return
		case det.Harness != harness || det.ID != id:
			// Never render a session other than the one requested.
			http.Error(w, notFoundText, http.StatusNotFound)
			return
		}
		row := toRow(det.Session)
		row.LastRel = relTimeISO(d.now().UTC(), row.LastISO)
		p := page{Title: "Session " + truncRunes(det.ID, 128) + " - Cirak", Active: "sessions", Detail: &row, Pending: harness == harnessClaudeCode}
		if turn {
			d.turnPage(ctx, w, p, harness, id, parts[3])
			return
		}
		p.Executions, p.Truncate = execRows(det.Executions)
		p.Sig = execSignature(p.Executions)
		clockRows(p.Executions, d.now().UTC())
		p.Poll, p.PollHarness, p.PollID = "detail", harness, id
		p.Diff = d.detailSection(ctx, harness, id)
		d.render(ctx, w, http.StatusOK, p)
	})
}

// turnPage renders the diff of one user turn, on demand. The presence state of
// info.summary.diffs decides first: "not computed" and "computed, no changes" make
// no native diff call, and "not computed" is never shown as "no changes".
func (d *dash) turnPage(ctx context.Context, w http.ResponseWriter, p page, harness, id, messageID string) {
	p.BackHref = sessionHref(harness, id)
	if harness == harnessClaudeCode {
		p.Diff = &diffSection{Pending: true}
		d.render(ctx, w, http.StatusOK, p)
		return
	}
	scope := "turn " + truncRunes(messageID, 128)
	turns, err := d.src.SessionTurns(ctx, harness, id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, notFoundText, http.StatusNotFound)
		return
	}
	if err != nil {
		p.Diff = &diffSection{Single: finishView(unavailableView(scope, err))}
		d.render(ctx, w, http.StatusOK, p)
		return
	}
	var found *Turn
	for i := range turns {
		if turns[i].MessageID == messageID {
			found = &turns[i]
		}
	}
	if found == nil {
		http.Error(w, notFoundText, http.StatusNotFound)
		return
	}
	var v *diffView
	switch found.State {
	case TurnNotComputed:
		v = finishView(diffView{Label: LabelNotComputed, Scope: scope, Reason: "OpenCode has not computed a diff for this turn"})
	case TurnComputedEmpty:
		v = finishView(diffView{Label: LabelComputedEmpty, Scope: scope})
	case TurnChanges:
		res, derr := d.cache.get(ctx, "turn\x00"+harness+"\x00"+id+"\x00"+messageID, func(c context.Context) (DiffResult, error) {
			return d.src.TurnDiff(c, harness, id, messageID)
		})
		switch {
		case errors.Is(derr, ErrNotFound):
			http.Error(w, notFoundText, http.StatusNotFound)
			return
		case derr != nil:
			v = finishView(unavailableView(scope, derr))
		case len(res.Files) == 0:
			v = finishView(unavailableView(scope, errors.New("empty answer for a computed diff")))
			v.Reason = "empty answer for a diff OpenCode reported as computed"
		default:
			v = finishView(buildDiffView(LabelChanges, scope, res, d.red))
		}
	default:
		v = finishView(diffView{Label: LabelNotComputed, Scope: scope, Reason: "unknown state"})
	}
	p.Diff = &diffSection{Single: v}
	d.render(ctx, w, http.StatusOK, p)
}
