package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ArgvConfig is everything the per-run argv depends on. There is deliberately no
// prompt field: the prompt travels over the child's stdin, never argv (§1).
type ArgvConfig struct {
	UUID         string // lowercase UUID of the exact session to resume
	Model        string
	MaxTurns     int    // 1..1000
	MaxBudgetUSD string // optional decimal such as "1.50"; empty means no flag

	// Root is the session's canonical root; it names the CLAUDE.md files excluded by
	// the inline --settings (§8.3 item 2).
	Root string
	// Tools is the built-in tool profile (--tools); see CheckToolProfile.
	Tools []string
	// ConfigDir is the directory of CLAUDE_CONFIG_DIR. It is TEST-ONLY defense in depth
	// (§8.3 item 6): the production child environment does not carry it, so production
	// leaves it empty and relies on the ~/.claude/** and ~/.config/anthropic/** rules.
	ConfigDir string
}

var (
	modelPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	budgetPattern = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,4})?$`)
	turnsPattern  = regexp.MustCompile(`^[1-9][0-9]{0,3}$`)
)

const maxTurnsLimit = 1000

// profileTools is the closed set of built-in tools a profile may name (§7: read-only).
var profileTools = map[string]bool{"Read": true, "Glob": true, "Grep": true}

// CheckToolProfile accepts a non-empty list of distinct names from {Read, Glob, Grep}.
func CheckToolProfile(tools []string) error {
	if len(tools) == 0 || len(tools) > len(profileTools) {
		return errors.New("claudecode: the tool profile must name one to three read-only tools")
	}
	seen := map[string]bool{}
	for _, t := range tools {
		if !profileTools[t] || seen[t] {
			return errors.New("claudecode: the tool profile has an unknown or repeated tool")
		}
		seen[t] = true
	}
	return nil
}

// credentialRoots are the credential locations denied to every read tool (§8.3 item 6).
// Their rule syntax (path glob, ~ expansion) is UNVERIFIED until the live canary decides.
var credentialRoots = []string{"~/.claude/**", "~/.config/anthropic/**"}

var denyTools = []string{"Read", "Glob", "Grep"}

// settingsDoc is the fixed inline --settings JSON (§8.3 item 2): hooks off, the root's
// CLAUDE.md files excluded, the credential deny rules.
type settingsDoc struct {
	DisableAllHooks  bool     `json:"disableAllHooks"`
	ClaudeMdExcludes []string `json:"claudeMdExcludes"`
	Permissions      struct {
		Deny []string `json:"deny"`
	} `json:"permissions"`
}

var claudeMdNames = []string{"CLAUDE.md", "**/CLAUDE.md", "CLAUDE.local.md", "**/CLAUDE.local.md"}

// validSettingsPath accepts an absolute clean path without glob or control characters (it is
// embedded in a glob pattern).
func validSettingsPath(p string) bool {
	return p != "" && p != "/" && path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsAny(p, "*?[]{}\\\x00\n\r\t\"'") && len(p) <= 1024
}

// renderSettings is the one canonical rendering of the inline settings JSON.
func renderSettings(root, configDir string) (string, error) {
	if !validSettingsPath(root) {
		return "", errors.New("claudecode: invalid root for the inline settings")
	}
	if configDir != "" && !validSettingsPath(configDir) {
		return "", errors.New("claudecode: invalid config dir for the inline settings")
	}
	var d settingsDoc
	d.DisableAllHooks = true
	for _, n := range claudeMdNames {
		d.ClaudeMdExcludes = append(d.ClaudeMdExcludes, root+"/"+n)
	}
	roots := append([]string(nil), credentialRoots...)
	if configDir != "" {
		roots = append(roots, configDir+"/**")
	}
	for _, tool := range denyTools {
		for _, r := range roots {
			d.Permissions.Deny = append(d.Permissions.Deny, tool+"("+r+")")
		}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(d); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// checkSettings accepts exactly what renderSettings produces for some root (and optional
// config dir): hooks off, the credential deny rules present for every read tool, nothing else.
func checkSettings(s string) error {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var d settingsDoc
	if err := dec.Decode(&d); err != nil || dec.More() {
		return errors.New("claudecode: --settings is not the pinned JSON document")
	}
	if !d.DisableAllHooks || len(d.ClaudeMdExcludes) != len(claudeMdNames) {
		return errors.New("claudecode: --settings does not disable hooks or exclude CLAUDE.md")
	}
	root := strings.TrimSuffix(d.ClaudeMdExcludes[0], "/"+claudeMdNames[0])
	var configDir string
	if n := len(d.Permissions.Deny); n == len(denyTools)*(len(credentialRoots)+1) {
		// one extra rule per tool: the config dir (test-only)
		last := d.Permissions.Deny[len(credentialRoots)]
		configDir = strings.TrimSuffix(strings.TrimPrefix(last, denyTools[0]+"("), "/**)")
	}
	want, err := renderSettings(root, configDir)
	if err != nil || want != s {
		return errors.New("claudecode: --settings is not the pinned JSON document")
	}
	return nil
}

// BuildArgv returns the arguments after the wrapper path (ADR-021 §1, §8.3 item 2), in the
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
	if err := CheckToolProfile(c.Tools); err != nil {
		return nil, err
	}
	settings, err := renderSettings(c.Root, c.ConfigDir)
	if err != nil {
		return nil, err
	}
	argv := []string{"-p", "--resume", c.UUID, "--output-format", "stream-json", "--verbose",
		"--model", c.Model, "--permission-mode", "default", "--permission-prompts", "none",
		"--setting-sources", "user", "--strict-mcp-config", "--settings", settings,
		"--tools", strings.Join(c.Tools, ","), "--disallowedTools", "mcp__*",
		"--max-turns", strconv.Itoa(c.MaxTurns)}
	if c.MaxBudgetUSD != "" {
		argv = append(argv, "--max-budget-usd", c.MaxBudgetUSD)
	}
	return argv, nil
}

const argvBase = 23 // length without the optional budget pair

// CheckArgv is the allow-pattern the Bridge applies to the argv it is about to
// exec: exactly the BuildArgv shape, nothing else. Any extra argument, a changed
// permission flag, a reordering, a missing hardening flag or a positional (a prompt)
// is refused.
func CheckArgv(argv []string) error {
	if len(argv) != argvBase && len(argv) != argvBase+2 {
		return errors.New("claudecode: argv does not match the pinned shape")
	}
	fixed := map[int]string{0: "-p", 1: "--resume", 3: "--output-format", 4: "stream-json", 5: "--verbose",
		6: "--model", 8: "--permission-mode", 9: "default", 10: "--permission-prompts", 11: "none",
		12: "--setting-sources", 13: "user", 14: "--strict-mcp-config", 15: "--settings",
		17: "--tools", 19: "--disallowedTools", 20: "mcp__*", 21: "--max-turns"}
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
	if err := checkSettings(argv[16]); err != nil {
		return err
	}
	if err := CheckToolProfile(strings.Split(argv[18], ",")); err != nil {
		return errors.New("claudecode: argv tool profile invalid")
	}
	if !turnsPattern.MatchString(argv[22]) {
		return errors.New("claudecode: argv max turns invalid")
	}
	if n, _ := strconv.Atoi(argv[22]); n > maxTurnsLimit {
		return errors.New("claudecode: argv max turns invalid")
	}
	if len(argv) == argvBase+2 && (argv[23] != "--max-budget-usd" || !budgetPattern.MatchString(argv[24])) {
		return errors.New("claudecode: argv budget invalid")
	}
	return nil
}
