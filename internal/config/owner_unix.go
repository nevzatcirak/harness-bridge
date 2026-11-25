//go:build unix

package config

import (
	"io/fs"
	"syscall"
)

// ownerOf returns the owning user of a file; ok is false where the platform does not expose it, which the
// callers treat as a refusal.
func ownerOf(info fs.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return st.Uid, true
}
