package overlay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func sid(n int) string { return fmt.Sprintf("opencode:ses_%026d", n) }

func newDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "ov")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func open(t *testing.T, dir string, writer bool) *Store {
	t.Helper()
	s, err := Open(dir, Options{Writer: writer})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func fileBytes(sn *snapshot) []byte {
	out := fileOut{canonBody: sn.body()}
	out.Sum = checksum(out.canonBody)
	b, _ := json.Marshal(out)
	return b
}

func snapOf(gen uint64, ids ...string) *snapshot {
	sn := &snapshot{generation: gen, entries: map[string]Entry{}}
	for _, id := range ids {
		sn.entries[id] = Entry{SessionID: id, ArchivedAt: time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC), Principal: "op"}
	}
	return sn
}

func putFile(t *testing.T, dir string, data []byte, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, FileName)
	_ = os.Remove(p)
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func noTmp(t *testing.T, dir string) {
	t.Helper()
	for _, n := range listDir(t, dir) {
		if strings.HasPrefix(n, tmpPrefix) {
			t.Fatalf("leftover tmp file %s", n)
		}
	}
}

var bg = context.Background()

// Test 1: fault injection at each step; after a simulated crash the loaded state is exactly the
// old or exactly the new snapshot.
func TestAtomicWriteFaultAtEachStep(t *testing.T) {
	type c struct {
		step string
		kind Kind
		want bool // the new entry is on disk after the fault
	}
	for _, tc := range []c{
		{"open_tmp", KindUnavailable, false}, {"write", KindUnavailable, false}, {"fsync_file", KindUnavailable, false},
		{"rename", KindUnavailable, false}, {"fsync_dir", KindUncertain, true}, {"before_publish", KindUncertain, true},
	} {
		t.Run(tc.step, func(t *testing.T) {
			dir := newDir(t)
			s := open(t, dir, true)
			if _, _, err := s.Archive(bg, sid(1), "op"); err != nil {
				t.Fatal(err)
			}
			s.Hook = func(step string) error {
				if step == tc.step {
					return fmt.Errorf("injected")
				}
				return nil
			}
			_, _, err := s.Archive(bg, sid(2), "op")
			if KindOf(err) != tc.kind {
				t.Fatalf("kind = %v (%v), want %v", KindOf(err), err, tc.kind)
			}
			if ok, _ := s.Archived(bg, sid(2)); ok {
				t.Fatal("a failed write changed the in-memory snapshot")
			}
			s.Close()
			again := open(t, dir, true)
			got, _ := again.Archived(bg, sid(2))
			if got != tc.want {
				t.Fatalf("after restart archived(2) = %v, want %v", got, tc.want)
			}
			if ok, _ := again.Archived(bg, sid(1)); !ok {
				t.Fatal("the old entry was lost")
			}
			noTmp(t, dir)
		})
	}
}

// Test 2: every invalid-file reason is the unknown state, never the empty overlay.
func TestCorruptionTableIsUnknownNeverEmpty(t *testing.T) {
	good := fileBytes(snapOf(3, sid(1), sid(2)))
	mutate := func(f func(m map[string]any)) []byte {
		var m map[string]any
		_ = json.Unmarshal(good, &m)
		f(m)
		b, _ := json.Marshal(m)
		return b
	}
	var many []string
	for i := 0; i <= MaxEntries; i++ {
		many = append(many, sid(i))
	}
	cases := map[string]struct {
		data []byte
		mode os.FileMode
	}{
		"not json":        {[]byte("{"), 0o600},
		"trailing data":   {append(append([]byte{}, good...), []byte(" {}")...), 0o600},
		"unknown key":     {mutate(func(m map[string]any) { m["extra"] = 1 }), 0o600},
		"missing sum":     {mutate(func(m map[string]any) { delete(m, "sum") }), 0o600},
		"null entries":    {mutate(func(m map[string]any) { m["entries"] = nil }), 0o600},
		"bad version":     {mutate(func(m map[string]any) { m["v"] = 2 }), 0o600},
		"bad sum":         {mutate(func(m map[string]any) { m["sum"] = strings.Repeat("0", 64) }), 0o600},
		"changed value":   {mutate(func(m map[string]any) { m["generation"] = 9 }), 0o600},
		"claudecode id":   {fileBytes(&snapshot{generation: 1, entries: map[string]Entry{"claudecode:" + strings.Repeat("a", 36): {SessionID: "claudecode:" + strings.Repeat("a", 36), ArchivedAt: time.Now().UTC().Truncate(time.Second), Principal: "op"}}}), 0o600},
		"over entries":    {fileBytes(snapOf(1, many...)), 0o600},
		"wrong mode":      {good, 0o644},
		"over size":       {append(append([]byte{}, good...), bytes.Repeat([]byte(" "), MaxFileBytes)...), 0o600},
		"bad principal":   {bytes.ReplaceAll(good, []byte(`"op"`), []byte(`"o p"`)), 0o600},
		"bad archived_at": {bytes.ReplaceAll(good, []byte("2026-11-16T01:02:03Z"), []byte("yesterday")), 0o600},
	}
	var dup []byte
	{
		var m map[string]any
		_ = json.Unmarshal(good, &m)
		es := m["entries"].([]any)
		m["entries"] = []any{es[0], es[0]}
		dup, _ = json.Marshal(m)
	}
	cases["duplicate id"] = struct {
		data []byte
		mode os.FileMode
	}{dup, 0o600}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := newDir(t)
			putFile(t, dir, tc.data, tc.mode)
			if _, err := Open(dir, Options{Writer: true}); err == nil {
				t.Fatal("the writer started on an invalid file (Q3)")
			}
			r := open(t, dir, false)
			if r.Err() == nil {
				t.Fatal("an invalid file is not the unknown state")
			}
			ok, err := r.Archived(bg, sid(1))
			if err == nil || ok || KindOf(err) != KindUnavailable {
				t.Fatalf("Archived = (%v,%v), want the unavailable error", ok, err)
			}
		})
	}
	t.Run("sanity: the good file loads", func(t *testing.T) {
		dir := newDir(t)
		putFile(t, dir, good, 0o600)
		s := open(t, dir, true)
		if ok, err := s.Archived(bg, sid(2)); !ok || err != nil || s.Generation() != 3 {
			t.Fatalf("good file: %v %v gen=%d", ok, err, s.Generation())
		}
	})
}

func TestFileKindsAreRefused(t *testing.T) {
	good := fileBytes(snapOf(1, sid(1)))
	t.Run("symlink file", func(t *testing.T) {
		dir := newDir(t)
		other := filepath.Join(t.TempDir(), "elsewhere")
		_ = os.WriteFile(other, good, 0o600)
		_ = os.Symlink(other, filepath.Join(dir, FileName))
		if _, err := Open(dir, Options{Writer: true}); err == nil {
			t.Fatal("symlinked file accepted")
		}
	})
	t.Run("symlink to a file inside the directory", func(t *testing.T) {
		dir := newDir(t)
		if err := os.WriteFile(filepath.Join(dir, "real.json"), good, 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Symlink("real.json", filepath.Join(dir, FileName))
		if _, err := Open(dir, Options{Writer: true}); err == nil {
			t.Fatal("an in-directory symlink was accepted")
		}
	})
	t.Run("hard link", func(t *testing.T) {
		dir := newDir(t)
		putFile(t, dir, good, 0o600)
		if err := os.Link(filepath.Join(dir, FileName), filepath.Join(t.TempDir(), "link")); err != nil {
			t.Skip("hard links unavailable")
		}
		if _, err := Open(dir, Options{Writer: true}); err == nil {
			t.Fatal("hard-linked file accepted")
		}
	})
	t.Run("directory in place of the file", func(t *testing.T) {
		dir := newDir(t)
		_ = os.Mkdir(filepath.Join(dir, FileName), 0o700)
		if _, err := Open(dir, Options{Writer: true}); err == nil {
			t.Fatal("non-regular file accepted")
		}
	})
}

// Test 3: directory rules and leftover removal by exact name only.
func TestDirectoryRules(t *testing.T) {
	t.Run("mode", func(t *testing.T) {
		d := newDir(t)
		_ = os.Chmod(d, 0o755)
		if _, err := Open(d, Options{Writer: true}); err == nil {
			t.Fatal("0755 accepted")
		}
	})
	t.Run("symlink final component", func(t *testing.T) {
		d := newDir(t)
		l := d + "-link"
		_ = os.Symlink(d, l)
		if _, err := Open(l, Options{Writer: true}); err == nil {
			t.Fatal("symlinked directory accepted")
		}
	})
	t.Run("relative and missing and file", func(t *testing.T) {
		for _, p := range []string{"relative/dir", filepath.Join(t.TempDir(), "missing")} {
			if _, err := Open(p, Options{Writer: true}); err == nil {
				t.Fatalf("%q accepted", p)
			}
		}
		f := filepath.Join(t.TempDir(), "f")
		_ = os.WriteFile(f, nil, 0o600)
		if _, err := Open(f, Options{Writer: true}); err == nil {
			t.Fatal("a file accepted as the directory")
		}
	})
	t.Run("leftover removal is exact", func(t *testing.T) {
		d := newDir(t)
		for _, n := range []string{"overlay.json.tmp-0123456789abcdef", "overlay.json.tmp-xyz", "overlay.json.tmp-0123456789abcdeF", "notes.txt"} {
			_ = os.WriteFile(filepath.Join(d, n), nil, 0o600)
		}
		open(t, d, true)
		got := listDir(t, d)
		want := []string{"notes.txt", "overlay.json.tmp-0123456789abcdeF", "overlay.json.tmp-xyz", LockName}
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("dir = %v, want %v", got, want)
		}
	})
	t.Run("a reader removes nothing", func(t *testing.T) {
		d := newDir(t)
		_ = os.WriteFile(filepath.Join(d, "overlay.json.tmp-0123456789abcdef"), nil, 0o600)
		open(t, d, false)
		if len(listDir(t, d)) != 1 {
			t.Fatal("a read-only process changed the directory")
		}
	})
}

// The path-based os functions are never used for the overlay files (ADR-025 test 3, mutation).
func TestNoPathBasedFileOperations(t *testing.T) {
	banned := map[string]bool{"Rename": true, "Remove": true, "RemoveAll": true, "OpenFile": true, "Create": true, "WriteFile": true, "ReadFile": true, "Open": true, "Mkdir": true, "MkdirAll": true, "Truncate": true}
	allowed := map[string]bool{"Lstat": true}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "os" && banned[sel.Sel.Name] && !allowed[sel.Sel.Name] {
					t.Errorf("%s: path-based os.%s", fset.Position(sel.Pos()), sel.Sel.Name)
				}
				return true
			})
			_ = name
		}
	}
}

// Test 7: idempotence and bounds; nothing is dropped silently.
func TestIdempotenceAndBounds(t *testing.T) {
	t.Run("idempotent", func(t *testing.T) {
		s := open(t, newDir(t), true)
		e1, ok1, err := s.Archive(bg, sid(1), "op")
		if err != nil || !ok1 {
			t.Fatal(ok1, err)
		}
		gen := s.Generation()
		e2, ok2, err := s.Archive(bg, sid(1), "other")
		if err != nil || ok2 || !e2.ArchivedAt.Equal(e1.ArchivedAt) || e2.Principal != "op" || s.Generation() != gen {
			t.Fatalf("second archive: %+v %v %v gen %d->%d", e2, ok2, err, gen, s.Generation())
		}
		if removed, err := s.Unarchive(bg, sid(9)); err != nil || removed {
			t.Fatalf("unarchive of an absent entry: %v %v", removed, err)
		}
	})
	t.Run("entry bound", func(t *testing.T) {
		dir := newDir(t)
		var ids []string
		for i := 0; i < MaxEntries; i++ {
			ids = append(ids, sid(i))
		}
		putFile(t, dir, fileBytes(snapOf(5, ids...)), 0o600)
		s := open(t, dir, true)
		if _, _, err := s.Archive(bg, sid(5000), "op"); KindOf(err) != KindLimit {
			t.Fatalf("at the bound: %v, want SESSION_LIMIT", err)
		}
		if s.Entries() != MaxEntries || s.Generation() != 5 {
			t.Fatal("a refused archive changed the overlay")
		}
		if removed, err := s.Unarchive(bg, sid(0)); err != nil || !removed {
			t.Fatal(removed, err)
		}
		if _, ok, err := s.Archive(bg, sid(5000), "op"); err != nil || !ok {
			t.Fatalf("after un-archive freed one: %v %v", ok, err)
		}
	})
	t.Run("size bound is separate", func(t *testing.T) {
		old := fileBound
		t.Cleanup(func() { fileBound = old })
		s := open(t, newDir(t), true)
		if _, _, err := s.Archive(bg, sid(1), "op"); err != nil {
			t.Fatal(err)
		}
		fileBound = 100 // below one more entry
		if _, _, err := s.Archive(bg, sid(2), "op"); KindOf(err) != KindLimit {
			t.Fatalf("size bound: %v, want SESSION_LIMIT", err)
		}
		if s.Entries() != 1 {
			t.Fatal("an entry was dropped or added at the size bound")
		}
	})
	t.Run("claudecode and malformed ids never become entries", func(t *testing.T) {
		s := open(t, newDir(t), true)
		for _, id := range []string{"claudecode:" + strings.Repeat("a", 36), "ses_x", "opencode:bad", ""} {
			if _, _, err := s.Archive(bg, id, "op"); KindOf(err) != KindInvalidID {
				t.Fatalf("%q: %v", id, err)
			}
		}
	})
}

// Test 8: two processes.
func TestSecondWriterHasNoWriteRole(t *testing.T) {
	dir := newDir(t)
	a := open(t, dir, true)
	if !a.CanWrite() {
		t.Fatal("the first writer has no lock")
	}
	if _, _, err := a.Archive(bg, sid(1), "op"); err != nil {
		t.Fatal(err)
	}
	b := open(t, dir, true)
	if b.CanWrite() {
		t.Fatal("the second flock succeeded")
	}
	if _, _, err := b.Archive(bg, sid(2), "op"); KindOf(err) != KindReadOnly {
		t.Fatalf("second process archive: %v", err)
	}
	if ok, err := b.Archived(bg, sid(1)); !ok || err != nil {
		t.Fatalf("the second process cannot read: %v %v", ok, err)
	}
	if _, err := b.Unarchive(bg, sid(1)); KindOf(err) != KindReadOnly {
		t.Fatalf("second process unarchive: %v", err)
	}
	a.Close()
	if c := open(t, dir, true); !c.CanWrite() {
		t.Fatal("the lock is not released with the process")
	}
	if _, err := os.Stat(filepath.Join(dir, LockName)); err != nil {
		t.Fatal("the lock file was unlinked")
	}
}

// Test 9: a restart restores the labels and nothing else.
func TestRestartRestoresLabelsOnly(t *testing.T) {
	dir := newDir(t)
	s := open(t, dir, true)
	for i := 1; i <= 3; i++ {
		if _, _, err := s.Archive(bg, sid(i), "op"); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = s.Unarchive(bg, sid(2))
	gen := s.Generation()
	s.Close()
	r := open(t, dir, true)
	for id, want := range map[string]bool{sid(1): true, sid(2): false, sid(3): true} {
		if got, _ := r.Archived(bg, id); got != want {
			t.Fatalf("%s archived=%v want %v", id, got, want)
		}
	}
	if r.Generation() != gen || r.Entries() != 2 {
		t.Fatalf("restored gen=%d entries=%d", r.Generation(), r.Entries())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	if len(m) != 4 {
		t.Fatalf("file keys = %d, want exactly v, generation, entries, sum", len(m))
	}
	if raw[len(raw)-1] != '\n' {
		t.Fatal("file does not end in a newline")
	}
}

// Test 12: concurrent distinct archives lose nothing, with both delays.
func TestConcurrentChangesLoseNothing(t *testing.T) {
	for _, delayStep := range []string{"write", "before_publish"} {
		t.Run(delayStep, func(t *testing.T) {
			dir := newDir(t)
			s := open(t, dir, true)
			s.Hook = func(step string) error {
				if step == delayStep {
					time.Sleep(3 * time.Millisecond)
				}
				return nil
			}
			const n = 20
			var wg sync.WaitGroup
			errs := make(chan error, n)
			for i := 1; i <= n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, _, err := s.Archive(bg, sid(i), "op"); err != nil {
						errs <- err
					}
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Fatal(err)
			}
			if s.Entries() != n || s.Generation() != n {
				t.Fatalf("snapshot entries=%d generation=%d, want %d", s.Entries(), s.Generation(), n)
			}
			s.Close()
			r := open(t, dir, false)
			if r.Entries() != n || r.Generation() != n {
				t.Fatalf("file entries=%d generation=%d, want %d", r.Entries(), r.Generation(), n)
			}
			// distinct archives and un-archives interleaved
			w := open(t, dir, true)
			var wg2 sync.WaitGroup
			for i := 1; i <= n; i += 2 {
				wg2.Add(1)
				go func() { defer wg2.Done(); _, _ = w.Unarchive(bg, sid(i)) }()
			}
			wg2.Wait()
			if w.Entries() != n/2 {
				t.Fatalf("after interleaved un-archives: %d entries, want %d", w.Entries(), n/2)
			}
		})
	}
}

// Test 13: uncertain and hung writes.
func TestUncertainDirFsyncThenAdopt(t *testing.T) {
	dir := newDir(t)
	s := open(t, dir, true)
	s.Hook = func(step string) error {
		if step == "fsync_dir" {
			return fmt.Errorf("EIO")
		}
		return nil
	}
	if _, _, err := s.Archive(bg, sid(1), "op"); KindOf(err) != KindUncertain {
		t.Fatalf("got %v, want the uncertain result", err)
	}
	if ok, _ := s.Archived(bg, sid(1)); ok {
		t.Fatal("the memory snapshot was replaced on an uncertain write")
	}
	s.Hook = nil
	if _, _, err := s.Archive(bg, sid(2), "op"); err != nil {
		t.Fatalf("the next change must adopt and proceed: %v", err)
	}
	for _, id := range []string{sid(1), sid(2)} {
		if ok, _ := s.Archived(bg, id); !ok {
			t.Fatalf("%s missing after adoption", id)
		}
	}
	if s.Generation() != 2 {
		t.Fatalf("generation %d, want 2", s.Generation())
	}
}

func TestUncertainProceedsWhenTheFileIsStillPrevious(t *testing.T) {
	s := open(t, newDir(t), true)
	s.markUncertain(0, 1) // as if a rename was in flight that never landed
	if _, _, err := s.Archive(bg, sid(1), "op"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Archived(bg, sid(1)); !ok || s.Generation() != 1 {
		t.Fatal("the change did not proceed from the previous generation")
	}
}

func TestUncertainThenAnotherGenerationLatchesUnknown(t *testing.T) {
	dir := newDir(t)
	s := open(t, dir, true)
	s.Hook = func(step string) error {
		if step == "fsync_dir" {
			return fmt.Errorf("EIO")
		}
		return nil
	}
	_, _, _ = s.Archive(bg, sid(1), "op")
	s.Hook = nil
	putFile(t, dir, fileBytes(snapOf(99, sid(7))), 0o600) // someone else's generation
	if _, _, err := s.Archive(bg, sid(2), "op"); KindOf(err) != KindUnavailable {
		t.Fatalf("got %v, want unavailable", err)
	}
	if s.Err() == nil {
		t.Fatal("the overlay did not latch unknown")
	}
	if _, err := s.Archived(bg, sid(1)); KindOf(err) != KindUnavailable {
		t.Fatal("lists must fail while unknown")
	}
	if _, _, err := s.Archive(bg, sid(3), "op"); KindOf(err) != KindUnavailable {
		t.Fatal("archive must refuse while unknown")
	}
	// the prompt path keeps the last good snapshot (ADR-025 4.5)
	if s.ArchivedKnown(sid(7)) {
		t.Fatal("the unknown file was published")
	}
}

func shortBound(t *testing.T, d time.Duration) {
	old := WriteBound
	WriteBound = d
	t.Cleanup(func() { WriteBound = old })
}

func TestDeadlineBeforeRenameNeverCommitsLater(t *testing.T) {
	shortBound(t, 80*time.Millisecond)
	dir := newDir(t)
	s := open(t, dir, true)
	gate := make(chan struct{})
	s.Hook = func(step string) error {
		if step == "fsync_file" {
			<-gate
		}
		return nil
	}
	_, _, err := s.Archive(bg, sid(1), "op")
	if KindOf(err) != KindUnavailable {
		t.Fatalf("got %v, want a clean refusal", err)
	}
	close(gate)
	// the worker must see the abandoned flag before the rename and remove its tmp
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(listDir(t, dir)) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, statErr := os.Stat(filepath.Join(dir, FileName)); statErr == nil {
		t.Fatal("a call that was refused committed later")
	}
	noTmp(t, dir)
	if ok, _ := s.Archived(bg, sid(1)); ok {
		t.Fatal("refused entry visible")
	}
}

func TestHungRenameIsUncertainHoldsTheMutexAndIsAdopted(t *testing.T) {
	shortBound(t, 100*time.Millisecond)
	dir := newDir(t)
	s := open(t, dir, true)
	gate := make(chan struct{})
	s.Hook = func(step string) error {
		if step == "rename" {
			<-gate
		}
		return nil
	}
	_, _, err := s.Archive(bg, sid(1), "op")
	if KindOf(err) != KindUncertain {
		t.Fatalf("hung rename: %v, want uncertain", err)
	}
	if ok, _ := s.Archived(bg, sid(1)); ok {
		t.Fatal("memory snapshot replaced on a hung write")
	}
	start := time.Now()
	_, _, err = s.Archive(bg, sid(2), "op") // waits behind the hung worker, own deadline
	if KindOf(err) != KindTimeout || time.Since(start) < 80*time.Millisecond {
		t.Fatalf("second write during the hang: %v after %v, want UPSTREAM_TIMEOUT after its own deadline", err, time.Since(start))
	}
	close(gate)
	time.Sleep(50 * time.Millisecond)
	if ok, _ := s.Archived(bg, sid(1)); ok {
		t.Fatal("a worker that outlived its caller published")
	}
	if _, _, err := s.Archive(bg, sid(3), "op"); err != nil {
		t.Fatalf("next change after the hang: %v", err)
	}
	for _, id := range []string{sid(1), sid(3)} {
		if ok, _ := s.Archived(bg, id); !ok {
			t.Fatalf("%s missing: the next change must adopt the late commit", id)
		}
	}
	if ok, _ := s.Archived(bg, sid(2)); ok {
		t.Fatal("the refused timeout call committed")
	}
}

// Test 14: the checksum is over re-encoded values.
func TestChecksumIsOverValuesNotBytes(t *testing.T) {
	dir := newDir(t)
	sn := snapOf(4, sid(1), sid(2))
	out := fileOut{canonBody: sn.body()}
	out.Sum = checksum(out.canonBody)
	pretty, _ := json.MarshalIndent(map[string]any{"sum": out.Sum, "entries": out.Entries, "generation": out.Generation, "v": out.V}, "\n\n", "\t")
	putFile(t, dir, pretty, 0o600)
	s := open(t, dir, true)
	if s.Entries() != 2 || s.Generation() != 4 {
		t.Fatal("a reformatted, reordered file with unchanged values must verify")
	}
	for name, edit := range map[string]func(string) string{
		"entry value": func(b string) string { return strings.Replace(b, `"op"`, `"ox"`, 1) },
		"generation":  func(b string) string { return strings.Replace(b, `"generation": 4`, `"generation": 5`, 1) },
	} {
		d2 := newDir(t)
		putFile(t, d2, []byte(edit(string(pretty))), 0o600)
		if _, err := Open(d2, Options{Writer: true}); err == nil {
			t.Fatalf("%s edit verified", name)
		}
	}
	extra := snapOf(4, sid(1), sid(2), sid(3))
	var m map[string]any
	_ = json.Unmarshal(pretty, &m)
	var m2 map[string]any
	b, _ := json.Marshal(extra.body())
	_ = json.Unmarshal(b, &m2)
	m["entries"] = m2["entries"]
	d3 := newDir(t)
	raw, _ := json.Marshal(m)
	putFile(t, d3, raw, 0o600)
	if _, err := Open(d3, Options{Writer: true}); err == nil {
		t.Fatal("an extra entry verified")
	}
}

// Q8: a read-only process re-reads a changed file at most once a second and keeps the last good
// snapshot when the reload fails.
func TestReaderReloadsAtMostOncePerSecond(t *testing.T) {
	dir := newDir(t)
	w := open(t, dir, true)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	r, err := Open(dir, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if _, _, err := w.Archive(bg, sid(1), "op"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.Archived(bg, sid(1)); ok {
		t.Fatal("reloaded within the same second")
	}
	now = now.Add(1100 * time.Millisecond)
	if ok, err := r.Archived(bg, sid(1)); !ok || err != nil {
		t.Fatalf("not reloaded after a second: %v %v", ok, err)
	}
	putFile(t, dir, []byte("garbage"), 0o600)
	now = now.Add(2 * time.Second)
	if _, err := r.Archived(bg, sid(1)); KindOf(err) != KindUnavailable {
		t.Fatal("lists must report unknown for a failed reload")
	}
	if !r.ArchivedKnown(sid(1)) {
		t.Fatal("the last good snapshot must be kept for the prompt path")
	}
}

// Test 1 (mutation: skip the fsync calls): a write syncs the temporary file and the directory.
func TestWriteSyncsTheFileAndTheDirectory(t *testing.T) {
	var mu sync.Mutex
	var kinds []string
	old := syncFile
	syncFile = func(f *os.File) error {
		if fi, err := f.Stat(); err == nil && fi.IsDir() {
			mu.Lock()
			kinds = append(kinds, "dir")
			mu.Unlock()
		} else {
			mu.Lock()
			kinds = append(kinds, "file")
			mu.Unlock()
		}
		return old(f)
	}
	t.Cleanup(func() { syncFile = old })
	s := open(t, newDir(t), true)
	if _, _, err := s.Archive(bg, sid(1), "op"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(kinds, ",") != "file,dir" {
		t.Fatalf("fsync calls = %v, want the file then the directory", kinds)
	}
}

// ADR-025 section 5.4: the overlay is never rebuilt, repaired or extended from audit lines: the
// package imports neither the logger nor the audit packages.
func TestNoJournalSource(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		for name, f := range p.Files {
			for _, imp := range f.Imports {
				if v := imp.Path.Value; strings.Contains(v, "log/slog") || strings.Contains(v, "internal/audit") || strings.Contains(v, "journal") {
					t.Fatalf("%s imports %s", name, v)
				}
			}
		}
	}
}

// #308 P3-1: a writer-role process that loses the flock is a reader (ADR-025 Q8, 4.4): it re-reads a
// changed file at most once a second, so an entry archived by the lock holder AFTER it opened becomes
// visible within the poll window.
func TestLockLosingWriterBehavesAsAReader(t *testing.T) {
	dir := newDir(t)
	a := open(t, dir, true)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	b, err := Open(dir, Options{Writer: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	if b.CanWrite() {
		t.Fatal("the second writer holds the lock")
	}
	if _, _, err := a.Archive(bg, sid(1), "op"); err != nil { // after b opened
		t.Fatal(err)
	}
	if ok, _ := b.Archived(bg, sid(1)); ok {
		t.Fatal("re-read within the same second (the poll is at most once a second)")
	}
	now = now.Add(1100 * time.Millisecond)
	if ok, err := b.Archived(bg, sid(1)); !ok || err != nil {
		t.Fatalf("a lock-losing writer never reloaded the file: %v %v", ok, err)
	}
	// still unable to write
	if _, _, err := b.Archive(bg, sid(2), "op"); KindOf(err) != KindReadOnly {
		t.Fatalf("lock-losing writer archive: %v", err)
	}
}

// #308 P3-2: Close latches. A change after Close is a clean KindUnavailable, never a panic in the
// worker goroutine; Close twice is harmless; a reader reload after Close does nothing.
func TestCloseLatchesTheStore(t *testing.T) {
	s := open(t, newDir(t), true)
	if _, _, err := s.Archive(bg, sid(1), "op"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s.Close()
	if _, _, err := s.Archive(bg, sid(2), "op"); KindOf(err) != KindUnavailable {
		t.Fatalf("Archive after Close: %v, want KindUnavailable", err)
	}
	if _, err := s.Unarchive(bg, sid(1)); KindOf(err) != KindUnavailable {
		t.Fatalf("Unarchive after Close: %v, want KindUnavailable", err)
	}
	if ok, err := s.Archived(bg, sid(1)); !ok || err != nil {
		t.Fatalf("the published snapshot must stay readable after Close: %v %v", ok, err)
	}
	// a reader closed before the file changes does not reload it (and does not turn unknown)
	dir := newDir(t)
	w := open(t, dir, true)
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	r, err := Open(dir, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, _, err := w.Archive(bg, sid(1), "op"); err != nil { // the file changes after r closed
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	if ok, err := r.Archived(bg, sid(1)); err != nil || ok {
		t.Fatalf("reader Archived after Close = (%v, %v), want (false, nil): no reload on a closed store", ok, err)
	}
}

// #308 P3-2: Close waits for a hung write (it takes overlayMu); it returns when the worker does.
func TestCloseWaitsForAHungWrite(t *testing.T) {
	shortBound(t, 80*time.Millisecond)
	dir := newDir(t)
	s := open(t, dir, true)
	gate := make(chan struct{})
	s.Hook = func(step string) error {
		if step == "write" {
			<-gate
		}
		return nil
	}
	if _, _, err := s.Archive(bg, sid(1), "op"); KindOf(err) != KindUnavailable {
		t.Fatalf("hung write: %v", err)
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned while the write worker still held overlayMu")
	case <-time.After(150 * time.Millisecond):
	}
	close(gate)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close never returned after the worker finished")
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		t.Fatal("an abandoned write committed")
	}
}
