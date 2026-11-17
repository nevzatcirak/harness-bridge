package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
)

// R350-CLEANUP (#354 P3-3), R359-CLEANUP (#360 P3, #362 P3-3), R365-CLEANUP (#369 P3-2): the live
// command validates every flag before it creates the -out file, so an invalid configuration leaves no
// file behind. The command (build tag live, package main) is not unit-testable, so the order is pinned
// on its syntax tree, in `main`:
//
//   - the validators (cfg.Validate, acceptance.ParseItems) are counted only where they certainly run
//     before whatever follows: as top-level statements of main's body (an expression statement, the
//     right side of an assignment, a var declaration) or in the init or condition of a top-level `if`.
//     A call inside an if/for/switch body, a case clause or a closure does NOT count: a dead early
//     call (`if false { acceptance.ParseItems(...) }`) cannot satisfy the check (it fails closed, so a
//     legitimate move of a validator into a block is reported and has to be done on purpose);
//   - the creation (acceptance.CreateEvidenceFile) is the EARLIEST occurrence anywhere in main, nested
//     or not, which is the stricter reading;
//   - both validators must precede the creation. Comments and string literals are not calls.
func flagOrderViolations(fset *token.FileSet, f *ast.File) []string {
	callName := func(call *ast.CallExpr) string {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				return id.Name + "." + sel.Sel.Name
			}
		}
		return ""
	}
	// exprCalls records the calls of an expression, not entering closures.
	exprCalls := func(e ast.Node, into map[string]int) {
		if e == nil {
			return
		}
		ast.Inspect(e, func(x ast.Node) bool {
			if _, lit := x.(*ast.FuncLit); lit {
				return false
			}
			if call, ok := x.(*ast.CallExpr); ok {
				if n := callName(call); n != "" {
					off := fset.Position(call.Pos()).Offset
					if old, seen := into[n]; !seen || off < old {
						into[n] = off
					}
				}
			}
			return true
		})
	}
	var topCalls func(st ast.Stmt, into map[string]int)
	topCalls = func(st ast.Stmt, into map[string]int) {
		switch v := st.(type) {
		case *ast.ExprStmt:
			exprCalls(v.X, into)
		case *ast.AssignStmt:
			for _, r := range v.Rhs {
				exprCalls(r, into)
			}
		case *ast.DeclStmt:
			if gd, ok := v.Decl.(*ast.GenDecl); ok {
				for _, sp := range gd.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok {
						for _, val := range vs.Values {
							exprCalls(val, into)
						}
					}
				}
			}
		case *ast.IfStmt: // the init and the condition run unconditionally; the bodies do not
			if v.Init != nil {
				topCalls(v.Init, into)
			}
			exprCalls(v.Cond, into)
		}
	}
	validators := map[string]int{} // counted: top-level only
	anywhere := map[string]int{}   // the creation: earliest occurrence anywhere, closures included
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Name.Name != "main" || fd.Body == nil {
			continue
		}
		for _, st := range fd.Body.List {
			topCalls(st, validators)
		}
		ast.Inspect(fd.Body, func(x ast.Node) bool {
			if call, ok := x.(*ast.CallExpr); ok {
				if n := callName(call); n != "" {
					off := fset.Position(call.Pos()).Offset
					if old, seen := anywhere[n]; !seen || off < old {
						anywhere[n] = off
					}
				}
			}
			return true
		})
	}
	create, ok := anywhere["acceptance.CreateEvidenceFile"]
	if !ok {
		return []string{"main does not call acceptance.CreateEvidenceFile"}
	}
	var out []string
	for _, before := range []string{"cfg.Validate", "acceptance.ParseItems"} {
		off, ok := validators[before]
		switch {
		case !ok:
			out = append(out, "main does not call "+before+" as a top-level statement (or in a top-level if's init or condition)")
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
	goods := map[string]string{
		"statements and assignments":                            head + "\tif err := cfg.Validate(); err != nil {}\n\tn, _ := acceptance.ParseItems(*items)\n\tf, _ := acceptance.CreateEvidenceFile(p)\n}\n",
		"validator in an if condition and a var declaration":    head + "\tif cfg.Validate() != nil {}\n\tvar n, _ = acceptance.ParseItems(*items)\n\tacceptance.CreateEvidenceFile(p)\n}\n",
		"creation nested in an if body, as in the real command": head + "\tif err := cfg.Validate(); err != nil {}\n\tn, _ := acceptance.ParseItems(*items)\n\tif *out != \"\" {\n\t\tf, _ := acceptance.CreateEvidenceFile(p)\n\t}\n}\n",
	}
	for name, src := range goods {
		fset, f := parseMain(t, src)
		if v := flagOrderViolations(fset, f); len(v) != 0 {
			t.Errorf("good order flagged (%s): %v", name, v)
		}
	}
	bad := map[string]string{
		"parse after create, a comment mentions it above":                head + "\t// acceptance.ParseItems( happens here\n\tcfg.Validate()\n\tacceptance.CreateEvidenceFile(p)\n\tacceptance.ParseItems(*items)\n}\n",
		"parse after create, a string mentions it above":                 head + "\ts := \"acceptance.ParseItems(\"\n\t_ = s\n\tcfg.Validate()\n\tacceptance.CreateEvidenceFile(p)\n\tacceptance.ParseItems(*items)\n}\n",
		"validate after create":                                          head + "\tacceptance.ParseItems(*items)\n\tacceptance.CreateEvidenceFile(p)\n\tcfg.Validate()\n}\n",
		"parse only in a comment":                                        head + "\t// acceptance.ParseItems(\n\tcfg.Validate()\n\tacceptance.CreateEvidenceFile(p)\n}\n",
		"no creation call":                                               head + "\tcfg.Validate()\n}\n",
		"a dead early call in an if false block (#369 P3-2)":             head + "\tcfg.Validate()\n\tif false {\n\t\tacceptance.ParseItems(*items)\n\t}\n\tacceptance.CreateEvidenceFile(p)\n\tacceptance.ParseItems(*items)\n}\n",
		"the only validator call sits in an if false block":              head + "\tcfg.Validate()\n\tif false {\n\t\tacceptance.ParseItems(*items)\n\t}\n\tacceptance.CreateEvidenceFile(p)\n}\n",
		"the only validator call sits in a closure that is never called": head + "\tcfg.Validate()\n\t_ = func() { acceptance.ParseItems(*items) }\n\tacceptance.CreateEvidenceFile(p)\n}\n",
		"the validator call sits in a for loop body":                     head + "\tcfg.Validate()\n\tfor i := 0; i < 0; i++ {\n\t\tacceptance.ParseItems(*items)\n\t}\n\tacceptance.CreateEvidenceFile(p)\n}\n",
		"the validator call sits in a switch case":                       head + "\tcfg.Validate()\n\tswitch {\n\tcase false:\n\t\tacceptance.ParseItems(*items)\n\t}\n\tacceptance.CreateEvidenceFile(p)\n}\n",
		"the creation call is first inside a nested block":               head + "\tif x {\n\t\tacceptance.CreateEvidenceFile(p)\n\t}\n\tcfg.Validate()\n\tacceptance.ParseItems(*items)\n}\n",
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
