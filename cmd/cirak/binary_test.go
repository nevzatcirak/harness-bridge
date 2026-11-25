package main

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// builtBinary compiles this package once per test and returns the path. The cirakEnv scan and the
// version subcommand are properties of the real process (an exit code and what it prints), which
// no in-process call proves.
func builtBinary(t *testing.T) string {
	t.Helper()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool on PATH: the binary tests cannot run (a skip is not a pass)")
	}
	out := filepath.Join(t.TempDir(), "cirak")
	cmd := exec.Command(goTool, "build", "-o", out, ".")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	return out
}

func run(t *testing.T, bin string, env []string, args ...string) (string, int) {
	t.Helper()
	c := exec.Command(bin, args...)
	c.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, env...)
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return buf.String(), code
}

func TestBinaryVersionSubcommand(t *testing.T) {
	bin := builtBinary(t)
	out, code := run(t, bin, nil, "version")
	if code != 0 || !strings.HasPrefix(out, "cirak ") || !strings.Contains(out, "\ngo go") {
		t.Fatalf("exit %d, output %q", code, out)
	}
	if strings.Contains(out, "0.2.0") {
		t.Fatalf("the old hard-coded version is still printed: %q", out)
	}
	// `version` needs no environment and is not blocked by an environment conflict.
	out2, code2 := run(t, bin, []string{"CIRAK_READ_ONLY=1", "BRIDGE_READ_ONLY=0"}, "version")
	if code2 != 0 || out2 != out {
		t.Fatalf("version with a conflicting environment: exit %d, %q", code2, out2)
	}
}

func TestBinaryRefusesAConflictBeforeAnythingElse(t *testing.T) {
	bin := builtBinary(t)
	// The listen address is free to bind and the token is valid: the only reason to stop is the
	// conflict, and it must stop before the token or any listener is looked at.
	secret := "legacy-secret-value-0123456789abcdef0123456789"
	out, code := run(t, bin, []string{"CIRAK_TOKEN=" + strings.Repeat("n", 40), "BRIDGE_TOKEN=" + secret, "CIRAK_LISTEN=127.0.0.1:0"})
	if code == 0 {
		t.Fatalf("a conflict started the daemon: %q", out)
	}
	if !strings.Contains(out, "BRIDGE_TOKEN and CIRAK_TOKEN are both set with different values") {
		t.Fatalf("the refusal does not name both variables: %q", out)
	}
	if strings.Contains(out, secret) || strings.Contains(out, strings.Repeat("n", 40)) || strings.Contains(out, "listening") {
		t.Fatalf("the refusal printed a value or started listening: %q", out)
	}
	// a conflict in a feature that is off (the dashboard) is found too
	out, code = run(t, bin, []string{"CIRAK_DASHBOARD_ADDR=127.0.0.1:1", "BRIDGE_DASHBOARD_ADDR=127.0.0.1:2"})
	if code == 0 || !strings.Contains(out, "BRIDGE_DASHBOARD_ADDR and CIRAK_DASHBOARD_ADDR") {
		t.Fatalf("exit %d: %q", code, out)
	}
}

// The H5 host keeps its BRIDGE_* drop-in: the legacy name is read, warned about once, and the empty
// BRIDGE_TOKEN= idiom still neutralises an inherited value.
func TestBinaryAcceptsTheLegacyNamesWithOneWarning(t *testing.T) {
	bin := builtBinary(t)
	out, code := run(t, bin, []string{"BRIDGE_TOKEN=short-legacy-token", "BRIDGE_READ_ONLY=1"})
	if code == 0 {
		t.Fatalf("a short token must still refuse: %q", out)
	}
	for _, want := range []string{
		"legacy environment variable BRIDGE_TOKEN is deprecated; set CIRAK_TOKEN instead (not accepted from v0.2.0)",
		"legacy environment variable BRIDGE_READ_ONLY is deprecated; set CIRAK_READ_ONLY instead (not accepted from v0.2.0)",
		"CIRAK_TOKEN must be at least 32 characters",
	} {
		if strings.Count(out, want) != 1 {
			t.Errorf("want exactly one %q in %q", want, out)
		}
	}
	if strings.Contains(out, "short-legacy-token") {
		t.Fatalf("a warning printed a value: %q", out)
	}
	out, _ = run(t, bin, []string{"CIRAK_TOKEN=short-new-token", "BRIDGE_TOKEN="})
	if strings.Contains(out, "deprecated") || !strings.Contains(out, "CIRAK_TOKEN must be at least 32 characters") {
		t.Fatalf("an empty legacy twin must be silent: %q", out)
	}
}

// bootOnce starts the real daemon with the given environment on a free loopback port, waits until
// it answers, checks the 401 realm, stops it with SIGTERM and returns what it logged.
func bootOnce(t *testing.T, bin string, env []string) string {
	t.Helper()
	return bootOnceArgs(t, bin, env)
}

// bootOnceArgs is bootOnce with command-line arguments.
func bootOnceArgs(t *testing.T, bin string, env []string, args ...string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	c := exec.Command(bin, args...)
	c.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "OPENCODE_URL=http://127.0.0.1:1"}, env...)
	c.Env = append(c.Env, listenEnv(env, addr)...)
	var buf lockedBuffer
	c.Stdout, c.Stderr = &buf, &buf
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	t.Cleanup(func() { _ = c.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("the daemon exited before it listened: %v\n%s", err, buf.String())
		default:
		}
		r, err := http.Post("http://"+addr+"/mcp", "application/json", strings.NewReader("{}"))
		if err == nil {
			resp = r
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if resp == nil {
		t.Fatalf("the daemon never answered on %s\n%s", addr, buf.String())
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer realm="cirak"` {
		t.Fatalf("unauthenticated request: %d %q (want 401 with realm cirak)", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	_ = c.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("no clean stop on SIGTERM\n%s", buf.String())
	}
	return buf.String()
}

// listenEnv adds the listen address under the prefix the other variables use, so the same helper
// boots a legacy-only environment and a new-only one.
func listenEnv(env []string, addr string) []string {
	for _, e := range env {
		if strings.HasPrefix(e, "BRIDGE_") {
			return []string{"BRIDGE_LISTEN=" + addr}
		}
	}
	return []string{"CIRAK_LISTEN=" + addr}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// ADR-026 section 2.5: the live H5 host keeps its BRIDGE_* drop-in and must keep working. A
// legacy-only environment (the H5 shape: token, roots, sessions, read-only, an empty
// BRIDGE_TOKEN_FILE neutraliser) boots, listens, answers, warns once per variable and prints no value.
func TestBinaryBootsOnTheLegacyH5Environment(t *testing.T) {
	bin := builtBinary(t)
	root := t.TempDir()
	token := "h5-legacy-token-0123456789abcdef0123456789abcdef"
	log := bootOnce(t, bin, []string{
		"BRIDGE_TOKEN=" + token, "BRIDGE_ALLOWED_ROOTS=" + root, "BRIDGE_ALLOWED_SESSIONS=ses_h5", "BRIDGE_READ_ONLY=1",
		"BRIDGE_TOKEN_FILE=", "BRIDGE_DASHBOARD_ADDR=", "BRIDGE_TOKEN_MAX_LIFETIME=24h",
	})
	for _, name := range []string{"TOKEN", "ALLOWED_ROOTS", "ALLOWED_SESSIONS", "READ_ONLY", "LISTEN", "TOKEN_MAX_LIFETIME"} {
		want := "legacy environment variable BRIDGE_" + name + " is deprecated; set CIRAK_" + name + " instead (not accepted from v0.2.0)"
		if strings.Count(log, want) != 1 {
			t.Errorf("want exactly one warning for BRIDGE_%s, log:\n%s", name, log)
		}
	}
	if strings.Count(log, "is deprecated") != 6 {
		t.Errorf("an empty legacy variable must stay silent; log:\n%s", log)
	}
	if strings.Contains(log, token) || strings.Contains(log, root) {
		t.Errorf("a value reached the log:\n%s", log)
	}
}

func TestBinaryBootsOnTheNewEnvironmentWithoutWarnings(t *testing.T) {
	bin := builtBinary(t)
	log := bootOnce(t, bin, []string{
		"CIRAK_TOKEN=" + strings.Repeat("n", 40), "CIRAK_ALLOWED_ROOTS=" + t.TempDir(), "CIRAK_ALLOWED_SESSIONS=ses_h5", "CIRAK_READ_ONLY=1",
	})
	if strings.Contains(log, "deprecated") || !strings.Contains(log, "Bridge listening on") {
		t.Fatalf("log:\n%s", log)
	}
}

// ---- ADR-027 S1: the config layer, as the real process ----

// configEnv is the minimum environment of a daemon whose settings come from a config file.
func configBoot(t *testing.T) (dir, cfg string) {
	t.Helper()
	dir = privDir(t)
	tok := filepath.Join(dir, "token")
	writeFile(t, tok, strings.Repeat("c", 48)+"\n", 0o600)
	ws := filepath.Join(dir, "ws")
	if err := os.Mkdir(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg = filepath.Join(dir, "config.json")
	writeFile(t, cfg, `{"server":{"read_only":true},"token":{"file":"`+tok+`","expires":"2099-01-01T00:00:00Z"},"acl":{"roots":["`+ws+`"],"sessions":["ses_cfg"]}}`, 0o600)
	return dir, cfg
}

// The daemon starts from a config file alone (the file holds the token reference, the ACL and a local
// deadline), logs the path in use, and listens in local mode: TOKEN_EXPIRES alone did not make it remote.
func TestBinaryBootsFromAConfigFile(t *testing.T) {
	bin := builtBinary(t)
	_, cfg := configBoot(t)
	log := bootOnceArgs(t, bin, nil, "--config", cfg)
	if !strings.Contains(log, "config: using "+cfg) || !strings.Contains(log, "Bridge listening on") {
		t.Fatalf("log:\n%s", log)
	}
	if strings.Contains(log, "deprecated") || strings.Contains(log, "remote token lifetime") {
		t.Fatalf("a local config started in remote mode or warned:\n%s", log)
	}
	// the same file through CIRAK_CONFIG and with the optional "serve" word
	if log := bootOnceArgs(t, bin, []string{"CIRAK_CONFIG=" + cfg}, "serve"); !strings.Contains(log, "config: using "+cfg) {
		t.Fatalf("log:\n%s", log)
	}
}

// With no file at all the daemon logs that it reads the environment only (today's behaviour).
func TestBinaryWithoutAFileSaysEnvironmentOnly(t *testing.T) {
	bin := builtBinary(t)
	log := bootOnce(t, bin, []string{"CIRAK_TOKEN=" + strings.Repeat("n", 40), "CIRAK_ALLOWED_ROOTS=" + t.TempDir(), "CIRAK_ALLOWED_SESSIONS=ses_h5", "CIRAK_READ_ONLY=1"})
	if !strings.Contains(log, "config: no config file (environment only)") {
		t.Fatalf("log:\n%s", log)
	}
}

// A differing environment/file pair stops the start before any listener, naming the key and the variable.
func TestBinaryRefusesADifferingEnvironmentAndFile(t *testing.T) {
	bin := builtBinary(t)
	_, cfg := configBoot(t)
	out, code := run(t, bin, []string{"BRIDGE_READ_ONLY=0", "CIRAK_LISTEN=127.0.0.1:0"}, "--config", cfg)
	if code == 0 || !strings.Contains(out, "server.read_only") || !strings.Contains(out, "CIRAK_READ_ONLY") || strings.Contains(out, "listening") {
		t.Fatalf("exit %d: %q", code, out)
	}
	// a passed local deadline warns exactly once and still starts
	_, cfg2 := configBoot(t)
	b, _ := os.ReadFile(cfg2)
	writeFile(t, cfg2, strings.Replace(string(b), "2099-01-01T00:00:00Z", "2001-01-01T00:00:00Z", 1), 0o600)
	log := bootOnceArgs(t, bin, nil, "--config", cfg2)
	if strings.Count(log, "CIRAK_TOKEN_EXPIRES has passed") != 1 || strings.Contains(log, "2001") {
		t.Fatalf("log:\n%s", log)
	}
	// a bad flag is a usage error and never starts
	out, code = run(t, bin, nil, "--nope")
	if code != 2 || strings.Contains(out, "listening") {
		t.Fatalf("exit %d: %q", code, out)
	}
	if out, code = run(t, bin, nil, "--config", filepath.Join(t.TempDir(), "missing.json")); code == 0 || !strings.Contains(out, "does not exist") {
		t.Fatalf("a missing --config: exit %d: %q", code, out)
	}
}

func TestBinaryConfigCheck(t *testing.T) {
	bin := builtBinary(t)
	dir, cfg := configBoot(t)
	out, code := run(t, bin, nil, "config", "check", "--config", cfg)
	if code != 0 || !strings.Contains(out, "result: clean") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, strings.Repeat("c", 48)) {
		t.Fatalf("the token leaked:\n%s", out)
	}
	if err := os.Chmod(cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code = run(t, bin, nil, "config", "check", "--config", cfg); code != 2 || !strings.Contains(out, "mode 0600") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if out, code = run(t, bin, []string{"BRIDGE_TOKEN=x", "CIRAK_TOKEN=y"}, "config", "check", "--config", filepath.Join(dir, "none")); code != 2 || !strings.Contains(out, "BRIDGE_TOKEN and CIRAK_TOKEN") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, code = run(t, bin, nil, "config"); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
