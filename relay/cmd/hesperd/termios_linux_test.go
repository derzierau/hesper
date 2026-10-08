//go:build linux

package main

import "golang.org/x/sys/unix"

// ioctlGetTermios is the request that reads a terminal's modes.
const ioctlGetTermios = unix.TCGETS
