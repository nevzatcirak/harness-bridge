//go:build linux

package repository

import (
	"context"
	"github.com/nevzatcirak/harness-bridge/internal/security"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestSpecialStorageAndIgnoredConfig(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init")
	p, _ := security.New([]string{root}, []string{"ses_test"})
	config := filepath.Join(root, ".git", "config")
	os.Remove(config)
	if e := unix.Mkfifo(config, 0600); e != nil {
		t.Fatal(e)
	}
	// A FIFO config would stall Git; the metadata reader never opens it.
	if _, _, e := Inspect(context.Background(), root, p, "ses_test"); e != nil {
		t.Fatal(e)
	}
	head := filepath.Join(root, ".git", "HEAD")
	os.Remove(head)
	unix.Mkfifo(head, 0600)
	if _, _, e := Inspect(context.Background(), root, p, "ses_test"); e == nil {
		t.Fatal("FIFO HEAD accepted")
	}
	os.Remove(head)
	outside := filepath.Join(t.TempDir(), "HEAD")
	os.WriteFile(outside, []byte("ref: refs/heads/main\n"), 0600)
	os.Link(outside, head)
	if _, _, e := Inspect(context.Background(), root, p, "ses_test"); e == nil {
		t.Fatal("hardlink HEAD accepted")
	}
}

func TestConfinedHandleDuringComponentReplacement(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init")
	p, _ := security.New([]string{root}, []string{"ses_test"})
	handle, rel, e := p.OpenStorage("ses_test", filepath.Join(root, ".git", "HEAD"))
	if e != nil {
		t.Fatal(e)
	}
	defer handle.Close()
	gd := filepath.Join(root, ".git")
	moved := filepath.Join(root, "saved-git")
	if e := os.Rename(gd, moved); e != nil {
		t.Fatal(e)
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "HEAD"), []byte("ref: refs/heads/secret\n"), 0600)
	os.Symlink(outside, gd)
	// This open bypasses the earlier Lstat checks deliberately. os.Root, rather
	// than a lexical check or post-read validation, must block the escaped read.
	if _, _, e := readRegular(handle, rel, 4096); e == nil {
		t.Fatal("replacement escaped confined handle")
	}
	if _, _, e := Inspect(context.Background(), root, p, "ses_test"); e == nil {
		t.Fatal("replacement inspected")
	}
}
