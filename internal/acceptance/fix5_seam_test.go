package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// seamReaders names, for each test seam of OSDecoyFS, the only functions of non-test code that may
// READ it. Any other reference (a read elsewhere, an assignment, taking the address with &, a
// composite-literal key) is a violation: production code must never set or reach a seam.
//
// The match is by field NAME, so an unrelated field that happens to be called fchown, race,
// afterMkdir or uidOf in non-test code would be flagged too (a false positive that fails closed:
// rename the field). It is a syntactic guard: reflection or unsafe could still reach a field.
var seamReaders = map[string]map[string]bool{
	"fchown":     {"doFchown": true},
	"race":       {"raceAt": true},
	"afterMkdir": {"mkdirOwned": true},
	"uidOf":      {"mkdirOwned": true},
}

func seamViolations(f *ast.File) []string {
	var out []string
	var stack []ast.Node
	fn := func() string { // the nearest enclosing function declaration ("" at file level)
		for i := len(stack) - 1; i >= 0; i-- {
			if d, ok := stack[i].(*ast.FuncDecl); ok {
				return d.Name.Name
			}
		}
		return ""
	}
	ast.Inspect(f, func(x ast.Node) bool {
		if x == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, x)
		switch v := x.(type) {
		case *ast.KeyValueExpr:
			if id, ok := v.Key.(*ast.Ident); ok && seamReaders[id.Name] != nil {
				out = append(out, "sets the test seam "+id.Name+" in a literal")
			}
		case *ast.SelectorExpr:
			readers, isSeam := seamReaders[v.Sel.Name]
			if !isSeam {
				break
			}
			var parent ast.Node
			if len(stack) >= 2 {
				parent = stack[len(stack)-2]
			}
			switch p := parent.(type) {
			case *ast.UnaryExpr:
				if p.Op == token.AND {
					out = append(out, "takes the address of the test seam "+v.Sel.Name)
					return true
				}
			case *ast.AssignStmt:
				for _, l := range p.Lhs {
					if l == ast.Expr(v) {
						out = append(out, "assigns the test seam "+v.Sel.Name)
						return true
					}
				}
			case *ast.IncDecStmt:
				out = append(out, "modifies the test seam "+v.Sel.Name)
				return true
			}
			if !readers[fn()] {
				out = append(out, "references the test seam "+v.Sel.Name+" outside its accessor (in \""+fn()+"\")")
			}
		}
		return true
	})
	return out
}

func parseSrc(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// R350-CLEANUP (#352 P3-1, #354 P3-1): the guard flags every way production code can reach a seam,
// not only plain assignments: taking the address (the #354 PoC), assigning from a function that is
// not the one that reads it, an increment-like write, and a literal key.
func TestSeamGuardCatchesEveryWayToReachASeam(t *testing.T) {
	bad := map[string]string{
		"address-of then store (#354 PoC)": `package p
func f(o *OSDecoyFS) { p := &o.afterMkdir; *p = func(int, string) {} }`,
		"assignment": `package p
func f(o *OSDecoyFS) { o.uidOf = nil }`,
		"read outside its own accessor": `package p
func f(o *OSDecoyFS) { _ = o.fchown }`,
		"address-of inside the accessor": `package p
func (o *OSDecoyFS) doFchown(fd int) error { q := &o.fchown; _ = q; return nil }`,
		"literal key": `package p
var o = OSDecoyFS{race: nil}`,
		"call through the field from another function": `package p
func f(o *OSDecoyFS) { o.race("x") }`,
	}
	for name, src := range bad {
		if len(seamViolations(parseSrc(t, src))) == 0 {
			t.Errorf("not flagged: %s", name)
		}
	}
	good := map[string]string{
		"the accessors read their seams": `package p
func (o *OSDecoyFS) doFchown(fd int) error {
	if o.fchown != nil {
		return o.fchown(fd, o.UID, o.GID)
	}
	return nil
}
func (o *OSDecoyFS) raceAt(p string) {
	if o.race != nil {
		o.race(p)
	}
}
func (o *OSDecoyFS) mkdirOwned(parent int, name, full string) (int, error) {
	if o.afterMkdir != nil {
		o.afterMkdir(parent, name)
	}
	owner := 1
	if o.uidOf != nil {
		owner = o.uidOf(0, owner)
	}
	return owner, nil
}`,
		"an unrelated field of another name": `package p
func f(o *OSDecoyFS) { o.UID = 1; o.mu.Lock() }`,
	}
	for name, src := range good {
		if v := seamViolations(parseSrc(t, src)); len(v) != 0 {
			t.Errorf("false positive on %s: %v", name, v)
		}
	}
}
