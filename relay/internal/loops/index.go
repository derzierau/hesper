package loops

import (
	"sort"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The index: what the store derives from its loops to answer LoopOf and
// to know which views a change touches, kept per loop as the records
// change (commitLocked reindexes the loops it commits), never rebuilt
// whole on the hook path. Not persisted, not synced.

// loopMeta is what the index keeps of one loop.
type loopMeta struct {
	members   map[string]stamp // member refs (agents, sessions) → the newest member link's ref stamp
	agents    []string         // refs of its agent links, any relation (views join their live state)
	blockedBy []string         // the loops of its blockedBy links (as written, merges not followed)
	milestone string
	merged    bool // mergedInto set
	updated   time.Time
}

type index struct {
	loops   map[string]*loopMeta
	of      map[string]string           // ref → loop: the newest member link wins
	members map[string]map[string]stamp // ref → loop → its newest member link's stamp
	agents  map[string]map[string]bool  // agent ref → the loops linking it
}

// indexLocked is the index, built on first use.
func (s *Store) indexLocked() *index {
	if s.idx == nil {
		s.idx = &index{loops: map[string]*loopMeta{}, of: map[string]string{}, members: map[string]map[string]stamp{},
			agents: map[string]map[string]bool{}}
		for id := range s.loops {
			s.reindexLocked(id)
		}
	}
	return s.idx
}

func metaOf(l *LoopRecord) *loopMeta {
	m := &loopMeta{members: map[string]stamp{}, milestone: l.Milestone.V, merged: l.MergedInto.V != "", updated: updated(l.stamps)}
	seen := map[string]bool{}
	for _, k := range l.Links {
		if k.Deleted.V {
			continue
		}
		if member(k) {
			if at, ok := m.members[k.Ref.V]; !ok || at.Less(k.Ref.S) {
				m.members[k.Ref.V] = k.Ref.S
			}
		}
		switch {
		case k.Kind.V == wire.LinkAgent && !seen[k.Ref.V]:
			seen[k.Ref.V] = true
			m.agents = append(m.agents, k.Ref.V)
		case k.Kind.V == wire.LinkLoop && k.Rel.V == wire.RelBlockedBy:
			m.blockedBy = append(m.blockedBy, k.Ref.V)
		}
	}
	return m
}

// reindexLocked brings loop id's entries up to date (after a change of
// its record). It returns the loop's milestone before the change.
func (s *Store) reindexLocked(id string) (oldMilestone string) {
	x := s.idx
	old := x.loops[id]
	cur := &loopMeta{}
	if l := s.loops[id]; l != nil {
		cur = metaOf(l)
		x.loops[id] = cur
	} else {
		delete(x.loops, id)
	}
	if old == nil {
		old = &loopMeta{}
	}
	for ref := range old.members {
		if _, still := cur.members[ref]; !still {
			delete(x.members[ref], id)
			x.pick(ref)
		}
	}
	for ref, at := range cur.members {
		if o, ok := old.members[ref]; ok && o == at {
			continue
		}
		if x.members[ref] == nil {
			x.members[ref] = map[string]stamp{}
		}
		x.members[ref][id] = at
		x.pick(ref)
	}
	for _, ref := range old.agents {
		if set := x.agents[ref]; set != nil {
			delete(set, id)
			if len(set) == 0 {
				delete(x.agents, ref)
			}
		}
	}
	for _, ref := range cur.agents {
		if x.agents[ref] == nil {
			x.agents[ref] = map[string]bool{}
		}
		x.agents[ref][id] = true
	}
	return old.milestone
}

// pick sets of[ref] to the loop of ref's newest member link (ties: the
// lowest id, the same on every Mac).
func (x *index) pick(ref string) {
	best, at := "", stamp{}
	for id, st := range x.members[ref] {
		if best == "" || at.Less(st) || at == st && id < best {
			best, at = id, st
		}
	}
	if best == "" {
		delete(x.members, ref)
		delete(x.of, ref)
		return
	}
	x.of[ref] = best
}

// listedLocked are the loops lists and watchers show: not merged, the
// newest maxViewLoops (by their last change), in no order.
func (s *Store) listedLocked() []string {
	x := s.indexLocked()
	out := make([]string, 0, len(x.loops))
	for id, m := range x.loops {
		if !m.merged {
			out = append(out, id)
		}
	}
	if len(out) <= maxViewLoops {
		return out // every one: no order needed
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := x.loops[out[i]].updated, x.loops[out[j]].updated
		if !a.Equal(b) {
			return a.After(b)
		}
		return out[i] < out[j]
	})
	return out[:maxViewLoops]
}
