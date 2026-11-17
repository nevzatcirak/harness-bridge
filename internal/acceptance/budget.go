package acceptance

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// Hard limits of the task (#314): at most 30 CLI runs, --max-turns at most 5.
const (
	DefaultMaxRuns = 30
	MaxTurnsLimit  = 5
)

// ErrBudget is returned by Take once the run budget is exhausted. It is sticky: after the first
// breach no later run is allowed, whatever its label.
var ErrBudget = errors.New("acceptance: the run budget is exhausted")

// Budget counts CLI runs.
type Budget struct {
	mu      sync.Mutex
	max     int
	used    int
	stopped bool
}

// NewBudget returns a budget of max runs (1..DefaultMaxRuns; a larger value is refused so a
// configuration cannot raise the task's hard cap).
func NewBudget(max int) (*Budget, error) {
	if max < 1 || max > DefaultMaxRuns {
		return nil, fmt.Errorf("acceptance: the run budget must be 1..%d", DefaultMaxRuns)
	}
	return &Budget{max: max}, nil
}

// Take reserves one run, or reports ErrBudget (and stays stopped).
func (b *Budget) Take() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped || b.used >= b.max {
		b.stopped = true
		return ErrBudget
	}
	b.used++
	return nil
}

// Used is the number of runs taken.
func (b *Budget) Used() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// Stopped reports whether the budget has been breached.
func (b *Budget) Stopped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopped
}

// ValidateArgv is the driver's own allow-check on an argv it is about to run through the wrapper:
// never --bare, never a flag that grants permissions (any --dangerously-*, an allow-list of tools,
// a permission mode other than default, permission prompts other than none, a permission-prompt
// tool), --max-turns present and at most 5.
func ValidateArgv(args []string) error {
	turns := -1
	val := func(i int) string {
		if i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	for i, a := range args {
		switch {
		case a == "--bare":
			return errors.New("acceptance: --bare is never used (it cannot use the subscription login)")
		case strings.HasPrefix(a, "--dangerously-"), strings.HasPrefix(a, "--allow-dangerously-"):
			return errors.New("acceptance: a permission-skipping flag is refused")
		case a == "--allowedTools", a == "--allowed-tools", a == "--permission-prompt-tool":
			return errors.New("acceptance: a flag that grants tool permissions is refused")
		case a == "--permission-mode" && val(i) != "default":
			return errors.New("acceptance: only --permission-mode default is allowed")
		case a == "--permission-prompts" && val(i) != "none":
			return errors.New("acceptance: only --permission-prompts none is allowed")
		case a == "--max-turns":
			if i+1 >= len(args) {
				return errors.New("acceptance: --max-turns has no value")
			}
			n := 0
			if _, err := fmt.Sscanf(args[i+1], "%d", &n); err != nil || n < 1 || n > MaxTurnsLimit {
				return fmt.Errorf("acceptance: --max-turns must be 1..%d", MaxTurnsLimit)
			}
			turns = n
		}
	}
	if turns < 0 {
		return errors.New("acceptance: --max-turns is required")
	}
	return nil
}

// ValidateWrapper accepts an absolute, clean wrapper path and refuses a direct CLI path.
func ValidateWrapper(p string) error {
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return errors.New("acceptance: the wrapper must be an absolute clean path")
	}
	if filepath.Base(p) == "claude" {
		return errors.New("acceptance: the CLI is run only through the wrapper, never directly")
	}
	return nil
}
