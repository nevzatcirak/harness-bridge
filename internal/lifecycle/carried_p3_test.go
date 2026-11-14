package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// #230 P3-1 / #232 P3-1: core.bare is read like git reads a boolean, and a value
// the scan cannot classify refuses (fail closed). The expectations were checked
// against `git config --type=bool` offline when this was written.
func TestCoreBareFollowsGitBooleanSpellings(t *testing.T) {
	for value, want := range map[string]string{
		"true":                 causeConfigBare,
		"True":                 causeConfigBare,
		"yes":                  causeConfigBare,
		"on":                   causeConfigBare,
		"1":                    causeConfigBare,
		"2":                    causeConfigBare,
		"0x1":                  causeConfigBare,
		"010":                  causeConfigBare, // octal 8
		"1k":                   causeConfigBare,
		"-1":                   causeConfigBare,
		"true # comment":       causeConfigBare,
		"true ; comment":       causeConfigBare,
		"true\t# comment":      causeConfigBare,
		`"true"`:               causeConfigBare,
		`"true" # comment`:     causeConfigBare,
		"":                     causeConfigBare, // git reads an empty value as false: refusing is the safe side
		"false":                "",
		"False":                "",
		"no":                   "",
		"off":                  "",
		"0":                    "",
		"00":                   "",
		"0x0":                  "",
		"0k":                   "",
		"false # comment":      "",
		"false ; comment":      "",
		`"false"`:              "",
		`"false" # c ; d`:      "",
		"nonsense":             causeConfigMalform,
		"fal#se":               causeConfigMalform, // the comment cuts it to "fal"
		"k":                    causeConfigMalform,
		"0xzz":                 causeConfigMalform,
		"99999999999999999999": causeConfigMalform, // out of range: git errors, the scan refuses
		"tru e":                causeConfigMalform,
	} {
		cfg := "[core]\n\tbare = " + value + "\n"
		if got := scanGitConfig([]byte(cfg)); got != want {
			t.Errorf("bare = %q: %q, want %q", value, got, want)
		}
	}
	// A key with no value is true in git, with or without a trailing comment.
	for _, cfg := range []string{"[core]\nbare\n", "[core]\nbare # c\n", "[core]\nbare ; c\n"} {
		if got := scanGitConfig([]byte(cfg)); got != causeConfigBare {
			t.Errorf("%q: %q", cfg, got)
		}
	}
	// The same comment handling applies to extensions.refstorage: a harmless trailing
	// comment no longer turns "files" into a reftable refusal.
	for cfg, want := range map[string]string{
		"[extensions]\n\trefstorage = files # ok\n":     "",
		"[extensions]\n\trefstorage = \"files\" ; ok\n": "",
		"[extensions]\n\trefstorage = reftable # x\n":   causeConfigReftable,
		"[extensions]\n\trefstorage = files-v2\n":       causeConfigReftable,
	} {
		if got := scanGitConfig([]byte(cfg)); got != want {
			t.Errorf("%q: %q, want %q", cfg, got, want)
		}
	}
}

// A bare repository whose config says "bare = true # c" is refused end to end.
func TestBareConfigWithAnInlineCommentIsRefusedByTheAuthorizer(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	for _, value := range []string{"true # c", "2", "0x1", "yes"} {
		proj := mkdir(t, root, "p-"+filepath.Base(t.TempDir()))
		repoAt(t, proj)
		write(t, filepath.Join(proj, ".git", "config"), "[core]\n\tbare = "+value+"\n")
		_, err := authz(newAuthorizer(t, cfgFor(root, ModeCreate)), proj, ModeCreate)
		wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeConfigBare)
	}
}

// #230 P3-2 / #232 P3-2: a hard link is refused for every file the Bridge reads. A hard
// link is the one thing neither O_NOFOLLOW nor os.Root stops: it makes a file outside
// the root look like a regular file inside it. HEAD is only Lstat'ed (never read), so a
// hard link there has nothing to leak and is not part of this test.
func TestHardLinkedMetadataIsRefused(t *testing.T) {
	base := canonicalTemp(t)
	root := mkdir(t, base, "lc")
	outside := mkdir(t, base, "outside")
	secret := func(name, content string) string {
		p := filepath.Join(outside, name)
		write(t, p, content)
		return p
	}
	t.Run("the .git file", func(t *testing.T) {
		main := mkdir(t, root, "hl-main")
		repoAt(t, main)
		wt := mkdir(t, root, "hl-wt")
		gd := filepath.Join(main, ".git", "worktrees", "wt")
		write(t, filepath.Join(gd, "HEAD"), "ref: refs/heads/wt\n")
		write(t, filepath.Join(gd, "commondir"), "../..\n")
		if err := os.Link(secret("dotgit", "gitdir: "+gd+"\n"), filepath.Join(wt, ".git")); err != nil {
			t.Skip("hard links unavailable:", err)
		}
		_, err := authz(newAuthorizer(t, cfgFor(root, ModeCreate)), wt, ModeCreate)
		wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeGitdir)
	})
	t.Run("commondir", func(t *testing.T) {
		main := mkdir(t, root, "hl2-main")
		repoAt(t, main)
		wt := mkdir(t, root, "hl2-wt")
		gd := filepath.Join(main, ".git", "worktrees", "wt")
		write(t, filepath.Join(gd, "HEAD"), "ref: refs/heads/wt\n")
		write(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
		if err := os.Link(secret("commondir", "../..\n"), filepath.Join(gd, "commondir")); err != nil {
			t.Skip("hard links unavailable:", err)
		}
		_, err := authz(newAuthorizer(t, cfgFor(root, ModeCreate)), wt, ModeCreate)
		wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeLayout)
	})
	for _, name := range []string{"config", "config.worktree"} {
		t.Run(name, func(t *testing.T) {
			proj := mkdir(t, root, "hl-"+name)
			repoAt(t, proj)
			if err := os.Remove(filepath.Join(proj, ".git", name)); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Link(secret(name, "[core]\n\tbare = false\n"), filepath.Join(proj, ".git", name)); err != nil {
				t.Skip("hard links unavailable:", err)
			}
			_, err := authz(newAuthorizer(t, cfgFor(root, ModeCreate)), proj, ModeCreate)
			wantRefusal(t, err, CodeForbidden, ReasonNonGit, causeConfigBounds)
		})
	}
}

// #232 P3-4: Recheck runs the top level and config scan again. A config rewritten to
// the same size and the same modification time passes the identity comparison, and
// only the scan sees it.
func TestRecheckSeesAConfigRewrittenToTheSameSizeAndTime(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	proj := mkdir(t, root, "p")
	repoAt(t, proj)
	cfg := filepath.Join(proj, ".git", "config")
	write(t, cfg, "[core]\n\tbare = false\n")
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	g, err := authz(a, proj, ModeCreate)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[core]\n\tbare = true \n"), 0o644); err != nil { // same length
		t.Fatal(err)
	}
	if err := os.Chtimes(cfg, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	now, _ := os.Stat(cfg)
	if now.Size() != before.Size() || !now.ModTime().Equal(before.ModTime()) {
		t.Fatalf("the probe did not keep size and time: %d/%v vs %d/%v", now.Size(), now.ModTime(), before.Size(), before.ModTime())
	}
	wantRefusal(t, g.Recheck(context.Background()), CodeForbidden, "", causeChanged)
}

// #232 P3-4: a nearer .git that appears between the directory and the old top level
// after the decision is found by the refreshed ascent.
func TestRecheckSeesANewerNearerTopLevel(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	proj := mkdir(t, root, "p")
	repoAt(t, proj)
	sub := mkdir(t, proj, "pkg", "deep")
	a := newAuthorizer(t, cfgFor(root, ModeCreate))
	g, err := authz(a, sub, ModeCreate)
	if err != nil || g.TopLevel != proj {
		t.Fatalf("%+v %v", g, err)
	}
	if err := g.Recheck(context.Background()); err != nil {
		t.Fatalf("an unchanged grant: %v", err)
	}
	repoAt(t, filepath.Join(proj, "pkg")) // a new nested repository: inner wins now
	wantRefusal(t, g.Recheck(context.Background()), CodeForbidden, "", causeChanged)
}

// The refreshed Recheck is bounded like the decision: a hung filesystem is unknown.
func TestRecheckOfAnUnchangedGrantStillPasses(t *testing.T) {
	root := mkdir(t, canonicalTemp(t), "lc")
	proj := mkdir(t, root, "p")
	repoAt(t, proj)
	g, err := authz(newAuthorizer(t, cfgFor(root, ModeCreate)), proj, ModeCreate)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 3; i++ {
		if err := g.Recheck(ctx); err != nil {
			t.Fatalf("recheck %d: %v", i, err)
		}
	}
}
