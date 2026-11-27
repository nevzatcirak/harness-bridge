package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Polling JSON (ADR-023 section 4): GET /api/state (the list view model) and
// GET /api/state/session/<harness>/<id> (the detail view model), and the audit feed
// GET /api/audit. Each response carries an ETag (a hash of the body) and
// Last-Modified (the time the body last changed); a request whose If-None-Match
// matches gets 304 with no body. A view model is built at most once a second per key
// and concurrent builds of a key share one read (single-flight).

const (
	modelTTL       = time.Second // ADR-023 section 4: the view model is cached for 1 s
	modelMax       = 64
	maxModelBytes  = maxBodyBytes
	statePrefix    = "/api/state/session/"
	maxINMHeader   = 4096
	maxModTimeKeys = 256
)

// modelResult is one built view model.
type modelResult struct {
	body     []byte
	etag     string
	modified time.Time
}

type apiGuard struct {
	Held  bool   `json:"held"`
	State string `json:"state,omitempty"`
}

// apiSession is a session row of the polling JSON. Every string is the same
// normalized, length-bounded value the HTML shows; the client renders it with
// textContent, never as markup.
type apiSession struct {
	Harness       string   `json:"harness"`
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Root          string   `json:"root"`
	LifecycleRoot string   `json:"lifecycle_root,omitempty"`
	Archived      bool     `json:"archived,omitempty"`
	State         string   `json:"state"`
	Guard         apiGuard `json:"guard"`
	Current       string   `json:"current_execution,omitempty"`
	LastActivity  string   `json:"last_activity"`
	Href          string   `json:"href,omitempty"`
	// Display fields of the sessions table (A1.4): the script sets them with textContent and never decides a label.
	HarnessName string       `json:"harness_name"`
	StateLabel  string       `json:"state_label"`
	StateTone   string       `json:"state_tone"`
	GuardText   string       `json:"guard_text"`
	PathShort   string       `json:"path_short"`
	LastISO     string       `json:"last_activity_iso,omitempty"`
	Liveness    *apiLiveness `json:"liveness,omitempty"`
}

type apiExec struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Started string `json:"started"`
	Updated string `json:"updated"`
	Ended   string `json:"ended"`
	Current bool   `json:"current"`
	// Report and Permission are the liveness report and the pending-permission row of the CURRENT execution, as the
	// same parts the page renders; the script rebuilds those two blocks from them without deciding any label.
	Report     *reportView `json:"report,omitempty"`
	Permission *permView   `json:"permission,omitempty"`
	// Liveness is the A2.8 optional block and badge set of this execution, for the live poll.
	Liveness *apiLiveness `json:"liveness,omitempty"`
}

type apiBadge struct {
	Label string `json:"label"`
	Tone  string `json:"tone"`
	Title string `json:"title,omitempty"`
}

type apiLivenessTool struct {
	Name         string `json:"name"`
	RunningSince string `json:"running_since,omitempty"`
}

type apiThresholds struct {
	NoProgressS int `json:"no_progress_s"`
	ToolLongS   int `json:"tool_long_s"`
}

// apiLiveness is the optional A2 liveness block of the poll JSON and the public twin: closed words, bounded values
// and absolute timestamps only. Badges carry the same labels as the HTML; Text is the cell's short detail.
type apiLiveness struct {
	NativeStatus         string           `json:"native_status,omitempty"`
	RetryAttempt         *int64           `json:"retry_attempt,omitempty"`
	RetryMax             *int64           `json:"retry_max,omitempty"`
	RetryNextAt          string           `json:"retry_next_at,omitempty"`
	LastNativeProgressAt string           `json:"last_native_progress_at,omitempty"`
	RetryOverdue         bool             `json:"retry_overdue,omitempty"`
	Degraded             bool             `json:"degraded,omitempty"`
	DegradationReason    string           `json:"degradation_reason,omitempty"`
	StallSuspected       bool             `json:"stall_suspected,omitempty"`
	StallReason          string           `json:"stall_reason,omitempty"`
	Tool                 *apiLivenessTool `json:"tool,omitempty"`
	Explanation          string           `json:"explanation,omitempty"`
	CauseHint            string           `json:"cause_hint,omitempty"`
	CauseSource          string           `json:"cause_source,omitempty"`
	ProgressSource       string           `json:"progress_source,omitempty"`
	Thresholds           *apiThresholds   `json:"thresholds,omitempty"`
	ObservedAt           string           `json:"observed_at,omitempty"`
	UnavailableSince     string           `json:"unavailable_since,omitempty"`
	Badges               []apiBadge       `json:"badges,omitempty"`
	Text                 string           `json:"text,omitempty"`
}

func apiLivenessFor(l *Liveness, unavailableSince *time.Time, live livenessCell) *apiLiveness {
	if l == nil && unavailableSince == nil && len(live.Badges) == 0 && live.Text == "" {
		return nil
	}
	out := &apiLiveness{Text: live.Text}
	for _, b := range live.Badges {
		out.Badges = append(out.Badges, apiBadge{Label: b.Label, Tone: b.Tone, Title: b.Title})
	}
	if unavailableSince != nil && !unavailableSince.IsZero() {
		out.UnavailableSince = isoTime(*unavailableSince)
	}
	if l == nil {
		return out
	}
	out.NativeStatus = closedWord(l.NativeStatus, nativeStatuses)
	out.RetryAttempt, out.RetryMax = l.RetryAttempt, l.RetryMax
	if l.RetryNextAt != nil {
		out.RetryNextAt = isoTime(*l.RetryNextAt)
	}
	if l.LastNativeProgressAt != nil {
		out.LastNativeProgressAt = isoTime(*l.LastNativeProgressAt)
	}
	out.RetryOverdue = l.RetryOverdue
	out.Degraded = l.Degraded || l.DegradationReason != ""
	out.DegradationReason = closedReason(l.DegradationReason, degradationReasons)
	out.StallSuspected = l.StallSuspected
	out.StallReason = closedReason(l.StallReason, stallReasons)
	out.Explanation = closedReason(l.Explanation, explanations)
	out.CauseHint = closedString(l.CauseHint, causeHints)
	out.CauseSource = closedString(l.CauseSource, causeSources)
	out.ProgressSource = closedString(l.ProgressSource, progressSources)
	if !l.ObservedAt.IsZero() {
		out.ObservedAt = isoTime(l.ObservedAt)
	}
	if l.Tool != nil {
		out.Tool = &apiLivenessTool{Name: truncRunes(l.Tool.Name, 64)}
		if !l.Tool.RunningSince.IsZero() {
			out.Tool.RunningSince = isoTime(l.Tool.RunningSince)
		}
	}
	if l.Thresholds != nil {
		out.Thresholds = &apiThresholds{NoProgressS: l.Thresholds.NoProgressS, ToolLongS: l.Thresholds.ToolLongS}
	}
	return out
}

func closedReason(word string, m map[string]reasonInfo) string {
	if word == "" {
		return ""
	}
	if _, ok := m[word]; ok {
		return word
	}
	return "unknown"
}

func closedWord(word string, m map[string]chip) string {
	if word == "" {
		return ""
	}
	if _, ok := m[word]; ok {
		return word
	}
	return "unknown"
}

func closedString(word string, m map[string]string) string {
	if word == "" {
		return ""
	}
	if _, ok := m[word]; ok {
		return word
	}
	return "unknown"
}

type apiList struct {
	Kind      string       `json:"kind"`
	Connected bool         `json:"connected"`
	Message   string       `json:"message"`
	Truncated string       `json:"truncated,omitempty"`
	Count     int          `json:"count"`
	CountText string       `json:"count_text"`
	Sessions  []apiSession `json:"sessions"`
}

type apiDetail struct {
	Kind       string     `json:"kind"`
	Sig        string     `json:"sig"`
	Session    apiSession `json:"session"`
	Pending    bool       `json:"claude_code_pending,omitempty"`
	Truncated  string     `json:"truncated,omitempty"`
	Executions []apiExec  `json:"executions"`
}

func toAPISession(r sessionRow) apiSession {
	g := apiGuard{Held: r.Guard.Held}
	if g.Held {
		g.State = r.Guard.State
	}
	return apiSession{Harness: r.Harness, ID: r.ID, Title: r.Title, Root: r.Root, LifecycleRoot: r.LifecycleRoot, Archived: r.Archived, State: r.State, Guard: g, Current: r.Current, LastActivity: r.LastActivity, Href: r.Href,
		HarnessName: r.HarnessName, StateLabel: r.StateLabel, StateTone: r.StateTone, GuardText: r.GuardText, PathShort: r.PathShort, LastISO: r.LastISO,
		Liveness: apiLivenessFor(r.Liveness, r.UnavailableSince, r.Live)}
}

// listRows applies the list cap and the row normalization once, for the HTML and
// the JSON alike.
func listRows(rows []Session) (out []sessionRow, truncated string) {
	if len(rows) > maxListRows {
		truncated = fmt.Sprintf("Showing the first %d of %d sessions.", maxListRows, len(rows))
		rows = rows[:maxListRows]
	}
	for _, s := range rows {
		out = append(out, toRow(s))
	}
	disambiguatePaths(out)
	return out, truncated
}

// disambiguatePaths gives a stable 6-hex suffix to the rows of one list whose shortened paths would render
// identically although their full paths differ (A1.4 item 1). Rows with the very same full path are the same
// place and get nothing; a path shown whole is never ambiguous.
func disambiguatePaths(rows []sessionRow) {
	full := map[string]map[string]bool{} // shortened form -> the distinct full paths that share it
	for _, r := range rows {
		if r.PathShort == r.Root {
			continue
		}
		if full[r.PathShort] == nil {
			full[r.PathShort] = map[string]bool{}
		}
		full[r.PathShort][r.Root] = true
	}
	for i := range rows {
		if len(full[rows[i].PathShort]) > 1 && rows[i].PathShort != rows[i].Root {
			rows[i].PathShort += " (" + pathSuffix(rows[i].Root) + ")"
		}
	}
}

// execRows applies the detail cap and normalization once, for the HTML and the JSON. The liveness report and the
// pending-permission row are built for the current execution only (A1.4 item 2).
func execRows(execs []Execution) (out []execRow, truncated string) {
	if len(execs) > maxDetailRows {
		truncated = fmt.Sprintf("Showing the first %d of %d executions.", maxDetailRows, len(execs))
		execs = execs[:maxDetailRows]
	}
	for _, e := range execs {
		state := normExecutionState(e.State)
		c := stateChip(state)
		row := execRow{
			ID: truncRunes(e.ID, 128), State: state, Current: e.Current,
			Started: fmtTime(e.Started), Updated: fmtTime(e.Updated), Ended: fmtTimePtr(e.Ended),
			StateLabel: c.Label, StateClass: chipClass(c.Tone), StartedISO: isoTime(e.Started), Live: cellFor(e),
			startedAt: e.Started, endedAt: e.Ended, Liveness: e.Liveness, UnavailableSince: e.UnavailableSince,
		}
		if e.Current {
			row.Report = buildReport(e.Liveness, e.UnavailableSince)
			row.Perm = permissionRow(e.Permission)
		}
		out = append(out, row)
	}
	return out, truncated
}

// clockRows sets the fields of the HTML timeline that depend on the clock: the relative start, the duration of an
// ended execution, and the running time of one that has not ended. They never reach the JSON.
func clockRows(rows []execRow, now time.Time) {
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
		if r.Report != nil {
			stampReport(r.Report, now)
		}
		if r.Perm != nil {
			for j := range r.Perm.Parts {
				stampPart(&r.Perm.Parts[j], now)
			}
		}
	}
}

// stampReport sets the relative text of every time part of a report (HTML only).
func stampReport(v *reportView, now time.Time) {
	for i := range v.Rows {
		for j := range v.Rows[i].Parts {
			stampPart(&v.Rows[i].Parts[j], now)
		}
	}
}

func stampPart(p *part, now time.Time) {
	if p.Kind == "time" {
		p.Rel = relTimeISO(now, p.ISO)
	}
}

// execSignature identifies the shape of the timeline: which executions exist, their states, which is current and
// which blocks it carries. The script reloads the page when the polled model's signature differs from the one the
// page was rendered with, and updates the report and the permission row in place otherwise.
func execSignature(rows []execRow) string {
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(r.ID + ":" + r.State)
		if r.Current {
			b.WriteString(":c")
		}
		if r.Report != nil {
			b.WriteString("r")
		}
		if r.Perm != nil {
			b.WriteString("p")
		}
	}
	return b.String()
}

func (d *dash) buildList(ctx context.Context) ([]byte, error) {
	m := apiList{Kind: "list", Sessions: []apiSession{}}
	if _, none := d.src.(NullSource); none {
		m.Message = "No data source is connected."
		return json.Marshal(m)
	}
	rows, err := d.src.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	m.Connected = true
	m.Message = fmt.Sprintf("%d sessions.", len(rows))
	m.Count, m.CountText = len(rows), sessionsCountText(len(rows))
	shown, trunc := listRows(rows)
	m.Truncated = trunc
	for _, r := range shown {
		m.Sessions = append(m.Sessions, toAPISession(r))
	}
	return json.Marshal(m)
}

func (d *dash) buildDetail(ctx context.Context, harness, id string) ([]byte, error) {
	det, err := d.src.SessionDetail(ctx, harness, id)
	if err != nil {
		return nil, err
	}
	if det.Harness != harness || det.ID != id {
		return nil, ErrNotFound // never a session other than the one requested
	}
	m := apiDetail{Kind: "detail", Session: toAPISession(toRow(det.Session)), Pending: harness == harnessClaudeCode, Executions: []apiExec{}}
	execs, trunc := execRows(det.Executions)
	m.Truncated = trunc
	for _, e := range execs {
		m.Executions = append(m.Executions, apiExec{ID: e.ID, State: e.State, Started: e.Started, Updated: e.Updated, Ended: e.Ended, Current: e.Current,
			Report: e.Report, Permission: e.Perm, Liveness: apiLivenessFor(e.Liveness, e.UnavailableSince, e.Live)})
	}
	m.Sig = execSignature(execs)
	return json.Marshal(m)
}

type apiAudit struct {
	Enabled bool         `json:"enabled"`
	Limit   int          `json:"limit"`
	Entries []AuditEntry `json:"entries"`
}

func (d *dash) buildAudit(context.Context) ([]byte, error) {
	m := apiAudit{Limit: AuditRingSize, Entries: []AuditEntry{}}
	if d.audit != nil {
		m.Enabled = true
		if e := d.audit.Entries(); len(e) > 0 {
			m.Entries = e
		}
	}
	return json.Marshal(m)
}

// model returns the view model for key, built at most once a second and shared by
// concurrent callers. The build uses its own 5 s bound, detached from the caller, so a
// client that disconnects cannot hand its cancellation to the callers sharing the read.
func (d *dash) model(ctx context.Context, key string, build func(context.Context) ([]byte, error)) (modelResult, error) {
	return d.models.get(ctx, key, func(c context.Context) (modelResult, error) {
		bctx, cancel := context.WithTimeout(context.WithoutCancel(c), requestBound)
		defer cancel()
		body, err := build(bctx)
		if err != nil {
			return modelResult{}, err
		}
		if len(body) > maxModelBytes {
			return modelResult{}, errors.New("view model over its size bound")
		}
		sum := sha256.Sum256(body)
		etag := `"` + hex.EncodeToString(sum[:16]) + `"`
		return modelResult{body: body, etag: etag, modified: d.modifiedAt(key, etag)}, nil
	})
}

// modifiedAt is the time the model of key last changed: unchanged while the ETag is.
func (d *dash) modifiedAt(key, etag string) time.Time {
	d.modMu.Lock()
	defer d.modMu.Unlock()
	if p, ok := d.mod[key]; ok && p.etag == etag {
		return p.at
	}
	if len(d.mod) >= maxModTimeKeys {
		clear(d.mod) // bounded; the worst case is a Last-Modified that moves forward
	}
	now := d.now().UTC().Truncate(time.Second)
	d.mod[key] = modStamp{etag: etag, at: now}
	return now
}

type modStamp struct {
	etag string
	at   time.Time
}

// etagMatches implements If-None-Match for a strong or weak validator list or "*".
func etagMatches(header, etag string) bool {
	if len(header) > maxINMHeader {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if part == "*" || part == etag {
			return true
		}
	}
	return false
}

func notModified(r *http.Request, m modelResult) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		return etagMatches(inm, m.etag)
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if t, err := http.ParseTime(ims); err == nil && !m.modified.After(t) {
			return true
		}
	}
	return false
}

func writeModel(w http.ResponseWriter, r *http.Request, m modelResult) {
	h := w.Header()
	h.Set("ETag", m.etag)
	h.Set("Last-Modified", m.modified.UTC().Format(http.TimeFormat))
	if notModified(r, m) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(m.body)
}

// writeJSONError answers a failed view model with a closed, fixed body.
func writeJSONError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if errors.Is(err, ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"session not found"}`))
		return
	}
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"data source unavailable"}`))
}

// archivedCacheKey separates the cached models of a request that asked for archived sessions from
// the others: one model must never answer both (D17 Q7: hidden by default).
func archivedCacheKey(ctx context.Context) string {
	if ArchivedShown(ctx) {
		return "\x00archived"
	}
	return ""
}

func (d *dash) stateListHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := d.model(r.Context(), "state\x00list"+archivedCacheKey(r.Context()), d.buildList)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeModel(w, r, m)
	})
}

func (d *dash) stateDetailHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, statePrefix), "/")
		if len(parts) != 2 || !knownHarness[parts[0]] || !idPattern.MatchString(parts[1]) {
			writeJSONError(w, ErrNotFound)
			return
		}
		harness, id := parts[0], parts[1]
		m, err := d.model(r.Context(), "state\x00detail\x00"+harness+"\x00"+id+archivedCacheKey(r.Context()), func(c context.Context) ([]byte, error) {
			return d.buildDetail(c, harness, id)
		})
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeModel(w, r, m)
	})
}

func (d *dash) auditJSONHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := d.model(r.Context(), "audit", d.buildAudit)
		if err != nil {
			writeJSONError(w, err)
			return
		}
		writeModel(w, r, m)
	})
}
