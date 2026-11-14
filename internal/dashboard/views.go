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

type guardRow struct {
	Held  bool
	State string
}

type sessionRow struct {
	Harness, HarnessClass string
	ID, Title, Root       string
	State                 string
	Guard                 guardRow
	Current               string
	LastActivity          string
	Href                  string
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
	return sessionRow{
		Harness: label, HarnessClass: class,
		ID: truncRunes(s.ID, 128), Title: truncRunes(s.Title, maxTitleRunes), Root: truncRunes(s.Root, maxRootRunes),
		State: normSessionState(s.State), Guard: g,
		Current:      truncRunes(s.CurrentExecution, 128),
		LastActivity: fmtTime(s.LastActivity),
		Href:         sessionHref(s.Harness, s.ID),
	}
}

type execRow struct {
	ID, State, Started, Updated, Ended string
	Current                            bool
}

type page struct {
	Title    string
	Message  string
	Truncate string
	Rows     []sessionRow
	// Detail view
	Detail     *sessionRow
	Executions []execRow
	Pending    bool // Claude Code: history and diffs pending ADR-021
	Diff       *diffSection
	BackHref   string
	// Polling: the client script polls the JSON model of this view (data-poll).
	Poll, PollHarness, PollID string
	// Audit feed
	AuditPage bool
	AuditOn   bool
	AuditMsg  string
	Audit     []auditRow
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{.Title}}</title>
<link rel="stylesheet" href="/dashboard.css">
</head>
<body>
<header><h1>Harness Bridge dashboard</h1><span class="badge">read-only</span></header>
<main id="app" data-slice="4"{{if .Poll}} data-poll="{{.Poll}}"{{if .PollHarness}} data-harness="{{.PollHarness}}" data-session="{{.PollID}}"{{end}}{{end}}>
<div id="live">
{{if .Message}}<p>{{.Message}}</p>{{end}}
{{if .Rows}}<table class="sessions">
<thead><tr><th>Harness</th><th>Session</th><th>Title</th><th>Root</th><th>State</th><th>Guard</th><th>Current execution</th><th>Last activity</th></tr></thead>
<tbody>
{{range .Rows}}<tr>
<td><span class="badge {{.HarnessClass}}">{{.Harness}}</span></td>
<td>{{if .Href}}<a href="{{.Href}}">{{.ID}}</a>{{else}}{{.ID}}{{end}}</td>
<td>{{.Title}}</td>
<td><code>{{.Root}}</code></td>
<td>{{.State}}</td>
<td>{{if .Guard.Held}}held ({{.Guard.State}}){{else}}none{{end}}</td>
<td>{{if .Current}}{{.Current}}{{else}}-{{end}}</td>
<td>{{.LastActivity}}</td>
</tr>
{{end}}</tbody></table>{{end}}
{{if .Truncate}}<p class="note">{{.Truncate}}</p>{{end}}
</div>
{{with .Detail}}<section class="session">
<p><a href="/">back to the list</a></p>
<h2><span class="badge {{.HarnessClass}}">{{.Harness}}</span> {{.ID}}</h2>
<dl>
<dt>Title</dt><dd data-f="title">{{.Title}}</dd>
<dt>Root</dt><dd data-f="root"><code>{{.Root}}</code></dd>
<dt>State</dt><dd data-f="state">{{.State}}</dd>
<dt>Guard</dt><dd data-f="guard">{{if .Guard.Held}}held ({{.Guard.State}}){{else}}none{{end}}</dd>
<dt>Current execution</dt><dd data-f="current">{{if .Current}}{{.Current}}{{else}}-{{end}}</dd>
<dt>Last activity</dt><dd data-f="last">{{.LastActivity}}</dd>
</dl>
{{if $.Pending}}<p class="note">Claude Code: history and diffs unavailable (ADR-021 pending).</p>{{end}}
</section>
<section class="executions">
<h3>Executions</h3>
{{if $.Executions}}<table>
<thead><tr><th>Execution</th><th>State</th><th>Started</th><th>Updated</th><th>Ended</th></tr></thead>
<tbody id="exec-rows">
{{range $.Executions}}<tr><td>{{.ID}}{{if .Current}} (current){{end}}</td><td>{{.State}}</td><td>{{.Started}}</td><td>{{.Updated}}</td><td>{{.Ended}}</td></tr>
{{end}}</tbody></table>{{else}}<p>No executions recorded by this Bridge process.</p>{{end}}
</section>{{end}}
{{with .Diff}}<section class="changes">
<h3>Changes</h3>
{{if .Pending}}<p class="note">diff unavailable (ADR-021 pending)</p>{{end}}
{{with .Working}}{{template "diffview" .}}{{end}}
{{with .Single}}{{template "diffview" .}}{{end}}
{{if .TurnsNote}}<p class="note">{{.TurnsNote}}</p>{{end}}
{{if .Turns}}<h4>Turns</h4><table class="turns">
<thead><tr><th>Turn</th><th>Time</th><th>Diff</th></tr></thead>
<tbody>
{{range .Turns}}<tr><td>{{if .Href}}<a href="{{.Href}}">{{.ID}}</a>{{else}}{{.ID}}{{end}}</td><td>{{.Time}}</td><td>{{.Label}}</td></tr>
{{end}}</tbody></table>{{end}}
</section>{{end}}
{{if .BackHref}}<p><a href="{{.BackHref}}">back to the session</a></p>{{end}}
{{if .AuditPage}}<section class="audit">
<h2>Audit</h2>
<p><a href="/">back to the list</a></p>
{{if .AuditMsg}}<p class="note">{{.AuditMsg}}</p>{{end}}
{{if .AuditOn}}<p><label>Filter <input type="search" id="audit-filter" maxlength="64" autocomplete="off"></label></p>
{{if .Audit}}<table class="audit-feed">
<thead><tr><th>Time</th><th>Event</th><th>Outcome</th><th>Error</th><th>Session</th><th>Execution</th><th>Principal</th><th>Other</th></tr></thead>
<tbody id="audit-rows">
{{range .Audit}}<tr><td>{{.Time}}</td><td>{{.Event}}</td><td>{{.Outcome}}</td><td>{{.Error}}</td><td>{{.Session}}</td><td>{{.Execution}}</td><td>{{.Principal}}</td><td>{{.Other}}</td></tr>
{{end}}</tbody></table>{{end}}{{end}}
</section>{{end}}
</main>
<script src="/dashboard.js"></script>
</body>
</html>
{{define "diffview"}}<div class="diff"><p><span class="badge label-{{.LabelClass}}">{{.Label}}</span> {{.Scope}}{{if .Reason}} ({{.Reason}}){{end}}</p>
{{if .Redaction}}<p class="note">{{.Redaction}}</p>{{end}}
{{range .Files}}<section class="file"><h5>{{.Path}} <span class="status">{{.Status}}</span> +{{.Additions}} -{{.Deletions}}</h5>
{{if .Note}}<p class="note">{{.Note}}</p>{{end}}{{if .ShowPatch}}<pre>{{.Patch}}</pre>{{end}}</section>
{{end}}{{if .More}}<p class="note">{{.More}}</p>{{end}}</div>{{end}}
`))

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
			size := len(template.HTMLEscapeString(f.Patch)) + len(f.Patch)/16 // + a margin for escapes the string form leaves out
			if used+size > budget {
				f.ShowPatch, f.Patch = false, ""
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
	return &dash{src: src, audit: opts.Audit, red: red, cache: newDiffCache(opts.Now), now: now, mod: map[string]modStamp{},
		models: newFlightCache[modelResult](opts.Now, modelTTL, modelMax)}
}

// unavailablePage answers a data source that failed: HTTP 503 with Retry-After,
// for the list and the detail alike (review #223 P3-1: one semantics). The page
// states it; the error text is never shown.
func unavailablePage(w http.ResponseWriter, p page) {
	w.Header().Set("Retry-After", "5")
	renderPage(w, http.StatusServiceUnavailable, p)
}

func (d *dash) indexHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestBound)
		defer cancel()
		p := page{Title: "Harness Bridge dashboard", Poll: "list"}
		if _, none := d.src.(NullSource); none {
			p.Message = "No data source is connected."
			renderPage(w, http.StatusOK, p)
			return
		}
		rows, err := d.src.Sessions(ctx)
		if err != nil {
			p.Message = "Data source unavailable."
			unavailablePage(w, p)
			return
		}
		p.Message = fmt.Sprintf("%d sessions.", len(rows))
		p.Rows, p.Truncate = listRows(rows)
		renderPage(w, http.StatusOK, p)
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
		p := page{Title: "Harness Bridge dashboard · session " + truncRunes(det.ID, 128), Detail: &row, Pending: harness == harnessClaudeCode}
		if turn {
			d.turnPage(ctx, w, p, harness, id, parts[3])
			return
		}
		p.Executions, p.Truncate = execRows(det.Executions)
		p.Poll, p.PollHarness, p.PollID = "detail", harness, id
		p.Diff = d.detailSection(ctx, harness, id)
		renderPage(w, http.StatusOK, p)
	})
}

// turnPage renders the diff of one user turn, on demand. The presence state of
// info.summary.diffs decides first: "not computed" and "computed, no changes" make
// no native diff call, and "not computed" is never shown as "no changes".
func (d *dash) turnPage(ctx context.Context, w http.ResponseWriter, p page, harness, id, messageID string) {
	p.BackHref = sessionHref(harness, id)
	if harness == harnessClaudeCode {
		p.Diff = &diffSection{Pending: true}
		renderPage(w, http.StatusOK, p)
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
		renderPage(w, http.StatusOK, p)
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
	renderPage(w, http.StatusOK, p)
}
