//go:build unix

package acceptance

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R317-CLEANUP (#346 P3-1): the owner check in mkdirOwned is the only defence against a directory of
// another user swapped in between mkdirat and the no-follow reopen. The swap is real (rename and
// mkdir); only the owner read from the reopened descriptor is stubbed (non-root test).
func TestMkdirRefusesADirectorySwappedForAnotherUsersOne(t *testing.T) {
	root := realDir(t)
	d := filepath.Join(root, "canary-control")
	var createdIno uint64
	chowned := false
	o := newFS()
	o.afterMkdir = func(_ int, _ string) {
		createdIno = inoOf(t, d)
		if err := os.Rename(d, d+".moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(d, 0o700); err != nil { // the replacement
			t.Fatal(err)
		}
	}
	o.uidOf = func(fd, real int) int {
		if fdIno(fd) != createdIno {
			return os.Geteuid() + 1 // the replacement belongs to someone else
		}
		return real
	}
	o.fchown = func(int, int, int) error { chowned = true; return nil }
	err := o.Mkdir(d)
	if !errors.Is(err, errUnsafePath) {
		t.Fatalf("Mkdir over a swapped directory: %v, want errUnsafePath", err)
	}
	if chowned {
		t.Fatal("a directory of another user was chowned")
	}
	if len(o.created) != 0 {
		t.Fatalf("nothing may be recorded: %v", o.created)
	}
	// an ancestor made by Write is protected the same way
	root2 := realDir(t)
	anc := filepath.Join(root2, "a")
	o2 := newFS()
	var created2 uint64
	o2.afterMkdir = func(_ int, _ string) {
		created2 = inoOf(t, anc)
		_ = os.Rename(anc, anc+".moved")
		_ = os.Mkdir(anc, 0o700)
	}
	o2.uidOf = func(fd, real int) int {
		if fdIno(fd) != created2 {
			return os.Geteuid() + 1
		}
		return real
	}
	o2.fchown = func(int, int, int) error { chowned = true; return nil }
	if err := o2.Write(filepath.Join(anc, "decoy-profile.json"), []byte("m")); err == nil {
		t.Fatal("Write went on below a swapped ancestor")
	}
	if chowned || len(o2.created) != 0 {
		t.Fatalf("chowned %v recorded %v", chowned, o2.created)
	}
	if _, err := os.Stat(filepath.Join(anc, "decoy-profile.json")); err == nil {
		t.Fatal("a file was created below a swapped ancestor")
	}
}

// The test seams exist only for tests: no non-test file of the package assigns them.
func TestSeamsAreNeverSetByProductionCode(t *testing.T) {
	seams := map[string]bool{"fchown": true, "race": true, "afterMkdir": true, "uidOf": true}
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(x ast.Node) bool {
			switch v := x.(type) {
			case *ast.AssignStmt:
				for _, l := range v.Lhs {
					if se, ok := l.(*ast.SelectorExpr); ok && seams[se.Sel.Name] {
						t.Errorf("%s assigns the test seam %s", n, se.Sel.Name)
					}
				}
			case *ast.KeyValueExpr:
				if id, ok := v.Key.(*ast.Ident); ok && seams[id.Name] {
					t.Errorf("%s sets the test seam %s in a literal", n, id.Name)
				}
			}
			return true
		})
	}
}
