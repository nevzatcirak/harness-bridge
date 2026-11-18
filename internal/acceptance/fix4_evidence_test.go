//go:build unix

package acceptance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"
)

// R317-CLEANUP (#346 P3-3): the evidence file is created without following a symlink in ANY
// component of its path, and never over an existing file.
func TestEvidenceFileIsCreatedWithoutFollowingSymlinks(t *testing.T) {
	root := realDir(t)
	good := filepath.Join(root, "evidence.txt")
	f, err := CreateEvidenceFile(good)
	if err != nil {
		t.Fatalf("plain create: %v", err)
	}
	_, _ = f.WriteString("verdict\n")
	_ = f.Close()
	fi, err := os.Lstat(good)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
		t.Fatalf("evidence file: %v %v", fi, err)
	}
	if f2, err := CreateEvidenceFile(good); err == nil {
		_ = f2.Close()
		t.Fatal("an existing evidence file was reused")
	}
	victimDir := filepath.Join(realDir(t), "victim-dir")
	mkdirs(t, victimDir)
	link := filepath.Join(root, "linkdir")
	if err := os.Symlink(victimDir, link); err != nil {
		t.Fatal(err)
	}
	if f3, err := CreateEvidenceFile(filepath.Join(link, "evidence.txt")); err == nil {
		_ = f3.Close()
		t.Fatal("the evidence file was created through a symlinked ancestor")
	}
	if _, err := os.Stat(filepath.Join(victimDir, "evidence.txt")); err == nil {
		t.Fatal("a file appeared in the symlink target")
	}
	victim := filepath.Join(realDir(t), "victim-file")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "final.txt")); err != nil {
		t.Fatal(err)
	}
	if f4, err := CreateEvidenceFile(filepath.Join(root, "final.txt")); err == nil {
		_ = f4.Close()
		t.Fatal("the evidence file followed a final-component symlink")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatal("the symlink target was written")
	}
	if f5, err := CreateEvidenceFile("relative.txt"); err == nil {
		_ = f5.Close()
		t.Fatal("a relative path was accepted")
	}
}

// R350-CLEANUP (#354 P3-2): a missing parent directory is an error and nothing is created (the
// walk never creates a directory for the evidence file).
func TestEvidenceFileNeverCreatesAMissingParent(t *testing.T) {
	root := realDir(t)
	missing := filepath.Join(root, "missing", "deeper")
	if f, err := CreateEvidenceFile(filepath.Join(missing, "evidence.txt")); err == nil {
		_ = f.Close()
		t.Fatal("the evidence file was created below a missing directory")
	}
	if _, err := os.Lstat(filepath.Join(root, "missing")); err == nil {
		t.Fatal("a missing parent directory was created for the evidence file")
	}
	if es, _ := os.ReadDir(root); len(es) != 0 {
		t.Fatalf("something was created: %v", es)
	}
}

// R359-CLEANUP (#362 P3-2): the missing-parent property must hold for a non-root caller too. Here the
// decoy filesystem's chown succeeds, so a walk that created directories (walk(..., true)) would leave
// them behind: the test fails for it as root and as any other uid.
func TestEvidenceFileNeverCreatesAMissingParentEvenIfChownWouldSucceed(t *testing.T) {
	root := realDir(t)
	o := &OSDecoyFS{UID: os.Getuid(), GID: os.Getgid()}
	o.fchown = func(int, int, int) error { return nil }
	f, err := createEvidenceFile(o, filepath.Join(root, "missing", "deeper", "evidence.txt"))
	if err == nil {
		_ = f.Close()
		t.Fatal("the evidence file was created below a missing directory")
	}
	if es, _ := os.ReadDir(root); len(es) != 0 {
		t.Fatalf("a directory was created for the evidence file: %v", es)
	}
	if len(o.created) != 0 {
		t.Fatalf("the walk recorded created directories: %v", o.created)
	}
	// and the same helper still creates the file when the directory exists
	f, err = createEvidenceFile(o, filepath.Join(root, "evidence.txt"))
	if err != nil {
		t.Fatalf("existing parent: %v", err)
	}
	_ = f.Close()
}

// R365-CLEANUP (#369 P3-3): the exported CreateEvidenceFile must create nothing for a missing parent
// at every uid. A behavioural test cannot see a wrapper that ignores its walker and walks a fresh
// create=true one as a non-root caller (the fresh walker's chown to uid 0 fails with EPERM, the
// directory is removed again, and the result looks like a refusal), so the wrapper's shape is pinned
// on its syntax tree: CreateEvidenceFile is exactly `return createEvidenceFile(&OSDecoyFS{}, p)`, and
// createEvidenceFile walks with its OWN walker parameter and create=false, once, never with a literal
// OSDecoyFS or a walk that creates.
func evidenceWrapperViolations(f *ast.File) []string {
	var out []string
	var wrapper, helper *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
			switch fd.Name.Name {
			case "CreateEvidenceFile":
				wrapper = fd
			case "createEvidenceFile":
				helper = fd
			}
		}
	}
	if wrapper == nil || helper == nil {
		return []string{"CreateEvidenceFile or createEvidenceFile is missing"}
	}
	// the wrapper: one return statement calling the helper with &OSDecoyFS{} and its own path parameter
	pathParam := ""
	if ps := wrapper.Type.Params.List; len(ps) == 1 && len(ps[0].Names) == 1 {
		pathParam = ps[0].Names[0].Name
	}
	okWrapper := false
	if len(wrapper.Body.List) == 1 {
		if ret, ok := wrapper.Body.List[0].(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
			if call, ok := ret.Results[0].(*ast.CallExpr); ok && len(call.Args) == 2 {
				fun, _ := call.Fun.(*ast.Ident)
				u, isUnary := call.Args[0].(*ast.UnaryExpr)
				var lit *ast.CompositeLit
				if isUnary && u.Op == token.AND {
					lit, _ = u.X.(*ast.CompositeLit)
				}
				arg1, _ := call.Args[1].(*ast.Ident)
				if fun != nil && fun.Name == "createEvidenceFile" && lit != nil && len(lit.Elts) == 0 &&
					lit.Type != nil && types.ExprString(lit.Type) == "OSDecoyFS" && arg1 != nil && arg1.Name == pathParam {
					okWrapper = true
				}
			}
		}
	}
	if !okWrapper {
		out = append(out, "CreateEvidenceFile is not exactly `return createEvidenceFile(&OSDecoyFS{}, p)`")
	}
	// the helper: walks with its first parameter, create=false, exactly once, no OSDecoyFS literal
	fsParam := ""
	if ps := helper.Type.Params.List; len(ps) >= 1 && len(ps[0].Names) == 1 {
		fsParam = ps[0].Names[0].Name
	}
	walks := 0
	ast.Inspect(helper.Body, func(x ast.Node) bool {
		switch v := x.(type) {
		case *ast.CompositeLit:
			if v.Type != nil && types.ExprString(v.Type) == "OSDecoyFS" {
				out = append(out, "createEvidenceFile builds its own OSDecoyFS instead of using its parameter")
			}
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "walk" {
				walks++
				recv, _ := sel.X.(*ast.Ident)
				if recv == nil || fsParam == "" || recv.Name != fsParam {
					out = append(out, "createEvidenceFile walks with a walker that is not its parameter")
				}
				if len(v.Args) != 2 {
					out = append(out, "createEvidenceFile calls walk with an unexpected argument list")
				} else if id, ok := v.Args[1].(*ast.Ident); !ok || id.Name != "false" {
					out = append(out, "createEvidenceFile walks with create other than false")
				}
			}
		}
		return true
	})
	if walks != 1 {
		out = append(out, "createEvidenceFile must walk exactly once")
	}
	return out
}

func parseEvidenceSrc(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEvidenceWrapperUsesItsOwnWalkerAndNeverCreates(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "evidencefile_unix.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range evidenceWrapperViolations(f) {
		t.Error(v)
	}
}

// The pin itself: the real shape passes; the #369 P3-3 mutant (ignore fs, fresh create=true walker)
// and its relatives are flagged.
func TestEvidenceWrapperPinCatchesTheIgnoredWalkerMutant(t *testing.T) {
	const wrapper = "func CreateEvidenceFile(p string) (*os.File, error) { return createEvidenceFile(&OSDecoyFS{}, p) }\n"
	body := func(walk string) string {
		return "package p\n" + wrapper + "func createEvidenceFile(fs *OSDecoyFS, p string) (*os.File, error) {\n" + walk + "\n\treturn nil, nil\n}\n"
	}
	good := body("\tparent, err := fs.walk(path.Dir(p), false)\n\t_, _ = parent, err")
	if v := evidenceWrapperViolations(parseEvidenceSrc(t, good)); len(v) != 0 {
		t.Fatalf("the real shape was flagged: %v", v)
	}
	bad := map[string]string{
		"ignores fs, fresh create=true walker (#369 P3-3)": body("\t_ = fs\n\tparent, err := (&OSDecoyFS{}).walk(path.Dir(p), true)\n\t_, _ = parent, err"),
		"uses fs but creates":                              body("\tparent, err := fs.walk(path.Dir(p), true)\n\t_, _ = parent, err"),
		"ignores fs, fresh create=false walker":            body("\t_ = fs\n\tparent, err := (&OSDecoyFS{}).walk(path.Dir(p), false)\n\t_, _ = parent, err"),
		"walks twice":                                      body("\ta, _ := fs.walk(path.Dir(p), false)\n\tb, _ := fs.walk(path.Dir(p), false)\n\t_, _ = a, b"),
		"never walks":                                      body("\t_ = fs"),
		"the wrapper passes a different walker":            "package p\nfunc CreateEvidenceFile(p string) (*os.File, error) { return createEvidenceFile(shared, p) }\nfunc createEvidenceFile(fs *OSDecoyFS, p string) (*os.File, error) {\n\tparent, err := fs.walk(path.Dir(p), false)\n\t_, _ = parent, err\n\treturn nil, nil\n}\n",
		"the wrapper does more than delegate":              "package p\nfunc CreateEvidenceFile(p string) (*os.File, error) { os.MkdirAll(p, 0o700); return createEvidenceFile(&OSDecoyFS{}, p) }\nfunc createEvidenceFile(fs *OSDecoyFS, p string) (*os.File, error) {\n\tparent, err := fs.walk(path.Dir(p), false)\n\t_, _ = parent, err\n\treturn nil, nil\n}\n",
	}
	for name, src := range bad {
		if len(evidenceWrapperViolations(parseEvidenceSrc(t, src))) == 0 {
			t.Errorf("not flagged: %s", name)
		}
	}
}
