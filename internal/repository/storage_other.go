//go:build !linux

package repository

import "os"

// No weaker fallback on platforms without the verified nonblocking/no-follow read.
func readRegular(*os.Root, string, int) (string, os.FileInfo, error) { return "", nil, unavailable }
