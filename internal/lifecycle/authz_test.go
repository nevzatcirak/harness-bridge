package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/security"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repoAt makes dir look like the top level of a plain repository: a .git
// directory with a HEAD and a harmless config. No git is run.
func repoAt(t *testing.T, dir string) {
	t.Helper()
	write(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(dir, ".git", "config"), "[core]\n\trepositoryformatversion = 0\n\tbare = false\n")
}

// worktreeAt makes wt a linked worktree of the repository at main.
func worktreeAt(t *testing.T, main, wt, name string) string {
	t.Helper()
	gd := filepath.Join(main, ".git", "worktrees", name)
	write(t, filepath.Join(gd, "HEAD"), "ref: refs/heads/"+name+"\n")
	write(t, filepath.Join(gd, "commondir"), "../..\n")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
	return gd
}

func cfgFor(base string, modes ...string) *Config {
	return &Config{Roots: []Root{{Path: base, Modes: modes, MaxSessions: DefaultMaxSessions}}, CreatePerHour: 10, ArchivePerHour: 10, MaxTotalSessions: 50}
}

func newAuthorizer(t *testing.T, cfg *Config) *Authorizer {
	t.Helper()
	a, err := NewAuthorizer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// wantRefusal asserts an *AuthError with the code, reason and cause.
func wantRefusal(t *testing.T, err error, code, reason, cause string) {
	t.Helper()
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("want refusal %s/%s/%s, got %v", code, reason, cause, err)
	}
	if ae.Code != code || ae.Reason != reason || ae.Cause != cause {
		t.Fatalf("want %s/%s/%s, got %s/%s/%s", code, reason, cause, ae.Code, ae.Reason, ae.Cause)
	}
}

func authz(a *Authorizer, dir, mode string) (*Grant, error) {
	return a.Authorize(context.Background(), dir, mode)
}

func TestPlainGitDirectoryIsTheTopLevel(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	proj := mkdir(t, root, "proj")
	repoAt(t, proj)
	sub := mkdir(t, proj, "pkg", "deep")
	a := newAuthorizer(t, cfgFor(root, ModeCreate, ModeArchive))
	for _, dir := range []string{proj, sub} {
		g, err := authz(a, dir, ModeCreate)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if g.TopLevel != proj || g.GitDir != filepath.Join(proj, ".git") || g.CommonDir != g.GitDir || g.Root != root || g.Directory != dir {
			t.Fatalf("grant: %+v", g)
		}
	}
}

func TestLinkedWorktreeInsideTheRoot(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	main := mkdir(t, root, "main")
	repoAt(t, main)
	wt := mkdir(t, root, "wt")
	gd := worktreeAt(t, main, wt, "wt")
	a := newAuthorizer(t, cfgFor(root, ModeArchive))
	g, err := authz(a, mkdir(t, wt, "src"), ModeArchive)
	if err != nil {
		t.Fatal(err)
	}
	if g.TopLevel != wt || g.GitDir != gd || g.CommonDir != filepath.Join(main, ".git") {
		t.Fatalf("grant: %+v", g)
	}
}

func TestGitdirPointerLeavingTheRootIsNotInRoot(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	outside := mkdir(t, base, "outside")
	repoAt(t, outside)
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	for name, pointer := range map[string]string{
		"absolute outside":   filepath.Join(outside, ".git"),
		"relative escape":    "../../outside/.git",
		"root itself parent": base,
		"dot dot":            filepath.Join(root, "..", "outside", ".git"),
	} {
		t.Run(name, func(t *testing.T) {
			wt := mkdir(t, root, "wt-"+strings.ReplaceAll(name, " ", "-"))
			write(t, filepath.Join(wt, ".git"), "gitdir: "+pointer+"\n")
			_, err := authz(a, wt, ModeCreate)
			wantRefusal(t, err, CodeForbidden, ReasonNotInRoot, causePointer)
		})
	}
}

func TestPointerThroughASymlinkOrMissingTargetCannotBeOpened(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	main := mkdir(t, root, "main")
	repoAt(t, main)
	if err := os.Symlink(filepath.Join(main, ".git"), filepath.Join(root, "gitlink")); err != nil {
		t.Fatal(err)
	}
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	for name, pointer := range map[string]string{
		"symlink inside the root": filepath.Join(root, "gitlink"),
		"missing target":          filepath.Join(root, "nope", ".git"),
	} {
		wt := mkdir(t, root, "w-"+strings.ReplaceAll(name, " ", "-"))
		write(t, filepath.Join(wt, ".git"), "gitdir: "+pointer+"\n")
		_, err := authz(a, wt, ModeCreate)
		wantRefusal(t, err, CodeForbidden, ReasonNotInRoot, causePointer)
	}
}

func TestCommondirLeavingTheRootIsNotInRoot(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	outside := mkdir(t, base, "outside")
	repoAt(t, outside)
	wt := mkdir(t, root, "wt")
	gd := filepath.Join(root, "gits", "wt")
	write(t, filepath.Join(gd, "HEAD"), "ref: refs/heads/x\n")
	write(t, filepath.Join(gd, "commondir"), filepath.Join(outside, ".git")+"\n")
	write(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	_, err := authz(a, wt, ModeCreate)
	wantRefusal(t, err, CodeForbidden, ReasonNotInRoot, causePointer)
	// A relative commondir that climbs out of the root is the same refusal.
	write(t, filepath.Join(gd, "commondir"), "../../../../outside/.git\n")
	_, err = authz(a, wt, ModeCreate)
	wantRefusal(t, err, CodeForbidden, ReasonNotInRoot, causePointer)
}

func TestDotGitSymlinkIsRefusedAndNeverAscendedPast(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	repoAt(t, root) // a valid top level ABOVE the project: must not be reached
	real := mkdir(t, base, "realgit")
	write(t, filepath.Join(real, "HEAD"), "ref: refs/heads/main\n")
	proj := mkdir(t, root, "proj")
	if err := os.Symlink(real, filepath.Join(proj, ".git")); err != nil {
		t.Fatal(err)
	}
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	_, err := authz(a, proj, ModeCreate)
	wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeDotGit)
	// The same for a dangling symlink and a .git that is neither file nor dir.
	proj2 := mkdir(t, root, "proj2")
	if err := os.Symlink(filepath.Join(base, "missing"), filepath.Join(proj2, ".git")); err != nil {
		t.Fatal(err)
	}
	_, err = authz(a, proj2, ModeCreate)
	wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeDotGit)
	proj3 := mkdir(t, root, "proj3")
	if err := syscall.Mkfifo(filepath.Join(proj3, ".git"), 0o600); err != nil {
		t.Skip("mkfifo unavailable:", err)
	}
	_, err = authz(a, proj3, ModeCreate)
	wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeDotGit)
}

func TestMalformedGitdirFiles(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	main := mkdir(t, root, "main")
	repoAt(t, main)
	good := filepath.Join(main, ".git")
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	for name, content := range map[string]string{
		"no prefix":         good + "\n",
		"no space":          "gitdir:" + good + "\n",
		"empty":             "",
		"only prefix":       "gitdir: \n",
		"two lines":         "gitdir: " + good + "\nextra\n",
		"carriage return":   "gitdir: " + good + "\r\n",
		"nul":               "gitdir: " + good + "\x00x\n",
		"too long":          "gitdir: " + strings.Repeat("a", 5000),
		"leading blank":     "\ngitdir: " + good + "\n",
		"second pointer":    "gitdir: " + good + "\ngitdir: " + good + "\n",
		"prefix upper case": "GITDIR: " + good + "\n",
	} {
		wt := mkdir(t, root, "w-"+strings.ReplaceAll(name, " ", "-"))
		write(t, filepath.Join(wt, ".git"), content)
		_, err := authz(a, wt, ModeCreate)
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Code != CodeForbidden || ae.Reason != ReasonNonGit {
			t.Errorf("%s: %v, want FORBIDDEN non_git", name, err)
		}
	}
}

func TestUnsupportedLayouts(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	cases := map[string]func(dir string){
		"reftable directory": func(d string) { repoAt(t, d); mkdir(t, d, ".git", "reftable") },
		"no HEAD":            func(d string) { write(t, filepath.Join(d, ".git", "config"), "[core]\n") },
		"HEAD is a directory": func(d string) {
			write(t, filepath.Join(d, ".git", "config"), "[core]\n")
			mkdir(t, d, ".git", "HEAD")
		},
		"core.bare true": func(d string) { repoAt(t, d); write(t, filepath.Join(d, ".git", "config"), "[core]\n\tbare = true\n") },
		"refstorage": func(d string) {
			repoAt(t, d)
			write(t, filepath.Join(d, ".git", "config"), "[extensions]\n\trefStorage = reftable\n")
		},
		".git is an empty file": func(d string) { write(t, filepath.Join(d, ".git"), "") },
	}
	for name, setup := range cases {
		dir := mkdir(t, root, "p-"+strings.ReplaceAll(name, " ", "-"))
		setup(dir)
		_, err := authz(a, dir, ModeCreate)
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Code != CodeForbidden || ae.Reason != ReasonNonGit {
			t.Errorf("%s: %v, want FORBIDDEN non_git", name, err)
		}
	}
	// A bare repository has no .git of its own: its files at the project level
	// are not a top level, and the ascent ends at the root.
	bare := mkdir(t, root, "bare.git")
	write(t, filepath.Join(bare, "HEAD"), "ref: refs/heads/main\n")
	mkdir(t, bare, "objects")
	mkdir(t, bare, "refs")
	_, err := authz(a, bare, ModeCreate)
	wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeNoTopLevel)
}

func TestNearestGitWins(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	outer := mkdir(t, root, "outer")
	repoAt(t, outer)
	inner := mkdir(t, outer, "vendor", "inner")
	repoAt(t, inner)
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	g, err := authz(a, mkdir(t, inner, "src"), ModeCreate)
	if err != nil || g.TopLevel != inner {
		t.Fatalf("inner repository must win: %+v %v", g, err)
	}
	g, err = authz(a, mkdir(t, outer, "other"), ModeCreate)
	if err != nil || g.TopLevel != outer {
		t.Fatalf("outer: %+v %v", g, err)
	}
}

func TestNonGitDirectoryAndTopLevelAboveTheRoot(t *testing.T) {
	base := canonicalTemp(t)
	repoAt(t, base) // the repository top level lies ABOVE the lifecycle root
	root := mkdir(t, base, "lc")
	plain := mkdir(t, root, "plain", "deep")
	a := newAuthorizer(t, cfgFor(root, ModeCreate, ModeArchive))
	for _, dir := range []string{plain, root} {
		_, err := authz(a, dir, ModeCreate)
		wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeNoTopLevel)
	}
	// The root may itself be a worktree: then a directory below it is fine.
	repoAt(t, root)
	if _, err := authz(a, plain, ModeCreate); err != nil {
		t.Fatalf("root is a worktree: %v", err)
	}
}

func TestAscentIsBounded(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	repoAt(t, root) // a top level exists, but more than 128 levels up
	parts := []string{root}
	for i := 0; i < ascentLimit+2; i++ {
		parts = append(parts, "d")
	}
	deep := mkdir(t, parts...)
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	_, err := authz(a, deep, ModeCreate)
	wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeAscentBound)
}

func TestModesPerRoot(t *testing.T) {
	base := canonicalTemp(t)
	rc, ra, rb := mkdir(t, base, "create-only"), mkdir(t, base, "archive-only"), mkdir(t, base, "both")
	for _, r := range []string{rc, ra, rb} {
		repoAt(t, mkdir(t, r, "p"))
	}
	cfg := &Config{Roots: []Root{
		{Path: rc, Modes: []string{ModeCreate}}, {Path: ra, Modes: []string{ModeArchive}}, {Path: rb, Modes: []string{ModeCreate, ModeArchive}},
	}}
	a := newAuthorizer(t, cfg)
	for _, tc := range []struct {
		root, mode string
		ok         bool
	}{
		{rc, ModeCreate, true}, {rc, ModeArchive, false},
		{ra, ModeArchive, true}, {ra, ModeCreate, false},
		{rb, ModeCreate, true}, {rb, ModeArchive, true},
	} {
		_, err := authz(a, filepath.Join(tc.root, "p"), tc.mode)
		if tc.ok && err != nil {
			t.Errorf("%s %s: %v", filepath.Base(tc.root), tc.mode, err)
		}
		if !tc.ok {
			wantRefusal(t, err, CodeForbidden, "", causeMode)
		}
	}
}

func TestDirectoriesOutsideEveryRootAreRefusedEvenUnderAllowedRoots(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	repoAt(t, mkdir(t, root, "p"))
	allowed := mkdir(t, base, "allowed")
	staticProj := mkdir(t, allowed, "static")
	repoAt(t, staticProj)
	a := newAuthorizer(t, cfgFor(root, ModeCreate, ModeArchive))
	// A static session's directory is under BRIDGE_ALLOWED_ROOTS, not under a
	// lifecycle root: the lifecycle authorization does not accept it.
	p, err := security.New([]string{allowed}, []string{"ses_static"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Allows("ses_static", staticProj) {
		t.Fatal("test premise: the static session is allowed by the existing ACL")
	}
	_, err = authz(a, staticProj, ModeCreate)
	wantRefusal(t, err, CodeForbidden, "", causeNoRoot)
	for _, dir := range []string{base, filepath.Join(base, "lcx"), allowed, filepath.Dir(base)} {
		_, err := authz(a, dir, ModeArchive)
		wantRefusal(t, err, CodeForbidden, "", causeNoRoot)
	}
}

func TestSymlinkDirectoryAndSymlinkComponentsAreRefused(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	real := mkdir(t, root, "real")
	repoAt(t, real)
	outside := mkdir(t, base, "outside", "p")
	repoAt(t, outside)
	if err := os.Symlink(real, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	for _, dir := range []string{filepath.Join(root, "link"), filepath.Join(root, "link", "x"), filepath.Join(root, "escape")} {
		_, err := authz(a, dir, ModeCreate)
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Code == "" {
			t.Errorf("%s: %v", dir, err)
		}
		if err == nil {
			t.Errorf("%s authorized through a symlink", dir)
		}
	}
}

func TestRootReplacedAfterStartupIsRefused(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	repoAt(t, mkdir(t, root, "p"))
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	if _, err := authz(a, filepath.Join(root, "p"), ModeCreate); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(base, "lc-old")); err != nil {
		t.Fatal(err)
	}
	repoAt(t, mkdir(t, root, "p")) // a different directory at the same path
	_, err := authz(a, filepath.Join(root, "p"), ModeCreate)
	wantRefusal(t, err, CodeForbidden, "", causeRootReplaced)
}

func TestInputRefusals(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	repoAt(t, mkdir(t, root, "p"))
	write(t, filepath.Join(root, "file"), "x")
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	for name, dir := range map[string]string{"relative": "lc/p", "unclean": root + "/p/../p", "empty": ""} {
		_, err := authz(a, dir, ModeCreate)
		if name != "" {
			wantRefusal(t, err, CodeInvalidInput, "", causeNotCanonical)
		}
	}
	_, err := authz(a, filepath.Join(root, "p"), "delete")
	wantRefusal(t, err, CodeInvalidInput, "", causeMode)
	_, err = authz(a, filepath.Join(root, "missing"), ModeCreate)
	wantRefusal(t, err, CodeNotFound, "", causeNotDir)
	_, err = authz(a, filepath.Join(root, "file"), ModeCreate)
	wantRefusal(t, err, CodeInvalidInput, "", causeNotDir)
	var nilAuth *Authorizer
	if _, err := nilAuth.Authorize(context.Background(), root, ModeCreate); err == nil {
		t.Fatal("nil authorizer authorized")
	}
	if _, err := NewAuthorizer(nil); err == nil {
		t.Fatal("NewAuthorizer(nil)")
	}
	if _, err := NewAuthorizer(&Config{}); err == nil {
		t.Fatal("NewAuthorizer with no roots")
	}
}

// Rule 2: the identity of the accepted directory is checked again right before
// the native call; a directory swapped after the decision is refused.
func TestDirectoryReplacedAfterTheDecisionIsRefusedByRecheck(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	proj := mkdir(t, root, "p")
	repoAt(t, proj)
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	g, err := authz(a, proj, ModeCreate)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Recheck(context.Background()); err != nil {
		t.Fatalf("recheck of an unchanged grant: %v", err)
	}
	if err := os.Rename(proj, filepath.Join(base, "p-old")); err != nil {
		t.Fatal(err)
	}
	repoAt(t, mkdir(t, root, "p"))
	wantRefusal(t, g.Recheck(context.Background()), CodeForbidden, "", causeChanged)
	var none *Grant
	wantRefusal(t, none.Recheck(context.Background()), CodeForbidden, "", causeChanged)
}

func TestDirectoryReplacedDuringTheDecisionIsRefused(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	proj := mkdir(t, root, "p")
	repoAt(t, proj)
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	a.between = func() { // after rule 1, before rule 3
		if err := os.Rename(proj, filepath.Join(base, "p-old")); err != nil {
			t.Error(err)
		}
		repoAt(t, mkdir(t, root, "p"))
	}
	_, err := authz(a, proj, ModeCreate)
	wantRefusal(t, err, CodeForbidden, "", causeChanged)
}

func TestGitMetadataChangedAfterTheDecisionIsRefusedByRecheck(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	proj := mkdir(t, root, "p")
	repoAt(t, proj)
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	g, err := authz(a, proj, ModeCreate)
	if err != nil {
		t.Fatal(err)
	}
	// A hostile config appears after the decision.
	write(t, filepath.Join(proj, ".git", "config"), "[core]\n\tworktree = /\n")
	wantRefusal(t, g.Recheck(context.Background()), CodeForbidden, "", causeChanged)
}

// existing_tools is parsed but grants nothing: the decision is identical, and
// the existing ACL is untouched.
func TestExistingToolsFlagGrantsNothing(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	proj := mkdir(t, root, "p")
	repoAt(t, proj)
	off, on := cfgFor(root, ModeCreate), cfgFor(root, ModeCreate)
	on.Roots[0].ExistingTools = true
	ga, err1 := authz(newAuthorizer(t, off), proj, ModeCreate)
	gb, err2 := authz(newAuthorizer(t, on), proj, ModeCreate)
	if err1 != nil || err2 != nil || ga.TopLevel != gb.TopLevel || ga.Root != gb.Root {
		t.Fatalf("flag changed the decision: %+v %v / %+v %v", ga, err1, gb, err2)
	}
	allowed := mkdir(t, base, "allowed")
	p, err := security.New([]string{allowed}, []string{"ses_a"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Allows("ses_a", proj) || p.Allows("ses_lifecycle", proj) {
		t.Fatal("the existing-tool ACL accepted a lifecycle-only directory")
	}
}

// The scan reads only the repository config, follows no include and starts no
// process: the file an include names is never opened.
func TestConfigScanFollowsNoInclude(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	proj := mkdir(t, root, "p")
	repoAt(t, proj)
	canary := filepath.Join(root, "canary-include")
	write(t, canary, "[core]\n\tworktree = /\n")
	write(t, filepath.Join(proj, ".git", "config"), "[include]\n\tpath = "+canary+"\n")
	var mu sync.Mutex
	var opened []string
	restore := security.SetFSHookForTesting(func(p string) { mu.Lock(); opened = append(opened, p); mu.Unlock() })
	defer restore()
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	_, err := authz(a, proj, ModeCreate)
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Reason != ReasonNonGit {
		t.Fatalf("include must refuse: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range opened {
		if p == canary {
			t.Fatalf("the include target was opened")
		}
	}
}

func TestConfigWorktreeAndIncludeRefuseInEveryConfigFile(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	bad := map[string]string{
		"core.worktree":          "[core]\n\tworktree = ../elsewhere\n",
		"worktree upper":         "[CORE]\n\tWorkTree = /x\n",
		"key on the header line": "[core] worktree = /x\n",
		"no spaces":              "[core]\nworktree=/x\n",
		"bare key":               "[core]\n\tworktree\n",
		"include":                "[include]\n\tpath = x\n",
		"includeIf":              "[includeIf \"gitdir:/x/\"]\n\tpath = y\n",
		"includeif lower":        "[includeif \"onbranch:main\"]\n\tpath = y\n",
		"dotted include":         "[include.path]\n\tx = y\n",
		"after a clean section":  "[user]\n\tname = x\n[core]\n\tbare = false\n\tworktree = /x\n",
		"in a continuation":      "[user]\n\tname = a \\\n[include]\n",
	}
	i := 0
	for name, cfg := range bad {
		i++
		for _, where := range []string{"config", "config.worktree"} {
			dir := mkdir(t, root, "p", strings.ReplaceAll(name, " ", "-")+where)
			repoAt(t, dir)
			write(t, filepath.Join(dir, ".git", where), cfg)
			_, err := authz(a, dir, ModeCreate)
			var ae *AuthError
			if !errors.As(err, &ae) || ae.Reason != ReasonNonGit {
				t.Errorf("%s in %s: %v, want non_git", name, where, err)
			}
		}
	}
	// In the common directory of a linked worktree, and in the worktree's own config.
	main := mkdir(t, root, "main")
	repoAt(t, main)
	wt := mkdir(t, root, "wt")
	gd := worktreeAt(t, main, wt, "wt")
	if _, err := authz(a, wt, ModeCreate); err != nil {
		t.Fatalf("clean linked worktree: %v", err)
	}
	write(t, filepath.Join(main, ".git", "config"), "[core]\n\tworktree = /x\n")
	_, err := authz(a, wt, ModeCreate)
	wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeConfigWorktree)
	write(t, filepath.Join(main, ".git", "config"), "[core]\n")
	write(t, filepath.Join(gd, "config.worktree"), "[core]\n\tworktree = /x\n")
	_, err = authz(a, wt, ModeCreate)
	wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeConfigWorktree)
}

func TestCleanConfigsAreAccepted(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	for name, cfg := range map[string]string{
		"typical":                   "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n[remote \"origin\"]\n\turl = x\n[branch \"main\"]\n\tremote = origin\n",
		"comments":                  "# core.worktree = /x\n; [include]\n[core]\n\t# worktree = /y\n\tfilemode = true\n",
		"empty":                     "",
		"worktree in a value":       "[alias]\n\tw = !echo core.worktree include\n",
		"files storage":             "[extensions]\n\trefStorage = files\n",
		"subsection named worktree": "[remote \"worktree\"]\n\turl = x\n",
		"bom":                       "\xef\xbb\xbf[core]\n\tfilemode = true\n",
	} {
		dir := mkdir(t, root, "p-"+strings.ReplaceAll(name, " ", "-"))
		repoAt(t, dir)
		write(t, filepath.Join(dir, ".git", "config"), cfg)
		if _, err := authz(a, dir, ModeCreate); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An absent config has no settings.
	dir := mkdir(t, root, "noconfig")
	write(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	if _, err := authz(a, dir, ModeCreate); err != nil {
		t.Errorf("absent config: %v", err)
	}
}

func TestConfigScanIsBounded(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	for name, cfg := range map[string]string{
		"too large":      "[core]\n" + strings.Repeat("#"+strings.Repeat("x", 100)+"\n", maxGitConfigBytes/100+10),
		"long line":      "[core]\n\tfilemode = " + strings.Repeat("a", maxGitConfigLine+10) + "\n",
		"too many lines": strings.Repeat("\n", maxGitConfigLines+5),
		"nul byte":       "[core]\n\tfilemode = true\x00\n",
		"unclosed":       "[core\n\tfilemode = true\n",
	} {
		dir := mkdir(t, root, "b-"+strings.ReplaceAll(name, " ", "-"))
		repoAt(t, dir)
		write(t, filepath.Join(dir, ".git", "config"), cfg)
		_, err := authz(a, dir, ModeCreate)
		var ae *AuthError
		if !errors.As(err, &ae) || ae.Reason != ReasonNonGit {
			t.Errorf("%s: %v, want non_git", name, err)
		}
	}
	// A config that is not a regular file (a symlink) is refused: not known safe.
	dir := mkdir(t, root, "cfg-symlink")
	repoAt(t, dir)
	_ = os.Remove(filepath.Join(dir, ".git", "config"))
	write(t, filepath.Join(root, "elsewhere"), "[core]\n")
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(dir, ".git", "config")); err != nil {
		t.Fatal(err)
	}
	_, err := authz(a, dir, ModeCreate)
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Reason != ReasonNonGit {
		t.Errorf("symlinked config: %v", err)
	}
}

func TestScanGitConfigCauses(t *testing.T) {
	for cfg, want := range map[string]string{
		"[core]\nworktree = x":                causeConfigWorktree,
		"[include]\npath = x":                 causeConfigInclude,
		"[core]\nbare = true":                 causeConfigBare,
		"[core]\nbare = YES":                  causeConfigBare,
		"[core]\nbare":                        causeConfigBare,
		"[core]\nbare = false":                "",
		"[extensions]\nrefstorage = reftable": causeConfigReftable,
		"":                                    "",
	} {
		if got := scanGitConfig([]byte(cfg)); got != want {
			t.Errorf("%q: %q, want %q", cfg, got, want)
		}
	}
}

// A filesystem call that blocks must not hold the request: the outcome is
// unknown and never an authorization.
func TestABlockedFilesystemIsUnavailableNotAuthorized(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	proj := mkdir(t, root, "p")
	repoAt(t, proj)
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	release := make(chan struct{})
	restore := security.SetFSHookForTesting(func(string) { <-release })
	defer restore()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.Authorize(ctx, proj, ModeCreate); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, security.ErrCheckUnavailable) {
			t.Fatalf("want ErrCheckUnavailable, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a blocked filesystem call held the request past its deadline")
	}
	close(release)
}
