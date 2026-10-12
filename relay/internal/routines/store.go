package routines

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Store holds a daemon's routines: dir/routines.json (the list, written
// atomically, 0600) and dir/routines/<id>.runs.jsonl (one run record per
// line in Seq order, from the run's begin; the newest KeepRuns kept, a
// running record never trimmed). Changes reach subscribers coalesced per
// routine, as drafts; a change to a routine's clock (NextRun, Paused) also
// signals Changed for the scheduler.
type Store struct {
	path    string // "": in memory only
	runsDir string
	loc     *time.Location
	logf    func(string, ...any)
	now     func() time.Time

	mu       sync.Mutex
	routines map[string]wire.Routine
	memRuns  map[string][]wire.RoutineRun // the run records when in memory only
	subs     map[*Sub]struct{}
	changed  chan struct{}
	wmu      sync.Mutex // serializes file writes
}

// StoreOptions configure Open. Zero values take the defaults.
type StoreOptions struct {
	Dir  string               // the state directory; "": memory only
	Loc  *time.Location       // the cadence's local time; time.Local
	Logf func(string, ...any) // nil: silent
	Now  func() time.Time     // the clock; time.Now
}

// Open loads dir/routines.json. A broken file moves to .broken and the
// store starts empty; a routine with a bad id or cadence is dropped. An
// unpaused routine's NextRun is kept when it is already due (the clock
// catches up as its policy says) and recomputed otherwise.
func Open(opt StoreOptions) (*Store, error) {
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Loc == nil {
		opt.Loc = time.Local
	}
	s := &Store{loc: opt.Loc, logf: opt.Logf, now: opt.Now, routines: map[string]wire.Routine{}, memRuns: map[string][]wire.RoutineRun{},
		subs: map[*Sub]struct{}{}, changed: make(chan struct{}, 1)}
	if opt.Dir == "" {
		return s, nil
	}
	s.path = filepath.Join(opt.Dir, "routines.json")
	s.runsDir = filepath.Join(opt.Dir, "routines")
	if err := os.MkdirAll(s.runsDir, 0o700); err != nil {
		return nil, fmt.Errorf("routines: %w", err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			opt.Logf("hesperd: routines.json: %v", err)
		}
		return s, nil
	}
	var f routineFile
	if err := json.Unmarshal(data, &f); err != nil {
		os.Rename(s.path, s.path+".broken")
		opt.Logf("hesperd: routines.json: %v (moved to routines.json.broken)", err)
		return s, nil
	}
	now := opt.Now().UTC()
	for _, r := range f.Routines {
		if !ID.MatchString(r.ID) {
			opt.Logf("hesperd: routines.json: dropped a routine with id %q", r.ID)
			continue
		}
		if err := Validate(&r); err != nil {
			opt.Logf("hesperd: routines.json: dropped %s: %v", r.ID, err)
			continue
		}
		if r.Paused {
			r.NextRun = time.Time{}
		} else if r.NextRun.IsZero() || r.NextRun.After(now) {
			r.NextRun = s.next(r, now)
		}
		s.routines[r.ID] = r
	}
	return s, nil
}

type routineFile struct {
	Version  int            `json:"version"`
	Routines []wire.Routine `json:"routines"`
}

// Loc is the location the cadence is read in.
func (s *Store) Loc() *time.Location { return s.loc }

// next is r's NextRun after after (zero when paused or the cadence is
// bad, which Validate rules out for saved routines).
func (s *Store) next(r wire.Routine, after time.Time) time.Time {
	if r.Paused {
		return time.Time{}
	}
	t, err := Next(r, after, s.loc)
	if err != nil {
		s.logf("hesperd: routine %s: %v", r.ID, err)
		return time.Time{}
	}
	return t
}

// List is every routine, oldest first.
func (s *Store) List() []wire.Routine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) listLocked() []wire.Routine {
	out := make([]wire.Routine, 0, len(s.routines))
	for _, r := range s.routines {
		out = append(out, clone(r))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// clone copies a routine so a caller's copy never shares LastRun or At.
func clone(r wire.Routine) wire.Routine {
	if r.LastRun != nil {
		run := *r.LastRun
		r.LastRun = &run
	}
	if r.At != nil {
		r.At = append([]string(nil), r.At...)
	}
	return r
}

// Get is one routine (not_found when there is none).
func (s *Store) Get(id string) (wire.Routine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.routines[id]
	if !ok {
		return wire.Routine{}, wire.Errorf(wire.CodeNotFound, "no routine %s", id)
	}
	return clone(r), nil
}

// Save creates or replaces a routine (Validate fills its defaults; an
// empty ID gets one). Created, CreatedBy, Runs and LastRun stay from the
// existing routine; Updated is now; NextRun is recomputed.
func (s *Store) Save(r wire.Routine) (wire.Routine, error) {
	if r.ID == "" {
		r.ID = newID()
	}
	if err := Validate(&r); err != nil {
		return wire.Routine{}, err
	}
	now := s.now().UTC()
	s.mu.Lock()
	old, exists := s.routines[r.ID]
	if !exists && len(s.routines) >= RoutineLimit {
		s.mu.Unlock()
		return wire.Routine{}, wire.Errorf(wire.CodeInvalid, "too many routines (%d)", RoutineLimit)
	}
	if exists {
		r.Created, r.CreatedBy, r.Runs, r.LastRun = old.Created, old.CreatedBy, old.Runs, old.LastRun
	} else {
		r.Created, r.Runs, r.LastRun = now, 0, nil
	}
	r.Updated = now
	r.NextRun = s.next(r, now)
	s.routines[r.ID] = r
	s.notifyLocked(r.ID, &r)
	if !exists || !old.NextRun.Equal(r.NextRun) || old.Paused != r.Paused {
		s.wake()
	}
	s.mu.Unlock()
	s.save()
	return clone(r), nil
}

// Remove forgets a routine and its run records (not_found when there is
// none).
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	if _, ok := s.routines[id]; !ok {
		s.mu.Unlock()
		return wire.Errorf(wire.CodeNotFound, "no routine %s", id)
	}
	delete(s.routines, id)
	delete(s.memRuns, id)
	s.notifyLocked(id, nil)
	s.wake()
	s.mu.Unlock()
	if s.path != "" {
		if err := os.Remove(s.runsPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.logf("hesperd: removing %s: %v", s.runsPath(id), err)
		}
	}
	s.save()
	return nil
}

// SetPaused pauses (NextRun zero) or resumes (NextRun from now) a
// routine.
func (s *Store) SetPaused(id string, paused bool) (wire.Routine, error) {
	now := s.now().UTC()
	s.mu.Lock()
	r, ok := s.routines[id]
	if !ok {
		s.mu.Unlock()
		return wire.Routine{}, wire.Errorf(wire.CodeNotFound, "no routine %s", id)
	}
	r.Paused = paused
	r.NextRun = s.next(r, now)
	r.Updated = now
	s.routines[id] = r
	s.notifyLocked(id, &r)
	s.wake()
	s.mu.Unlock()
	s.save()
	return clone(r), nil
}

// BeginRun records that run Runs+1 of a routine starts now: a running
// record, and LastRun.
func (s *Store) BeginRun(id string, manual bool) (wire.Routine, wire.RoutineRun, error) {
	now := s.now().UTC()
	s.mu.Lock()
	r, ok := s.routines[id]
	if !ok {
		s.mu.Unlock()
		return wire.Routine{}, wire.RoutineRun{}, wire.Errorf(wire.CodeNotFound, "no routine %s", id)
	}
	r.Runs++
	run := wire.RoutineRun{Seq: r.Runs, Started: now, Outcome: wire.RunRunning, Manual: manual}
	r.LastRun = &run
	s.routines[id] = r
	s.notifyLocked(id, &r)
	s.mu.Unlock()
	if err := s.putRun(id, run, keepOf(r)); err != nil {
		s.logf("hesperd: %s: %v", s.runsPath(id), err)
	}
	s.save()
	return clone(r), run, nil
}

// UpdateRun replaces a run's record (its Seq) with run: the agent once it
// is spawned; and then LastRun when it is the latest run (the record
// first: LastRun is the records' cache).
func (s *Store) UpdateRun(id string, run wire.RoutineRun) error {
	s.mu.Lock()
	r, ok := s.routines[id]
	s.mu.Unlock()
	if !ok {
		return wire.Errorf(wire.CodeNotFound, "no routine %s", id)
	}
	if err := s.putRun(id, run, keepOf(r)); err != nil {
		s.logf("hesperd: %s: %v", s.runsPath(id), err)
	}
	s.mu.Lock()
	if r, ok = s.routines[id]; ok && r.LastRun != nil && r.LastRun.Seq == run.Seq {
		latest := run
		r.LastRun = &latest
		s.routines[id] = r
		s.notifyLocked(id, &r)
	}
	s.mu.Unlock()
	s.save()
	return nil
}

// EndRun records how a run ended (UpdateRun; Ended is now when the run
// left it zero).
func (s *Store) EndRun(id string, run wire.RoutineRun) error {
	if run.Ended.IsZero() {
		run.Ended = s.now().UTC()
	}
	return s.UpdateRun(id, run)
}

// keepOf is a routine's KeepRuns with the default.
func keepOf(r wire.Routine) int {
	if r.Policy.KeepRuns <= 0 {
		return DefaultKeepRuns
	}
	return r.Policy.KeepRuns
}

// RecordSkipped records a run that never started (BeginRun and EndRun
// with outcome skipped and reason as its error).
func (s *Store) RecordSkipped(id, reason string, manual bool) (wire.RoutineRun, error) {
	_, run, err := s.BeginRun(id, manual)
	if err != nil {
		return wire.RoutineRun{}, err
	}
	run.Outcome, run.Error, run.Ended = wire.RunSkipped, reason, run.Started
	if err := s.EndRun(id, run); err != nil {
		return wire.RoutineRun{}, err
	}
	return run, nil
}

// Advance moves a routine's clock: NextRun is the first run after after
// (unchanged, zero, while paused).
func (s *Store) Advance(id string, after time.Time) (wire.Routine, error) {
	s.mu.Lock()
	r, ok := s.routines[id]
	if !ok {
		s.mu.Unlock()
		return wire.Routine{}, wire.Errorf(wire.CodeNotFound, "no routine %s", id)
	}
	if !r.Paused {
		r.NextRun = s.next(r, after.UTC())
		s.routines[id] = r
		s.notifyLocked(id, &r)
		s.wake()
	}
	s.mu.Unlock()
	s.save()
	return clone(r), nil
}

// Runs is a routine's run records, newest (highest Seq) first, at most
// limit (default 50).
func (s *Store) Runs(id string, limit int) ([]wire.RoutineRun, error) {
	if limit <= 0 {
		limit = 50
	}
	runs, err := s.records(id)
	if err != nil {
		return nil, err
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Seq > runs[j].Seq })
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

// Running is a routine's records still running, oldest first (none for
// an unknown routine): what a daemon start has to pick up.
func (s *Store) Running(id string) []wire.RoutineRun {
	runs, _ := s.records(id)
	var out []wire.RoutineRun
	for _, run := range runs {
		if run.Outcome == wire.RunRunning {
			out = append(out, run)
		}
	}
	return out
}

// records is a routine's run records in Seq order (not_found without the
// routine).
func (s *Store) records(id string) ([]wire.RoutineRun, error) {
	s.mu.Lock()
	if _, ok := s.routines[id]; !ok {
		s.mu.Unlock()
		return nil, wire.Errorf(wire.CodeNotFound, "no routine %s", id)
	}
	var runs []wire.RoutineRun
	if s.path == "" {
		runs = append(runs, s.memRuns[id]...)
	}
	s.mu.Unlock()
	if s.path != "" {
		runs = s.readRuns(id)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Seq < runs[j].Seq })
	return runs, nil
}

// Due is the unpaused routines whose NextRun is at or before now, the
// earliest first.
func (s *Store) Due(now time.Time) []wire.Routine {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []wire.Routine
	for _, r := range s.routines {
		if !r.Paused && !r.NextRun.IsZero() && !r.NextRun.After(now) {
			out = append(out, clone(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].NextRun.Equal(out[j].NextRun) {
			return out[i].NextRun.Before(out[j].NextRun)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Changed is signalled whenever a routine's clock changed (NextRun,
// Paused, a routine saved or removed): the scheduler recomputes its wait.
func (s *Store) Changed() <-chan struct{} { return s.changed }

func (s *Store) wake() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// --- files ---

func (s *Store) save() {
	if s.path == "" {
		return
	}
	// One writer at a time, each writing the latest list: the file never
	// goes back to an older state.
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	f := routineFile{Version: 1, Routines: s.listLocked()}
	s.mu.Unlock()
	data, err := json.MarshalIndent(f, "", "  ")
	if err == nil {
		err = atomicWrite(s.path, append(data, '\n'), 0o600)
	}
	if err != nil {
		s.logf("hesperd: saving routines.json: %v", err)
	}
}

func (s *Store) runsPath(id string) string { return filepath.Join(s.runsDir, id+".runs.jsonl") }

// putRun writes a run into a routine's records (mergeRun) and rewrites
// the file (it is small: at most keep lines).
func (s *Store) putRun(id string, run wire.RoutineRun, keep int) error {
	if s.path == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.memRuns[id] = mergeRun(s.memRuns[id], run, keep)
		return nil
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	var buf bytes.Buffer
	for _, r := range mergeRun(s.readRuns(id), run, keep) {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf.Write(append(line, '\n'))
	}
	return atomicWrite(s.runsPath(id), buf.Bytes(), 0o600)
}

// mergeRun puts run into runs: the record with its Seq is replaced, else
// it is added; the records stay in Seq order and are trimmed to keep,
// the oldest ended ones first (a running record is never trimmed).
func mergeRun(runs []wire.RoutineRun, run wire.RoutineRun, keep int) []wire.RoutineRun {
	replaced := false
	for i := range runs {
		if runs[i].Seq == run.Seq {
			runs[i], replaced = run, true
			break
		}
	}
	if !replaced {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Seq < runs[j].Seq })
	for len(runs) > keep {
		trimmed := false
		for i, r := range runs {
			if r.Outcome != wire.RunRunning {
				runs, trimmed = append(runs[:i], runs[i+1:]...), true
				break
			}
		}
		if !trimmed {
			break
		}
	}
	return runs
}

// readLines is the run records file's lines, each ending in a newline.
func (s *Store) readLines(id string) [][]byte {
	data, err := os.ReadFile(s.runsPath(id))
	if err != nil {
		return nil
	}
	var lines [][]byte
	for _, l := range bytes.SplitAfter(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(l)) > 0 {
			lines = append(lines, l)
		}
	}
	return lines
}

// readRuns is the run records file, oldest first; a bad line is skipped.
func (s *Store) readRuns(id string) []wire.RoutineRun {
	var runs []wire.RoutineRun
	for _, l := range s.readLines(id) {
		var run wire.RoutineRun
		if err := json.Unmarshal(l, &run); err == nil {
			runs = append(runs, run)
		}
	}
	return runs
}

// atomicWrite replaces path with data (temp file, fsync, rename).
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func newID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return "r-" + string(b)
}

// --- subscriptions: like drafts', coalesced per routine (the latest wins) ---

// Sub is a subscription to the store's changes: a new one first gets
// every routine.
type Sub struct {
	mu      sync.Mutex
	pending map[string]*wire.Routine // nil: removed
	order   []string
	wake    chan struct{}
}

// Subscribe starts a subscription; Unsubscribe ends it.
func (s *Store) Subscribe() *Sub {
	sub := &Sub{pending: map[string]*wire.Routine{}, wake: make(chan struct{}, 1)}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.listLocked() {
		c := r
		sub.push(r.ID, &c)
	}
	s.subs[sub] = struct{}{}
	return sub
}

// Unsubscribe ends a subscription.
func (s *Store) Unsubscribe(sub *Sub) {
	s.mu.Lock()
	delete(s.subs, sub)
	s.mu.Unlock()
}

func (s *Store) notifyLocked(id string, r *wire.Routine) {
	for sub := range s.subs {
		var v *wire.Routine
		if r != nil {
			c := clone(*r)
			v = &c
		}
		sub.push(id, v)
	}
}

func (sub *Sub) push(id string, v *wire.Routine) {
	sub.mu.Lock()
	if _, ok := sub.pending[id]; !ok {
		sub.order = append(sub.order, id)
	}
	sub.pending[id] = v
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

// Take drains the pending changes: the routines changed (in order of
// first change) and the ids removed.
func (sub *Sub) Take() (changed []wire.Routine, removed []string) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	for _, id := range sub.order {
		if v := sub.pending[id]; v != nil {
			changed = append(changed, *v)
		} else {
			removed = append(removed, id)
		}
	}
	sub.order, sub.pending = nil, map[string]*wire.Routine{}
	return
}

// Wake is signalled when there is something to Take.
func (sub *Sub) Wake() <-chan struct{} { return sub.wake }
