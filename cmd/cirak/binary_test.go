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
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	c := exec.Command(bin)
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
