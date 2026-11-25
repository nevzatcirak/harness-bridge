package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Reviews #535 and #536 (R528-FIX-2): the token only on a terminal stdout, quoted root commands, unsafe paths,
// the pinned guards, and the staged symlink coverage.

// ---- #536 P2-1: the token is never printed when stdout is not a terminal ----

func TestInitDoesNotPrintTheTokenWhenStdoutIsRedirected(t *testing.T) {
	mk := func(redirected bool) (*ih, int) {
		h := newIH(t)
		h.tty, h.stdoutRedirected = true, redirected
		ws := h.dir("ws", 0o755)
		h.stdin = strings.Join([]string{"", "", ws, "", "ses_two", "", ""}, "\n") + "\n"
		return h, h.init("--config", h.cfgPath(), "--token-file", filepath.Join(h.base, "cfg", "token"))
	}
	h, code := mk(true)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	tok := h.expectedToken()
	if strings.Contains(h.all(), tok) {
		t.Fatalf("the token reached a redirected stdout:\n%s", h.out.String())
	}
	if !strings.Contains(h.out.String(), "not printed: standard output is not a terminal") || strings.TrimSpace(readF(t, filepath.Join(h.base, "cfg", "token"))) != tok {
		t.Errorf("the path/note or the token file is wrong:\n%s", h.out.String())
	}
	// the same run with a terminal stdout still shows it exactly once
	h, code = mk(false)
	if code != 0 || strings.Count(h.all(), tok) != 1 {
		t.Fatalf("exit %d, token shown %d times", code, strings.Count(h.all(), tok))
	}
	// and in the staged flow
	old := rootOwnerForTest(t)
	defer old()
	h = newIH(t)
	h.svcUID = os.Geteuid() + 4242
	h.tty, h.stdoutRedirected = true, true
	ws := h.dir("ws", 0o755)
	h.stdin = strings.Join([]string{"", "", ws, "", "ses_two", "", ""}, "\n") + "\n"
	if code := h.init("--config", h.cfgPath(), "--token-file", filepath.Join(h.base, "cfg", "token"), "--service-user", "cirak"); code != 0 {
		t.Fatalf("staged: exit %d\n%s", code, h.all())
	}
	if strings.Contains(h.all(), tok) || !strings.Contains(h.out.String(), "not printed: standard output is not a terminal") {
		t.Fatalf("staged flow printed the token to a redirected stdout:\n%s", h.out.String())
	}
}

func rootOwnerForTest(t *testing.T) func() {
	t.Helper()
	old := rootOwner
	rootOwner = func(fi os.FileInfo) (uint32, bool) { return 0, true }
	return func() { rootOwner = old }
}

// ---- #536 P2-2: every printed root command is quoted ----

func TestShqRoundTripsThroughAShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh: the quoting round trip did not run (a skip is not a pass)")
	}
	for _, v := range []string{"plain", "my dir", "a;b", "it's", "'", "''", `a\b`, "$(echo PWNED)", "`echo PWNED`", "a && echo PWNED", "a#b", "*", "~", "-n", "é ç", "", "a'b'c", "tab\there", "$HOME"} {
		out, err := exec.Command(sh, "-c", "printf %s "+shq(v)).Output()
		if err != nil || string(out) != v {
			t.Errorf("%q: the shell read back %q (%v)", v, out, err)
		}
	}
}

func TestInitPrintedCommandsAreQuoted(t *testing.T) {
	hostile := []string{"/nonexistent; echo PWNED #", "/no such dir/ws", "/tmp/it's here", "/x$(echo PWNED)y", "/a`echo PWNED`"}
	for _, root := range hostile {
		t.Run("fix "+root, func(t *testing.T) {
			h := newIH(t)
			a := h.yesArgs()
			a[2] = root
			if code := h.init(a...); code != 1 {
				t.Fatalf("exit %d\n%s", code, h.all())
			}
			want := "fix (init never applies it): chown 'root:operators' " + qt(root) + " && chmod 0710 " + qt(root) + " && setfacl -m 'u:operator:rwx' " + qt(root)
			if !strings.Contains(h.out.String(), want) {
				t.Errorf("want %q in:\n%s", want, h.out.String())
			}
		})
	}
	t.Run("staged install commands", func(t *testing.T) {
		defer rootOwnerForTest(t)()
		h := newIH(t)
		h.svcUID = os.Geteuid() + 4242
		d := filepath.Join(h.base, "my dir;x'y")
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg, tok := filepath.Join(d, "config.json"), filepath.Join(d, "tok en")
		if code := h.init("--yes", "--root", h.dir("ws", 0o755), "--session", "ses_one", "--service-user", "cirak", "--config", cfg, "--token-file", tok); code != 0 {
			t.Fatalf("exit %d\n%s", code, h.all())
		}
		for _, want := range []string{
			"install -d -o 'cirak' -g 'cirak' -m 0700 " + qt(d),
			"install -o 'cirak' -g 'cirak' -m 0600 " + qt(filepath.Join(h.stage, "config.json")) + " " + qt(cfg),
			"install -o 'cirak' -g 'cirak' -m 0600 " + qt(filepath.Join(h.stage, "token")) + " " + qt(tok),
			"rm -r " + qt(h.stage),
		} {
			if !strings.Contains(h.out.String(), want) {
				t.Errorf("missing %q in:\n%s", want, h.out.String())
			}
		}
		for _, line := range strings.Split(h.out.String(), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "install ") && strings.Contains(line, "my dir;x'y") && !strings.Contains(line, `'\''`) {
				t.Errorf("an unquoted hostile path in %q", line)
			}
		}
	})
	t.Run("sudo probe display", func(t *testing.T) {
		defer rootOwnerForTest(t)()
		h := newIH(t)
		h.svcUID = os.Geteuid() + 4242
		h.tty = true
		ws := filepath.Join(h.base, "my ws")
		if err := os.Mkdir(ws, 0o700); err != nil {
			t.Fatal(err)
		}
		h.stdin = strings.Join([]string{"", "", ws, "", "ses_one", "", "", "n"}, "\n") + "\n"
		h.init("--config", h.cfgPath(), "--token-file", filepath.Join(h.base, "cfg", "token"), "--service-user", "cirak")
		if !strings.Contains(h.out.String(), "sudo -u 'cirak' test -r "+qt(ws)) || !strings.Contains(h.out.String(), "sudo -u 'cirak' test -x "+qt(ws)) {
			t.Errorf("the probe is not shown quoted:\n%s", h.out.String())
		}
	})
}

func TestInitRefusesPathsWithNewlineOrNUL(t *testing.T) {
	for _, bad := range []string{"/tmp/a\nb", "/tmp/a\rb", "/tmp/a\x00b"} {
		for _, field := range []string{"root", "config", "token", "lifecycle-root"} {
			t.Run(field, func(t *testing.T) {
				h := newIH(t)
				a := h.yesArgs()
				switch field {
				case "root":
					a[2] = bad
				case "config":
					a[len(a)-1] = bad
				case "token":
					a[6] = bad
				case "lifecycle-root":
					a = append(a, "--lifecycle-root", bad)
				}
				before := listTree(t, h.base)
				code := h.init(a...)
				if code != 2 || !strings.Contains(h.errb.String(), "newline") || strings.Contains(h.out.String(), "install") || strings.Contains(h.out.String(), "fix (") {
					t.Fatalf("%q in %s: exit %d\n%s", bad, field, code, h.all())
				}
				if strings.Join(before, "\n") != strings.Join(listTree(t, h.base), "\n") || h.stage != "" {
					t.Error("something was written or staged")
				}
				if len(h.probeCalls) != 0 {
					t.Error("OpenCode was probed before the refusal")
				}
			})
		}
	}
}

// ---- #536 P3-1: the sudo probe is never run with --yes, even on a terminal ----

func TestInitYesNeverProbesEvenOnATerminal(t *testing.T) {
	defer rootOwnerForTest(t)()
	h := newIH(t)
	h.svcUID = os.Geteuid() + 4242
	h.tty = true
	h.stdin = "y\ny\ny\ny\n"
	ws := h.dir("ws", 0o700)
	code := h.init("--yes", "--root", ws, "--session", "ses_one", "--token-file", filepath.Join(h.base, "cfg", "token"), "--config", h.cfgPath(), "--service-user", "cirak")
	if code != 1 || len(h.runs) != 0 || strings.Contains(h.out.String(), "Run the probe?") {
		t.Fatalf("exit %d, runs %v:\n%s", code, h.runs, h.out.String())
	}
}

// ---- #536 P3-2: the no-chown pin covers methods and sibling functions ----

func TestInitHasNoOwnershipOrModeChangeCalls(t *testing.T) {
	for _, file := range []string{"init.go", "initfs.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			chmods := 0
			ast.Inspect(fd, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				name := sel.Sel.Name
				switch {
				case strings.HasPrefix(name, "Chown") || strings.HasPrefix(name, "Lchown") || strings.HasPrefix(name, "Fchown"):
					t.Errorf("%s: %s uses %s (init never changes an owner)", file, fd.Name.Name, name)
				case strings.HasPrefix(name, "Chmod") || strings.HasPrefix(name, "Fchmod") || strings.HasPrefix(name, "Lchmod"):
					chmods++
				}
				return true
			})
			switch {
			case chmods == 0:
			case file == "initfs.go" && fd.Name.Name == "createExclusive" && chmods == 1:
				// the one sanctioned call: f.Chmod(mode.Perm()) on the descriptor of a file this call just created
			default:
				t.Errorf("%s: %s has %d mode-change call(s); only the descriptor Chmod in createExclusive is allowed", file, fd.Name.Name, chmods)
			}
		}
	}
}

// ---- #535 N1 and #536 P3-4: every staged target is checked, the lifecycle sibling and the lock directory too ----

func TestInitStagedChecksTheLifecycleSiblingAndTheLockDirectory(t *testing.T) {
	refused := func(t *testing.T, h *ih, code int) {
		t.Helper()
		if code != 1 || h.stage != "" || strings.Contains(h.out.String(), "install ") || !strings.Contains(h.errb.String(), "symbolic link") {
			t.Fatalf("exit %d, staged %q:\n%s", code, h.stage, h.all())
		}
	}
	t.Run("lifecycle.json is a symlink, config.json missing", func(t *testing.T) {
		defer rootOwnerForTest(t)()
		h := newIH(t)
		h.svcUID = os.Geteuid() + 4242
		d := h.dir("cfgd", 0o700)
		victim := filepath.Join(h.dir("victim", 0o700), "v")
		writeFile(t, victim, "VICTIM", 0o600)
		if err := os.Symlink(victim, filepath.Join(d, "lifecycle.json")); err != nil {
			t.Fatal(err)
		}
		code := h.init("--yes", "--root", h.dir("ws", 0o755), "--session", "ses_one", "--lifecycle-root", h.dir("lc", 0o755), "--service-user", "cirak", "--config", filepath.Join(d, "config.json"), "--token-file", filepath.Join(h.dir("tok", 0o700), "token"))
		refused(t, h, code)
		if readF(t, victim) != "VICTIM" {
			t.Error("the victim was touched")
		}
	})
	t.Run("the lock directory path goes through a symlink", func(t *testing.T) {
		defer rootOwnerForTest(t)()
		oldLock := serviceLockDir
		defer func() { serviceLockDir = oldLock }()
		h := newIH(t)
		h.svcUID = os.Geteuid() + 4242
		real := h.dir("realstate", 0o700)
		link := filepath.Join(h.base, "statelink")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		serviceLockDir = filepath.Join(link, "cirak", "writer-locks")
		code := h.init(h.yesArgs("--service-user", "cirak")...)
		refused(t, h, code)
	})
}

// ---- documentation points the reviews asked for (#535 N2, N3; #536 P3-3) ----

func TestContractsStatesTheInitBehavioursTheReviewsAskedFor(t *testing.T) {
	b, err := os.ReadFile("../../docs/CONTRACTS.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, want := range []string{
		"A failed check keeps the files init wrote",
		"empty environment",
		"printed without a confirmation prompt",
		"standard output must both be terminals",
		"single-quoted",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("CONTRACTS.md does not say %q", want)
		}
	}
}
