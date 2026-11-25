package execution

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
)

// defaultStallThreshold is the NO_PROGRESS report threshold (ADR-017 §3(a)).
// Suspicion is advisory only, so a long native step without part updates may
// be reported as suspected; that never changes state or the guard.
const defaultStallThreshold = 2 * time.Minute

// Liveness is the ADR-017 option (a) report-only block on execution status
// and result. It is computed from the reads the probe already makes and never
// changes state, ended_at, unresolved or the session guard. stall_suspected is
// never a terminal state and never success.
type Liveness struct {
	// NativeStatus is busy, retry, absent (no entry, or an explicit idle entry,
	// which OpenCode 1.19.4 does not keep) or unavailable (the native reads of
	// this probe did not all succeed, or the type is not recognized).
	NativeStatus string `json:"native_status"`
	// RetryAttempt and RetryNextAt accompany native_status retry when the
	// native values are well-formed.
	RetryAttempt *int64     `json:"retry_attempt,omitempty"`
	RetryNextAt  *time.Time `json:"retry_next_at,omitempty"`
	// LastNativeProgressAt is the latest native timestamp of the correlated
	// assistant messages and their recognized parts, never after ObservedAt.
	LastNativeProgressAt *time.Time `json:"last_native_progress_at,omitempty"`
	StallSuspected       bool       `json:"stall_suspected"`
	StallReason          string     `json:"stall_reason,omitempty"`
	// ADR-017 A2 S1: Variant A provider degradation, independent of
	// stall_suspected. A healthy retry is degradation, never a stall.
	DegradationReason string `json:"degradation_reason,omitempty"`
	// Degraded is only the alias of DegradationReason != "".
	Degraded bool `json:"degraded,omitempty"`
	// RetryOverdue is an additional hint: the current retry's next is past by
	// more than max(30s, no_progress_s) and the attempt has not advanced.
	RetryOverdue bool `json:"retry_overdue,omitempty"`
	// CauseHint is one closed word (network, rate_limit, auth, server, unknown)
	// and CauseSource its provenance (structured, bounded_text, none). The raw
	// provider text is never exported.
	CauseHint   string `json:"cause_hint,omitempty"`
	CauseSource string `json:"cause_source,omitempty"`
	// ObservedAt is the Bridge time of the probe that produced this block.
	ObservedAt time.Time `json:"observed_at"`
}

// maxMillis bounds native epoch-millisecond values (year 9999).
const maxMillis = 253402300799999

// maxPartBytes bounds the native part JSON parsed for progress timestamps;
// larger parts are ignored (ADR-017 §5 item 9), never partially parsed.
const maxPartBytes = 1 << 20

// ADR-017 A2 S1 (D32 Q5): the bounded-text fallback reads at most this many
// bytes of the retry message and matches only the closed pattern set below.
// The text itself is never stored, exported or audited.
const maxCauseText = 4096

// boundedCausePatterns is exactly the closed A2.5/D32 Q5 pattern set:
// 429/rate limit, 5xx/overloaded, DNS/TLS/connection, 401/403, timeout. A
// pattern is kept only when it literally expresses one of those terms; widening
// the set needs a new maintainer decision (D32). Matching is case-insensitive
// (the bounded text is lowercased) and only a closed word is produced. Order is
// precedence: rate_limit > auth > server > network; the first group that
// matches wins. Numeric codes use word boundaries so an identifier like "15003"
// is not a 5xx.
var boundedCausePatterns = []struct {
	hint string
	re   *regexp.Regexp
}{
	{"rate_limit", regexp.MustCompile(`\b429\b|\brate[\s_-]?limit`)},
	{"auth", regexp.MustCompile(`\b(?:401|403)\b`)},
	{"server", regexp.MustCompile(`\b5\d\d\b|\boverloaded\b`)},
	{"network", regexp.MustCompile(`\bdns\b|\btls\b|\bconnection\b`)},
	// A textual timeout is a connection-level signal; the structured mapping
	// still sends HTTP 408 to server (A2.5).
	{"network", regexp.MustCompile(`\btimeout\b`)},
}

// unavailableLiveness reports a probe whose native reads did not all succeed.
func unavailableLiveness(now time.Time) *Liveness {
	return &Liveness{NativeStatus: "unavailable", ObservedAt: now}
}

// setLiveness replaces the report block. Caller holds run.mu. It never touches
// state, error, unresolved, ended_at, updated_at or the guard.
func (m *Manager) setLiveness(x *run, l *Liveness) {
	if m.noLiveness {
		return
	}
	x.view.Liveness = l
}

// updateRetryMemory keeps the ADR-017 A2 retry_attempt_since memory on the
// execution record. Caller holds run.mu. A missing status (a failed read) is
// not evidence that the retry ended, so it leaves the memory untouched; a
// successful non-retry status clears it, and an attempt change resets it. It is
// report-only and never influences state, the guard or the terminal decision.
func (x *run) updateRetryMemory(status opencode.Status, exists bool, now time.Time) {
	if !exists {
		return
	}
	if status.Type != "retry" {
		x.retrySince = time.Time{}
		x.retryAttemptSeen = 0
		x.view.RetryAttemptSince = nil
		return
	}
	attempt := int64(-1) // absent or malformed attempt
	if v, err := strconv.ParseInt(strings.TrimSpace(string(status.Attempt)), 10, 64); err == nil && v >= 0 {
		attempt = v
	}
	if x.retrySince.IsZero() || x.retryAttemptSeen != attempt {
		x.retrySince = now
		x.retryAttemptSeen = attempt
	}
	t := x.retrySince
	x.view.RetryAttemptSince = &t
}

// nativeMillis accepts only a positive JSON integer literal no later than
// limit (epoch ms). Anything else (strings, floats, objects, overflow) is
// ignored, so hostile native JSON can only remove evidence, never add it.
func nativeMillis(raw json.RawMessage, limit int64) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || v <= 0 || v > limit {
		return 0, false
	}
	return v, true
}

type partTimes struct {
	Start json.RawMessage `json:"start"`
	End   json.RawMessage `json:"end"`
}

// partProgress returns the latest recognized timestamp of one native part:
// text/reasoning time.{start,end} and tool state.time.{start,end} (ADR-017
// N11). Unknown part types, malformed parts and parts over maxPartBytes are
// ignored.
func partProgress(raw json.RawMessage, limit int64) int64 {
	if len(raw) > maxPartBytes {
		return 0
	}
	var p struct {
		Type  string          `json:"type"`
		Time  *partTimes      `json:"time"`
		State json.RawMessage `json:"state"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return 0
	}
	var t *partTimes
	switch p.Type {
	case "text", "reasoning":
		t = p.Time
	case "tool":
		var s struct {
			Time *partTimes `json:"time"`
		}
		if json.Unmarshal(p.State, &s) != nil {
			return 0
		}
		t = s.Time
	}
	if t == nil {
		return 0
	}
	latest := int64(0)
	for _, v := range []json.RawMessage{t.Start, t.End} {
		if ms, ok := nativeMillis(v, limit); ok && ms > latest {
			latest = ms
		}
	}
	return latest
}

// providerError is one typed provider error candidate (an assistant error
// without terminal proof, or a persisted retry part), with the native timestamp
// used for "newest wins" (D32 Q8).
type providerError struct {
	name string
	data json.RawMessage
	at   int64
}

// assistantError returns the typed error of an assistant message. A malformed or
// nameless error is not a typed provider error.
func assistantError(raw json.RawMessage) (providerError, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return providerError{}, false
	}
	var e struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Name == "" {
		return providerError{}, false
	}
	return providerError{name: e.Name, data: e.Data}, true
}

// retryPartError returns the typed error of a persisted retry part
// ({type:"retry", attempt, error, time.created}); Q7 stays UNVERIFIED, but the
// part is honored when present. Oversized parts are ignored whole.
func retryPartError(raw json.RawMessage, limit int64) (providerError, bool) {
	if len(raw) > maxPartBytes {
		return providerError{}, false
	}
	var p struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
		Time  struct {
			Created json.RawMessage `json:"created"`
		} `json:"time"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Type != "retry" {
		return providerError{}, false
	}
	e, ok := assistantError(p.Error)
	if !ok {
		return providerError{}, false
	}
	// A timestamp after observed_at is ignored (A2.12 item 14); it must not
	// let a future error win "newest".
	if ms, ok := nativeMillis(p.Time.Created, limit); ok {
		e.at = ms
	}
	return e, true
}

// newestProviderError keeps the candidate with the greatest native timestamp
// (ties and missing timestamps prefer the later history entry).
func newestProviderError(current *providerError, next providerError) *providerError {
	if current != nil && next.at < current.at {
		return current
	}
	v := next
	return &v
}

// retryActionReason reads status.action.reason. An open string is not an error;
// it maps to unknown but stays structured (A2.5).
func retryActionReason(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var a struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(raw, &a) != nil || a.Reason == "" {
		return "", false
	}
	return a.Reason, true
}

// retryMessage reads the retry free-text status.message. It is only ever used
// by boundedHint; the caller must not store it.
func retryMessage(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" {
		return "", false
	}
	return s, true
}

// boundedHint lowercases at most maxCauseText bytes and maps the closed pattern
// set to a closed word. matched is false when no pattern matched; the caller
// then reports unknown with no text provenance (bounded_text is only ever set
// from the closed set, A2.3).
func boundedHint(message string) (hint string, matched bool) {
	if len(message) > maxCauseText {
		message = message[:maxCauseText]
	}
	s := strings.ToLower(message)
	for _, g := range boundedCausePatterns {
		if g.re.MatchString(s) {
			return g.hint, true
		}
	}
	return "unknown", false
}

// errorHint maps a typed error to the A2.5 closed cause words. A structured
// field that maps to unknown stays a structured source.
func errorHint(name string, data json.RawMessage) string {
	switch name {
	case "ProviderAuthError":
		return "auth"
	case "APIError":
		code := int64(0)
		var d struct {
			StatusCode json.RawMessage `json:"statusCode"`
		}
		if len(data) > 0 && string(data) != "null" {
			if json.Unmarshal(data, &d) != nil {
				return "unknown"
			}
			if len(d.StatusCode) > 0 && string(d.StatusCode) != "null" {
				v, err := strconv.ParseInt(strings.TrimSpace(string(d.StatusCode)), 10, 64)
				if err != nil {
					return "unknown"
				}
				code = v
			}
		}
		switch code {
		case 401, 403:
			return "auth"
		case 429:
			return "rate_limit"
		case 408, 500, 502, 503, 504, 529:
			return "server"
		case 0:
			return "network"
		default:
			return "unknown"
		}
	default:
		// ContextOverflowError, ContentFilterError, MessageOutputLengthError,
		// StructuredOutputError, MessageAbortedError, UnknownError and anything
		// unrecognized are not a provider transport cause.
		return "unknown"
	}
}

// retryCause picks the newest applicable structured source first (status action
// reason, then the newest typed error); only a status-only retry falls back to
// the bounded text. An open unknown action.reason is still structured, but the
// D32 Q5 fallback may refine it from the message (A2.12 item 2); with no
// matching text it stays unknown/structured. It never returns the raw text.
func retryCause(status opencode.Status, typed *providerError) (hint, source string) {
	if reason, ok := retryActionReason(status.Action); ok {
		switch reason {
		case "free_tier_limit", "account_rate_limit":
			return "rate_limit", "structured"
		default:
			if message, ok := retryMessage(status.Message); ok {
				if hint, matched := boundedHint(message); matched {
					return hint, "bounded_text"
				}
			}
			return "unknown", "structured"
		}
	}
	if typed != nil {
		return errorHint(typed.name, typed.data), "structured"
	}
	if message, ok := retryMessage(status.Message); ok {
		if hint, matched := boundedHint(message); matched {
			return hint, "bounded_text"
		}
	}
	return "unknown", "none"
}

type livenessEvidence struct {
	status            opencode.Status
	statusExists      bool
	userFound         bool
	assistants        []opencode.Message
	unfinished, proof bool
	permissionPending bool
	started           time.Time
	// retrySince is the Bridge-clock memory of the current retry attempt (zero
	// when none). ADR-017 A2 S1.
	retrySince time.Time
}

// assessLiveness computes the ADR-017 §3(a) report. It is advisory: callers
// must not derive state, ended_at or guard decisions from it.
func assessLiveness(now time.Time, threshold time.Duration, e livenessEvidence) *Liveness {
	l := &Liveness{ObservedAt: now}
	limit := now.UnixMilli()
	switch {
	case !e.statusExists || e.status.Type == "idle":
		l.NativeStatus = "absent"
	case e.status.Type == "busy" || e.status.Type == "retry":
		l.NativeStatus = e.status.Type
	default:
		l.NativeStatus = "unavailable"
	}
	if l.NativeStatus == "retry" {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(e.status.Attempt)), 10, 64); err == nil && v >= 0 {
			l.RetryAttempt = &v
		}
		if v, ok := nativeMillis(e.status.Next, maxMillis); ok {
			next := time.UnixMilli(v).UTC()
			l.RetryNextAt = &next
		}
	}
	progress, completed := int64(0), false
	var typed *providerError
	for _, msg := range e.assistants {
		if msg.Info.Time.Completed > 0 {
			completed = true
		}
		for _, v := range []int64{msg.Info.Time.Created, msg.Info.Time.Completed} {
			if v > progress && v <= limit {
				progress = v
			}
		}
		// ADR-017 A2 S1: an assistant error without terminal proof is a typed
		// provider-error candidate (A2.4 item 4). A timestamp after observed_at
		// is ignored (A2.12 item 14), so it cannot win "newest".
		if pe, ok := assistantError(msg.Info.Error); ok && terminalState(msg) == "" {
			pe.at = msg.Info.Time.Created
			if pe.at > limit {
				pe.at = 0
			}
			typed = newestProviderError(typed, pe)
		}
		for _, part := range msg.Parts {
			if pe, ok := retryPartError(part, limit); ok {
				typed = newestProviderError(typed, pe)
			}
			if v := partProgress(part, limit); v > progress {
				progress = v
			}
		}
	}
	if progress > 0 {
		at := time.UnixMilli(progress).UTC()
		l.LastNativeProgressAt = &at
	}
	// ADR-017 A2 S1 Variant A: provider degradation is an independent dimension.
	// A healthy retry is degradation, never a stall; the retry overdue grace is
	// independent of A1's wedged-retry NO_PROGRESS.
	if l.NativeStatus == "retry" {
		if l.RetryNextAt != nil && !e.retrySince.IsZero() {
			grace := 30 * time.Second
			if threshold > grace {
				grace = threshold
			}
			if now.Sub(*l.RetryNextAt) > grace && now.Sub(e.retrySince) >= threshold {
				l.RetryOverdue = true
			}
		}
		if l.RetryOverdue {
			l.DegradationReason = "PROVIDER_RETRY_OVERDUE"
		} else {
			l.DegradationReason = "PROVIDER_RETRYING"
		}
		l.CauseHint, l.CauseSource = retryCause(e.status, typed)
	} else if !e.proof && typed != nil {
		l.DegradationReason = "PROVIDER_ERROR"
		l.CauseHint, l.CauseSource = errorHint(typed.name, typed.data), "structured"
	}
	l.Degraded = l.DegradationReason != ""
	if e.permissionPending {
		return l
	}
	absent := l.NativeStatus == "absent"
	switch {
	case absent && e.userFound && !e.unfinished && !e.proof && completed:
		l.StallReason = "COMPLETED_WITHOUT_TERMINAL_EVIDENCE"
	case absent && e.userFound && len(e.assistants) == 0:
		l.StallReason = "NO_CORRELATED_ASSISTANT"
	case l.NativeStatus == "busy" || (l.NativeStatus == "retry" && l.RetryNextAt != nil && l.RetryNextAt.Before(now)):
		baseline := e.started
		if l.LastNativeProgressAt != nil && l.LastNativeProgressAt.After(baseline) {
			baseline = *l.LastNativeProgressAt
		}
		if now.Sub(baseline) > threshold {
			l.StallReason = "NO_PROGRESS"
		}
	}
	l.StallSuspected = l.StallReason != ""
	return l
}

func copyLiveness(l *Liveness) *Liveness {
	if l == nil {
		return nil
	}
	v := *l
	if l.RetryAttempt != nil {
		a := *l.RetryAttempt
		v.RetryAttempt = &a
	}
	v.RetryNextAt = copyTime(l.RetryNextAt)
	v.LastNativeProgressAt = copyTime(l.LastNativeProgressAt)
	return &v
}
