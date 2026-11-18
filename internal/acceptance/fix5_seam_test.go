package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// seamReaders names, for each test seam of OSDecoyFS, the only functions of non-test code that may
// READ it. Two groups of rules, both applied to the package's non-test files by
// TestSeamsAreNeverSetByProductionCode.
//
// seamViolations (syntactic, per file, by field NAME): a read of a seam anywhere but its accessor, an
// assignment, taking the address with & (parentheses are looked through, also inside an accessor), an
// increment, and a composite-literal key with a seam's name.
//
// literalViolations (go/types over the whole package, imports faked so only the package's own types
// are resolved): any unkeyed (positional) composite literal of ANY struct type, including elided inner
// literals of slices, arrays and maps and literals through an alias or a defined type (a positional
// literal could fill a seam field without naming it); any conversion to a type whose underlying type is
// OSDecoyFS's; and any type declaration, alias or defined type, whose underlying type is OSDecoyFS's.
// Empty and keyed literals are allowed.
//
// Limits (a convention check, not a proof): the seam rules match by field NAME, so an unrelated field
// called fchown, race, afterMkdir or uidOf in non-test code is flagged too (a false positive that
// fails closed: rename it); the type rules need the types of the package's own declarations to
// resolve: an expression whose type cannot be resolved is skipped, and a struct type that merely has
// the same field names and types as OSDecoyFS is treated as OSDecoyFS (also fails closed); reflection,
// unsafe, cgo or assembly, a copy of the struct made outside this package, and a seam handed in
// through a new function parameter or interface are not seen; and only this package's non-test files
// are scanned (every build tag, since every file is parsed and type-checked together; duplicate
// declarations across tagged files are ignored).
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

// prelude declares a stand-in OSDecoyFS (same shape for the guard's purposes) for the snippet tests.
const prelude = `
type OSDecoyFS struct {
	UID, GID   int
	race       func(string)
	fchown     func(int, int, int) error
	afterMkdir func(int, string)
	uidOf      func(int, int) int
}
`

// allViolations runs every rule of the guard on one snippet: the per-file seam rules and the
// literal/conversion/declaration rules.
func allViolations(t *testing.T, src string) []string {
	t.Helper()
	if !strings.Contains(src, "type OSDecoyFS") {
		i := strings.Index(src, "\n")
		src = src[:i] + "\n" + prelude + src[i:]
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	return append(seamViolations(f), literalViolations(fset, []*ast.File{f})...)
}

// fakeImporter resolves every import to an empty complete package: nothing outside the package is
// needed to resolve the package's own struct types, and no build, export data or network is involved.
type fakeImporter struct{ pkgs map[string]*types.Package }

func (f *fakeImporter) Import(path string) (*types.Package, error) {
	if p, ok := f.pkgs[path]; ok {
		return p, nil
	}
	name := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		name = path[i+1:]
	}
	p := types.NewPackage(path, name)
	p.MarkComplete()
	f.pkgs[path] = p
	return p, nil
}

// literalViolations applies the type-based rules of the guard to the files of one package. Type errors
// (from the faked imports, from duplicate tagged declarations, from the positional literals under
// test) are ignored: go/types still records the types of the expressions it can resolve.
func literalViolations(fset *token.FileSet, files []*ast.File) []string {
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: &fakeImporter{pkgs: map[string]*types.Package{}}, Error: func(error) {}, FakeImportC: true}
	pkg, _ := conf.Check("acceptance", fset, files, info)
	var base types.Type // the underlying struct type of OSDecoyFS
	if pkg != nil {
		if o := pkg.Scope().Lookup("OSDecoyFS"); o != nil {
			base = o.Type().Underlying()
		}
	}
	var out []string
	if base == nil { // the type rules would silently pass: fail loudly instead
		out = append(out, "the type OSDecoyFS could not be resolved: the type rules checked nothing")
	}
	sameAsBase := func(t types.Type) bool {
		return base != nil && t != nil && types.Identical(t.Underlying(), base)
	}
	for _, f := range files {
		ast.Inspect(f, func(x ast.Node) bool {
			switch v := x.(type) {
			case *ast.CompositeLit:
				tv, ok := info.Types[v]
				if !ok || tv.Type == nil || len(v.Elts) == 0 {
					return true
				}
				t := tv.Type
				if p, isPtr := t.Underlying().(*types.Pointer); isPtr {
					t = p.Elem() // an elided &T{...} element of a []*T
				}
				if _, isStruct := t.Underlying().(*types.Struct); !isStruct {
					return true
				}
				if _, keyed := v.Elts[0].(*ast.KeyValueExpr); !keyed {
					out = append(out, "has an unkeyed composite literal of a struct type ("+t.String()+")")
				}
			case *ast.CallExpr:
				if tv, ok := info.Types[v.Fun]; ok && tv.IsType() && len(v.Args) == 1 {
					t := tv.Type
					if p, isPtr := t.Underlying().(*types.Pointer); isPtr {
						t = p.Elem()
					}
					if sameAsBase(t) {
						out = append(out, "converts to OSDecoyFS ("+tv.Type.String()+")")
					}
				}
			case *ast.TypeSpec:
				if v.Name.Name == "OSDecoyFS" {
					return true
				}
				if obj := info.Defs[v.Name]; obj != nil && sameAsBase(obj.Type()) {
					out = append(out, "declares the type "+v.Name.Name+" over OSDecoyFS")
				}
			}
			return true
		})
	}
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
		"elided literal in a slice of values": `package p
var x = []OSDecoyFS{{0, 0}}`,
		"elided literal in a slice of pointers": `package p
var x = []*OSDecoyFS{{0, 0}}`,
		"elided literal as a map value": `package p
var x = map[string]OSDecoyFS{"a": {0, 0}}`,
		"elided literal two levels deep": `package p
var x = [][]OSDecoyFS{{{0, 0}}}`,
		"positional literal through a type alias": `package p
type A = OSDecoyFS
var x = A{0, 0}`,
		"positional literal through a defined type": `package p
type D OSDecoyFS
var x = D{0, 0}`,
		"conversion of a struct literal to OSDecoyFS": `package p
var x = OSDecoyFS(struct {
	UID, GID   int
	race       func(string)
	fchown     func(int, int, int) error
	afterMkdir func(int, string)
	uidOf      func(int, int) int
}{0, 0, nil, nil, nil, nil})`,
		"conversion through a pointer": `package p
func f(p *struct {
	UID, GID   int
	race       func(string)
	fchown     func(int, int, int) error
	afterMkdir func(int, string)
	uidOf      func(int, int) int
}) *OSDecoyFS { return (*OSDecoyFS)(p) }`,
		"a type declaration aliasing OSDecoyFS": `package p
type A = OSDecoyFS`,
		"a type declaration over OSDecoyFS": `package p
type D OSDecoyFS`,
	}
	for name, src := range bad {
		if len(allViolations(t, src)) == 0 {
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
		"keyed elided literals, other structs, non-struct literals": `package p
type T struct{ a, b int }
var _ = []OSDecoyFS{{UID: 1}}
var _ = []*OSDecoyFS{{UID: 1, GID: 2}}
var _ = map[string]OSDecoyFS{"a": {UID: 1}}
var _ = T{a: 1, b: 2}
var _ = []int{1, 2}
var _ = map[string]int{"a": 1}
var _ = []string{"x"}
var _ = &OSDecoyFS{}`,
	}
	for name, src := range good {
		if v := allViolations(t, src); len(v) != 0 {
			t.Errorf("false positive on %s: %v", name, v)
		}
	}
}

// If OSDecoyFS cannot be resolved the type rules would silently check nothing; they fail loudly.
func TestSeamGuardFailsLoudlyWhenTheTypeIsMissing(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", "package p\nvar x = []int{1}\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	if v := literalViolations(fset, []*ast.File{f}); len(v) == 0 {
		t.Fatal("a package without OSDecoyFS passed the type rules silently")
	}
}
