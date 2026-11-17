//go:build unix

package acceptance

import (
	"errors"
	"os"
	"path"

	"golang.org/x/sys/unix"
)

// CreateEvidenceFile creates the evidence file (mode 0644 before the umask) without following a
// symlink in any component of its path: the directory is walked from "/" with
// O_DIRECTORY|O_NOFOLLOW (the same walk as the decoy filesystem, never creating a directory) and
// the file is made with openat(O_CREAT|O_EXCL|O_NOFOLLOW) relative to its parent. The path must be
// absolute and clean; an existing file, a symlink anywhere or a missing directory is refused. The
// driver may run as root, so the evidence path is not trusted to be free of symlinks.
func CreateEvidenceFile(p string) (*os.File, error) { return createEvidenceFile(&OSDecoyFS{}, p) }

// createEvidenceFile is CreateEvidenceFile with the directory walker passed in. Production always
// passes a fresh &OSDecoyFS{}; a test passes one whose chown seam succeeds, so the property "a missing
// directory is never created" is observable for a non-root caller too (a walk that created
// directories would otherwise fail its chown to uid 0 with EPERM and look identical).
func createEvidenceFile(fs *OSDecoyFS, p string) (*os.File, error) {
	if !cleanAbs(p) {
		return nil, errors.New("acceptance: the evidence path must be absolute and clean")
	}
	parent, err := fs.walk(path.Dir(p), false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, path.Base(p), unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path.Base(p)), nil
}
