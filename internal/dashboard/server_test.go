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

// Shutdown must close the listener deterministically, even when it runs before
// the Serve goroutine has registered the listener with the http.Server (review
// #194 P2-1). No sleeps: many Open+Shutdown pairs, each followed by a dial.
func TestShutdownImmediatelyAfterOpenClosesTheListener(t *testing.T) {
	for i := 0; i < 300; i++ {
		srv, err := Open(Config{Addr: "127.0.0.1:0"}, nil, os.Geteuid())
		if err != nil {
			t.Fatal(err)
		}
		addr := srv.Addr().String()
		shutdown(t, srv)
		if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			conn.Close()
			t.Fatalf("iteration %d: listener still accepting after Shutdown returned", i)
		}
	}
}

func TestShutdownImmediatelyAfterOpenRemovesTheUnixSocket(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "dash.sock")
	for i := 0; i < 100; i++ {
		srv, err := Open(Config{Socket: sock}, nil, os.Geteuid())
		if err != nil {
			t.Fatal(err)
		}
		shutdown(t, srv)
		if conn, err := net.DialTimeout("unix", sock, time.Second); err == nil {
			conn.Close()
			t.Fatalf("iteration %d: unix socket still accepting after Shutdown returned", i)
		}
	}
}

// Shutdown is idempotent: a second call neither errors nor blocks.
func TestShutdownTwiceIsHarmless(t *testing.T) {
	srv, err := Open(Config{Addr: "127.0.0.1:0"}, nil, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	shutdown(t, srv)
	shutdown(t, srv)
}

// Scope of the symlink rule (review #194 P3-2): the FINAL directory component is
// opened with O_NOFOLLOW and refused if it is a symlink. Ancestor components of
// the path are followed, like the admin socket does; an ancestor that another
// user can replace is the same-UID/root residual of the ADR-020 trust boundary.
// This test states that scope so it cannot be read as stronger than it is.
func TestSymlinkRuleCoversOnlyTheFinalDirectoryComponent(t *testing.T) {
	real := t.TempDir()
	if err := os.Chmod(real, 0o700); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(real, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// Final component is a symlink: refused.
	if srv, err := Open(Config{Socket: filepath.Join(link, "d.sock")}, nil, os.Geteuid()); err == nil || srv != nil {
		t.Fatal("a symlink as the final directory component must be refused")
	}
	// An ancestor is a symlink: followed (documented residual, not a guarantee).
	srv, err := Open(Config{Socket: filepath.Join(link, "sub", "d.sock")}, nil, os.Geteuid())
	if err != nil {
		t.Fatalf("an ancestor symlink is followed by design (final component %s is a real 0700 directory): %v", sub, err)
	}
	shutdown(t, srv)
}

// The directory identity is re-checked after the bind (review #194 P3-3). A
// swap of the parent directory between the pre-bind check and the bind is
// detected: the listener is closed, nothing is served, and the error says so.
func TestDirectorySwappedAfterBindIsDetected(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "run")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "dash.sock")
	restore := setPostBindHookForTesting(func() {
		// Replace the directory by another 0700 directory at the same path.
		if err := os.Rename(dir, filepath.Join(base, "moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	})
	defer restore()
	srv, err := Open(Config{Socket: sock}, nil, os.Geteuid())
	if err == nil || srv != nil {
		t.Fatalf("a directory swapped after the bind must be refused: srv=%v err=%v", srv, err)
	}
	if !strings.Contains(err.Error(), "changed") {
		t.Errorf("error should say the directory changed: %v", err)
	}
	if conn, derr := net.DialTimeout("unix", sock, time.Second); derr == nil {
		conn.Close()
		t.Fatal("nothing may listen at the swapped path")
	}
}

// Without a swap the post-bind re-check passes (guards against a re-check that
// refuses everything).
func TestPostBindRecheckPassesWhenNothingChanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	called := false
	restore := setPostBindHookForTesting(func() { called = true })
	defer restore()
	srv, err := Open(Config{Socket: filepath.Join(dir, "d.sock")}, nil, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown(t, srv)
	if !called {
		t.Fatal("the post-bind hook was not reached: the re-check is not between bind and use")
	}
}

// The device and inode comparison matters on its own: if the swapped-in
// directory also holds a socket of the service user at the same path, the
// socket check alone passes, and only the directory identity refuses.
func TestDirectorySwappedWithAForgedSocketIsDetected(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "run")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "dash.sock")
	var forged net.Listener
	restore := setPostBindHookForTesting(func() {
		if err := os.Rename(dir, filepath.Join(base, "moved")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		var err error
		if forged, err = net.Listen("unix", sock); err != nil {
			t.Fatal(err)
		}
	})
	defer restore()
	defer func() {
		if forged != nil {
			forged.Close()
		}
	}()
	srv, err := Open(Config{Socket: sock}, nil, os.Geteuid())
	if err == nil || srv != nil {
		t.Fatalf("a swapped directory holding a forged socket must be refused: srv=%v err=%v", srv, err)
	}
	if !strings.Contains(err.Error(), "changed") {
		t.Errorf("error should say the directory changed: %v", err)
	}
}
