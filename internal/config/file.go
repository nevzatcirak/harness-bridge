package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Load reads and parses the config file at path under the file rules of ADR-027 section 1.4: an absolute
// canonical path, no symlink in any component, a regular file owned by uid (the process user; root when the
// process runs as root), mode exactly 0600, and a parent directory that is not group or world writable. The
// file is opened with O_NOFOLLOW and the descriptor is checked against the inspected entry, so a swap between
// the check and the read is refused. Errors name the path (not a secret) and never a value.
func Load(path string, uid uint32) (*File, error) {
	data, err := ReadChecked(path, uid)
	if err != nil {
		return nil, err
	}
	f, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// ReadChecked applies the file rules and returns the bytes (at most 1 MiB).
func ReadChecked(path string, uid uint32) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("config path %q must be absolute and canonical", path)
	}
	if err := noSymlinkComponent(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, unwrapPathError(err))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config file %s must be a regular file", path)
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("config file %s must have mode 0600", path)
	}
	owner, ok := ownerOf(info)
	if !ok || owner != uid {
		return nil, fmt.Errorf("config file %s must be owned by the process user", path)
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("config directory of %s: %w", path, unwrapPathError(err))
	}
	if parent.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("the directory of config file %s must not be group or world writable", path)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), "config")
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("config file %s changed while it was opened", path)
	}
	if opened.Size() > MaxFileSize {
		return nil, errors.New("config file is larger than 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("config file %s is unreadable", path)
	}
	if len(data) > MaxFileSize {
		return nil, errors.New("config file is larger than 1 MiB")
	}
	return data, nil
}

// NoSymlinkComponent refuses a path with a symbolic link in any component, the last one included. It is the
// rule the file loader and `cirak init` share.
func NoSymlinkComponent(path string) error { return noSymlinkComponent(path) }

// noSymlinkComponent refuses a path with a symbolic link in any component, the last one included.
func noSymlinkComponent(path string) error {
	cur := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, cur), cur) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return fmt.Errorf("config file %s: %w", path, unwrapPathError(err))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("config file %s has a symbolic link in its path", path)
		}
	}
	return nil
}

func unwrapPathError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// Exists reports whether a path is present (a dangling or any other symlink counts, so the file rules
// refuse it instead of the discovery skipping it). An error other than "does not exist" is returned.
func Exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("config file %s: %w", path, unwrapPathError(err))
}
