package agents

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/review"
)

// Review logs (review.go): review/<local id>.json in the state directory,
// one per agent, written shortly after a change and when the daemon
// stops, removed with the agent.

// reviewSaveDelay coalesces bursts of changes into one write.
var reviewSaveDelay = 200 * time.Millisecond

type reviewStore struct {
	dir   string
	logf  func(format string, args ...any)
	mu    sync.Mutex
	logs  map[string]*review.Log
	dirty map[string]bool
	timer *time.Timer
}

// reviews is the registry's review log store.
func (r *Registry) reviews() *reviewStore {
	r.reviewOnce.Do(func() {
		r.reviewLogs = &reviewStore{dir: filepath.Join(r.opt.StateDir, "review"), logf: r.opt.Logf, logs: map[string]*review.Log{}, dirty: map[string]bool{}}
	})
	return r.reviewLogs
}

func (s *reviewStore) path(local string) string { return filepath.Join(s.dir, local+".json") }

// load is local's log (the lock is held).
func (s *reviewStore) load(local string) *review.Log {
	if l := s.logs[local]; l != nil {
		return l
	}
	l := &review.Log{}
	if data, err := os.ReadFile(s.path(local)); err == nil {
		if err := json.Unmarshal(data, l); err != nil {
			s.logf("hesperd: %s: %v (review log started anew)", s.path(local), err)
			l = &review.Log{}
		}
	}
	s.logs[local] = l
	return l
}

// get is a copy of local's log.
func (s *reviewStore) get(local string, fn func(*review.Log)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.load(local))
}

// update changes local's log and schedules its write.
func (s *reviewStore) update(local string, fn func(*review.Log)) {
	s.record(local, func(l *review.Log) bool { fn(l); return true })
}

// record is update when fn reports a change.
func (s *reviewStore) record(local string, fn func(*review.Log) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !fn(s.load(local)) {
		return
	}
	s.dirty[local] = true
	if s.timer == nil {
		s.timer = time.AfterFunc(reviewSaveDelay, s.flush)
	}
}

// forget removes local's log (the agent left the registry).
func (s *reviewStore) forget(local string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.logs, local)
	delete(s.dirty, local)
	if err := os.Remove(s.path(local)); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logf("hesperd: %v", err)
	}
}

// flush writes the changed logs.
func (s *reviewStore) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	if len(s.dirty) == 0 {
		return
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		s.logf("hesperd: %v", err)
		return
	}
	for local := range s.dirty {
		l := s.logs[local]
		if l == nil {
			continue
		}
		data, err := json.Marshal(l)
		if err == nil {
			err = atomicWrite(s.path(local), append(data, '\n'), 0o600)
		}
		if err != nil {
			s.logf("hesperd: saving %s: %v", s.path(local), err)
		}
	}
	s.dirty = map[string]bool{}
}
