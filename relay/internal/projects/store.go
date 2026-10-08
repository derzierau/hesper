// Package projects is hesperd's project registry (workspace model, step
// 1: data): projects (a folder with an identity: repository, monorepo
// package, folder, reference), groups (named ordered sets of projects),
// which project an agent works in (the deepest one containing its folder;
// worktrees map back to their repository; scratch outside every project),
// the projects.* and groups.* methods and their notifications,
// persistence ($HESPER_STATE_DIR/projects.json) and the replicated state
// the daemons of the owner's Macs exchange (crdt.go; carried by part R's
// links, internal/host and internal/remote).
package projects

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Options configure a Store.
type Options struct {
	// StateDir holds projects.json ("": in memory only).
	StateDir string
	// Machine is this Mac's short name (paths are shown per machine);
	// SetMachine sets it later.
	Machine string
	// Home: repositories at the home folder or above it are never made
	// projects by themselves (a dotfiles repository in ~ would swallow
	// every folder); default the user's home.
	Home string
	// GitEnv is git's environment (default: this process's, minus GIT_DIR
	// and friends).
	GitEnv []string
	Logf   func(format string, args ...any)
	// Now is the clock (tests).
	Now func() time.Time
}

const (
	recentLimit     = 30
	tombstoneMaxAge = 180 * 24 * time.Hour
	maxRecords      = 5000
	maxGroups       = 1000
)

var groupID = regexp.MustCompile(`^g-[a-z0-9-]{1,40}$`)

// Store is the project registry of this daemon.
type Store struct {
	opt  Options
	path string
	det  *detector

	mu       sync.Mutex
	node     string
	short    string
	clock    hlc
	projects map[string]*Record
	groups   map[string]*GroupRecord
	nodes    map[string]string // node → its own short name
	aliases  map[string]string // node → this Mac's name for that machine
	recent   map[string]wire.Project
	views    map[string]json.RawMessage // what watchers last got, "p:<id>" / "g:<id>"
	watchers map[*watcher]struct{}
	shared   chan struct{} // closed when the shared state changes

	agents    func() []wire.Agent
	forward   func(ctx context.Context, machine, method string, params any) (json.RawMessage, error)
	reproject func()

	saveCh    chan struct{}
	resolveCh chan struct{}
	done      chan struct{}
	wg        sync.WaitGroup
	wmu       sync.Mutex // one file write at a time
	closed    bool
}

type storeFile struct {
	Version  int               `json:"version"`
	Node     string            `json:"node"`
	Clock    Stamp             `json:"clock"`
	Projects []*Record         `json:"projects"`
	Groups   []*GroupRecord    `json:"groups"`
	Nodes    map[string]string `json:"nodes,omitempty"`
	Aliases  map[string]string `json:"aliases,omitempty"`
	Recent   []wire.Project    `json:"recent,omitempty"`
}

// Open loads dir/projects.json. The file of older daemons (a list of
// recent folders) becomes the recent folders.
func Open(opt Options) *Store {
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Home == "" {
		opt.Home, _ = os.UserHomeDir()
	}
	if opt.Home != "" {
		opt.Home = canonical(opt.Home)
	}
	s := &Store{opt: opt, det: newDetector(opt.GitEnv, opt.Now), short: opt.Machine,
		projects: map[string]*Record{}, groups: map[string]*GroupRecord{}, nodes: map[string]string{}, aliases: map[string]string{},
		recent: map[string]wire.Project{}, views: map[string]json.RawMessage{}, watchers: map[*watcher]struct{}{},
		shared: make(chan struct{}), saveCh: make(chan struct{}, 1), resolveCh: make(chan struct{}, 1), done: make(chan struct{})}
	if opt.StateDir != "" {
		s.path = filepath.Join(opt.StateDir, "projects.json")
		s.load()
	}
	if s.node == "" {
		s.node = "n-" + randomHex(8)
		s.scheduleSave()
	}
	s.clock.node, s.clock.now = s.node, opt.Now
	s.views = s.computeViewsLocked()
	s.wg.Add(2)
	go s.saver()
	go s.resolver()
	return s
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.opt.Logf("hesperd: projects.json: %v", err)
		}
		return
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		var legacy []wire.Project
		if json.Unmarshal(data, &legacy) == nil {
			for _, p := range legacy {
				if p.Path != "" {
					s.recent[p.Path] = wire.Project{Path: p.Path, Name: p.Name, LastUsed: p.LastUsed}
				}
			}
		}
		s.scheduleSave()
		return
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		os.Rename(s.path, s.path+".broken")
		s.opt.Logf("hesperd: projects.json: %v (moved to projects.json.broken)", err)
		return
	}
	s.node = f.Node
	s.clock.last = Stamp{T: f.Clock.T, C: f.Clock.C}
	cutoff := s.opt.Now().Add(-tombstoneMaxAge).UnixMilli()
	for _, r := range f.Projects {
		if r == nil || r.ID == "" || r.ID != ProjectID(r.Identity) {
			continue
		}
		if r.Deleted.V && r.Deleted.S.T < cutoff {
			continue
		}
		if r.Paths == nil {
			r.Paths = map[string]Field[string]{}
		}
		s.projects[r.ID] = r
	}
	for _, g := range f.Groups {
		if g == nil || !groupID.MatchString(g.ID) || g.Deleted.V && g.Deleted.S.T < cutoff {
			continue
		}
		s.groups[g.ID] = g
	}
	for k, v := range f.Nodes {
		s.nodes[k] = v
	}
	for k, v := range f.Aliases {
		s.aliases[k] = v
	}
	for _, p := range f.Recent {
		s.recent[p.Path] = p
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

// SetMachine sets this Mac's short name.
func (s *Store) SetMachine(short string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.short != short {
		s.short = short
		s.commitLocked(true, false)
	}
}

// SetAgents gives the store every agent (local and remote) for the
// scratch projects of projects.list. It is called without the store's
// lock held.
func (s *Store) SetAgents(fn func() []wire.Agent) {
	s.mu.Lock()
	s.agents = fn
	s.mu.Unlock()
}

// SetForward lets projects.promote with another machine run there (part
// R: a signed request through the channel).
func (s *Store) SetForward(fn func(ctx context.Context, machine, method string, params any) (json.RawMessage, error)) {
	s.mu.Lock()
	s.forward = fn
	s.mu.Unlock()
}

// SetReproject is called (debounced, from a goroutine of its own) when
// which project a folder belongs to may have changed: the registry
// re-resolves its agents.
func (s *Store) SetReproject(fn func()) {
	s.mu.Lock()
	s.reproject = fn
	s.mu.Unlock()
}

// Node is this daemon's node id (replication).
func (s *Store) Node() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.node
}

// Changes is closed at the next change of the shared state (sync).
func (s *Store) Changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shared
}

// commitLocked publishes a change: notifications, save, sync, re-resolve.
func (s *Store) commitLocked(shared, resolve bool) {
	s.emitLocked()
	s.scheduleSave()
	if shared {
		close(s.shared)
		s.shared = make(chan struct{})
	}
	if resolve {
		select {
		case s.resolveCh <- struct{}{}:
		default:
		}
	}
}

func (s *Store) scheduleSave() {
	select {
	case s.saveCh <- struct{}{}:
	default:
	}
}

func (s *Store) saver() {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			return
		case <-s.saveCh:
			time.Sleep(50 * time.Millisecond)
			s.write()
		}
	}
}

func (s *Store) resolver() {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			return
		case <-s.resolveCh:
			time.Sleep(50 * time.Millisecond)
			s.mu.Lock()
			fn := s.reproject
			s.mu.Unlock()
			if fn != nil {
				fn()
			}
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
	f := storeFile{Version: 2, Node: s.node, Clock: Stamp{T: s.clock.last.T, C: s.clock.last.C}, Nodes: s.nodes, Aliases: s.aliases}
	st := s.exportLocked()
	f.Projects, f.Groups = st.Projects, st.Groups
	for _, p := range s.recent {
		f.Recent = append(f.Recent, p)
	}
	sort.Slice(f.Recent, func(i, j int) bool { return f.Recent[i].LastUsed.After(f.Recent[j].LastUsed) })
	data, err := json.MarshalIndent(f, "", "  ")
	s.mu.Unlock()
	if err == nil {
		err = atomicWrite(s.path, append(data, '\n'), 0o600)
	}
	if err != nil {
		s.opt.Logf("hesperd: saving projects.json: %v", err)
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

// --- resolution ---

// autoAllowed: a repository at root may become a project by itself.
func (s *Store) autoAllowed(root string) bool {
	if root == "/" || root == "" {
		return false
	}
	if s.opt.Home != "" && within(s.opt.Home, root) {
		return false // the home folder or above
	}
	return true
}

// Resolve is the project an agent in dir belongs to: the deepest project
// containing it (a worktree counts as its repository's main checkout),
// else "scratch:<dir>". A repository (and the monorepo package dir is in)
// becomes a project when it is not one yet, unless it was removed.
// Detection is cached: a folder costs two git calls the first time.
func (s *Store) Resolve(dir string) string {
	dir = filepath.Clean(dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	real := canonical(dir)
	gi := s.det.gitOf(existing(real))
	var pkgs []wire.DetectedPackage
	if gi.OK {
		pkgs = s.det.packagesOf(gi.Top)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, changed := s.resolveLocked(real, gi, pkgs, true)
	if changed {
		s.commitLocked(true, true)
	}
	if id == "" {
		return wire.ScratchPrefix + dir
	}
	return id
}

// resolveLocked finds (and with register, makes) the project of a folder.
func (s *Store) resolveLocked(real string, gi gitInfo, pkgs []wire.DetectedPackage, register bool) (id string, changed bool) {
	logical := real
	var repo, pkg *Record
	var pkgInfo *wire.DetectedPackage
	if gi.OK {
		relp := rel(gi.Top, real)
		logical = filepath.Join(gi.Root, relp)
		pkgInfo = packageAt(pkgs, relp)
		if register && s.autoAllowed(gi.Root) {
			var ch bool
			repo, ch = s.ensureRepoLocked(gi, false)
			changed = changed || ch
			if repo != nil && pkgInfo != nil {
				pkg, ch = s.ensurePackageLocked(repo, gi.Root, pkgInfo.Path, pkgInfo.Name, wire.ProjectPackage, false)
				changed = changed || ch
			}
		} else {
			repo = s.repoRecordLocked(gi)
			if repo != nil && pkgInfo != nil {
				pkg = s.projects[ProjectID(packageIdentity(repo, pkgInfo.Path))]
			}
		}
	}
	best, bestLen := "", -1
	consider := func(r *Record, p string) {
		if r == nil || r.Deleted.V || p == "" {
			return
		}
		if len(p) > bestLen {
			best, bestLen = r.ID, len(p)
		}
	}
	for _, r := range s.projects {
		p := r.Paths[s.node].V
		if p != "" && (within(logical, p) || within(real, p)) {
			consider(r, p)
		}
	}
	if gi.OK {
		consider(repo, gi.Root)
		if pkgInfo != nil {
			consider(pkg, filepath.Join(gi.Root, pkgInfo.Path))
		}
	}
	return best, changed
}

func packageIdentity(repo *Record, relp string) wire.ProjectIdentity {
	return wire.ProjectIdentity{Remote: repo.Identity.Remote, Local: repo.Identity.Local, Package: relp}
}

// repoRecordLocked is the record of a repository (by remote, else by its
// folder here), nil when there is none.
func (s *Store) repoRecordLocked(gi gitInfo) *Record {
	if gi.Remote != "" {
		return s.projects[ProjectID(wire.ProjectIdentity{Remote: gi.Remote})]
	}
	return s.byLocalPathLocked(gi.Root, func(r *Record) bool { return r.Identity.Remote == "" && r.Identity.Package == "" })
}

func (s *Store) byLocalPathLocked(p string, ok func(*Record) bool) *Record {
	var found *Record
	for _, r := range s.projects {
		if r.Paths[s.node].V == p && ok(r) {
			if found == nil || !r.Deleted.V && found.Deleted.V {
				found = r
			}
		}
	}
	return found
}

// ensureRepoLocked makes the repository's project (revive: also when it
// was removed). nil: removed (and not revived).
func (s *Store) ensureRepoLocked(gi gitInfo, revive bool) (*Record, bool) {
	r := s.repoRecordLocked(gi)
	if r == nil {
		ident := wire.ProjectIdentity{Remote: gi.Remote}
		if ident.Remote == "" {
			ident.Local = newLocalIdentity()
		}
		r = &Record{ID: ProjectID(ident), Identity: ident, Name: Field[string]{V: repoName(gi.RawRemote, gi.Root)},
			Kind: Field[string]{V: wire.ProjectRepo}, Paths: map[string]Field[string]{}}
		if len(s.projects) >= maxRecords {
			return nil, false
		}
		s.projects[r.ID] = r
		s.setPathLocked(r, gi.Root, true)
		return r, true
	}
	changed := false
	if r.Deleted.V {
		if !revive {
			return nil, false
		}
		r.Deleted = Field[bool]{V: false, S: s.clock.tick()}
		changed = true
	}
	return r, s.setPathLocked(r, gi.Root, revive) || changed
}

// ensurePackageLocked makes a package's (or a repository folder's)
// project.
func (s *Store) ensurePackageLocked(repo *Record, root, relp, name, kind string, revive bool) (*Record, bool) {
	ident := packageIdentity(repo, relp)
	id := ProjectID(ident)
	r := s.projects[id]
	if r == nil {
		if len(s.projects) >= maxRecords {
			return nil, false
		}
		if name == "" {
			name = filepath.Base(relp)
		}
		r = &Record{ID: id, Identity: ident, ParentID: repo.ID, Name: Field[string]{V: name}, Kind: Field[string]{V: kind}, Paths: map[string]Field[string]{}}
		s.projects[id] = r
		s.setPathLocked(r, filepath.Join(root, relp), true)
		return r, true
	}
	changed := false
	if r.Deleted.V {
		if !revive {
			return nil, false
		}
		r.Deleted = Field[bool]{V: false, S: s.clock.tick()}
		changed = true
	}
	return r, s.setPathLocked(r, filepath.Join(root, relp), revive) || changed
}

// setPathLocked records the project's folder on this machine. A second
// clone of the same repository does not take over (force does), unless
// the recorded folder is gone.
func (s *Store) setPathLocked(r *Record, p string, force bool) bool {
	cur := r.Paths[s.node]
	if cur.V == p {
		return false
	}
	if cur.V != "" && !force {
		if _, err := os.Stat(cur.V); err == nil {
			return false
		}
	}
	r.Paths[s.node] = Field[string]{V: p, S: s.clock.tick()}
	return true
}

// Touch records that an agent started in path (projects.recent, the
// project's lastUsed). It never runs git: what Resolve found is reused.
func (s *Store) Touch(path string, at time.Time) {
	path = filepath.Clean(path)
	real := canonical(path)
	gi, ok := s.det.cached(real)
	if !ok {
		gi = gitInfo{}
	}
	var pkgs []wire.DetectedPackage
	if gi.OK {
		pkgs = s.det.cachedPackages(gi.Top)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, _ := s.resolveLocked(real, gi, pkgs, false)
	shared := false
	if r := s.projects[id]; r != nil && at.After(r.LastUsed) {
		r.LastUsed, shared = at, true
	}
	s.recent[path] = wire.Project{Path: path, Name: filepath.Base(path), LastUsed: at}
	if len(s.recent) > recentLimit {
		list := s.recentLocked()
		for _, p := range list[recentLimit:] {
			delete(s.recent, p.Path)
		}
	}
	s.commitLocked(shared, false)
}

func (s *Store) recentLocked() []wire.Project {
	list := make([]wire.Project, 0, len(s.recent))
	for _, p := range s.recent {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastUsed.After(list[j].LastUsed) })
	return list
}

// Recent is projects.recent: the projects with a folder on this machine
// (most recently used first), then the other folders agents started in.
func (s *Store) Recent() []wire.Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	var projects []wire.Project
	seen := map[string]bool{}
	for _, r := range s.projects {
		p := r.Paths[s.node].V
		if r.Deleted.V || p == "" {
			continue
		}
		projects = append(projects, wire.Project{Path: p, Name: r.Name.V, LastUsed: r.LastUsed, ProjectID: r.ID})
		seen[p] = true
	}
	sort.Slice(projects, func(i, j int) bool {
		if !projects[i].LastUsed.Equal(projects[j].LastUsed) {
			return projects[i].LastUsed.After(projects[j].LastUsed)
		}
		return projects[i].Name < projects[j].Name
	})
	out := append([]wire.Project{}, projects...)
	for _, p := range s.recentLocked() {
		if !seen[p.Path] {
			out = append(out, p)
		}
	}
	return out
}
