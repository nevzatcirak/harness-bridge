//go:build linux

package dashboard

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// listenUnix opens the dashboard socket at path, with the same checks as the
// ADR-020 admin socket (internal/admin/socket_linux.go): a clean absolute path;
// a parent directory opened without following symlinks, owned by uid with mode
// exactly 0700; a stale socket unlinked only if it is a socket owned by uid,
// anything else at path refuses startup; the socket itself is 0600.
func listenUnix(path string, uid int) (net.Listener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("BRIDGE_DASHBOARD_SOCKET must be a clean absolute path")
	}
	dir := filepath.Dir(path)
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open dashboard socket directory: %w", err)
	}
	var st syscall.Stat_t
	err = syscall.Fstat(fd, &st)
	_ = syscall.Close(fd)
	if err != nil {
		return nil, err
	}
	if st.Uid != uint32(uid) || st.Mode&0o7777 != 0o700 {
		return nil, fmt.Errorf("dashboard socket directory must have mode 0700 and be owned by the service user")
	}
	switch err = syscall.Lstat(path, &st); {
	case err == nil:
		if st.Mode&syscall.S_IFMT != syscall.S_IFSOCK || st.Uid != uint32(uid) {
			return nil, fmt.Errorf("refusing to replace a non-socket or foreign file at the dashboard socket path")
		}
		if err = syscall.Unlink(path); err != nil {
			return nil, fmt.Errorf("remove stale dashboard socket: %w", err)
		}
	case !errors.Is(err, syscall.ENOENT):
		return nil, fmt.Errorf("inspect dashboard socket path: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on dashboard socket: %w", err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod dashboard socket: %w", err)
	}
	return ln, nil
}
