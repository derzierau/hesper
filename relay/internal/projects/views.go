package projects

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// nameOf is a node's machine name: this Mac's short name, the name this
// Mac's controller gives that machine, else the name it gives itself.
func (s *Store) nameOf(node string) string {
	if node == s.node {
		return s.short
	}
	if n := s.aliases[node]; n != "" {
		return n
	}
	if n := s.nodes[node]; n != "" && n != s.short {
		return n
	}
	return node
}

// viewLocked is a project as the wire shows it.
func (s *Store) viewLocked(r *Record) wire.ProjectInfo {
	info := wire.ProjectInfo{ID: r.ID, Name: r.Name.V, Kind: r.Kind.V, Identity: r.Identity, ParentID: r.ParentID,
		Defaults: r.Defaults.V, LastUsed: r.LastUsed, Paths: map[string]string{}, Groups: []string{}}
	if r.Color.V != "" {
		info.Color, info.ColorSet = r.Color.V, true
	} else {
		info.Color = AutoColor(r.ID)
	}
	nodes := make([]string, 0, len(r.Paths))
	for node := range r.Paths {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes) // stable when two nodes share a name
	for _, node := range nodes {
		if p := r.Paths[node].V; p != "" {
			name := s.nameOf(node)
			if _, taken := info.Paths[name]; taken && node != s.node {
				name = node
			}
			info.Paths[name] = p
		}
	}
	for _, g := range s.groups {
		if g.Deleted.V {
			continue
		}
		for _, id := range g.ProjectIDs.V {
			if id == r.ID {
				info.Groups = append(info.Groups, g.ID)
				break
			}
		}
	}
	sort.Strings(info.Groups)
	if r.Identity.Package == "" {
		if p := r.Paths[s.node].V; p != "" {
			info.DetectedPackages = s.det.cachedPackages(p)
		}
	}
	return info
}

func (s *Store) groupViewLocked(g *GroupRecord) wire.Group {
	out := wire.Group{ID: g.ID, Name: g.Name.V, Order: g.Order.V, Color: g.Color.V, ProjectIDs: []string{}}
	for _, id := range g.ProjectIDs.V {
		if r := s.projects[id]; r != nil && r.Deleted.V {
			continue
		}
		out.ProjectIDs = append(out.ProjectIDs, id)
	}
	return out
}

// computeViewsLocked is every live project and group as notifications
// carry them.
func (s *Store) computeViewsLocked() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, r := range s.projects {
		if !r.Deleted.V {
			out["p:"+r.ID], _ = json.Marshal(s.viewLocked(r))
		}
	}
	for _, g := range s.groups {
		if !g.Deleted.V {
			out["g:"+g.ID], _ = json.Marshal(s.groupViewLocked(g))
		}
	}
	return out
}

// emitLocked tells watchers what changed since the last time.
func (s *Store) emitLocked() {
	views := s.computeViewsLocked()
	for key, v := range views {
		if old, ok := s.views[key]; ok && bytes.Equal(old, v) {
			continue
		}
		for w := range s.watchers {
			w.push(key, v)
		}
	}
	for key := range s.views {
		if _, ok := views[key]; !ok {
			for w := range s.watchers {
				w.push(key, nil)
			}
		}
	}
	s.views = views
}

// watcher is one subscribed connection: changes coalesce per key (the
// latest wins), like agents' subscribers.
type watcher struct {
	mu      sync.Mutex
	pending map[string]json.RawMessage // nil: removed
	order   []string
	closed  bool
	wake    chan struct{}
}

func (w *watcher) push(key string, v json.RawMessage) {
	w.mu.Lock()
	if _, ok := w.pending[key]; !ok {
		w.order = append(w.order, key)
	}
	w.pending[key] = v
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

// Watch sends projects.changed / projects.removed / groups.changed /
// groups.removed with send until done closes or send fails: first every
// project, then every group, then the changes.
func (s *Store) Watch(done <-chan struct{}, send func(method string, params any) error) {
	w := &watcher{pending: map[string]json.RawMessage{}, wake: make(chan struct{}, 1)}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	keys := make([]string, 0, len(s.views))
	for k := range s.views {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		// projects ("p:") before groups ("g:")
		pi, pj := strings.HasPrefix(keys[i], "p:"), strings.HasPrefix(keys[j], "p:")
		if pi != pj {
			return pi
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		w.push(k, s.views[k])
	}
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
		order, pending := w.order, w.pending
		w.order, w.pending = nil, map[string]json.RawMessage{}
		w.mu.Unlock()
		for _, key := range order {
			v := pending[key]
			kind, id := key[:1], key[2:]
			var method string
			var params any
			switch {
			case kind == "p" && v != nil:
				method, params = wire.NoteProjectChanged, json.RawMessage(`{"project":`+string(v)+`}`)
			case kind == "p":
				method, params = wire.NoteProjectRemoved, wire.Removed{ID: id}
			case v != nil:
				method, params = wire.NoteGroupChanged, json.RawMessage(`{"group":`+string(v)+`}`)
			default:
				method, params = wire.NoteGroupRemoved, wire.Removed{ID: id}
			}
			if send(method, params) != nil {
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

// scratchViews are the scratch projects of the agents there are: one per
// folder outside every project.
func scratchViews(agents []wire.Agent) []wire.ProjectInfo {
	byID := map[string]*wire.ProjectInfo{}
	var order []string
	for _, a := range agents {
		if !strings.HasPrefix(a.ProjectID, wire.ScratchPrefix) {
			continue
		}
		folder := strings.TrimPrefix(a.ProjectID, wire.ScratchPrefix)
		p := byID[a.ProjectID]
		if p == nil {
			p = &wire.ProjectInfo{ID: a.ProjectID, Name: filepath.Base(folder), Color: AutoColor(a.ProjectID), Kind: wire.ProjectScratch,
				Paths: map[string]string{}, Groups: []string{}}
			byID[a.ProjectID] = p
			order = append(order, a.ProjectID)
		}
		p.Paths[a.Machine] = folder
		if a.Created.After(p.LastUsed) {
			p.LastUsed = a.Created
		}
	}
	out := make([]wire.ProjectInfo, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}
