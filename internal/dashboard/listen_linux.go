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
// a parent directory owned by uid with mode exactly 0700; a stale socket
// unlinked only if it is a socket owned by uid, anything else at path refuses
// startup; the socket itself is 0600.
//
// Scope of "no symlink following": the parent directory is opened with
// O_NOFOLLOW, which refuses a symlink only as the FINAL component. Ancestor
// components of path are followed, as for the admin socket; an ancestor that
// another user can replace is the same-UID/root residual of the ADR-020 trust
// boundary (ADR-023 limitations).
//
// TOCTOU: the checks above and the bind are by path, so a swap of the parent
// directory in between is possible for the same UID or root. After the bind the
// directory is opened again and its device, inode, owner and mode must equal the
// ones checked before, and the socket must be a socket owned by uid; otherwise
// the listener is closed and startup is refused. This detects a swap that
// happened before the re-check; it does not close the window after it, which
// stays the stated same-UID/root residual.
func listenUnix(path string, uid int) (net.Listener, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("BRIDGE_DASHBOARD_SOCKET must be a clean absolute path")
	}
	dir := filepath.Dir(path)
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open dashboard socket directory: %w", err)
	}
	var st, dirBefore syscall.Stat_t
	err = syscall.Fstat(fd, &dirBefore)
	_ = syscall.Close(fd)
	if err != nil {
		return nil, err
	}
	if dirBefore.Uid != uint32(uid) || dirBefore.Mode&0o7777 != 0o700 {
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
	if hook := postBindHook; hook != nil {
		hook()
	}
	if err = recheckAfterBind(dir, path, uid, &dirBefore); err != nil {
		// Do not unlink by path: after a swap the path may name something else.
		ln.(*net.UnixListener).SetUnlinkOnClose(false)
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// postBindHook is a test seam, called between the bind and the identity
// re-check. It is nil in production. It is package-level because the window it
// opens is inside listenUnix; a test that sets it must restore it.
var postBindHook func()

// setPostBindHookForTesting installs hook and returns the function that
// restores the previous value.
func setPostBindHookForTesting(hook func()) (restore func()) {
	prev := postBindHook
	postBindHook = hook
	return func() { postBindHook = prev }
}

// recheckAfterBind opens the parent directory again and requires the same
// device, inode, owner and mode as before the bind, and the socket at path to be
// a socket owned by uid.
func recheckAfterBind(dir, path string, uid int, before *syscall.Stat_t) error {
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("dashboard socket directory changed after the bind: %w", err)
	}
	var after syscall.Stat_t
	err = syscall.Fstat(fd, &after)
	_ = syscall.Close(fd)
	if err != nil {
		return err
	}
	if after.Dev != before.Dev || after.Ino != before.Ino || after.Uid != uint32(uid) || after.Mode&0o7777 != 0o700 {
		return fmt.Errorf("dashboard socket directory changed between the check and the bind: refusing to serve")
	}
	var sock syscall.Stat_t
	if err = syscall.Lstat(path, &sock); err != nil || sock.Mode&syscall.S_IFMT != syscall.S_IFSOCK || sock.Uid != uint32(uid) {
		return fmt.Errorf("dashboard socket at %s changed after the bind: refusing to serve", path)
	}
	return nil
}
