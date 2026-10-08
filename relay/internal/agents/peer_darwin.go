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

// kernelPeerPID is the process ID of the other end of a Unix socket
// (LOCAL_PEERPID; agent tree).
func kernelPeerPID(conn net.Conn) (int, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return -1, fmt.Errorf("not a Unix socket")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return -1, err
	}
	pid := -1
	var pidErr error
	if err := raw.Control(func(fd uintptr) {
		pid, pidErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return -1, err
	}
	return pid, pidErr
}

// parentPID is a process's parent (the kern.proc.pid sysctl).
func parentPID(pid int) (int, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return -1, err
	}
	if int(kp.Proc.P_pid) != pid {
		return -1, fmt.Errorf("no process %d", pid)
	}
	return int(kp.Eproc.Ppid), nil
}

// processName is a process's command name (p_comm; a shell's foreground
// job, shell.go).
func processName(pid int) string {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return ""
	}
	return unix.ByteSliceToString(kp.Proc.P_comm[:])
}
