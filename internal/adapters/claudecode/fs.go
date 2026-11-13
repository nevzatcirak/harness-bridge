package claudecode

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// FileInfo is the subset of lstat the validators need.
type FileInfo struct {
	Mode  fs.FileMode // type and permission bits (setuid/sticky as fs.Mode flags)
	UID   uint32
	Nlink uint64
}

// FS is the lstat seam; production uses OSFS, tests use a fake.
type FS interface {
	Lstat(path string) (FileInfo, error)
}

func (i FileInfo) isDir() bool     { return i.Mode&fs.ModeDir != 0 }
func (i FileInfo) isSymlink() bool { return i.Mode&fs.ModeSymlink != 0 }
func (i FileInfo) isRegular() bool { return i.Mode.IsRegular() }

// ancestors lists the cleaned ancestors of p from "/" down to its parent.
func ancestors(p string) []string {
	var out []string
	for d := path.Dir(p); ; d = path.Dir(d) {
		out = append([]string{d}, out...)
		if d == "/" {
			return out
		}
	}
}

// ValidateWrapper applies ADR-021 §1: an absolute, clean path to a regular file
// that is not a symlink, owned by root, without group or other write, not set-id
// and not hard-linked, in directories (every ancestor) that are real directories
// owned by root without group or other write.
func ValidateWrapper(fsys FS, p string) error {
	if err := cleanAbs(p); err != nil || p == "/" || strings.ContainsAny(p, " \t\n\r") {
		return errors.New("claudecode: wrapper path must be absolute, clean and a single word")
	}
	info, err := fsys.Lstat(p)
	if err != nil {
		return fmt.Errorf("claudecode: wrapper: %w", err)
	}
	switch {
	case info.isSymlink():
		return errors.New("claudecode: wrapper is a symlink")
	case !info.isRegular():
		return errors.New("claudecode: wrapper is not a regular file")
	case info.UID != 0:
		return errors.New("claudecode: wrapper is not owned by root")
	case info.Mode.Perm()&0o022 != 0:
		return errors.New("claudecode: wrapper is group or other writable")
	case info.Mode&(fs.ModeSetuid|fs.ModeSetgid) != 0:
		return errors.New("claudecode: wrapper is set-id")
	case info.Nlink > 1:
		return errors.New("claudecode: wrapper has more than one link")
	}
	for _, d := range ancestors(p) {
		di, err := fsys.Lstat(d)
		if err != nil {
			return fmt.Errorf("claudecode: wrapper directory: %w", err)
		}
		switch {
		case di.isSymlink() || !di.isDir():
			return errors.New("claudecode: wrapper path has a non-directory or symlink component")
		case di.UID != 0:
			return errors.New("claudecode: wrapper directory is not owned by root")
		case di.Mode.Perm()&0o022 != 0:
			return errors.New("claudecode: wrapper directory is group or other writable")
		}
	}
	return nil
}

// ValidateHistoryDir applies ADR-021 §5: an absolute clean directory, not a
// symlink, owned by the service user, mode exactly 0700, whose ancestors are
// directories owned by root or the service user that others cannot write to
// (a sticky root-owned directory such as /tmp is accepted).
func ValidateHistoryDir(fsys FS, p string, serviceUID uint32) error {
	if err := cleanAbs(p); err != nil || p == "/" {
		return errors.New("claudecode: history dir must be absolute and clean")
	}
	info, err := fsys.Lstat(p)
	if err != nil {
		return fmt.Errorf("claudecode: history dir: %w", err)
	}
	switch {
	case info.isSymlink():
		return errors.New("claudecode: history dir is a symlink")
	case !info.isDir():
		return errors.New("claudecode: history dir is not a directory")
	case info.UID != serviceUID:
		return errors.New("claudecode: history dir is not owned by the service user")
	case info.Mode.Perm() != 0o700:
		return errors.New("claudecode: history dir mode must be exactly 0700")
	}
	for _, d := range ancestors(p) {
		di, err := fsys.Lstat(d)
		if err != nil {
			return fmt.Errorf("claudecode: history dir ancestor: %w", err)
		}
		if di.isSymlink() || !di.isDir() {
			return errors.New("claudecode: history dir ancestor is a symlink or not a directory")
		}
		if di.UID != 0 && di.UID != serviceUID {
			return errors.New("claudecode: history dir ancestor has a foreign owner")
		}
		if di.Mode.Perm()&0o022 != 0 && !(di.Mode&fs.ModeSticky != 0 && di.UID == 0) {
			return errors.New("claudecode: history dir ancestor is writable by others")
		}
	}
	return nil
}
