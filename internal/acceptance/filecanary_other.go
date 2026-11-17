//go:build !unix

package acceptance

import "errors"

const maxCanaryEntries = 256

// FileCanaryScan is unsupported off unix: the scan fails closed (it never observes).
type FileCanaryScan struct{}

// NewFileCanaryScan is refused on this platform.
func NewFileCanaryScan(dir, runHome string) (*FileCanaryScan, error) {
	return nil, errors.New("acceptance: the file-canary scan needs a unix platform (fails closed)")
}

// Count never observes on this platform.
func (f *FileCanaryScan) Count() (int, bool) { return 0, false }
