package dashboard

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// auditRow is one line of the audit feed page. Every field is plain text for html/template; there is no free text
// beyond the writer's own constant message. The event and the outcome are chips with a sentence-case label from a
// fixed map (A1.1): a word outside the map is shown escaped, in a neutral chip.
type auditRow struct {
	Time                                        string
	Event, EventLabel, EventClass               string
	Outcome, OutcomeLabel, OutcomeClass         string
	Error, Session, Execution, Principal, Other string
}

// auditShown are the attributes that have their own column; the rest go in Other.
var auditShown = map[string]bool{"event": true, "outcome": true, "error_code": true, "session_id": true, "execution_id": true, "principal": true}

// auditEventLabels are the events the ring keeps (auditEvents), each a neutral chip.
var auditEventLabels = map[string]string{
	"guard_release": "Guard release", "prompt_submission": "Prompt submission", "interrupt": "Interrupt", "session_create": "Session create",
}

// auditOutcomeLabels are every outcome word the AUDIT-01 writers emit (internal/mcp/audit.go, internal/admin/admin.go);
// a test scans those files, so a new outcome without a label fails.
var auditOutcomeLabels = map[string]chip{
	"new": {Label: "New", Tone: "ok"}, "accepted": {Label: "Accepted", Tone: "ok"}, "created": {Label: "Created", Tone: "ok"},
	"released": {Label: "Released", Tone: "ok"}, "replay": {Label: "Replay", Tone: "neutral"},
	"archived": {Label: "Archived", Tone: "neutral"}, "unarchived": {Label: "Unarchived", Tone: "neutral"}, "closed": {Label: "Closed", Tone: "neutral"},
	"already_archived": {Label: "Already archived", Tone: "neutral"}, "not_archived": {Label: "Not archived", Tone: "neutral"},
	"rejected": {Label: "Rejected", Tone: "error"}, "refused": {Label: "Refused", Tone: "error"},
}

func toAuditRow(e AuditEntry) auditRow {
	r := auditRow{Time: fmtTime(e.Time), Event: e.Event, Outcome: e.Attr("outcome"), Error: e.Attr("error_code"),
		Session: e.Attr("session_id"), Execution: e.Attr("execution_id"), Principal: e.Attr("principal")}
	r.EventClass = chipClass("neutral")
	if l, ok := auditEventLabels[e.Event]; ok {
		r.EventLabel = l
	} else {
		r.EventLabel = e.Event // an unknown word is shown escaped, neutral (A1.1)
	}
	if r.Outcome != "" {
		if c, ok := auditOutcomeLabels[r.Outcome]; ok {
			r.OutcomeLabel, r.OutcomeClass = c.Label, chipClass(c.Tone)
		} else {
			r.OutcomeLabel, r.OutcomeClass = r.Outcome, chipClass("neutral")
		}
	}
	var other []string
	for _, a := range e.Attrs {
		if !auditShown[a.Key] {
			other = append(other, a.Key+"="+a.Value)
		}
	}
	r.Other = strings.Join(other, " ")
	return r
}

// auditPageSize is the page of the audit table (A1.4 item 5): a server-side, bounded GET parameter.
const auditPageSize = 100

// auditPager is the "Newer/Older" navigation of the audit page.
type auditPager struct {
	Show                 bool
	Range                string
	NewerHref, OlderHref string
}

// parseAuditPage reads ?page=N: digits only, 1 to the last page; anything else (empty, zero, negative, a word, a
// number too large, a second value) is page 1, never an error and never reflected.
func parseAuditPage(v string, pages int) int {
	if v == "" || len(v) > 6 {
		return 1
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return 1
		}
		n = n*10 + int(c-'0')
	}
	if n < 1 || n > pages {
		return 1
	}
	return n
}

func auditHref(page int) string {
	if page <= 1 {
		return "/audit"
	}
	return "/audit?page=" + strconv.Itoa(page)
}

// auditPageHandler serves GET /audit (ADR-023 A1.4 item 5): the ring in pages of 100, newest first, escaped, with a
// filter that runs in the browser only. The ring is the D17 Q4 process audit and is NOT ACL-filtered (A1.0).
func (d *dash) auditPageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := page{Title: "Audit - Cirak", Active: "audit", AuditPage: true}
		if d.audit == nil {
			p.AuditMsg = "The audit feed is not enabled."
			d.render(r.Context(), w, http.StatusOK, p)
			return
		}
		p.AuditOn = true
		entries := d.audit.Entries()
		pages := (len(entries) + auditPageSize - 1) / auditPageSize
		if pages < 1 {
			pages = 1
		}
		n := parseAuditPage(r.URL.Query().Get("page"), pages)
		p.AuditMsg = fmt.Sprintf("%d entries (the ring keeps the last %d; the journal is the audit authority).", len(entries), AuditRingSize)
		lo, hi := (n-1)*auditPageSize, n*auditPageSize
		if hi > len(entries) {
			hi = len(entries)
		}
		for _, e := range entries[lo:hi] {
			p.Audit = append(p.Audit, toAuditRow(e))
		}
		if len(entries) > auditPageSize {
			pg := auditPager{Show: true, Range: fmt.Sprintf("Entries %d to %d of %d", lo+1, hi, len(entries))}
			if n > 1 {
				pg.NewerHref = auditHref(n - 1)
			}
			if n < pages {
				pg.OlderHref = auditHref(n + 1)
			}
			p.AuditPager = pg
		}
		d.render(r.Context(), w, http.StatusOK, p)
	})
}
