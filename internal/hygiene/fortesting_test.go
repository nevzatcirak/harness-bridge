package hygiene

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TEST-HARDENING-01 (#41 re-review P3-3): an exported *ForTesting hook (for
// example repository.SetFSHookForTesting) may be declared in production code
// but must be referenced only from _test.go files, so production code can
// never install a test hook. The check covers this Go module; tests/confinement
// is a separate module and is skipped.
func TestForTestingHooksOnlyUsedInTests(t *testing.T) {
	root := filepath.Join("..", "..")
	if _, e := os.Stat(filepath.Join(root, "go.mod")); e != nil {
		t.Fatal("module root not found:", e)
	}
	fset := token.NewFileSet()
	files, violations := 0, []string{}
	e := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata") {
				return filepath.SkipDir
			}
			if path != root {
				if _, e := os.Stat(filepath.Join(path, "go.mod")); e == nil {
					return filepath.SkipDir // nested module, e.g. tests/confinement
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, e := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if e != nil {
			return e
		}
		files++
		// Declarations are allowed; every other identifier is a reference.
		declared := map[*ast.Ident]bool{}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				declared[fn.Name] = true
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if ok && strings.HasSuffix(id.Name, "ForTesting") && !declared[id] {
				violations = append(violations, fset.Position(id.Pos()).String()+": "+id.Name)
			}
			return true
		})
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if files == 0 {
		t.Fatal("no production Go files scanned")
	}
	for _, v := range violations {
		t.Errorf("*ForTesting hook referenced outside a _test.go file: %s", v)
	}
}
