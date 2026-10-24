//go:build !linux

package main

import "os"

func systemdCredential(func(string) string, string, *os.File) bool { return false }

func openSecret(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrPermission
	}
	return os.Open(path)
}
