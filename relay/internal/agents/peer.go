package agents

import (
	"net"
	"sync/atomic"
)

var peerCheck atomic.Pointer[func(net.Conn) (int, error)]

// peerPIDCheck finds a socket peer's process (agent tree; replaceable in
// tests).
var peerPIDCheck atomic.Pointer[func(net.Conn) (int, error)]

func init() {
	f := kernelPeerUID
	peerCheck.Store(&f)
	g := kernelPeerPID
	peerPIDCheck.Store(&g)
}

// peerUID is the uid of a socket peer (replaceable in tests).
func peerUID(c net.Conn) (int, error) { return (*peerCheck.Load())(c) }

// peerPID is the process ID of a socket peer.
func peerPID(c net.Conn) (int, error) { return (*peerPIDCheck.Load())(c) }
