package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Cleanup sweep C (#434), review #198 (A) P3-1: ListenAndServe returns http.ErrServerClosed as soon as the MCP
// server's Shutdown BEGINS, not when it ends, so main used to return while the shutdown goroutine was still
// running: dash.Shutdown (which closes the dashboard listener and removes its unix socket) could be cut off. main
// must wait for that goroutine. Like mainLifecycleProblems this is a structural pin of main.go (main cannot be run
// as a test); the live behaviour at a real SIGTERM is UNVERIFIED.
//
//   - a top-level `done := make(chan struct{})`;
//   - a top-level `go func() { defer close(done); ...; <shutdown calls> }()` that shuts the MCP server and the
//     dashboard down (calls to X.Shutdown for both srv and dash);
//   - AFTER the serve call, a receive `<-done`, either bare or inside an `if` that mentions http.ErrServerClosed.
func mainShutdownProblems(src string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, 0)
	if err != nil {
		return []string{"main.go does not parse: " + err.Error()}
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "main" {
			body = fd.Body
		}
	}
	if body == nil {
		return []string{"no func main"}
	}
	off := func(n ast.Node) int { return fset.Position(n.Pos()).Offset }
	sel := func(c *ast.CallExpr) (recv, name string) {
		if s, ok := c.Fun.(*ast.SelectorExpr); ok {
			if id, ok := s.X.(*ast.Ident); ok {
				return id.Name, s.Sel.Name
			}
		}
		if id, ok := c.Fun.(*ast.Ident); ok {
			return "", id.Name
		}
		return "", ""
	}
	var problems []string
	done := ""
	for _, st := range body.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		c, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || len(c.Args) != 1 {
			continue
		}
		if _, isMake := c.Fun.(*ast.Ident); !isMake || c.Fun.(*ast.Ident).Name != "make" {
			continue
		}
		if ct, ok := c.Args[0].(*ast.ChanType); ok {
			if st, ok := ct.Value.(*ast.StructType); ok && len(st.Fields.List) == 0 {
				if id, ok := as.Lhs[0].(*ast.Ident); ok {
					done = id.Name
				}
			}
		}
	}
	if done == "" {
		return []string{"main has no top-level `done := make(chan struct{})` for the shutdown goroutine"}
	}
	goOff := -1
	for _, st := range body.List {
		gs, ok := st.(*ast.GoStmt)
		if !ok {
			continue
		}
		lit, ok := gs.Call.Fun.(*ast.FuncLit)
		if !ok {
			continue
		}
		deferredClose := false
		shut := map[string]bool{}
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.DeferStmt:
				if r, name := sel(x.Call); r == "" && name == "close" && len(x.Call.Args) == 1 {
					if id, ok := x.Call.Args[0].(*ast.Ident); ok && id.Name == done {
						deferredClose = true
					}
				}
			case *ast.CallExpr:
				if r, name := sel(x); name == "Shutdown" && r != "" {
					shut[r] = true
				}
			}
			return true
		})
		if deferredClose && shut["srv"] && shut["dash"] {
			goOff = off(gs)
		}
	}
	if goOff < 0 {
		problems = append(problems, "no top-level goroutine that shuts srv and dash down and does `defer close("+done+")`")
	}
	serveOff := -1
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if r, name := sel(c); r == "srv" && (name == "ListenAndServe" || name == "ListenAndServeTLS") && serveOff < 0 {
				serveOff = off(c)
			}
		}
		return true
	})
	if serveOff < 0 {
		problems = append(problems, "main does not call srv.ListenAndServe(TLS)")
	}
	isRecv := func(n ast.Node) bool {
		es, ok := n.(*ast.ExprStmt)
		if !ok {
			return false
		}
		u, ok := es.X.(*ast.UnaryExpr)
		if !ok || u.Op != token.ARROW {
			return false
		}
		id, ok := u.X.(*ast.Ident)
		return ok && id.Name == done
	}
	waits := false
	for _, st := range body.List {
		if off(st) < serveOff {
			if isRecv(st) {
				problems = append(problems, "main waits for the shutdown goroutine BEFORE it serves: it would never start")
			}
			continue
		}
		if isRecv(st) {
			waits = true
		}
		if ifs, ok := st.(*ast.IfStmt); ok {
			cond := src[off(ifs.Cond):fset.Position(ifs.Cond.End()).Offset]
			if strings.Contains(cond, "ErrServerClosed") && !strings.Contains(cond, "!=") {
				for _, b := range ifs.Body.List {
					if isRecv(b) {
						waits = true
					}
				}
			}
		}
	}
	if !waits {
		problems = append(problems, "main does not wait for the shutdown goroutine (`<-"+done+"`) after the serve call returns: dash.Shutdown can be cut off")
	}
	return problems
}

func TestMainWaitsForTheShutdownGoroutine(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if p := mainShutdownProblems(src); len(p) > 0 {
		t.Fatalf("main.go: %s", strings.Join(p, "; "))
	}
	// The pin must see each mutation of main that breaks the property.
	for _, m := range []struct{ name, old, new string }{
		{"main does not wait", "\tif serveErr == http.ErrServerClosed {\n\t\t<-shutdownDone\n\t}\n", ""},
		{"the wait is unconditional but before the serve call", "\tlog.Printf(\"Bridge listening on %s\", cfg.addr)\n", "\t<-shutdownDone\n\tlog.Printf(\"Bridge listening on %s\", cfg.addr)\n"},
		{"the channel is never closed (not deferred)", "\t\tdefer close(shutdownDone)\n", "\t\tclose(shutdownDone)\n"},
		{"the goroutine no longer shuts the dashboard down", "\t\tif dash != nil {\n\t\t\t_ = dash.Shutdown(c)\n\t\t}\n", ""},
	} {
		if strings.Count(src, m.old) != 1 {
			t.Fatalf("mutation %q: the text it replaces occurs %d times in main.go; update the pin", m.name, strings.Count(src, m.old))
		}
		if p := mainShutdownProblems(strings.Replace(src, m.old, m.new, 1)); len(p) == 0 {
			t.Errorf("mutation %q is not caught by the shutdown pin", m.name)
		}
	}
}
