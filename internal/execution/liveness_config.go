package execution

// ADR-017 A2 S3: the operator thresholds (A2.6, D32 Q3/Q13). They are read from
// the effective configuration at startup (the CIRAK_* environment names or the
// ADR-027 liveness.* file keys) and are never a per-execution input. There is no
// legacy twin: only the CIRAK_* names are read.

import (
	"fmt"
	"strconv"
	"time"
)

const (
	// DefaultNoProgressS and DefaultToolLongS are A2.6's defaults (seconds).
	DefaultNoProgressS = 120
	DefaultToolLongS   = 600

	// A2.6's bounds (seconds).
	MinNoProgressS = 30
	MaxNoProgressS = 900
	MinToolLongS   = 60
	MaxToolLongS   = 3600
)

// unavailableThreshold is the fixed A2.4 item 1 native-read failure window
// (unavailable_s): it is not operator-configured in v2.
const unavailableThreshold = 2 * time.Minute

// LivenessConfig is the A2.6 operator configuration actually applied.
type LivenessConfig struct {
	NoProgressS int
	ToolLongS   int
}

// ParseLivenessEnv reads CIRAK_LIVENESS_NO_PROGRESS_S and
// CIRAK_LIVENESS_TOOL_LONG_S from the effective environment: the ADR-027
// liveness.no_progress_s/liveness.tool_long_s file keys resolve to the same two
// names, so the file value, the environment value and their every-key conflict
// rule are handled by internal/config. An absent (or empty) variable uses its
// default; a non-integer, a negative value or an out-of-range value is an error
// naming the variable, never a silent default. There is no legacy twin.
func ParseLivenessEnv(env func(string) string) (LivenessConfig, error) {
	cfg := LivenessConfig{NoProgressS: DefaultNoProgressS, ToolLongS: DefaultToolLongS}
	if v := env("CIRAK_LIVENESS_NO_PROGRESS_S"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < MinNoProgressS || n > MaxNoProgressS {
			return LivenessConfig{}, fmt.Errorf("CIRAK_LIVENESS_NO_PROGRESS_S must be an integer between %d and %d", MinNoProgressS, MaxNoProgressS)
		}
		cfg.NoProgressS = n
	}
	if v := env("CIRAK_LIVENESS_TOOL_LONG_S"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < MinToolLongS || n > MaxToolLongS {
			return LivenessConfig{}, fmt.Errorf("CIRAK_LIVENESS_TOOL_LONG_S must be an integer between %d and %d", MinToolLongS, MaxToolLongS)
		}
		cfg.ToolLongS = n
	}
	return cfg, nil
}

// NewWithLiveness is New with the A2.6 thresholds applied. cfg is expected to
// come from ParseLivenessEnv, which enforces the bounds; a zero field keeps the
// built-in default.
func NewWithLiveness(r Runner, cfg LivenessConfig) *Manager {
	m := New(r)
	if cfg.NoProgressS > 0 {
		m.stallThreshold = time.Duration(cfg.NoProgressS) * time.Second
	}
	if cfg.ToolLongS > 0 {
		m.toolLongThreshold = time.Duration(cfg.ToolLongS) * time.Second
	}
	return m
}

// thresholdsObject is the {no_progress_s, tool_long_s} object echoed in the
// liveness block (A2.3). The Manager's fields are immutable after startup.
func (m *Manager) thresholdsObject() *Thresholds {
	return &Thresholds{
		NoProgressS: int(m.stallThreshold / time.Second),
		ToolLongS:   int(m.toolLongThreshold / time.Second),
	}
}
