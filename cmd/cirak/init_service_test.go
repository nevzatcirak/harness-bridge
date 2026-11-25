package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevzatcirak/cirak/internal/config"
)

// ADR-027 S3: the service step of `cirak init --install-service`. Every unit and plist goes to a temp directory;
// nothing here touches /etc, systemd or launchd, and no service command is ever run.

type svcEnv struct {
	h        *ih
	unitDir  string // stands for /etc/systemd/system
	packaged string
}

// newSvcEnv points the system paths at temp directories and makes the service lock directory exist (0700), which a
// service user's init requires before it writes.
func newSvcEnv(t *testing.T) *svcEnv {
	t.Helper()
	h := newIH(t)
	e := &svcEnv{h: h, unitDir: h.dir("systemd-system", 0o755), packaged: filepath.Join(h.base, "packaged", "cirak.service")}
	oldUnit, oldPkg, oldLock := systemUnitDir, packagedUnitPath, serviceLockDir
	systemUnitDir, packagedUnitPath = e.unitDir, e.packaged
	serviceLockDir = filepath.Join(h.base, "state", "writer-locks")
	if err := os.MkdirAll(serviceLockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	h.dir("cfg", 0o700) // a service user's system directory must already exist
	t.Cleanup(func() { systemUnitDir, packagedUnitPath, serviceLockDir = oldUnit, oldPkg, oldLock })
	return e
}

func (e *svcEnv) withPackagedUnit(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.packaged), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, e.packaged, "[Service]\nLoadCredential=token\nEnvironment=CIRAK_TOKEN_FILE=%d/token\n", 0o644)
}

func (e *svcEnv) args(extra ...string) []string {
	return e.h.yesArgs(append([]string{"--service-user", "cirak", "--install-service"}, extra...)...)
}

func TestInstallServiceWritesTheFullUnitAndTheCredentialConfig(t *testing.T) {
	e := newSvcEnv(t)
	h := e.h
	if code := h.init(e.args()...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	unit := filepath.Join(e.unitDir, "cirak.service")
	want, err := renderFullUnit(unitOptions{User: "cirak", Group: "cirak", ConfigPath: h.cfgPath(), TokenPath: filepath.Join(h.base, "cfg", "token")})
	if err != nil {
		t.Fatal(err)
	}
	if got := readF(t, unit); got != want || mode(t, unit) != 0o644 {
		t.Fatalf("the unit is not the rendered one (mode %o):\n%s", mode(t, unit), got)
	}
	// the full unit uses the credential: token.credential "token", no token.file
	f, err := config.Parse([]byte(readF(t, h.cfgPath())))
	if err != nil {
		t.Fatal(err)
	}
	if f.EnvForm("token.credential") != "token" || f.Has("token.file") {
		t.Errorf("the config names credential %q and token.file present=%v", f.EnvForm("token.credential"), f.Has("token.file"))
	}
	// the token file is still generated (it is the credential's source), and the generated check came out clean
	tok := strings.TrimSpace(readF(t, filepath.Join(h.base, "cfg", "token")))
	if len(tok) != 64 || !strings.Contains(h.out.String(), "result: clean") {
		t.Fatalf("token length %d:\n%s", len(tok), h.out.String())
	}
	// nothing secret in the unit, the config or the output; nothing was run
	for _, text := range []string{readF(t, unit), readF(t, h.cfgPath()), h.all()} {
		if strings.Contains(text, tok) {
			t.Fatal("the token leaked")
		}
	}
	if len(h.runs) != 0 {
		t.Errorf("init ran %v", h.runs)
	}
	for _, want := range []string{"Service: a systemd system unit", "no packaged unit: init writes the full unit", "systemctl daemon-reload", "systemctl restart cirak", "(printed, never run by init)"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, h.out.String())
		}
	}
	// the preview directory of the check is gone
	if ents, _ := filepath.Glob(filepath.Join(h.base, "cirak-init-check-*")); len(ents) != 0 {
		t.Errorf("the credential preview was left behind: %v", ents)
	}
}

func TestInstallServiceWithAPackagedUnitWritesOnlyTheDropIn(t *testing.T) {
	e := newSvcEnv(t)
	e.withPackagedUnit(t)
	h := e.h
	// a full unit that already shadows the package unit is warned about and left alone
	writeFile(t, filepath.Join(e.unitDir, "cirak.service"), "# an old shadow\n", 0o644)
	if code := h.init(e.args("--force")...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	drop := filepath.Join(e.unitDir, "cirak.service.d", "zz-cirak-init.conf")
	want, _ := renderDropIn(unitOptions{User: "cirak", Group: "cirak", ConfigPath: h.cfgPath(), TokenPath: filepath.Join(h.base, "cfg", "token")})
	if readF(t, drop) != want || mode(t, drop) != 0o644 || mode(t, filepath.Dir(drop)) != 0o755 {
		t.Fatalf("the drop-in:\n%s", readF(t, drop))
	}
	if readF(t, filepath.Join(e.unitDir, "cirak.service")) != "# an old shadow\n" {
		t.Error("init rewrote the shadowing unit")
	}
	if !strings.Contains(h.out.String(), "already shadows the packaged unit") || !strings.Contains(h.out.String(), "drop-in only") {
		t.Errorf("the choice is not said:\n%s", h.out.String())
	}
	// the packaged unit supplies the token: the config names neither token.file nor token.credential
	f, _ := config.Parse([]byte(readF(t, h.cfgPath())))
	if f.Has("token.file") || f.Has("token.credential") {
		t.Error("the packaged-unit config names a token source")
	}
	if !strings.Contains(h.out.String(), "result: clean") {
		t.Fatalf("the check was not clean:\n%s", h.out.String())
	}
	// never both: without a shadow there is exactly one file, the drop-in
	e2 := newSvcEnv(t)
	e2.withPackagedUnit(t)
	if code := e2.h.init(e2.args()...); code != 0 {
		t.Fatalf("exit %d\n%s", code, e2.h.all())
	}
	if exists(filepath.Join(e2.unitDir, "cirak.service")) || !exists(filepath.Join(e2.unitDir, "cirak.service.d", "zz-cirak-init.conf")) {
		t.Error("init wrote a second cirak.service or no drop-in")
	}
}

func TestInstallServiceStagesWhenTheUnitDirectoryCannotBeWritten(t *testing.T) {
	e := newSvcEnv(t)
	h := e.h
	systemUnitDir = filepath.Join(h.base, "no-such-root", "systemd", "system") // cannot be created: its parent is missing
	if code := h.init(e.args()...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	dst := filepath.Join(systemUnitDir, "cirak.service")
	if exists(dst) || exists(filepath.Dir(dst)) {
		t.Fatal("init wrote under a directory it cannot write")
	}
	if h.stage == "" || mode(t, filepath.Join(h.stage, "cirak.service")) != 0o644 {
		t.Fatal("the unit was not staged 0644")
	}
	for _, want := range []string{
		"install -d -o 'root' -g 'root' -m 0755 " + qt(filepath.Dir(dst)),
		"install -o 'root' -g 'root' -m 0644 " + qt(filepath.Join(h.stage, "cirak.service")) + " " + qt(dst),
		"rm -r " + qt(h.stage),
	} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, h.out.String())
		}
	}
	if len(h.runs) != 0 {
		t.Errorf("init ran %v", h.runs)
	}
}

func TestInstallServiceInTheStagedFlowStagesTheUnitToo(t *testing.T) {
	defer rootOwnerForTest(t)()
	e := newSvcEnv(t)
	h := e.h
	h.svcUID = os.Geteuid() + 4242
	if code := h.init(e.args()...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	if exists(filepath.Join(e.unitDir, "cirak.service")) {
		t.Fatal("the unit was written although the config was staged")
	}
	for _, want := range []string{
		"install -o 'root' -g 'root' -m 0644 " + qt(filepath.Join(h.stage, "cirak.service")) + " " + qt(filepath.Join(e.unitDir, "cirak.service")),
		"install -o 'cirak' -g 'cirak' -m 0600 " + qt(filepath.Join(h.stage, "config.json")),
	} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, h.out.String())
		}
	}
	staged := readF(t, filepath.Join(h.stage, "config.json"))
	if !strings.Contains(staged, `"credential": "token"`) || strings.Contains(staged, `"file": "`+filepath.Join(h.base, "cfg", "token")) {
		t.Errorf("the staged config does not use the credential:\n%s", staged)
	}
}

func TestInstallServiceNeverOverwritesAndBacksUp(t *testing.T) {
	e := newSvcEnv(t)
	h := e.h
	unit := filepath.Join(e.unitDir, "cirak.service")
	writeFile(t, unit, "# hand edited\n", 0o644)
	args := e.args()
	before := listTree(t, h.base)
	if code := h.init(args...); code != 2 || !strings.Contains(h.errb.String(), "already exists") {
		t.Fatalf("exit %d: %q", code, h.errb.String())
	}
	if readF(t, unit) != "# hand edited\n" || strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") {
		t.Error("something was written before the refusal")
	}
	if code := h.init(append(args, "--force")...); code != 0 {
		t.Fatalf("--force: exit %d\n%s", code, h.all())
	}
	backups, _ := filepath.Glob(unit + ".bak-*")
	if len(backups) != 1 || readF(t, backups[0]) != "# hand edited\n" || mode(t, backups[0]) != 0o600 {
		t.Fatalf("backups %v", backups)
	}
}

func TestInstallServiceAdapterVariantAndRefusals(t *testing.T) {
	e := newSvcEnv(t)
	if code := e.h.init(e.args("--claude-home", "/home/hbclaude")...); code != 0 {
		t.Fatalf("exit %d\n%s", code, e.h.all())
	}
	unit := readF(t, filepath.Join(e.unitDir, "cirak.service"))
	if strings.Contains(directives(unit), "NoNewPrivileges=yes") || !strings.Contains(directives(unit), "ProtectHome=off") || !strings.Contains(e.h.out.String(), "Claude adapter variant") {
		t.Errorf("the adapter variant is wrong:\n%s", unit)
	}
	refuse := func(name string, h *ih, args ...string) {
		t.Helper()
		before := listTree(t, h.base)
		if code := h.init(args...); code != 2 {
			t.Errorf("%s: exit %d\n%s", name, code, h.all())
		}
		if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") || h.stage != "" {
			t.Errorf("%s: something was written or staged", name)
		}
	}
	e = newSvcEnv(t)
	refuse("no service user", e.h, e.h.yesArgs("--install-service")...)
	e = newSvcEnv(t)
	e.h.goos = "windows"
	refuse("unsupported OS", e.h, e.args()...)
	e = newSvcEnv(t)
	refuse("launch-daemon on linux", e.h, e.args("--launch-daemon")...)
	e = newSvcEnv(t)
	a := e.h.yesArgs("--service-user", "cirak", "--install-service")
	a[len(a)-1-0] = "--install-service"
	refuse("a config path a unit cannot hold", e.h, append(e.h.yesArgs("--service-user", "cirak", "--install-service"), "--config", filepath.Join(e.h.base, "my dir", "config.json"))...)
	for _, bad := range [][]string{{"--install-service", "--no-service"}, {"--launch-daemon"}, {"--claude-home", "/x"}} {
		h := newIH(t)
		if code := h.init(h.yesArgs(bad...)...); code != 2 {
			t.Errorf("%v: exit %d", bad, code)
		}
	}
}

func TestInstallServiceLaunchdAgentAndDaemon(t *testing.T) {
	h := newIH(t)
	h.goos = "darwin"
	oldDir := launchDaemonDir
	launchDaemonDir = filepath.Join(h.base, "no-such-root", "LaunchDaemons")
	defer func() { launchDaemonDir = oldDir }()
	home := filepath.Join(h.base, "home")
	if err := os.MkdirAll(filepath.Join(home, "Library"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code := h.init(h.yesArgs("--install-service")...); code != 0 {
		t.Fatalf("agent: exit %d\n%s", code, h.all())
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist")
	if mode(t, plist) != 0o644 || mode(t, filepath.Join(home, "Library", "Logs", "cirak")) != 0o700 {
		t.Fatalf("plist mode %o, log dir mode %o", mode(t, plist), mode(t, filepath.Join(home, "Library", "Logs", "cirak")))
	}
	body := readF(t, plist)
	for _, want := range []string{"<string>" + agentLabel + "</string>", "<string>/usr/local/bin/cirak</string>", "<string>" + h.cfgPath() + "</string>", "<key>KeepAlive</key>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the plist lacks %q", want)
		}
	}
	// macOS has no credential: the config keeps token.file (a 0600 file of the run user)
	f, _ := config.Parse([]byte(readF(t, h.cfgPath())))
	if !f.Has("token.file") || f.Has("token.credential") {
		t.Error("the LaunchAgent config must use token.file")
	}
	if !strings.Contains(h.out.String(), "launchctl bootstrap gui/") || !strings.Contains(h.out.String(), "(printed, never run by init)") || len(h.runs) != 0 {
		t.Errorf("the hint or a run:\n%s", h.out.String())
	}
	// a LaunchAgent runs as you: a service user is refused; the daemon needs one and is staged when it cannot be written
	h2 := newIH(t)
	h2.goos = "darwin"
	if code := h2.init(h2.yesArgs("--install-service", "--service-user", "cirak")...); code != 2 {
		t.Errorf("agent with a service user: exit %d", code)
	}
	h3 := newIH(t)
	h3.goos = "darwin"
	if code := h3.init(h3.yesArgs("--install-service", "--launch-daemon")...); code != 2 {
		t.Errorf("daemon without a service user: exit %d", code)
	}
	defer rootOwnerForTest(t)()
	h4 := newIH(t)
	h4.goos = "darwin"
	h4.svcUID = os.Geteuid() + 4242
	if code := h4.init(h4.yesArgs("--install-service", "--launch-daemon", "--service-user", "cirak")...); code != 0 {
		t.Fatalf("daemon: exit %d\n%s", code, h4.all())
	}
	want := "install -o 'root' -g 'wheel' -m 0644 " + qt(filepath.Join(h4.stage, daemonLabel+".plist")) + " " + qt(filepath.Join(launchDaemonDir, daemonLabel+".plist"))
	if !strings.Contains(h4.out.String(), want) || !strings.Contains(h4.out.String(), "launchctl bootstrap system") {
		t.Errorf("missing %q in:\n%s", want, h4.out.String())
	}
	if !strings.Contains(readF(t, filepath.Join(h4.stage, daemonLabel+".plist")), "<key>UserName</key>") {
		t.Error("the daemon plist does not name the run user")
	}
}

// init never starts or loads a service: no exec of systemctl or launchctl anywhere in the service code.
func TestServiceCodeNeverRunsAServiceManager(t *testing.T) {
	for _, file := range []string{"initservice.go", "unit.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"os/exec"` {
				t.Errorf("%s imports os/exec", file)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "w" && sel.Sel.Name == "d" {
						return true
					}
					if sel.Sel.Name == "run" || sel.Sel.Name == "Command" {
						t.Errorf("%s calls %s", file, sel.Sel.Name)
					}
				}
			}
			return true
		})
	}
	for _, bad := range []string{`"systemctl"`, `"launchctl"`, `"sudo"`} {
		if strings.Contains(readF(t, "initservice.go"), bad) || strings.Contains(readF(t, "unit.go"), bad) {
			t.Errorf("the service code names %s as a command", bad)
		}
	}
}
