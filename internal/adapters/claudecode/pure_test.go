package claudecode

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Slice 1 is a pure core: no exec, no network, no process start. The imports of
// every non-test file are checked.
func TestNoExecInThePureCore(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	banned := map[string]bool{"os/exec": true, "net": true, "net/http": true, "os/signal": true, "plugin": true}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			if p := strings.Trim(imp.Path.Value, `"`); banned[p] {
				t.Errorf("%s imports %s", f, p)
			}
		}
		src, _ := os.ReadFile(f)
		for _, call := range []string{"syscall.Exec(", "syscall.ForkExec(", "syscall.StartProcess(", "os.StartProcess("} {
			if strings.Contains(string(src), call) {
				t.Errorf("%s calls %s", f, call)
			}
		}
	}
}
