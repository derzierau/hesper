package sessions

import "time"

// throttle paces one indexing worker so the indexer never competes with
// agents. Transcripts of the last two weeks (read first) are read at
// normal priority with a duty cycle — after every slice of work (a file,
// or 4 MiB of one) the worker rests as long as it worked, so at most
// half a core each — and are searchable within seconds. Everything
// older, and everything while one of hesperd's agents is working, is
// read in macOS's background band (lowest CPU priority, throttled disk
// I/O: the kernel gives it only what nobody else wants), resting three
// times as long as it worked while agents work. Foreground (tests) does
// none of it.
type throttle struct {
	s      *Service
	bg     bool
	recent bool
	since  time.Time
}

// recentWindow: transcripts written within it are indexed first and at
// normal priority.
const recentWindow = 14 * 24 * time.Hour

func newThrottle(s *Service) *throttle { return &throttle{s: s, since: time.Now()} }

// begin starts a file.
func (th *throttle) begin(f fileInfo) {
	if th == nil || th.s.opt.Foreground {
		return
	}
	th.recent = th.s.opt.Now().Sub(time.Unix(0, f.mtime)) < recentWindow
	th.band(th.s.busy())
	th.since = time.Now()
}

func (th *throttle) band(busy bool) {
	bg := busy || !th.recent
	if bg != th.bg {
		setBackground(bg)
		th.bg = bg
	}
}

// slice ends a slice of work: band and rest.
func (th *throttle) slice() {
	if th == nil || th.s.opt.Foreground {
		return
	}
	busy := th.s.busy()
	th.band(busy)
	var rest time.Duration
	switch {
	case busy:
		rest = 3 * time.Since(th.since)
	case !th.bg:
		rest = time.Since(th.since)
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
