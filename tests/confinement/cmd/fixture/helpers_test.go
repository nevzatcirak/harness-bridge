//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// F-A: new-file creation never opens a planted symlink or existing name.
func TestWriteRefusesSymlinkAndExistingName(t *testing.T) {
	c := newCanary(t)
	dir := t.TempDir()
	planted := filepath.Join(dir, "planted")
	if e := os.Symlink(c.file, planted); e != nil {
		t.Fatal(e)
	}
	existing := filepath.Join(dir, "existing")
	if e := os.WriteFile(existing, []byte("KEEP"), 0644); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{planted, existing} {
		if catch(func() { writeOwned(p, []byte("X"), 0600, os.Getuid()) }) == nil {
			t.Errorf("writeOwned(%s) did not fail closed", p)
		}
		if catch(func() { copyFile(c.file, p, 0755) }) == nil {
			t.Errorf("copyFile(%s) did not fail closed", p)
		}
	}
	c.verify(t)
	if b, _ := os.ReadFile(existing); string(b) != "KEEP" {
		t.Errorf("existing file modified: %q", b)
	}
	fresh := filepath.Join(dir, "fresh")
	writeOwned(fresh, []byte("NEW"), 0640, os.Getuid())
	if fi, e := os.Lstat(fresh); e != nil || fi.Mode().Perm() != 0640 {
		t.Errorf("fresh file mode: %v %v", fi, e)
	}
}

// F-A: handoff refuses a symlink in any path component, including the last.
func TestHandoffRefusesSymlinkComponents(t *testing.T) {
	c := newCanary(t)
	base := t.TempDir()
	if e := os.MkdirAll(filepath.Join(base, "real/child"), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(c.dir, filepath.Join(base, "link")); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(c.dir, filepath.Join(base, "real/leaf")); e != nil {
		t.Fatal(e)
	}
	for _, rel := range []string{"link", "link/sentinel", "real/leaf"} {
		failure := catch(func() { handoff(base, rel, os.Getuid()) })
		e, _ := failure.(error)
		if !errors.Is(e, syscall.ELOOP) && !errors.Is(e, syscall.ENOTDIR) {
			t.Errorf("handoff(%s) = %v, want ELOOP/ENOTDIR", rel, failure)
		}
	}
	handoff(base, "real/child", os.Getuid())
	if fi, e := os.Lstat(filepath.Join(base, "real/child")); e != nil || fi.Mode().Perm() != 0700 {
		t.Errorf("real handoff: %v %v", fi, e)
	}
	c.verify(t)
}

// F-C: cleanup is idempotent and does not follow symlinks out of the tree.
func TestCleanupIdempotentAndNoFollow(t *testing.T) {
	c := newCanary(t)
	base := filepath.Join(t.TempDir(), "base")
	if e := os.MkdirAll(filepath.Join(base, "state/home"), 0700); e != nil {
		t.Fatal(e)
	}
	for name, target := range map[string]string{"state/to-file": c.file, "state/to-dir": c.dir, "state/home/deep": c.dir} {
		if e := os.Symlink(target, filepath.Join(base, name)); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 3; i++ {
		if e := cleanup(base); e != nil {
			t.Fatalf("cleanup #%d: %v", i+1, e)
		}
	}
	if _, e := os.Lstat(base); !errors.Is(e, os.ErrNotExist) {
		t.Errorf("base still present: %v", e)
	}
	c.verify(t)
}

// checkBase rejects every malformed fixture root before touching it.
func TestCheckBaseRejectsUntrustedRoots(t *testing.T) {
	link := filepath.Join(t.TempDir(), "link")
	if e := os.Symlink(t.TempDir(), link); e != nil {
		t.Fatal(e)
	}
	for _, base := range []string{"", "relative/hb-confinement-fixture-x", basePrefix + "x/../y", basePrefix + "x/nested", "/var/tmp/hb-confinement-fixture-x", "/tmp/other-x", link, basePrefix + "absent-" + strings.Repeat("z", 8)} {
		if catch(func() { checkBase(base) }) == nil {
			t.Errorf("checkBase(%q) accepted", base)
		}
	}
}
