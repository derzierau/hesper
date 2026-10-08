package agents

import (
	"net"
	"sync/atomic"
)

var peerCheck atomic.Pointer[func(net.Conn) (int, error)]

func init() {
	f := kernelPeerUID
	peerCheck.Store(&f)
}

// peerUID is the uid of a socket peer (replaceable in tests).
func peerUID(c net.Conn) (int, error) { return (*peerCheck.Load())(c) }
