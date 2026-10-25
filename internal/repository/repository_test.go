package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	ctx := context.Background()
	r, w, e := Inspect(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	r2, w2, e := Inspect(ctx, linked)
	if e != nil {
		t.Fatal(e)
	}
	if r.ID != r2.ID || w.ID == w2.ID || w2.Branch != "linked" || w.Head != w2.Head {
		t.Fatal(r, w, r2, w2)
	}
	clone := t.TempDir()
	gitTest(t, clone, "init")
	r3, _, e := Inspect(ctx, clone)
	if e != nil || r.ID == r3.ID {
		t.Fatal("independent repository identity", e)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if e := os.Symlink(root, link); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{link, root + "/../" + filepath.Base(root), "relative", t.TempDir()} {
		if _, _, e := Inspect(ctx, p); e == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, e := Inspect(canceled, root); e == nil {
		t.Fatal("ignored cancellation")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr, ww, e := Inspect(ctx, linked)
			if e != nil || rr.ID != r.ID || ww.ID != w2.ID {
				t.Error("concurrent identity", e)
			}
		}()
	}
	wg.Wait()
}
