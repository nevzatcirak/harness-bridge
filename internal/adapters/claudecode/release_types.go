package claudecode

import "time"

// The types of the Claude guard release (ADR-021 slice 4, ADR-020 extension). They live in
// the pure core, with no process code, so internal/admin can name them without linking the
// runner (and with it os/exec).

// DefaultReleaseSettle is the interval between the check and the re-check.
const DefaultReleaseSettle = 5 * time.Second

// ReleaseOptions are the operator's choices for one release.
type ReleaseOptions struct {
	// Settle is the interval between the check and the re-check; <= 0 means
	// DefaultReleaseSettle.
	Settle time.Duration
}

// ReleaseEvidence is what the release observed. There is never a native read.
type ReleaseEvidence struct {
	Harness         string     `json:"harness"`
	NativeRead      bool       `json:"native_read"`
	Row             string     `json:"row,omitempty"`
	ExitClass       string     `json:"exit_class,omitempty"`
	ChildExited     bool       `json:"child_exited"`
	GroupEmpty      bool       `json:"group_empty"`
	Rechecked       bool       `json:"rechecked"`
	SettleMS        int64      `json:"settle_ms,omitempty"`
	GuardReleasedAt *time.Time `json:"guard_released_at,omitempty"`
}
