//go:build linux

package lifecycle

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

var errUnreadable = errors.New("lifecycle: metadata file unreadable")

// readRegular reads one small regular file through a confined root. A final
// symlink, a non-regular file, a hard-linked file or one over limit is refused;
// the open is non-blocking so a FIFO cannot stall it.
func readRegular(root *os.Root, rel string, limit int) ([]byte, os.FileInfo, error) {
	before, err := root.Lstat(rel)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > int64(limit) {
		return nil, nil, errUnreadable
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, errUnreadable
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(before, st) || st.Size() > int64(limit) {
		return nil, nil, errUnreadable
	}
	var raw unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &raw); err != nil || raw.Nlink != 1 {
		return nil, nil, errUnreadable
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(b) > limit {
		return nil, nil, errUnreadable
	}
	return b, st, nil
}
