package acceptance

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// R317-FIX-3 P3-3: item 6 registers the cleanup of the decoy file only after a successful create, so
// a file that already existed (the operator's, or a leftover) is never removed by the driver.
func TestItem6NeverRemovesAFileItDidNotCreate(t *testing.T) {
	d, _, fsys := newTestDriver(t, &sim{})
	pre := tHome + "/.claude/.credentials.decoy.json"
	fsys.content[pre] = []byte("operator data")
	fsys.failOn = ".credentials.decoy.json" // the create of the decoy fails (it already exists)
	v := verdict(d.RunItems(context.Background(), []int{6}), 6)
	if v.Result == Pass {
		t.Fatalf("item 6 PASS although the decoy could not be created: %+v", v)
	}
	if string(fsys.get(pre)) != "operator data" {
		t.Fatal("item 6 removed a file it did not create")
	}
}

// R317-FIX-3 P3-2: no exported identifier of the claudecode package assumes that the unobserved
// checks were made (AllObserved was the last public way to ask for that).
func TestNoExportedAllObserved(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "../adapters/claudecode/canary.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, sp := range gd.Specs {
			if vs, ok := sp.(*ast.ValueSpec); ok {
				for _, n := range vs.Names {
					if n.IsExported() && strings.Contains(n.Name, "AllObserved") {
						t.Errorf("canary.go exports %s", n.Name)
					}
				}
			}
		}
	}
}

// R317-FIX-3 P3-5 / #340 P2-1: the live command's header does not tell the operator to build into
// /tmp or to give the binary a file capability.
func TestLiveCommandHeaderDoesNotSuggestACapabilityOrTmp(t *testing.T) {
	b, err := os.ReadFile("../../cmd/hb-claude-acceptance/main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	head := s[:strings.Index(s, "package main")]
	for _, bad := range []string{"setcap", "cap_chown+ep", "-o /tmp/", "/tmp/hb-claude-acceptance"} {
		if strings.Contains(head, bad) {
			t.Errorf("the header still suggests %q", bad)
		}
	}
	if !strings.Contains(head, "root-owned") {
		t.Error("the header does not say to build/install into a root-owned directory")
	}
}
