// Package loops is hesperd's loops store: loops (a lasting record of one
// piece of work: the agents, sessions, projects, URLs, files and other
// loops that worked on it, its items and who acts next), the goals and
// milestones of products (projects and groups), the Steward's shared
// configuration and the shared loop settings. Every field is a
// last-writer-wins register (internal/crdt), so the store is shared
// between the owner's Macs like projects are (sync.go; carried by part
// R's links, internal/host and internal/remote; the relay sees none of
// it). Persistence: $HESPER_STATE_DIR/loops.json.
//
// The store keeps settled facts only (an agent's name, how its turns
// ended, how it was closed). Live state (is it working, waiting on the
// person) comes from the agent list, joined in when views are built
// (views.go), never while the store's lock is held: the registry calls
// into the store with its own lock held (internal/agents/loophook.go).
package loops

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/crdt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Options configure a Store.
type Options struct {
	// StateDir holds loops.json ("": in memory only).
	StateDir string
	// Node is this Mac's node (the projects store's: Store.Node, the
	// first half of every agent ref this Mac writes); "": a new one.
	Node string
	// Machine is this Mac's short name (views).
	Machine string
	Logf    func(format string, args ...any)
	// Now is the clock (tests).
	Now func() time.Time
}

// Limits. Caps apply when views are built, never when merging (two Macs
// must keep the same records whatever they show).
const (
	maxViewLinks  = 64
	maxViewItems  = 100
	maxViewLoops  = 2000
	resultMax     = 1 << 10
	resultMaxAge  = 90 * 24 * time.Hour
	pruneEvery    = 24 * time.Hour
	viewsDebounce = 50 * time.Millisecond
	saveDebounce  = 50 * time.Millisecond
)

// Record keys (seqs, views): "l:<loop>", "g:<goal>", "m:<milestone>",
// "steward", "settings".
const (
	keySteward  = "steward"
	keySettings = "settings"
)

// Store is the loops store of this daemon.
type Store struct {
	opt  Options
	path string

	mu         sync.Mutex
	node       string
	epoch      string // a new file: other Macs pull it from the start
	clock      crdt.Clock
	loops      map[string]*LoopRecord
	goals      map[string]*GoalRecord
	milestones map[string]*MilestoneRecord
	steward    StewardRecord
	settings   SettingsRecord
	seq        int64            // the newest change's sequence number
	seqs       map[string]int64 // record key → its last change's
	idx        *index           // index.go (nil: built on first use)
	dirty      map[string]bool  // loops changed since the last view build
	allDirty   bool             // every view (names, agents source)
	shared     chan struct{}    // closed at the next change (sync)
	stewardCh  chan struct{}    // closed at the next Steward config change

	// Joined in when views are built (views.go), each called without
	// the lock held.
	agents    func() []wire.Agent
	nodeOf    func(machine string) string
	machineOf func(node string) string
	sessNode  func() string
	classify  func(message string) (ending string, sentences []string)
	// created (SetCreated): told of each loop a method call made here
	created func(id, service string)

	views    map[string]json.RawMessage // what watchers last got
	live     map[string]liveFact        // the live agents views last joined, by ref
	watchers map[*watcher]struct{}
	viewsCh  chan struct{}
	vmu      sync.Mutex // one view build at a time

	saveCh chan struct{}
	done   chan struct{}
	wg     sync.WaitGroup
	wmu    sync.Mutex // one file write at a time
	closed bool
}

type storeFile struct {
	Version    int                `json:"version"`
	Node       string             `json:"node"`
	Epoch      string             `json:"epoch"`
	Clock      stamp              `json:"clock"`
	Seq        int64              `json:"seq"`
	Seqs       map[string]int64   `json:"seqs,omitempty"`
	Loops      []*LoopRecord      `json:"loops"`
	Goals      []*GoalRecord      `json:"goals"`
	Milestones []*MilestoneRecord `json:"milestones"`
	Steward    StewardRecord      `json:"steward"`
	Settings   SettingsRecord     `json:"settings"`
}

// Open loads dir/loops.json (a file that does not decode is moved to
// loops.json.broken and the store starts empty).
func Open(opt Options) *Store {
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	s := &Store{opt: opt, node: opt.Node, loops: map[string]*LoopRecord{}, goals: map[string]*GoalRecord{},
		milestones: map[string]*MilestoneRecord{}, seqs: map[string]int64{}, dirty: map[string]bool{}, shared: make(chan struct{}),
		stewardCh: make(chan struct{}), views: map[string]json.RawMessage{}, watchers: map[*watcher]struct{}{},
		viewsCh: make(chan struct{}, 1), saveCh: make(chan struct{}, 1), done: make(chan struct{})}
	s.classify = func(string) (string, []string) { return wire.EndingDone, nil }
	if opt.StateDir != "" {
		s.path = filepath.Join(opt.StateDir, "loops.json")
		s.load()
	}
	if s.node == "" {
		s.node = "n-" + randomHex(8)
	}
	if s.epoch == "" {
		s.epoch = randomHex(8)
		s.seq, s.seqs = 0, map[string]int64{}
		for _, k := range s.keysLocked() {
			s.bumpLocked(k)
		}
		s.scheduleSave()
	}
	s.clock.Node, s.clock.Now = s.node, opt.Now
	s.pruneLocked()
	s.commitLocked(s.breakCyclesLocked()...) // a file from before cycles were broken
	s.wg.Add(2)
	go s.saver()
	go s.viewer()
	s.kickViews()
	return s
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.opt.Logf("hesperd: loops.json: %v", err)
		}
		return
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		os.Rename(s.path, s.path+".broken")
		s.opt.Logf("hesperd: loops.json: %v (moved to loops.json.broken)", err)
		return
	}
	if s.node != "" && f.Node != "" && f.Node != s.node {
		// The projects store got a new node (its file was lost): this
		// file's sequence numbers start over under a new epoch.
		f.Epoch = ""
	}
	s.epoch, s.seq, s.clock.Last = f.Epoch, f.Seq, stamp{T: f.Clock.T, C: f.Clock.C}
	if s.node == "" {
		s.node = f.Node
	}
	for k, v := range f.Seqs {
		s.seqs[k] = v
	}
	for _, l := range f.Loops {
		if l != nil && validID(l.ID, "lp-") {
			l.normalize()
			s.loops[l.ID] = l
		}
	}
	for _, g := range f.Goals {
		if g != nil && validID(g.ID, "gl-") {
			s.goals[g.ID] = g
		}
	}
	for _, m := range f.Milestones {
		if m != nil && validID(m.ID, "ms-") {
			if m.Items == nil {
				m.Items = map[string]*ItemRecord{}
			}
			s.milestones[m.ID] = m
		}
	}
	s.steward, s.settings = f.Steward, f.Settings
}

func (l *LoopRecord) normalize() {
	if l.Links == nil {
		l.Links = map[string]*LinkRecord{}
	}
	if l.Items == nil {
		l.Items = map[string]*ItemRecord{}
	}
	for k, v := range l.Links {
		if v == nil {
			delete(l.Links, k)
		}
	}
	for k, v := range l.Items {
		if v == nil {
			delete(l.Items, k)
		}
	}
}

// Close stops the store and writes it.
func (s *Store) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	for w := range s.watchers {
		w.close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.write()
}

// Node is this Mac's node (the first half of its agent refs).
func (s *Store) Node() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.node
}

// SetAgents gives the store every agent (local and remote) whose live
// state views join in. It is called without the store's lock held.
func (s *Store) SetAgents(fn func() []wire.Agent) {
	s.mu.Lock()
	s.agents, s.allDirty = fn, true
	s.mu.Unlock()
	s.kickViews()
}

// SetNames maps machine names (as this Mac calls them) to nodes and back,
// for agent refs and views. Called without the store's lock held.
func (s *Store) SetNames(nodeOf func(machine string) string, machineOf func(node string) string) {
	s.mu.Lock()
	s.nodeOf, s.machineOf, s.allDirty = nodeOf, machineOf, true
	s.mu.Unlock()
	s.kickViews()
}

// SetSessionsNode gives the shared history's node (session refs
// "<node>:<kind>:<session id>").
func (s *Store) SetSessionsNode(fn func() string) {
	s.mu.Lock()
	s.sessNode = fn
	s.mu.Unlock()
}

// SetClassify sets how a final message's ending is read (internal/
// endings.Classify); the default calls every turn done, with no
// sentences.
func (s *Store) SetClassify(fn func(message string) (ending string, sentences []string)) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	s.classify = fn
	s.mu.Unlock()
}

// SetCreated sets who is told when loops.save (without an id) or
// loops.split makes a loop on this Mac: its id and the calling agent's
// service ("" a person or an ordinary agent), so the Steward never wakes
// on its own loops. Called without the store's lock held; fn is too.
func (s *Store) SetCreated(fn func(id, service string)) {
	s.mu.Lock()
	s.created = fn
	s.mu.Unlock()
}

// loopCreated tells SetCreated's fn (the lock is not held).
func (s *Store) loopCreated(id, service string) {
	s.mu.Lock()
	fn := s.created
	s.mu.Unlock()
	if fn != nil && id != "" {
		fn(id, service)
	}
}

// AgentsChanged tells the store an agent's live state changed: views are
// rebuilt (debounced).
func (s *Store) AgentsChanged() { s.kickViews() }

// Changes is closed at the next change of the shared records (sync).
func (s *Store) Changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shared
}

// StewardChanges is closed at the next change of the Steward's
// configuration.
func (s *Store) StewardChanges() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stewardCh
}

// keysLocked are every record's key.
func (s *Store) keysLocked() []string {
	keys := make([]string, 0, len(s.loops)+len(s.goals)+len(s.milestones)+2)
	for id := range s.loops {
		keys = append(keys, "l:"+id)
	}
	for id := range s.goals {
		keys = append(keys, "g:"+id)
	}
	for id := range s.milestones {
		keys = append(keys, "m:"+id)
	}
	sort.Strings(keys)
	return append(keys, keySteward, keySettings)
}

func (s *Store) bumpLocked(key string) {
	s.seq++
	s.seqs[key] = s.seq
}

// commitLocked publishes changes to the records with these keys: sync,
// save, views (and the Steward's watchers when its configuration
// changed).
func (s *Store) commitLocked(keys ...string) {
	if len(keys) == 0 {
		return
	}
	for _, k := range keys {
		s.bumpLocked(k)
		if k == keySteward {
			close(s.stewardCh)
			s.stewardCh = make(chan struct{})
		}
		if len(k) > 2 && k[0] == 'l' {
			s.dirty[k[2:]] = true
			if s.idx != nil {
				s.reindexLocked(k[2:])
			}
		}
	}
	close(s.shared)
	s.shared = make(chan struct{})
	s.scheduleSave()
	s.kickViews()
}

func (s *Store) scheduleSave() {
	select {
	case s.saveCh <- struct{}{}:
	default:
	}
}

func (s *Store) kickViews() {
	select {
	case s.viewsCh <- struct{}{}:
	default:
	}
}

func (s *Store) saver() {
	defer s.wg.Done()
	prune := time.NewTicker(pruneEvery)
	defer prune.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-s.saveCh:
			time.Sleep(saveDebounce)
			s.write()
		case <-prune.C:
			s.mu.Lock()
			s.pruneLocked()
			s.mu.Unlock()
		}
	}
}

func (s *Store) write() {
	if s.path == "" {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	f := storeFile{Version: 1, Node: s.node, Epoch: s.epoch, Clock: stamp{T: s.clock.Last.T, C: s.clock.Last.C}, Seq: s.seq,
		Seqs: s.seqs, Steward: s.steward, Settings: s.settings, Loops: []*LoopRecord{}, Goals: []*GoalRecord{}, Milestones: []*MilestoneRecord{}}
	for _, l := range s.loops {
		f.Loops = append(f.Loops, l)
	}
	for _, g := range s.goals {
		f.Goals = append(f.Goals, g)
	}
	for _, m := range s.milestones {
		f.Milestones = append(f.Milestones, m)
	}
	sort.Slice(f.Loops, func(i, j int) bool { return f.Loops[i].ID < f.Loops[j].ID })
	sort.Slice(f.Goals, func(i, j int) bool { return f.Goals[i].ID < f.Goals[j].ID })
	sort.Slice(f.Milestones, func(i, j int) bool { return f.Milestones[i].ID < f.Milestones[j].ID })
	data, err := json.MarshalIndent(f, "", "  ")
	s.mu.Unlock()
	if err == nil {
		err = atomicWrite(s.path, append(data, '\n'), 0o600)
	}
	if err != nil {
		s.opt.Logf("hesperd: saving loops.json: %v", err)
	}
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

// pruneLocked drops the result excerpts of links closed more than 90
// days ago: a stamped write, so every Mac drops them too.
func (s *Store) pruneLocked() {
	cutoff := s.opt.Now().Add(-resultMaxAge)
	var keys []string
	for id, l := range s.loops {
		changed := false
		for _, k := range l.Links {
			if k.Result.V != "" && !k.Closed.V.IsZero() && k.Closed.V.Before(cutoff) {
				changed = k.Result.Set("", s.clock.Tick()) || changed
			}
		}
		if changed {
			keys = append(keys, "l:"+id)
		}
	}
	s.commitLocked(keys...)
}

// --- ids and refs ---

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newID(prefix string) string {
	if prefix == "it-" {
		return prefix + randomHex(4)
	}
	return prefix + randomHex(6)
}

// validID: prefix, then 8 to 32 lowercase hex digits.
func validID(id, prefix string) bool {
	if len(id) < len(prefix)+8 || len(id) > len(prefix)+32 || id[:len(prefix)] != prefix {
		return false
	}
	for _, c := range id[len(prefix):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// linkKey is a link's key: the same link added on two Macs at once is
// one entry.
func linkKey(kind, rel, ref, from string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + rel + "\x00" + ref + "\x00" + from))
	return "ln-" + hex.EncodeToString(h[:8])
}

// member reports whether a link puts an agent or a session in its loop.
func member(k *LinkRecord) bool {
	if k.Deleted.V || k.Kind.V != wire.LinkAgent && k.Kind.V != wire.LinkSession {
		return false
	}
	switch k.Rel.V {
	case wire.RelMember, wire.RelStarted, wire.RelContinued:
		return true
	}
	return false
}

// maxChain bounds a mergedInto chain.
const maxChain = 16

// resolveLocked follows a loop's mergedInto chain to the loop that holds
// its work now ("" unknown).
func (s *Store) resolveLocked(id string) string {
	end, _ := s.chainLocked(id)
	return end
}

// chainLocked follows id's mergedInto chain: its end ("" id unknown) and,
// when it runs into a cycle (two Macs merged both ways at once), the
// cycle's loops. A cycle ends on its lowest id, on every Mac alike
// (breakCyclesLocked makes that loop unmerged).
func (s *Store) chainLocked(id string) (end string, cycle []string) {
	if s.loops[id] == nil {
		return "", nil
	}
	path := []string{id}
	for range maxChain {
		into := s.loops[id].MergedInto.V
		if into == "" || s.loops[into] == nil {
			return id, nil
		}
		if i := slices.Index(path, into); i >= 0 {
			cycle = path[i:]
			return slices.Min(cycle), cycle
		}
		path = append(path, into)
		id = into
	}
	return id, nil
}

// breakCyclesLocked undoes mergedInto cycles (a merge on each of two Macs
// at once, A into B and B into A): the cycle's lowest id is unmerged and
// takes the others' links and items, the others stay merged (into it,
// through the chain). Every Mac does the same, so they agree. It returns
// the keys it changed.
func (s *Store) breakCyclesLocked() []string {
	var keys []string
	for id, l := range s.loops {
		if l.MergedInto.V == "" {
			continue
		}
		root, cycle := s.chainLocked(id)
		if cycle == nil {
			continue
		}
		to := s.loops[root]
		to.MergedInto.Set("", s.clock.Tick())
		for _, m := range cycle {
			if m != root {
				s.moveLocked(s.loops[m], to, func(*LinkRecord) bool { return true }, func(*ItemRecord) bool { return true })
			}
			keys = append(keys, "l:"+m)
		}
	}
	return keys
}

// loopOfLocked is the loop a ref is a member of ("" none): the newest
// membership wins, merges followed.
func (s *Store) loopOfLocked(ref string) string {
	if ref == "" {
		return ""
	}
	return s.resolveLocked(s.indexLocked().of[ref])
}

// LoopOf is the loop an agent or session ref belongs to ("" none).
func (s *Store) LoopOf(ref string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loopOfLocked(ref)
}
