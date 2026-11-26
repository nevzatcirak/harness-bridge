package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ADR-027 S4: the remote/TLS path of `cirak init`. Certificates are generated in a temp directory; nothing
// starts a listener, opens a firewall or edits a proxy, and the check of what was written runs in process.

const originCanary = "ORIGIN-PASSWORD-CANARY-4e3d2c"

// remoteSetup makes a valid certificate and a private key and returns the arguments of a remote init.
func remoteSetup(t *testing.T, h *ih) (cert, key string, args []string) {
	t.Helper()
	h.clock = time.Now // the daemon's own origin/principal/deadline validator reads the real clock
	dir := h.dir("tls", 0o755)
	cert, key = filepath.Join(dir, "bridge.crt"), filepath.Join(dir, "bridge.key")
	writeFile(t, cert, string(selfSignedPEM(t, time.Now().Add(90*24*time.Hour))), 0o644)
	writeFile(t, key, "KEY-CONTENT-CANARY\n", 0o600)
	args = h.yesArgs("--remote", "--tls-cert", cert, "--tls-key", key, "--public-origin", "https://bridge.example", "--principal", "operator")
	return cert, key, args
}

func TestInitRemoteWritesARemoteConfigThatChecksClean(t *testing.T) {
	h := newIH(t)
	cert, key, args := remoteSetup(t, h)
	if code := h.init(args...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	f := parseOut(t, h.cfgPath())
	for path, want := range map[string]string{"server.public_origin": "https://bridge.example", "server.principal": "operator", "server.tls.cert_file": cert, "server.tls.key_file": key, "server.listen": "127.0.0.1:8080"} {
		if got := f.EnvForm(path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if !f.Has("token.expires") || f.EnvForm("limits.token_max_lifetime") != "168h" {
		t.Error("a remote token needs an explicit deadline within its maximum lifetime")
	}
	out := h.out.String()
	for _, want := range []string{"result: clean", "URL:    https://bridge.example/mcp", "both the TLS certificate and key", "never a plaintext fallback", "docs/deploy/Caddyfile.example", "never opens a firewall or edits a proxy"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "http://127.0.0.1:8080/mcp") {
		t.Error("the connector URL is the plain local one")
	}
	if strings.Contains(h.all(), "KEY-CONTENT-CANARY") || strings.Contains(readF(t, h.cfgPath()), "KEY-CONTENT-CANARY") || strings.Contains(h.all(), h.expectedToken()) {
		t.Error("the key content or the token reached the output or the config")
	}
	if len(h.runs) != 0 {
		t.Errorf("init ran %v", h.runs)
	}
}

func TestInitRemoteAcceptsAPrivateListenAddressAndRefusesAPublicOne(t *testing.T) {
	h := newIH(t)
	_, _, args := remoteSetup(t, h)
	if code := h.init(append(args, "--listen", "10.1.2.3:8443")...); code != 0 {
		t.Fatalf("a private address in remote mode: exit %d\n%s", code, h.all())
	}
	h = newIH(t)
	_, _, args = remoteSetup(t, h)
	before := listTree(t, h.base)
	if code := h.init(append(args, "--listen", "8.8.8.8:443")...); code == 0 || !strings.Contains(h.errb.String(), "--listen") {
		t.Fatalf("a public address: exit %d\n%s", code, h.all())
	}
	if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") {
		t.Error("something was written")
	}
	// local mode still refuses a private address and says how to get one
	h = newIH(t)
	if code := h.init(h.yesArgs("--listen", "10.1.2.3:8443")...); code == 0 || !strings.Contains(h.errb.String(), "use --remote for a private address") {
		t.Fatalf("local mode with a private address: exit %d\n%s", code, h.all())
	}
}

func TestInitRemoteRefusalsHappenBeforeAnythingIsProbedOrWritten(t *testing.T) {
	cases := []struct {
		name  string
		extra func(t *testing.T, h *ih, cert, key string) []string // replaces or adds flags
		text  string
	}{
		{"http origin", func(*testing.T, *ih, string, string) []string {
			return []string{"--public-origin", "http://bridge.example"}
		}, "HTTPS origin"},
		{"origin with a path", func(*testing.T, *ih, string, string) []string {
			return []string{"--public-origin", "https://bridge.example/mcp"}
		}, "HTTPS origin"},
		{"origin with credentials", func(*testing.T, *ih, string, string) []string {
			return []string{"--public-origin", "https://user:" + originCanary + "@bridge.example"}
		}, "HTTPS origin"},
		{"origin without a scheme", func(*testing.T, *ih, string, string) []string { return []string{"--public-origin", "bridge.example"} }, "HTTPS origin"},
		{"principal with a space", func(*testing.T, *ih, string, string) []string { return []string{"--principal", "a b"} }, "named principal"},
		{"deadline too far", func(*testing.T, *ih, string, string) []string {
			return []string{"--expires", time.Now().Add(100 * time.Hour * 24).UTC().Format(time.RFC3339)}
		}, "--expires must be in the future and within"},
		{"relative cert", func(*testing.T, *ih, string, string) []string { return []string{"--tls-cert", "bridge.crt"} }, "--tls-cert must be an absolute canonical path"},
		{"missing cert", func(t *testing.T, h *ih, cert, key string) []string { return []string{"--tls-cert", cert + ".gone"} }, "must be an existing regular file"},
		{"cert is not PEM", func(t *testing.T, h *ih, cert, key string) []string {
			writeFile(t, cert, "not a certificate\n", 0o644)
			return nil
		}, "holds no PEM certificate"},
		{"cert expired", func(t *testing.T, h *ih, cert, key string) []string {
			writeFile(t, cert, string(selfSignedPEM(t, time.Now().Add(-time.Hour))), 0o644)
			return nil
		}, "the TLS certificate expired on"},
		{"key is group readable", func(t *testing.T, h *ih, cert, key string) []string {
			if err := os.Chmod(key, 0o640); err != nil {
				t.Fatal(err)
			}
			return nil
		}, "must be private (mode 0600)"},
		{"key is a symlink", func(t *testing.T, h *ih, cert, key string) []string {
			real := key + ".real"
			if err := os.Rename(key, real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, key); err != nil {
				t.Fatal(err)
			}
			return nil
		}, "symbolic link"},
		{"missing key", func(t *testing.T, h *ih, cert, key string) []string { return []string{"--tls-key", key + ".gone"} }, "must be an existing regular file"},
		{"cert under a symlinked directory", func(t *testing.T, h *ih, cert, key string) []string {
			link := filepath.Join(h.base, "tlslink")
			if err := os.Symlink(filepath.Dir(cert), link); err != nil {
				t.Fatal(err)
			}
			return []string{"--tls-cert", filepath.Join(link, "bridge.crt")}
		}, "symbolic link"},
	}
	for i, tc := range cases {
		t.Run("case-"+string(rune('a'+i)), func(t *testing.T) {
			h := newIH(t)
			cert, key, args := remoteSetup(t, h)
			override := tc.extra(t, h, cert, key)
			args = append(args, override...) // the last occurrence of a flag wins
			before := listTree(t, h.base)
			code := h.init(args...)
			if code == 0 || !strings.Contains(h.all(), tc.text) {
				t.Fatalf("%s: exit %d, want a refusal with %q:\n%s", tc.name, code, tc.text, h.all())
			}
			if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") || h.stage != "" {
				t.Errorf("%s: something was written or staged", tc.name)
			}
			if len(h.probeCalls) != 0 || len(h.runs) != 0 {
				t.Errorf("%s: OpenCode was probed or a command run before the refusal", tc.name)
			}
			if strings.Contains(h.all(), originCanary) || strings.Contains(h.all(), "KEY-CONTENT-CANARY") {
				t.Errorf("%s: a credential or key content reached the output", tc.name)
			}
		})
	}
}

func TestInitRemoteNeedsEveryAnswerAndAFlagNeedsRemote(t *testing.T) {
	for _, missing := range []string{"--tls-cert", "--tls-key", "--public-origin", "--principal"} {
		h := newIH(t)
		_, _, args := remoteSetup(t, h)
		for i := range args {
			if args[i] == missing {
				args = append(args[:i], args[i+2:]...)
				break
			}
		}
		if code := h.init(args...); code != 2 || !strings.Contains(h.errb.String(), missing) {
			t.Errorf("without %s: exit %d\n%s", missing, code, h.all())
		}
	}
	// a certificate alone never turns a local setup into a remote one
	for _, flag := range [][]string{{"--tls-cert", "/a"}, {"--tls-key", "/b"}, {"--public-origin", "https://x.example"}, {"--principal", "p"}} {
		h := newIH(t)
		if code := h.init(h.yesArgs(flag...)...); code != 2 || !strings.Contains(h.errb.String(), "need --remote") {
			t.Errorf("%v without --remote: exit %d\n%s", flag, code, h.all())
		}
	}
	h := newIH(t)
	_, _, args := remoteSetup(t, h)
	if code := h.init(append(args, "--local")...); code != 2 {
		t.Errorf("--local with --remote: exit %d", code)
	}
}

// The service user must be able to read the certificate and the key; init prints the fix and applies nothing.
func TestInitRemoteChecksTheServiceUserCanReadTheTLSFiles(t *testing.T) {
	defer rootOwnerForTest(t)()
	h := newIH(t)
	h.svcUID = os.Geteuid() + 4242
	_, key, args := remoteSetup(t, h)
	args = append(args, "--service-user", "cirak")
	before := listTree(t, h.base)
	code := h.init(args...)
	if code == 0 || h.stage != "" || len(h.runs) != 0 {
		t.Fatalf("exit %d, staged %q, ran %v:\n%s", code, h.stage, h.runs, h.all())
	}
	if want := "fix (init never applies it): chown " + qt("cirak:cirak") + " " + qt(key) + " && chmod 0600 " + qt(key); !strings.Contains(h.out.String(), want) {
		t.Errorf("the key fix is not printed:\n%s", h.out.String())
	}
	if strings.Contains(h.all(), "--tls-cert") && strings.Contains(h.out.String(), "setfacl") {
		t.Error("a readable certificate was reported as a problem")
	}
	if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") {
		t.Error("something was written")
	}
}

func TestInitRemoteAsksForItsAnswersOnATerminal(t *testing.T) {
	h := newIH(t)
	cert, key, _ := remoteSetup(t, h)
	h.tty = true
	ws := h.dir("ws", 0o755)
	// listen, OpenCode URL, one root, one session, the four remote answers, no lifecycle root, then spare lines
	h.stdin = strings.Join([]string{"", "", ws, "", "ses_two", "", cert, key, "https://bridge.example", "operator", "", "", ""}, "\n") + "\n"
	cfg := h.cfgPath()
	if code := h.init("--remote", "--config", cfg, "--token-file", filepath.Join(h.base, "cfg", "token")); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	f := parseOut(t, cfg)
	if f.EnvForm("server.public_origin") != "https://bridge.example" || f.EnvForm("server.principal") != "operator" || f.EnvForm("server.tls.key_file") != key {
		t.Fatalf("the answers were not used:\n%s", readF(t, cfg))
	}
	if !strings.Contains(h.out.String(), "Remote mode: the Bridge itself needs a TLS certificate and key") {
		t.Errorf("the remote-mode warning is not shown:\n%s", h.out.String())
	}
}
