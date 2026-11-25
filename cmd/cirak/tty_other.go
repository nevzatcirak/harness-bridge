//go:build !linux

package main

import (
	"bufio"
	"errors"
)

func stdinIsTerminal() bool { return false }

func stdoutIsTerminal() bool { return false }

func readPasswordNoEcho(*bufio.Reader) (string, error) {
	return "", errors.New("a hidden password prompt is not available on this platform")
}
