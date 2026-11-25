//go:build !linux

package main

import "errors"

func stdinIsTerminal() bool { return false }

func stdoutIsTerminal() bool { return false }

func readPasswordNoEcho() (string, error) {
	return "", errors.New("a hidden password prompt is not available on this platform")
}
