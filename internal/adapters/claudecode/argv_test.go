package claudecode

import (
	"reflect"
	"strings"
	"testing"
)

const testUUID = "4e59c1bd-4333-4a71-875d-cba9301bd4a9"

func TestBuildArgvPinnedFlags(t *testing.T) {
	got, err := BuildArgv(ArgvConfig{UUID: testUUID, Model: "sonnet", MaxTurns: 5})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "--resume", testUUID, "--output-format", "stream-json", "--verbose",
		"--model", "sonnet", "--permission-mode", "default", "--permission-prompts", "none", "--max-turns", "5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
	if err := CheckArgv(got); err != nil {
		t.Fatalf("own argv fails the allow-pattern: %v", err)
	}
	withBudget, err := BuildArgv(ArgvConfig{UUID: testUUID, Model: "claude-sonnet-5-5", MaxTurns: 3, MaxBudgetUSD: "1.50"})
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
	argv, _ := BuildArgv(ArgvConfig{UUID: testUUID, Model: "sonnet", MaxTurns: 1})
	for _, a := range argv {
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
	for name, c := range map[string]ArgvConfig{
		"uppercase uuid": {UUID: strings.ToUpper(testUUID), Model: "sonnet", MaxTurns: 1},
		"flag as uuid":   {UUID: "--dangerously-skip-permissions", Model: "sonnet", MaxTurns: 1},
		"empty model":    {UUID: testUUID, MaxTurns: 1},
		"model with sp":  {UUID: testUUID, Model: "son net", MaxTurns: 1},
		"model flag":     {UUID: testUUID, Model: "--bare", MaxTurns: 1},
		"zero turns":     {UUID: testUUID, Model: "sonnet", MaxTurns: 0},
		"huge turns":     {UUID: testUUID, Model: "sonnet", MaxTurns: 100000},
		"budget text":    {UUID: testUUID, Model: "sonnet", MaxTurns: 1, MaxBudgetUSD: "lots"},
		"budget flag":    {UUID: testUUID, Model: "sonnet", MaxTurns: 1, MaxBudgetUSD: "--bare"},
		"budget neg":     {UUID: testUUID, Model: "sonnet", MaxTurns: 1, MaxBudgetUSD: "-1"},
	} {
		if _, err := BuildArgv(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheckArgvRejectsEveryDeviation(t *testing.T) {
	good, _ := BuildArgv(ArgvConfig{UUID: testUUID, Model: "sonnet", MaxTurns: 4})
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
