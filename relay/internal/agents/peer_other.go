//go:build !darwin && !linux

package agents

import (
	"errors"
	"net"
	"os"
)

// peerUID cannot ask the kernel here; the 0600 socket in a 0700 directory
// keeps other users out.
func kernelPeerUID(net.Conn) (int, error) { return os.Getuid(), nil }

// kernelPeerPID and parentPID are unknown here: callers are who they say
// (agent tree).
func kernelPeerPID(net.Conn) (int, error) { return -1, errors.ErrUnsupported }
func parentPID(int) (int, error)          { return -1, errors.ErrUnsupported }

// processName is unknown here (a shell's foreground job, shell.go).
func processName(int) string { return "" }
