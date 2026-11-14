package claudecode

import (
	"strings"
	"testing"
)

// ADR-021 §8.5, D14 Q7: BRIDGE_CLAUDE_VERSIONS is an exact-version allowlist, nothing else.
func TestParseVersionsAcceptsOnlyExactVersions(t *testing.T) {
	got, err := ParseVersions("2.1.322,2.1.323")
	if err != nil || len(got) != 2 || got[0] != "2.1.322" || got[1] != "2.1.323" {
		t.Fatalf("got %v, %v", got, err)
	}
	for name, in := range map[string]string{
		"empty":           "",
		"blank":           "   ",
		"wildcard":        "2.1.*",
		"caret range":     "^2.1.322",
		"tilde range":     "~2.1.322",
		"gte range":       ">=2.1.322",
		"hyphen range":    "2.1.300-2.1.322",
		"prefix":          "2.1",
		"major only":      "2",
		"v prefix":        "v2.1.322",
		"prerelease":      "2.1.322-beta",
		"build":           "2.1.322+x",
		"four parts":      "2.1.322.1",
		"leading space":   " 2.1.322",
		"inner space":     "2.1.322, 2.1.323",
		"empty entry":     "2.1.322,",
		"double comma":    "2.1.322,,2.1.323",
		"duplicate":       "2.1.322,2.1.322",
		"letters":         "2.1.x",
		"newline":         "2.1.322\n",
		"latest":          "latest",
		"too many digits": "2.1.32222",
	} {
		if _, err := ParseVersions(in); err == nil {
			t.Errorf("%s (%q): accepted", name, in)
		}
	}
	if _, err := ParseVersions(""); err != ErrNoVersions {
		t.Errorf("an empty value must be ErrNoVersions (fail closed, no default), got %v", err)
	}
	var many []string
	for i := 0; i <= MaxVersions; i++ {
		many = append(many, "2.1."+itoa(i))
	}
	if _, err := ParseVersions(strings.Join(many[:MaxVersions], ",")); err != nil {
		t.Errorf("exactly %d versions must load: %v", MaxVersions, err)
	}
	if _, err := ParseVersions(strings.Join(many, ",")); err == nil {
		t.Errorf("%d versions must fail", MaxVersions+1)
	}
}

func TestCheckVersionsAndToolProfile(t *testing.T) {
	if err := CheckVersions(nil); err != ErrNoVersions {
		t.Errorf("nil list: %v", err)
	}
	if err := CheckVersions([]string{"2.1.322"}); err != nil {
		t.Error(err)
	}
	for name, l := range map[string][]string{
		"one element holding two versions": {"2.1.322,2.1.323"},
		"comma element among others":       {"2.1.322", "2.1.323,2.1.324"},
		"empty element":                    {"2.1.322", ""},
		"padded element":                   {" 2.1.322"},
		"duplicate":                        {"2.1.322", "2.1.322"},
	} {
		if err := CheckVersions(l); err == nil {
			t.Errorf("%s: CheckVersions accepted %q", name, l)
		}
	}
	var tooMany []string
	for i := 0; i <= MaxVersions; i++ {
		tooMany = append(tooMany, "2.1."+itoa(i))
	}
	if err := CheckVersions(tooMany); err == nil {
		t.Error("more than MaxVersions elements accepted")
	}
	if err := CheckVersions([]string{"2.1.322", "2.1.*"}); err == nil {
		t.Error("a wildcard in a Config list passes")
	}
	if err := CheckToolProfile([]string{"Read", "Glob", "Grep"}); err != nil {
		t.Error(err)
	}
	for _, bad := range [][]string{nil, {}, {"Bash"}, {"Read", "Edit"}, {"Read", "Read"}, {"Read", "Glob", "Grep", "Write"}, {""}} {
		if err := CheckToolProfile(bad); err == nil {
			t.Errorf("profile %v accepted", bad)
		}
	}
}

// The machine side: an init version outside the list is a row-0 violation, by equality only.
func TestInitVersionIsComparedByEqualityOnly(t *testing.T) {
	for _, v := range []string{"2.1.323", "2.1", "2.1.322-beta", "2.1.3220", " 2.1.322", "2.1.322 ", "2.1.*", ""} {
		got := Decide(testCfg(), []Step{line(initJSON(map[string]any{"claude_code_version": v}))})
		if !got.Decided || got.Row != "0" {
			t.Errorf("version %q: %+v", v, got)
		}
	}
}
