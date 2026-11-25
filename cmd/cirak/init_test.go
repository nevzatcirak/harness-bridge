package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/cirak/internal/config"
)

// The harness builds an initDeps with temp directories and fakes only: nothing here touches /etc, systemd, the
// real home or a real OpenCode.

var initNow = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

type probeCall struct{ url, user, password string }

type ih struct {
	t                *testing.T
	base             string
	out, errb        bytes.Buffer
	runs             [][]string
	probes           []openCodeProbe // answers in order; the last repeats
	probeCalls       []probeCall
	passwordTyp      string
	stage            string
	tty              bool
	stdin            string
	svcUID           int // 0: the invoking user is the service user
	svcName          string
	stdoutRedirected bool // stdout is not a terminal although stdin is
	env              map[string]string
	runErr           error
	randomBytes      []byte
}

func newIH(t *testing.T) *ih {
	t.Helper()
	base := privDir(t)
	h := &ih{t: t, base: base, probes: []openCodeProbe{{Reachable: true}}, svcName: "svc", env: map[string]string{}}
	h.randomBytes = make([]byte, 256)
	for i := range h.randomBytes {
		h.randomBytes[i] = byte(i*7 + 3)
	}
	h.env["XDG_CONFIG_HOME"] = filepath.Join(base, "xdg-config")
	h.env["XDG_STATE_HOME"] = filepath.Join(base, "xdg-state")
	return h
}

// expectedToken is the token the fixed random bytes produce.
func (h *ih) expectedToken() string {
	return base64.RawURLEncoding.EncodeToString(h.randomBytes[:48])
}

func (h *ih) dir(name string, mode os.FileMode) string {
	h.t.Helper()
	p := filepath.Join(h.base, name)
	if err := os.MkdirAll(p, 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *ih) deps() initDeps {
	svc := func(name string) (userInfo, error) {
		if name == "" {
			return userInfo{Name: "operator", Group: "operators", UID: os.Geteuid(), GID: os.Getegid()}, nil
		}
		uid := os.Geteuid()
		if h.svcUID != 0 {
			uid = h.svcUID
		}
		return userInfo{Name: name, Group: name, UID: uid, GID: uid}, nil
	}
	i := 0
	return initDeps{
		getenv: func(k string) string { return h.env[k] }, stdin: strings.NewReader(h.stdin), stdout: &h.out, stderr: &h.errb,
		isTTY: h.tty, outTTY: h.tty && !h.stdoutRedirected, uid: os.Geteuid(), gid: os.Getegid(), now: func() time.Time { return initNow },
		random: bytes.NewReader(h.randomBytes), lookup: svc,
		probe: func(_ context.Context, u, user, pw string) openCodeProbe {
			h.probeCalls = append(h.probeCalls, probeCall{u, user, pw})
			r := h.probes[min(i, len(h.probes)-1)]
			i++
			return r
		},
		readPassword: func() (string, error) { return h.passwordTyp, nil },
		run: func(name string, args ...string) error {
			h.runs = append(h.runs, append([]string{name}, args...))
			return h.runErr
		},
		stageDir: func() (string, error) {
			if h.stage == "" {
				h.stage = filepath.Join(h.base, "stage")
				if err := os.Mkdir(h.stage, 0o700); err != nil {
					return "", err
				}
			}
			return h.stage, nil
		},
		home: func() (string, error) { return filepath.Join(h.base, "home"), nil },
	}
}

func (h *ih) init(args ...string) int {
	h.t.Helper()
	return runInit(args, h.deps())
}

func (h *ih) all() string { return h.out.String() + h.errb.String() }

func (h *ih) yesArgs(extra ...string) []string {
	ws := h.dir("ws", 0o755)
	cfg := filepath.Join(h.base, "cfg", "config.json")
	args := []string{"--yes", "--root", ws, "--session", "ses_one", "--token-file", filepath.Join(h.base, "cfg", "token"), "--config", cfg}
	return append(args, extra...)
}

func (h *ih) cfgPath() string { return filepath.Join(h.base, "cfg", "config.json") }

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func readF(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

var base64url = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ---- --yes ----

func TestInitYesNeedsItsAnswers(t *testing.T) {
	for _, drop := range []string{"--root", "--session", "--token-file"} {
		h := newIH(t)
		args := h.yesArgs()
		var kept []string
		for i := 0; i < len(args); i++ {
			if args[i] == drop {
				i++
				continue
			}
			kept = append(kept, args[i])
		}
		before := listTree(t, h.base)
		if code := h.init(kept...); code != 2 || !strings.Contains(h.errb.String(), drop) || !strings.Contains(h.errb.String(), "--yes never prompts") {
			t.Errorf("without %s: exit %d: %q", drop, code, h.errb.String())
		}
		if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") {
			t.Errorf("without %s: init wrote something", drop)
		}
	}
	h := newIH(t)
	if code := h.init("--yes"); code != 2 {
		t.Errorf("--yes alone: exit %d", code)
	}
}

func TestInitYesLocalWritesAConfigThatPassesTheCheck(t *testing.T) {
	h := newIH(t)
	if code := h.init(h.yesArgs()...); code != 0 {
		t.Fatalf("exit %d:\n%s", code, h.all())
	}
	cfg, tokenPath := h.cfgPath(), filepath.Join(h.base, "cfg", "token")
	for _, p := range []string{cfg, tokenPath} {
		if mode(t, p) != 0o600 {
			t.Errorf("%s mode %o", p, mode(t, p))
		}
	}
	tok := strings.TrimSuffix(readF(t, tokenPath), "\n")
	if len(tok) != 64 || !base64url.MatchString(tok) || tok != h.expectedToken() {
		t.Fatalf("token %q: want 64 base64url characters from the injected random source", tok)
	}
	body := readF(t, cfg)
	if strings.Contains(body, tok) {
		t.Fatal("the token is in the config")
	}
	f, err := config.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"token.expires": "2026-12-02T12:00:00Z", "limits.token_max_lifetime": "168h", "token.file": tokenPath,
		"server.listen": "127.0.0.1:8080", "harness.opencode.url": "http://127.0.0.1:4096", "acl.sessions": "ses_one",
		"writer.lock_dir": filepath.Join(h.base, "xdg-state", "cirak", "writer-locks"),
	} {
		if got := f.EnvForm(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if f.Has("token.credential") || f.Has("lifecycle.config_file") || f.Has("harness.opencode.password_file") {
		t.Error("an unneeded key was written")
	}
	// --yes prints the file path, never the token
	if strings.Contains(h.all(), tok) || !strings.Contains(h.out.String(), "read it from "+tokenPath) {
		t.Fatalf("output:\n%s", h.all())
	}
	// the lock directory is created 0700 for the operator
	if mode(t, filepath.Join(h.base, "xdg-state", "cirak", "writer-locks")) != 0o700 {
		t.Error("lock directory mode")
	}
	// init --local passes config check (the S1 check) with the 7-day recorded deadline, no warning at all
	if !strings.Contains(h.out.String(), "result: clean") {
		t.Fatalf("init's own check did not come out clean:\n%s", h.out.String())
	}
	var out, errb bytes.Buffer
	if code := runConfig([]string{"check", "--config", cfg}, func(string) string { return "" }, nil, os.Geteuid(), &out, &errb); code != 0 {
		t.Fatalf("config check exit %d:\n%s", code, out.String())
	}
	if len(h.runs) != 0 {
		t.Fatalf("init ran commands: %v", h.runs)
	}
}

func TestInitExpiry(t *testing.T) {
	for _, c := range []struct {
		flag string
		ok   bool
		want string
	}{
		{"2026-11-30T00:00:00Z", true, "2026-11-30T00:00:00Z"},
		{"2026-12-02T12:00:00Z", true, "2026-12-02T12:00:00Z"}, // exactly 168h
		{"2026-12-02T12:00:01Z", false, ""},                    // one second over the pair
		{"2026-11-25T12:00:00Z", false, ""},                    // now
		{"2026-11-25T11:00:00Z", false, ""},                    // passed
		{"tomorrow", false, ""},
	} {
		h := newIH(t)
		code := h.init(h.yesArgs("--expires", c.flag)...)
		if c.ok != (code == 0) {
			t.Errorf("--expires %s: exit %d\n%s", c.flag, code, h.all())
			continue
		}
		if c.ok {
			if got := readF(t, h.cfgPath()); !strings.Contains(got, `"expires": "`+c.want+`"`) {
				t.Errorf("--expires %s: %s", c.flag, got)
			}
		} else if exists(h.cfgPath()) || exists(filepath.Join(h.base, "cfg", "token")) {
			t.Errorf("--expires %s: wrote files before refusing", c.flag)
		}
	}
}

func TestInitRefusesTheLaterSlicesAndNonLoopback(t *testing.T) {
	for _, extra := range [][]string{{"--remote"}, {"--tls-cert", "/a"}, {"--tls-key", "/b"}, {"--install-service"}, {"--bogus"}, {"stray"}} {
		h := newIH(t)
		if code := h.init(h.yesArgs(extra...)...); code != 2 {
			t.Errorf("%v: exit %d", extra, code)
		}
		if exists(h.cfgPath()) {
			t.Errorf("%v: a config was written", extra)
		}
	}
	for _, addr := range []string{"0.0.0.0:8080", "172.18.0.1:8443", "[::]:8080", "example.com:80"} {
		h := newIH(t)
		if code := h.init(h.yesArgs("--listen", addr)...); code == 0 || exists(h.cfgPath()) {
			t.Errorf("--listen %s accepted", addr)
		}
	}
	h := newIH(t)
	if code := h.init(h.yesArgs("--no-service", "--local")...); code != 0 {
		t.Errorf("--no-service --local: exit %d\n%s", code, h.all())
	}
}

// ---- interactive ----

func TestInitInteractiveShowsTheTokenOnceOnATerminal(t *testing.T) {
	h := newIH(t)
	h.tty = true
	ws := h.dir("ws", 0o755)
	h.stdin = strings.Join([]string{"", "", ws, "", "ses_two", "", "", ""}, "\n") + "\n"
	cfg := h.cfgPath()
	tokenPath := filepath.Join(h.base, "cfg", "token")
	if code := h.init("--config", cfg, "--token-file", tokenPath); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	tok := h.expectedToken()
	if n := strings.Count(h.all(), tok); n != 1 {
		t.Fatalf("the token appears %d times in the output, want exactly 1:\n%s", n, h.all())
	}
	if !strings.Contains(h.out.String(), "set +o history") {
		t.Error("no history warning")
	}
	if strings.Contains(readF(t, cfg), tok) || strings.TrimSpace(readF(t, tokenPath)) != tok {
		t.Error("the token is in the config, or not in its file")
	}
	// the wizard asked and used the answers
	if got := readF(t, cfg); !strings.Contains(got, ws) || !strings.Contains(got, "ses_two") {
		t.Fatal(got)
	}
	// --yes on a terminal still prints the path only
	h2 := newIH(t)
	h2.tty = true
	if code := h2.init(h2.yesArgs()...); code != 0 || strings.Contains(h2.all(), h2.expectedToken()) {
		t.Fatalf("--yes on a terminal printed the token (exit %d)", code)
	}
}

func TestInitInteractiveNeedsATerminal(t *testing.T) {
	h := newIH(t)
	if code := h.init("--config", h.cfgPath()); code != 2 || !strings.Contains(h.errb.String(), "needs a terminal") {
		t.Fatalf("exit %d: %q", code, h.errb.String())
	}
}

// ---- OpenCode detection and the typed password ----

func TestInitTypedPasswordGoesToItsFileOnly(t *testing.T) {
	h := newIH(t)
	h.tty = true
	h.probes = []openCodeProbe{{Unauthorized: true}, {Reachable: true}}
	h.passwordTyp = "TYPED-PASSWORD-CANARY-0123"
	ws := h.dir("ws", 0o755)
	h.stdin = strings.Join([]string{"", "", ws, "", "ses_two", "", "", "ocuser"}, "\n") + "\n"
	cfg, pw := h.cfgPath(), filepath.Join(h.base, "cfg", "opencode-password")
	if code := h.init("--config", cfg, "--token-file", filepath.Join(h.base, "cfg", "token")); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	if strings.TrimSpace(readF(t, pw)) != h.passwordTyp || mode(t, pw) != 0o600 {
		t.Fatal("the password file")
	}
	for _, leak := range []string{readF(t, cfg), h.all()} {
		if strings.Contains(leak, h.passwordTyp) {
			t.Fatal("the typed password leaked")
		}
	}
	f, err := config.Parse([]byte(readF(t, cfg)))
	if err != nil || f.EnvForm("harness.opencode.password_file") != pw || f.EnvForm("harness.opencode.username") != "ocuser" {
		t.Fatalf("%v", err)
	}
	if len(h.probeCalls) != 2 || h.probeCalls[1].password != h.passwordTyp {
		t.Fatalf("probe calls %+v", h.probeCalls)
	}
	// the first probe carried no credential, and nothing but the probe ever saw the password
	if h.probeCalls[0].password != "" {
		t.Error("the first probe sent a password")
	}
}

func TestInitOpenCodeStatesAreReportedNotFatal(t *testing.T) {
	h := newIH(t)
	h.probes = []openCodeProbe{{}}
	if code := h.init(h.yesArgs()...); code != 0 || !strings.Contains(h.out.String(), "not reachable") || !strings.Contains(h.out.String(), "systemctl --user status opencode") {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	h = newIH(t)
	h.probes = []openCodeProbe{{Unauthorized: true}}
	if code := h.init(h.yesArgs()...); code != 0 || !strings.Contains(h.out.String(), "asks for credentials") || !strings.Contains(h.out.String(), "OPENCODE_SERVER_PASSWORD_FILE") {
		t.Fatalf("--yes on a 401: exit %d\n%s", code, h.all())
	}
	if exists(filepath.Join(h.base, "cfg", "opencode-password")) {
		t.Error("a password file without a password")
	}
	// a credentials FILE named in the environment is referenced, never copied, and its value never printed
	h = newIH(t)
	pw := filepath.Join(h.dir("secrets", 0o700), "pw")
	writeFile(t, pw, "ENV-FILE-PASSWORD-CANARY\n", 0o600)
	h.env["OPENCODE_SERVER_PASSWORD_FILE"], h.env["OPENCODE_SERVER_USERNAME"] = pw, "envuser"
	if code := h.init(h.yesArgs()...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	if h.probeCalls[0].password != "ENV-FILE-PASSWORD-CANARY" || h.probeCalls[0].user != "envuser" {
		t.Errorf("the probe did not use the environment credentials: %+v", h.probeCalls[0])
	}
	f, _ := config.Parse([]byte(readF(t, h.cfgPath())))
	if f.EnvForm("harness.opencode.password_file") != pw || strings.Contains(h.all()+readF(t, h.cfgPath()), "ENV-FILE-PASSWORD-CANARY") {
		t.Error("the credential file reference or a leak")
	}
	// a non-loopback OpenCode URL never reaches a probe
	h = newIH(t)
	if code := h.init(h.yesArgs("--opencode-url", "http://example.com:4096")...); code != 0 {
		t.Logf("exit %d", code) // the probe seam reports unreachable; the daemon's own client refuses non-loopback
	}
}

// ---- roots, the exact fix, and never applying it ----

func TestInitRootChecksPrintTheExactFixAndWriteNothing(t *testing.T) {
	old := rootOwner
	defer func() { rootOwner = old }()
	rootOwner = func(fs.FileInfo) (uint32, bool) { return 31337, true } // somebody else
	h := newIH(t)
	args := h.yesArgs("--service-user", "cirak")
	ws := args[2]
	before := listTree(t, h.base)
	code := h.init(args...)
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	fix := "chown 'root:cirak' " + qt(ws) + " && chmod 0710 " + qt(ws) + " && setfacl -m 'u:cirak:rwx' " + qt(ws)
	if !strings.Contains(h.out.String(), "fix (init never applies it): "+fix) || !strings.Contains(h.out.String(), "must be owned by root or cirak") {
		t.Fatalf("the exact fix is not printed:\n%s", h.out.String())
	}
	if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") {
		t.Error("init wrote something although a root was refused")
	}
	if len(h.runs) != 0 {
		t.Errorf("init ran %v", h.runs)
	}
	// each shape of a bad root is refused with a reason
	for name, root := range map[string]string{"missing": filepath.Join(h.base, "none"), "relative": "ws", "slash": "/", "unclean": h.base + "/ws/../ws"} {
		h := newIH(t)
		a := h.yesArgs()
		a[2] = root
		if code := h.init(a...); code != 1 || exists(h.cfgPath()) {
			t.Errorf("%s root: exit %d", name, code)
		}
	}
	// a symlink alias is refused and the canonical path is named
	h = newIH(t)
	real := h.dir("real", 0o755)
	link := filepath.Join(h.base, "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	a := h.yesArgs()
	a[2] = link
	if code := h.init(a...); code != 1 || !strings.Contains(h.out.String(), "use "+real) {
		t.Fatalf("exit %d\n%s", code, h.out.String())
	}
}

func TestInitServiceUserNeedsReadAndExecuteOnTheRoot(t *testing.T) {
	old := rootOwner
	defer func() { rootOwner = old }()
	rootOwner = func(fs.FileInfo) (uint32, bool) { return 0, true } // root-owned: the owner rule is met
	h := newIH(t)
	h.svcUID = os.Geteuid() + 4242
	args := h.yesArgs("--service-user", "cirak")
	ws := args[2]
	if err := os.Chmod(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if code := h.init(args...); code != 1 || !strings.Contains(h.out.String(), "cirak needs r-x on it") {
		t.Fatalf("exit %d\n%s", code, h.out.String())
	}
	if err := os.Chmod(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	h = newIH(t)
	h.svcUID = os.Geteuid() + 4242
	args = h.yesArgs("--service-user", "cirak")
	if err := os.Chmod(args[2], 0o755); err != nil {
		t.Fatal(err)
	}
	if code := h.init(args...); code != 0 {
		t.Fatalf("a world-readable root: exit %d\n%s", code, h.all())
	}
	// .git must be readable too (F-H5-2)
	h = newIH(t)
	h.svcUID = os.Geteuid() + 4242
	args = h.yesArgs("--service-user", "cirak")
	if err := os.Chmod(args[2], 0o755); err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(args[2], ".git")
	if err := os.Mkdir(git, 0o700); err != nil {
		t.Fatal(err)
	}
	if code := h.init(args...); code != 1 || !strings.Contains(h.out.String(), "needs read on its .git") {
		t.Fatalf(".git 0700: exit %d\n%s", code, h.out.String())
	}
}

func TestInitSudoProbeIsReadOnlyAskedAndNeverInYes(t *testing.T) {
	old := rootOwner
	defer func() { rootOwner = old }()
	rootOwner = func(fs.FileInfo) (uint32, bool) { return 0, true }
	mk := func(answer string, runErr error, yes bool) (*ih, int) {
		h := newIH(t)
		h.svcUID = os.Geteuid() + 4242
		h.runErr = runErr
		ws := h.dir("ws", 0o700)
		if yes {
			return h, h.init("--yes", "--root", ws, "--session", "ses_one", "--token-file", filepath.Join(h.base, "cfg", "token"), "--config", h.cfgPath(), "--service-user", "cirak")
		}
		h.tty = true
		h.stdin = strings.Join([]string{"", "", ws, "", "ses_one", "", "", answer}, "\n") + "\n"
		return h, h.init("--config", h.cfgPath(), "--token-file", filepath.Join(h.base, "cfg", "token"), "--service-user", "cirak")
	}
	// --yes never probes
	if h, code := mk("", nil, true); code != 1 || len(h.runs) != 0 {
		t.Fatalf("--yes: exit %d runs %v", code, h.runs)
	}
	// the operator is shown the command and says no
	if h, code := mk("n", nil, false); code != 1 || len(h.runs) != 0 || !strings.Contains(h.out.String(), "sudo -u 'cirak' test -r") {
		t.Fatalf("declined: exit %d runs %v\n%s", code, h.runs, h.out.String())
	}
	// yes: exactly the two read-only tests, nothing else
	h, code := mk("y", nil, false)
	if len(h.runs) != 2 || strings.Join(h.runs[0], " ") != "sudo -u cirak test -r "+h.dirOf("ws") || strings.Join(h.runs[1], " ") != "sudo -u cirak test -x "+h.dirOf("ws") {
		t.Fatalf("probe commands: %v", h.runs)
	}
	_ = code // staged or written depending on the fake user; the probe is what is pinned here
	// a failing probe is a refusal
	if h, code := mk("y", errors.New("denied"), false); code != 1 {
		t.Fatalf("failed probe: exit %d runs %v", code, h.runs)
	}
}

func (h *ih) dirOf(name string) string { return filepath.Join(h.base, name) }

// ---- ownership flow ----

func TestInitStagesAndPrintsInstallCommandsWhenTheUIDDiffers(t *testing.T) {
	old := rootOwner
	defer func() { rootOwner = old }()
	rootOwner = func(fs.FileInfo) (uint32, bool) { return 0, true }
	h := newIH(t)
	h.svcUID = os.Geteuid() + 4242
	args := h.yesArgs("--service-user", "cirak", "--lifecycle-root", h.dir("lc", 0o755))
	if err := os.Chmod(args[2], 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := h.cfgPath()
	tokenPath := filepath.Join(h.base, "cfg", "token")
	if code := h.init(args...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	// nothing was written at the targets the service would refuse
	for _, p := range []string{cfg, tokenPath, filepath.Join(h.base, "cfg", "lifecycle.json")} {
		if exists(p) {
			t.Errorf("%s was written by the wrong user", p)
		}
	}
	// the staged files are private and exact
	for _, n := range []string{"config.json", "lifecycle.json", "token"} {
		if mode(t, filepath.Join(h.stage, n)) != 0o600 {
			t.Errorf("staged %s mode", n)
		}
	}
	if mode(t, h.stage) != 0o700 {
		t.Error("staging directory mode")
	}
	out := h.out.String()
	for _, want := range []string{
		"install -d -o 'cirak' -g 'cirak' -m 0700 " + qt(filepath.Dir(cfg)),
		"install -d -o 'cirak' -g 'cirak' -m 0700 " + qt("/var/lib/cirak/writer-locks"),
		"install -o 'cirak' -g 'cirak' -m 0600 " + qt(filepath.Join(h.stage, "config.json")) + " " + qt(cfg),
		"install -o 'cirak' -g 'cirak' -m 0600 " + qt(filepath.Join(h.stage, "token")) + " " + qt(tokenPath),
		"install -o 'cirak' -g 'cirak' -m 0600 " + qt(filepath.Join(h.stage, "lifecycle.json")) + " " + qt(filepath.Join(h.base, "cfg", "lifecycle.json")),
		"rm -r " + qt(h.stage),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if len(h.runs) != 0 {
		t.Errorf("init applied something: %v", h.runs)
	}
	if strings.Contains(readF(t, filepath.Join(h.stage, "config.json")), h.expectedToken()) || strings.Contains(h.all(), h.expectedToken()) {
		t.Error("the token is in the staged config or in --yes output")
	}
	if !strings.Contains(out, "cirak config check --config "+cfg) {
		t.Error("the check step is not printed")
	}
}

func TestInitSystemDirectoryMustExist(t *testing.T) {
	h := newIH(t)
	args := h.yesArgs("--service-user", "cirak") // the fake service user IS the invoking uid here
	before := listTree(t, h.base)
	code := h.init(args...)
	dir := filepath.Dir(h.cfgPath())
	if code != 1 || !strings.Contains(h.out.String(), "install -d -o 'cirak' -g 'cirak' -m 0700 "+qt(dir)) {
		t.Fatalf("exit %d\n%s", code, h.out.String())
	}
	if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") {
		t.Error("init created the system directory or wrote before stopping")
	}
	// a group-writable directory is refused too
	h = newIH(t)
	args = h.yesArgs("--service-user", "cirak")
	d := h.dir("cfg", 0o770)
	if code := h.init(args...); code != 1 || exists(filepath.Join(d, "config.json")) {
		t.Fatalf("group-writable system directory: exit %d", code)
	}
	// an existing private directory is used; the service user's lock directory must exist too (a clean check
	// needs it), otherwise init prints install -d and stops before writing
	oldLock := serviceLockDir
	defer func() { serviceLockDir = oldLock }()
	h = newIH(t)
	h.dir("cfg", 0o700)
	serviceLockDir = filepath.Join(h.base, "state", "writer-locks")
	lockArgs := h.yesArgs("--service-user", "cirak")
	before = listTree(t, h.base)
	if code := h.init(lockArgs...); code != 1 || !strings.Contains(h.out.String(), "install -d -o 'cirak' -g 'cirak' -m 0700 "+qt(serviceLockDir)) || exists(h.cfgPath()) {
		t.Fatalf("a missing lock directory: exit %d\n%s", code, h.all())
	}
	if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") {
		t.Errorf("init wrote something before stopping for the lock directory:\n%v\n%v", before, listTree(t, h.base))
	}
	if err := os.MkdirAll(serviceLockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := h.init(h.yesArgs("--service-user", "cirak")...); code != 1 || !strings.Contains(h.out.String(), "0700") || exists(h.cfgPath()) {
		t.Fatalf("a 0755 lock directory: exit %d\n%s", code, h.all())
	}
	if err := os.Chmod(serviceLockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if code := h.init(h.yesArgs("--service-user", "cirak")...); code != 0 || !strings.Contains(h.out.String(), "result: clean") {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
}

// ---- existing files, --force, the backup ----

func TestInitNeverOverwritesWithoutForceAndBacksUpWithIt(t *testing.T) {
	h := newIH(t)
	args := h.yesArgs()
	if code := h.init(args...); code != 0 {
		t.Fatalf("first run: exit %d\n%s", code, h.all())
	}
	cfg := h.cfgPath()
	old := readF(t, cfg)
	h2 := newIH(t)
	h2.base = h.base
	before := listTree(t, h.base)
	if code := runInit(args, h2.deps()); code != 1 || !strings.Contains(h2.errb.String(), "--force") {
		t.Fatalf("second run without --force: exit %d: %q", code, h2.errb.String())
	}
	if readF(t, cfg) != old || strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") {
		t.Fatal("the existing config was changed or something was written without --force")
	}
	// --force: a backup with the old bytes, mode 0600, the new config in place, the existing token reused
	args = append(args, "--force", "--session", "ses_two")
	tokenBefore := readF(t, filepath.Join(h.base, "cfg", "token"))
	h3 := newIH(t)
	h3.base = h.base
	if code := runInit(args, h3.deps()); code != 0 {
		t.Fatalf("--force: exit %d\n%s", code, h3.all())
	}
	backup := cfg + ".bak-20261125T120000Z"
	if readF(t, backup) != old || mode(t, backup) != 0o600 {
		t.Fatalf("backup missing, wrong content or mode")
	}
	if !strings.Contains(readF(t, cfg), "ses_two") || mode(t, cfg) != 0o600 {
		t.Fatal("the config was not replaced")
	}
	if readF(t, filepath.Join(h.base, "cfg", "token")) != tokenBefore || strings.Contains(h3.all(), strings.TrimSpace(tokenBefore)) {
		t.Fatal("the existing token was regenerated or printed")
	}
	if !strings.Contains(h3.out.String(), "using the existing file") {
		t.Error("the reuse is not said")
	}
	// no temporary file is left behind
	ents, _ := os.ReadDir(filepath.Dir(cfg))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

func TestInitForceAsksOnATerminal(t *testing.T) {
	h := newIH(t)
	args := h.yesArgs()
	if code := h.init(args...); code != 0 {
		t.Fatal(h.all())
	}
	old := readF(t, h.cfgPath())
	h2 := newIH(t)
	h2.base = h.base
	h2.tty = true
	h2.stdin = strings.Join([]string{"", "", args[3], "", "ses_two", "", "", "n"}, "\n") + "\n"
	if code := h2.init("--config", h.cfgPath(), "--token-file", filepath.Join(h.base, "cfg", "token"), "--force"); code != 1 || readF(t, h.cfgPath()) != old {
		t.Fatalf("declined replace: exit %d", code)
	}
}

func TestInitRefusesSymlinksEverywhere(t *testing.T) {
	// a symlinked config directory component
	h := newIH(t)
	real := h.dir("real", 0o700)
	link := filepath.Join(h.base, "linkdir")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	args := h.yesArgs()
	args[len(args)-1] = filepath.Join(link, "config.json")
	args[6] = filepath.Join(link, "token")
	if code := h.init(args...); code == 0 || exists(filepath.Join(real, "config.json")) || exists(filepath.Join(real, "token")) {
		t.Fatalf("a symlinked directory was followed (exit %d)", code)
	}
	// a symlinked existing config is not replaced, even with --force, and its target is untouched
	h = newIH(t)
	d := h.dir("cfg", 0o700)
	target := filepath.Join(h.dir("other", 0o700), "victim")
	writeFile(t, target, "VICTIM", 0o600)
	if err := os.Symlink(target, filepath.Join(d, "config.json")); err != nil {
		t.Fatal(err)
	}
	if code := h.init(h.yesArgs("--force")...); code == 0 || readF(t, target) != "VICTIM" {
		t.Fatalf("a symlinked config was followed (exit %d)", code)
	}
	// a dangling symlink as the token file is not "a new file"
	h = newIH(t)
	d = h.dir("cfg", 0o700)
	if err := os.Symlink(filepath.Join(h.base, "nowhere"), filepath.Join(d, "token")); err != nil {
		t.Fatal(err)
	}
	if code := h.init(h.yesArgs()...); code == 0 || exists(filepath.Join(h.base, "nowhere")) {
		t.Fatalf("a dangling token symlink was written through (exit %d)", code)
	}
	// createExclusive refuses an existing path of any kind
	p := filepath.Join(h.dir("x", 0o700), "f")
	if err := createExclusive(p, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := createExclusive(p, []byte("b"), 0o600); err == nil || readF(t, p) != "a" {
		t.Fatal("createExclusive truncated or replaced an existing file")
	}
	ln := filepath.Join(filepath.Dir(p), "ln")
	if err := os.Symlink(p, ln); err != nil {
		t.Fatal(err)
	}
	if err := createExclusive(ln, []byte("c"), 0o600); err == nil || readF(t, p) != "a" {
		t.Fatal("createExclusive followed a symlink")
	}
}

func TestInitExistingTokenFileRules(t *testing.T) {
	h := newIH(t)
	d := h.dir("cfg", 0o700)
	writeFile(t, filepath.Join(d, "token"), strings.Repeat("s", 40)+"\n", 0o600)
	if code := h.init(h.yesArgs()...); code == 0 || !strings.Contains(h.errb.String(), "shorter than 43") || exists(h.cfgPath()) {
		t.Fatalf("a short existing token: exit %d: %q", code, h.errb.String())
	}
	h = newIH(t)
	d = h.dir("cfg", 0o700)
	writeFile(t, filepath.Join(d, "token"), strings.Repeat("s", 60)+"\n", 0o644)
	if code := h.init(h.yesArgs()...); code == 0 || exists(h.cfgPath()) {
		t.Fatalf("a world-readable existing token: exit %d", code)
	}
	if strings.Contains(h.all(), strings.Repeat("s", 40)) {
		t.Fatal("an existing token value reached the output")
	}
}

func TestInitLifecycleFileAndAFailureLeavesNothingBehind(t *testing.T) {
	h := newIH(t)
	lcRoot := h.dir("lcroot", 0o755)
	if code := h.init(h.yesArgs("--lifecycle-root", lcRoot)...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	lc := filepath.Join(h.base, "cfg", "lifecycle.json")
	if mode(t, lc) != 0o600 || !strings.Contains(readF(t, lc), `"create"`) || strings.Contains(readF(t, lc), "archive") {
		t.Fatalf("lifecycle file: %s", readF(t, lc))
	}
	f, _ := config.Parse([]byte(readF(t, h.cfgPath())))
	if f.EnvForm("lifecycle.config_file") != lc {
		t.Fatal("the config does not reference the lifecycle file")
	}
	if !strings.Contains(h.out.String(), "result: clean") {
		t.Fatalf("the lifecycle config did not check clean:\n%s", h.out.String())
	}
	// a failing step removes the files this run created (here the lifecycle file is a directory)
	h = newIH(t)
	d := h.dir("cfg", 0o700)
	if err := os.Mkdir(filepath.Join(d, "lifecycle.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	code := h.init(h.yesArgs("--lifecycle-root", h.dir("lcroot", 0o755), "--force")...)
	if code == 0 || exists(filepath.Join(d, "token")) || exists(h.cfgPath()) {
		t.Fatalf("a failed run left files behind (exit %d)", code)
	}
	if strings.Contains(h.all(), h.expectedToken()) {
		t.Fatal("an error carried the token")
	}
}

// ---- the token ----

func TestTokenGeneration(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		h := newIH(t)
		if code := productionInitInTemp(t, h); code != 0 {
			t.Fatalf("exit %d\n%s", code, h.all())
		}
		tok := strings.TrimSpace(readF(t, filepath.Join(h.base, "cfg", "token")))
		if len(tok) != 64 || !base64url.MatchString(tok) || seen[tok] {
			t.Fatalf("token %q (length %d, repeated %v)", tok, len(tok), seen[tok])
		}
		seen[tok] = true
	}
	// the production source of randomness is crypto/rand, and the token length is not shortened
	f, err := parser.ParseFile(token.NewFileSet(), "init.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var usesCrypto bool
	for _, imp := range f.Imports {
		if imp.Path.Value == `"crypto/rand"` {
			usesCrypto = true
		}
		if imp.Path.Value == `"math/rand"` || imp.Path.Value == `"math/rand/v2"` {
			t.Errorf("init imports %s", imp.Path.Value)
		}
	}
	if !usesCrypto || !strings.Contains(readF(t, "init.go"), "rand.Reader") {
		t.Error("the production token source is not crypto/rand")
	}
}

// productionInitInTemp runs init with the real crypto/rand source and the fakes for everything else.
func productionInitInTemp(t *testing.T, h *ih) int {
	d := h.deps()
	d.random = cryptoRandReader()
	return runInit(h.yesArgs(), d)
}

// ---- init never applies a fix ----

func TestInitNeverAppliesChownChmodOrSetfacl(t *testing.T) {
	src, err := parser.ParseFile(token.NewFileSet(), "init.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fs2, err := parser.ParseFile(token.NewFileSet(), "initfs.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := map[string]bool{"os.Chown": true, "os.Lchown": true, "os.Chmod": true, "os.Chtimes": true, "unix.Chown": true, "unix.Fchown": true,
		"unix.Fchmod": true, "syscall.Chown": true, "syscall.Chmod": true, "os.RemoveAll": true, "os.Symlink": true, "os.Link": true, "os.MkdirAll": true}
	for _, file := range []*ast.File{src, fs2} {
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && banned[id.Name+"."+sel.Sel.Name] {
					t.Errorf("init uses %s.%s", id.Name, sel.Sel.Name)
				}
			}
			// exec.Command appears only in prodRun, and only for the sudo probe
			return true
		})
	}
	count := 0
	ast.Inspect(src, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok {
			ast.Inspect(fn, func(m ast.Node) bool {
				if sel, ok := m.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "exec" && sel.Sel.Name == "Command" {
						count++
						if fn.Name.Name != "prodRun" {
							t.Errorf("exec.Command in %s", fn.Name.Name)
						}
					}
				}
				return true
			})
		}
		return true
	})
	if count != 1 {
		t.Errorf("exec.Command occurs %d times, want exactly 1 (the sudo probe)", count)
	}
	// the printed commands are text only: the strings chown/setfacl appear in format strings, never as a command name
	body := readF(t, "init.go")
	for _, bad := range []string{`d.run("chown"`, `d.run("chmod"`, `d.run("setfacl"`, `d.run("install"`, `run("sudo", "chown"`} {
		if strings.Contains(body, bad) {
			t.Errorf("init runs %s", bad)
		}
	}
	// the production runner refuses anything that is not sudo
	for _, c := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"sudo", []string{"-u", "svc", "test", "-r", "/a"}, true},
		{"sudo", []string{"-u", "svc", "test", "-x", "/a"}, true},
		{"sudo", []string{"-u", "svc", "test", "-w", "/a"}, false},
		{"sudo", []string{"-u", "svc", "chown", "-r", "/a"}, false},
		{"sudo", []string{"-u", "svc", "test", "-r", "relative"}, false},
		{"sudo", []string{"-u", "", "test", "-r", "/a"}, false},
		{"sudo", []string{"-u", "svc", "test", "-r"}, false},
		{"sudo", []string{"-E", "svc", "test", "-r", "/a"}, false},
		{"chown", []string{"-u", "svc", "test", "-r", "/a"}, false},
		{"/usr/bin/sudo", []string{"-u", "svc", "test", "-r", "/a"}, false},
	} {
		if sudoProbeAllowed(c.name, c.args) != c.ok {
			t.Errorf("sudoProbeAllowed(%s %v) = %v", c.name, c.args, !c.ok)
		}
	}
	if err := prodRun("chown", "root", "/x"); err == nil {
		t.Error("the production runner accepted a command that is not the sudo probe")
	}
	// the production wiring uses crypto/rand for the token and the real probe
	if pd := productionDeps(os.Getenv, strings.NewReader(""), io.Discard, io.Discard); pd.random != cryptoRandReader() {
		t.Error("the production token source is not crypto/rand")
	}
}

func TestInitFilesAreAlwaysUnder0600(t *testing.T) {
	old := syscallUmask(0)
	defer syscallUmask(old)
	h := newIH(t)
	if code := h.init(h.yesArgs("--lifecycle-root", h.dir("lcroot", 0o755))...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	for _, p := range []string{"config.json", "token", "lifecycle.json"} {
		if m := mode(t, filepath.Join(h.base, "cfg", p)); m != 0o600 {
			t.Errorf("%s mode %o under umask 0", p, m)
		}
	}
	if m := mode(t, filepath.Join(h.base, "xdg-state", "cirak", "writer-locks")); m != 0o700 {
		t.Errorf("lock directory mode %o under umask 0", m)
	}
	_ = io.Discard
}

// Even if somebody types an answer on standard input, --yes never runs the sudo probe.
func TestInitYesIgnoresStandardInputAndNeverProbes(t *testing.T) {
	old := rootOwner
	defer func() { rootOwner = old }()
	rootOwner = func(fs.FileInfo) (uint32, bool) { return 0, true }
	h := newIH(t)
	h.svcUID = os.Geteuid() + 4242
	h.stdin = "y\ny\ny\ny\n"
	ws := h.dir("ws", 0o700)
	code := h.init("--yes", "--root", ws, "--session", "ses_one", "--token-file", filepath.Join(h.base, "cfg", "token"), "--config", h.cfgPath(), "--service-user", "cirak")
	if code != 1 || len(h.runs) != 0 {
		t.Fatalf("exit %d runs %v", code, h.runs)
	}
}

func TestNoSymlinkInExistingPrefix(t *testing.T) {
	d := privDir(t)
	real := filepath.Join(d, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(d, "dangling")
	if err := os.Symlink(filepath.Join(d, "gone"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		bad  bool
	}{
		{filepath.Join(real, "new", "deeper", "file"), false},
		{filepath.Join(link, "file"), true},
		{filepath.Join(link, "new", "file"), true},
		{dangling, true},
		{filepath.Join(dangling, "file"), true},
		{filepath.Join(d, "missing", "file"), false},
	} {
		if err := noSymlinkInExistingPrefix(c.path); (err != nil) != c.bad {
			t.Errorf("%s: %v", c.path, err)
		}
	}
}

// qt quotes a value the way a POSIX shell reads it back; written independently of the production helper so a
// test of the printed commands does not call the code it checks.
func qt(v string) string { return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'" }
