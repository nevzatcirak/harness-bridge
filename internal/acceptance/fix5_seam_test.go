package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// seamReaders names, for each test seam of OSDecoyFS, the only functions of non-test code that may
// READ it. Any other reference is a violation: a read elsewhere, an assignment, taking the address
// with & (parentheses around the seam are looked through, also inside an accessor), an increment, a
// composite-literal key, and any unkeyed (positional) composite literal of OSDecoyFS, which could
// fill a seam field without naming it. Empty and keyed literals are allowed.
//
// Limits (the guard is a convention check, not a proof): it matches by field NAME, so an unrelated
// field called fchown, race, afterMkdir or uidOf in non-test code would be flagged too (a false
// positive that fails closed: rename the field); it is syntactic and per file, so reflection,
// unsafe, cgo or assembly, a copy of the struct made outside this package, or a seam handed in
// through a new function parameter or interface are not seen; and it scans only this package's
// non-test files (every build tag, since each file is parsed).
var seamReaders = map[string]map[string]bool{
	"fchown":     {"doFchown": true},
	"race":       {"raceAt": true},
	"afterMkdir": {"mkdirOwned": true},
	"uidOf":      {"mkdirOwned": true},
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
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
		case *ast.CompositeLit:
			if id, ok := v.Type.(*ast.Ident); ok && id.Name == "OSDecoyFS" && len(v.Elts) > 0 {
				if _, keyed := v.Elts[0].(*ast.KeyValueExpr); !keyed {
					out = append(out, "has an unkeyed composite literal of OSDecoyFS")
				}
			}
		case *ast.KeyValueExpr:
			if id, ok := v.Key.(*ast.Ident); ok && seamReaders[id.Name] != nil {
				out = append(out, "sets the test seam "+id.Name+" in a literal")
			}
		case *ast.SelectorExpr:
			readers, isSeam := seamReaders[v.Sel.Name]
			if !isSeam {
				break
			}
			// the nearest ancestor that is not a pair of parentheses
			var parent ast.Node
			for i := len(stack) - 2; i >= 0; i-- {
				if _, paren := stack[i].(*ast.ParenExpr); !paren {
					parent = stack[i]
					break
				}
			}
			switch p := parent.(type) {
			case *ast.UnaryExpr:
				if p.Op == token.AND {
					out = append(out, "takes the address of the test seam "+v.Sel.Name)
					return true
				}
			case *ast.AssignStmt:
				for _, l := range p.Lhs {
					if unparen(l) == ast.Expr(v) {
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
		"unkeyed (positional) literal of OSDecoyFS": `package p
var o = OSDecoyFS{0, 0}`,
		"unkeyed pointer literal of OSDecoyFS": `package p
func f() *OSDecoyFS { return &OSDecoyFS{1, 2} }`,
		"parenthesised left side": `package p
func f(o *OSDecoyFS) { (o.afterMkdir) = nil }`,
		"parenthesised left side inside its own accessor": `package p
func (o *OSDecoyFS) mkdirOwned(parent int, name, full string) (int, error) { (o.afterMkdir) = nil; return 0, nil }`,
		"address of a parenthesised seam": `package p
func f(o *OSDecoyFS) { p := &(o.race); _ = p }`,
		"doubly parenthesised address inside the accessor": `package p
func (o *OSDecoyFS) raceAt(p string) { q := &((o.race)); _ = q }`,
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
		"empty and keyed literals of OSDecoyFS": `package p
func f() { _ = &OSDecoyFS{}; _ = OSDecoyFS{UID: 1, GID: 2} }`,
		"a parenthesised read inside its accessor": `package p
func (o *OSDecoyFS) raceAt(p string) { if (o.race) != nil { (o.race)(p) } }`,
	}
	for name, src := range good {
		if v := seamViolations(parseSrc(t, src)); len(v) != 0 {
			t.Errorf("false positive on %s: %v", name, v)
		}
	}
}
