package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nevzatcirak/cirak/internal/config"
)

// File helpers of `cirak init` (ADR-027 section 1.4). Every file init creates is created exclusively with
// O_EXCL|O_NOFOLLOW and its mode set at creation, below a parent that is not group or world writable; an
// existing file is never opened for truncation: a replacement is written to a fresh temporary file in the same
// directory and renamed over the old one after a backup. init only ever writes the explicit target paths.

// createExclusive creates path with mode and writes data. The mode is set on the descriptor, so a permissive
// umask cannot widen it. On any failure the file this call created is removed (it is ours: O_EXCL).
func createExclusive(path string, data []byte, mode os.FileMode) error {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return fmt.Errorf("cannot create %s: %w", path, unwrapErr(&os.PathError{Err: err}))
	}
	f := os.NewFile(uintptr(fd), path)
	fail := func(err error) error {
		f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := f.Chmod(mode.Perm()); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	return nil
}

// checkTargetDir applies the rules of a directory init writes into: an existing real directory (no symlink in
// the path), not group or world writable, writable by the invoking user.
func checkTargetDir(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return fmt.Errorf("%s must be an absolute canonical path", dir)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, unwrapErr(err))
	}
	if err := config.NoSymlinkComponent(dir); err != nil {
		return fmt.Errorf("%s has a symbolic link in its path", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s must not be group or world writable", dir)
	}
	if !canWriteDir(dir) {
		return fmt.Errorf("%s is not writable by the invoking user", dir)
	}
	return nil
}

// backupAndReplace replaces an existing regular file: it copies the old bytes to path.bak-<stamp> (mode 0600,
// exclusive), writes the new bytes to a fresh temporary file in the same directory and renames it over path.
// The old file is never opened for truncation. It returns the backup path.
func backupAndReplace(path string, data []byte, mode os.FileMode, stamp string, rand io.Reader) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file: refusing to replace it", path)
	}
	old, err := readNoFollow(path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s for the backup", path)
	}
	backup := path + ".bak-" + stamp
	if err := createExclusive(backup, old, 0o600); err != nil {
		return "", err
	}
	if err := atomicReplace(path, data, mode, rand); err != nil {
		return backup, err
	}
	return backup, nil
}

// atomicReplace writes data to a fresh O_EXCL temporary file in the directory of path and renames it over
// path: the old file is never opened for truncation.
func atomicReplace(path string, data []byte, mode os.FileMode, rand io.Reader) error {
	var nonce [6]byte
	if _, err := io.ReadFull(rand, nonce[:]); err != nil {
		return errors.New("no randomness for a temporary file name")
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.tmp-%x", filepath.Base(path), nonce))
	if err := createExclusive(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cannot replace %s", path)
	}
	return nil
}

func readNoFollow(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, config.MaxFileSize+1))
}

// stamp is the backup suffix: a UTC time that is also a valid file name.
func stamp(now time.Time) string { return now.UTC().Format("20060102T150405Z") }

// noSymlinkInExistingPrefix refuses a symlink anywhere in the part of path that exists (the path itself
// included). A missing tail is fine: it is created by init, below a prefix that was checked.
func noSymlinkInExistingPrefix(path string) error {
	p := path
	for {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
	if err := config.NoSymlinkComponent(p); err != nil {
		return fmt.Errorf("%s has a symbolic link in its path", path)
	}
	return nil
}

// shq quotes one value for a POSIX shell: single quotes around it, an embedded single quote written as '\”.
// Every value init interpolates into a command it PRINTS for root goes through it, so a path with a space or a
// shell metacharacter cannot split the command or run another one.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
