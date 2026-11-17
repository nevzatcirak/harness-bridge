package overlay

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// uncertainMarker reports the internal uncertain-write marker (ADR-025 4.3), which only a rename that may
// have happened leaves set.
func uncertainMarker(s *Store) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.uncertain
}

// ADR-022 S7, #318 P3-3: a failure of the rename step (the Hook("rename") seam) changes nothing at
// all: not the published snapshot, not its counters (Entries, Generation), not the file on disk.
// TestAtomicWriteFaultAtEachStep already checks the in-memory Archived answer and the state after a
// restart; this test pins the counters and the exact file bytes, so a snapshot published before the
// rename fails here with a message naming what changed.
func TestRenameHookErrorLeavesEverythingUnchanged(t *testing.T) {
	dir := newDir(t)
	s := open(t, dir, true)
	if _, _, err := s.Archive(bg, sid(1), "op"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	genBefore, entriesBefore := s.Generation(), s.Entries()

	s.Hook = func(step string) error {
		if step == "rename" {
			return fmt.Errorf("injected rename failure")
		}
		return nil
	}
	if _, archived, err := s.Archive(bg, sid(2), "op"); KindOf(err) != KindUnavailable || archived {
		t.Fatalf("Archive with a failing rename: archived=%v kind=%v err=%v, want KindUnavailable and archived=false", archived, KindOf(err), err)
	}
	if ok, _ := s.Archived(bg, sid(2)); ok {
		t.Fatal("rename failed but the snapshot already shows the new entry: published before the rename")
	}
	if g := s.Generation(); g != genBefore {
		t.Fatalf("rename failed but Generation moved %d -> %d", genBefore, g)
	}
	if n := s.Entries(); n != entriesBefore {
		t.Fatalf("rename failed but Entries moved %d -> %d", entriesBefore, n)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rename failed but overlay.json changed on disk")
	}
	noTmp(t, dir)
	// The rename step marks the write uncertain before the rename is issued and clears the marker when the
	// rename fails ("nothing changed"): a left-over marker would make the next change re-read the file.
	if uncertainMarker(s) {
		t.Fatal("rename failed but the uncertain-write marker is still set: the failure would be treated as an uncertain write")
	}

	// Unarchive takes the same step: its rename failure changes nothing either.
	if _, err := s.Unarchive(bg, sid(1)); KindOf(err) != KindUnavailable {
		t.Fatalf("Unarchive with a failing rename: %v, want KindUnavailable", err)
	}
	if ok, _ := s.Archived(bg, sid(1)); !ok || s.Generation() != genBefore || s.Entries() != entriesBefore {
		t.Fatalf("a failed Unarchive changed the snapshot: archived=%v generation=%d entries=%d", ok, s.Generation(), s.Entries())
	}

	if uncertainMarker(s) {
		t.Fatal("a failed Unarchive left the uncertain-write marker set")
	}

	// Nothing visible is latched (the marker is clear, checked above, and no error state is set): with the
	// fault gone the same change commits and moves the counters by one.
	s.Hook = nil
	if _, archived, err := s.Archive(bg, sid(2), "op"); err != nil || !archived {
		t.Fatalf("Archive after the fault cleared: archived=%v err=%v", archived, err)
	}
	if s.Generation() != genBefore+1 || s.Entries() != entriesBefore+1 {
		t.Fatalf("after the retry generation=%d entries=%d, want %d and %d", s.Generation(), s.Entries(), genBefore+1, entriesBefore+1)
	}
}

// ADR-022 S7, #312 P3-1: after Close every change is refused, including a change that would have
// been an idempotent no-op. Archive of an ID that is already archived and Unarchive of an ID that is
// absent must return KindUnavailable, never (changed=false, err=nil): a closed store cannot answer
// "nothing to do" for a state it will never be asked to change.
func TestClosedStoreRefusesNoOpChangesToo(t *testing.T) {
	s := open(t, newDir(t), true)
	if _, _, err := s.Archive(bg, sid(1), "op"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if e, archived, err := s.Archive(bg, sid(1), "op"); KindOf(err) != KindUnavailable || archived || e.SessionID != "" {
		t.Fatalf("Archive of an already archived ID after Close: entry=%v archived=%v err=%v kind=%v, want KindUnavailable (not a no-op success)", e, archived, err, KindOf(err))
	}
	if removed, err := s.Unarchive(bg, sid(99)); KindOf(err) != KindUnavailable || removed {
		t.Fatalf("Unarchive of an absent ID after Close: removed=%v err=%v kind=%v, want KindUnavailable (not a no-op success)", removed, err, KindOf(err))
	}
	// The published snapshot stays readable (the last good one) and is unchanged.
	if ok, err := s.Archived(bg, sid(1)); !ok || err != nil {
		t.Fatalf("the snapshot must stay readable after Close: %v %v", ok, err)
	}
	if s.Entries() != 1 {
		t.Fatalf("Entries = %d after Close, want 1", s.Entries())
	}
}

// ADR-022 S7, #363 P3-1: the REAL root.Rename failure branch of commit (not the Hook("rename") seam).
// overlay.json is a directory, so renaming the temporary file onto it fails with EISDIR without any
// privilege. The branch must leave the published snapshot, its counters and the directory unchanged,
// remove its temporary file and clear the uncertain-write marker that was set before the rename was
// issued; a left-over marker would make the failure look like an uncertain write.
func TestRealRenameFailureClearsTheUncertainMarkerAndChangesNothing(t *testing.T) {
	dir := newDir(t)
	s := open(t, dir, true) // no overlay.json yet: the store starts empty
	blocker := filepath.Join(dir, FileName)
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	genBefore, entriesBefore := s.Generation(), s.Entries()

	if _, archived, err := s.Archive(bg, sid(1), "op"); KindOf(err) != KindUnavailable || archived {
		t.Fatalf("Archive with a real rename failure: archived=%v kind=%v err=%v, want KindUnavailable and archived=false", archived, KindOf(err), err)
	}
	if uncertainMarker(s) {
		t.Fatal("the real rename failed but the uncertain-write marker is still set: the failure would be treated as an uncertain write")
	}
	if ok, _ := s.Archived(bg, sid(1)); ok {
		t.Fatal("the real rename failed but the snapshot shows the new entry")
	}
	if s.Generation() != genBefore || s.Entries() != entriesBefore {
		t.Fatalf("the real rename failed but the counters moved: generation %d -> %d, entries %d -> %d", genBefore, s.Generation(), entriesBefore, s.Entries())
	}
	noTmp(t, dir)
	if fi, err := os.Lstat(blocker); err != nil || !fi.IsDir() {
		t.Fatalf("the blocking directory was touched: %v %v", fi, err)
	}

	// Nothing is latched: with the obstacle gone the same change commits and moves the counters by one.
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if _, archived, err := s.Archive(bg, sid(1), "op"); err != nil || !archived {
		t.Fatalf("Archive after the obstacle was removed: archived=%v err=%v", archived, err)
	}
	if s.Generation() != genBefore+1 || s.Entries() != entriesBefore+1 {
		t.Fatalf("after the retry generation=%d entries=%d, want %d and %d", s.Generation(), s.Entries(), genBefore+1, entriesBefore+1)
	}
}
