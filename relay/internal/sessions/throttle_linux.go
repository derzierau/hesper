package sessions

import (
	"runtime"
	"syscall"
)

// lockThread pins the calling goroutine to its thread until the
// returned function runs (which also restores its priority).
func lockThread() func() {
	runtime.LockOSThread()
	return func() {
		syscall.Setpriority(syscall.PRIO_PROCESS, syscall.Gettid(), 0)
		runtime.UnlockOSThread()
	}
}

// setBackground: nice 19 for the (locked) calling thread, or back to 0.
func setBackground(on bool) {
	prio := 0
	if on {
		prio = 19
	}
	syscall.Setpriority(syscall.PRIO_PROCESS, syscall.Gettid(), prio)
}

func background() func() {
	done := lockThread()
	setBackground(true)
	return done
}
