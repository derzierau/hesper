package host

import (
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// dropWindow is how far back hesperctl status counts dropped sessions.
const dropWindow = 5 * time.Minute

// dropTracker remembers the relay sessions that ended without the host
// stopping: how many in the last dropWindow and why the last one did. A
// host that flaps says so in hesperctl status instead of only in the log.
type dropTracker struct {
	mu         sync.Mutex
	times      []time.Time // within dropWindow of the newest, oldest first
	lastAt     time.Time
	lastReason string
}

func (d *dropTracker) record(at time.Time, reason string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.times = append(d.within(at), at)
	d.lastAt, d.lastReason = at, reason
}

// within: the drop times inside the window ending at now (d.mu held).
func (d *dropTracker) within(now time.Time) []time.Time {
	i := 0
	for i < len(d.times) && now.Sub(d.times[i]) >= dropWindow {
		i++
	}
	return d.times[i:]
}

// session is what hello tells; nil before the first drop.
func (d *dropTracker) session(now time.Time) *wire.RelaySession {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastAt.IsZero() {
		return nil
	}
	return &wire.RelaySession{Drops5m: len(d.within(now)), LastReason: d.lastReason, LastAt: d.lastAt}
}

// RelaySession reports the host's dropped relay sessions (hello's
// relaySession); nil when none has dropped since hesperd started.
func (r *Runner) RelaySession() *wire.RelaySession {
	return r.drops.session(time.Now())
}
