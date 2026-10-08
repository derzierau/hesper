//go:build !darwin && !linux

package sessions

func lockThread() func()    { return func() {} }
func setBackground(on bool) {}
func background() func()    { return func() {} }
