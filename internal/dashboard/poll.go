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
	Harness      string   `json:"harness"`
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Root         string   `json:"root"`
	State        string   `json:"state"`
	Guard        apiGuard `json:"guard"`
	Current      string   `json:"current_execution,omitempty"`
	LastActivity string   `json:"last_activity"`
	Href         string   `json:"href,omitempty"`
}

type apiExec struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Started string `json:"started"`
	Updated string `json:"updated"`
	Ended   string `json:"ended"`
	Current bool   `json:"current"`
}

type apiList struct {
	Kind      string       `json:"kind"`
	Connected bool         `json:"connected"`
	Message   string       `json:"message"`
	Truncated string       `json:"truncated,omitempty"`
	Sessions  []apiSession `json:"sessions"`
}

type apiDetail struct {
	Kind       string     `json:"kind"`
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
	return apiSession{Harness: r.Harness, ID: r.ID, Title: r.Title, Root: r.Root, State: r.State, Guard: g, Current: r.Current, LastActivity: r.LastActivity, Href: r.Href}
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
	return out, truncated
}

// execRows applies the detail cap and normalization once, for the HTML and the JSON.
func execRows(execs []Execution) (out []execRow, truncated string) {
	if len(execs) > maxDetailRows {
		truncated = fmt.Sprintf("Showing the first %d of %d executions.", maxDetailRows, len(execs))
		execs = execs[:maxDetailRows]
	}
	for _, e := range execs {
		out = append(out, execRow{
			ID: truncRunes(e.ID, 128), State: normExecutionState(e.State), Current: e.Current,
			Started: fmtTime(e.Started), Updated: fmtTime(e.Updated), Ended: fmtTimePtr(e.Ended),
		})
	}
	return out, truncated
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
		m.Executions = append(m.Executions, apiExec{ID: e.ID, State: e.State, Started: e.Started, Updated: e.Updated, Ended: e.Ended, Current: e.Current})
	}
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

func (d *dash) stateListHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := d.model(r.Context(), "state\x00list", d.buildList)
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
		m, err := d.model(r.Context(), "state\x00detail\x00"+harness+"\x00"+id, func(c context.Context) ([]byte, error) {
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
