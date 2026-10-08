package sessions

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// darwin's background band for one thread (setpriority(2)): lowest CPU
// priority and throttled disk I/O, the policy macOS uses for Spotlight
// and Time Machine.
const (
	prioDarwinThread = 3
	prioDarwinBG     = 0x1000
)

// lockThread pins the calling goroutine to its thread (so the band set
// for it stays with it) until the returned function runs, which also
// leaves the band.
func lockThread() func() {
	runtime.LockOSThread()
	return func() {
		unix.Setpriority(prioDarwinThread, 0, 0)
		runtime.UnlockOSThread()
	}
}

// setBackground moves the (locked) calling thread into or out of the
// background band.
func setBackground(on bool) {
	prio := 0
	if on {
		prio = prioDarwinBG
	}
	unix.Setpriority(prioDarwinThread, 0, prio)
}

// background: the calling goroutine's thread in the background band
// until the returned function runs.
func background() func() {
	done := lockThread()
	setBackground(true)
	return done
}
