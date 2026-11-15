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
// error is returned. It is NOT wired into any tool (slice S5 does that).
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

	"github.com/nevzatcirak/harness-bridge/internal/execution"
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

// Message is the log message of an attempt line.
const Message = "permission reply attempt"

// AuditUnavailableMessage is the message of the typed refusal (ADR-024 4.2 step 8).
const AuditUnavailableMessage = "audit_unavailable: the audit record could not be written; no native write was made"

// Refusal is the typed refusal a tool returns when the attempt record could not be written (or
// the writer is latched): UPSTREAM_FAILURE with reason audit_unavailable, retryable. CONTRACTS has
// no local-failure code, so this is the nearest existing one (a proposal of ADR-024). It is
// defined here and exposed by no tool yet.
func Refusal() *execution.Error {
	return &execution.Error{Code: "UPSTREAM_FAILURE", Message: AuditUnavailableMessage, Retryable: true}
}

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
	"attempt":       s(`1|2`, false),
	"attempt_id":    s(`[A-Za-z0-9_-]{22}`, true),
	"supersedes":    s(`[A-Za-z0-9_-]{22}`, false),
	"outcome":       s(`[a-z_]{1,32}`, false),
	"accepted":      {re: regexp.MustCompile(`^(?:true|false)$`), kind: 'b'},
	"error_code":    s(`[A-Z_]{1,32}`, false),
	"decision":      s(`reject|allow_once`, true),
	"process_start": s(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z`, false),
	"permission_id": s(`[A-Za-z0-9_-]{1,64}`, true),
	"execution_id":  s(`[0-9a-f]{32}`, true),
	"session_id":    s(`opencode:ses[A-Za-z0-9_-]{1,64}`, false),
	"native_tool":   s(`[A-Za-z0-9_.:-]{1,64}`, false),
	"kind":          s(`[a-z_]{1,32}`, false),
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

// validate checks every key and value and builds the attributes of the line in their fixed order.
func (f Fields) attrs() ([]any, error) {
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
	for _, k := range order {
		if fields[k].required {
			if _, ok := f[k]; !ok {
				return nil, fmt.Errorf("%w: %q is required", ErrInvalid, k)
			}
		}
	}
	// The AUDIT-01 order puts tool right after event, then phase.
	for _, k := range order {
		v, ok := f[k]
		if k == "tool" {
			attrs = append(attrs, "tool", v, "phase", "attempt")
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

// NewWriter returns a writer with its own latch, writing through the process logger.
func NewWriter() *Writer {
	return &Writer{handler: func() slog.Handler { return slog.Default().Handler() }, now: time.Now}
}

var process = NewWriter()

// Default is the process-wide writer: its latch is shared by every caller in the process.
func Default() *Writer { return process }

// Unavailable reports whether a write has failed (the latch is set).
func (w *Writer) Unavailable() bool { return w.unavailable.Load() }

// Write writes one attempt line and returns nil only if the journal handler accepted it. It
// returns ErrInvalid (nothing written, latch untouched) for bad fields, and an error wrapping
// ErrUnavailable if the writer was already latched (nothing written) or the write failed (the
// latch is now set). The caller must make no native write on any error.
func (w *Writer) Write(ctx context.Context, f Fields) error {
	attrs, err := f.attrs()
	if err != nil {
		return err
	}
	if w.unavailable.Load() {
		return fmt.Errorf("%w (latched until restart)", ErrUnavailable)
	}
	h := w.handler()
	if h == nil || !h.Enabled(ctx, slog.LevelInfo) {
		w.unavailable.Store(true)
		return fmt.Errorf("%w: the journal handler does not accept info records", ErrUnavailable)
	}
	r := slog.NewRecord(w.now(), slog.LevelInfo, Message, 0)
	r.Add(attrs...)
	return w.handle(ctx, h, r)
}

// handle calls the journal handler. An error or a panic is a failed write: the latch is set
// and the error wraps ErrUnavailable.
func (w *Writer) handle(ctx context.Context, h slog.Handler, r slog.Record) (err error) {
	defer func() {
		if p := recover(); p != nil {
			w.unavailable.Store(true)
			err = fmt.Errorf("%w: the journal handler panicked", ErrUnavailable)
		}
	}()
	if herr := h.Handle(ctx, r); herr != nil {
		w.unavailable.Store(true)
		return fmt.Errorf("%w: %v", ErrUnavailable, herr)
	}
	return nil
}
