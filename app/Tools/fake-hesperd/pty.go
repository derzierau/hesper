package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// openPTY opens a pseudo-terminal pair using the macOS ptmx ioctls.
func openPTY() (master *os.File, slaveName string, err error) {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", err
	}
	if err := ioctl(fd, syscall.TIOCPTYGRANT, 0); err != nil {
		syscall.Close(fd)
		return nil, "", fmt.Errorf("grantpt: %w", err)
	}
	if err := ioctl(fd, syscall.TIOCPTYUNLK, 0); err != nil {
		syscall.Close(fd)
		return nil, "", fmt.Errorf("unlockpt: %w", err)
	}
	var name [128]byte
	if err := ioctl(fd, syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); err != nil {
		syscall.Close(fd)
		return nil, "", fmt.Errorf("ptsname: %w", err)
	}
	n := strings.IndexByte(string(name[:]), 0)
	return os.NewFile(uintptr(fd), "ptmx"), string(name[:n]), nil
}

func ioctl(fd int, req uint, arg uintptr) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(req), arg)
	if e != 0 {
		return e
	}
	return nil
}

type winsize struct{ Rows, Cols, X, Y uint16 }

func getWinsize(fd int) (cols, rows int, err error) {
	var ws winsize
	if err := ioctl(fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); err != nil {
		return 0, 0, err
	}
	return int(ws.Cols), int(ws.Rows), nil
}

func setWinsize(fd int, cols, rows int) error {
	ws := winsize{Rows: uint16(rows), Cols: uint16(cols)}
	return ioctl(fd, syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
}

// makeRaw puts a tty into raw mode and returns a restore function.
func makeRaw(fd int) (func(), error) {
	var old syscall.Termios
	if err := ioctl(fd, syscall.TIOCGETA, uintptr(unsafe.Pointer(&old))); err != nil {
		return func() {}, err
	}
	raw := old
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, syscall.TIOCSETA, uintptr(unsafe.Pointer(&raw))); err != nil {
		return func() {}, err
	}
	return func() { _ = ioctl(fd, syscall.TIOCSETA, uintptr(unsafe.Pointer(&old))) }, nil
}
