//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// openPTY returns a pseudo-terminal pair: the master (kept open so the slave stays a terminal) and the slave.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: the terminal test did not run (a skip is not a pass): %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		t.Skipf("cannot unlock the pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		t.Skipf("cannot name the pty: %v", err)
	}
	s, err := os.OpenFile("/dev/pts/"+itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Skipf("cannot open the pty slave: %v", err)
	}
	t.Cleanup(func() { m.Close(); s.Close() })
	return m, s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// #536 P2-1 as the real process: standard input is a terminal, standard output is a file. The token is written
// to its 0600 file and its path is printed, but the 64-character value is never in the redirected output.
func TestBinaryInitDoesNotPrintTheTokenToARedirectedStdout(t *testing.T) {
	bin := builtBinary(t)
	_, slave := openPTY(t)
	base := privDir(t)
	ws := filepath.Join(base, "ws")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(base, "cfg", "token")
	outPath := filepath.Join(base, "out.log")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	c := exec.Command(bin, "init", "--root", ws, "--session", "ses_one", "--listen", "127.0.0.1:18081", "--opencode-url", "http://127.0.0.1:1",
		"--lifecycle-root", ws, "--config", filepath.Join(base, "cfg", "config.json"), "--token-file", tokenPath)
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + base, "XDG_STATE_HOME=" + filepath.Join(base, "state")}
	c.Stdin, c.Stdout, c.Stderr = slave, out, out
	if err := c.Run(); err != nil {
		b, _ := os.ReadFile(outPath)
		t.Fatalf("init: %v\n%s", err, b)
	}
	tok := strings.TrimSpace(readF(t, tokenPath))
	log := readF(t, outPath)
	if len(tok) != 64 {
		t.Fatalf("token length %d", len(tok))
	}
	if strings.Contains(log, tok) {
		t.Fatalf("the token is in the redirected stdout file:\n%s", log)
	}
	if !strings.Contains(log, "not printed: standard output is not a terminal") || !strings.Contains(log, tokenPath) {
		t.Fatalf("the path or the note is missing:\n%s", log)
	}
}
