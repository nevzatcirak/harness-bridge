package claudecode

import (
	"io/fs"
	"syscall"
)

// OSFS is the production FS.
type OSFS struct{}

// Lstat reports mode, owner and link count without following symlinks.
func (OSFS) Lstat(p string) (FileInfo, error) {
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		return FileInfo{}, &fs.PathError{Op: "lstat", Path: p, Err: err}
	}
	return FileInfo{Mode: modeFromStat(st.Mode), UID: st.Uid, Nlink: uint64(st.Nlink)}, nil
}

func modeFromStat(m uint32) fs.FileMode {
	out := fs.FileMode(m & 0o777)
	switch m & syscall.S_IFMT {
	case syscall.S_IFDIR:
		out |= fs.ModeDir
	case syscall.S_IFLNK:
		out |= fs.ModeSymlink
	case syscall.S_IFREG:
	default:
		out |= fs.ModeIrregular
	}
	if m&syscall.S_ISUID != 0 {
		out |= fs.ModeSetuid
	}
	if m&syscall.S_ISGID != 0 {
		out |= fs.ModeSetgid
	}
	if m&syscall.S_ISVTX != 0 {
		out |= fs.ModeSticky
	}
	return out
}
