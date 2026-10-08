//go:build !darwin && !linux

package controlsock

import (
	"net"
	"os"
)

// peerUID cannot ask the kernel here; the 0600 socket in a 0700 directory
// keeps other users out.
func peerUID(net.Conn) (int, error) { return os.Getuid(), nil }
