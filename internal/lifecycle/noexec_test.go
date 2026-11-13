package lifecycle

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ADR-022 section 3.2: the Git top level procedure runs no git and starts no
// process. The package and the lifecycle opener in internal/security import no
// os/exec, net, cgo, unsafe or golang.org/x/sys beyond the one file that needs
// unix.O_NOFOLLOW, and call no exec-like entry point under any alias.
func TestNoSubprocessInTheLifecycleCore(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	more, _ := filepath.Glob(filepath.Join("..", "security", "lifecycle_root.go"))
	files = append(files, more...)
	banned := func(p string) bool {
		switch {
		case p == "C", p == "unsafe", p == "os/exec", p == "os/signal", p == "plugin", p == "reflect", p == "net", strings.HasPrefix(p, "net/"):
			return true
		}
		return false
	}
	exec := map[string]bool{"Exec": true, "ForkExec": true, "StartProcess": true, "Command": true, "CommandContext": true, "Syscall": true, "RawSyscall": true, "Syscall6": true}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if banned(p) {
				t.Errorf("%s imports %q", f, p)
			}
			if strings.HasPrefix(p, "golang.org/x/") && filepath.Base(f) != "readfile_linux.go" {
				t.Errorf("%s imports %q", f, p)
			}
			if imp.Name != nil && (imp.Name.Name == "." || imp.Name.Name == "_") {
				t.Errorf("%s uses a dot or blank import of %q", f, p)
			}
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && exec[sel.Sel.Name] {
				t.Errorf("%s uses %s", f, sel.Sel.Name)
			}
			return true
		})
	}
	ents, _ := os.ReadDir(".")
	for _, e := range ents {
		switch filepath.Ext(e.Name()) {
		case ".go":
		case ".c", ".h", ".s", ".S", ".cc", ".cpp":
			t.Errorf("non-Go source %s", e.Name())
		}
	}
}
