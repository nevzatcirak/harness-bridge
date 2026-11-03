package repository

import (
	"context"
	"encoding/json"
	"github.com/nevzatcirak/harness-bridge/internal/security"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if b, e := c.CombinedOutput(); e != nil {
		t.Fatalf("git: %v %s", e, b)
	}
}
func TestWorktreeIdentityAndBounds(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init")
	gitTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "seed")
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, root, "worktree", "add", "-b", "linked", linked)
	policy, _ := security.New([]string{root, linked}, []string{"ses_test"})
	ctx := context.Background()
	r, w, e := Inspect(ctx, root, policy, "ses_test")
	if e != nil {
		t.Fatal(e)
	}
	r2, w2, e := Inspect(ctx, linked, policy, "ses_test")
	if e != nil {
		t.Fatal(e)
	}
	if r.ID != r2.ID || w.ID == w2.ID || w2.Branch != "linked" || w.Head != w2.Head {
		t.Fatal(r, w, r2, w2)
	}
	clone := t.TempDir()
	gitTest(t, clone, "init")
	cp, _ := security.New([]string{clone}, []string{"ses_test"})
	r3, _, e := Inspect(ctx, clone, cp, "ses_test")
	if e != nil || r.ID == r3.ID {
		t.Fatal("independent repository identity", e)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if e := os.Symlink(root, link); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{link, root + "/../" + filepath.Base(root), "relative", t.TempDir()} {
		if _, _, e := Inspect(ctx, p, policy, "ses_test"); e == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, e := Inspect(canceled, root, policy, "ses_test"); e == nil {
		t.Fatal("ignored cancellation")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr, ww, e := Inspect(ctx, linked, policy, "ses_test")
			if e != nil || rr.ID != r.ID || ww.ID != w2.ID {
				t.Error("concurrent identity", e)
			}
		}()
	}
	wg.Wait()
}

func TestStorageBoundariesAndDisplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	gitTest(t, root, "init")
	gitTest(t, root, "-c", "user.name=Test", "-c", "user.email=t@example.invalid", "commit", "--allow-empty", "-m", "seed")
	p, _ := security.New([]string{root}, []string{"ses_test"})
	inspect := func(dir string, policy *security.Policy) (Repository, Workspace, error) {
		return Inspect(ctx, dir, policy, "ses_test")
	}
	r, w, e := inspect(root, p)
	if e != nil || r.DisplayName != filepath.Base(root) || w.DisplayName != filepath.Base(root) || w.Head == "" || w.Branch == "" {
		t.Fatal(r, w, e)
	}
	// Config is never opened, even when it contains external includes, executable
	// helpers/filters, and a credential-bearing remote. Host Git env is irrelevant.
	secret := "credential_must_not_escape"
	config := "[include]\npath = /outside/not-approved\n[core]\nfsmonitor = /outside/not-approved\n[filter \"evil\"]\nsmudge = /outside/not-approved\n[remote \"origin\"]\nurl = https://user:" + secret + "@example.invalid/repo\n"
	if e := os.WriteFile(filepath.Join(root, ".git", "config"), []byte(config), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("GIT_DIR", "/outside/not-approved")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
	t.Setenv("GIT_CONFIG_VALUE_0", "/outside/not-approved")
	rr, ww, e := inspect(root, p)
	if e != nil || r.ID != rr.ID || w.ID != ww.ID {
		t.Fatal(rr, ww, e)
	}
	data, _ := json.Marshal([]any{rr, ww})
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "example.invalid") {
		t.Fatal("remote disclosure")
	}
	// Unapproved external pointer: even an existing Git directory cannot be read.
	pointed := t.TempDir()
	if e := os.WriteFile(filepath.Join(pointed, ".git"), []byte("gitdir: "+filepath.Join(root, ".git")+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	pp, _ := security.New([]string{pointed}, []string{"ses_test"})
	if _, _, e := inspect(pointed, pp); e == nil {
		t.Fatal("external pointer accepted")
	}
	// No metadata grant is inferred from the linked workspace's session grant.
	// Create fixture with a clean config, since Git fixture setup does parse config.
	os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\nrepositoryformatversion = 0\nbare = false\n"), 0600)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, root, "worktree", "add", "-b", "review", linked)
	lp, _ := security.New([]string{linked}, []string{"ses_test"})
	if _, _, e := inspect(linked, lp); e == nil {
		t.Fatal("unapproved common storage accepted")
	}
	both, _ := security.New([]string{root, linked}, []string{"ses_test"})
	lr, lw, e := inspect(linked, both)
	if e != nil || lr.ID != r.ID || !lw.LinkedWorktree || lw.DisplayName != "linked" || lw.ID == w.ID {
		t.Fatal(lr, lw, e)
	}
	// Independent local clone: same upstream and revision never unify local IDs.
	clone := filepath.Join(t.TempDir(), "clone")
	gitTest(t, root, "clone", "--no-hardlinks", root, clone)
	cp, _ := security.New([]string{clone}, []string{"ses_test"})
	cr, cw, e := inspect(clone, cp)
	if e != nil || cr.ID == r.ID || cw.Head != w.Head {
		t.Fatal(cr, cw, e)
	}
	gitTest(t, root, "pack-refs", "--all")
	_, packed, e := inspect(root, p)
	if e != nil || packed.Head != w.Head {
		t.Fatal(packed, e)
	}
	if _, _, e := inspect(root, &security.Policy{}); e == nil {
		t.Fatal("missing ACL accepted")
	}
	nonGit := t.TempDir()
	np, _ := security.New([]string{nonGit}, []string{"ses_test"})
	if _, _, e := inspect(nonGit, np); e == nil {
		t.Fatal("non-Git metadata invented")
	}
	if got := display(strings.Repeat("x", 200) + "\n"); len([]rune(got)) != 128 || strings.ContainsAny(got, "\r\n") {
		t.Fatal("display bound")
	}
}

func TestReplacedAndSymlinkStorage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	gitTest(t, root, "init")
	p, _ := security.New([]string{root}, []string{"ses_test"})
	head := filepath.Join(root, ".git", "HEAD")
	saved := filepath.Join(root, ".git", "HEAD.saved")
	if e := os.Rename(head, saved); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte(strings.Repeat("a", 40)), 0600)
	os.Symlink(outside, head)
	if _, _, e := Inspect(ctx, root, p, "ses_test"); e == nil {
		t.Fatal("HEAD symlink accepted")
	}
	os.Remove(head)
	os.Rename(saved, head)
	// A pinned storage reader detects replacement, even with an ordinary file.
	s := storage{policy: p, session: "ses_test", ctx: ctx, observed: map[string]os.FileInfo{}}
	if _, e := s.read(head, 4096); e != nil {
		t.Fatal(e)
	}
	os.Rename(head, saved)
	os.WriteFile(head, []byte("ref: refs/heads/replaced\n"), 0600)
	if s.unchanged() {
		t.Fatal("file replacement undetected")
	}
	// A root replaced by another directory at the same pathname must be rejected.
	moved := root + "-old"
	if e := os.Rename(root, moved); e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(moved)
	os.Mkdir(root, 0700)
	if p.Allows("ses_test", root) {
		t.Fatal("root replacement authorized")
	}
	if _, _, e := Inspect(ctx, root, p, "ses_test"); e == nil {
		t.Fatal("root replacement inspected")
	}
}

func TestUnapprovedCommonDirectory(t *testing.T) {
	root := t.TempDir()
	gd := filepath.Join(root, ".git")
	os.Mkdir(gd, 0700)
	external := t.TempDir()
	os.WriteFile(filepath.Join(gd, "HEAD"), []byte("ref: refs/heads/main\n"), 0600)
	os.WriteFile(filepath.Join(gd, "commondir"), []byte(external+"\n"), 0600)
	p, _ := security.New([]string{root}, []string{"ses_test"})
	if _, _, e := Inspect(context.Background(), root, p, "ses_test"); e == nil {
		t.Fatal("external commondir accepted")
	}
	// Final .git symlink is rejected, even when its target is inside approved storage.
	os.Remove(filepath.Join(gd, "commondir"))
	os.Rename(gd, filepath.Join(root, "git-store"))
	os.Symlink("git-store", gd)
	if _, _, e := Inspect(context.Background(), root, p, "ses_test"); e == nil {
		t.Fatal("internal Git symlink accepted")
	}
}

func TestMissingSymlinkRefIsNotUnborn(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init")
	gd := filepath.Join(root, ".git")
	os.Rename(filepath.Join(gd, "refs"), filepath.Join(gd, "real-refs"))
	os.Symlink("real-refs", filepath.Join(gd, "refs"))
	p, _ := security.New([]string{root}, []string{"ses_test"})
	if _, _, e := Inspect(context.Background(), root, p, "ses_test"); e == nil {
		t.Fatal("rejected ref path was mistaken for an unborn branch")
	}
}
