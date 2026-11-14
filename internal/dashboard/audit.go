package dashboard

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// maxAuditValue bounds one displayed attribute value or message to this many bytes; a
// value that was cut carries a one-character ellipsis after them (so at most
// maxAuditValue+3 bytes), which is what the ADR means by "cut at 256 bytes".
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
	// ADR-021 slice 4/wiring: the Claude Code guard_release and mutation keys
	// (internal/admin/admin.go auditClaude, internal/mcp/audit.go harness). Enum, boolean
	// and short-token values only; never stream content.
	"harness": true, "child_exited": true, "group_empty": true, "rechecked": true, "row": true, "exit_class": true,
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

// Test seams for the two windows of the swap (nil in production): each runs at the exact
// point where the process logger is half swapped, so a test can log there.
var teeInstallWindow, teeRestoreWindow func()

// syncWriter serializes writes to the journal destination. The log package (after the swap)
// and the tee's journal branch both write through one of these, so their lines never
// interleave, as they never did when the log package's own lock was the only one.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// stdJournal is the journal branch used when the original default handler is Go's own.
//
// Go's default handler writes through the log package's GLOBAL logger, and slog.SetDefault
// points that same global logger back at the new default handler: a journal branch made of
// the original default handler would call the log lock that the very call being handled
// already holds (or is about to take), and the global logger deadlocks. Between
// SetDefault and the restore of the log writer that is a window in which any concurrent
// log or slog call hangs (review #252 P2-1). stdJournal therefore never touches the global
// logger: it formats a record exactly as the default handler does ("LEVEL message" and the
// attributes in text form, which is the same code as slog.TextHandler) and writes it with a
// private log.Logger on the original writer, prefix and flags. Nothing it calls can route
// back to the tee, so no ordering of install, restore and concurrent loggers can loop or
// block.
type stdJournal struct {
	enabled slog.Handler // the original default handler: only Enabled is ever called on it
	lg      *log.Logger
	ops     []func(slog.Handler) slog.Handler // WithAttrs/WithGroup, replayed in order
}

func (j *stdJournal) Enabled(ctx context.Context, l slog.Level) bool {
	return j.enabled.Enabled(ctx, l)
}

func (j *stdJournal) Handle(ctx context.Context, r slog.Record) error {
	var buf bytes.Buffer
	var h slog.Handler = slog.NewTextHandler(&buf, nil)
	for _, op := range j.ops {
		h = op(h)
	}
	// The built-in fields are produced with a fixed level, no time and an empty message and
	// stripped by their exact text, so a user attribute named time, level or msg is kept
	// as an ordinary attribute, as the default handler keeps it.
	nr := slog.NewRecord(time.Time{}, slog.LevelInfo, "", 0)
	r.Attrs(func(a slog.Attr) bool { nr.AddAttrs(a); return true })
	if err := h.Handle(ctx, nr); err != nil {
		return err
	}
	attrs := strings.TrimPrefix(strings.TrimSuffix(buf.String(), "\n"), `level=INFO msg=""`)
	return j.lg.Output(2, r.Level.String()+" "+r.Message+attrs)
}

func (j *stdJournal) WithAttrs(a []slog.Attr) slog.Handler {
	c := *j
	c.ops = append(append([]func(slog.Handler) slog.Handler(nil), j.ops...), func(h slog.Handler) slog.Handler { return h.WithAttrs(a) })
	return &c
}

func (j *stdJournal) WithGroup(name string) slog.Handler {
	if name == "" {
		return j
	}
	c := *j
	c.ops = append(append([]func(slog.Handler) slog.Handler(nil), j.ops...), func(h slog.Handler) slog.Handler { return h.WithGroup(name) })
	return &c
}

// isStdDefault reports whether h is Go's own default handler (an unexported type).
func isStdDefault(h slog.Handler) bool { return fmt.Sprintf("%T", h) == "*slog.defaultHandler" }

// InstallAuditTee replaces the process default logger with a tee of the original default
// handler and ring, and returns a function that puts the original back.
//
// The swap is unobservable to concurrent loggers: no log or slog call can deadlock, loop or
// lose its line at any moment of the install or of the restore (review #252 P2-1). The
// journal branch never goes through the log package's global logger (see stdJournal), so
// the half-swapped states are harmless: slog.SetDefault leaves the log package pointed at
// the tee, and the writer and flags are put back right after; a log.Printf that runs in
// between is converted by the log package into an Info record, goes to the tee and is
// written to the journal as "INFO <message>" (its message is the same, the line carries the
// "INFO " level word that a plain log line does not). On restore, slog.SetDefault with the
// original default handler does not touch the log package at all. After install the log
// package writes to the same original destination as before, through a lock that the
// journal branch shares, so lines never interleave.
//
// When the original default handler is not Go's own (a program that installed its own),
// it is used as the journal branch as it is. Not supported: the log flags Lshortfile and
// Llongfile (a journal line would name the tee, not the caller; the Bridge uses neither),
// and installing a second tee over this one. Call it once, before the first audit line.
func InstallAuditTee(ring *AuditRing) (restore func()) {
	prev := slog.Default()
	w, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	shared := &syncWriter{w: w}
	var journal slog.Handler = prev.Handler()
	if isStdDefault(journal) {
		journal = &stdJournal{enabled: journal, lg: log.New(shared, prefix, flags)}
	}
	slog.SetDefault(slog.New(NewAuditTee(journal, ring)))
	if teeInstallWindow != nil {
		teeInstallWindow()
	}
	log.SetOutput(shared)
	log.SetFlags(flags)
	log.SetPrefix(prefix)
	return func() {
		slog.SetDefault(prev)
		if teeRestoreWindow != nil {
			teeRestoreWindow()
		}
		log.SetOutput(w)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	}
}
