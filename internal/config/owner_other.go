//go:build !unix

package config

import "io/fs"

func ownerOf(fs.FileInfo) (uint32, bool) { return 0, false }
