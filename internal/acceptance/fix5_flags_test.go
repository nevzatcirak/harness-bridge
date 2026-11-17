package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// R350-CLEANUP (#354 P3-3), R359-CLEANUP (#360 P3, #362 P3-3): the live command validates every flag
// before it creates the -out file, so an invalid configuration leaves no file behind. The command
// (build tag live, package main) is not unit-testable, so the order is pinned on its syntax tree: the
// first call expressions of cfg.Validate and acceptance.ParseItems inside main come before the first
// call of acceptance.CreateEvidenceFile. Comments and string literals are not calls, so text that
// merely mentions a function cannot satisfy the check.
func flagOrderViolations(fset *token.FileSet, f *ast.File) []string {
	first := map[string]int{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Name.Name != "main" {
			continue
		}
		ast.Inspect(fd.Body, func(x ast.Node) bool {
			call, ok := x.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			name := id.Name + "." + sel.Sel.Name
			off := fset.Position(call.Pos()).Offset
			if old, seen := first[name]; !seen || off < old {
				first[name] = off
			}
			return true
		})
	}
	var out []string
	create, ok := first["acceptance.CreateEvidenceFile"]
	if !ok {
		return []string{"main does not call acceptance.CreateEvidenceFile"}
	}
	for _, before := range []string{"cfg.Validate", "acceptance.ParseItems"} {
		off, ok := first[before]
		switch {
		case !ok:
			out = append(out, "main does not call "+before)
		case off > create:
			out = append(out, "main calls "+before+" after creating the evidence file")
		}
	}
	return out
}

func parseMain(t *testing.T, src string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	return fset, f
}

func TestLiveCommandValidatesFlagsBeforeCreatingTheEvidenceFile(t *testing.T) {
	b, err := os.ReadFile("../../cmd/hb-claude-acceptance/main.go")
	if err != nil {
		t.Fatal(err)
	}
	fset, f := parseMain(t, string(b))
	for _, v := range flagOrderViolations(fset, f) {
		t.Error(v)
	}
}

// The check itself: the good order passes; a real reorder is caught even when a comment or a string
// above the creation mentions the function (the first-textual-occurrence check was fooled by that).
func TestFlagOrderCheckIsNotFooledByCommentsOrStrings(t *testing.T) {
	const head = "package main\nfunc main() {\n"
	good := head + "\tif err := cfg.Validate(); err != nil {}\n\tn, _ := acceptance.ParseItems(*items)\n\tf, _ := acceptance.CreateEvidenceFile(p)\n}\n"
	fset, f := parseMain(t, good)
	if v := flagOrderViolations(fset, f); len(v) != 0 {
		t.Fatalf("good order flagged: %v", v)
	}
	bad := map[string]string{
		"parse after create, a comment mentions it above": head + "\t// acceptance.ParseItems( happens here\n\tcfg.Validate()\n\tacceptance.CreateEvidenceFile(p)\n\tacceptance.ParseItems(*items)\n}\n",
		"parse after create, a string mentions it above":  head + "\ts := \"acceptance.ParseItems(\"\n\t_ = s\n\tcfg.Validate()\n\tacceptance.CreateEvidenceFile(p)\n\tacceptance.ParseItems(*items)\n}\n",
		"validate after create":                           head + "\tacceptance.ParseItems(*items)\n\tacceptance.CreateEvidenceFile(p)\n\tcfg.Validate()\n}\n",
		"parse only in a comment":                         head + "\t// acceptance.ParseItems(\n\tcfg.Validate()\n\tacceptance.CreateEvidenceFile(p)\n}\n",
		"no creation call":                                head + "\tcfg.Validate()\n}\n",
	}
	for name, src := range bad {
		fset, f := parseMain(t, src)
		if len(flagOrderViolations(fset, f)) == 0 {
			t.Errorf("not flagged: %s", name)
		}
	}
}

func TestParseItems(t *testing.T) {
	good := map[string][]int{"1": {1}, "1,2,4": {1, 2, 4}, " 8 , 3 ": {8, 3}}
	for in, want := range good {
		got, err := ParseItems(in)
		if err != nil || len(got) != len(want) {
			t.Errorf("ParseItems(%q) = %v, %v", in, got, err)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("ParseItems(%q) = %v, want %v", in, got, want)
			}
		}
	}
	for _, in := range []string{"", "0", "9", "a", "1,,2", "1,x", "-1", "1;2"} {
		if got, err := ParseItems(in); err == nil {
			t.Errorf("ParseItems(%q) accepted: %v", in, got)
		}
	}
}
