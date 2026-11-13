//go:build !linux

package lifecycle

import "io/fs"

// ownerOf cannot determine ownership on this platform; every ownership check
// then refuses (fail closed).
func ownerOf(fs.FileInfo) (uint32, bool) { return 0, false }
