// Package lockwatch makes a hang of hesperd diagnosable from its log: a
// Mutex remembers who holds it (the caller's program counters, taken on
// Lock: cheap), and a Watchdog logs the holder's stack once when a lock
// is held longer than a limit, and when the process itself did not run
// for that long (a stop-the-world that waited for a thread). The stack
// is only resolved to names when it is logged.
package lockwatch

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// depth bounds the frames a Mutex remembers of its holder.
const depth = 24

// Mutex is a sync.Mutex that remembers its holder for a Watchdog. The
// zero value is an unlocked mutex.
type Mutex struct {
	mu   sync.Mutex
	held atomic.Pointer[holder]
}

type holder struct {
	since    time.Time
	n        int
	pcs      [depth]uintptr
	reported atomic.Bool
}

// Lock locks m and notes the caller as its holder.
func (m *Mutex) Lock() {
	m.mu.Lock()
	m.hold()
}

// TryLock is sync.Mutex's TryLock.
func (m *Mutex) TryLock() bool {
	if !m.mu.TryLock() {
		return false
	}
	m.hold()
	return true
}

func (m *Mutex) hold() {
	h := &holder{since: time.Now()}
	h.n = runtime.Callers(3, h.pcs[:])
	m.held.Store(h)
}

// Unlock unlocks m.
func (m *Mutex) Unlock() {
	m.held.Store(nil)
	m.mu.Unlock()
}

// stack is the holder's stack as a goroutine dump shows one.
func (h *holder) stack() string {
	var b strings.Builder
	frames := runtime.CallersFrames(h.pcs[:h.n])
	for {
		f, more := frames.Next()
		fmt.Fprintf(&b, "\n\t%s\n\t\t%s:%d", f.Function, f.File, f.Line)
		if !more {
			break
		}
	}
	return b.String()
}

// Options configure a Watchdog.
type Options struct {
	// Limit is how long a lock may be held (and the process may not
	// run) before it is logged; 10 s when zero.
	Limit time.Duration
	// Every is how often the locks are looked at; Limit/5 when zero.
	Every time.Duration
	// Stalls also logs a stall of the whole process (one watchdog per
	// process should).
	Stalls bool
	Logf   func(format string, args ...any)
}

// Lock is a Mutex a Watchdog watches and its name for the log.
type Lock struct {
	Name string
	Mu   *Mutex
}

// Watchdog watches named Mutexes.
type Watchdog struct {
	opt   Options
	locks []Lock
	stop  chan struct{}
	done  chan struct{}
}

// Start watches locks until Stop.
func Start(opt Options, locks ...Lock) *Watchdog {
	if opt.Limit <= 0 {
		opt.Limit = 10 * time.Second
	}
	if opt.Every <= 0 {
		opt.Every = opt.Limit / 5
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	w := &Watchdog{opt: opt, locks: locks, stop: make(chan struct{}), done: make(chan struct{})}
	go w.run()
	return w
}

// Stop ends the watchdog.
func (w *Watchdog) Stop() {
	if w == nil {
		return
	}
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	<-w.done
}

func (w *Watchdog) run() {
	defer close(w.done)
	t := time.NewTicker(w.opt.Every)
	defer t.Stop()
	last := time.Now()
	reported := make([]*holder, len(w.locks)) // the hold last logged, per lock
	for {
		select {
		case <-w.stop:
			return
		case now := <-t.C:
			if late := time.Since(last) - w.opt.Every; w.opt.Stalls && late > w.opt.Limit {
				w.opt.Logf("hesperd: watchdog: the process did not run for %v (a stop-the-world waiting for a thread, or the Mac slept)", late.Round(time.Second))
			}
			last = time.Now()
			for i, l := range w.locks {
				h := l.Mu.held.Load()
				if prev := reported[i]; prev != nil && prev != h {
					w.opt.Logf("hesperd: watchdog: %s released (held up to %v)", l.Name, now.Sub(prev.since).Round(time.Millisecond))
					reported[i] = nil
				}
				if h == nil || now.Sub(h.since) < w.opt.Limit || h.reported.Swap(true) {
					continue
				}
				reported[i] = h
				w.opt.Logf("hesperd: watchdog: %s held for %v by:%s", l.Name, now.Sub(h.since).Round(time.Second), h.stack())
			}
		}
	}
}
