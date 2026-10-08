package agents

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID is the user ID of the process on the other end of a Unix socket.
func kernelPeerUID(conn net.Conn) (int, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return -1, fmt.Errorf("not a Unix socket")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid := -1
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		if cred, credErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED); credErr == nil {
			uid = int(cred.Uid)
		}
	}); err != nil {
		return -1, err
	}
	return uid, credErr
}
