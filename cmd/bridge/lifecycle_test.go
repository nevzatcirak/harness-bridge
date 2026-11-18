package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/harness-bridge/internal/adapters/opencode"
	"github.com/nevzatcirak/harness-bridge/internal/execution"
	"github.com/nevzatcirak/harness-bridge/internal/mcp"
	"github.com/nevzatcirak/harness-bridge/internal/mcpclient"
	"github.com/nevzatcirak/harness-bridge/internal/security"

	"net/http/httptest"
)

// ADR-022/ADR-025 production wiring (D22): enabled by default, inert without configuration, a refusal
// to start with an invalid one. These tests build the real wiring and list the tools of an in-process
// MCP server, so the registration is what a client would see.

var baseTools = []string{"find_sessions", "get_execution_result", "get_execution_status", "get_harness_capabilities", "get_repository", "get_session", "interrupt_execution", "list_repositories", "list_sessions", "read_session_history", "send_prompt"}

func lifecycleTool(names ...string) []string {
	return append(append([]string(nil), baseTools...), names...)
}

func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// lcEnv writes a lifecycle config with one root (create and archive modes) and returns the env.
func lcEnv(t *testing.T, overlayDir string) (func(string) string, string) {
	t.Helper()
	work := realDir(t)
	root := filepath.Join(work, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(work, "lifecycle.json")
	body := `{"roots":[{"path":"` + root + `","modes":["create","archive"]}]}`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(k string) string {
		switch k {
		case "BRIDGE_LIFECYCLE_CONFIG":
			return cfgPath
		case "BRIDGE_OVERLAY_DIR":
			return overlayDir
		}
		return ""
	}, cfgPath
}

func newOverlayDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(realDir(t), "overlay")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

// listTools builds the server the way main does and returns the sorted tool names a client sees.
func listTools(t *testing.T, w *lifecycleWiring, readOnly bool) []string {
	t.Helper()
	return listToolsWithRelay(t, w, readOnly, "")
}

// listToolsWithRelay is listTools with the production permission relay wired in the given mode
// ("" wires none, which is the eleven-tool base).
func listToolsWithRelay(t *testing.T, w *lifecycleWiring, readOnly bool, relayMode string) []string {
	t.Helper()
	root := realDir(t)
	client, err := opencode.New("http://127.0.0.1:1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	runs := execution.NewWithTiming(&relayRunner{directory: root}, 30*time.Second, 50*time.Millisecond)
	t.Cleanup(runs.Close)
	pol, err := security.New([]string{root}, []string{"ses_a"})
	if err != nil {
		t.Fatal(err)
	}
	srv := newMCPServer(client, runs, strings.Repeat("t", 32), pol, nil, readOnly, nil)
	w.apply(srv, runs)
	if relayMode != "" {
		if err := wirePermissionRelay(srv, runs, client, relayMode); err != nil {
			t.Fatal(err)
		}
	}
	front := httptest.NewServer(srv)
	t.Cleanup(front.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := mcpclient.Connect(ctx, front.URL, srv.Token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func sameTools(t *testing.T, got, want []string, what string) {
	t.Helper()
	w := append([]string(nil), want...)
	sort.Strings(w)
	if strings.Join(got, ",") != strings.Join(w, ",") {
		t.Errorf("%s: tools\n got  %v\n want %v", what, got, w)
	}
}

// Unset: lifecycle is inert, exactly the tool set of a deployment without the feature.
func TestLifecycleUnsetIsInert(t *testing.T) {
	w, err := openLifecycle(func(string) string { return "" }, os.Geteuid(), false)
	if err != nil || w != nil {
		t.Fatalf("unset: wiring=%v err=%v, want nil and nil", w, err)
	}
	sameTools(t, listTools(t, w, false), baseTools, "unset")
}

// A valid configuration registers list_catalog and create_session; with a valid overlay directory
// the three archive tools too.
func TestLifecycleValidConfigRegistersTheTools(t *testing.T) {
	env, _ := lcEnv(t, "")
	w, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || w == nil {
		t.Fatalf("valid config: %v %v", w, err)
	}
	t.Cleanup(w.Close)
	sameTools(t, listTools(t, w, false), lifecycleTool("list_catalog", "create_session"), "config without overlay")

	env2, _ := lcEnv(t, newOverlayDir(t))
	w2, err := openLifecycle(env2, os.Geteuid(), false)
	if err != nil || w2 == nil {
		t.Fatalf("valid config and overlay: %v %v", w2, err)
	}
	t.Cleanup(w2.Close)
	sameTools(t, listTools(t, w2, false), lifecycleTool("list_catalog", "create_session", "archive_session", "unarchive_session", "close_session"), "config with overlay")
}

// BRIDGE_READ_ONLY hides every mutating lifecycle tool; list_catalog stays, and the overlay (when
// set) is opened without the write role.
func TestLifecycleReadOnlyHidesTheMutatingTools(t *testing.T) {
	env, _ := lcEnv(t, newOverlayDir(t))
	w, err := openLifecycle(env, os.Geteuid(), true)
	if err != nil || w == nil {
		t.Fatalf("read-only: %v %v", w, err)
	}
	t.Cleanup(w.Close)
	sameTools(t, listTools(t, w, true), lifecycleTool("list_catalog"), "read-only")
	if w.create != nil || w.archive != nil {
		t.Fatalf("read-only built a mutating configuration: create=%v archive=%v", w.create, w.archive)
	}
	if w.store != nil && w.store.CanWrite() {
		t.Fatal("read-only opened the overlay with the write role")
	}
}

// Invalid configuration is a startup error, never "start without it".
func TestLifecycleInvalidConfigurationRefusesToStart(t *testing.T) {
	good, cfgPath := lcEnv(t, "")
	uid := os.Geteuid()
	with := func(m map[string]string) func(string) string {
		return func(k string) string {
			if v, ok := m[k]; ok {
				return v
			}
			return good(k)
		}
	}
	bad := map[string]func(string) string{}

	// config file shapes
	if err := os.Chmod(cfgPath, 0o644); err != nil {
		t.Fatal(err)
	}
	bad["config mode 0644"] = good
	work := realDir(t)
	junk := filepath.Join(work, "junk.json")
	_ = os.WriteFile(junk, []byte(`{"roots": [`), 0o600)
	bad["config is not JSON"] = with(map[string]string{"BRIDGE_LIFECYCLE_CONFIG": junk})
	missing := filepath.Join(work, "missing.json")
	bad["config file missing"] = with(map[string]string{"BRIDGE_LIFECYCLE_CONFIG": missing})
	bad["config path relative"] = with(map[string]string{"BRIDGE_LIFECYCLE_CONFIG": "lifecycle.json"})
	noRoots := filepath.Join(work, "noroots.json")
	_ = os.WriteFile(noRoots, []byte(`{"roots":[]}`), 0o600)
	bad["config names no root"] = with(map[string]string{"BRIDGE_LIFECYCLE_CONFIG": noRoots})
	for name, env := range bad {
		if w, err := openLifecycle(env, uid, false); err == nil {
			if w != nil {
				w.Close()
			}
			t.Errorf("%s: started (wiring=%v), want an error", name, w != nil)
		}
	}
}

// A bad overlay directory is a startup error: missing, relative, a symlink, a wrong mode, an
// invalid file for the writer; and the overlay directory without lifecycle roots is refused too.
func TestLifecycleOverlayDirectoryIsValidatedAtStartup(t *testing.T) {
	uid := os.Geteuid()
	base := realDir(t)
	relative := "overlay"
	missing := filepath.Join(base, "nope")
	loose := filepath.Join(base, "loose")
	_ = os.Mkdir(loose, 0o755)
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	_ = os.Mkdir(target, 0o700)
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(base, "invalid")
	_ = os.Mkdir(invalid, 0o700)
	_ = os.WriteFile(filepath.Join(invalid, "overlay.json"), []byte(`not json`), 0o600)

	for name, dir := range map[string]string{"relative": relative, "missing": missing, "mode 0755": loose, "symlink final component": link, "invalid overlay file (writer)": invalid} {
		env, _ := lcEnv(t, dir)
		if w, err := openLifecycle(env, uid, false); err == nil {
			if w != nil {
				w.Close()
			}
			t.Errorf("overlay dir %s: started, want an error", name)
		}
	}
	// the overlay directory without a lifecycle configuration is not silently ignored
	only := func(k string) string {
		if k == "BRIDGE_OVERLAY_DIR" {
			return target
		}
		return ""
	}
	if w, err := openLifecycle(only, uid, false); err == nil {
		if w != nil {
			w.Close()
		}
		t.Error("BRIDGE_OVERLAY_DIR without BRIDGE_LIFECYCLE_CONFIG started")
	}
}

// A second writer on the same overlay directory does not get the write role: it keeps the catalog
// and create tools but registers no archive tool (ADR-025 4.3).
func TestLifecycleSecondWriterHasNoArchiveTools(t *testing.T) {
	dir := newOverlayDir(t)
	env, _ := lcEnv(t, dir)
	first, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || first == nil || first.archive == nil {
		t.Fatalf("first writer: %v %v", first, err)
	}
	t.Cleanup(first.Close)
	second, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || second == nil {
		t.Fatalf("second writer: %v %v", second, err)
	}
	t.Cleanup(second.Close)
	if second.archive != nil {
		t.Fatal("a second writer registered the archive tools without the write lock")
	}
	sameTools(t, listTools(t, second, false), lifecycleTool("list_catalog", "create_session"), "second writer")
}

// The startup line names the registered tools and the root count and contains no path.
func TestLifecycleSummaryNamesToolsAndRootsWithoutPaths(t *testing.T) {
	dir := newOverlayDir(t)
	env, cfgPath := lcEnv(t, dir)
	w, err := openLifecycle(env, os.Geteuid(), false)
	if err != nil || w == nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	s := w.summary(false)
	for _, want := range []string{"list_catalog", "create_session", "archive_session", "unarchive_session", "close_session", "roots=1"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary %q lacks %q", s, want)
		}
	}
	for _, leak := range []string{dir, cfgPath, "/"} {
		if strings.Contains(s, leak) {
			t.Errorf("summary %q contains %q", s, leak)
		}
	}
	var none *lifecycleWiring
	if got := none.summary(false); !strings.Contains(got, "no lifecycle tools") || !strings.Contains(got, "roots=0") {
		t.Errorf("inert summary %q", got)
	}
	rw, err := openLifecycle(func(k string) string {
		if k == "BRIDGE_OVERLAY_DIR" {
			return newOverlayDir(t)
		}
		return env(k)
	}, os.Geteuid(), true)
	if err != nil || rw == nil {
		t.Fatal(err)
	}
	t.Cleanup(rw.Close)
	ro := rw.summary(true)
	if strings.Contains(ro, "create_session") || strings.Contains(ro, "archive_session") || !strings.Contains(ro, "list_catalog") || !strings.Contains(ro, "read-only") {
		t.Errorf("read-only summary %q", ro)
	}
}

var _ = mcp.CatalogConfig{}

// main itself is not unit-testable, so its lifecycle steps are pinned on its syntax tree (#379 P3-1):
//   - openLifecycle is a statement of main's body and is followed at once by an `if err != nil` whose first
//     statement is log.Fatal (an invalid configuration refuses to start; log.Print would start without it);
//   - `defer lc.Close()` follows;
//   - the wiring is applied to the very server newMCPServer returned, and to the manager main made
//     (lc.apply(<server>, runs)), after it was built, and announced after that (lc.summary);
//   - nothing listens (ListenAndServe, ListenAndServeTLS, Serve, Listen) before the wiring is opened.
//
// mainLifecycleProblems returns what is wrong with the source of main.go, nothing when it is right.
func mainLifecycleProblems(src string) []string {
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
	callName := func(c *ast.CallExpr) string {
		switch fun := c.Fun.(type) {
		case *ast.Ident:
			return fun.Name
		case *ast.SelectorExpr:
			if id, ok := fun.X.(*ast.Ident); ok {
				return id.Name + "." + fun.Sel.Name
			}
			return "." + fun.Sel.Name
		}
		return ""
	}
	offset := func(n ast.Node) int { return fset.Position(n.Pos()).Offset }
	// topCall returns the call a top-level statement makes (assignment right side, expression, defer).
	topCall := func(st ast.Stmt) *ast.CallExpr {
		switch s := st.(type) {
		case *ast.AssignStmt:
			if len(s.Rhs) == 1 {
				c, _ := s.Rhs[0].(*ast.CallExpr)
				return c
			}
		case *ast.ExprStmt:
			c, _ := s.X.(*ast.CallExpr)
			return c
		case *ast.DeferStmt:
			return s.Call
		}
		return nil
	}
	var problems []string
	stmts := body.List
	openIdx, serverIdx, applyIdx, summaryIdx := -1, -1, -1, -1
	server := ""
	for i, st := range stmts {
		as, isAssign := st.(*ast.AssignStmt)
		c := topCall(st)
		if c == nil {
			// log.Print(lc.summary(...)) is an expression statement whose argument is the call.
			continue
		}
		switch callName(c) {
		case "openLifecycle":
			if _, isDefer := st.(*ast.DeferStmt); !isDefer && isAssign && openIdx < 0 {
				openIdx = i
			}
		case "newMCPServer":
			if isAssign && serverIdx < 0 {
				if id, ok := as.Lhs[0].(*ast.Ident); ok {
					server, serverIdx = id.Name, i
				}
			}
		case "lc.apply":
			if applyIdx < 0 {
				applyIdx = i
				if len(c.Args) != 2 {
					problems = append(problems, "lc.apply does not take (server, runs)")
				} else {
					a0, ok0 := c.Args[0].(*ast.Ident)
					a1, ok1 := c.Args[1].(*ast.Ident)
					if !ok0 || a0.Name != server || server == "" {
						problems = append(problems, "lc.apply is not applied to the server newMCPServer returned")
					}
					if !ok1 || a1.Name != "runs" {
						problems = append(problems, "lc.apply is not applied with the execution manager `runs`")
					}
				}
			}
		case "log.Print", "log.Printf":
			for _, a := range c.Args {
				if ac, ok := a.(*ast.CallExpr); ok && callName(ac) == "lc.summary" && summaryIdx < 0 {
					summaryIdx = i
				}
			}
		}
	}
	switch {
	case openIdx < 0:
		return append(problems, "main does not call openLifecycle as a top-level assignment")
	case serverIdx < 0:
		return append(problems, "main does not build the server with newMCPServer as a top-level assignment")
	case applyIdx < 0:
		return append(problems, "main does not apply the lifecycle wiring (lc.apply)")
	case summaryIdx < 0:
		return append(problems, "main does not announce the lifecycle tools (log.Print(lc.summary(...)))")
	}
	// Fail closed: the statement right after openLifecycle is `if err != nil { log.Fatal(...) ... }`.
	if openIdx+1 >= len(stmts) {
		return append(problems, "nothing follows openLifecycle")
	}
	ifs, ok := stmts[openIdx+1].(*ast.IfStmt)
	if !ok {
		problems = append(problems, "the statement after openLifecycle is not an if on its error")
	} else {
		cond, isBin := ifs.Cond.(*ast.BinaryExpr)
		if !isBin || cond.Op != token.NEQ {
			problems = append(problems, "the error check after openLifecycle is not `err != nil`")
		}
		fatal := false
		if len(ifs.Body.List) > 0 {
			if es, ok := ifs.Body.List[0].(*ast.ExprStmt); ok {
				if c, ok := es.X.(*ast.CallExpr); ok {
					n := callName(c)
					fatal = n == "log.Fatal" || n == "log.Fatalf" || n == "log.Fatalln"
				}
			}
		}
		if !fatal {
			problems = append(problems, "an openLifecycle error does not end main with log.Fatal (an invalid configuration would start)")
		}
	}
	// defer lc.Close() right after the check.
	deferred := false
	if openIdx+2 < len(stmts) {
		if d, ok := stmts[openIdx+2].(*ast.DeferStmt); ok && callName(d.Call) == "lc.Close" {
			deferred = true
		}
	}
	if !deferred {
		problems = append(problems, "`defer lc.Close()` does not follow the openLifecycle error check")
	}
	if serverIdx < openIdx {
		problems = append(problems, "the server is built before the lifecycle wiring is opened")
	}
	if applyIdx < serverIdx {
		problems = append(problems, "the wiring is applied before the server exists")
	}
	if summaryIdx < applyIdx {
		problems = append(problems, "the lifecycle tools are announced before they are applied")
	}
	// Nothing listens before the wiring is opened (and, in fact, before it is applied).
	open, apply := offset(stmts[openIdx]), offset(stmts[applyIdx])
	ast.Inspect(body, func(x ast.Node) bool {
		c, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch n := callName(c); n {
		case ".ListenAndServe", ".ListenAndServeTLS", ".Serve", ".Listen", "net.Listen", "http.ListenAndServe", "http.ListenAndServeTLS":
			if p := offset(c); p < open {
				problems = append(problems, "a listener ("+n+") is opened before the lifecycle wiring")
			} else if p < apply {
				problems = append(problems, "a listener ("+n+") is opened before the lifecycle wiring is applied")
			}
		}
		return true
	})
	return problems
}

func TestMainOpensAppliesAndAnnouncesTheLifecycle(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if p := mainLifecycleProblems(src); len(p) > 0 {
		t.Fatalf("main.go: %s", strings.Join(p, "; "))
	}
	// The pin must see each mutation of main that breaks the property (#379 P3-1).
	for _, m := range []struct{ name, old, new string }{
		{"an invalid configuration is only logged", "lc, err := openLifecycle(os.Getenv, os.Geteuid(), cfg.readOnly)\n\tif err != nil {\n\t\tlog.Fatal(err)", "lc, err := openLifecycle(os.Getenv, os.Geteuid(), cfg.readOnly)\n\tif err != nil {\n\t\tlog.Print(err)"},
		{"the wiring is applied to another server", "lc.apply(mcpServer, runs)", "lc.apply(newMCPServer(client, runs, token, policy, cfg.remote, cfg.readOnly, claude), runs)"},
		{"the wiring is never closed", "defer lc.Close()", "lc.Close()"},
		{"the wiring is never applied", "lc.apply(mcpServer, runs)\n", "\n"},
		{"the wiring is not announced", "log.Print(lc.summary(cfg.readOnly))", "log.Print(\"lifecycle\")"},
		{"a listener opens first", "func main() {\n", "func main() {\n\tgo http.ListenAndServe(\"127.0.0.1:0\", nil)\n"},
	} {
		if strings.Count(src, m.old) != 1 {
			t.Fatalf("mutation %q: the text it replaces occurs %d times in main.go; update the pin", m.name, strings.Count(src, m.old))
		}
		if p := mainLifecycleProblems(strings.Replace(src, m.old, m.new, 1)); len(p) == 0 {
			t.Errorf("mutation %q is not caught by the main pin", m.name)
		}
	}
}

// The tool counts stated in docs/CONTRACTS.md ("Registration and production wiring of the five
// lifecycle tools"): base eleven, reply_permission is the twelfth under the default reject_only relay.
func TestLifecycleToolCountsMatchTheContracts(t *testing.T) {
	uid := os.Geteuid()
	type row struct {
		name     string
		w        func() *lifecycleWiring
		readOnly bool
		want     map[string]int // relay mode -> count
	}
	open := func(overlay bool, readOnly bool) func() *lifecycleWiring {
		return func() *lifecycleWiring {
			dir := ""
			if overlay {
				dir = newOverlayDir(t)
			}
			env, _ := lcEnv(t, dir)
			w, err := openLifecycle(env, uid, readOnly)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(w.Close)
			return w
		}
	}
	rows := []row{
		{"no lifecycle configuration", func() *lifecycleWiring { return nil }, false, map[string]int{"reject_only": 12, "off": 11}},
		{"configuration, no overlay", open(false, false), false, map[string]int{"reject_only": 14, "off": 13}},
		{"configuration and overlay", open(true, false), false, map[string]int{"reject_only": 17, "off": 16}},
		{"configuration, read-only", open(false, true), true, map[string]int{"reject_only": 13, "off": 12}},
		{"configuration and overlay, read-only", open(true, true), true, map[string]int{"reject_only": 13, "off": 12}},
	}
	for _, r := range rows {
		for mode, want := range r.want {
			if got := len(listToolsWithRelay(t, r.w(), r.readOnly, mode)); got != want {
				t.Errorf("%s, relay %s: %d tools, want %d (docs/CONTRACTS.md)", r.name, mode, got, want)
			}
		}
	}
}
