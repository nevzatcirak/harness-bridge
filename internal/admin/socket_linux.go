//go:build linux

package admin

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// Listen opens the admin socket at path. The parent directory must be owned by
// uid with mode exactly 0700. A stale socket is unlinked only if it is a socket
// owned by uid; anything else at path refuses startup. The socket is 0600.
func Listen(path string, uid int) (net.Listener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("BRIDGE_ADMIN_SOCKET must be a clean absolute path")
	}
	dir := filepath.Dir(path)
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open admin socket directory: %w", err)
	}
	var st syscall.Stat_t
	err = syscall.Fstat(fd, &st)
	_ = syscall.Close(fd)
	if err != nil {
		return nil, err
	}
	if st.Uid != uint32(uid) || st.Mode&0o7777 != 0o700 {
		return nil, fmt.Errorf("admin socket directory must have mode 0700 and be owned by the service user")
	}
	switch err = syscall.Lstat(path, &st); {
	case err == nil:
		if st.Mode&syscall.S_IFMT != syscall.S_IFSOCK || st.Uid != uint32(uid) {
			return nil, fmt.Errorf("refusing to replace a non-socket or foreign file at the admin socket path")
		}
		if err = syscall.Unlink(path); err != nil {
			return nil, fmt.Errorf("remove stale admin socket: %w", err)
		}
	case !errors.Is(err, syscall.ENOENT):
		return nil, fmt.Errorf("inspect admin socket path: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on admin socket: %w", err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod admin socket: %w", err)
	}
	return ln, nil
}

// peerUID returns the connecting process's UID from SO_PEERCRED.
func peerUID(conn net.Conn) (int, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Uid), nil
}
