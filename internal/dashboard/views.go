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
<main id="app" data-slice="2">
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
{{with .Detail}}<section class="session">
<p><a href="/">back to the list</a></p>
<h2><span class="badge {{.HarnessClass}}">{{.Harness}}</span> {{.ID}}</h2>
<dl>
<dt>Title</dt><dd>{{.Title}}</dd>
<dt>Root</dt><dd><code>{{.Root}}</code></dd>
<dt>State</dt><dd>{{.State}}</dd>
<dt>Guard</dt><dd>{{if .Guard.Held}}held ({{.Guard.State}}){{else}}none{{end}}</dd>
<dt>Current execution</dt><dd>{{if .Current}}{{.Current}}{{else}}-{{end}}</dd>
<dt>Last activity</dt><dd>{{.LastActivity}}</dd>
</dl>
{{if $.Pending}}<p class="note">Claude Code: history and diffs unavailable (ADR-021 pending).</p>{{end}}
</section>
<section class="executions">
<h3>Executions</h3>
{{if $.Executions}}<table>
<thead><tr><th>Execution</th><th>State</th><th>Started</th><th>Updated</th><th>Ended</th></tr></thead>
<tbody>
{{range $.Executions}}<tr><td>{{.ID}}{{if .Current}} (current){{end}}</td><td>{{.State}}</td><td>{{.Started}}</td><td>{{.Updated}}</td><td>{{.Ended}}</td></tr>
{{end}}</tbody></table>{{else}}<p>No executions recorded by this Bridge process.</p>{{end}}
</section>{{end}}
</main>
<script src="/dashboard.js"></script>
</body>
</html>
`))

// renderPage executes the template into a buffer and refuses a body over the
// cap, so a page is never served partially.
func renderPage(w http.ResponseWriter, status int, p page) {
	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, p); err != nil || buf.Len() > maxBodyBytes {
		http.Error(w, "page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func indexHandler(src Source) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestBound)
		defer cancel()
		p := page{Title: "Harness Bridge dashboard"}
		if _, none := src.(NullSource); none {
			p.Message = "No data source is connected."
			renderPage(w, http.StatusOK, p)
			return
		}
		rows, err := src.Sessions(ctx)
		if err != nil {
			p.Message = "Data source unavailable."
			renderPage(w, http.StatusOK, p)
			return
		}
		p.Message = fmt.Sprintf("%d sessions.", len(rows))
		if len(rows) > maxListRows {
			p.Truncate = fmt.Sprintf("Showing the first %d of %d sessions.", maxListRows, len(rows))
			rows = rows[:maxListRows]
		}
		for _, s := range rows {
			p.Rows = append(p.Rows, toRow(s))
		}
		renderPage(w, http.StatusOK, p)
	})
}

// detailHandler serves GET /session/<harness>/<id>. The identifier is validated
// before the Source is asked; an invalid or unknown session is 404 with a fixed
// text that never echoes the request.
func detailHandler(src Source) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, sessionPrefix), "/")
		if len(parts) != 2 || !knownHarness[parts[0]] || !idPattern.MatchString(parts[1]) {
			http.Error(w, notFoundText, http.StatusNotFound)
			return
		}
		harness, id := parts[0], parts[1]
		ctx, cancel := context.WithTimeout(r.Context(), requestBound)
		defer cancel()
		d, err := src.SessionDetail(ctx, harness, id)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, notFoundText, http.StatusNotFound)
			return
		case err != nil:
			http.Error(w, unavailableText, http.StatusServiceUnavailable)
			return
		case d.Harness != harness || d.ID != id:
			// Never render a session other than the one requested.
			http.Error(w, notFoundText, http.StatusNotFound)
			return
		}
		row := toRow(d.Session)
		p := page{Title: "Harness Bridge dashboard · session " + truncRunes(d.ID, 128), Detail: &row, Pending: harness == harnessClaudeCode}
		execs := d.Executions
		if len(execs) > maxDetailRows {
			p.Truncate = fmt.Sprintf("Showing the first %d of %d executions.", maxDetailRows, len(execs))
			execs = execs[:maxDetailRows]
		}
		for _, e := range execs {
			p.Executions = append(p.Executions, execRow{
				ID: truncRunes(e.ID, 128), State: normExecutionState(e.State), Current: e.Current,
				Started: fmtTime(e.Started), Updated: fmtTime(e.Updated), Ended: fmtTimePtr(e.Ended),
			})
		}
		renderPage(w, http.StatusOK, p)
	})
}
