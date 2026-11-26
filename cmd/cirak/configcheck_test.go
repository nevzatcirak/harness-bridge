package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// fixture is a host-shaped temp tree: a service-owned 0700 area with a token file, a workspace and a lock
// directory. Nothing here touches the real host.
type fixture struct {
	dir, token, ws, locks, cfg string
	tokenValue                 string
}

const canaryToken = "CANARY-TOKEN-0123456789-abcdefghijklmnopqrstuvwxyz-ABCDEF"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := privDir(t)
	f := &fixture{dir: d, token: filepath.Join(d, "token"), ws: filepath.Join(d, "ws"), locks: filepath.Join(d, "locks"), cfg: filepath.Join(d, "config.json"), tokenValue: canaryToken}
	writeFile(t, f.token, f.tokenValue+"\n", 0o600)
	for _, p := range []string{f.ws, f.locks} {
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := systemConfigPath
	systemConfigPath = filepath.Join(d, "no-system-config.json")
	t.Cleanup(func() { systemConfigPath = old })
	return f
}

func (f *fixture) write(t *testing.T, body string) {
	t.Helper()
	writeFile(t, f.cfg, strings.NewReplacer("@TOKEN@", f.token, "@WS@", f.ws, "@LOCKS@", f.locks, "@DIR@", f.dir).Replace(body), 0o600)
}

const cleanConfig = `{"token":{"file":"@TOKEN@","expires":"2099-01-01T00:00:00Z"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`

func check(t *testing.T, f *fixture, env map[string]string, args ...string) (string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	sort.Strings(environ)
	code := runConfig(append([]string{"check"}, args...), envFn(env), environ, os.Geteuid(), &out, &errb)
	return out.String() + errb.String(), code
}

func TestCheckCleanConfigExitsZero(t *testing.T) {
	f := newFixture(t)
	f.write(t, cleanConfig)
	out, code := check(t, f, nil, "--config", f.cfg)
	if code != 0 || !strings.Contains(out, "result: clean") || !strings.Contains(out, "config: using "+f.cfg) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "REFUSED") || strings.Contains(out, "warning") || strings.Contains(out, "UNVERIFIED") {
		t.Fatalf("a clean check printed a problem:\n%s", out)
	}
}

// init --local shape: the 7-day local deadline alone stays local and passes (ADR-027 section 4, P2-1).
func TestCheckLocalDeadlineAloneIsClean(t *testing.T) {
	f := newFixture(t)
	f.write(t, `{"token":{"file":"@TOKEN@","expires":"`+time.Now().Add(7*24*time.Hour).UTC().Format(time.RFC3339)+`"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"},"limits":{"token_max_lifetime":"168h"}}`)
	if out, code := check(t, f, nil, "--config", f.cfg); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestCheckLocalExpiryRules(t *testing.T) {
	f := newFixture(t)
	body := func(exp, extra string) string {
		return `{"token":{"file":"@TOKEN@","expires":"` + exp + `"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}` + extra + `}`
	}
	f.write(t, body("2001-01-01T00:00:00Z", ""))
	out, code := check(t, f, nil, "--config", f.cfg)
	if code != 1 || strings.Count(out, "CIRAK_TOKEN_EXPIRES has passed") != 1 || strings.Contains(out, "2001") {
		t.Fatalf("a passed deadline warns once without the value: exit %d\n%s", code, out)
	}
	f.write(t, body("next-week", ""))
	if out, code := check(t, f, nil, "--config", f.cfg); code != 2 || !strings.Contains(out, "RFC3339") {
		t.Fatalf("unparseable: exit %d\n%s", code, out)
	}
	f.write(t, body("2099-01-01T00:00:00Z", `,"server":{"listen":"0.0.0.0:8443"}`))
	if out, code := check(t, f, nil, "--config", f.cfg); code != 2 {
		t.Fatalf("expires alone on a non-loopback address: exit %d\n%s", code, out)
	}
}

func TestCheckRefusals(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(f.dir, "other-token")
	writeFile(t, other, "x"+strings.Repeat("y", 50)+"\n", 0o600)
	short := filepath.Join(f.dir, "short-token")
	writeFile(t, short, strings.Repeat("s", 40)+"\n", 0o600)
	loose := filepath.Join(f.dir, "loose-token")
	writeFile(t, loose, strings.Repeat("z", 50)+"\n", 0o640)
	cases := []struct {
		name, cfg string
		env       map[string]string
		want      string
	}{
		{"unknown key", `{"surprise":1}`, nil, "unknown config key"},
		{"inline secret key", `{"token":{"value":"x"}}`, nil, "unknown config key"},
		{"stale env over file", `{"server":{"read_only":false},"token":{"file":"@TOKEN@"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`, map[string]string{"CIRAK_READ_ONLY": "1"}, "server.read_only"},
		{"env list differs", cleanConfig, map[string]string{"CIRAK_ALLOWED_SESSIONS": "ses_other"}, "acl.sessions"},
		{"env token vs file token", cleanConfig, map[string]string{"CIRAK_TOKEN": strings.Repeat("a", 40)}, "mutually exclusive"},
		{"loose token file", `{"token":{"file":"` + loose + `"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`, nil, "private regular file"},
		{"short token", `{"token":{"file":"` + short + `"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"},"limits":{"token_max_lifetime":"168h"}}`, nil, "at least 43"},
		{"tls key alone", `{"server":{"tls":{"key_file":"/a/k"}},"token":{"file":"@TOKEN@"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`, nil, "remote mode requires TLS"},
		{"opencode prefix from the env", `{"token":{"file":"@TOKEN@"},"acl":{"roots":["@WS@"]},"writer":{"lock_dir":"@LOCKS@"}}`, map[string]string{"CIRAK_ALLOWED_SESSIONS": "opencode:ses_abc"}, "bare native ID"},
		{"opencode prefix in the file", `{"acl":{"sessions":["opencode:ses_abc"]}}`, nil, "bare native ID"},
		{"no roots", `{"token":{"file":"@TOKEN@"},"acl":{"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`, nil, "acl:"},
		{"missing root", `{"token":{"file":"@TOKEN@"},"acl":{"roots":["@DIR@/nope"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`, nil, "acl:"},
		{"writer without lock dir", `{"token":{"file":"@TOKEN@"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]}}`, nil, "writer.lock_dir is required"},
		{"relay policy", cleanConfig, map[string]string{"CIRAK_PERMISSION_RELAY": "policy"}, "permission.relay"},
		{"dashboard both", cleanConfig, map[string]string{"CIRAK_DASHBOARD_ADDR": "127.0.0.1:1", "CIRAK_DASHBOARD_SOCKET": "@DIR@/d/s"}, "dashboard:"},
		{"claude partial", cleanConfig, map[string]string{"CIRAK_CLAUDE_MODEL": "m"}, "configured completely or not at all"},
		{"overlay without lifecycle", cleanConfig, map[string]string{"CIRAK_OVERLAY_DIR": "@DIR@/ov"}, "needs lifecycle roots"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.write(t, c.cfg)
			env := map[string]string{}
			for k, v := range c.env {
				env[k] = strings.ReplaceAll(v, "@DIR@", f.dir)
			}
			out, code := check(t, f, env, "--config", f.cfg)
			if code != 2 || !strings.Contains(out, c.want) || !strings.Contains(out, "result: refused") {
				t.Fatalf("exit %d, want 2 with %q:\n%s", code, c.want, out)
			}
			if strings.Contains(out, canaryToken) {
				t.Fatalf("the token leaked:\n%s", out)
			}
		})
	}
}

func TestCheckFileRules(t *testing.T) {
	f := newFixture(t)
	f.write(t, cleanConfig)
	for _, mode := range []os.FileMode{0o644, 0o640, 0o666} {
		if err := os.Chmod(f.cfg, mode); err != nil {
			t.Fatal(err)
		}
		if out, code := check(t, f, nil, "--config", f.cfg); code != 2 || !strings.Contains(out, "mode 0600") {
			t.Errorf("mode %o: exit %d\n%s", mode, code, out)
		}
	}
	if err := os.Chmod(f.cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.dir, "link.json")
	if err := os.Symlink(f.cfg, link); err != nil {
		t.Fatal(err)
	}
	if out, code := check(t, f, nil, "--config", link); code != 2 || !strings.Contains(out, "symbolic link") {
		t.Errorf("symlinked config: exit %d\n%s", code, out)
	}
	if out, code := check(t, f, nil, "--config", filepath.Join(f.dir, "missing.json")); code != 2 || !strings.Contains(out, "does not exist") {
		t.Errorf("missing --config: exit %d\n%s", code, out)
	}
	if out, code := check(t, f, map[string]string{"CIRAK_CONFIG": filepath.Join(f.dir, "missing.json")}); code != 2 || !strings.Contains(out, "does not exist") {
		t.Errorf("missing CIRAK_CONFIG: exit %d\n%s", code, out)
	}
	// a token file that is a symlink is refused by the existing readSecret rule
	tl := filepath.Join(f.dir, "token-link")
	if err := os.Symlink(f.token, tl); err != nil {
		t.Fatal(err)
	}
	f.write(t, strings.Replace(cleanConfig, "@TOKEN@", tl, 1))
	if out, code := check(t, f, nil, "--config", f.cfg); code != 2 {
		t.Errorf("symlinked token: exit %d\n%s", code, out)
	}
	// 0400 stays accepted: P3-3 keeps today's readSecret rule
	if err := os.Chmod(f.token, 0o400); err != nil {
		t.Fatal(err)
	}
	f.write(t, cleanConfig)
	if out, code := check(t, f, nil, "--config", f.cfg); code != 0 {
		t.Errorf("a 0400 token file must stay accepted: exit %d\n%s", code, out)
	}
}

// Without a visible source the token is UNVERIFIED (exit 1), never PASS (section 1.5).
func TestCheckUnseenTokenIsUnverified(t *testing.T) {
	f := newFixture(t)
	f.write(t, `{"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	out, code := check(t, f, nil, "--config", f.cfg)
	if code != 1 || !strings.Contains(out, "UNVERIFIED token") || strings.Contains(out, "token source readable") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// a credential outside systemd cannot be read here
	f.write(t, `{"token":{"credential":"token"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	out, code = check(t, f, nil, "--config", f.cfg)
	if code != 1 || !strings.Contains(out, "UNVERIFIED token: it is a systemd credential") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// with the credentials directory present the same file is read through the existing rule
	credDir := filepath.Join(f.dir, "creds")
	writeFile(t, filepath.Join(credDir, "token"), canaryToken+"\n", 0o600)
	out, code = check(t, f, map[string]string{"CREDENTIALS_DIRECTORY": credDir}, "--config", f.cfg)
	if code != 0 || !strings.Contains(out, "token source readable") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// a hostile credential name never reaches outside the directory
	f.write(t, `{"token":{"credential":"../token"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	if out, code := check(t, f, map[string]string{"CREDENTIALS_DIRECTORY": credDir}, "--config", f.cfg); code != 2 {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// No value of any kind appears in the output: a canary token (file and inline), a canary OpenCode password,
// and a canary in an unknown variable's value.
func TestCheckOutputCarriesNoValue(t *testing.T) {
	f := newFixture(t)
	pw := filepath.Join(f.dir, "pw")
	writeFile(t, pw, "CANARY-PASSWORD-0123456789\n", 0o600)
	f.write(t, `{"token":{"file":"@TOKEN@"},"harness":{"opencode":{"password_file":"`+pw+`","username":"u"}},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	env := map[string]string{"CIRAK_BOGUS_NAME": "CANARY-UNKNOWN-VALUE", "BRIDGE_ALSO_BOGUS": "CANARY-LEGACY-VALUE"}
	out, code := check(t, f, env, "--config", f.cfg, "--print-effective")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, leak := range []string{canaryToken, "CANARY-PASSWORD", "CANARY-UNKNOWN-VALUE", "CANARY-LEGACY-VALUE", f.token, pw} {
		if strings.Contains(out, leak) {
			t.Errorf("the output contains %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{"unknown environment name CIRAK_BOGUS_NAME", "unknown environment name BRIDGE_ALSO_BOGUS"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !regexpMatch(out, `token\.file\s+config file\s+set`) || !regexpMatch(out, `harness\.opencode\.password_file\s+config file\s+set`) {
		t.Errorf("secret references must read set:\n%s", out)
	}
	// an inline token in the environment (legacy) is also never printed
	inline := strings.Repeat("I", 40)
	f.write(t, `{"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	out, _ = check(t, f, map[string]string{"BRIDGE_TOKEN": inline}, "--config", f.cfg, "--print-effective")
	if strings.Contains(out, inline) || !strings.Contains(out, "legacy environment variable BRIDGE_TOKEN is deprecated") {
		t.Fatalf("%s", out)
	}
}

func TestCheckKnownNamesAreNotWarnedAbout(t *testing.T) {
	f := newFixture(t)
	f.write(t, cleanConfig)
	env := map[string]string{"CIRAK_CONFIG": f.cfg, "OPENCODE_URL": "http://127.0.0.1:4096", "OPENCODE_SERVER_USERNAME": "u", "CREDENTIALS_DIRECTORY": f.dir}
	for _, s := range envSuffixes {
		if s != "TOKEN" && s != "TOKEN_FILE" {
			env["CIRAK_"+s] = ""
		}
	}
	env["CIRAK_TOKEN_CREDENTIAL"], env["CIRAK_OPENCODE_PASSWORD_CREDENTIAL"] = "", ""
	out, _ := check(t, f, env)
	if strings.Contains(out, "unknown environment name") || strings.Contains(out, "deprecated") {
		t.Fatalf("a known or non-legacy name was warned about:\n%s", out)
	}
}

func TestCheckLegacyNamesWarnAndConflictsRefuse(t *testing.T) {
	f := newFixture(t)
	f.write(t, `{"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	out, code := check(t, f, map[string]string{"BRIDGE_TOKEN_FILE": f.token}, "--config", f.cfg)
	if code != 1 || strings.Count(out, "legacy environment variable BRIDGE_TOKEN_FILE is deprecated") != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	out, code = check(t, f, map[string]string{"BRIDGE_READ_ONLY": "1", "CIRAK_READ_ONLY": "0"}, "--config", f.cfg)
	if code != 2 || !strings.Contains(out, "BRIDGE_READ_ONLY and CIRAK_READ_ONLY") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestCheckRootOwnershipRule(t *testing.T) {
	f := newFixture(t)
	f.write(t, cleanConfig)
	old := rootOwner
	defer func() { rootOwner = old }()
	rootOwner = func(fs.FileInfo) (uint32, bool) { return uint32(os.Geteuid()) + 4242, true }
	out, code := check(t, f, nil, "--config", f.cfg)
	if code != 2 || !strings.Contains(out, "must be owned by root or the service user") {
		t.Fatalf("a root owned by a third user: exit %d\n%s", code, out)
	}
	rootOwner = func(fs.FileInfo) (uint32, bool) { return 0, true } // root is always acceptable
	if out, code := check(t, f, nil, "--config", f.cfg); code != 0 {
		t.Fatalf("a root owned by root: exit %d\n%s", code, out)
	}
	rootOwner = func(fs.FileInfo) (uint32, bool) { return 0, false } // an unknown owner is a refusal
	if _, code := check(t, f, nil, "--config", f.cfg); code != 2 {
		t.Fatalf("an unknown owner: exit %d", code)
	}
}

func TestCheckPrivateDirRules(t *testing.T) {
	f := newFixture(t)
	f.write(t, cleanConfig)
	if err := os.Chmod(f.locks, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := check(t, f, nil, "--config", f.cfg); code != 2 || !strings.Contains(out, "writer.lock_dir") || !strings.Contains(out, "0700") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if err := os.Chmod(f.locks, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.locks); err != nil {
		t.Fatal(err)
	}
	if out, code := check(t, f, nil, "--config", f.cfg); code != 1 || !strings.Contains(out, "does not exist yet") {
		t.Fatalf("a lock dir a service creates is a warning: exit %d\n%s", code, out)
	}
}

func TestCheckClaudeRules(t *testing.T) {
	f := newFixture(t)
	cmd, hist, home := filepath.Join(f.dir, "claude-wrapper"), filepath.Join(f.dir, "hist"), filepath.Join(f.dir, "home")
	sess := filepath.Join(f.dir, "claude-sessions.json")
	writeFile(t, cmd, "#!/bin/sh\n", 0o700)
	body := `{"token":{"file":"@TOKEN@"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"},"harness":{"claude":{"command":"` + cmd + `","sessions_file":"` + sess + `","versions":"2.1.1","history_dir":"` + hist + `","home":"` + home + `","model":"m"}}}`
	f.write(t, body)
	if out, code := check(t, f, nil, "--config", f.cfg); code != 2 || !strings.Contains(out, "sessions_file") {
		t.Fatalf("a missing sessions file: exit %d\n%s", code, out)
	}
	writeFile(t, sess, "{}", 0o644)
	if out, code := check(t, f, nil, "--config", f.cfg); code != 2 || !strings.Contains(out, "mode 0600") {
		t.Fatalf("a loose sessions file: exit %d\n%s", code, out)
	}
	writeFile(t, sess, "{}", 0o600)
	if out, code := check(t, f, nil, "--config", f.cfg); code != 0 || !strings.Contains(out, "harness.claude (command") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	f.write(t, strings.Replace(body, `"versions":"2.1.1"`, `"versions":"latest"`, 1))
	if out, code := check(t, f, nil, "--config", f.cfg); code != 2 || !strings.Contains(out, "harness.claude.versions") {
		t.Fatalf("a bad version list: exit %d\n%s", code, out)
	}
}

func TestCheckUsage(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{nil, {"bogus"}, {"check", "extra"}, {"check", "--nope"}} {
		out.Reset()
		errb.Reset()
		if code := runConfig(args, envFn(nil), nil, os.Geteuid(), &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage: cirak config check") {
			t.Errorf("%v: exit %d %q", args, code, errb.String())
		}
	}
	// the daemon's flags are applied as a start would: a flag over a file value is the flag, and a bad one is refused
	f := newFixture(t)
	f.write(t, `{"server":{"listen":"127.0.0.1:9999"},"token":{"file":"@TOKEN@"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	if o, code := check(t, f, nil, "--config", f.cfg, "--listen", "0.0.0.0:8443"); code != 2 || !strings.Contains(o, "ingress") {
		t.Errorf("--listen 0.0.0.0 over a file: exit %d\n%s", code, o)
	}
	if o, code := check(t, f, nil, "--config", f.cfg, "--listen", "127.0.0.1:1", "--read-only"); code != 0 {
		t.Errorf("flags over a file: exit %d\n%s", code, o)
	}
	out.Reset()
	errb.Reset()
	if code := runConfig([]string{"check", "-h"}, envFn(nil), nil, os.Geteuid(), &out, &errb); code != 0 {
		t.Errorf("-h: exit %d", code)
	}
}

// config check writes nothing, opens nothing and starts nothing: the tree is the same after the run, and the
// source of the command contains no call that could.
func TestCheckTouchesNothing(t *testing.T) {
	f := newFixture(t)
	f.write(t, cleanConfig)
	before := listTree(t, f.dir)
	check(t, f, nil, "--config", f.cfg, "--print-effective")
	if after := listTree(t, f.dir); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("the tree changed:\nbefore %v\nafter  %v", before, after)
	}
	fset := token.NewFileSet()
	for _, name := range []string{"configcheck.go", "config.go"} {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		forbidden := map[string]bool{
			"net.Listen": true, "net.Dial": true, "net.DialTimeout": true, "http.Get": true, "http.Post": true, "http.ListenAndServe": true,
			"os.Create": true, "os.WriteFile": true, "os.Mkdir": true, "os.MkdirAll": true, "os.Remove": true, "os.RemoveAll": true,
			"os.Rename": true, "os.Chmod": true, "os.Chown": true, "os.OpenFile": true, "exec.Command": true, "exec.CommandContext": true,
			"os.Setenv": true, "syscall.Flock": true, "unix.Flock": true,
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && forbidden[id.Name+"."+sel.Sel.Name] {
					t.Errorf("%s uses %s.%s: config check must start and write nothing", name, id.Name, sel.Sel.Name)
				}
			}
			return true
		})
	}
}

func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, _ := d.Info()
		out = append(out, p+" "+info.Mode().String()+" "+info.ModTime().String())
		return nil
	})
	sort.Strings(out)
	return out
}

func regexpMatch(s, pattern string) bool { return regexp.MustCompile(pattern).MatchString(s) }

// config check calls the daemon's own validators; it does not carry a second copy (ADR-027 section 1.5). The
// calls are pinned by name in the check, and the start path must use the same functions.
func TestCheckReusesTheRuntimeValidators(t *testing.T) {
	calls := func(file string) map[string]bool {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				switch fn := c.Fun.(type) {
				case *ast.Ident:
					got[fn.Name] = true
				case *ast.SelectorExpr:
					if id, ok := fn.X.(*ast.Ident); ok {
						got[id.Name+"."+fn.Sel.Name] = true
					}
				}
			}
			return true
		})
		return got
	}
	check := calls("configcheck.go")
	for _, want := range []string{"readSecret", "ingressWithToken", "readIngressAt", "claudeConfig", "lifecycleConfig", "permissionRelayMode",
		"permissionIncarnationMode", "dashboard.ConfigFromEnv", "dashboard.RedactorFromEnv", "security.New", "security.NewLifecycleRoots",
		"opencode.New", "claudecode.ParseVersions", "config.ValidACLSession", "execution.ParseLivenessEnv", "loadEffective", "cirakEnv"} {
		if !check[want] {
			t.Errorf("config check does not call %s: the rule would be copied or dropped", want)
		}
	}
	// and the start path reaches the same validators through its own functions
	start := map[string]bool{}
	for _, file := range []string{"main.go", "startup.go", "lifecycle.go"} {
		for k := range calls(file) {
			start[k] = true
		}
	}
	for _, want := range []string{"ingressWithToken", "readIngressAt", "lifecycleConfig", "claudeConfig", "readSecret", "loadEffective", "security.New", "permissionRelayMode", "execution.ParseLivenessEnv"} {
		if !start[want] && !(want == "claudeConfig" && calls("claude.go")[want]) {
			t.Errorf("the start path does not use %s", want)
		}
	}
}

// #526 P2-1 and P3-1 through the real command: credentials in a URL-valued key, from the environment or the
// file, never reach the output (the listing, the refusal or any other line); a value cannot forge a line.
func TestCheckPrintEffectiveNeverPrintsURLCredentials(t *testing.T) {
	f := newFixture(t)
	f.write(t, cleanConfig)
	env := map[string]string{
		"OPENCODE_URL":        "http://leakuser:LEAKPASSWORD@127.0.0.1:4096",
		"CIRAK_PUBLIC_ORIGIN": "https://orig:LEAKORIGINPW@x.example/?token=LEAKTOKENQ",
	}
	out, code := check(t, f, env, "--config", f.cfg, "--print-effective")
	if code != 2 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, leak := range []string{"leakuser", "LEAKPASSWORD", "orig:", "LEAKORIGINPW", "LEAKTOKENQ"} {
		if strings.Contains(out, leak) {
			t.Errorf("the output contains %q:\n%s", leak, out)
		}
	}
	if !regexpMatch(out, `harness\.opencode\.url\s+environment\s+http://\*\*\*@127\.0\.0\.1:4096`) || !regexpMatch(out, `server\.public_origin\s+environment\s+https://\*\*\*@x\.example \(path or query not shown\)`) {
		t.Errorf("the redacted forms are not listed:\n%s", out)
	}
	// the same from the file
	f.write(t, `{"harness":{"opencode":{"url":"http://fileuser:FILELEAKPW@127.0.0.1:4096"}},"server":{"public_origin":"https://fo:FILEORIGINPW@x.example"},"token":{"file":"@TOKEN@"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	out, _ = check(t, f, nil, "--config", f.cfg, "--print-effective")
	for _, leak := range []string{"fileuser", "FILELEAKPW", "FILEORIGINPW", "fo:"} {
		if strings.Contains(out, leak) {
			t.Errorf("file source: the output contains %q:\n%s", leak, out)
		}
	}
	// an unparseable URL is not echoed
	f.write(t, cleanConfig)
	out, _ = check(t, f, map[string]string{"OPENCODE_URL": "http://u:LEAKBAD@[::1"}, "--config", f.cfg, "--print-effective")
	if strings.Contains(out, "LEAKBAD") || !strings.Contains(out, "set (not shown: not a valid URL)") {
		t.Errorf("an unparseable URL was echoed or not marked:\n%s", out)
	}
	// the binary agrees (the reproduction of #526, dummy values)
}

func TestCheckPrintEffectiveCannotForgeALine(t *testing.T) {
	f := newFixture(t)
	f.write(t, cleanConfig)
	out, _ := check(t, f, map[string]string{"CIRAK_PRINCIPAL": "bob\nINJECTED line\r\nRESULT: clean\x1b[2J"}, "--config", f.cfg, "--print-effective")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "INJECTED") || strings.HasPrefix(line, "RESULT") || strings.Contains(line, "\x1b") || strings.Contains(line, "\r") {
			t.Errorf("a forged or control line in the output: %q", line)
		}
	}
	if !strings.Contains(out, `"bob\nINJECTED line\r\nRESULT: clean\x1b[2J"`) {
		t.Errorf("the value is not shown quoted:\n%s", out)
	}
}
