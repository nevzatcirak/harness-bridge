package execution

import (
	"encoding/json"
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
	// ObservedAt is the Bridge time of the probe that produced this block.
	ObservedAt time.Time `json:"observed_at"`
}

// maxMillis bounds native epoch-millisecond values (year 9999).
const maxMillis = 253402300799999

// maxPartBytes bounds the native part JSON parsed for progress timestamps;
// larger parts are ignored (ADR-017 §5 item 9), never partially parsed.
const maxPartBytes = 1 << 20

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

// livenessEvidence is what one successful probe already read; assessing it
// performs no I/O.
type livenessEvidence struct {
	status            opencode.Status
	statusExists      bool
	userFound         bool
	assistants        []opencode.Message
	unfinished, proof bool
	permissionPending bool
	started           time.Time
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
	for _, msg := range e.assistants {
		if msg.Info.Time.Completed > 0 {
			completed = true
		}
		for _, v := range []int64{msg.Info.Time.Created, msg.Info.Time.Completed} {
			if v > progress && v <= limit {
				progress = v
			}
		}
		for _, part := range msg.Parts {
			if v := partProgress(part, limit); v > progress {
				progress = v
			}
		}
	}
	if progress > 0 {
		at := time.UnixMilli(progress).UTC()
		l.LastNativeProgressAt = &at
	}
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
