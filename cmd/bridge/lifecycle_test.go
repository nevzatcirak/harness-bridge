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

// main itself is not unit-testable, so its lifecycle steps are pinned on its syntax tree: the wiring is
// opened before the server is built (an invalid configuration refuses to start before anything
// listens), applied to that very server, and announced in the startup log.
func TestMainOpensAppliesAndAnnouncesTheLifecycle(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
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
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				name = fun.Name
			case *ast.SelectorExpr:
				if id, ok := fun.X.(*ast.Ident); ok {
					name = id.Name + "." + fun.Sel.Name
				}
			}
			if name == "" {
				return true
			}
			off := fset.Position(call.Pos()).Offset
			if old, seen := first[name]; !seen || off < old {
				first[name] = off
			}
			return true
		})
	}
	for _, need := range []string{"openLifecycle", "newMCPServer", "lc.apply", "lc.summary", "lc.Close"} {
		if _, ok := first[need]; !ok {
			t.Fatalf("main does not call %s", need)
		}
	}
	if first["openLifecycle"] > first["newMCPServer"] {
		t.Error("main opens the lifecycle wiring after building the server")
	}
	if first["lc.apply"] < first["newMCPServer"] {
		t.Error("main applies the lifecycle wiring before the server exists")
	}
	if first["lc.summary"] < first["lc.apply"] {
		t.Error("main announces the lifecycle tools before applying them")
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
