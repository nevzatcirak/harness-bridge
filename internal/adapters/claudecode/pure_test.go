package claudecode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Slice 1 is a pure core: no exec, no network, no process start, no cgo.
// pureCoreImports is the EXACT import set of the non-test files; adding an
// import (any import) fails this test until a reviewer extends the list.
var pureCoreImports = []string{
	"bufio", "bytes", "crypto/sha256", "encoding/hex", "encoding/json", "errors", "fmt", "io", "io/fs",
	"os", "path", "regexp", "sort", "strconv", "strings", "sync", "syscall", "time", "unicode/utf8",
}

// bannedImport reports import paths that must never appear, allowlist or not.
func bannedImport(p string) bool {
	switch {
	case p == "C", p == "unsafe", p == "os/exec", p == "os/signal", p == "plugin", p == "reflect":
		return true
	case p == "net" || strings.HasPrefix(p, "net/"):
		return true
	case strings.HasPrefix(p, "golang.org/x/"):
		return true
	}
	return false
}

var execLikeCalls = map[string]bool{"Exec": true, "ForkExec": true, "StartProcess": true, "Command": true, "CommandContext": true}

func TestNoExecInThePureCore(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := map[string]bool{}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() {
			if name != "testdata" {
				t.Errorf("unexpected subdirectory %q in the pure core", name)
			}
			continue
		}
		switch filepath.Ext(name) {
		case ".go":
		case ".md":
			continue
		default:
			t.Errorf("non-Go source file %q in the pure core (cgo or assembly are not allowed)", name)
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			found[p] = true
			if bannedImport(p) {
				t.Errorf("%s imports banned package %q", name, p)
			}
		}
		// Whatever the import is called locally, a selector call named like an
		// exec entry point is refused.
		ast.Inspect(af, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && execLikeCalls[sel.Sel.Name] {
					t.Errorf("%s calls %s", name, sel.Sel.Name)
				}
			}
			return true
		})
	}
	var got []string
	for p := range found {
		got = append(got, p)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(pureCoreImports, ",") {
		t.Errorf("import set changed:\n got  %v\n want %v", got, pureCoreImports)
	}
}

// The guard itself must detect what it claims to detect.
func TestPureCoreGuardRecognisesBannedImports(t *testing.T) {
	for _, p := range []string{"C", "unsafe", "os/exec", "os/signal", "plugin", "net", "net/http", "net/netip", "golang.org/x/sys/unix", "golang.org/x/net/x"} {
		if !bannedImport(p) {
			t.Errorf("%q is not banned", p)
		}
	}
	for _, p := range pureCoreImports {
		if bannedImport(p) {
			t.Errorf("allowlisted %q is banned", p)
		}
	}
}
