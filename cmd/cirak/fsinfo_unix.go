//go:build unix

package main

import (
	"io/fs"
	"syscall"

	"golang.org/x/sys/unix"
)

func fileOwner(info fs.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return st.Uid, true
}

// canReadExec reports whether the effective user can read and traverse a directory (access(2) with
// AT_EACCESS, so it is the service user that is asked, not the real uid of a setuid wrapper).
func canReadExec(path string) bool {
	return unix.Faccessat(unix.AT_FDCWD, path, unix.R_OK|unix.X_OK, unix.AT_EACCESS) == nil
}
