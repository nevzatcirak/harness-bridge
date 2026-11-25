package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Carried findings of the S2 reviews, fixed in S3: #540 P3-1 (quote the non-root printed commands), #540 P3-2
// (a control character in the staging directory), #536 P3-5 (one reader for standard input).

func TestInitQuotesTheOperatorCommandsToo(t *testing.T) {
	defer rootOwnerForTest(t)()
	h := newIH(t)
	h.svcUID = os.Geteuid() + 4242
	d := filepath.Join(h.base, "it's;$(touch HIT)")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(d, "config.json")
	if code := h.init("--yes", "--root", h.dir("ws", 0o755), "--session", "ses_one", "--service-user", "cirak", "--config", cfg, "--token-file", filepath.Join(d, "token")); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	for _, want := range []string{
		"The files are staged in " + qt(h.stage) + " (private, mode 0700)",
		"Then: cirak config check --config " + qt(cfg),
	} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, h.out.String())
		}
	}
	// the same-user flow prints the next steps quoted as well
	h2 := newIH(t)
	d2 := filepath.Join(h2.base, "o'neil;$(touch HIT2)")
	if err := os.Mkdir(d2, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg2 := filepath.Join(d2, "config.json")
	if code := h2.init("--yes", "--root", h2.dir("ws", 0o755), "--session", "ses_one", "--config", cfg2, "--token-file", filepath.Join(d2, "token")); code != 0 {
		t.Fatalf("exit %d\n%s", code, h2.all())
	}
	for _, want := range []string{"1. cirak config check --config " + qt(cfg2), "2. start it: cirak --config " + qt(cfg2)} {
		if !strings.Contains(h2.out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, h2.out.String())
		}
	}
	for _, line := range strings.Split(h.out.String()+h2.out.String(), "\n") {
		if (strings.Contains(line, "$(touch HIT") || strings.Contains(line, "$(touch HIT2")) && !strings.Contains(line, "'") {
			t.Errorf("a command line with an unquoted hostile path: %q", line)
		}
	}
}

func TestInitRefusesAStagingDirectoryWithAControlCharacter(t *testing.T) {
	defer rootOwnerForTest(t)()
	h := newIH(t)
	h.svcUID = os.Geteuid() + 4242
	bad := filepath.Join(h.base, "tmp\nINJECTED")
	d := h.deps()
	d.stageDir = func() (string, error) { return bad, os.Mkdir(bad, 0o700) }
	args := h.yesArgs("--service-user", "cirak")
	if code := runInit(args, d); code != 2 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	if !strings.Contains(h.errb.String(), "staging directory") || strings.Contains(h.out.String(), "install ") || strings.Contains(h.out.String(), "INJECTED") {
		t.Errorf("a command or the forged line was printed:\n%s", h.all())
	}
	if exists(bad) {
		t.Error("the empty staging directory was left behind")
	}
}

// The password prompt reads through the wizard's own reader, so input the wizard already buffered is not lost.
func TestInitPasswordIsReadThroughTheWizardsReader(t *testing.T) {
	h := newIH(t)
	h.tty = true
	h.probes = []openCodeProbe{{Unauthorized: true}, {Reachable: true}}
	ws := h.dir("ws", 0o755)
	// every answer, the username and the password included, is on standard input in one piece
	h.stdin = strings.Join([]string{"", "", ws, "", "ses_two", "", "", "ocuser", "PIPED-PW-CANARY"}, "\n") + "\n"
	d := h.deps()
	var gotSame bool
	d.readPassword = func(r *bufio.Reader) (string, error) {
		line, err := r.ReadString('\n')
		gotSame = err == nil
		return strings.TrimRight(line, "\n"), err
	}
	if code := runInit([]string{"--config", h.cfgPath(), "--token-file", filepath.Join(h.base, "cfg", "token")}, d); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.all())
	}
	if !gotSame || len(h.probeCalls) != 2 || h.probeCalls[1].password != "PIPED-PW-CANARY" {
		t.Fatalf("the password was not read from the wizard's reader: %+v", h.probeCalls)
	}
	if strings.Contains(h.all(), "PIPED-PW-CANARY") {
		t.Fatal("the password reached the output")
	}
	// production wires the same reader: readPasswordNoEcho takes the wizard's *bufio.Reader (not os.Stdin)
	src := readF(t, "tty_linux.go")
	if strings.Contains(src, "bufio.NewReader(os.Stdin)") || !strings.Contains(src, "r.ReadString") {
		t.Error("readPasswordNoEcho builds a second reader on standard input")
	}
}
