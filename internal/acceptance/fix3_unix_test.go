//go:build unix

package acceptance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// R317-FIX-3 (#340 P2-1): the decoy filesystem never operates by path inside the run user's HOME.
// Everything runs in t.TempDir(); the fchown/race seams are the point where a swap would happen.

// realDir returns a temp directory whose path has no symlink component (the walk refuses them).
func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func newFS() *OSDecoyFS { return &OSDecoyFS{UID: os.Getuid(), GID: os.Getgid()} }

func inoOf(t *testing.T, p string) uint64 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return uint64(st.Ino)
}

func fdIno(fd int) uint64 {
	var st unix.Stat_t
	_ = unix.Fstat(fd, &st)
	return uint64(st.Ino)
}

func mkdirs(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

// swapForSymlink moves p away and puts a symlink to target in its place (what the run user can do).
func swapForSymlink(t *testing.T, p, target string) {
	t.Helper()
	if err := os.Rename(p, p+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// A swap between the create of a FILE and its chown: the chown acts on the open descriptor of the
// file that was created, never on the symlink target.
func TestFileCreateSwapChownsOnlyTheCreatedFile(t *testing.T) {
	root := realDir(t)
	victim := filepath.Join(realDir(t), "victim-root-file")
	if err := os.WriteFile(victim, []byte("v"), 0o600); err != nil {
		t.Fatal(err)
	}
	victimIno := inoOf(t, victim)
	f := filepath.Join(root, "x", "decoy-profile.json")
	var created, chowned uint64
	o := newFS()
	o.race = func(p string) {
		if p == f {
			created = inoOf(t, p)
			swapForSymlink(t, p, victim)
		}
	}
	o.fchown = func(fd, _, _ int) error {
		if chowned == 0 && created != 0 {
			chowned = fdIno(fd)
		}
		return nil
	}
	if err := o.Write(f, []byte("m")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if created == 0 || chowned != created || chowned == victimIno {
		t.Fatalf("chown reached inode %d; created %d, victim %d", chowned, created, victimIno)
	}
}

// The same for a DIRECTORY (Mkdir and an ancestor made by Write).
func TestDirCreateSwapChownsOnlyTheCreatedDir(t *testing.T) {
	root := realDir(t)
	victimDir := filepath.Join(realDir(t), "victim-dir")
	mkdirs(t, victimDir)
	victimIno := inoOf(t, victimDir)
	d := filepath.Join(root, "canary-control")
	var created, chowned uint64
	o := newFS()
	o.race = func(p string) {
		if p == d {
			created = inoOf(t, p)
			swapForSymlink(t, p, victimDir)
		}
	}
	o.fchown = func(fd, _, _ int) error { chowned = fdIno(fd); return nil }
	if err := o.Mkdir(d); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if created == 0 || chowned != created || chowned == victimIno {
		t.Fatalf("chown reached inode %d; created %d, victim %d", chowned, created, victimIno)
	}
	// an ancestor created by Write is protected the same way
	root2 := realDir(t)
	anc := filepath.Join(root2, "a")
	created, chowned = 0, 0
	o2 := newFS()
	o2.race = func(p string) {
		if p == anc {
			created = inoOf(t, p)
			swapForSymlink(t, p, victimDir)
		}
	}
	o2.fchown = func(fd, _, _ int) error {
		if chowned == 0 {
			chowned = fdIno(fd)
		}
		return nil
	}
	_ = o2.Write(filepath.Join(anc, "decoy-profile.json"), []byte("m"))
	if created == 0 || chowned != created {
		t.Fatalf("ancestor chown reached inode %d, created %d", chowned, created)
	}
}

// A symlinked component (an ancestor inside the run home, the run home itself, or the final name)
// is never followed.
func TestSymlinkedComponentsAreNeverFollowed(t *testing.T) {
	root := realDir(t)
	victimDir := filepath.Join(realDir(t), "victim-dir")
	mkdirs(t, filepath.Join(victimDir, "anthropic"))
	if err := os.Symlink(victimDir, filepath.Join(root, ".config")); err != nil {
		t.Fatal(err)
	}
	o := newFS()
	if err := o.Write(filepath.Join(root, ".config", "anthropic", "configs", "decoy-profile.json"), []byte("m")); err == nil {
		t.Fatal("Write succeeded through a symlinked ancestor")
	}
	if _, err := os.Stat(filepath.Join(victimDir, "anthropic", "configs")); err == nil {
		t.Fatal("directories were created through a symlinked ancestor")
	}
	// the run home itself is reached through a symlink
	real := filepath.Join(realDir(t), "home")
	mkdirs(t, real)
	link := filepath.Join(realDir(t), "homelink")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := newFS().Write(filepath.Join(link, "d", "decoy-profile.json"), []byte("m")); err == nil {
		t.Fatal("Write succeeded below a symlinked run home")
	}
	if _, err := os.Stat(filepath.Join(real, "d")); err == nil {
		t.Fatal("something was created below a symlinked run home")
	}
	// the final name is a pre-planted symlink: O_EXCL|O_NOFOLLOW refuses it, the target is untouched
	victim := filepath.Join(realDir(t), "victim-file")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "decoy-profile.json")); err != nil {
		t.Fatal(err)
	}
	if err := newFS().Write(filepath.Join(root, "decoy-profile.json"), []byte("m")); err == nil {
		t.Fatal("Write replaced a pre-planted symlink")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatal("the symlink target was written")
	}
}

// Remove and RemoveDir never follow a symlink swapped in for an ancestor after the create.
func TestRemoveNeverFollowsASwappedAncestor(t *testing.T) {
	root := realDir(t)
	o := newFS()
	f := filepath.Join(root, "d", "decoy-profile.json")
	if err := o.Write(f, []byte("m")); err != nil {
		t.Fatal(err)
	}
	victimDir := filepath.Join(realDir(t), "victim-dir")
	mkdirs(t, victimDir)
	victimFile := filepath.Join(victimDir, "decoy-profile.json")
	if err := os.WriteFile(victimFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapForSymlink(t, filepath.Join(root, "d"), victimDir)
	if err := o.Remove(f); err == nil {
		t.Fatal("Remove succeeded through a swapped ancestor")
	}
	if _, err := os.Stat(victimFile); err != nil {
		t.Fatal("Remove deleted a file outside the created tree")
	}
}

func TestRemoveDirNeverFollowsASwappedAncestor(t *testing.T) {
	root := realDir(t)
	o := newFS()
	if err := o.Mkdir(filepath.Join(root, "p")); err != nil {
		t.Fatal(err)
	}
	if err := o.Mkdir(filepath.Join(root, "p", "canary-control")); err != nil {
		t.Fatal(err)
	}
	victimDir := filepath.Join(realDir(t), "victim-dir")
	mkdirs(t, filepath.Join(victimDir, "canary-control"))
	swapForSymlink(t, filepath.Join(root, "p"), victimDir)
	if err := o.RemoveDir(filepath.Join(root, "p")); err == nil {
		t.Fatal("RemoveDir reported success through a swapped ancestor")
	}
	if _, err := os.Stat(filepath.Join(victimDir, "canary-control")); err != nil {
		t.Fatal("RemoveDir removed a directory outside the created tree")
	}
}

// Remove re-checks the entry (device and inode) before unlinking: a different file under the same
// name is left alone, as is a different directory under a created directory's name.
func TestRemoveRefusesAReplacedEntry(t *testing.T) {
	root := realDir(t)
	o := newFS()
	f := filepath.Join(root, "decoy-profile.json")
	if err := o.Write(f, []byte("m")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f); err != nil { // the run user replaces it with a file of their own
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := o.Remove(f); err == nil {
		t.Fatal("Remove deleted a file it did not create")
	}
	if b, _ := os.ReadFile(f); string(b) != "theirs" {
		t.Fatal("the replacing file was removed")
	}
	d := filepath.Join(root, "canary-control")
	if err := o.Mkdir(d); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(d); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, d)
	if err := o.RemoveDir(d); err == nil {
		t.Fatal("RemoveDir removed a directory it did not create")
	}
	if _, err := os.Stat(d); err != nil {
		t.Fatal("the replacing directory was removed")
	}
	if err := o.Remove(filepath.Join(root, "never-created.json")); err == nil {
		t.Fatal("Remove accepted a file it never created")
	}
}

// Write never overwrites: an existing file makes it fail and stays as it was.
func TestWriteNeverOverwritesAnExistingFile(t *testing.T) {
	root := realDir(t)
	f := filepath.Join(root, "decoy-profile.json")
	if err := os.WriteFile(f, []byte("theirs"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newFS().Write(f, []byte("m")); err == nil {
		t.Fatal("Write replaced an existing file")
	}
	if b, _ := os.ReadFile(f); string(b) != "theirs" {
		t.Fatal("the existing file changed")
	}
}

// R317-FIX-2 P3-3 (now on descriptors): a chown failure leaves nothing behind and nothing recorded.
func TestMkdirChownFailureLeavesNothingBehind(t *testing.T) {
	root := realDir(t)
	fail := func(int, int, int) error { return errors.New("operation not permitted") }
	o := newFS()
	o.fchown = fail
	dir := filepath.Join(root, "canary-control")
	if err := o.Mkdir(dir); err == nil {
		t.Fatal("Mkdir succeeded although the chown failed")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the directory was left behind after a failed chown")
	}
	o.fchown = nil // a later (privileged) run must be able to create it again
	if err := o.Mkdir(dir); err != nil {
		t.Fatalf("second Mkdir: %v", err)
	}
	_ = o.RemoveDir(dir)
	o2 := newFS()
	o2.fchown = fail
	if err := o2.Write(filepath.Join(root, "a", "b", "decoy-profile.json"), []byte("x")); err == nil {
		t.Fatal("Write succeeded although the chown failed")
	}
	if _, err := os.Stat(filepath.Join(root, "a")); err == nil {
		t.Fatal("a created ancestor was left behind after a failed chown")
	}
}

func TestWriteFileChownFailureRemovesTheFile(t *testing.T) {
	root := realDir(t)
	o := newFS()
	o.fchown = func(fd, _, _ int) error {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err == nil && !isDirMode(uint32(st.Mode)) {
			return errors.New("operation not permitted")
		}
		return nil
	}
	f := filepath.Join(root, "x", "decoy-profile.json")
	if err := o.Write(f, []byte("x")); err == nil {
		t.Fatal("Write succeeded although the file chown failed")
	}
	if _, err := os.Stat(f); err == nil {
		t.Fatal("a decoy file with the wrong owner was left behind")
	}
	if err := o.RemoveDir(filepath.Join(root, "x")); err != nil {
		t.Fatalf("RemoveDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Fatal("the created directory was not removed")
	}
}

// The driver only ever wrote under the paths it was given, with mode 0600 files.
func TestCreatedFileModeAndPlacement(t *testing.T) {
	root := realDir(t)
	o := newFS()
	f := filepath.Join(root, "a", "decoy-profile.json")
	if err := o.Write(f, []byte("m")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(f)
	if err != nil || fi.Mode().Perm() != 0o600 || !fi.Mode().IsRegular() {
		t.Fatalf("mode %v err %v", fi, err)
	}
	if b, _ := os.ReadFile(f); !strings.Contains(string(b), "m") {
		t.Fatal("content")
	}
	if err := o.Remove(f); err != nil {
		t.Fatal(err)
	}
	if err := o.RemoveDir(filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(root); len(es) != 0 {
		t.Fatalf("left behind: %v", es)
	}
}
