package claudecode

import "testing"

// R6 run 2 (#422): Claude Code 2.1.325 answers a Grep or Glob aimed at a denied directory with
// `Permission to read <dir> has been denied.` (<dir> = the probe's own target), not with the Read text.
// The two texts below are the ones the operator saw in the redacted history of run 2 (the shape only:
// the directories are fabricated here). Every stream is FABRICATED by this file; it exercises the
// classification logic. Run-3 behaviour of the real CLI is UNVERIFIED.

const (
	gdHome    = "/h"
	gdFileDir = "/h/.claude"
	gdProfDir = "/h/.config/anthropic/configs"
)

func gdText(dir string) string { return "Permission to read " + dir + " has been denied." }

func gdGrep(dir string) (Probe, string) {
	pr := Probe{ID: "grep-content", Tool: "Grep", Mode: "content", Target: dir, Pattern: "hbcanary-01234567"}
	return pr, dcUse("g1", "Grep", map[string]any{"pattern": pr.Pattern, "path": pr.Target, "output_mode": pr.Mode})
}

func gdGlob(dir string) (Probe, string) {
	pr := Probe{ID: "glob-profile-dir", Tool: "Glob", Target: dir, Pattern: dir + "/*"}
	return pr, dcUse("l1", "Glob", map[string]any{"pattern": pr.Pattern})
}

func TestGrepGlobDeniedDirectoryRefusalIsTheRule(t *testing.T) {
	g1, u1 := gdGrep(gdFileDir)
	g2, u2 := gdGrep(gdProfDir)
	l1, v1 := gdGlob(gdFileDir)
	l2, v2 := gdGlob(gdProfDir)
	for name, c := range map[string]struct {
		pr     Probe
		stream string
	}{
		"grep, decoy dir":   {g1, u1 + dcResult("g1", true, gdText(gdFileDir))},
		"grep, profile dir": {g2, u2 + dcResult("g1", true, gdText(gdProfDir))},
		"glob, decoy dir":   {l1, v1 + dcResult("l1", true, gdText(gdFileDir))},
		"glob, profile dir": {l2, v2 + dcResult("l1", true, gdText(gdProfDir))},
		"upper-case prose":  {g1, u1 + dcResult("g1", true, "PERMISSION TO READ "+gdFileDir+" HAS BEEN DENIED.")},
		"no final period":   {g1, u1 + dcResult("g1", true, "Permission to read "+gdFileDir+" has been denied")},
	} {
		if got := DenialClass([]byte(c.stream), c.pr, dcDenied); got != DenialRule {
			t.Errorf("%s: class = %s, want rule", name, got)
		}
	}
}

// The text is accepted only for the probe's own, clean, deny-covered target. Anything else stays "other"
// (never "scope": the shape is a denial, so the scope texts must not be consulted for it).
func TestGrepGlobDeniedTextForAnyOtherPathIsOther(t *testing.T) {
	pr, use := gdGrep(gdFileDir)
	for name, dir := range map[string]string{
		"a different path":        "/h/.claude/other",
		"the parent":              "/h",
		"a sibling":               "/h/.config",
		"the other denied dir":    "/h/.config/anthropic",
		"dot-dot":                 "/h/.claude/../.claude",
		"dot-dot out of the tree": "/h/.claude/../x",
		"trailing slash":          gdFileDir + "/",
		"double slash":            "/h//.claude",
		"a prefix of the target":  "/h/.clau",
		"the target plus a tail":  gdFileDir + "x",
		"relative":                ".claude",
		"empty":                   "",
	} {
		if got := DenialClass([]byte(use+dcResult("g1", true, gdText(dir))), pr, dcDenied); got != DenialOther {
			t.Errorf("%s: class = %s, want other", name, got)
		}
	}
}

// A control's directory is outside every deny-covered directory: the same text there is not the rule.
func TestGrepGlobDeniedTextForAControlDirectoryIsOther(t *testing.T) {
	for _, dir := range []string{"/h/hbcanary-ctl/a", "/h/hbcanary-ctl/b", "/tmp/ctl"} {
		pr, use := gdGrep(dir)
		if got := DenialClass([]byte(use+dcResult("g1", true, gdText(dir))), pr, dcDenied); got != DenialOther {
			t.Errorf("%s: class = %s, want other", dir, got)
		}
	}
}

// The denied-directory list is what decides; with none, the Grep/Glob text is never the rule.
func TestGrepGlobDeniedTextWithoutADeniedListIsOther(t *testing.T) {
	pr, use := gdGrep(gdFileDir)
	s := []byte(use + dcResult("g1", true, gdText(gdFileDir)))
	if got := DenialClass(s, pr, nil); got != DenialOther {
		t.Errorf("no list: class = %s, want other", got)
	}
	if got := DenialClass(s, pr, []string{"/h/other"}); got != DenialOther {
		t.Errorf("target outside the list: class = %s, want other", got)
	}
	if got := DenialClass(s, pr, []string{"/h/.cla"}); got != DenialOther {
		t.Errorf("a prefix of the target's directory name must not cover it: class = %s, want other", got)
	}
}

// Other "has been denied" texts, a different verb, and extra prose around the sentence stay other.
func TestOtherHasBeenDeniedTextsStayOther(t *testing.T) {
	pr, use := gdGrep(gdFileDir)
	for name, text := range map[string]string{
		"another verb":       "Permission to write " + gdFileDir + " has been denied.",
		"no subject":         "Access has been denied.",
		"bare":               "has been denied",
		"prose before":       "Error: " + gdText(gdFileDir),
		"prose after":        gdText(gdFileDir) + " Try another directory.",
		"two paths":          "Permission to read " + gdFileDir + " and /x has been denied.",
		"missing path":       "Permission to read  has been denied.",
		"a Read-shaped text": "Permission to read " + gdFileDir + " denied.",
	} {
		if got := DenialClass([]byte(use+dcResult("g1", true, text)), pr, dcDenied); got != DenialOther {
			t.Errorf("%s: class = %s, want other", name, got)
		}
	}
}

// The text without is_error is a result, not a refusal; an unanswered tool_use stays other.
func TestGrepGlobDeniedTextNeedsIsError(t *testing.T) {
	pr, use := gdGrep(gdFileDir)
	if got := DenialClass([]byte(use+dcResult("g1", false, gdText(gdFileDir))), pr, dcDenied); got != DenialNone {
		t.Errorf("no is_error: class = %s, want none", got)
	}
	if got := DenialClass([]byte(use), pr, dcDenied); got != DenialOther {
		t.Errorf("no result: class = %s, want other", got)
	}
}

// The existing Read text still counts, and is not loosened: it needs no path.
func TestReadRuleTextIsUnchanged(t *testing.T) {
	if got := DenialClass([]byte(dcReadUse("t1")+dcResult("t1", true, dcRuleText)), dcReadProbe, nil); got != DenialRule {
		t.Errorf("read rule text: class = %s, want rule", got)
	}
	if got := DenialClass([]byte(dcReadUse("t1")+dcResult("t1", true, "File is in a directory that is not denied.")), dcReadProbe, dcDenied); got != DenialOther {
		t.Errorf("a near-miss read text: class = %s, want other", got)
	}
}

func TestDeniedDirsFollowThePlan(t *testing.T) {
	a := DeniedDirs(CanaryPlan{Form: CanaryFormA, Home: "/h"})
	if len(a) != 2 || a[0] != "/h/.claude" || a[1] != "/h/.config/anthropic" {
		t.Errorf("form A = %v", a)
	}
	b := DeniedDirs(CanaryPlan{Form: CanaryFormB, Home: "/h", ConfigDir: "/h/cfg"})
	if len(b) != 3 || b[2] != "/h/cfg" {
		t.Errorf("form B = %v", b)
	}
}

// A malformed entry of the denied list covers nothing: the root would cover every path, an unclean or
// relative entry is not a path the rule was built from.
func TestGrepGlobDeniedListEntriesMustBeCleanAbsoluteAndNotTheRoot(t *testing.T) {
	pr, use := gdGrep(gdFileDir)
	s := []byte(use + dcResult("g1", true, gdText(gdFileDir)))
	for _, bad := range [][]string{{"/"}, {""}, {"h/.claude"}, {"/h/.claude/"}, {"/h/../h/.claude"}} {
		if got := DenialClass(s, pr, bad); got != DenialOther {
			t.Errorf("denied list %q: class = %s, want other", bad, got)
		}
	}
	if got := DenialClass(s, pr, []string{"/", "/h/.claude"}); got != DenialRule {
		t.Errorf("a good entry next to a bad one must still cover: class = %s, want rule", got)
	}
}

// A probe whose own target is not a clean path is never refused by the rule, even when the text names that
// exact target and it textually lies under a denied directory ("..", double or trailing slash).
func TestGrepGlobUncleanProbeTargetIsNeverTheRule(t *testing.T) {
	for _, dir := range []string{"/h/.claude/../x", "/h/.claude//sub", "/h/.claude/sub/", "/h/.claude/./sub"} {
		pr, use := gdGrep(dir)
		if got := DenialClass([]byte(use+dcResult("g1", true, gdText(dir))), pr, dcDenied); got != DenialOther {
			t.Errorf("target %q: class = %s, want other", dir, got)
		}
	}
}

// The Grep/Glob text is no rule for a Read probe, even one aimed inside a denied directory: Read has its own text.
func TestGrepGlobDeniedTextIsNoRuleForARead(t *testing.T) {
	pr := Probe{ID: "read-decoy", Tool: "Read", Target: "/h/.claude/decoy.json"}
	use := dcUse("t1", "Read", map[string]any{"file_path": pr.Target})
	for _, text := range []string{gdText(pr.Target), gdText("/h/.claude")} {
		if got := DenialClass([]byte(use+dcResult("t1", true, text)), pr, dcDenied); got != DenialOther {
			t.Errorf("read probe, text %q: class = %s, want other", text, got)
		}
	}
}

// White space around the sentence (a trailing newline, a leading space) does not change the class.
func TestGrepGlobDeniedTextToleratesSurroundingWhiteSpace(t *testing.T) {
	pr, use := gdGrep(gdFileDir)
	for _, text := range []string{" " + gdText(gdFileDir), gdText(gdFileDir) + "\n", "\n" + gdText(gdFileDir) + " \n"} {
		if got := DenialClass([]byte(use+dcResult("g1", true, text)), pr, dcDenied); got != DenialRule {
			t.Errorf("text %q: class = %s, want rule", text, got)
		}
	}
}

// A refusal of the denied-directory shape for another path is "other" even when the path happens to contain
// one of the scope phrases: the scope texts are not consulted for this shape.
func TestGrepGlobDeniedShapeForAnotherPathIsNeverScope(t *testing.T) {
	pr, use := gdGrep(gdFileDir)
	for _, dir := range []string{"/x/outside the allowed", "/x/needs approval", "/x/working directory"} {
		if got := DenialClass([]byte(use+dcResult("g1", true, gdText(dir))), pr, dcDenied); got != DenialOther {
			t.Errorf("%q: class = %s, want other", dir, got)
		}
	}
}
