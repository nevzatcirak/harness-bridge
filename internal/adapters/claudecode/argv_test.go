package claudecode

import (
	"reflect"
	"strings"
	"testing"
)

const testUUID = "4e59c1bd-4333-4a71-875d-cba9301bd4a9"

var testTools = []string{"Read", "Glob", "Grep"}

// cfg returns a valid ArgvConfig; mod edits it.
func cfg(mod func(*ArgvConfig)) ArgvConfig {
	c := ArgvConfig{UUID: testUUID, Model: "sonnet", MaxTurns: 4, Root: testRoot, Tools: testTools}
	if mod != nil {
		mod(&c)
	}
	return c
}

func TestBuildArgvPinnedFlags(t *testing.T) {
	got, err := BuildArgv(cfg(func(c *ArgvConfig) { c.MaxTurns = 5 }))
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := renderSettings(testRoot, "")
	want := []string{"-p", "--resume", testUUID, "--output-format", "stream-json", "--verbose",
		"--model", "sonnet", "--permission-mode", "default", "--permission-prompts", "none",
		"--setting-sources", "user", "--strict-mcp-config", "--settings", settings,
		"--tools", "Read,Glob,Grep", "--disallowedTools", "mcp__*", "--max-turns", "5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
	if err := CheckArgv(got); err != nil {
		t.Fatalf("own argv fails the allow-pattern: %v", err)
	}
	withBudget, err := BuildArgv(cfg(func(c *ArgvConfig) { c.Model, c.MaxTurns, c.MaxBudgetUSD = "claude-sonnet-5-5", 3, "1.50" }))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(withBudget); withBudget[n-2] != "--max-budget-usd" || withBudget[n-1] != "1.50" {
		t.Fatalf("budget flag missing: %q", withBudget)
	}
	if err := CheckArgv(withBudget); err != nil {
		t.Fatal(err)
	}
}

// The prompt is delivered on stdin: no argv element may carry it, and nothing in
// the builder accepts one.
func TestArgvNeverContainsAPrompt(t *testing.T) {
	argv, _ := BuildArgv(cfg(nil))
	for i, a := range argv {
		if i == 16 { // the inline --settings JSON is the one element with structure
			continue
		}
		if strings.ContainsAny(a, " \n\t") {
			t.Fatalf("argv element looks like free text: %q", a)
		}
	}
	for _, bad := range append(append([]string{}, argv...), "summarize the repository") {
		if bad == "-p" {
			continue
		}
	}
	if err := CheckArgv(append(append([]string{}, argv...), "summarize the repository")); err == nil {
		t.Fatal("a trailing positional (a prompt) passes the allow-pattern")
	}
	if err := CheckArgv(append([]string{"-p", "summarize the repository"}, argv[1:]...)); err == nil {
		t.Fatal("a prompt after -p passes the allow-pattern")
	}
}

func TestBuildArgvRejectsBadConfig(t *testing.T) {
	bad := func(mod func(*ArgvConfig)) ArgvConfig { return cfg(mod) }
	for name, c := range map[string]ArgvConfig{
		"uppercase uuid":  bad(func(c *ArgvConfig) { c.UUID = strings.ToUpper(testUUID) }),
		"flag as uuid":    bad(func(c *ArgvConfig) { c.UUID = "--dangerously-skip-permissions" }),
		"empty model":     bad(func(c *ArgvConfig) { c.Model = "" }),
		"model with sp":   bad(func(c *ArgvConfig) { c.Model = "son net" }),
		"model flag":      bad(func(c *ArgvConfig) { c.Model = "--bare" }),
		"zero turns":      bad(func(c *ArgvConfig) { c.MaxTurns = 0 }),
		"huge turns":      bad(func(c *ArgvConfig) { c.MaxTurns = 100000 }),
		"budget text":     bad(func(c *ArgvConfig) { c.MaxBudgetUSD = "lots" }),
		"budget flag":     bad(func(c *ArgvConfig) { c.MaxBudgetUSD = "--bare" }),
		"budget neg":      bad(func(c *ArgvConfig) { c.MaxBudgetUSD = "-1" }),
		"no root":         bad(func(c *ArgvConfig) { c.Root = "" }),
		"relative root":   bad(func(c *ArgvConfig) { c.Root = "proj" }),
		"glob root":       bad(func(c *ArgvConfig) { c.Root = "/srv/*" }),
		"root with quote": bad(func(c *ArgvConfig) { c.Root = "/srv/a\"b" }),
		"unclean root":    bad(func(c *ArgvConfig) { c.Root = "/srv/../etc" }),
		"no tools":        bad(func(c *ArgvConfig) { c.Tools = nil }),
		"Bash tool":       bad(func(c *ArgvConfig) { c.Tools = []string{"Read", "Bash"} }),
		"Write tool":      bad(func(c *ArgvConfig) { c.Tools = []string{"Write"} }),
		"repeated tool":   bad(func(c *ArgvConfig) { c.Tools = []string{"Read", "Read"} }),
		"glob configdir":  bad(func(c *ArgvConfig) { c.ConfigDir = "/tmp/*" }),
	} {
		if _, err := BuildArgv(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheckArgvRejectsEveryDeviation(t *testing.T) {
	good, _ := BuildArgv(cfg(nil))
	replace := func(old, new string) []string {
		out := append([]string(nil), good...)
		for i, a := range out {
			if a == old {
				out[i] = new
			}
		}
		return out
	}
	cases := map[string][]string{
		"mode auto":            replace("default", "auto"),
		"mode acceptEdits":     replace("default", "acceptEdits"),
		"mode bypass":          replace("default", "bypassPermissions"),
		"mode dontAsk":         replace("default", "dontAsk"),
		"prompts not none":     replace("none", "ask"),
		"skip flag":            append(append([]string(nil), good...), "--dangerously-skip-permissions"),
		"bare":                 append(append([]string(nil), good...), "--bare"),
		"allowedTools":         append(append([]string(nil), good...), "--allowedTools", "Bash"),
		"fork":                 append(append([]string(nil), good...), "--fork-session"),
		"session-id":           append(append([]string(nil), good...), "--session-id", testUUID),
		"mcp-config":           append(append([]string(nil), good...), "--mcp-config", "/x.json"),
		"permission-prompt-tl": append(append([]string(nil), good...), "--permission-prompt-tool", "x"),
		"missing verbose":      append(append([]string(nil), good[:5]...), good[6:]...),
		"reordered":            append([]string{good[1], good[0]}, good[2:]...),
		"empty":                {},
		"uuid upper":           replace(testUUID, strings.ToUpper(testUUID)),
		"turns 0":              replace("4", "0"),
		"duplicate mode":       append(append([]string(nil), good...), "--permission-mode", "auto"),
		"mode glued":           replace("--permission-mode", "--permission-mode=auto"),
		"model with space":     replace("sonnet", "son net"),
		"model is a flag":      replace("sonnet", "--bare"),
		"model empty":          replace("sonnet", ""),
		"model too long":       replace("sonnet", strings.Repeat("m", 65)),
		"resume replaced":      replace("--resume", "--session-id"),
		"resume replaced fork": replace("--resume", "--fork-session"),
		"output-format text":   replace("--output-format", "--input-format"),
	}
	for name, argv := range cases {
		if err := CheckArgv(argv); err == nil {
			t.Errorf("%s: passes the allow-pattern: %q", name, argv)
		}
	}
}

// §8.3 item 2: the hardening flags are part of the pinned shape and none can be dropped.
func TestCheckArgvRequiresEveryHardeningFlag(t *testing.T) {
	good, _ := BuildArgv(cfg(nil))
	drop := func(flag string, n int) []string {
		var out []string
		for i := 0; i < len(good); i++ {
			if good[i] == flag {
				i += n - 1
				continue
			}
			out = append(out, good[i])
		}
		return out
	}
	for name, argv := range map[string][]string{
		"no setting-sources":     drop("--setting-sources", 2),
		"no strict-mcp-config":   drop("--strict-mcp-config", 1),
		"no settings":            drop("--settings", 2),
		"no tools":               drop("--tools", 2),
		"no disallowedTools":     drop("--disallowedTools", 2),
		"project setting source": func() []string { o := append([]string(nil), good...); o[13] = "user,project"; return o }(),
		"tools widened":          func() []string { o := append([]string(nil), good...); o[18] = "Read,Glob,Grep,Bash"; return o }(),
		"mcp deny weakened":      func() []string { o := append([]string(nil), good...); o[20] = "mcp__x"; return o }(),
	} {
		if err := CheckArgv(argv); err == nil {
			t.Errorf("%s: passes the allow-pattern", name)
		}
	}
}

// §8.3 item 2 and 6: the inline settings turn hooks off, exclude the root's CLAUDE.md files
// and deny Read, Glob and Grep on the credential locations. Dropping any one of those rules
// fails the allow-pattern (mutation: drop the credential deny rule).
func TestInlineSettingsCarryTheCredentialDenyRules(t *testing.T) {
	s, err := renderSettings(testRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"disableAllHooks":true`, testRoot + "/CLAUDE.md", testRoot + "/**/CLAUDE.md"} {
		if !strings.Contains(s, want) {
			t.Errorf("settings lack %q: %s", want, s)
		}
	}
	for _, tool := range []string{"Read", "Glob", "Grep"} {
		for _, loc := range []string{"~/.claude/**", "~/.config/anthropic/**"} {
			rule := tool + "(" + loc + ")"
			if !strings.Contains(s, rule) {
				t.Errorf("no deny rule %s: %s", rule, s)
			}
			argv, _ := BuildArgv(cfg(nil))
			argv[16] = strings.Replace(s, `"`+rule+`",`, "", 1)
			if argv[16] == s {
				argv[16] = strings.Replace(s, `,"`+rule+`"`, "", 1)
			}
			if err := CheckArgv(argv); err == nil {
				t.Errorf("an argv without %s passes the allow-pattern", rule)
			}
		}
	}
	// the test-only CLAUDE_CONFIG_DIR rule is optional and also exact
	withDir, err := BuildArgv(cfg(func(c *ArgvConfig) { c.ConfigDir = "/tmp/throwaway/cfg" }))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckArgv(withDir); err != nil {
		t.Fatalf("the config-dir form fails its own check: %v", err)
	}
	if !strings.Contains(withDir[16], "Grep(/tmp/throwaway/cfg/**)") {
		t.Fatalf("no config-dir rule: %s", withDir[16])
	}
	for name, mut := range map[string]func(string) string{
		"hooks on": func(x string) string {
			return strings.Replace(x, `"disableAllHooks":true`, `"disableAllHooks":false`, 1)
		},
		"allow rule":      func(x string) string { return strings.Replace(x, `"deny":[`, `"allow":["Bash"],"deny":[`, 1) },
		"foreign exclude": func(x string) string { return strings.Replace(x, testRoot+"/CLAUDE.md", "/other/CLAUDE.md", 1) },
		"extra key": func(x string) string {
			return strings.Replace(x, `{"disableAllHooks"`, `{"env":{"A":"b"},"disableAllHooks"`, 1)
		},
		"trailing data": func(x string) string { return x + " {}" },
	} {
		argv, _ := BuildArgv(cfg(nil))
		argv[16] = mut(argv[16])
		if err := CheckArgv(argv); err == nil {
			t.Errorf("%s: passes the allow-pattern", name)
		}
	}
}
