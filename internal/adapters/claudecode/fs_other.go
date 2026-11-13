//go:build !linux

package claudecode

// OSFS is the production FS on linux. Elsewhere it refuses every lookup, so the
// wrapper and directory validators fail closed.
type OSFS struct{}

// Lstat always fails off linux.
func (OSFS) Lstat(string) (FileInfo, error) { return FileInfo{}, errUnsupportedPlatform }
