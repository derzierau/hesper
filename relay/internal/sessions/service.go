// Package sessions is hesperd's shared history (rebuild contract, "As
// built — shared history (data)"): every Claude Code and Codex
// conversation on every Mac of the owner.
//
//   - scan.go reads the transcripts both CLIs write (~/.claude/projects,
//     ~/.codex/sessions incl. .jsonl.zst) incrementally in the background
//     at low priority;
//   - db.go keeps the index in SQLite with FTS5 ($STATE/history.db);
//   - sync.go replicates the index entries to every other Mac over the
//     encrypted links (pull, deltas by sequence number, compressed,
//     batched), merged last-writer-wins per field;
//   - mirror.go copies recent transcripts to the other Macs (sealed);
//   - actions.go resumes, forks, moves, briefs, archives and deletes.
//
// The relay sees none of it: everything travels as signed requests and
// results inside the end-to-end channel.
package sessions

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Options configure the shared history.
type Options struct {
	StateDir   string // history.db, history/mirror
	ConfigDir  string // settings.json: historyMirrorDays
	ClaudeHome string // ~/.claude
	CodexHome  string // ~/.codex
	UserHome   string
	Machine    string // this Mac's short name
	// MirrorDays: transcripts active within this many days are mirrored
	// to the other Macs (settings.json historyMirrorDays, default 14).
	MirrorDays int
	// ScanEvery is how often recent transcripts are looked at (default
	// 2 s); FullScanEvery how often every folder is walked (default 60 s).
	ScanEvery, FullScanEvery time.Duration
	// Foreground: no background priority, no pauses (tests).
	Foreground bool
	// Workers for the first full index (default 2).
	Workers int
	// Busy (benchmarks) replaces "one of this Mac's agents is working".
	Busy func() bool
	Logf func(format string, args ...any)
	Now  func() time.Time
}

// Projects is the project registry (internal/projects.Store).
type Projects interface {
	Resolve(dir string) string
	// PathOn is a project's folder on this Mac ("" when none).
	PathOn(id string) string
	Changes() <-chan struct{}
}

// Service is the shared history of this Mac.
type Service struct {
	opt   Options
	db    *DB
	clock *hlc

	mu       sync.Mutex
	reg      *agents.Registry
	projects Projects
	peers    Peers
	hostKey  *ecdh.PrivateKey
	names    map[string]string // node → this Mac's name for it
	watchers map[*watcher]struct{}
	notes    map[string]bool // keys waiting for sessions.changed
	indexing wire.SessionIndexing
	closed   bool

	ownedMu sync.Mutex
	owned   map[string]bool // "kind:sid" started by hesperd

	scan    *scanner
	sync    *syncer
	mirror  *mirrorer
	deletes *deleter
	gitc    *gitCache

	changed chan struct{} // closed and replaced on every committed change
	stop    chan struct{}
	ctx     context.Context // canceled by Close (requests to other Macs)
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Open opens the index and starts the scanner.
func Open(opt Options) (*Service, error) {
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.ScanEvery <= 0 {
		opt.ScanEvery = 2 * time.Second
	}
	if opt.FullScanEvery <= 0 {
		opt.FullScanEvery = time.Minute
	}
	if opt.Workers <= 0 {
		opt.Workers = 2
	}
	home, _ := os.UserHomeDir()
	if opt.UserHome == "" {
		opt.UserHome = home
	}
	if opt.ClaudeHome == "" {
		if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
			opt.ClaudeHome = d
		} else {
			opt.ClaudeHome = filepath.Join(home, ".claude")
		}
	}
	if opt.CodexHome == "" {
		if d := os.Getenv("CODEX_HOME"); d != "" {
			opt.CodexHome = d
		} else {
			opt.CodexHome = filepath.Join(home, ".codex")
		}
	}
	if opt.MirrorDays <= 0 {
		opt.MirrorDays = mirrorDaysSetting(opt.ConfigDir)
	}
	db, err := openDB(filepath.Join(opt.StateDir, "history.db"))
	if err != nil {
		return nil, err
	}
	s := &Service{opt: opt, db: db, clock: &hlc{node: db.node, now: opt.Now}, names: map[string]string{}, watchers: map[*watcher]struct{}{},
		notes: map[string]bool{}, owned: map[string]bool{}, changed: make(chan struct{}), stop: make(chan struct{})}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.loadOwned()
	s.loadNames()
	s.gitc = newGitCache()
	s.deletes = newDeleter(s)
	s.scan = newScanner(s)
	s.sync = newSyncer(s)
	s.mirror = newMirrorer(s)
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.scan.run() }()
	go func() { defer s.wg.Done(); s.notifier() }()
	return s, nil
}

// mirrorDaysSetting reads settings.json historyMirrorDays (default 14).
func mirrorDaysSetting(dir string) int {
	var st struct {
		HistoryMirrorDays int `json:"historyMirrorDays"`
	}
	if dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil {
			json.Unmarshal(data, &st)
		}
	}
	if st.HistoryMirrorDays > 0 {
		return st.HistoryMirrorDays
	}
	return 14
}

// Close stops everything and closes the index.
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.stop)
	s.cancel()
	for w := range s.watchers {
		w.close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.db.close()
}

// Node is this Mac's node id in the shared history.
func (s *Service) Node() string { return s.db.node }

// SetRegistry connects the agents: live detection, ownership, resume.
func (s *Service) SetRegistry(reg *agents.Registry) {
	s.mu.Lock()
	s.reg = reg
	s.mu.Unlock()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.watchAgents(reg) }()
}

// SetProjects connects the project registry.
func (s *Service) SetProjects(p Projects) {
	s.mu.Lock()
	s.projects = p
	s.mu.Unlock()
	if p != nil {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.watchProjects(p) }()
	}
}

// SetHostKey is this Mac's transfer key (mirror chunks are sealed with it
// for the requester).
func (s *Service) SetHostKey(key *ecdh.PrivateKey) {
	s.mu.Lock()
	s.hostKey = key
	s.mu.Unlock()
}

func (s *Service) registry() *agents.Registry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reg
}

func (s *Service) projectReg() Projects {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects
}

// machine is this Mac's short name.
func (s *Service) machine() string {
	if reg := s.registry(); reg != nil {
		return reg.Machine()
	}
	if s.opt.Machine != "" {
		return s.opt.Machine
	}
	return "L"
}

// nameOf is this Mac's name for a node: itself, the machine it pulled the
// node's entries from, else the name the node gives itself.
func (s *Service) nameOf(node, home string) string {
	if node == s.db.node {
		return s.machine()
	}
	s.mu.Lock()
	n := s.names[node]
	s.mu.Unlock()
	if n != "" {
		return n
	}
	if home != "" {
		return home
	}
	return node
}

// nodeOf is the node a machine name stands for ("" when unknown).
func (s *Service) nodeOf(name string) string {
	if name == "" || name == s.machine() {
		return s.db.node
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for node, n := range s.names {
		if n == name {
			return node
		}
	}
	return ""
}

func (s *Service) loadNames() {
	rows, err := s.db.r.Query(`SELECT node, short FROM peers WHERE short != ''`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var node, short string
		if rows.Scan(&node, &short) == nil {
			s.names[node] = short
		}
	}
}

func (s *Service) loadOwned() {
	rows, err := s.db.r.Query(`SELECT kind, sid FROM owned`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var kind, sid string
		if rows.Scan(&kind, &sid) == nil {
			s.owned[kind+":"+sid] = true
		}
	}
}

func (s *Service) isOwned(kind, sid string) bool {
	s.ownedMu.Lock()
	defer s.ownedMu.Unlock()
	return s.owned[kind+":"+sid]
}

// own records that hesperd started a session (not external).
func (s *Service) own(kind, sid string) {
	if sid == "" || (kind != wire.KindClaude && kind != wire.KindCodex) {
		return
	}
	s.ownedMu.Lock()
	known := s.owned[kind+":"+sid]
	s.owned[kind+":"+sid] = true
	s.ownedMu.Unlock()
	if known {
		return
	}
	s.db.write(func(tx sqlTx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO owned (kind, sid) VALUES (?, ?)`, kind, sid)
		return err
	})
	// An indexed session that turns out to be hesperd's is not external.
	s.updateLocal(kind, sid, func(r *Record) bool {
		if !r.Meta.External {
			return false
		}
		r.Meta.External = false
		return true
	})
}

// toWire is a record as the app sees it.
func (s *Service) toWire(r *Record) wire.Session {
	m := &r.Meta
	machine := s.nameOf(r.Node, r.Home)
	todos := m.Todos
	if todos == nil {
		todos = []wire.SessionTodo{}
	}
	out := wire.Session{ID: machine + ":" + r.Kind + ":" + r.SID, Kind: r.Kind, SessionID: r.SID, Machine: machine, Cwd: m.Cwd,
		ProjectID: m.ProjectID, Branch: m.Branch, Title: m.Title, FirstPrompt: m.FirstPrompt, LastUser: m.LastUser,
		LastAssistant: m.LastAssistant, Todos: todos, Turns: m.Turns, Tokens: m.Tokens, StartedAt: msTime(m.StartedAt),
		LastActivity: msTime(m.LastActivity), Live: m.Live, External: m.External, Archived: r.Archived, Mirrored: []string{machine},
		Origin: m.Origin, RemovedAt: msTime(r.RemovedAt), Bytes: m.Size}
	if r.MovedTo != "" {
		out.MovedTo = s.nameOf(r.MovedTo, "")
	}
	for node, mk := range r.Mirrors {
		if mk.At > 0 && node != r.Node {
			out.Mirrored = append(out.Mirrored, s.nameOf(node, ""))
		}
	}
	if len(out.Mirrored) > 2 {
		rest := out.Mirrored[1:]
		sortStrings(rest)
	}
	return out
}

// find resolves a wire id ("<machine>:<kind>:<sessionId>") or an internal
// key ("<node>:<kind>:<sid>").
func (s *Service) find(id string) (*row, error) {
	machine, kind, sid, ok := splitKey(id)
	if !ok || !sidRE.MatchString(sid) {
		return nil, wire.Errorf(wire.CodeInvalid, "bad session id %q", id)
	}
	if rw, err := s.db.get(id); err != nil {
		return nil, err
	} else if rw != nil {
		return rw, nil
	}
	rows, err := s.db.bySID(kind, sid)
	if err != nil {
		return nil, err
	}
	var any *row
	for _, rw := range rows {
		if s.nameOf(rw.rec.Node, rw.rec.Home) == machine {
			return rw, nil
		}
		if any == nil || rw.rec.MovedTo == "" {
			any = rw
		}
	}
	if any != nil && len(rows) == 1 {
		return any, nil
	}
	return nil, wire.Errorf(wire.CodeNotFound, "no session %s", id)
}

// --- notifications ---

// watcher is one subscribed connection: notes coalesce per key.
type watcher struct {
	mu      sync.Mutex
	pending map[string]any
	methods map[string]string
	order   []string
	closed  bool
	wake    chan struct{}
}

func (w *watcher) push(key, method string, params any) {
	w.mu.Lock()
	if _, ok := w.pending[key]; !ok {
		w.order = append(w.order, key)
	}
	w.pending[key], w.methods[key] = params, method
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *watcher) close() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Watch sends sessions.changed / sessions.removed / sessions.indexing
// with send until done closes or send fails. It starts with the indexing
// state (the app queries sessions.search itself).
func (s *Service) Watch(done <-chan struct{}, send func(method string, params any) error) {
	w := &watcher{pending: map[string]any{}, methods: map[string]string{}, wake: make(chan struct{}, 1)}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	w.push("~indexing", wire.NoteSessionIndexing, s.indexing)
	s.watchers[w] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.watchers, w)
		s.mu.Unlock()
	}()
	for {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return
		}
		order, pending, methods := w.order, w.pending, w.methods
		w.order, w.pending, w.methods = nil, map[string]any{}, map[string]string{}
		w.mu.Unlock()
		for _, key := range order {
			if send(methods[key], pending[key]) != nil {
				return
			}
		}
		select {
		case <-w.wake:
		case <-done:
			return
		}
	}
}

func (s *Service) broadcast(key, method string, params any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for w := range s.watchers {
		w.push(key, method, params)
	}
}

// noteChange queues a sessions.changed for key (sent ≤ 250 ms later,
// coalesced) and wakes whoever waits for changes (replication hints).
func (s *Service) noteChange(key string, visible bool) {
	s.mu.Lock()
	if visible {
		s.notes[key] = true
	}
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

// Changes is closed at the next committed change.
func (s *Service) Changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// notifyWindow: entries active within it (or changed by a person:
// archive, delete, move, removal) are announced with sessions.changed;
// older ones only show in searches (the first full index announces
// thousands otherwise).
const notifyWindow = 3 * 24 * time.Hour

// visible: worth a sessions.changed (recent, or archived / deleted /
// moved / removed: a person did that).
func (s *Service) visible(r *Record) bool {
	return s.opt.Now().Sub(time.UnixMilli(r.Meta.LastActivity)) < notifyWindow || r.Archived || r.Deleted || r.MovedTo != "" || r.RemovedAt > 0
}

func (s *Service) notifier() {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
		s.mu.Lock()
		keys := s.notes
		s.notes = map[string]bool{}
		s.mu.Unlock()
		for key := range keys {
			rw, err := s.db.get(key)
			if err != nil || rw == nil {
				continue
			}
			sess := s.toWire(&rw.rec)
			if rw.rec.Deleted {
				s.broadcast("s:"+key, wire.NoteSessionRemoved, wire.Removed{ID: sess.ID})
				continue
			}
			s.broadcast("s:"+key, wire.NoteSessionChanged, wire.SessionChanged{Session: sess})
		}
	}
}

func (s *Service) setIndexing(done, total int) {
	s.mu.Lock()
	s.indexing = wire.SessionIndexing{Done: done, Total: total}
	s.mu.Unlock()
	s.broadcast("~indexing", wire.NoteSessionIndexing, wire.SessionIndexing{Done: done, Total: total})
}

// --- writes ---

// apply merges incoming records (from peers, or local edits) and reports
// the keys that changed.
func (s *Service) apply(recs []*Record, local bool) ([]string, error) {
	var changed []string
	err := s.db.write(func(tx sqlTx) error {
		changed = changed[:0]
		for _, in := range recs {
			key := in.Key()
			old, err := getTx(tx, key)
			if err != nil {
				return err
			}
			if !local {
				in.stamps(s.clock.observe)
			}
			if old == nil {
				if err := s.db.putTx(tx, nil, in); err != nil {
					return err
				}
				changed = append(changed, key)
				continue
			}
			cur := old.rec
			cur.Mirrors = cloneMirrors(old.rec.Mirrors)
			if !cur.merge(in) {
				continue
			}
			if err := s.db.putTx(tx, old, &cur); err != nil {
				return err
			}
			changed = append(changed, key)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, key := range changed {
		rw, _ := s.db.get(key)
		s.noteChange(key, local || rw != nil && s.visible(&rw.rec))
	}
	return changed, nil
}

func cloneMirrors(m map[string]MirrorMark) map[string]MirrorMark {
	if m == nil {
		return nil
	}
	out := make(map[string]MirrorMark, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// edit changes one record's person-made fields (archive, delete, move,
// removal, mirror marks) with a fresh stamp.
func (s *Service) edit(key string, fn func(r *Record, st Stamp) bool) (*Record, error) {
	var out *Record
	var changed bool
	err := s.db.write(func(tx sqlTx) error {
		old, err := getTx(tx, key)
		if err != nil {
			return err
		}
		if old == nil {
			return wire.Errorf(wire.CodeNotFound, "no session %s", key)
		}
		cur := old.rec
		cur.Mirrors = cloneMirrors(old.rec.Mirrors)
		if changed = fn(&cur, s.clock.tick()); !changed {
			out = &cur
			return nil
		}
		out = &cur
		return s.db.putTx(tx, old, &cur)
	})
	if err != nil {
		return nil, err
	}
	if changed {
		s.noteChange(key, true)
	}
	return out, nil
}

// updateLocal changes the meta of this Mac's record of a session.
func (s *Service) updateLocal(kind, sid string, fn func(r *Record) bool) {
	key := s.db.node + ":" + kind + ":" + sid
	var changed bool
	s.db.write(func(tx sqlTx) error {
		old, err := getTx(tx, key)
		if err != nil || old == nil {
			return err
		}
		cur := old.rec
		cur.Mirrors = cloneMirrors(old.rec.Mirrors)
		if changed = fn(&cur); !changed {
			return nil
		}
		cur.MetaS = s.clock.tick()
		return s.db.putTx(tx, old, &cur)
	})
	if changed {
		s.noteChange(key, true)
	}
}

var errClosed = errors.New("history is closed")

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && strings.Compare(v[j-1], v[j]) > 0; j-- {
			v[j-1], v[j] = v[j], v[j-1]
		}
	}
}
