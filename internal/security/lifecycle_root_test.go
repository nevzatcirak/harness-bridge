package security

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func canonicalDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mk(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewLifecycleRootsRefusals(t *testing.T) {
	base := canonicalDir(t)
	real := mk(t, base, "real")
	if err := os.Symlink(real, filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, paths := range map[string][]string{
		"none":         nil,
		"relative":     {"rel/x"},
		"slash":        {"/"},
		"unclean":      {real + "/"},
		"symlink":      {filepath.Join(base, "alias")},
		"missing":      {filepath.Join(base, "nope")},
		"not a dir":    {file},
		"one bad of 2": {real, filepath.Join(base, "nope")},
	} {
		if _, err := NewLifecycleRoots(paths); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewLifecycleRoots([]string{real}); err != nil {
		t.Fatal(err)
	}
}

// ADR-022 S2: the opener works without a session, unlike OpenStorage.
func TestLifecycleOpenWorksWithoutASession(t *testing.T) {
	base := canonicalDir(t)
	root := mk(t, base, "lc")
	sub := mk(t, root, "a", "b")
	l, err := NewLifecycleRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	h, rel, gotBase, err := l.Open(sub)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if rel != filepath.Join("a", "b") || gotBase != root {
		t.Fatalf("rel %q base %q", rel, gotBase)
	}
	if _, err := h.Lstat(rel); err != nil {
		t.Fatal(err)
	}
	// The root itself opens too.
	if h2, rel2, _, err := l.Open(root); err != nil || rel2 != "." {
		t.Fatalf("root: %q %v", rel2, err)
	} else {
		h2.Close()
	}
	// The session-bound opener refuses the same path for lack of a session.
	p, err := New([]string{root}, []string{"ses_a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.OpenStorage("", sub); err == nil {
		t.Fatal("OpenStorage accepted a session-less call")
	}
	if _, _, err := p.OpenStorage("ses_unknown", sub); err == nil {
		t.Fatal("OpenStorage accepted an unknown session")
	}
}

// The path is refused outside the lifecycle root even when it lies under
// BRIDGE_ALLOWED_ROOTS: there is no fallback.
func TestLifecycleOpenNeverFallsBackToAllowedRoots(t *testing.T) {
	base := canonicalDir(t)
	allowed := mk(t, base, "allowed")
	inAllowed := mk(t, allowed, "proj")
	lc := mk(t, base, "lc")
	l, err := NewLifecycleRoots([]string{lc})
	if err != nil {
		t.Fatal(err)
	}
	p, err := New([]string{allowed}, []string{"ses_a"})
	if err != nil {
		t.Fatal(err)
	}
	// The existing opener accepts that path for a listed session ...
	if h, _, err := p.OpenStorage("ses_a", inAllowed); err != nil {
		t.Fatalf("test premise: %v", err)
	} else {
		h.Close()
	}
	// ... the lifecycle opener must not.
	if _, _, _, err := l.Open(inAllowed); !errors.Is(err, ErrNotInLifecycleRoot) {
		t.Fatalf("path under an allowed root but outside the lifecycle root: %v", err)
	}
	for _, outside := range []string{base, filepath.Join(base, "lcx"), filepath.Join(lc, ".."), "/", "relative"} {
		if _, _, _, err := l.Open(outside); err == nil {
			t.Errorf("%q opened", outside)
		}
	}
	// A lifecycle path is not opened by OpenStorage either: no shared grant.
	if _, _, err := p.OpenStorage("ses_a", lc); err == nil {
		t.Fatal("OpenStorage opened a lifecycle-only path")
	}
}

func TestLifecycleOpenRefusesAReplacedRoot(t *testing.T) {
	base := canonicalDir(t)
	root := mk(t, base, "lc")
	mk(t, root, "proj")
	l, err := NewLifecycleRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(base, "lc-old")); err != nil {
		t.Fatal(err)
	}
	mk(t, root, "proj") // a different directory at the same path
	if _, _, _, err := l.Open(filepath.Join(root, "proj")); !errors.Is(err, ErrLifecycleRootReplaced) {
		t.Fatalf("replaced root: %v", err)
	}
}

func TestLifecycleOpenRefusesSymlinkComponents(t *testing.T) {
	base := canonicalDir(t)
	root := mk(t, base, "lc")
	mk(t, root, "real", "deep")
	outside := mk(t, base, "outside", "x")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	l, err := NewLifecycleRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(root, "link"), filepath.Join(root, "link", "deep"),
		filepath.Join(root, "escape"), filepath.Join(root, "escape"),
	} {
		if _, _, _, err := l.Open(p); !errors.Is(err, ErrLifecycleSymlink) {
			t.Errorf("%s: %v, want a symlink refusal", p, err)
		}
	}
	if _, _, _, err := l.Open(filepath.Join(root, "real", "missing")); !os.IsNotExist(err) {
		t.Errorf("missing component must stay distinguishable: %v", err)
	}
}

// The session-bound opener keeps its behavior: the lifecycle opener adds a
// separate primitive and changes nothing about it.
func TestOpenStorageBehaviorUnchanged(t *testing.T) {
	base := canonicalDir(t)
	allowed := mk(t, base, "allowed")
	sub := mk(t, allowed, "p")
	other := mk(t, base, "other")
	p, err := New([]string{allowed}, []string{"ses_a"})
	if err != nil {
		t.Fatal(err)
	}
	if h, rel, err := p.OpenStorage("ses_a", sub); err != nil || rel != "p" {
		t.Fatalf("listed session, allowed path: %q %v", rel, err)
	} else {
		h.Close()
	}
	if _, _, err := p.OpenStorage("ses_a", other); err == nil {
		t.Fatal("path outside the allowed roots opened")
	}
	if _, _, err := p.OpenStorage("ses_b", sub); err == nil {
		t.Fatal("unlisted session opened a path")
	}
	// Building a lifecycle opener over the same tree changes none of that.
	if _, err := NewLifecycleRoots([]string{other}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.OpenStorage("ses_a", other); err == nil {
		t.Fatal("a lifecycle root became an allowed root")
	}
	if p.Allows("ses_a", other) {
		t.Fatal("Allows accepted a lifecycle-only directory")
	}
}

// #230 P3-3 / #232 P3-3: the primitive refuses duplicate and nested roots itself, so the
// root a path belongs to never depends on list order and an ascent cannot cross into a
// second root, whatever built the list.
func TestNewLifecycleRootsRefusesOverlap(t *testing.T) {
	base := canonicalDir(t)
	outer := mk(t, base, "outer")
	inner := mk(t, outer, "inner")
	other := mk(t, base, "other")
	for name, paths := range map[string][]string{
		"duplicate":      {outer, outer},
		"nested":         {outer, inner},
		"nested reverse": {inner, outer},
		"nested of 3":    {other, outer, inner},
	} {
		if _, err := NewLifecycleRoots(paths); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	l, err := NewLifecycleRoots([]string{outer, other})
	if err != nil {
		t.Fatal(err)
	}
	if got := l.RootOf(inner); got != outer {
		t.Errorf("RootOf(%s) = %q", inner, got)
	}
	// A sibling that shares a name prefix is not nested.
	if _, err := NewLifecycleRoots([]string{outer, outer + "-2"}); err == nil {
		t.Log("(outer-2 does not exist, so it is refused as missing; the prefix case is below)")
	}
	sib := mk(t, base, "outer-2")
	if _, err := NewLifecycleRoots([]string{outer, sib}); err != nil {
		t.Errorf("a name-prefix sibling is not nested: %v", err)
	}
}

// ---- H5 live finding F-H5-2 (task #460): the startup check must be the open the request path uses ----

// Live, a lifecycle root whose service user had only search permission (--x) passed startup and
// then made every create_session FORBIDDEN without a reason, because the request path opens the
// root with os.OpenRoot (read permission needed). Startup now performs that open, and closes it.

// withOpenRoot replaces the opener for one test. The security tests are not parallel.
func withOpenRoot(t *testing.T, f func(string) (*os.Root, error)) {
	t.Helper()
	prev := openRoot
	openRoot = f
	t.Cleanup(func() { openRoot = prev })
}

func TestNewLifecycleRootsOpensAndClosesEveryRootAtStartup(t *testing.T) {
	base := canonicalDir(t)
	a, b := mk(t, base, "a"), mk(t, base, "b")
	var opened []*os.Root
	withOpenRoot(t, func(p string) (*os.Root, error) {
		r, err := os.OpenRoot(p)
		if err == nil {
			opened = append(opened, r)
		}
		return r, err
	})
	if _, err := NewLifecycleRoots([]string{a, b}); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 2 {
		t.Fatalf("%d roots opened at startup, want 2", len(opened))
	}
	for i, r := range opened {
		if _, err := r.Stat("."); err == nil {
			t.Errorf("root %d was left open by the startup check", i)
		}
	}
}

func TestNewLifecycleRootsRefusesARootTheServiceUserCannotOpen(t *testing.T) {
	base := canonicalDir(t)
	good, bad := mk(t, base, "good"), mk(t, base, "bad")
	withOpenRoot(t, func(p string) (*os.Root, error) {
		if p == bad {
			return nil, &os.PathError{Op: "openat", Path: p, Err: os.ErrPermission}
		}
		return os.OpenRoot(p)
	})
	_, err := NewLifecycleRoots([]string{good, bad})
	if err == nil {
		t.Fatal("a root the process cannot open was accepted at startup")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("the cause is lost: %v", err)
	}
	for _, want := range []string{bad, "cannot be opened", "r-x"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("startup error %q lacks %q", err, want)
		}
	}
}

// The real thing, with a real permission failure. It needs a non-root user (root opens anything):
// run as an unprivileged user it must pass; as root it is NOT EXERCISED (reported, never a pass).
func TestNewLifecycleRootsRefusesAnUnreadableRootOnDisk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("NOT EXERCISED as root: root can open any directory; run as an unprivileged user")
	}
	base := canonicalDir(t)
	root := mk(t, base, "root")
	if err := os.Chmod(root, 0o300); err != nil { // search and write, no read: the live "--x" shape
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if _, err := NewLifecycleRoots([]string{root}); err == nil {
		t.Fatal("an unreadable root was accepted at startup")
	}
}
