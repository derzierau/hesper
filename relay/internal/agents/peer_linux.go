package agents

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// peerUID is the user ID of the process on the other end of a Unix socket.
func kernelPeerUID(conn net.Conn) (int, error) {
	cred, err := peerCred(conn)
	if err != nil {
		return -1, err
	}
	return int(cred.Uid), nil
}

// kernelPeerPID is the process ID of the other end of a Unix socket
// (SO_PEERCRED; agent tree).
func kernelPeerPID(conn net.Conn) (int, error) {
	cred, err := peerCred(conn)
	if err != nil {
		return -1, err
	}
	return int(cred.Pid), nil
}

func peerCred(conn net.Conn) (*unix.Ucred, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("not a Unix socket")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return cred, credErr
}

// parentPID is a process's parent (/proc/PID/stat: after the command in
// parentheses come the state and the parent).
func parentPID(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1, err
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return -1, fmt.Errorf("bad /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 2 {
		return -1, fmt.Errorf("bad /proc/%d/stat", pid)
	}
	return strconv.Atoi(fields[1])
}
