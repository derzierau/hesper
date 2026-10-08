//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package main

import "golang.org/x/sys/unix"

// ioctlGetTermios is the request that reads a terminal's modes.
const ioctlGetTermios = unix.TIOCGETA
