package claudecode

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// ReadConfigFile opens an operator file without following a symlink, checks what
// it opened on the descriptor (regular, one link, mode exactly 0600, owned by
// serviceUID) and reads at most limit bytes. A larger file is refused, not cut.
func ReadConfigFile(p string, serviceUID uint32, limit int64) ([]byte, error) {
	fd, err := syscall.Open(p, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("claudecode: open config file: %w", err)
	}
	f := os.NewFile(uintptr(fd), p)
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return nil, err
	}
	switch {
	case st.Mode&syscall.S_IFMT != syscall.S_IFREG:
		return nil, errors.New("claudecode: config file is not a regular file")
	case st.Nlink != 1:
		return nil, errors.New("claudecode: config file has more than one link")
	case st.Mode&0o777 != 0o600:
		return nil, errors.New("claudecode: config file mode must be exactly 0600")
	case st.Uid != serviceUID:
		return nil, errors.New("claudecode: config file is not owned by the service user")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("claudecode: config file exceeds its bound")
	}
	return data, nil
}

// LoadSessions reads BRIDGE_CLAUDE_SESSIONS: the path is validated, the file is
// opened and checked on its descriptor, then parsed (ParseSessions).
func LoadSessions(fsys FS, p string, serviceUID uint32, allowedRoots []string) ([]Session, error) {
	if err := ValidateConfigFile(fsys, p, serviceUID); err != nil {
		return nil, err
	}
	data, err := ReadConfigFile(p, serviceUID, maxSessionsFile)
	if err != nil {
		return nil, err
	}
	return ParseSessions(data, allowedRoots)
}
