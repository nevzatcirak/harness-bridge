// Package audit holds the failure-reporting attempt writer of the Mutation Gate prerequisite
// (D15, ADR-024 section 4.2 steps 8 and 9, section 6): one AUDIT-01-shaped line written
// SYNCHRONOUSLY through the process logger, with the write error returned to the caller.
//
// The AUDIT-01 lines of internal/mcp/audit.go are written with slog.Info, which cannot report
// a failure, and after the call. The attempt record of a permission mutation must exist BEFORE
// the native write, and if it cannot be written there is no native write. This package is that
// writer. It defines no new journal record type and changes no existing line: the line has the
// AUDIT-01 shape (event, tool, outcome, principal, ... as slog attributes), goes through the same
// default slog handler (so through the same journal destination, the same lock and, when the
// dashboard is enabled, the same #251 tee) and differs from slog.Info only in that the handler's
// error is returned. S3 wrote the phase=attempt line; S5 adds the phase=result and phase=refused
// lines (ADR-024 section 6) through the same writer, the same lock and the same process-wide
// latch.
package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"
)

// Errors.
var (
	// ErrUnavailable means the attempt record was not written, or the writer is latched
	// unavailable: the caller must make no native write. It is the cause of every refusal
	// of this package except ErrInvalid.
	ErrUnavailable = errors.New("audit: the attempt record cannot be written; the audit is unavailable")
	// ErrInvalid means the fields were refused before anything was written (an unknown key, a
	// value outside its closed shape, a missing required field). Nothing was logged, the latch is
	// not set, and the caller still makes no native write.
	ErrInvalid = errors.New("audit: invalid attempt fields")
)

// Phases of a permission_reply line (ADR-024 section 6) and their log messages.
const (
	PhaseAttempt = "attempt"
	PhaseResult  = "result"
	PhaseRefused = "refused"
)

// Message is the log message of an attempt line.
const Message = "permission reply attempt"

var messages = map[string]string{PhaseAttempt: Message, PhaseResult: "permission reply result", PhaseRefused: "permission reply refused"}

// resultOutcomes are the closed outcomes of a result line: the reply was accepted (replied), the
// recorded result of an earlier call was returned (replay), or the native write did not take
// effect as an answer (unknown_outcome, not_found, native_rejected_id).
var resultOutcomes = map[string]bool{"replied": true, "replay": true, "unknown_outcome": true, "not_found": true, "native_rejected_id": true}

// field is one allowlisted attribute: its closed shape and how it is typed in the line.
type field struct {
	re       *regexp.Regexp
	kind     byte // 's' string, 'i' integer, 'b' bool
	required bool
}

func s(pattern string, required bool) field {
	return field{re: regexp.MustCompile(`^(?:` + pattern + `)$`), kind: 's', required: required}
}

// order is the fixed order of the attributes of the line. The AUDIT-01 attributes are written in
// a stable order; a caller cannot reorder them.
var order = []string{"tool", "attempt", "attempt_id", "supersedes", "outcome", "accepted", "error_code", "decision", "process_start",
	"permission_id", "execution_id", "session_id", "native_tool", "kind", "scope_digest", "pattern_count", "policy_mode", "policy_rule", "reason", "principal"}

// fields is the allowlist (ADR-024 section 6). Every value has a closed shape, so no free text,
// no pattern, no native text and no prompt can reach the line; a key that is not here is refused.
var fields = map[string]field{
	"tool":          s(`reply_permission`, true),
	"attempt":       s(`1|2`, true),
	"attempt_id":    s(`[A-Za-z0-9_-]{22}`, true),
	"supersedes":    s(`[A-Za-z0-9_-]{22}`, false),
	"outcome":       s(`[a-z_]{1,32}`, false),
	"accepted":      {re: regexp.MustCompile(`^(?:true|false)$`), kind: 'b'},
	"error_code":    s(`[A-Z_]{1,32}`, false),
	"decision":      s(`reject|allow_once`, true),
	"process_start": s(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z`, false),
	"permission_id": s(`per[A-Za-z0-9_-]{1,125}`, true),
	"execution_id":  s(`[0-9a-f]{32}`, true),
	"session_id":    s(`opencode:ses[A-Za-z0-9_-]{1,253}`, false),
	"native_tool":   s(`[A-Za-z0-9_.:-]{1,64}`, false),
	// kind has the shape the Bridge accepts from the observer (execution.safeTool); a narrower
	// shape here would pass the re-read and then fail the attempt line with INTERNAL (review #293 P3-2).
	"kind":          s(`[A-Za-z0-9_.:-]{1,64}`, false),
	"scope_digest":  s(`[0-9a-f]{16,64}`, false),
	"pattern_count": {re: regexp.MustCompile(`^[0-9]{1,4}$`), kind: 'i'},
	"policy_mode":   s(`reject_only|policy`, false),
	"policy_rule":   s(`[a-z0-9_]{1,48}`, false),
	"reason":        s(`[a-z0-9_]{1,48}`, false),
	"principal":     s(`[A-Za-z0-9._:=@-]{1,64}`, false),
}

// Fields is one attempt record: attribute name to value. event, phase, decided_by and
// human_verified are fixed by the writer and cannot be supplied.
type Fields map[string]string

// phaseRequired are the keys each phase must carry. The refused phase has no attempt: it is the
// one line of a call that returned before any attempt record was written.
var phaseRequired = map[string][]string{
	PhaseAttempt: {"tool", "attempt", "attempt_id", "decision", "permission_id", "execution_id"},
	PhaseResult:  {"tool", "attempt", "attempt_id", "decision", "permission_id", "execution_id", "outcome", "accepted"},
	PhaseRefused: {"tool", "outcome", "accepted", "error_code"},
}

// refusedForbidden are keys a refused line never carries: it describes no native request and no
// attempt of its own (a replay of a recorded UNKNOWN_OUTCOME may quote attempt and attempt_id).
var refusedForbidden = []string{"native_tool", "kind", "scope_digest", "pattern_count"}

// Clean returns the subset of f that is allowlisted and well formed. The refused and replay lines
// describe a call whose input may be arbitrary (a schema-rejected request, a malformed id): every
// value that fails its closed shape is dropped, so a refusal line can always be written and never
// carries what the input said.
func Clean(f Fields) Fields {
	out := Fields{}
	for k, v := range f {
		if fd, ok := fields[k]; ok && fd.re.MatchString(v) {
			out[k] = v
		}
	}
	return out
}

// attrs checks every key and value for phase and builds the attributes of the line in their
// fixed order.
func (f Fields) attrs(phase string) ([]any, error) {
	if _, ok := phaseRequired[phase]; !ok {
		return nil, fmt.Errorf("%w: unknown phase", ErrInvalid)
	}
	for k, v := range f {
		fd, ok := fields[k]
		if !ok {
			return nil, fmt.Errorf("%w: %q is not an allowlisted attribute", ErrInvalid, k)
		}
		if !fd.re.MatchString(v) {
			return nil, fmt.Errorf("%w: %q is outside its closed shape", ErrInvalid, k)
		}
	}
	attrs := []any{"event", "permission_reply"}
	for _, k := range phaseRequired[phase] {
		if _, ok := f[k]; !ok {
			return nil, fmt.Errorf("%w: %q is required", ErrInvalid, k)
		}
	}
	if err := f.crossCheck(phase); err != nil {
		return nil, err
	}
	// The AUDIT-01 order puts tool right after event, then phase.
	for _, k := range order {
		v, ok := f[k]
		if k == "tool" {
			attrs = append(attrs, "tool", v, "phase", phase)
			continue
		}
		if !ok {
			continue
		}
		switch fields[k].kind {
		case 'b':
			attrs = append(attrs, k, v == "true")
		case 'i':
			n, _ := strconv.Atoi(v)
			attrs = append(attrs, k, n)
		default:
			attrs = append(attrs, k, v)
		}
	}
	attrs = append(attrs, "decided_by", "chat_client", "human_verified", false)
	return attrs, nil
}

// crossCheck refuses combinations that are individually well formed but inconsistent (review
// #278 P3-3): a superseding attempt (attempt 2) carries `supersedes`, and only it does; a
// superseding value never equals the attempt's own ID; `allow_once` is never recorded under
// `policy_mode=reject_only`; the request description (native_tool, kind, scope_digest,
// pattern_count) is all present or all absent. The call site (ADR-024 S3) always knows these.
func (f Fields) crossCheck(phase string) error {
	bad := func(m string) error { return fmt.Errorf("%w: %s", ErrInvalid, m) }
	_, sup := f["supersedes"]
	// A replay returns the recorded result of an earlier call; it does not know that call's
	// supersedes value, so the attempt/supersedes pairing is checked for real attempts only.
	if phase == PhaseAttempt || (phase == PhaseResult && f["outcome"] != "replay") {
		if (f["attempt"] == "2") != sup {
			return bad("supersedes is required for attempt 2 and only allowed there")
		}
	}
	if sup && f["supersedes"] == f["attempt_id"] {
		return bad("supersedes equals attempt_id")
	}
	if f["policy_mode"] == "reject_only" && f["decision"] != "" && f["decision"] != "reject" && phase != PhaseRefused {
		return bad("only reject is recorded under policy_mode=reject_only")
	}
	n := 0
	for _, k := range []string{"native_tool", "kind", "scope_digest", "pattern_count"} {
		if _, ok := f[k]; ok {
			n++
		}
	}
	if n != 0 && n != 4 {
		return bad("native_tool, kind, scope_digest and pattern_count are all or none")
	}
	switch phase {
	case PhaseResult:
		if !resultOutcomes[f["outcome"]] {
			return bad("result outcome is outside the closed set")
		}
		ok := f["outcome"] == "replied" || f["outcome"] == "replay"
		if (f["accepted"] == "true") != ok {
			return bad("accepted is true exactly for replied and replay")
		}
		if _, has := f["error_code"]; has == ok {
			return bad("error_code is present exactly when the result was not accepted")
		}
	case PhaseRefused:
		if f["outcome"] != "refused" || f["accepted"] != "false" {
			return bad("a refused line is outcome=refused accepted=false")
		}
		for _, k := range refusedForbidden {
			if _, has := f[k]; has {
				return bad("a refused line carries no native request description")
			}
		}
		if _, a := f["attempt"]; a != hasKey(f, "attempt_id") {
			return bad("attempt and attempt_id go together")
		}
	}
	return nil
}

func hasKey(f Fields, k string) bool { _, ok := f[k]; return ok }

// journalGate is implemented by a handler that fans out to a journal and something else.
type journalGate interface {
	JournalEnabled(context.Context, slog.Level) bool
}

// Writer is the attempt writer. Its latch is the availability state of ADR-024 4.2 step 9: after
// one failed write it reports unavailable and every later attempt is refused WITHOUT writing
// anything, until the process restarts (the fail-closed minimum; any other recovery rule is a
// maintainer decision, see the as-built note of ADR-024). Use Default for the process-wide writer.
type Writer struct {
	unavailable atomic.Bool
	// handler returns the journal handler at call time; production uses slog.Default().Handler().
	handler func() slog.Handler
	now     func() time.Time
}

// newWriter returns a writer with its own latch, writing through the process logger. It is
// unexported on purpose (review #277 P3-4): production has exactly one latch, Default(), so a
// caller cannot obtain a fresh unlatched writer and bypass the process-wide fail-closed state.
// Other packages take the AttemptWriter interface of their consumer and fake it.
func newWriter() *Writer {
	return &Writer{handler: func() slog.Handler { return slog.Default().Handler() }, now: time.Now}
}

var process = newWriter()

// Default is the process-wide writer: its latch is shared by every caller in the process.
func Default() *Writer { return process }

// Unavailable reports whether a write has failed (the latch is set).
func (w *Writer) Unavailable() bool { return w.unavailable.Load() }

// Write writes one phase=attempt line; see WritePhase.
func (w *Writer) Write(ctx context.Context, f Fields) error {
	return w.WritePhase(ctx, PhaseAttempt, f)
}

// WritePhase writes one permission_reply line of the given phase and returns nil only if the
// journal handler accepted it. It returns ErrInvalid (nothing written, latch untouched) for bad
// fields, and an error wrapping ErrUnavailable if the writer was already latched (nothing
// written), the context was already done (nothing written, latch untouched: a caller that went
// away says nothing about the journal), or the write failed (the latch is now set). For the
// attempt phase the caller must make no native write on any error.
//
// The write is bounded by ctx (review #277 P3-2): a journal that does not return before the
// deadline latches the writer and the call refuses. The line may still reach the journal later;
// an attempt line without a native write is the conservative direction (it over-reports an
// attempt, never hides one), and the latch stops every later call.
func (w *Writer) WritePhase(ctx context.Context, phase string, f Fields) error {
	attrs, err := f.attrs(phase)
	if err != nil {
		return err
	}
	if w.unavailable.Load() {
		return fmt.Errorf("%w (latched until restart)", ErrUnavailable)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
	}
	h := w.handler()
	if h == nil || !h.Enabled(ctx, slog.LevelInfo) {
		w.unavailable.Store(true)
		return fmt.Errorf("%w: the journal handler does not accept info records", ErrUnavailable)
	}
	// A fan-out handler (the dashboard tee) reports Enabled when any branch wants the record, but
	// its journal branch may refuse it (a Warn-level journal): Handle then returns nil with no
	// line written (review #277 P3-1). Such a handler exposes JournalEnabled; the record is
	// accepted only if the journal itself accepts it.
	if g, ok := h.(journalGate); ok && !g.JournalEnabled(ctx, slog.LevelInfo) {
		w.unavailable.Store(true)
		return fmt.Errorf("%w: the journal branch does not accept info records", ErrUnavailable)
	}
	r := slog.NewRecord(w.now(), slog.LevelInfo, messages[phase], 0)
	r.Add(attrs...)
	return w.handle(ctx, h, r)
}

// handle calls the journal handler in its own goroutine so the call can honour ctx. An error, a
// panic or a deadline is a failed write: the latch is set and the error wraps ErrUnavailable. A
// cancellation (not a deadline) refuses the call without latching.
func (w *Writer) handle(ctx context.Context, h slog.Handler, r slog.Record) error {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- errPanicked
			}
		}()
		done <- h.Handle(ctx, r)
	}()
	select {
	case herr := <-done:
		if herr == errPanicked {
			w.unavailable.Store(true)
			return fmt.Errorf("%w: the journal handler panicked", ErrUnavailable)
		}
		if herr != nil {
			w.unavailable.Store(true)
			return fmt.Errorf("%w: %v", ErrUnavailable, herr)
		}
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			w.unavailable.Store(true)
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
	}
}

var errPanicked = errors.New("handler panicked")

// SetJournalForTesting replaces the journal handler of this writer and clears its latch, and
// returns a function that restores both. For tests only: production has no way to unlatch.
func (w *Writer) SetJournalForTesting(h slog.Handler) (restore func()) {
	prev := w.handler
	w.handler = func() slog.Handler { return h }
	w.unavailable.Store(false)
	return func() {
		w.handler = prev
		w.unavailable.Store(false)
	}
}

// CheckForTesting runs the validation of WritePhase without writing: it returns the attributes of
// the line or the ErrInvalid. For tests of the callers, so a fake writer accepts exactly what the
// real one would.
func CheckForTesting(phase string, f Fields) ([]any, error) { return f.attrs(phase) }
