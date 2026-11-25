//go:build !unix

package main

import "io/fs"

func fileOwner(fs.FileInfo) (uint32, bool) { return 0, false }

func canReadExec(string) bool { return false }
