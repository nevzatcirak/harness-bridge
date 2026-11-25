//go:build linux

package main

import (
	"bufio"
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// stdinIsTerminal reports whether standard input is a terminal.
func stdinIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}

// readPasswordNoEcho reads one line from the terminal with echo off and restores the terminal. The line is
// never written anywhere by this function.
func readPasswordNoEcho() (string, error) {
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return "", errors.New("standard input is not a terminal")
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &quiet); err != nil {
		return "", errors.New("cannot turn terminal echo off")
	}
	defer func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, old) }()
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
