//go:build linux

package dashboard

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func get(t *testing.T, c *http.Client, url string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func shutdown(t *testing.T, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestServesOnA0700UnixSocket(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "dash.sock")
	srv, err := Open(Config{Socket: sock}, NullSource{}, os.Geteuid())
	if err != nil || srv == nil {
		t.Fatalf("Open: %v", err)
	}
	defer shutdown(t, srv)
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket must exist with mode 0600: %v %v", fi, err)
	}
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, body := get(t, c, "http://dashboard/")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Security-Policy") != wantCSP {
		t.Fatalf("GET / over the socket: %d, CSP %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
	if !strings.Contains(body, "read-only") {
		t.Errorf("body: %s", body)
	}
}

func TestSocketDirectoryMustBe0700(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	srv, err := Open(Config{Socket: filepath.Join(dir, "dash.sock")}, nil, os.Geteuid())
	if err == nil || srv != nil {
		t.Fatalf("a 0755 socket directory must be refused: srv=%v err=%v", srv, err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "dash.sock")); serr == nil {
		t.Fatal("no socket may be created in a refused directory")
	}
}

func TestSocketPathRules(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Relative and unclean paths.
	for _, p := range []string{"dash.sock", "./dash.sock", dir + "/../" + filepath.Base(dir) + "/d.sock", dir + "//d.sock"} {
		if srv, err := Open(Config{Socket: p}, nil, os.Geteuid()); err == nil || srv != nil {
			t.Errorf("path %q must be refused", p)
		}
	}
	// A symlinked parent directory is refused (O_NOFOLLOW).
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if srv, err := Open(Config{Socket: filepath.Join(link, "d.sock")}, nil, os.Geteuid()); err == nil || srv != nil {
		t.Error("a symlinked socket directory must be refused")
	}
	// A directory owned by another uid is refused.
	if srv, err := Open(Config{Socket: filepath.Join(dir, "d.sock")}, nil, os.Geteuid()+12345); err == nil || srv != nil {
		t.Error("a directory not owned by the service user must be refused")
	}
	// A regular file at the socket path is never replaced.
	file := filepath.Join(dir, "regular")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if srv, err := Open(Config{Socket: file}, nil, os.Geteuid()); err == nil || srv != nil {
		t.Error("a non-socket file at the path must be refused")
	}
	if b, _ := os.ReadFile(file); string(b) != "x" {
		t.Error("the refused file was modified")
	}
}

func TestStaleSocketIsReplaced(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "dash.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	// Leave a stale socket file behind: stop listening without unlinking.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if fi, err := os.Lstat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("no stale socket to replace: %v", err)
	}
	srv, err := Open(Config{Socket: sock}, nil, os.Geteuid())
	if err != nil {
		t.Fatalf("a stale socket owned by the service user must be replaced: %v", err)
	}
	shutdown(t, srv)
}

func TestServesOnLoopbackTCPWithHostCheck(t *testing.T) {
	srv, err := Open(Config{Addr: "127.0.0.1:0"}, NullSource{}, os.Geteuid())
	if err != nil || srv == nil {
		t.Fatalf("Open: %v", err)
	}
	defer shutdown(t, srv)
	addr := srv.Addr().String()
	if host, _, _ := net.SplitHostPort(addr); host != "127.0.0.1" {
		t.Fatalf("bound to %s, want loopback", addr)
	}
	// One connection per request, so Shutdown has no idle or unused connection
	// to wait for.
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	if resp, _ := get(t, c, "http://"+addr+"/"); resp.StatusCode != 200 {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", "http://"+addr+"/", nil)
	req.Host = "evil.example"
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Host evil.example = %d, want 400", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", "http://"+addr+"/", nil)
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST / = %d, want 405", resp.StatusCode)
	}
}

func TestShutdownClosesTheListener(t *testing.T) {
	srv, err := Open(Config{Addr: "127.0.0.1:0"}, nil, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	addr := srv.Addr().String()
	shutdown(t, srv)
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		t.Fatal("listener still accepting after Shutdown")
	}
	_ = syscall.Getpid
}
