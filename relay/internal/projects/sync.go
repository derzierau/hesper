package projects

import (
	"path/filepath"
	"sort"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Export is the shared state to send to another machine's daemon.
func (s *Store) Export() *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exportLocked()
}

func (s *Store) exportLocked() *State {
	st := &State{Node: s.node, Short: s.short, Nodes: map[string]string{}, Projects: []*Record{}, Groups: []*GroupRecord{}}
	for k, v := range s.nodes {
		st.Nodes[k] = v
	}
	for _, r := range s.projects {
		st.Projects = append(st.Projects, r.clone())
	}
	for _, g := range s.groups {
		c := *g
		c.ProjectIDs.V = append([]string(nil), g.ProjectIDs.V...)
		st.Groups = append(st.Groups, &c)
	}
	sort.Slice(st.Projects, func(i, j int) bool { return st.Projects[i].ID < st.Projects[j].ID })
	sort.Slice(st.Groups, func(i, j int) bool { return st.Groups[i].ID < st.Groups[j].ID })
	return st
}

// Merge folds another daemon's state in (from: this Mac's name for the
// machine it came from, "" when unknown). Per field the newer write wins;
// tombstones stay. Projects whose id does not match their identity are
// dropped. Reports whether anything changed.
func (s *Store) Merge(st *State, from string) bool {
	if st == nil || st.Node == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.Node == s.node {
		return false
	}
	changed, paths := false, false
	for _, o := range st.Projects {
		if o == nil || o.ID == "" || o.ID != ProjectID(o.Identity) || !validRecord(o) {
			continue
		}
		o.stamps(s.clock.observe)
		r := s.projects[o.ID]
		if r == nil {
			if len(s.projects) >= maxRecords {
				continue
			}
			c := o.clone()
			s.projects[o.ID] = c
			changed, paths = true, true
			continue
		}
		c, p := r.merge(o)
		changed, paths = changed || c, paths || p
	}
	for _, o := range st.Groups {
		if o == nil || !groupID.MatchString(o.ID) {
			continue
		}
		o.stamps(s.clock.observe)
		g := s.groups[o.ID]
		if g == nil {
			if len(s.groups) >= maxGroups {
				continue
			}
			c := *o
			s.groups[o.ID] = &c
			changed = true
			continue
		}
		changed = g.merge(o) || changed
	}
	named := false
	for k, v := range st.Nodes {
		if k != s.node && k != st.Node && v != "" && s.nodes[k] == "" {
			s.nodes[k], named = v, true
		}
	}
	if st.Short != "" && s.nodes[st.Node] != st.Short {
		s.nodes[st.Node], named = st.Short, true
	}
	if from != "" && s.aliases[st.Node] != from {
		s.aliases[st.Node], named = from, true
	}
	if changed || named {
		s.commitLocked(changed, paths)
	}
	return changed
}

// validRecord: what a peer sends is sane (absolute paths, a known kind,
// bounded strings).
func validRecord(r *Record) bool {
	if len(r.Name.V) > 200 || r.Kind.V != "" && !validKind(r.Kind.V) && r.Kind.V != wire.ProjectScratch || r.Color.V != "" && !validColor(r.Color.V) {
		return false
	}
	if r.Scratch != nil && len(r.Scratch.Home.V) > 64 {
		return false
	}
	if len(r.Paths) > 64 {
		return false
	}
	for node, f := range r.Paths {
		if node == "" || len(node) > 64 || f.V != "" && (!filepath.IsAbs(f.V) || len(f.V) > 4096) {
			return false
		}
	}
	return true
}

var _ = wire.ProjectRepo
