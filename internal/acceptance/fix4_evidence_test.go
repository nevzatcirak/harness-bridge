//go:build unix

package acceptance

import (
	"os"
	"path/filepath"
	"testing"
)

// R317-CLEANUP (#346 P3-3): the evidence file is created without following a symlink in ANY
// component of its path, and never over an existing file.
func TestEvidenceFileIsCreatedWithoutFollowingSymlinks(t *testing.T) {
	root := realDir(t)
	good := filepath.Join(root, "evidence.txt")
	f, err := CreateEvidenceFile(good)
	if err != nil {
		t.Fatalf("plain create: %v", err)
	}
	_, _ = f.WriteString("verdict\n")
	_ = f.Close()
	fi, err := os.Lstat(good)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
		t.Fatalf("evidence file: %v %v", fi, err)
	}
	if f2, err := CreateEvidenceFile(good); err == nil {
		_ = f2.Close()
		t.Fatal("an existing evidence file was reused")
	}
	victimDir := filepath.Join(realDir(t), "victim-dir")
	mkdirs(t, victimDir)
	link := filepath.Join(root, "linkdir")
	if err := os.Symlink(victimDir, link); err != nil {
		t.Fatal(err)
	}
	if f3, err := CreateEvidenceFile(filepath.Join(link, "evidence.txt")); err == nil {
		_ = f3.Close()
		t.Fatal("the evidence file was created through a symlinked ancestor")
	}
	if _, err := os.Stat(filepath.Join(victimDir, "evidence.txt")); err == nil {
		t.Fatal("a file appeared in the symlink target")
	}
	victim := filepath.Join(realDir(t), "victim-file")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "final.txt")); err != nil {
		t.Fatal(err)
	}
	if f4, err := CreateEvidenceFile(filepath.Join(root, "final.txt")); err == nil {
		_ = f4.Close()
		t.Fatal("the evidence file followed a final-component symlink")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatal("the symlink target was written")
	}
	if f5, err := CreateEvidenceFile("relative.txt"); err == nil {
		_ = f5.Close()
		t.Fatal("a relative path was accepted")
	}
}
