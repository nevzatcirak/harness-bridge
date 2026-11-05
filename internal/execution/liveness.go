package execution

import "time"

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
