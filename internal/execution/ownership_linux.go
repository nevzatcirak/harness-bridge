//go:build linux

package execution

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Ownership holds host-local advisory locks for the entire writer lifetime.
// All writers must use the same directory/inodes; native clients do not participate.
// Never unlink a lock file: replacing its inode defeats exclusion.
type Ownership struct{ files []*os.File }

func AcquireOwnership(directory string, sessions []string) (*Ownership, error) {
	if !filepath.IsAbs(directory) || len(sessions) == 0 {
		return nil, fmt.Errorf("absolute shared writer lock directory and sessions required")
	}
	fd, err := syscall.Open(directory, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open writer lock directory: %w", err)
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err = syscall.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		return nil, fmt.Errorf("writer lock directory must be private and owned by service user")
	}
	owner := &Ownership{}
	fail := func(err error) (*Ownership, error) { owner.Close(); return nil, err }
	seen := map[string]bool{}
	for _, session := range sessions {
		if seen[session] {
			continue
		}
		seen[session] = true
		name := fmt.Sprintf("%x.lock", sha256.Sum256([]byte(session)))
		f, err := syscall.Openat(fd, name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
		if err != nil {
			return fail(fmt.Errorf("open writer session lock: %w", err))
		}
		file := os.NewFile(uintptr(f), name)
		owner.files = append(owner.files, file)
		if err = syscall.Fstat(f, &st); err != nil {
			return fail(err)
		}
		if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 || st.Nlink != 1 {
			return fail(fmt.Errorf("writer lock must be a private owned regular file"))
		}
		if err = syscall.Flock(f, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return fail(fmt.Errorf("writer ownership unavailable: %w", err))
		}
	}
	return owner, nil
}

func (o *Ownership) Close() {
	for _, f := range o.files {
		_ = f.Close()
	}
	o.files = nil
}
