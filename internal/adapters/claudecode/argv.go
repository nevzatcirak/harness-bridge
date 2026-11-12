package claudecode

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// ArgvConfig is everything the per-run argv depends on. There is deliberately no
// prompt field: the prompt travels over the child's stdin, never argv (§1).
type ArgvConfig struct {
	UUID         string // lowercase UUID of the exact session to resume
	Model        string
	MaxTurns     int    // 1..1000
	MaxBudgetUSD string // optional decimal such as "1.50"; empty means no flag
}

var (
	modelPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	budgetPattern = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,4})?$`)
	turnsPattern  = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
)

const maxTurnsLimit = 1000

// BuildArgv returns the arguments after the wrapper path (ADR-021 §1), in the
// one fixed order. The permission flags are pinned and never configurable.
func BuildArgv(c ArgvConfig) ([]string, error) {
	switch {
	case !ValidUUID(c.UUID):
		return nil, errors.New("claudecode: invalid session uuid")
	case !modelPattern.MatchString(c.Model):
		return nil, errors.New("claudecode: invalid model name")
	case c.MaxTurns < 1 || c.MaxTurns > maxTurnsLimit:
		return nil, errors.New("claudecode: max turns out of range")
	case c.MaxBudgetUSD != "" && !budgetPattern.MatchString(c.MaxBudgetUSD):
		return nil, errors.New("claudecode: invalid max budget")
	}
	argv := []string{"-p", "--resume", c.UUID, "--output-format", "stream-json", "--verbose",
		"--model", c.Model, "--permission-mode", "default", "--permission-prompts", "none",
		"--max-turns", strconv.Itoa(c.MaxTurns)}
	if c.MaxBudgetUSD != "" {
		argv = append(argv, "--max-budget-usd", c.MaxBudgetUSD)
	}
	return argv, nil
}

// CheckArgv is the allow-pattern the Bridge applies to the argv it is about to
// exec: exactly the BuildArgv shape, nothing else. Any extra argument, a changed
// permission flag, a reordering or a positional (a prompt) is refused.
func CheckArgv(argv []string) error {
	if len(argv) != 14 && len(argv) != 16 {
		return errors.New("claudecode: argv does not match the pinned shape")
	}
	fixed := map[int]string{0: "-p", 1: "--resume", 3: "--output-format", 4: "stream-json", 5: "--verbose",
		6: "--model", 8: "--permission-mode", 9: "default", 10: "--permission-prompts", 11: "none", 12: "--max-turns"}
	for i, want := range fixed {
		if argv[i] != want {
			return fmt.Errorf("claudecode: argv[%d] is not the pinned value", i)
		}
	}
	if !ValidUUID(argv[2]) {
		return errors.New("claudecode: argv session uuid invalid")
	}
	if !modelPattern.MatchString(argv[7]) {
		return errors.New("claudecode: argv model invalid")
	}
	if !turnsPattern.MatchString(argv[13]) {
		return errors.New("claudecode: argv max turns invalid")
	}
	if n, _ := strconv.Atoi(argv[13]); n > maxTurnsLimit {
		return errors.New("claudecode: argv max turns invalid")
	}
	if len(argv) == 16 && (argv[14] != "--max-budget-usd" || !budgetPattern.MatchString(argv[15])) {
		return errors.New("claudecode: argv budget invalid")
	}
	return nil
}
