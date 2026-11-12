package claudecode

import (
	"sort"
	"strings"
	"testing"
)

func poisoned() []string {
	return []string{
		"PATH=/home/attacker/bin:/usr/bin", "HOME=/root", "LANG=C", "USER=root",
		"ANTHROPIC_API_KEY=sk-ant-api03-SECRET", "ANTHROPIC_AUTH_TOKEN=tok", "ANTHROPIC_BASE_URL=https://evil.example",
		"ANTHROPIC_PROFILE=p", "ANTHROPIC_FEDERATION_RULE_ID=r", "ANTHROPIC_ORGANIZATION_ID=o",
		"CLAUDE_CONFIG_DIR=/tmp/x", "CLAUDE_CODE_OAUTH_TOKEN=oauth", "CLAUDE_CODE_USE_BEDROCK=1",
		"CLAUDE_CODE_USE_VERTEX=1", "CLAUDE_CODE_USE_FOUNDRY=1",
		"BRIDGE_TOKEN=bridge-secret", "BRIDGE_CLAUDE_COMMAND=/x", "OPENCODE_SERVER_PASSWORD=pw",
		"GITHUB_TOKEN=ghp_x", "AWS_SECRET_ACCESS_KEY=aws", "SOME_KEY=k", "DB_SECRET=s",
		"HTTPS_PROXY=http://proxy", "http_proxy=http://proxy", "NO_PROXY=x", "GOOGLE_APPLICATION_CREDENTIALS=/g.json",
	}
}

func TestScrubEnvIsAnExplicitAllowlist(t *testing.T) {
	env, err := ScrubEnv(poisoned(), EnvConfig{Home: "/home/hbclaude", Lang: "C.UTF-8"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "DISABLE_UPDATES=1",
		"HOME=/home/hbclaude", "LANG=C.UTF-8", "PATH=/usr/local/bin:/usr/bin:/bin", "TERM=dumb",
	}
	got := append([]string(nil), env...)
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("env =\n%v\nwant\n%v", got, want)
	}
	for _, e := range env {
		for _, banned := range []string{"ANTHROPIC", "SECRET", "TOKEN", "KEY", "BRIDGE", "OPENCODE", "PROXY", "proxy", "CLAUDE_CONFIG_DIR", "OAUTH", "BEDROCK", "VERTEX", "FOUNDRY", "attacker", "AWS", "GOOGLE"} {
			if strings.Contains(e, banned) {
				t.Fatalf("poisoned value leaked into the child environment: %q", e)
			}
		}
	}
}

func TestScrubEnvNeverReadsTheParentForAllowedNames(t *testing.T) {
	// HOME, LANG and PATH come from the configuration (the Claude user's), not
	// from the Bridge's own environment.
	env, err := ScrubEnv([]string{"HOME=/root", "LANG=de_DE", "PATH=/evil"}, EnvConfig{Home: "/home/hbclaude", Lang: "C.UTF-8", Path: "/usr/bin:/bin"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"HOME=/home/hbclaude", "LANG=C.UTF-8", "PATH=/usr/bin:/bin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %v", want, env)
		}
	}
	for _, bad := range []string{"/root", "de_DE", "/evil"} {
		if strings.Contains(joined, bad) {
			t.Errorf("parent value %q leaked", bad)
		}
	}
}

func TestScrubEnvValidatesItsConfig(t *testing.T) {
	for name, c := range map[string]EnvConfig{
		"no home":        {Lang: "C"},
		"relative home":  {Home: "home/x", Lang: "C"},
		"home with =":    {Home: "/home/a=b", Lang: "C"},
		"home with nul":  {Home: "/home/a\x00b", Lang: "C"},
		"home unclean":   {Home: "/home/../root", Lang: "C"},
		"lang injection": {Home: "/home/x", Lang: "C\nANTHROPIC_API_KEY=x"},
		"path relative":  {Home: "/home/x", Lang: "C", Path: "bin:/usr/bin"},
		"path empty el":  {Home: "/home/x", Lang: "C", Path: "/usr/bin::/bin"},
		"path newline":   {Home: "/home/x", Lang: "C", Path: "/usr/bin\n/bin"},
	} {
		if _, err := ScrubEnv(nil, c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
