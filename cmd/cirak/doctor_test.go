package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// `cirak doctor` (ADR-027 S3): temp directories, loopback servers and fakes only.

type dh struct {
	t        *testing.T
	f        *fixture
	env      map[string]string
	out, err bytes.Buffer
	show     string
	showErr  error
	files    map[string]string // fake environment files; "" content with a nil entry means "does not exist"
	denied   map[string]bool
	oc       openCodeLive
	listen   bool
	smokeRes []string
	smokeErr error
	smokeTok string
	smokeURL string
	dialled  []string
	now      time.Time
}

func newDH(t *testing.T) *dh {
	t.Helper()
	f := newFixture(t)
	f.write(t, cleanConfig)
	return &dh{t: t, f: f, env: map[string]string{}, oc: openCodeLive{OK: true}, listen: true, now: time.Now(), files: map[string]string{}, denied: map[string]bool{}}
}

func (h *dh) deps() doctorDeps {
	var environ []string
	for k, v := range h.env {
		environ = append(environ, k+"="+v)
	}
	sort.Strings(environ)
	return doctorDeps{
		getenv: func(k string) string { return h.env[k] }, environ: environ, uid: os.Geteuid(), now: func() time.Time { return h.now },
		stdout: &h.out, stderr: &h.err,
		showUnit: func(ctx context.Context, name string) (string, error) { return h.show, h.showErr },
		readFile: func(p string) ([]byte, error) {
			if h.denied[p] {
				return nil, os.ErrPermission
			}
			if c, ok := h.files[p]; ok {
				return []byte(c), nil
			}
			return os.ReadFile(p)
		},
		openCode: func(ctx context.Context, u, user, pw string) openCodeLive { return h.oc },
		dial: func(network, addr string, d time.Duration) error {
			h.dialled = append(h.dialled, network+" "+addr)
			if network == "tcp" && !h.listen {
				return errors.New("refused")
			}
			if network == "unix" {
				return dialOnce(network, addr, d)
			}
			return nil
		},
		smoke: func(ctx context.Context, endpoint, token string) ([]string, error) {
			h.smokeURL, h.smokeTok = endpoint, token
			return h.smokeRes, h.smokeErr
		},
	}
}

func (h *dh) run(args ...string) int {
	h.t.Helper()
	return runDoctor(args, h.deps())
}

func (h *dh) all() string { return h.out.String() + h.err.String() }

func TestDoctorCleanConfigPassesAndPrintsItsSources(t *testing.T) {
	h := newDH(t)
	code := h.run("--config", h.f.cfg)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	for _, want := range []string{"environment source: this process", "config: using " + h.f.cfg, "effective settings", "config file", "result: all checks pass",
		"opencode reachable at http://127.0.0.1:4096: status and session reads answered (version: unknown)", "token deadline in", "something is listening on 127.0.0.1:8080"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, h.out.String())
		}
	}
}

func TestDoctorExitCodes(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*dh)
		want int
		text string
	}{
		{"opencode 401", func(h *dh) { h.oc = openCodeLive{Status: 401} }, 2, "HTTP 401: a credential problem"},
		{"opencode 403", func(h *dh) { h.oc = openCodeLive{Status: 403} }, 2, "HTTP 403: a credential problem"},
		{"opencode 500", func(h *dh) { h.oc = openCodeLive{Status: 500} }, 1, "answered HTTP 500 to a read"},
		{"opencode down", func(h *dh) { h.oc = openCodeLive{} }, 1, "is not reachable"},
		{"nothing listening", func(h *dh) { h.listen = false }, 1, "nothing is listening on"},
		{"deadline passed", func(h *dh) { h.now = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }, 1, "token deadline passed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newDH(t)
			c.mut(h)
			if code := h.run("--config", h.f.cfg); code != c.want || !strings.Contains(h.out.String(), c.text) {
				t.Fatalf("exit %d, want %d with %q:\n%s", code, c.want, c.text, h.out.String())
			}
		})
	}
	// a refused configuration is exit 2 and stops before any live check
	h := newDH(t)
	f := h.f
	f.write(t, `{"surprise":1}`)
	if code := h.run("--config", f.cfg); code != 2 || len(h.dialled) != 0 {
		t.Fatalf("exit %d, dialled %v", code, h.dialled)
	}
	for _, args := range [][]string{{"--nope"}, {"stray"}, {"--from-unit", "a", "--env-file", "/b"}} {
		h := newDH(t)
		if code := h.run(args...); code != 2 || !strings.Contains(h.err.String(), "usage") && !strings.Contains(h.err.String(), "exclusive") {
			t.Errorf("%v: exit %d: %q", args, code, h.err.String())
		}
	}
	h = newDH(t)
	if code := h.run("-h"); code != 0 {
		t.Errorf("-h: exit %d", code)
	}
}

func TestDoctorOutputCarriesNoSecretAndNoProviderText(t *testing.T) {
	h := newDH(t)
	pw := filepath.Join(h.f.dir, "pw")
	writeFile(t, pw, "DOCTOR-PASSWORD-CANARY-0123\n", 0o600)
	h.f.write(t, `{"token":{"file":"@TOKEN@"},"harness":{"opencode":{"password_file":"`+pw+`","username":"u","url":"http://leakuser:LEAKPW@127.0.0.1:4096"}},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	h.env["CIRAK_BOGUS"] = "DOCTOR-UNKNOWN-VALUE"
	h.env["BRIDGE_OTHER"] = "DOCTOR-LEGACY-VALUE"
	h.oc = openCodeLive{Status: 401}
	h.run("--config", h.f.cfg)
	for _, leak := range []string{canaryToken, "DOCTOR-PASSWORD-CANARY", "DOCTOR-UNKNOWN-VALUE", "DOCTOR-LEGACY-VALUE", "LEAKPW", "leakuser", h.f.token, pw} {
		if strings.Contains(h.all(), leak) {
			t.Errorf("the output contains %q:\n%s", leak, h.all())
		}
	}
	if !strings.Contains(h.out.String(), "CIRAK_BOGUS") {
		t.Error("an unknown name is not named")
	}
}

func selfSignedPEM(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "doctor-test"}, NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestDoctorTLSExpiryAndKeyRules(t *testing.T) {
	for name, c := range map[string]struct {
		notAfter time.Duration
		keyMode  os.FileMode
		want     int
		text     string
	}{
		"valid":    {90 * 24 * time.Hour, 0o600, 0, "tls certificate valid until"},
		"expiring": {5 * 24 * time.Hour, 0o600, 1, "tls certificate expires in"},
		"expired":  {-time.Hour, 0o600, 2, "tls certificate expired on"},
		"open key": {90 * 24 * time.Hour, 0o644, 2, "tls.key_file must be a private regular file"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newDH(t)
			cert, key := filepath.Join(h.f.dir, "c.pem"), filepath.Join(h.f.dir, "k.pem")
			writeFile(t, cert, string(selfSignedPEM(t, time.Now().Add(c.notAfter))), 0o644)
			writeFile(t, key, "KEY-CONTENT-CANARY\n", c.keyMode)
			h.env["CIRAK_TLS_CERT"], h.env["CIRAK_TLS_KEY"] = cert, key
			h.env["CIRAK_PUBLIC_ORIGIN"], h.env["CIRAK_PRINCIPAL"] = "https://bridge.example", "operator"
			// the fixture config sets token.expires, so the environment must not set it again; remote mode takes the
			// file's far-future deadline only when the lifetime ceiling allows it: use a short one in the file
			h.f.write(t, `{"token":{"file":"@TOKEN@","expires":"`+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
			h.env["CIRAK_LISTEN"] = "127.0.0.1:9443"
			code := h.run("--config", h.f.cfg)
			if !strings.Contains(h.out.String(), c.text) {
				t.Fatalf("missing %q (exit %d):\n%s", c.text, code, h.out.String())
			}
			if strings.Contains(h.all(), "KEY-CONTENT-CANARY") || strings.Contains(h.all(), "BEGIN CERTIFICATE") {
				t.Error("key or certificate content printed")
			}
		})
	}
	// a half pair
	h := newDH(t)
	h.env["CIRAK_TLS_CERT"] = filepath.Join(h.f.dir, "c.pem")
	if code := h.run("--config", h.f.cfg); code != 2 {
		t.Fatalf("a certificate without a key: exit %d\n%s", code, h.out.String())
	}
}

func TestDoctorSockets(t *testing.T) {
	mkSock := func(t *testing.T, dir string, mode os.FileMode, keepOpen bool) string {
		t.Helper()
		p := filepath.Join(dir, "s.sock")
		l, err := net.Listen("unix", p)
		if err != nil {
			t.Skipf("no unix sockets here: %v", err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		if keepOpen {
			t.Cleanup(func() { l.Close() })
		} else {
			l.Close() // the file stays: a stale socket
		}
		return p
	}
	// sockets live in a private directory under the fixture; the unix path must stay short
	mkdir := func(t *testing.T, mode os.FileMode) string {
		d, err := os.MkdirTemp("", "dsock")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
		if err := os.Chmod(d, mode); err != nil {
			t.Fatal(err)
		}
		return d
	}
	t.Run("answering socket", func(t *testing.T) {
		h := newDH(t)
		p := mkSock(t, mkdir(t, 0o700), 0o600, true)
		h.env["CIRAK_ADMIN_SOCKET"] = p
		if code := h.run("--config", h.f.cfg); code != 0 || !strings.Contains(h.out.String(), "socket srw------- and answering") {
			t.Fatalf("exit %d\n%s", code, h.out.String())
		}
	})
	t.Run("stale socket warns", func(t *testing.T) {
		h := newDH(t)
		p := mkSock(t, mkdir(t, 0o700), 0o600, false)
		h.env["CIRAK_DASHBOARD_SOCKET"] = p
		if code := h.run("--config", h.f.cfg); code != 1 || !strings.Contains(h.out.String(), "stale socket") || !strings.Contains(h.out.String(), "only after the service is stopped") {
			t.Fatalf("exit %d\n%s", code, h.out.String())
		}
	})
	t.Run("open socket mode", func(t *testing.T) {
		h := newDH(t)
		p := mkSock(t, mkdir(t, 0o700), 0o666, true)
		h.env["CIRAK_ADMIN_SOCKET"] = p
		if code := h.run("--config", h.f.cfg); code != 2 || !strings.Contains(h.out.String(), "srw-------") {
			t.Fatalf("exit %d\n%s", code, h.out.String())
		}
	})
	t.Run("directory not private", func(t *testing.T) {
		h := newDH(t)
		d := mkdir(t, 0o755)
		h.env["CIRAK_ADMIN_SOCKET"] = filepath.Join(d, "s.sock")
		if code := h.run("--config", h.f.cfg); code != 2 || !strings.Contains(h.out.String(), "0700") {
			t.Fatalf("exit %d\n%s", code, h.out.String())
		}
	})
	t.Run("not a socket", func(t *testing.T) {
		h := newDH(t)
		d := mkdir(t, 0o700)
		writeFile(t, filepath.Join(d, "s.sock"), "x", 0o600)
		h.env["CIRAK_ADMIN_SOCKET"] = filepath.Join(d, "s.sock")
		if code := h.run("--config", h.f.cfg); code != 2 || !strings.Contains(h.out.String(), "is not a socket") {
			t.Fatalf("exit %d\n%s", code, h.out.String())
		}
	})
	t.Run("directory missing", func(t *testing.T) {
		h := newDH(t)
		h.env["CIRAK_ADMIN_SOCKET"] = filepath.Join(h.f.dir, "run", "s.sock")
		if code := h.run("--config", h.f.cfg); code != 1 || !strings.Contains(h.out.String(), "does not exist yet") {
			t.Fatalf("exit %d\n%s", code, h.out.String())
		}
	})
}

func TestDoctorHostChecksOfTheLifecycleACLAndClaudeFiles(t *testing.T) {
	// overlay directory rules
	h := newDH(t)
	lcRoot := h.f.ws
	lc := filepath.Join(h.f.dir, "lifecycle.json")
	writeFile(t, lc, `{"roots":[{"path":"`+lcRoot+`","modes":["create","archive"]}]}`, 0o600)
	ov := filepath.Join(h.f.dir, "overlay")
	if err := os.Mkdir(ov, 0o755); err != nil {
		t.Fatal(err)
	}
	h.env["CIRAK_LIFECYCLE_CONFIG"], h.env["CIRAK_OVERLAY_DIR"] = lc, ov
	// the file and the environment must not both set it: use a config without lifecycle keys (the fixture's)
	if code := h.run("--config", h.f.cfg); code != 2 || !strings.Contains(h.out.String(), "overlay.dir") {
		t.Fatalf("a 0755 overlay: exit %d\n%s", code, h.out.String())
	}
	h2 := newDH(t)
	h2.env["CIRAK_LIFECYCLE_CONFIG"], h2.env["CIRAK_OVERLAY_DIR"] = lc, filepath.Join(h2.f.dir, "missing-overlay")
	writeFile(t, filepath.Join(h2.f.dir, "lifecycle.json"), `{"roots":[{"path":"`+h2.f.ws+`","modes":["create","archive"]}]}`, 0o600)
	h2.env["CIRAK_LIFECYCLE_CONFIG"] = filepath.Join(h2.f.dir, "lifecycle.json")
	if code := h2.run("--config", h2.f.cfg); code != 1 || !strings.Contains(h2.out.String(), "overlay.dir") || !strings.Contains(h2.out.String(), "StateDirectory=") {
		t.Fatalf("a missing overlay: exit %d\n%s", code, h2.out.String())
	}
	if !strings.Contains(h2.out.String(), "lifecycle root "+h2.f.ws+": owner and r-x as the service user") {
		t.Errorf("the root checks are not reported:\n%s", h2.out.String())
	}
	// ACL: duplicates and nested roots warn
	h3 := newDH(t)
	nested := filepath.Join(h3.f.ws, "sub")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	h3.f.write(t, `{"token":{"file":"@TOKEN@"},"acl":{"roots":["@WS@","`+nested+`"],"sessions":["ses_abc","ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	if code := h3.run("--config", h3.f.cfg); code != 1 || !strings.Contains(h3.out.String(), "more than once") || !strings.Contains(h3.out.String(), "nested inside or equal to another") {
		t.Fatalf("exit %d\n%s", code, h3.out.String())
	}
	// Claude wrapper: stat only, never read
	for name, c := range map[string]struct {
		mode os.FileMode
		want int
		text string
	}{"ok": {0o755, 0, "a regular executable file, not writable by others (its content is not read)"}, "not executable": {0o644, 2, "must be a regular executable file"}, "group writable": {0o775, 2, "group or world writable"}} {
		t.Run("wrapper "+name, func(t *testing.T) {
			h := newDH(t)
			cmd, hist, home, sess := filepath.Join(h.f.dir, "wrapper"), filepath.Join(h.f.dir, "hist"), filepath.Join(h.f.dir, "home"), filepath.Join(h.f.dir, "sessions.json")
			writeFile(t, cmd, "WRAPPER-CONTENT-CANARY\n", c.mode)
			writeFile(t, sess, "{}", 0o600)
			h.env["CIRAK_CLAUDE_COMMAND"], h.env["CIRAK_CLAUDE_SESSIONS"], h.env["CIRAK_CLAUDE_VERSIONS"] = cmd, sess, "2.1.1"
			h.env["CIRAK_CLAUDE_HISTORY_DIR"], h.env["CIRAK_CLAUDE_HOME"], h.env["CIRAK_CLAUDE_MODEL"] = hist, home, "m"
			code := h.run("--config", h.f.cfg)
			if !strings.Contains(h.out.String(), c.text) || strings.Contains(h.all(), "WRAPPER-CONTENT-CANARY") {
				t.Fatalf("exit %d, want %q:\n%s", code, c.text, h.out.String())
			}
			_ = c.want
		})
	}
}

func TestDoctorListsEnvironmentNamesNotValues(t *testing.T) {
	h := newDH(t)
	h.env["CIRAK_READ_ONLY"] = "0"
	h.f.write(t, `{"token":{"file":"@TOKEN@"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	h.env["OPENCODE_URL"] = "http://127.0.0.1:4096"
	h.env["BRIDGE_LISTEN"] = "127.0.0.1:8080"
	code := h.run("--config", h.f.cfg)
	out := h.out.String()
	if code != 1 || !strings.Contains(out, "environment in this process: 1 CIRAK_ name(s) [CIRAK_READ_ONLY], 1 legacy name(s) [BRIDGE_LISTEN], OPENCODE_ names unchanged (not legacy) [OPENCODE_URL]") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "legacy environment variable OPENCODE_") {
		t.Error("OPENCODE_ was warned as legacy")
	}
}

// ---- --from-unit and --env-file ----

const showFmt = "Environment=%s\nEnvironmentFiles=%s\nUnsetEnvironment=%s\n"

func TestDoctorReadsTheUnitNotTheShell(t *testing.T) {
	h := newDH(t)
	envFile := filepath.Join(h.f.dir, "unit.env")
	writeFile(t, envFile, "# a comment\nCIRAK_LISTEN=127.0.0.1:9191\nCIRAK_READ_ONLY='1'\nSECRET_IN_FILE=UNIT-FILE-SECRET-CANARY\n", 0o600)
	missing := filepath.Join(h.f.dir, "optional.env")
	h.show = "Environment=CIRAK_CONFIG=" + h.f.cfg + " CIRAK_LISTEN=127.0.0.1:9090 \"CIRAK_PRINCIPAL=\"\n" +
		"EnvironmentFiles=" + envFile + " (ignore_errors=no) " + missing + " (ignore_errors=yes)\nUnsetEnvironment=CIRAK_PRINCIPAL\n"
	// the SHELL has a conflicting value; the unit is the source, so there is no refusal
	h.env["CIRAK_LISTEN"] = "127.0.0.1:1111"
	h.env["CIRAK_READ_ONLY"] = "0"
	code := h.run("--from-unit", "cirak")
	out := h.out.String()
	if code == 2 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"environment source: unit cirak", "  read: " + envFile, "  skipped (optional, missing): " + missing, "environment in the source:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// EnvironmentFile= overrides Environment= (9191, not 9090), and the shell's 1111 played no part
	if !regexpMatch(out, `server\.listen\s+environment\s+127\.0\.0\.1:9191`) || strings.Contains(out, "127.0.0.1:1111") || strings.Contains(out, "127.0.0.1:9090") {
		t.Errorf("the merge is wrong:\n%s", out)
	}
	if strings.Contains(h.all(), "UNIT-FILE-SECRET-CANARY") || strings.Contains(h.all(), "SECRET_IN_FILE") && strings.Contains(h.all(), "UNIT-FILE-SECRET") {
		t.Error("a value of the unit file reached the output")
	}
}

func TestDoctorUnitSourceErrorsAreErrorsNotGuesses(t *testing.T) {
	file := func(h *dh) string { return filepath.Join(h.f.dir, "u.env") }
	cases := []struct {
		name string
		show func(h *dh) string
		prep func(h *dh)
		text string
	}{
		{"unreadable file", func(h *dh) string { return "EnvironmentFiles=" + file(h) + " (ignore_errors=no)\n" }, func(h *dh) { h.denied[file(h)] = true }, "is unreadable"},
		{"missing mandatory file", func(h *dh) string { return "EnvironmentFiles=" + file(h) + " (ignore_errors=no)\n" }, func(h *dh) {}, "does not exist and is not marked optional"},
		{"not an assignment", func(h *dh) string { return "EnvironmentFiles=" + file(h) + " (ignore_errors=no)\n" }, func(h *dh) { h.files[file(h)] = "OK=1\nthis is not an assignment CANARYLINE\n" }, "line 2 is not a NAME=VALUE assignment"},
		{"specifier in a file", func(h *dh) string { return "EnvironmentFiles=" + file(h) + " (ignore_errors=no)\n" }, func(h *dh) { h.files[file(h)] = "CIRAK_TOKEN_FILE=%d/token\n" }, "unexpanded systemd specifier"},
		{"specifier in Environment=", func(h *dh) string { return "Environment=CIRAK_TOKEN_FILE=%d/token\n" }, func(h *dh) {}, "unexpanded systemd specifier"},
		{"word that is not NAME=VALUE", func(h *dh) string { return "Environment=just-a-word\n" }, func(h *dh) {}, "not NAME=VALUE"},
		{"specifier in a path", func(h *dh) string { return "EnvironmentFiles=%d/x.env (ignore_errors=no)\n" }, func(h *dh) {}, "unexpanded systemd specifier"},
		{"relative path", func(h *dh) string { return "EnvironmentFiles=x.env (ignore_errors=no)\n" }, func(h *dh) {}, "not absolute"},
		{"unknown property", func(h *dh) string { return "ExecStart=/bin/true\n" }, func(h *dh) {}, "unexpected property"},
		{"bad ignore_errors", func(h *dh) string { return "EnvironmentFiles=/a (ignore_errors=maybe)\n" }, func(h *dh) {}, "unknown ignore_errors"},
		{"unterminated quote", func(h *dh) string { return "Environment=\"A=1\n" }, func(h *dh) {}, "unterminated quote"},
		{"bad unset", func(h *dh) string { return "UnsetEnvironment=1BAD\n" }, func(h *dh) {}, "not a variable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newDH(t)
			c.prep(h)
			h.show = c.show(h)
			code := h.run("--from-unit", "cirak")
			if code != 2 || !strings.Contains(h.out.String(), c.text) {
				t.Fatalf("exit %d, want 2 with %q:\n%s", code, c.text, h.out.String())
			}
			if strings.Contains(h.all(), "CANARYLINE") {
				t.Error("an error carried the content of a line")
			}
			if strings.Contains(h.out.String(), "config: using") {
				t.Error("the doctor went on with an empty or guessed source")
			}
		})
	}
	h := newDH(t)
	h.showErr = errors.New("exit status 4")
	if code := h.run("--from-unit", "cirak"); code != 2 || !strings.Contains(h.out.String(), "systemctl show failed") {
		t.Errorf("a failing systemctl: exit %d\n%s", code, h.out.String())
	}
	for _, bad := range []string{"", "a b", "a;b", "../x", "x\ny", "$(id)"} {
		h := newDH(t)
		if bad == "" {
			continue
		}
		if code := h.run("--from-unit", bad); code != 2 || !strings.Contains(h.out.String(), "not a valid unit name") {
			t.Errorf("unit name %q: exit %d", bad, code)
		}
	}
}

func TestDoctorEnvFileIsTheSameSource(t *testing.T) {
	h := newDH(t)
	ef := filepath.Join(h.f.dir, "x.env")
	writeFile(t, ef, "CIRAK_CONFIG="+h.f.cfg+"\nCIRAK_READ_ONLY=0\n", 0o600)
	if code := h.run("--env-file", ef); code == 2 || !strings.Contains(h.out.String(), "environment source: file") || !strings.Contains(h.out.String(), "  read: "+ef) {
		t.Fatalf("exit %d\n%s", code, h.out.String())
	}
	h2 := newDH(t)
	if code := h2.run("--env-file", filepath.Join(h2.f.dir, "nope.env")); code != 2 || !strings.Contains(h2.out.String(), "does not exist") {
		t.Fatalf("a missing --env-file: exit %d\n%s", code, h2.out.String())
	}
}

func TestUnitEnvironmentParsers(t *testing.T) {
	words, err := splitShellWords(`A=1 "B=two words" C="x\"y" D=`)
	if err != nil || strings.Join(words, "|") != `A=1|B=two words|C=x"y|D=` {
		t.Fatalf("%q %v", words, err)
	}
	fs, err := parseEnvironmentFiles("/a/x.env (ignore_errors=no) /b/y.env (ignore_errors=yes)")
	if err != nil || len(fs) != 2 || fs[0].Optional || !fs[1].Optional || fs[1].Path != "/b/y.env" {
		t.Fatalf("%+v %v", fs, err)
	}
	vars, err := parseEnvironmentFile([]byte("# c\n; c2\n\nA=1\nB=\"q v\"\nC='s'\nD=100%%\r\n"))
	if err != nil || vars["A"] != "1" || vars["B"] != "q v" || vars["C"] != "s" || vars["D"] != "100%" {
		t.Fatalf("%v %v", vars, err)
	}
	// later definitions win inside one file, and EnvironmentFile= beats Environment=, Unset removes
	u, err := loadUnitEnvironment([]string{"A=env", "B=env"}, []envFileEntry{{Path: "/f"}}, []string{"B"}, func(string) ([]byte, error) { return []byte("A=file\nA=file2\n"), nil })
	if err != nil || u.Vars["A"] != "file2" || u.getenv("B") != "" {
		t.Fatalf("%v %v", u, err)
	}
}

// ---- --smoke ----

func TestDoctorSmokeChecksTheExpectedSurface(t *testing.T) {
	all12 := append(append([]string(nil), doctorBaseTools...), "reply_permission")
	cases := []struct {
		name  string
		tools []string
		mut   func(*dh)
		want  int
		text  string
	}{
		{"expected surface", all12, nil, 0, "12 tools (12 required by the configuration), no prompt sent"},
		{"a tool missing", all12[:11], nil, 2, "tools/list lacks [reply_permission]"},
		{"a wider surface", append(append([]string(nil), all12...), "create_session"), nil, 2, "wider surface than configured: [create_session]"},
		{"relay off keeps eleven", doctorBaseTools, func(h *dh) { h.env["CIRAK_PERMISSION_RELAY"] = "off" }, 0, "11 tools (11 required"},
		{"relay off but reply_permission shown", all12, func(h *dh) { h.env["CIRAK_PERMISSION_RELAY"] = "off" }, 2, "wider surface"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newDH(t)
			if c.mut != nil {
				c.mut(h)
			}
			h.f.write(t, `{"token":{"file":"@TOKEN@"},"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
			h.smokeRes = c.tools
			code := h.run("--config", h.f.cfg, "--smoke")
			if code != c.want || !strings.Contains(h.out.String(), c.text) {
				t.Fatalf("exit %d, want %d with %q:\n%s", code, c.want, c.text, h.out.String())
			}
			if h.smokeURL != "http://127.0.0.1:8080/mcp" || h.smokeTok != canaryToken {
				t.Errorf("the smoke call got %q (token match %v)", h.smokeURL, h.smokeTok == canaryToken)
			}
			if strings.Contains(h.all(), canaryToken) {
				t.Error("the token was printed")
			}
		})
	}
	// no answer is a warning, never a failure and never a hang
	h := newDH(t)
	h.smokeErr = errors.New("connection refused")
	if code := h.run("--config", h.f.cfg, "--smoke"); code != 1 || !strings.Contains(h.out.String(), "no answer from http://127.0.0.1:8080/mcp") {
		t.Fatalf("exit %d\n%s", code, h.out.String())
	}
	// a token the process cannot see is UNVERIFIED, never a pass
	h = newDH(t)
	h.f.write(t, `{"acl":{"roots":["@WS@"],"sessions":["ses_abc"]},"writer":{"lock_dir":"@LOCKS@"}}`)
	if code := h.run("--config", h.f.cfg, "--smoke"); code != 1 || !strings.Contains(h.out.String(), "UNVERIFIED smoke") || h.smokeURL != "" {
		t.Fatalf("exit %d, called %q\n%s", code, h.smokeURL, h.out.String())
	}
	// without --smoke the service is never called
	h = newDH(t)
	h.run("--config", h.f.cfg)
	if h.smokeURL != "" {
		t.Error("the smoke call ran without --smoke")
	}
}

func TestExpectedToolsFollowTheConfiguration(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	req, allowed := expectedTools(env(nil), os.Geteuid(), false)
	if len(req) != 12 || !req["reply_permission"] || len(allowed) != 12 {
		t.Fatalf("default: %d required, %d allowed", len(req), len(allowed))
	}
	req, _ = expectedTools(env(map[string]string{"CIRAK_PERMISSION_RELAY": "off"}), os.Geteuid(), false)
	if len(req) != 11 || req["reply_permission"] {
		t.Fatalf("relay off: %d", len(req))
	}
}

// ---- the live OpenCode reads against a loopback server ----

func TestLiveOpenCodeReads(t *testing.T) {
	serve := func(status, list int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			code, okBody := status, `{}`
			if r.URL.Path == "/session" {
				code, okBody = list, `[]`
			}
			w.WriteHeader(code)
			if code == 200 {
				_, _ = w.Write([]byte(okBody))
				return
			}
			_, _ = w.Write([]byte("PROVIDER-FREE-TEXT-ERROR-CANARY"))
		}))
	}
	for name, c := range map[string]struct {
		status, list int
		want         openCodeLive
	}{"ok": {200, 200, openCodeLive{OK: true}}, "401": {401, 200, openCodeLive{Status: 401}}, "403": {403, 200, openCodeLive{Status: 403}}, "list 500": {200, 500, openCodeLive{Status: 500}}} {
		t.Run(name, func(t *testing.T) {
			srv := serve(c.status, c.list)
			defer srv.Close()
			if got := liveOpenCode(context.Background(), srv.URL, "", ""); got != c.want {
				t.Fatalf("%+v, want %+v", got, c.want)
			}
		})
	}
	// nothing listening is a transport failure (Status 0)
	if got := liveOpenCode(context.Background(), "http://127.0.0.1:1", "", ""); got.OK || got.Status != 0 {
		t.Fatalf("%+v", got)
	}
	// a non-loopback URL is refused by the client before any request
	if got := liveOpenCode(context.Background(), "http://example.com:4096", "", ""); got.OK {
		t.Fatal("a non-loopback URL was probed")
	}
}

// ---- the doctor touches nothing ----

func TestDoctorTouchesNothingAndRunsOnlySystemctlShow(t *testing.T) {
	h := newDH(t)
	before := listTree(t, h.f.dir)
	h.run("--config", h.f.cfg, "--smoke")
	if after := listTree(t, h.f.dir); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("the tree changed:\n%v\n%v", before, after)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "doctor.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{"net.Listen": true, "os.Create": true, "os.WriteFile": true, "os.Mkdir": true, "os.MkdirAll": true, "os.Remove": true,
		"os.RemoveAll": true, "os.Rename": true, "os.Chmod": true, "os.Chown": true, "os.OpenFile": true, "os.Setenv": true, "http.ListenAndServe": true}
	var execs []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok {
				name := id.Name + "." + sel.Sel.Name
				if forbidden[name] {
					t.Errorf("doctor.go uses %s in %s", name, fd.Name.Name)
				}
				if id.Name == "exec" {
					execs = append(execs, fd.Name.Name+":"+sel.Sel.Name)
				}
			}
			return true
		})
	}
	if len(execs) != 1 || execs[0] != "prodShowUnit:CommandContext" {
		t.Errorf("the doctor runs %v; only systemctl show in prodShowUnit is allowed", execs)
	}
	src := readF(t, "doctor.go")
	if !strings.Contains(src, `"systemctl", "show", "-p", "Environment", "-p", "EnvironmentFiles", "-p", "UnsetEnvironment"`) {
		t.Error("the one command is not the read-only systemctl show")
	}
}

// The documentation says what the code does (the reviews asked for these points in S2; S3 adds its own).
func TestContractsDescribesTheServiceAndDoctorSurface(t *testing.T) {
	b, err := os.ReadFile("../../docs/CONTRACTS.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, want := range []string{"### Service install (ADR-027 S3)", "### `cirak doctor` (ADR-027 S3)", "init never runs `systemctl`", "proven live", "token.credential: \"token\"",
		"`ExecStart=/usr/bin/cirak`", "omitted", "zz-cirak-init.conf", "never a second `cirak.service`", "EnvironmentFile=` overrides", "unexpanded specifier", "already running"} {
		if !strings.Contains(doc, want) {
			t.Errorf("CONTRACTS.md does not say %q", want)
		}
	}
}
