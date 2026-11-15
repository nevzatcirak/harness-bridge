package dashboard

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// §7 row 5 (code part): the server timeouts and the header bound are set on the real listener
// of both transports. A zero value would leave the server open to a slow client.
func TestServerTimeoutsAreSetOnBothTransports(t *testing.T) {
	check := func(name string, s *Server) {
		t.Helper()
		h := s.srv
		if h.ReadHeaderTimeout <= 0 || h.ReadHeaderTimeout > 10*time.Second || h.ReadTimeout <= 0 || h.ReadTimeout > 30*time.Second ||
			h.WriteTimeout <= 0 || h.WriteTimeout > 60*time.Second || h.IdleTimeout <= 0 || h.IdleTimeout > 2*time.Minute ||
			h.MaxHeaderBytes <= 0 || h.MaxHeaderBytes > 64<<10 {
			t.Errorf("%s: timeouts %v/%v/%v/%v header bytes %d", name, h.ReadHeaderTimeout, h.ReadTimeout, h.WriteTimeout, h.IdleTimeout, h.MaxHeaderBytes)
		}
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	u, err := Open(Config{Socket: filepath.Join(dir, "d.sock")}, NullSource{}, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	defer u.Shutdown(context.Background())
	check("unix socket", u)
	tcp, err := Open(Config{Addr: "127.0.0.1:0"}, NullSource{}, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Shutdown(context.Background())
	check("tcp", tcp)
}
