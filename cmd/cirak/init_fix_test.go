package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review #529 (R528-FIX-1): P2-1 the OpenCode URL, P2-2 symlinks in the staged flow, P3-1 the staged token
// path, P3-2 a clean check, P3-3 rolling back a replaced file.

func stagedIH(t *testing.T) *ih {
	t.Helper()
	old := rootOwner
	rootOwner = func(fs.FileInfo) (uint32, bool) { return 0, true }
	t.Cleanup(func() { rootOwner = old })
	h := newIH(t)
	h.svcUID = os.Geteuid() + 4242
	return h
}

func TestInitValidatesTheOpenCodeURLBeforeAnything(t *testing.T) {
	bad := []string{
		"http://user:CANARYPW@127.0.0.1:4096",
		"http://CANARYONLYUSER@127.0.0.1:4096",
		"http://example.com:4096",
		"http://127.0.0.1:4096/CANARYPATH",
		"http://127.0.0.1:4096/?token=CANARYQ",
		"http://127.0.0.1:4096/#CANARYFRAG",
		"ftp://127.0.0.1:4096",
		"http://u:CANARYBAD@[::1",
		"127.0.0.1:4096",
		"CANARYGARBAGE",
	}
	for _, flow := range []string{"same-user", "staged"} {
		for _, url := range bad {
			t.Run(flow+"/"+url, func(t *testing.T) {
				var h *ih
				var args []string
				if flow == "staged" {
					h = stagedIH(t)
					args = h.yesArgs("--service-user", "cirak", "--opencode-url", url)
				} else {
					h = newIH(t)
					args = h.yesArgs("--opencode-url", url)
				}
				before := listTree(t, h.base)
				if code := h.init(args...); code != 2 {
					t.Fatalf("exit %d, want a usage error\n%s", code, h.all())
				}
				if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") || h.stage != "" {
					t.Error("something was written or staged")
				}
				if strings.Contains(h.all(), "CANARY") {
					t.Errorf("a credential reached the output:\n%s", h.all())
				}
				if !strings.Contains(h.errb.String(), "not a valid OpenCode base URL") || len(h.probeCalls) != 0 {
					t.Errorf("expected a refusal before any probe (probes %d): %q", len(h.probeCalls), h.errb.String())
				}
			})
		}
	}
	// the diagnostic shows the URL only in the redacted form
	h := newIH(t)
	h.init(h.yesArgs("--opencode-url", "http://user:CANARYPW@127.0.0.1:4096")...)
	if !strings.Contains(h.errb.String(), "(http://***@127.0.0.1:4096)") {
		t.Errorf("the redacted form is not shown: %q", h.errb.String())
	}
	// the valid loopback forms still work, and what is written is what was asked
	for _, url := range []string{"http://127.0.0.1:4096", "http://127.0.0.1:4096/", "http://localhost:4096", "https://127.0.0.1:4096", "http://[::1]:4096"} {
		h := newIH(t)
		if code := h.init(h.yesArgs("--opencode-url", url)...); code != 0 {
			t.Errorf("%s: exit %d\n%s", url, code, h.all())
		}
	}
}

func TestInitValidatesAnInteractiveOpenCodeURLToo(t *testing.T) {
	h := newIH(t)
	h.tty = true
	ws := h.dir("ws", 0o755)
	h.stdin = strings.Join([]string{"", "http://user:CANARYPW@127.0.0.1:4096", ws, "", "ses_one", "", ""}, "\n") + "\n"
	before := listTree(t, h.base)
	if code := h.init("--config", h.cfgPath(), "--token-file", filepath.Join(h.base, "cfg", "token")); code != 2 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	if strings.Contains(h.all(), "CANARYPW") || strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") {
		t.Fatalf("a credential leaked or something was written:\n%s", h.all())
	}
}

func TestInitStagedRefusesSymlinksInTheTargets(t *testing.T) {
	mk := func(t *testing.T) (*ih, string, string) {
		h := stagedIH(t)
		real, other := h.dir("real", 0o700), h.dir("other", 0o700)
		link := filepath.Join(h.base, "linkdir")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		return h, link, other
	}
	check := func(t *testing.T, h *ih, code int) {
		t.Helper()
		if code != 1 || h.stage != "" || strings.Contains(h.out.String(), "install ") {
			t.Fatalf("exit %d, staged %q:\n%s", code, h.stage, h.all())
		}
		if !strings.Contains(h.errb.String(), "symbolic link") || !strings.Contains(h.errb.String(), "no command was printed") {
			t.Errorf("the refusal does not say why: %q", h.errb.String())
		}
	}
	t.Run("config dir is a symlink, token elsewhere", func(t *testing.T) {
		h, link, other := mk(t)
		code := h.init("--yes", "--root", h.dir("ws", 0o755), "--session", "ses_one", "--service-user", "cirak", "--config", filepath.Join(link, "config.json"), "--token-file", filepath.Join(other, "token"))
		check(t, h, code)
	})
	t.Run("config file is a dangling symlink", func(t *testing.T) {
		h := stagedIH(t)
		d := h.dir("cfgd", 0o700)
		if err := os.Symlink(filepath.Join(h.base, "nowhere"), filepath.Join(d, "config.json")); err != nil {
			t.Fatal(err)
		}
		code := h.init("--yes", "--root", h.dir("ws", 0o755), "--session", "ses_one", "--service-user", "cirak", "--config", filepath.Join(d, "config.json"), "--token-file", filepath.Join(d, "token"))
		check(t, h, code)
	})
	t.Run("lifecycle target through a symlink", func(t *testing.T) {
		h, link, _ := mk(t)
		// the lifecycle file lives beside the config, so a symlinked config directory is also its path
		code := h.init("--yes", "--root", h.dir("ws", 0o755), "--session", "ses_one", "--lifecycle-root", h.dir("lc", 0o755), "--service-user", "cirak", "--config", filepath.Join(link, "config.json"), "--token-file", filepath.Join(h.dir("tok", 0o700), "token"))
		check(t, h, code)
	})
	t.Run("a symlink in the missing tail's existing prefix", func(t *testing.T) {
		h, link, other := mk(t)
		code := h.init("--yes", "--root", h.dir("ws", 0o755), "--session", "ses_one", "--service-user", "cirak", "--config", filepath.Join(link, "sub", "deeper", "config.json"), "--token-file", filepath.Join(other, "token"))
		check(t, h, code)
	})
	t.Run("a missing tail under a real directory is fine", func(t *testing.T) {
		h := stagedIH(t)
		d := h.dir("real", 0o700)
		code := h.init("--yes", "--root", h.dir("ws", 0o755), "--session", "ses_one", "--service-user", "cirak", "--config", filepath.Join(d, "new", "config.json"), "--token-file", filepath.Join(d, "new", "token"))
		if code != 0 || !strings.Contains(h.out.String(), "install -d -o cirak -g cirak -m 0700 "+filepath.Join(d, "new")) {
			t.Fatalf("exit %d\n%s", code, h.all())
		}
	})
}

func TestInitStagedConnectorPointsAtTheStagedToken(t *testing.T) {
	h := stagedIH(t)
	args := h.yesArgs("--service-user", "cirak")
	if code := h.init(args...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	final := filepath.Join(h.base, "cfg", "token")
	if strings.Contains(h.out.String(), "read it from "+final) {
		t.Errorf("the connector text points at the final path that does not exist yet:\n%s", h.out.String())
	}
	if !strings.Contains(h.out.String(), "Token: staged at "+filepath.Join(h.stage, "token")+", and put at "+final+" by the install command above") {
		t.Errorf("the staged token path is not named:\n%s", h.out.String())
	}
	if strings.Contains(h.all(), h.expectedToken()) {
		t.Error("--yes printed the token")
	}
	// an existing token file is only referenced: its own path is right
	h = stagedIH(t)
	d := h.dir("cfg", 0o700)
	writeFile(t, filepath.Join(d, "token"), strings.Repeat("e", 60)+"\n", 0o600)
	if code := h.init(h.yesArgs("--service-user", "cirak")...); code != 0 || !strings.Contains(h.out.String(), "Token: read it from "+filepath.Join(d, "token")) {
		t.Fatalf("exit %d\n%s", code, h.out.String())
	}
}

func TestInitFailsWhenTheGeneratedConfigDoesNotCheckClean(t *testing.T) {
	old := generatedConfigCheck
	defer func() { generatedConfigCheck = old }()
	for name, emit := range map[string]func(*report){
		"a warning":     func(r *report) { r.warn("synthetic warning") },
		"an unverified": func(r *report) { r.unverified("synthetic") },
		"a refusal":     func(r *report) { r.refuse("synthetic refusal") },
		"clean":         func(r *report) { r.ok("synthetic ok") },
	} {
		t.Run(name, func(t *testing.T) {
			generatedConfigCheck = func(rep *report, f serveFlags, g func(string) string, e []string, uid int, p bool) {
				emit(rep)
			}
			h := newIH(t)
			code := h.init(h.yesArgs()...)
			if name == "clean" {
				if code != 0 {
					t.Fatalf("exit %d\n%s", code, h.all())
				}
				return
			}
			if code != 1 || !strings.Contains(h.errb.String(), "does not check clean") {
				t.Fatalf("exit %d: %q", code, h.errb.String())
			}
		})
	}
}

// A replaced lifecycle file goes back to its old bytes when the later config replacement fails (the backup stays).
func TestInitRestoresAReplacedFileWhenALaterStepFails(t *testing.T) {
	h := newIH(t)
	d := h.dir("cfg", 0o700)
	lc := filepath.Join(d, "lifecycle.json")
	oldLC := `{"roots":[]}` + "\n"
	writeFile(t, lc, oldLC, 0o600)
	// the config path is a directory: it "exists", --force reaches its replacement, which fails
	if err := os.Mkdir(filepath.Join(d, "config.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	code := h.init(h.yesArgs("--lifecycle-root", h.dir("lcroot", 0o755), "--force")...)
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	if readF(t, lc) != oldLC || mode(t, lc) != 0o600 {
		t.Fatalf("the lifecycle file was not restored: %q", readF(t, lc))
	}
	if exists(filepath.Join(d, "token")) {
		t.Error("the new token file was left behind")
	}
	ents, _ := os.ReadDir(d)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover %s", e.Name())
		}
	}
	// the backup of the replaced file is kept
	var backups int
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "lifecycle.json.bak-") {
			backups++
			if readF(t, filepath.Join(d, e.Name())) != oldLC {
				t.Error("the backup does not hold the old bytes")
			}
		}
	}
	if backups != 1 {
		t.Errorf("%d backups", backups)
	}
	if strings.Contains(h.all(), h.expectedToken()) {
		t.Error("the token leaked")
	}
}

// The OpenCode URL is always shown through the shared redaction form (scheme and host only), never as typed.
func TestInitShowsTheOpenCodeURLInTheRedactedForm(t *testing.T) {
	h := newIH(t)
	h.probes = []openCodeProbe{{}}
	if code := h.init(h.yesArgs("--opencode-url", "http://127.0.0.1:4096/")...); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	if !strings.Contains(h.out.String(), "not reachable at http://127.0.0.1:4096. Check") {
		t.Errorf("the URL is not shown in the redacted form:\n%s", h.out.String())
	}
}
