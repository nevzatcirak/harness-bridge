package claudecode

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// openChecked opens a history segment without following a symlink and verifies
// what it opened: a regular file, one link, mode exactly 0600, owned by the
// service user. The checks run on the descriptor, not on the path.
func openChecked(path string, flag int) (*os.File, error) {
	fd, err := syscall.Open(path, flag|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("claudecode: open history segment: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		f.Close()
		return nil, err
	}
	switch {
	case st.Mode&syscall.S_IFMT != syscall.S_IFREG:
		f.Close()
		return nil, errors.New("claudecode: history segment is not a regular file")
	case st.Nlink != 1:
		f.Close()
		return nil, errors.New("claudecode: history segment has more than one link")
	case st.Mode&0o777 != 0o600:
		f.Close()
		return nil, errors.New("claudecode: history segment mode must be 0600")
	case st.Uid != uint32(os.Geteuid()):
		f.Close()
		return nil, errors.New("claudecode: history segment is not owned by the service user")
	}
	return f, nil
}

// appendLine writes one record with a single write call (O_APPEND).
func appendLine(path string, line []byte) error {
	f, err := openChecked(path, syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT)
	if err != nil {
		return err
	}
	n, err := f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	return err
}

// readFile reads a whole segment, refusing one larger than limit.
func readFile(path string, limit int64) ([]byte, error) {
	f, err := openChecked(path, syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("claudecode: history segment exceeds its bound")
	}
	return data, nil
}
