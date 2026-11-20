package dashboard

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Cleanup sweep C (#434): findings of the reviews of the dashboard listener, #198 (A) and #199 (B).

// #199 P3-1: an IPv4-mapped loopback literal is the same address as 127.0.0.1 to net.IP.Equal, so it used to pass
// ValidateAddr, bind and then answer 400 to every request (the Host allowlist names only 127.0.0.1 and [::1]).
func TestValidateAddrRefusesIPv4MappedLoopbackLiterals(t *testing.T) {
	for _, a := range []string{
		"[::ffff:127.0.0.1]:8080", "[::ffff:7f00:1]:8080", "[0:0:0:0:0:ffff:127.0.0.1]:8080", "[::FFFF:127.0.0.1]:0",
	} {
		if err := ValidateAddr(a); err == nil {
			t.Errorf("ValidateAddr(%q) accepted an IPv4-mapped literal", a)
		}
	}
	for _, a := range []string{"127.0.0.1:0", "127.0.0.1:8080", "[::1]:0", "[0:0:0:0:0:0:0:1]:8080"} {
		if err := ValidateAddr(a); err != nil {
			t.Errorf("ValidateAddr(%q) refused a loopback literal it must accept: %v", a, err)
		}
	}
}

// failingListener is a listener whose Close fails with an error that is not net.ErrClosed.
type failingListener struct{ net.Listener }

func (failingListener) Close() error { return errors.New("close failed: read-only directory") }

// #199 P3-3 / #198 P3-5: when closing the listener fails (for the unix transport, when the socket cannot be
// unlinked), Shutdown must still shut the http.Server down and run the cleanups, and return the error.
func TestShutdownStillShutsTheHTTPServerDownWhenTheListenerCloseFails(t *testing.T) {
	ran := false
	s := &Server{ln: failingListener{}, srv: &http.Server{}}
	s.cleanup = []func(){func() { ran = true }}
	err := s.Shutdown(context.Background())
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("the listener close error was lost: %v", err)
	}
	if !ran {
		t.Error("the cleanups did not run after a failed listener close")
	}
	// The http.Server must be marked shut down: a Serve on it returns ErrServerClosed at once.
	ln, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatal(lerr)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() { done <- s.srv.Serve(ln) }()
	select {
	case serr := <-done:
		if !errors.Is(serr, http.ErrServerClosed) {
			t.Errorf("Serve returned %v, want http.ErrServerClosed", serr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the http.Server was not shut down: Serve is still running")
	}
}

// A second Shutdown stays harmless and returns the same kind of result.
func TestShutdownIsIdempotentAfterAFailedClose(t *testing.T) {
	s := &Server{ln: failingListener{}, srv: &http.Server{}}
	_ = s.Shutdown(context.Background())
	_ = s.Shutdown(context.Background()) // must not panic or hang
}

// #198 P3-2: Cache-Control: no-store is part of the response contract (set before every response, errors too).
func TestEveryResponseCarriesCacheControlNoStore(t *testing.T) {
	h := NewHandler(NullSource{}, DefaultHosts)
	for _, c := range []struct{ method, target, host string }{
		{"GET", "/", "localhost"}, {"HEAD", "/", "localhost"}, {"GET", "/dashboard.css", "localhost"}, {"GET", "/dashboard.js", "localhost"},
		{"GET", "/nope", "localhost"}, {"POST", "/", "localhost"}, {"GET", "/", "evil.example"},
	} {
		rec := serve(h, c.method, c.target, c.host)
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s %s (Host %s) status %d: Cache-Control %q, want no-store", c.method, c.target, c.host, rec.Code, got)
		}
	}
}

// #198 P3-2: the socket directory must be EXACTLY 0700: a check that ignored group or world bits, or accepted a
// stricter-looking but different mode, would pass the single 0755 case.
func TestSocketDirectoryMustBeExactly0700ForEveryOtherMode(t *testing.T) {
	for _, mode := range []fs.FileMode{0o750, 0o770, 0o710, 0o701, 0o702, 0o705, 0o777, 0o500, 0o600, 0o711, 0o740} {
		dir := t.TempDir()
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		sock := filepath.Join(dir, "dash.sock")
		srv, err := Open(Config{Socket: sock}, nil, os.Geteuid())
		if err == nil || srv != nil {
			if srv != nil {
				_ = srv.Shutdown(context.Background())
			}
			t.Errorf("a %#o socket directory was accepted", mode)
		}
		_ = os.Chmod(dir, 0o700) // so that the temp dir can be cleaned up
		if _, serr := os.Stat(sock); serr == nil {
			t.Errorf("a socket was created in a refused %#o directory", mode)
		}
	}
}

// #199 P3-2: the post-bind re-check also requires the object at the socket path to be a socket. The swap tests
// all change the directory's identity too, so they passed on the directory comparison alone; here the directory is
// untouched and only the path is replaced by a regular file. (A socket owned by another user cannot be made as a
// single UID: not testable here.)
func TestPostBindRecheckRefusesANonSocketAtThePathInAnUnchangedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "dash.sock")
	restore := setPostBindHookForTesting(func() {
		if err := os.Remove(sock); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sock, []byte("not a socket"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	defer restore()
	srv, err := Open(Config{Socket: sock}, nil, os.Geteuid())
	if err == nil || srv != nil {
		if srv != nil {
			_ = srv.Shutdown(context.Background())
		}
		t.Fatalf("a regular file at the socket path after the bind must be refused: err=%v", err)
	}
	if !strings.Contains(err.Error(), "changed after the bind") {
		t.Errorf("error should name the socket check: %v", err)
	}
	if b, rerr := os.ReadFile(sock); rerr != nil || string(b) != "not a socket" {
		t.Errorf("the refused file at the path was removed or changed: %q %v", b, rerr)
	}
}

// #198 P3-2: the Serve goroutine hides only the errors of our own Shutdown; a real Serve error is always logged.
func TestServeErrorFilterHidesOnlyOurOwnShutdown(t *testing.T) {
	wrapped := errors.Join(errors.New("accept"), net.ErrClosed)
	for _, c := range []struct {
		name    string
		err     error
		closing bool
		logged  bool
	}{
		{"no error", nil, false, false},
		{"http.ErrServerClosed", http.ErrServerClosed, false, false},
		{"http.ErrServerClosed while closing", http.ErrServerClosed, true, false},
		{"closed listener during our Shutdown", net.ErrClosed, true, false},
		{"a wrapped closed listener during our Shutdown", wrapped, true, false},
		{"closed listener NOT during our Shutdown", net.ErrClosed, false, true},
		{"a real error", errors.New("accept: too many open files"), false, true},
		{"a real error while closing", errors.New("accept: too many open files"), true, true},
	} {
		if got := serveErrorIsLogged(c.err, c.closing); got != c.logged {
			t.Errorf("%s: logged=%v, want %v", c.name, got, c.logged)
		}
	}
}
