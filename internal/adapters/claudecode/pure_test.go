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

// The pure core (every non-test .go file directly in this directory) starts no
// process, opens no network connection and has no cgo. Its exec lives in ONE file
// of the runner subpackage, runner/spawn_linux.go; that boundary is explicit below
// and tested from both sides.
//
// pureCoreImports is the EXACT import set of the pure core's non-test files;
// adding an import (any import) fails this test until a reviewer extends the list.
var pureCoreImports = []string{
	"bufio", "bytes", "crypto/sha256", "encoding/hex", "encoding/json", "errors", "fmt", "io", "io/fs",
	"os", "path", "regexp", "sort", "strconv", "strings", "sync", "syscall", "time", "unicode/utf8",
}

// Selectors of syscall and os the pure core may use. Anything else (Exec, ForkExec,
// StartProcess, Kill, Setpgid, Flock, Socket, ...) fails the guard. O_* and S_*
// constants are allowed by prefix.
var (
	syscallAllowed = map[string]bool{"Open": true, "Openat": true, "Close": true, "Fstat": true, "Lstat": true, "Stat_t": true}
	osAllowed      = map[string]bool{"File": true, "Geteuid": true, "NewFile": true}
	// Only the spawn file may also signal and group the child.
	spawnSyscallExtra = map[string]bool{"Kill": true, "SysProcAttr": true, "SIGINT": true, "WaitStatus": true}
)

// The one file that may import os/exec and call its entry points, and the one
// subdirectory the pure core allows.
const (
	runnerDir  = "runner"
	spawnFile  = "spawn_linux.go"
	modulePath = "github.com/nevzatcirak/harness-bridge/"
)

// bannedImport reports import paths that must never appear, allowlist or not.
func bannedImport(p string) bool {
	switch {
	case p == "C", p == "unsafe", p == "plugin", p == "reflect":
		return true
	case p == "net" || strings.HasPrefix(p, "net/"):
		return true
	case strings.HasPrefix(p, "golang.org/x/"):
		return true
	}
	return false
}

var execLikeCalls = map[string]bool{"Exec": true, "ForkExec": true, "StartProcess": true, "Command": true, "CommandContext": true}

// guardReport scans one parsed file and returns every violation of the shared
// rules: banned imports, dot and blank imports, aliased syscall/os imports, and
// the selector allowlists for syscall and os. allowExec lifts only the os/exec,
// os/signal and exec-entry-point checks and adds the signal and process-group
// syscall selectors (the spawn file).
func guardReport(name string, af *ast.File, allowExec bool) (imports []string, problems []string) {
	local := map[string]string{} // local name -> import path, for syscall and os
	for _, imp := range af.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		imports = append(imports, p)
		if bannedImport(p) {
			problems = append(problems, name+" imports banned package "+strconv.Quote(p))
		}
		if !allowExec && (p == "os/exec" || p == "os/signal") {
			problems = append(problems, name+" imports "+strconv.Quote(p)+" outside "+runnerDir+"/"+spawnFile)
		}
		if imp.Name != nil {
			switch imp.Name.Name {
			case ".", "_":
				problems = append(problems, name+": dot and blank imports are refused ("+strconv.Quote(p)+")")
			default:
				if p == "syscall" || p == "os" || p == "os/exec" || p == "os/signal" {
					problems = append(problems, name+": aliased import of "+strconv.Quote(p)+" is refused")
				}
			}
		}
		if p == "syscall" || p == "os" {
			local[p] = p
		}
	}
	ast.Inspect(af, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			// Whatever the import is called locally, a selector call named like an
			// exec entry point is refused (outside the spawn file).
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && execLikeCalls[sel.Sel.Name] && !allowExec {
				problems = append(problems, name+" calls "+sel.Sel.Name)
			}
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch local[id.Name] {
			case "syscall":
				if !syscallAllowed[x.Sel.Name] && !(allowExec && spawnSyscallExtra[x.Sel.Name]) && !strings.HasPrefix(x.Sel.Name, "O_") && !strings.HasPrefix(x.Sel.Name, "S_") {
					problems = append(problems, name+": syscall."+x.Sel.Name+" is not in the allowlist")
				}
			case "os":
				if !osAllowed[x.Sel.Name] {
					problems = append(problems, name+": os."+x.Sel.Name+" is not in the allowlist")
				}
			}
		}
		return true
	})
	return imports, problems
}

func parseGoFiles(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		switch filepath.Ext(name) {
		case ".go":
		case ".md":
			continue
		default:
			t.Errorf("non-Go source file %q in %s (cgo or assembly are not allowed)", name, dir)
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = af
	}
	return out
}

func TestNoExecInThePureCore(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() && e.Name() != "testdata" && e.Name() != runnerDir {
			t.Errorf("unexpected subdirectory %q next to the pure core", e.Name())
		}
	}
	found := map[string]bool{}
	for name, af := range parseGoFiles(t, ".") {
		imps, problems := guardReport(name, af, false)
		for _, p := range imps {
			found[p] = true
		}
		for _, p := range problems {
			t.Error(p)
		}
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

// The runner subpackage may exec, but only in spawn_linux.go; every other file
// obeys the same rules as the pure core, and it may import the pure core and the
// execution package (the pure core never imports it back).
var runnerImportsExtra = map[string]bool{
	"context": true, "crypto/rand": true, "os/exec": true, "os/signal": false, "path/filepath": true, "sync/atomic": true, "math/rand": false,
	"github.com/nevzatcirak/harness-bridge/internal/adapters/claudecode": true,
	"github.com/nevzatcirak/harness-bridge/internal/execution":           true,
}

func TestExecIsConfinedToTheSpawnFile(t *testing.T) {
	files := parseGoFiles(t, runnerDir)
	if _, ok := files[spawnFile]; !ok {
		t.Fatalf("%s/%s is the one file allowed to exec, and it does not exist", runnerDir, spawnFile)
	}
	pure := map[string]bool{}
	for _, p := range pureCoreImports {
		pure[p] = true
	}
	execers := 0
	for name, af := range files {
		imps, problems := guardReport(runnerDir+"/"+name, af, name == spawnFile)
		for _, p := range problems {
			t.Error(p)
		}
		for _, p := range imps {
			if p == "os/exec" {
				execers++
				if name != spawnFile {
					t.Errorf("%s imports os/exec", name)
				}
			}
			if !pure[p] && !runnerImportsExtra[p] {
				t.Errorf("%s/%s imports %q, which is not on the runner allowlist", runnerDir, name, p)
			}
		}
	}
	if execers != 1 {
		t.Errorf("exactly one runner file imports os/exec, got %d", execers)
	}
	// The pure core must not import the runner (the boundary points one way).
	for name, af := range parseGoFiles(t, ".") {
		for _, imp := range af.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); strings.HasPrefix(p, modulePath) {
				t.Errorf("%s imports %q: the pure core imports nothing from the module", name, p)
			}
		}
	}
}

// The guard itself must detect what it claims to detect.
func guardOn(t *testing.T, src string, allowExec bool) []string {
	t.Helper()
	af, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, problems := guardReport("x.go", af, allowExec)
	return problems
}

func TestPureCoreGuardRecognisesBannedImports(t *testing.T) {
	for _, p := range []string{"C", "unsafe", "plugin", "net", "net/http", "net/netip", "golang.org/x/sys/unix", "golang.org/x/net/x", "reflect"} {
		if !bannedImport(p) {
			t.Errorf("%q is not banned", p)
		}
	}
	for _, p := range pureCoreImports {
		if bannedImport(p) {
			t.Errorf("allowlisted %q is banned", p)
		}
	}
	if len(guardOn(t, "package x\nimport \"os/exec\"\nfunc f(){ exec.Command(\"a\") }", false)) == 0 {
		t.Error("os/exec outside the spawn file must be refused")
	}
	if p := guardOn(t, "package x\nimport \"os/exec\"\nfunc f(){ exec.Command(\"a\") }", true); len(p) != 0 {
		t.Errorf("the spawn file may exec: %v", p)
	}
	// Signalling is the spawn file's alone.
	kill := "package x\nimport \"syscall\"\nfunc f(){ _ = syscall.Kill(-1, syscall.SIGINT); var a syscall.SysProcAttr; _ = a }"
	if len(guardOn(t, kill, false)) == 0 {
		t.Error("syscall.Kill outside the spawn file must be refused")
	}
	if p := guardOn(t, kill, true); len(p) != 0 {
		t.Errorf("the spawn file may signal its group: %v", p)
	}
	if len(guardOn(t, "package x\nimport \"syscall\"\nfunc f(){ _ = syscall.Exec(\"a\", nil, nil) }", true)) == 0 {
		t.Error("even the spawn file may not syscall.Exec")
	}
}

// #218 P3-2 bypasses B1-B3 and #219 P3-5: the selector allowlist and the refusal
// of dot, blank and aliased imports.
func TestGuardRefusesTheKnownBypasses(t *testing.T) {
	cases := map[string]string{
		"syscall.Exec":            "package x\nimport \"syscall\"\nfunc f(){ syscall.Exec(\"a\", nil, nil) }",
		"syscall.ForkExec":        "package x\nimport \"syscall\"\nfunc f(){ _, _ = syscall.ForkExec(\"a\", nil, nil) }",
		"syscall.Kill":            "package x\nimport \"syscall\"\nfunc f(){ _ = syscall.Kill(1, 9) }",
		"syscall.Flock":           "package x\nimport \"syscall\"\nfunc f(){ _ = syscall.Flock(1, 1) }",
		"syscall.SysProcAttr":     "package x\nimport \"syscall\"\nvar a syscall.SysProcAttr",
		"syscall value, no call":  "package x\nimport \"syscall\"\nvar f = syscall.Exec",
		"aliased syscall":         "package x\nimport sc \"syscall\"\nfunc f(){ _ = sc.Open }",
		"dot import of syscall":   "package x\nimport . \"syscall\"\nvar a = Stat_t{}",
		"dot import of os":        "package x\nimport . \"os\"\nfunc f(){ _ = StartProcess }",
		"blank import":            "package x\nimport _ \"syscall\"",
		"aliased os":              "package x\nimport o \"os\"\nfunc f(){ o.StartProcess(\"a\", nil, nil) }",
		"os.StartProcess":         "package x\nimport \"os\"\nfunc f(){ os.StartProcess(\"a\", nil, nil) }",
		"os.FindProcess":          "package x\nimport \"os\"\nfunc f(){ _, _ = os.FindProcess(1) }",
		"os.Getenv (not allowed)": "package x\nimport \"os\"\nvar a = os.Getenv",
		"aliased os/exec":         "package x\nimport e \"os/exec\"\nfunc f(){ e.Command(\"a\") }",
		"os/signal":               "package x\nimport \"os/signal\"\nvar a = signal.Notify",
	}
	for name, src := range cases {
		if len(guardOn(t, src, false)) == 0 {
			t.Errorf("%s: the guard accepted it", name)
		}
	}
	ok := map[string]string{
		"allowed syscall": "package x\nimport \"syscall\"\nvar a syscall.Stat_t\nvar b = syscall.O_RDONLY | syscall.S_IFMT\nfunc f(){ _, _ = syscall.Open(\"a\", 0, 0); _ = syscall.Close(1); _ = syscall.Fstat(1, &a); _ = syscall.Lstat(\"a\", &a); _, _ = syscall.Openat(1, \"a\", 0, 0) }",
		"allowed os":      "package x\nimport \"os\"\nvar a *os.File\nfunc f(){ _ = os.Geteuid(); _ = os.NewFile(1, \"a\") }",
	}
	for name, src := range ok {
		if p := guardOn(t, src, false); len(p) != 0 {
			t.Errorf("%s: wrongly refused: %v", name, p)
		}
	}
}
