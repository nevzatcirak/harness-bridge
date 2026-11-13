//go:build linux

package lifecycle

import (
	"io/fs"
	"syscall"
)

// ownerOf returns the owning user ID of a file. ok is false when the platform
// does not expose it, which callers treat as a refusal.
func ownerOf(info fs.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return st.Uid, true
}
