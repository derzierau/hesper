package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/derzierau/hesper/relay/internal/review"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// review.list's Git work, kept per agent (review.go). A scan (open the
// folder, snapshot it through a copy of the index, diff it against the
// review base) is the expensive part: in a large repository it takes
// seconds, and with 19 agents a review.list that scanned every folder on
// every call ran into its timeout again and again (418 "signal: killed"
// in a day, 2026-10-09). So a scan is reused while its key holds: the
// agent (its hook events, state, review base) and the stat of the files
// Git rewrites when the index or HEAD moves, of the folder and of the
// files the scan found changed — no Git to check it. Past reviewFresh a
// scan is redone anyway (an edit no hook announced, by the user or a
// tool the agent left running). Scans of one agent never overlap
// (callers share the running one), at most reviewScans run at once, a
// settle's scan waits reviewSettleDelay (bursts coalesce), and a scan
// that failed or was killed is retried only after a backoff, with a
// longer timeout. review.diff and the actions still run Git each time:
// they show and change the folder as it is now.

var (
	// reviewScanTimeout bounds a scan; it doubles after a failure (up
	// to four times as long).
	reviewScanTimeout = 3 * time.Minute
	// reviewFresh: a scan whose key holds is reused this long.
	reviewFresh = 2 * time.Minute
	// reviewListWait: review.list waits this long for the scans it
	// needs, then answers with what it has (review.changed follows when
	// a late scan finds something else): within the app's 30 s.
	reviewListWait = 20 * time.Second
	// reviewSettleDelay: a settle's scan waits this long.
	reviewSettleDelay = time.Second
	// reviewBackoff is the wait before retrying a failed scan; it
	// doubles with every failure in a row, up to reviewBackoffMax.
	reviewBackoff    = time.Minute
	reviewBackoffMax = 30 * time.Minute
)

// reviewScans bounds the scans running at once, of all agents.
const reviewScans = 2

// scansRun counts the scans started (tests).
var scansRun atomic.Int64

// scanned, when set, sees each scan's result before it is stored (tests).
var scanned atomic.Pointer[func(*reviewScan)]

// reviewKeyFiles bounds the changed files whose stat is in a key.
const reviewKeyFiles = 256

// reviewCandidate is an agent as a scan sees it: the agent and its hook
// count.
type reviewCandidate struct {
	a     wire.Agent
	hooks uint64
}

// reviewKey says whether a scan is still the folder's.
type reviewKey struct {
	created time.Time
	dir     string
	base    string // Agent.ReviewBase
	since   time.Time
	hooks   uint64
	state   string // the stat of the Git files and of the folder
	changed string // the stat of the changed files
}

// reviewScan is one scan's result.
type reviewScan struct {
	key      reviewKey
	at       time.Time
	top      string
	gitFiles []string // index, HEAD, logs/HEAD
	changed  []string // relative to top
	// item is the Git part of the agent's review.list entry (files,
	// lines, base, risk); nil: no changes, or no Git folder.
	item *wire.ReviewItem
}

type reviewEntry struct {
	scan     *reviewScan
	running  chan struct{} // closed when the running scan ends
	settled  bool          // the running scan follows a settle
	timer    *time.Timer   // a settle's scan, waiting
	failures int
	retryAt  time.Time
	gen      uint64 // bumped by forgetScan
}

type reviewCache struct {
	mu      sync.Mutex
	entries map[string]*reviewEntry // by local id
	sem     chan struct{}
}

func (r *Registry) reviewScans() *reviewCache {
	r.scansOnce.Do(func() {
		r.scans = &reviewCache{entries: map[string]*reviewEntry{}, sem: make(chan struct{}, reviewScans)}
	})
	return r.scans
}

// statOf fingerprints files: mtime and size each ("-" when missing).
func statOf(paths ...string) string {
	var b strings.Builder
	for _, p := range paths {
		if st, err := os.Lstat(p); err == nil {
			fmt.Fprintf(&b, "%d:%d;", st.ModTime().UnixNano(), st.Size())
		} else {
			b.WriteString("-;")
		}
	}
	return b.String()
}

// keyOf is c's key now, with the files sc (a previous scan; nil before
// the first) watches.
func keyOf(c reviewCandidate, sc *reviewScan) reviewKey {
	k := reviewKey{created: c.a.Created, dir: c.a.Dir(), base: c.a.ReviewBase, since: c.a.StateSince, hooks: c.hooks}
	if sc == nil {
		k.state = statOf(k.dir)
		return k
	}
	k.state = statOf(append(append([]string{}, sc.gitFiles...), k.dir, sc.top)...)
	k.changed = sc.changedStat()
	return k
}

func (sc *reviewScan) changedStat() string {
	paths := make([]string, len(sc.changed))
	for i, p := range sc.changed {
		paths[i] = filepath.Join(sc.top, p)
	}
	return statOf(paths...)
}

// reviewCandidates are this Mac's agents ready for review, and the local
// ids of every agent.
func (r *Registry) reviewCandidates() ([]reviewCandidate, map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var list []reviewCandidate
	all := map[string]bool{}
	for local, a := range r.agents {
		all[local] = true
		if checkpointable(a.Kind) && readyForReview(a.State) {
			list = append(list, reviewCandidate{a: a.Agent, hooks: a.hooks})
		}
	}
	return list, all
}

// reviewCandidateOf is one agent as reviewCandidates has it; false when it
// is gone or not ready for review.
func (r *Registry) reviewCandidateOf(local string) (reviewCandidate, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.agents[local]
	if a == nil || r.closing || !checkpointable(a.Kind) || !readyForReview(a.State) {
		return reviewCandidate{}, false
	}
	return reviewCandidate{a: a.Agent, hooks: a.hooks}, true
}

// wantScan is c's scan: nil when the cached one holds (or a failed one
// waits for its backoff), else the channel the scan (running or started
// now) closes.
func (r *Registry) wantScan(c reviewCandidate) chan struct{} {
	_, local := r.split(c.a.ID)
	rc := r.reviewScans()
	rc.mu.Lock()
	defer rc.mu.Unlock()
	e := rc.entries[local]
	if e == nil {
		e = &reviewEntry{}
		rc.entries[local] = e
	}
	if e.running != nil {
		return e.running
	}
	now := time.Now()
	if sc := e.scan; sc != nil && now.Sub(sc.at) < reviewFresh && keyOf(c, sc) == sc.key {
		return nil
	}
	if now.Before(e.retryAt) {
		return nil
	}
	e.running = make(chan struct{})
	timeout := reviewScanTimeout << min(e.failures, 2)
	go r.runScan(c, e, e.gen, timeout)
	return e.running
}

func (r *Registry) runScan(c reviewCandidate, e *reviewEntry, gen uint64, timeout time.Duration) {
	rc := r.reviewScans()
	_, local := r.split(c.a.ID)
	var sc *reviewScan
	var err error
	var start time.Time
	for {
		rc.sem <- struct{}{}
		start = time.Now()
		sc, err = r.scanFolder(c, timeout)
		<-rc.sem
		if f := scanned.Load(); f != nil {
			(*f)(sc)
		}
		rc.mu.Lock()
		if e.gen == gen {
			break
		}
		// forgetScan ran meanwhile (review.accept, reject or sendBack
		// changed the folder): this scan may show it as it was, so its
		// waiters get a new one.
		gen = e.gen
		rc.mu.Unlock()
		nc, ok := r.reviewCandidateOf(local)
		if !ok {
			rc.mu.Lock()
			e.settled = false
			close(e.running)
			e.running = nil
			rc.mu.Unlock()
			return
		}
		c = nc
	}
	prev := e.scan
	if err != nil {
		e.failures++
		wait := min(reviewBackoff<<min(e.failures-1, 10), reviewBackoffMax)
		e.retryAt = time.Now().Add(wait)
		r.opt.Logf("hesperd: review of %s: %v (after %v; next try in %v)", c.a.ID, err, time.Since(start).Round(time.Millisecond), wait)
	} else {
		e.failures, e.retryAt, e.scan = 0, time.Time{}, sc
	}
	settled := e.settled
	e.settled = false
	close(e.running)
	e.running = nil
	rc.mu.Unlock()
	if err == nil && (scanDiffers(prev, sc) || settled && sc.item != nil) {
		r.noteReview(c.a.ID)
	}
}

// scanDiffers: the folder's entry changed between two scans.
func scanDiffers(prev, sc *reviewScan) bool {
	var a, b *wire.ReviewItem
	if prev != nil {
		a = prev.item
	}
	if sc != nil {
		b = sc.item
	}
	if a == nil || b == nil {
		return (a == nil) != (b == nil)
	}
	return a.Files != b.Files || a.Added != b.Added || a.Removed != b.Removed || a.Base != b.Base || a.Risk != b.Risk
}

// scanFolder is one scan of c's folder, on its own deadline: a caller
// that stops waiting leaves it to finish for the next.
func (r *Registry) scanFolder(c reviewCandidate, timeout time.Duration) (*reviewScan, error) {
	scansRun.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	g := r.reviewGit(ctx)
	f, base, err := r.reviewFolder(g, c.a)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("git: %w", ctx.Err())
	}
	if err != nil { // no Git folder (or no commit yet): nothing to review
		return &reviewScan{key: keyOf(c, nil), at: time.Now()}, nil
	}
	files, err := g.StatePaths(f)
	if err != nil {
		return nil, err
	}
	sc := &reviewScan{at: time.Now(), top: f.Top, gitFiles: files}
	sc.key = keyOf(c, sc) // before Git reads the folder: a change while it does shows next time
	tree, err := g.Snapshot(f)
	if err != nil {
		return nil, err
	}
	changes, err := g.Changes(f, base, tree)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		sc.key.changed = sc.changedStat()
		return sc, nil
	}
	item := &wire.ReviewItem{Files: len(changes), Base: base}
	for _, ch := range changes {
		item.Added += ch.Added
		item.Removed += ch.Removed
		for _, p := range []string{ch.Path, ch.OldPath} {
			if p != "" && len(sc.changed) < reviewKeyFiles {
				sc.changed = append(sc.changed, p)
			}
		}
	}
	item.Risk, item.RiskNotes = review.ChangeRisk(changes)
	sc.item = item
	sc.key.changed = sc.changedStat()
	return sc, nil
}

// reviewSettled schedules the scan of an agent that settled (the
// registry's lock is held): after reviewSettleDelay, so a burst of
// settles scans once; its subscribers get review.changed when the folder
// shows changes.
func (r *Registry) reviewSettled(local string) {
	rc := r.reviewScans()
	rc.mu.Lock()
	defer rc.mu.Unlock()
	e := rc.entries[local]
	if e == nil {
		e = &reviewEntry{}
		rc.entries[local] = e
	}
	if e.timer != nil {
		e.timer.Reset(reviewSettleDelay)
		return
	}
	e.timer = time.AfterFunc(reviewSettleDelay, func() {
		rc.mu.Lock()
		e.timer = nil
		rc.mu.Unlock()
		c, ok := r.reviewCandidateOf(local)
		if !ok {
			return
		}
		rc.mu.Lock()
		e.settled = true
		e.retryAt = time.Time{} // a settle is worth a try
		rc.mu.Unlock()
		if r.wantScan(c) == nil {
			// The scan holds (nothing changed): its result stands.
			rc.mu.Lock()
			hasChanges := e.running == nil && e.scan != nil && e.scan.item != nil
			e.settled = false
			rc.mu.Unlock()
			if hasChanges {
				r.noteReview(c.a.ID)
			}
		}
	})
}

// forgetScan drops an agent's scan: the next review.list scans its folder
// (review.accept, reject and sendBack changed it).
func (r *Registry) forgetScan(local string) {
	rc := r.reviewScans()
	rc.mu.Lock()
	if e := rc.entries[local]; e != nil {
		e.scan, e.failures, e.retryAt = nil, 0, time.Time{}
		e.gen++
	}
	rc.mu.Unlock()
}

// pruneScans drops the scans of agents that are gone.
func (r *Registry) pruneScans(all map[string]bool) {
	rc := r.reviewScans()
	rc.mu.Lock()
	for local, e := range rc.entries {
		if !all[local] && e.running == nil {
			if e.timer != nil {
				e.timer.Stop()
			}
			delete(rc.entries, local)
		}
	}
	rc.mu.Unlock()
}

// scanned is local's last scan (nil: none).
func (r *Registry) scanned(local string) *reviewScan {
	rc := r.reviewScans()
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if e := rc.entries[local]; e != nil {
		return e.scan
	}
	return nil
}
