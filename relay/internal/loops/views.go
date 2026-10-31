package loops

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Views: loops, goals and milestones as the wire shows them. Building one
// joins the agent list in (live state), so it never runs with the store's
// lock held: snapshot the records, unlock, list the agents, build. Caps
// (64 links, 100 items a loop, 2,000 loops) apply here, never at merge.

// snapshot is what a view build reads: copies of the records.
type snapshot struct {
	node, machine string
	loops         map[string]*LoopRecord
	goals         map[string]*GoalRecord
	milestones    map[string]*MilestoneRecord
	settings      wire.LoopSettings
	agents        func() []wire.Agent
	nodeOf        func(string) string
	machineOf     func(string) string
	now           time.Time
}

func (s *Store) snapshotLocked() *snapshot {
	sn := &snapshot{node: s.node, machine: s.opt.Machine, loops: make(map[string]*LoopRecord, len(s.loops)),
		goals: make(map[string]*GoalRecord, len(s.goals)), milestones: make(map[string]*MilestoneRecord, len(s.milestones)),
		settings: settingsView(&s.settings), agents: s.agents, nodeOf: s.nodeOf, machineOf: s.machineOf, now: s.opt.Now()}
	for id, l := range s.loops {
		sn.loops[id] = l.clone()
	}
	for id, g := range s.goals {
		c := *g
		sn.goals[id] = &c
	}
	for id, m := range s.milestones {
		sn.milestones[id] = m.clone()
	}
	return sn
}

func (s *Store) snapshot() *snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// joined is a snapshot with the live agents, keyed by ref.
type joined struct {
	*snapshot
	live map[string]wire.Agent
}

func (sn *snapshot) join() *joined {
	j := &joined{snapshot: sn, live: map[string]wire.Agent{}}
	if sn.agents == nil {
		return j
	}
	for _, a := range sn.agents() {
		if ref := sn.refOf(a.ID); ref != "" {
			j.live[ref] = a
		}
	}
	return j
}

// refOf is the ref of an agent id as this Mac names it ("<machine>/<local>"
// for another Mac's agent, "<local>" for its own).
func (sn *snapshot) refOf(id string) string {
	return refOf(id, sn.node, sn.nodeOf)
}

func refOf(id, own string, nodeOf func(string) string) string {
	if id == "" {
		return ""
	}
	machine, local, remote := strings.Cut(id, "/")
	if !remote {
		return own + "/" + id
	}
	if nodeOf == nil {
		return ""
	}
	node := nodeOf(machine)
	if node == "" {
		return ""
	}
	return node + "/" + local
}

// nameOf is a node's machine name as this Mac calls it.
func (sn *snapshot) nameOf(node string) string {
	if node == sn.node {
		return sn.machine
	}
	if sn.machineOf != nil {
		if n := sn.machineOf(node); n != "" {
			return n
		}
	}
	return node
}

// agentID is the id this Mac gives the agent of a ref (live or not).
func (sn *snapshot) agentID(ref string) string {
	node, local, ok := strings.Cut(ref, "/")
	if !ok {
		return ""
	}
	if node == sn.node {
		return local
	}
	return sn.nameOf(node) + "/" + local
}

func settingsView(r *SettingsRecord) wire.LoopSettings {
	out := wire.LoopSettings{LoopLimit: r.LoopLimit.V, StallDays: r.StallDays.V, EveningAt: r.EveningAt.V, MaxWakesPerDay: r.MaxWakesPerDay.V}
	if out.LoopLimit <= 0 {
		out.LoopLimit = 5
	}
	if out.StallDays <= 0 {
		out.StallDays = 7
	}
	if out.EveningAt == "" {
		out.EveningAt = "18:00"
	}
	if out.MaxWakesPerDay <= 0 {
		out.MaxWakesPerDay = 40
	}
	return out
}

func itemView(id string, it *ItemRecord) wire.LoopItem {
	return wire.LoopItem{ID: id, Text: it.Text.V, Owner: it.Owner.V, State: it.State.V, Kind: it.Kind.V, Order: it.Order.V,
		After: append([]string(nil), it.After.V...), Draft: it.Draft.V, Source: it.Source.V, Check: it.Check.V,
		Created: it.Created.V, Done: it.Done.V}
}

// itemsView are the live items, sorted (order, then created), capped:
// open items first.
func itemsView(items map[string]*ItemRecord) []wire.LoopItem {
	out := []wire.LoopItem{}
	for id, it := range items {
		if !it.Deleted.V {
			out = append(out, itemView(id, it))
		}
	}
	sortItems(out)
	if len(out) > maxViewItems {
		var open, rest []wire.LoopItem
		for _, it := range out {
			if it.State == wire.ItemOpen {
				open = append(open, it)
			} else {
				rest = append(rest, it)
			}
		}
		sort.SliceStable(rest, func(i, j int) bool { return rest[i].Done.After(rest[j].Done) })
		out = append(open, rest...)[:maxViewItems]
		sortItems(out)
	}
	return out
}

func sortItems(items []wire.LoopItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		if !a.Created.Equal(b.Created) {
			return a.Created.Before(b.Created)
		}
		return a.ID < b.ID
	})
}

func (j *joined) linkView(k *LinkRecord) wire.LoopLink {
	v := wire.LoopLink{Kind: k.Kind.V, Ref: k.Ref.V, Rel: k.Rel.V, From: k.From.V, Label: k.Label.V, Ending: k.Ending.V,
		Summary: k.Summary.V, ClosedReason: k.ClosedReason.V, Closed: k.Closed.V, Result: k.Result.V, Added: k.Added.V}
	if k.Kind.V == wire.LinkAgent {
		if node, _, ok := strings.Cut(k.Ref.V, "/"); ok {
			v.Machine = j.nameOf(node)
		}
		if a, ok := j.live[k.Ref.V]; ok {
			v.Agent, v.State = a.ID, a.State
			if a.Name != "" {
				v.Label = a.Name
			}
			if v.Ending == "" {
				v.Ending = a.Ending
			}
		}
	}
	return v
}

// loopView is a loop as the wire shows it (rules.go fills who acts
// next).
func (j *joined) loopView(l *LoopRecord) wire.Loop {
	v := wire.Loop{ID: l.ID, Title: l.Title.V, Goal: l.Goal.V, DoneWhen: l.DoneWhen.V, Status: l.Status.V, NextText: l.NextText.V,
		Milestone: l.Milestone.V, ParkedUntil: l.ParkedUntil.V, MergedInto: l.MergedInto.V, Created: l.Created.V,
		Updated: updated(l.stamps), Closed: l.Closed.V, Items: itemsView(l.Items), Links: []wire.LoopLink{}}
	if v.Status == "" {
		v.Status = wire.LoopOpen
	}
	var links []wire.LoopLink
	for _, k := range l.Links {
		if !k.Deleted.V {
			links = append(links, j.linkView(k))
		}
	}
	sort.SliceStable(links, func(a, b int) bool {
		if !links[a].Added.Equal(links[b].Added) {
			return links[a].Added.Before(links[b].Added)
		}
		return links[a].Ref < links[b].Ref
	})
	if len(links) > maxViewLinks {
		// Live agents first, then the newest.
		keep := make([]bool, len(links))
		n := 0
		for i, k := range links {
			if k.State != "" && n < maxViewLinks {
				keep[i], n = true, n+1
			}
		}
		for i := len(links) - 1; i >= 0 && n < maxViewLinks; i-- {
			if !keep[i] {
				keep[i], n = true, n+1
			}
		}
		capped := links[:0:0]
		for i, k := range links {
			if keep[i] {
				capped = append(capped, k)
			}
		}
		links = capped
	}
	if links != nil {
		v.Links = links
	}
	return v
}

func goalView(g *GoalRecord) wire.Goal {
	return wire.Goal{ID: g.ID, Scope: g.Scope.V, Title: g.Title.V, Order: g.Order.V, Status: g.Status.V}
}

func milestoneView(m *MilestoneRecord) wire.Milestone {
	v := wire.Milestone{ID: m.ID, Scope: m.Scope.V, Goal: m.Goal.V, Title: m.Title.V, Status: m.Status.V, Order: m.Order.V,
		Due: m.Due.V, AutoFile: m.AutoFile.V, Items: itemsView(m.Items), Loops: []string{}, Created: m.Created.V, Updated: updated(m.stamps)}
	if v.Status == "" {
		v.Status = wire.LoopOpen
	}
	return v
}

// built is every view of a snapshot.
type built struct {
	loops      map[string]wire.Loop // merged loops included
	goals      map[string]wire.Goal
	milestones map[string]wire.Milestone
}

// build makes every view (no lock held: it lists the agents).
func (sn *snapshot) build() *built {
	j := sn.join()
	b := &built{loops: map[string]wire.Loop{}, goals: map[string]wire.Goal{}, milestones: map[string]wire.Milestone{}}
	for id, l := range sn.loops {
		b.loops[id] = j.loopView(l)
	}
	for id, g := range sn.goals {
		if !g.Deleted.V {
			b.goals[id] = goalView(g)
		}
	}
	for id, m := range sn.milestones {
		if !m.Deleted.V {
			b.milestones[id] = milestoneView(m)
		}
	}
	return b
}

// listed are the loops a list shows: not merged, the newest 2,000.
func (b *built) listed() []wire.Loop {
	out := make([]wire.Loop, 0, len(b.loops))
	for _, l := range b.loops {
		if l.MergedInto == "" {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > maxViewLoops {
		out = out[:maxViewLoops]
	}
	return out
}

// notes are the views watchers get, by key ("l:", "g:", "m:" + id).
func (b *built) notes() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, l := range b.listed() {
		out["l:"+l.ID], _ = json.Marshal(wire.LoopChanged{Loop: l})
	}
	for id, g := range b.goals {
		out["g:"+id], _ = json.Marshal(wire.GoalChanged{Goal: &g})
	}
	for id, m := range b.milestones {
		out["m:"+id], _ = json.Marshal(wire.GoalChanged{Milestone: &m})
	}
	return out
}

// viewer rebuilds the views after changes (debounced) and tells watchers
// what changed.
func (s *Store) viewer() {
	defer s.wg.Done()
	for {
		select {
		case <-s.done:
			return
		case <-s.viewsCh:
		}
		select {
		case <-s.done:
			return
		case <-time.After(viewsDebounce):
		}
		s.emit()
	}
}

func (s *Store) emit() {
	s.vmu.Lock()
	defer s.vmu.Unlock()
	views := s.snapshot().build().notes()
	s.mu.Lock()
	defer s.mu.Unlock()
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
// latest wins).
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

// Watch sends loops.changed / loops.removed / goals.changed /
// goals.removed with send until done closes or send fails: first a
// loops.changed for every open and parked loop, a goals.changed for every
// goal and milestone, then the changes.
func (s *Store) Watch(done <-chan struct{}, send func(method string, params any) error) {
	w := &watcher{pending: map[string]json.RawMessage{}, wake: make(chan struct{}, 1)}
	s.vmu.Lock() // no emit between the first views and registering
	if len(s.views) == 0 {
		views := s.snapshot().build().notes()
		s.mu.Lock()
		s.views = views
		s.mu.Unlock()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.vmu.Unlock()
		return
	}
	keys := make([]string, 0, len(s.views))
	for k, v := range s.views {
		if k[0] != 'l' || openLoopNote(v) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return rank(keys[i][0]) < rank(keys[j][0]) // loops, goals, milestones
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		w.push(k, s.views[k])
	}
	s.watchers[w] = struct{}{}
	s.mu.Unlock()
	s.vmu.Unlock()
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
			method, params := noteOf(key, v)
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

func rank(c byte) int {
	switch c {
	case 'l':
		return 0
	case 'g':
		return 1
	}
	return 2
}

func openLoopNote(v json.RawMessage) bool {
	var n struct {
		Loop struct {
			Status string `json:"status"`
		} `json:"loop"`
	}
	json.Unmarshal(v, &n)
	return n.Loop.Status == wire.LoopOpen || n.Loop.Status == wire.LoopParked
}

func noteOf(key string, v json.RawMessage) (string, any) {
	id := key[2:]
	switch {
	case key[0] == 'l' && v != nil:
		return wire.NoteLoopChanged, v
	case key[0] == 'l':
		return wire.NoteLoopRemoved, wire.Removed{ID: id}
	case v != nil:
		return wire.NoteGoalChanged, v
	}
	return wire.NoteGoalRemoved, wire.Removed{ID: id}
}
