package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// The 31 names of ADR-026 section 2.2, written out independently of the implementation so that a
// name missing from the shim's table is a failing test, not a silent gap.
var adr026Names = []string{
	"ADMIN_SOCKET", "ALLOWED_ROOTS", "ALLOWED_SESSIONS", "CLAUDE_COMMAND", "CLAUDE_HISTORY_DIR", "CLAUDE_HOME",
	"CLAUDE_LANG", "CLAUDE_MAX_BUDGET_USD", "CLAUDE_MAX_TURNS", "CLAUDE_MODEL", "CLAUDE_RUN_MAX", "CLAUDE_SESSIONS",
	"CLAUDE_VERSIONS", "DASHBOARD_ADDR", "DASHBOARD_REDACT_EXTRA", "DASHBOARD_SOCKET", "LIFECYCLE_CONFIG", "LISTEN",
	"OVERLAY_DIR", "PERMISSION_INCARNATION", "PERMISSION_RELAY", "PRINCIPAL", "PUBLIC_ORIGIN", "READ_ONLY",
	"TLS_CERT", "TLS_KEY", "TOKEN", "TOKEN_EXPIRES", "TOKEN_FILE", "TOKEN_MAX_LIFETIME", "WRITER_LOCK_DIR",
}

func mapEnv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

const (
	secretNew    = "new-value-AAAA"
	secretLegacy = "legacy-value-BBBB"
)

func TestEnvTableIsTheADRTable(t *testing.T) {
	if len(adr026Names) != 31 {
		t.Fatalf("the test list holds %d names, ADR-026 section 2.2 has 31", len(adr026Names))
	}
	got := append([]string(nil), envSuffixes...)
	want := append([]string(nil), adr026Names...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the shim table differs from ADR-026 section 2.2:\n got  %v\n want %v", got, want)
	}
}

// ADR-026 section 2.3, every row, for every name (the _FILE form is the TOKEN_FILE name).
func TestCirakEnvRule(t *testing.T) {
	for _, s := range adr026Names {
		n, l := "CIRAK_"+s, "BRIDGE_"+s
		warn := "legacy environment variable " + l + " is deprecated; set " + n + " instead (not accepted from v0.2.0)"
		for _, c := range []struct {
			name     string
			env      map[string]string
			want     string
			warnings []string
		}{
			{"neither", nil, "", nil},
			{"both empty", map[string]string{n: "", l: ""}, "", nil},
			{"new only", map[string]string{n: secretNew}, secretNew, nil},
			{"new set, legacy empty", map[string]string{n: secretNew, l: ""}, secretNew, nil},
			{"legacy only", map[string]string{l: secretLegacy}, secretLegacy, []string{warn}},
			{"new empty, legacy set", map[string]string{n: "", l: secretLegacy}, secretLegacy, []string{warn}},
			{"equal", map[string]string{n: secretNew, l: secretNew}, secretNew, []string{warn}},
		} {
			env, warnings, err := cirakEnv(mapEnv(c.env), phaseAccept)
			if err != nil {
				t.Fatalf("%s %s: %v", s, c.name, err)
			}
			if got := env(n); got != c.want {
				t.Errorf("%s %s: %s resolved to %q, want %q", s, c.name, n, got, c.want)
			}
			if strings.Join(warnings, "\n") != strings.Join(c.warnings, "\n") {
				t.Errorf("%s %s: warnings %q, want %q", s, c.name, warnings, c.warnings)
			}
			for _, w := range warnings {
				if strings.Contains(w, secretNew) || strings.Contains(w, secretLegacy) {
					t.Errorf("%s %s: a warning printed a value: %q", s, c.name, w)
				}
			}
		}
		// different values: refuse, naming both variables and no value.
		_, _, err := cirakEnv(mapEnv(map[string]string{n: secretNew, l: secretLegacy}), phaseAccept)
		if err == nil {
			t.Errorf("%s: different values were accepted", s)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, n) || !strings.Contains(msg, l) {
			t.Errorf("%s: the refusal does not name both variables: %q", s, msg)
		}
		if strings.Contains(msg, secretNew) || strings.Contains(msg, secretLegacy) {
			t.Errorf("%s: the refusal printed a value: %q", s, msg)
		}
	}
}

// Values are compared exactly: no case folding, no trimming; whitespace is a value, not "empty".
func TestCirakEnvCompareIsExact(t *testing.T) {
	for _, c := range []struct{ name, a, b string }{
		{"case", "Value", "value"},
		{"trailing space", "value", "value "},
		{"leading space", " value", "value"},
		{"newline", "value\n", "value"},
	} {
		_, _, err := cirakEnv(mapEnv(map[string]string{"CIRAK_LISTEN": c.a, "BRIDGE_LISTEN": c.b}), phaseAccept)
		if err == nil {
			t.Errorf("%s: %q and %q were treated as equal", c.name, c.a, c.b)
		}
	}
	env, warnings, err := cirakEnv(mapEnv(map[string]string{"BRIDGE_LISTEN": " "}), phaseAccept)
	if err != nil || env("CIRAK_LISTEN") != " " || len(warnings) != 1 {
		t.Errorf("a whitespace-only legacy value is a value: %q %v %v", env("CIRAK_LISTEN"), warnings, err)
	}
}

// The scan covers every known name up front, so a conflict in a feature that is off still refuses.
func TestCirakEnvScansEveryNameUpFront(t *testing.T) {
	for _, s := range []string{"DASHBOARD_ADDR", "DASHBOARD_SOCKET", "DASHBOARD_REDACT_EXTRA", "OVERLAY_DIR", "LIFECYCLE_CONFIG", "CLAUDE_COMMAND", "ADMIN_SOCKET"} {
		_, _, err := cirakEnv(mapEnv(map[string]string{"CIRAK_" + s: "a", "BRIDGE_" + s: "b"}), phaseAccept)
		if err == nil {
			t.Errorf("%s: a conflict in a name nothing reads yet was not found", s)
		}
	}
	// several conflicts are all named
	_, _, err := cirakEnv(mapEnv(map[string]string{"CIRAK_LISTEN": "a", "BRIDGE_LISTEN": "b", "CIRAK_TOKEN": "c", "BRIDGE_TOKEN": "d"}), phaseAccept)
	if err == nil || !strings.Contains(err.Error(), "BRIDGE_LISTEN") || !strings.Contains(err.Error(), "BRIDGE_TOKEN") {
		t.Errorf("every conflict is named: %v", err)
	}
}

// Names the daemon does not rename pass through untouched.
func TestCirakEnvPassThrough(t *testing.T) {
	env, warnings, err := cirakEnv(mapEnv(map[string]string{"OPENCODE_URL": "http://x", "OPENCODE_SERVER_PASSWORD_FILE": "/p", "CREDENTIALS_DIRECTORY": "/c", "BRIDGE_UNKNOWN": "u", "CIRAK_UNKNOWN": "v"}), phaseAccept)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("%v %v", warnings, err)
	}
	for k, want := range map[string]string{"OPENCODE_URL": "http://x", "OPENCODE_SERVER_PASSWORD_FILE": "/p", "CREDENTIALS_DIRECTORY": "/c", "BRIDGE_UNKNOWN": "u", "CIRAK_UNKNOWN": "v", "NOT_SET": ""} {
		if got := env(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// ADR-026 section 2.3, the window: v0.1.x accepts with a warning, v0.2.x refuses, v0.3.0 ignores.
func TestLegacyPhaseFor(t *testing.T) {
	for v, want := range map[string]legacyPhase{
		"devel": phaseAccept, "unknown": phaseAccept, "": phaseAccept, "(devel)": phaseAccept,
		"v0.0.0-20261121034852-e2ac772bf94c": phaseAccept, "v0.1.0": phaseAccept, "v0.1.7": phaseAccept,
		"v0.1.1-0.20261201000000-abcdef123456": phaseAccept, "v0.1.0+dirty": phaseAccept,
		"v0.2.0": phaseRefuse, "v0.2.9": phaseRefuse, "v0.2.1-0.20270101000000-abcdef123456": phaseRefuse,
		"v0.3.0": phaseIgnore, "v0.10.0": phaseIgnore, "v1.0.0": phaseIgnore, "v2.3.4+incompatible": phaseIgnore,
	} {
		if got := legacyPhaseFor(v); got != want {
			t.Errorf("legacyPhaseFor(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestCirakEnvAfterTheWindow(t *testing.T) {
	legacy := map[string]string{"BRIDGE_READ_ONLY": "1", "BRIDGE_ALLOWED_ROOTS": "/r", "CIRAK_TOKEN": secretNew}
	// v0.2.x: any known legacy name set is a startup error, even when the new name is also set and equal.
	_, _, err := cirakEnv(mapEnv(legacy), phaseRefuse)
	if err == nil || !strings.Contains(err.Error(), "BRIDGE_READ_ONLY is no longer supported; use CIRAK_READ_ONLY") ||
		!strings.Contains(err.Error(), "BRIDGE_ALLOWED_ROOTS is no longer supported; use CIRAK_ALLOWED_ROOTS") || strings.Contains(err.Error(), "/r") {
		t.Fatalf("v0.2.x: %v", err)
	}
	if _, _, err := cirakEnv(mapEnv(map[string]string{"CIRAK_READ_ONLY": "1", "BRIDGE_READ_ONLY": "1"}), phaseRefuse); err == nil {
		t.Fatal("v0.2.x: an equal legacy twin was accepted")
	}
	if _, _, err := cirakEnv(mapEnv(map[string]string{"CIRAK_READ_ONLY": "1", "BRIDGE_READ_ONLY": ""}), phaseRefuse); err != nil {
		t.Fatalf("v0.2.x: an empty legacy twin is unset: %v", err)
	}
	// v0.3.0: legacy names are ignored; the value is the new one only, and nothing is warned.
	env, warnings, err := cirakEnv(mapEnv(legacy), phaseIgnore)
	if err != nil || len(warnings) != 0 || env("CIRAK_READ_ONLY") != "" || env("CIRAK_ALLOWED_ROOTS") != "" || env("CIRAK_TOKEN") != secretNew {
		t.Fatalf("v0.3.0: %q %q %v %v", env("CIRAK_READ_ONLY"), env("CIRAK_ALLOWED_ROOTS"), warnings, err)
	}
}

// The merged view feeds the existing rules: TOKEN_FILE with a legacy BRIDGE_TOKEN is still the
// "mutually exclusive" refusal, and the H5 idiom (an empty BRIDGE_TOKEN=) still neutralises.
func TestMergedViewKeepsTheExistingRules(t *testing.T) {
	both, _, err := cirakEnv(mapEnv(map[string]string{"BRIDGE_TOKEN": token32, "CIRAK_TOKEN_FILE": "/x"}), phaseAccept)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadStartup(both); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("CIRAK_TOKEN_FILE with BRIDGE_TOKEN: %v", err)
	}
	legacyFile, _, err := cirakEnv(mapEnv(map[string]string{"BRIDGE_TOKEN_FILE": "/x", "BRIDGE_TOKEN": ""}), phaseAccept)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadStartup(legacyFile); err == nil || strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("the H5 idiom BRIDGE_TOKEN= with a token file: %v", err)
	}
	neutral, warnings, err := cirakEnv(mapEnv(map[string]string{"CIRAK_TOKEN": token32, "BRIDGE_TOKEN": ""}), phaseAccept)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("%v %v", warnings, err)
	}
	if tok, _, err := loadStartup(neutral); err != nil || tok != token32 {
		t.Fatalf("an empty legacy twin must neutralise, not conflict: %v", err)
	}
}

// main resolves the environment first and reads nothing but through the resolved view.
func TestMainResolvesTheEnvironmentFirst(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if p := mainEnvProblems(string(raw)); len(p) != 0 {
		t.Fatal(strings.Join(p, "; "))
	}
	for _, m := range []struct{ name, old, new string }{
		{"a direct os.Getenv read comes back", "allSessions := make([]string, 0, len(policy.Sessions))", "_ = os.Getenv(\"CIRAK_WRITER_LOCK_DIR\")\n\tallSessions := make([]string, 0, len(policy.Sessions))"},
		{"loadStartup runs before the scan", "envOnly, warnings, err := cirakEnv(", "token0, cfg0, err0 := loadStartup(os.Getenv)\n\t_, _, _ = token0, cfg0, err0\n\tenvOnly, warnings, err := cirakEnv("},
		{"a reader runs before the config layer", "token, cfg, err := loadStartup(env)", "token, cfg, err := loadStartup(envOnly)"},
		{"the scan result is not checked", "envOnly, warnings, err := cirakEnv(os.Getenv, legacyPhaseFor(version.Version()))\n\tif err != nil {\n\t\tlog.Fatal(err)\n\t}", "envOnly, warnings, _ := cirakEnv(os.Getenv, legacyPhaseFor(version.Version()))"},
		// ADR-017 A2 S3: the thresholds are validated but the Manager runs with the defaults.
		{"the Manager runs without the parsed thresholds", "runs := execution.NewWithLiveness(client, liveness)", "runs := execution.New(client)\n\t_ = liveness"},
		{"the Manager gets fresh defaults instead of the parsed thresholds", "runs := execution.NewWithLiveness(client, liveness)", "runs := execution.NewWithLiveness(client, execution.LivenessConfig{})"},
	} {
		src := string(raw)
		if strings.Count(src, m.old) != 1 {
			t.Fatalf("mutation %q: the text it replaces occurs %d times in main.go; update the pin", m.name, strings.Count(src, m.old))
		}
		if len(mainEnvProblems(strings.Replace(src, m.old, m.new, 1))) == 0 {
			t.Errorf("mutation %q survived", m.name)
		}
	}
}

func mainEnvProblems(src string) []string {
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
	var problems []string
	scan := token.NoPos
	firstUse := token.NoPos
	checked := false
	readers := map[string]bool{"loadStartup": true, "openClaude": true, "openLifecycle": true, "openAdmin": true, "openDashboard": true,
		"acquireOpenCodeOwnership": true, "runAdmin": true, "permissionRelayMode": true, "permissionIncarnationMode": true, "readSecret": true}
	for _, st := range body.List {
		if as, ok := st.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
			if c, ok := as.Rhs[0].(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "cirakEnv" && scan == token.NoPos {
					scan = c.Pos()
				}
			}
		}
	}
	if scan == token.NoPos {
		return []string{"main never calls cirakEnv"}
	}
	for i, st := range body.List {
		if ifs, ok := st.(*ast.IfStmt); ok && i > 0 && ifs.Pos() > scan && !checked {
			if prev, ok := body.List[i-1].(*ast.AssignStmt); ok && len(prev.Rhs) == 1 {
				if c, ok := prev.Rhs[0].(*ast.CallExpr); ok {
					if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "cirakEnv" {
						if b, ok := ifs.Cond.(*ast.BinaryExpr); ok && b.Op == token.NEQ {
							checked = true
						}
					}
				}
			}
		}
	}
	if !checked {
		problems = append(problems, "the error of cirakEnv is not checked right after the call")
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && readers[id.Name] && firstUse == token.NoPos {
				firstUse = x.Pos()
			}
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == "os" && x.Sel.Name == "Getenv" {
				// the only allowed mention is the argument handed to cirakEnv
				inScan := false
				ast.Inspect(body, func(m ast.Node) bool {
					if c, ok := m.(*ast.CallExpr); ok {
						// runConfig performs its own cirakEnv scan before anything else (config check); init reads only paths and the
						// OpenCode credential file names, never a CIRAK_ value.
						if cid, ok := c.Fun.(*ast.Ident); ok && (cid.Name == "cirakEnv" || cid.Name == "runConfig" || cid.Name == "productionInit") {
							for _, a := range c.Args {
								if a == ast.Expr(x) {
									inScan = true
								}
							}
						}
					}
					return true
				})
				if !inScan {
					problems = append(problems, "main reads the process environment directly (os.Getenv) instead of through the resolved view")
				}
			}
		}
		return true
	})
	if firstUse != token.NoPos && firstUse < scan {
		problems = append(problems, "a reader of the environment runs before the conflict scan")
	}
	// ADR-027: the config layer (flag, environment, file, default) is resolved once, before any reader, and the
	// readers receive its result, never the environment-only view.
	loadPos, firstServe := token.NoPos, token.NoPos // runAdmin is a client and keeps the environment-only view
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				if id.Name == "loadEffective" && loadPos == token.NoPos {
					loadPos = c.Pos()
				}
				if readers[id.Name] && id.Name != "runAdmin" && firstServe == token.NoPos {
					firstServe = c.Pos()
				}
			}
		}
		return true
	})
	switch {
	case loadPos == token.NoPos:
		problems = append(problems, "main never calls loadEffective")
	case firstServe != token.NoPos && firstServe < loadPos:
		problems = append(problems, "a reader of the environment runs before the config layer")
	}
	ast.Inspect(body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := c.Fun.(*ast.Ident)
		if !ok || !readers[id.Name] || id.Name == "runAdmin" {
			return true
		}
		for _, a := range c.Args {
			if arg, ok := a.(*ast.Ident); ok && arg.Name == "envOnly" {
				problems = append(problems, id.Name+" is given the environment-only view instead of the config layer's")
			}
		}
		return true
	})
	// ADR-017 A2 S3 (D32 Q13): main must parse the operator thresholds from the effective
	// environment before the native client, the writer lock and the adapter, and hand the parsed
	// values to the Manager. A daemon that validated them but built New(client) would silently run
	// with the defaults while the config check refused invalid values.
	parsePos, parseName := token.NoPos, ""
	envPos, envName := token.NoPos, ""
	clientPos, lockPos, claudePos := token.NoPos, token.NoPos, token.NoPos
	withLivenessPos := token.NoPos
	var withLivenessArg ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if id, ok := as.Lhs[0].(*ast.Ident); ok {
				if sel, ok := as.Rhs[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "Get" {
					envPos, envName = as.Pos(), id.Name
				}
			}
		}
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) >= 1 && len(as.Rhs) == 1 {
			if c, ok := as.Rhs[0].(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "execution" {
						switch sel.Sel.Name {
						case "ParseLivenessEnv":
							if id, ok := as.Lhs[0].(*ast.Ident); ok && len(c.Args) == 1 {
								parsePos, parseName = c.Pos(), id.Name
								if arg, ok := c.Args[0].(*ast.Ident); !ok || envName == "" || arg.Name != envName {
									problems = append(problems, "ParseLivenessEnv is not given the effective environment")
								}
							}
						case "NewWithLiveness":
							withLivenessPos = c.Pos()
							if len(c.Args) == 2 {
								withLivenessArg = c.Args[1]
							}
						}
					}
				}
			}
		}
		if c, ok := n.(*ast.CallExpr); ok {
			switch fn := c.Fun.(type) {
			case *ast.SelectorExpr:
				if x, ok := fn.X.(*ast.Ident); ok && x.Name == "opencode" && fn.Sel.Name == "New" {
					if clientPos == token.NoPos {
						clientPos = c.Pos()
					}
				}
			case *ast.Ident:
				switch fn.Name {
				case "acquireOpenCodeOwnership":
					if lockPos == token.NoPos {
						lockPos = c.Pos()
					}
				case "openClaude":
					if claudePos == token.NoPos {
						claudePos = c.Pos()
					}
				}
			}
		}
		return true
	})
	switch {
	case parsePos == token.NoPos || parseName == "":
		problems = append(problems, "main never calls execution.ParseLivenessEnv")
	case withLivenessPos == token.NoPos:
		problems = append(problems, "main never calls execution.NewWithLiveness: the parsed thresholds would be discarded")
	default:
		if id, ok := withLivenessArg.(*ast.Ident); !ok || id.Name != parseName {
			problems = append(problems, "execution.NewWithLiveness does not receive the parsed thresholds")
		}
		for _, after := range []struct {
			name string
			pos  token.Pos
		}{
			{"opencode.New", clientPos}, {"acquireOpenCodeOwnership", lockPos}, {"openClaude", claudePos}, {"execution.NewWithLiveness", withLivenessPos},
		} {
			if after.pos != token.NoPos && parsePos > after.pos {
				problems = append(problems, "the operator thresholds are parsed after "+after.name)
			}
		}
	}
	if envPos == token.NoPos || (parsePos != token.NoPos && envPos > parsePos) {
		problems = append(problems, "the thresholds are parsed before the effective environment is loaded")
	}
	return problems
}
