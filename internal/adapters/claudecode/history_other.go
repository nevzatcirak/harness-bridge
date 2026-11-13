//go:build !linux

package claudecode

import (
	"errors"
	"os"
)

// The history store relies on openat and descriptor checks that exist on Linux.
// Elsewhere the package builds, and every entry point fails closed.
var errUnsupportedPlatform = errors.New("claudecode: the history store is supported on linux only")

func openDir(string) (*os.File, error)                 { return nil, errUnsupportedPlatform }
func appendLine(*os.File, string, []byte) error        { return errUnsupportedPlatform }
func readFile(*os.File, string, int64) ([]byte, error) { return nil, errUnsupportedPlatform }
