//go:build !linux

package lifecycle

import (
	"errors"
	"os"
)

var errUnreadable = errors.New("lifecycle: metadata file unreadable")

// No weaker fallback on platforms without the verified non-blocking, no-follow
// read: every metadata read is refused, so lifecycle authorization fails closed.
func readRegular(*os.Root, string, int) ([]byte, os.FileInfo, error) { return nil, nil, errUnreadable }
