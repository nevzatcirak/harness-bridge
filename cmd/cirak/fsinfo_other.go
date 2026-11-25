//go:build !unix

package main

import "io/fs"

func fileOwner(fs.FileInfo) (uint32, bool) { return 0, false }

func canReadExec(string) bool { return false }

func canWriteDir(string) bool { return false }

func fileGroup(fs.FileInfo) (uint32, bool) { return 0, false }
