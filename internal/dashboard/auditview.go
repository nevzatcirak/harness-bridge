package dashboard

import (
	"fmt"
	"net/http"
	"strings"
)

// auditRow is one line of the audit feed page. Every field is plain text for
// html/template; there is no free text beyond the writer's own constant message.
type auditRow struct {
	Time, Event, Outcome, Error, Session, Execution, Principal, Other string
}

// auditShown are the attributes that have their own column; the rest go in Other.
var auditShown = map[string]bool{"event": true, "outcome": true, "error_code": true, "session_id": true, "execution_id": true, "principal": true}

func toAuditRow(e AuditEntry) auditRow {
	r := auditRow{Time: fmtTime(e.Time), Event: e.Event, Outcome: e.Attr("outcome"), Error: e.Attr("error_code"),
		Session: e.Attr("session_id"), Execution: e.Attr("execution_id"), Principal: e.Attr("principal")}
	var other []string
	for _, a := range e.Attrs {
		if !auditShown[a.Key] {
			other = append(other, a.Key+"="+a.Value)
		}
	}
	r.Other = strings.Join(other, " ")
	return r
}

// auditPageHandler serves GET /audit (ADR-023 section 5.3): the ring, newest first,
// escaped, with a filter that runs in the browser only.
func (d *dash) auditPageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := page{Title: "Harness Bridge dashboard · audit", AuditPage: true}
		if d.audit == nil {
			p.AuditMsg = "The audit feed is not enabled."
			renderPage(w, http.StatusOK, p)
			return
		}
		p.AuditOn = true
		entries := d.audit.Entries()
		p.AuditMsg = fmt.Sprintf("%d entries (the ring keeps the last %d; the journal is the audit authority).", len(entries), AuditRingSize)
		for _, e := range entries {
			p.Audit = append(p.Audit, toAuditRow(e))
		}
		renderPage(w, http.StatusOK, p)
	})
}
