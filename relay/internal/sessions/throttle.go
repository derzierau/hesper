package sessions

import "time"

// throttle paces one indexing worker so the indexer never competes with
// agents: after every slice of work (a file, or 4 MiB of one) the worker
// rests as long as it worked — at most half a core each — and three
// times as long while one of hesperd's agents is working. Transcripts of
// the last two weeks are read first, so they are searchable within
// seconds. Foreground (tests) does none of it.
//
// The pacing is the worker's own, never the kernel's: no thread of
// hesperd goes into macOS's background band (or nice 19). Such a thread
// gets no CPU while the Mac is loaded, and a Go thread cannot be left
// behind like that: the garbage collector stops every thread, and what
// the starved one holds (SQLite's locks, the registry's) stays held.
// That froze hesperd for good with 19 agents working (2026-10-09).
type throttle struct {
	s     *Service
	since time.Time
}

func newThrottle(s *Service) *throttle { return &throttle{s: s, since: time.Now()} }

// begin starts a file.
func (th *throttle) begin() {
	if th == nil || th.s.opt.Foreground {
		return
	}
	th.since = time.Now()
}

// slice ends a slice of work: rest.
func (th *throttle) slice() {
	if th == nil || th.s.opt.Foreground {
		return
	}
	rest := time.Since(th.since)
	if th.s.busy() {
		rest *= 3
	}
	if rest > 250*time.Millisecond {
		rest = 250 * time.Millisecond
	}
	if rest > 0 {
		select {
		case <-th.s.stop:
		case <-time.After(rest):
		}
	}
	th.since = time.Now()
}
