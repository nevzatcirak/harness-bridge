package main

import (
	"crypto/rand"
	"io"
	"syscall"
)

func cryptoRandReader() io.Reader { return rand.Reader }

func syscallUmask(m int) int { return syscall.Umask(m) }
