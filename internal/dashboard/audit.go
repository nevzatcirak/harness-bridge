package dashboard

import (
	"context"
	"log"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The audit feed of ADR-023 section 1.1 and 5.3: a bounded, in-memory, display-only
// copy of the AUDIT-01 lines. The journal stays the audit authority; nothing here
// reads the journal, opens a file or persists an entry.

// AuditRingSize is the capacity of the ring (D17 Q4): the oldest entry is dropped.
const AuditRingSize = 1000

// maxAuditValue bounds one displayed attribute value or message, in bytes.
const maxAuditValue = 256

// auditEvents is the closed allowlist of events the ring keeps (task #250; the
// ADR's "future lifecycle/permission events" are not shown until this list is
// amended). Every other record is dropped.
var auditEvents = map[string]bool{"guard_release": true, "prompt_submission": true, "interrupt": true}

// auditAttrs is the attribute allowlist enumerated from the existing AUDIT-01
// writers (internal/mcp/audit.go, internal/admin/admin.go). Any other attribute is
// dropped; a future attribute is not shown until this list is amended.
var auditAttrs = map[string]bool{
	"event": true, "tool": true, "outcome": true, "accepted": true, "error_code": true,
	"session_id": true, "execution_id": true, "idem": true, "principal": true,
	"native_state": true, "unfinished": true, "permission_pending": true, "correlated_user": true,
	"settle_ms": true, "allow_missing_user_message": true, "ticket": true,
}

// AuditAttr is one kept attribute, in the order the writer logged it.
type AuditAttr struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// AuditEntry is one ring entry: the record time, the message and the allowlisted
// attributes. It has no free-text field beyond the writer's own constant message.
type AuditEntry struct {
	Time    time.Time   `json:"time"`
	Message string      `json:"message"`
	Event   string      `json:"event"`
	Attrs   []AuditAttr `json:"attrs"`
}

// Attr returns the value of a key, or "".
func (e AuditEntry) Attr(key string) string {
	for _, a := range e.Attrs {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

// AuditSource is all the dashboard can do with the ring: read it, newest first. A
// test pins the method set; there is no method that writes.
type AuditSource interface {
	Entries() []AuditEntry
}

// AuditRing is the bounded ring. Its write method is unexported and used only by
// the tee handler of this package, so nothing that holds an AuditSource can add to it.
type AuditRing struct {
	mu    sync.Mutex
	buf   [AuditRingSize]AuditEntry
	start int // index of the oldest entry
	n     int
}

// NewAuditRing returns an empty ring.
func NewAuditRing() *AuditRing { return &AuditRing{} }

func (r *AuditRing) add(e AuditEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n < AuditRingSize {
		r.buf[(r.start+r.n)%AuditRingSize] = e
		r.n++
		return
	}
	r.buf[r.start] = e // drop-oldest
	r.start = (r.start + 1) % AuditRingSize
}

// Entries returns a copy of the ring, newest first.
func (r *AuditRing) Entries() []AuditEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]AuditEntry, 0, r.n)
	for i := r.n - 1; i >= 0; i-- {
		e := r.buf[(r.start+i)%AuditRingSize]
		e.Attrs = append([]AuditAttr(nil), e.Attrs...)
		out = append(out, e)
	}
	return out
}

// auditTee is the fan-out slog.Handler (ADR-023 section 1.1). Branch 1 is the
// original journal handler, called exactly once per enabled record with the record
// untouched, so the journal output is unchanged. Branch 2 copies an allowlisted
// projection of the record into the ring.
type auditTee struct {
	journal slog.Handler
	ring    *AuditRing
	red     *Redactor
	pre     []slog.Attr // allowlisted attributes bound with WithAttrs, before any group
	grouped bool        // WithGroup was called: later attributes are namespaced, so none is allowlisted
}

// NewAuditTee returns the tee of journal and ring.
func NewAuditTee(journal slog.Handler, ring *AuditRing) slog.Handler {
	return &auditTee{journal: journal, ring: ring, red: DefaultRedactor()}
}

// Enabled is true when the journal wants the record or the ring could (Info and up).
func (t *auditTee) Enabled(ctx context.Context, l slog.Level) bool {
	return t.journal.Enabled(ctx, l) || l >= slog.LevelInfo
}

// Handle writes the journal branch, then the ring branch. The ring branch never
// fails and never panics into the caller.
func (t *auditTee) Handle(ctx context.Context, r slog.Record) error {
	var err error
	if t.journal.Enabled(ctx, r.Level) {
		err = t.journal.Handle(ctx, r)
	}
	t.record(r)
	return err
}

func (t *auditTee) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *t
	c.journal = t.journal.WithAttrs(attrs)
	if !t.grouped {
		c.pre = append(append([]slog.Attr(nil), t.pre...), attrs...)
	}
	return &c
}

func (t *auditTee) WithGroup(name string) slog.Handler {
	if name == "" {
		return t
	}
	c := *t
	c.journal = t.journal.WithGroup(name)
	c.grouped = true
	return &c
}

// record projects r onto the allowlists and adds it to the ring. A record whose
// event is not allowlisted, or has none, is dropped; an attribute that is not
// allowlisted, is a group, is namespaced by WithGroup, or is not a plain scalar is
// dropped.
func (t *auditTee) record(r slog.Record) {
	defer func() { _ = recover() }() // display-only: a bug here must never fail a log call
	var attrs []slog.Attr
	attrs = append(attrs, t.pre...)
	if !t.grouped {
		r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
	}
	e := AuditEntry{Time: r.Time, Message: t.clean(r.Message)}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	index := map[string]int{}
	for _, a := range attrs {
		a.Value = a.Value.Resolve()
		if !auditAttrs[a.Key] {
			continue // includes group-kind attributes: their key is not an allowlisted name
		}
		v, ok := auditValue(a.Value)
		if !ok {
			continue
		}
		v = t.clean(v)
		if i, dup := index[a.Key]; dup {
			e.Attrs[i].Value = v // last one wins, as a reader of the line would see
			continue
		}
		index[a.Key] = len(e.Attrs)
		e.Attrs = append(e.Attrs, AuditAttr{Key: a.Key, Value: v})
	}
	i, ok := index["event"]
	if !ok || !auditEvents[e.Attrs[i].Value] {
		return
	}
	e.Event = e.Attrs[i].Value
	t.ring.add(e)
}

// auditValue renders a plain scalar. Anything else (a struct, an error, a group, a
// stringer) is not displayed: only the kinds the AUDIT-01 writers use pass.
func auditValue(v slog.Value) (string, bool) {
	switch v.Kind() {
	case slog.KindString:
		return v.String(), true
	case slog.KindBool:
		return strconv.FormatBool(v.Bool()), true
	case slog.KindInt64:
		return strconv.FormatInt(v.Int64(), 10), true
	case slog.KindUint64:
		return strconv.FormatUint(v.Uint64(), 10), true
	}
	return "", false
}

// clean makes a value displayable and bounded: valid UTF-8, no control characters,
// credential shapes redacted, at most maxAuditValue bytes.
func (t *auditTee) clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s, _ = t.red.Redact(s)
	if len(s) > maxAuditValue {
		s = s[:maxAuditValue]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}

// InstallAuditTee replaces the process default logger with a tee of the original
// default handler and ring, and returns a function that puts the original back.
// slog.SetDefault would also point the log package at the new handler, and the
// original default handler writes through the log package, so that would loop; the
// log writer and flags are therefore restored right after, and the journal branch
// writes exactly where it wrote before. Call it once, before the first audit line.
func InstallAuditTee(ring *AuditRing) (restore func()) {
	prev := slog.Default()
	w, flags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(NewAuditTee(prev.Handler(), ring)))
	log.SetOutput(w)
	log.SetFlags(flags)
	return func() {
		slog.SetDefault(prev)
		log.SetOutput(w)
		log.SetFlags(flags)
	}
}
