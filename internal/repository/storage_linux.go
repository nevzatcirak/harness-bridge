//go:build linux

package repository

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func readRegular(root *os.Root, path string, limit int) (string, os.FileInfo, error) {
	// Nonblocking avoids FIFO/device substitution stalls; no-follow rejects final
	// symlinks. os.Root confines intermediate resolution during replacement races.
	before, e := root.Lstat(path)
	if e != nil || !before.Mode().IsRegular() || before.Size() > int64(limit) {
		return "", nil, unavailable
	}
	f, e := root.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if e != nil {
		return "", nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || !os.SameFile(before, st) || st.Size() > int64(limit) {
		return "", nil, unavailable
	}
	var raw unix.Stat_t
	if e := unix.Fstat(int(f.Fd()), &raw); e != nil || raw.Nlink != 1 {
		return "", nil, unavailable
	}
	b, e := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if e != nil || len(b) > limit {
		return "", nil, unavailable
	}
	return string(b), st, nil
}
