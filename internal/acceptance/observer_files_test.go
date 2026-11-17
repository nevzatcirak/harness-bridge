//go:build unix

package acceptance

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// ADR021-OBSERVER (#372): the file-canary scan (ADR-021 §8.3: "no canary file exists" after the denied
// run). The hostile root's hook, apiKeyHelper and .mcp.json server would write into one canary
// directory; the scan counts what is there, without following a symlink, and only ever reads that
// directory (never the run user's ~/.claude, ~/.config/anthropic or a credential file).

func scanDirs(t *testing.T) (home, dir string) {
	t.Helper()
	home = filepath.Join(realDir(t), "home")
	mkdirs(t, home)
	dir = filepath.Join(realDir(t), "canaries")
	mkdirs(t, dir)
	return home, dir
}

func TestFileCanaryScanCountsWhatAppears(t *testing.T) {
	home, dir := scanDirs(t)
	sc, err := NewFileCanaryScan(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := sc.Count(); n != 0 || !ok {
		t.Fatalf("clean: %d %v", n, ok)
	}
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(dir, "c"+strconv.Itoa(i)), []byte("hook ran"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if n, ok := sc.Count(); n != 3 || !ok {
		t.Fatalf("three files: %d %v", n, ok)
	}
	// a directory and a symlink are canaries too (anything the hostile root can create counts)
	mkdirs(t, filepath.Join(dir, "sub"))
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if n, ok := sc.Count(); n != 5 || !ok {
		t.Fatalf("file, directory and symlink entries: %d %v", n, ok)
	}
}

func TestFileCanaryScanNeedsAnEmptyBaselineAndAGoodPath(t *testing.T) {
	home, dir := scanDirs(t)
	if err := os.WriteFile(filepath.Join(dir, "left-over"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileCanaryScan(dir, home); err == nil {
		t.Fatal("a non-empty canary directory was accepted as the baseline")
	}
	_, clean := scanDirs(t)
	for name, d := range map[string]string{
		"relative":           "canaries",
		"missing":            filepath.Join(clean, "nope"),
		"the run home":       home,
		"inside .claude":     filepath.Join(home, ".claude", "x"),
		".claude itself":     filepath.Join(home, ".claude"),
		"anthropic dir":      filepath.Join(home, ".config", "anthropic"),
		"under anthropic":    filepath.Join(home, ".config", "anthropic", "configs"),
		"parent of a denied": filepath.Join(home, ".config"),
		"root":               "/",
	} {
		if _, err := NewFileCanaryScan(d, home); err == nil {
			t.Errorf("accepted the canary directory %q (%s)", d, name)
		}
	}
	// the path of a denied directory is refused even when it exists
	mkdirs(t, filepath.Join(home, ".claude"))
	if _, err := NewFileCanaryScan(filepath.Join(home, ".claude"), home); err == nil {
		t.Error("an existing denied directory was accepted")
	}
	if _, err := NewFileCanaryScan(clean, ""); err == nil {
		t.Error("an empty run home was accepted")
	}
}

// A symlink swapped in for the canary directory or one of its ancestors is never followed: the scan
// becomes unobserved instead of counting somewhere else.
func TestFileCanaryScanNeverFollowsASwappedPath(t *testing.T) {
	home, dir := scanDirs(t)
	sc, err := NewFileCanaryScan(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(realDir(t), "other")
	mkdirs(t, other) // empty: following it would report a clean 0
	swapForSymlink(t, dir, other)
	if n, ok := sc.Count(); ok {
		t.Fatalf("the scan followed a symlink swapped in for the directory: %d %v", n, ok)
	}

	home2, dir2 := scanDirs(t)
	sc2, err := NewFileCanaryScan(dir2, home2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "real-canary"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	swapForSymlink(t, filepath.Dir(dir2), other) // an ancestor
	if n, ok := sc2.Count(); ok {
		t.Fatalf("the scan followed a symlinked ancestor: %d %v", n, ok)
	}
}

// The scan is bounded: a flood of entries saturates the count instead of walking them all.
func TestFileCanaryScanIsBounded(t *testing.T) {
	home, dir := scanDirs(t)
	sc, err := NewFileCanaryScan(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxCanaryEntries+50; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f"+strconv.Itoa(i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if n, ok := sc.Count(); n != maxCanaryEntries || !ok {
		t.Fatalf("flood: %d %v, want a saturated %d", n, ok, maxCanaryEntries)
	}
}

// A directory that disappears (or cannot be read) is unobserved, never a clean zero.
func TestFileCanaryScanIsUnobservedWhenTheDirectoryIsGone(t *testing.T) {
	home, dir := scanDirs(t)
	sc, err := NewFileCanaryScan(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if n, ok := sc.Count(); ok {
		t.Fatalf("a missing directory reported an observation: %d %v", n, ok)
	}
}
