package main

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// systemdCredential permits systemd's root-owned, ACL-granted 0440 files only
// on its read-only memory filesystem. An environment path alone is not trust.
func systemdCredential(env func(string) string, path string, file *os.File) bool {
	dir := env("CREDENTIALS_DIRECTORY")
	if dir == "" || filepath.Clean(dir) != dir || filepath.Dir(dir) != "/run/credentials" || filepath.Dir(path) != dir || filepath.Clean(path) != path {
		return false
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	var ds, fs unix.Stat_t
	var mount unix.Statfs_t
	if unix.Fstat(fd, &ds) != nil || unix.Fstat(int(file.Fd()), &fs) != nil || unix.Fstatfs(fd, &mount) != nil {
		return false
	}
	if ds.Uid != 0 || fs.Uid != 0 || ds.Gid != 0 || fs.Gid != 0 || ds.Mode&07777 != 0550 || fs.Mode&07777 != 0440 || ds.Dev != fs.Dev || mount.Flags&unix.ST_RDONLY == 0 || (mount.Type != unix.TMPFS_MAGIC && mount.Type != unix.RAMFS_MAGIC) {
		return false
	}
	// Bind the descriptor to the direct entry in the verified directory.
	var entry unix.Stat_t
	if unix.Fstatat(fd, filepath.Base(path), &entry, unix.AT_SYMLINK_NOFOLLOW) != nil || entry.Dev != fs.Dev || entry.Ino != fs.Ino {
		return false
	}
	return credentialACL(fd, uint32(os.Geteuid()), 5) && credentialACL(int(file.Fd()), uint32(os.Geteuid()), 4)
}

func credentialACL(fd int, uid uint32, permission uint16) bool {
	var buf [256]byte
	n, err := unix.Fgetxattr(fd, "system.posix_acl_access", buf[:])
	if err != nil {
		return false
	}
	return validCredentialACL(buf[:n], uid, permission)
}

func validCredentialACL(b []byte, uid uint32, permission uint16) bool {
	if len(b) != 44 || binary.LittleEndian.Uint32(b[:4]) != 2 {
		return false
	}
	tags := []uint16{1, 2, 4, 16, 32} // owner, named user, owning group, mask, other
	perms := []uint16{permission, permission, 0, permission, 0}
	for i, tag := range tags {
		entry := b[4+i*8 : 12+i*8]
		id := uint32(0xffffffff)
		if tag == 2 {
			id = uid
		}
		if binary.LittleEndian.Uint16(entry) != tag || binary.LittleEndian.Uint16(entry[2:]) != perms[i] || binary.LittleEndian.Uint32(entry[4:]) != id {
			return false
		}
	}
	return true
}

func openSecret(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "credential"), nil
}
