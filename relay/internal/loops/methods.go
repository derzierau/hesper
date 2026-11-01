package loops

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The loops.* and goals.* methods of the local socket. Who may call what
// beyond the rule below (person-only methods and status changes, what
// the Steward may do at its level) is the dispatcher's
// (internal/agents/loopscall.go); the store enforces one rule itself: an
// agent that is not a service may read everything but change only the
// items and links of its own loop.

var _ agents.Loops = (*Store)(nil)

// agentWrites are the methods an ordinary agent may call for its own
// loop.
var agentWrites = map[string]bool{wire.MethodLoopsItem: true, wire.MethodLoopsLink: true}

var reads = map[string]bool{wire.MethodLoopsList: true, wire.MethodLoopsGet: true, wire.MethodLoopsSettingsGet: true, wire.MethodGoalsList: true}

// Call runs a loops.* or goals.* method for caller.
func (s *Store) Call(method string, params json.RawMessage, caller agents.Caller) (result any, err error, handled bool) {
	if !strings.HasPrefix(method, "loops.") && !strings.HasPrefix(method, "goals.") {
		return nil, nil, false
	}
	decode := func(v any) error {
		if len(params) == 0 || string(params) == "null" {
			params = []byte("{}")
		}
		if err := json.Unmarshal(params, v); err != nil {
			return wire.Errorf(wire.CodeInvalid, "invalid params: %v", err)
		}
		return nil
	}
	if caller.Agent != "" && caller.Service == "" && !reads[method] {
		if !agentWrites[method] {
			return nil, wire.Errorf(wire.CodeForbidden, "agent %s may not call %s: agents change only the items and links of their own loop", caller.Agent, method), true
		}
		var p struct {
			ID string `json:"id"`
		}
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		own := s.callerLoop(caller)
		s.mu.Lock()
		target := s.resolveLocked(p.ID)
		s.mu.Unlock()
		if own == "" || target != own {
			return nil, wire.Errorf(wire.CodeForbidden, "agent %s may change only its own loop (%s)", caller.Agent, orNone(own)), true
		}
	}
	switch method {
	case wire.MethodLoopsList:
		var p wire.LoopsListParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		return s.List(p), nil, true
	case wire.MethodLoopsGet:
		var p wire.IDParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.Get(p.ID)
		return res, err, true
	case wire.MethodLoopsSave:
		var p wire.LoopSaveParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.Save(p)
		if err == nil && p.ID == "" {
			s.loopCreated(res.ID, caller.Service) // the Steward's own: no wake
		}
		return res, err, true
	case wire.MethodLoopsLink:
		var p wire.LoopLinkParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.Link(p)
		return res, err, true
	case wire.MethodLoopsItem:
		var p wire.LoopItemParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.Item(p)
		return res, err, true
	case wire.MethodLoopsMerge:
		var p wire.LoopMergeParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.MergeLoops(p)
		return res, err, true
	case wire.MethodLoopsSplit:
		var p wire.LoopSplitParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.Split(p)
		if err == nil {
			s.loopCreated(res.ID, caller.Service)
		}
		return res, err, true
	case wire.MethodLoopsSettingsGet:
		return s.Settings(), nil, true
	case wire.MethodLoopsSettingsSet:
		var p wire.LoopSettings
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.SetSettings(p)
		return res, err, true
	case wire.MethodGoalsList:
		var p wire.GoalsListParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		return s.Goals(p.Scope), nil, true
	case wire.MethodGoalsSave:
		var p wire.GoalSaveParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		if m := p.Milestone; caller.Agent != "" && m != nil && m.ID == "" && m.Status == nil {
			status := wire.MilestoneProposed // an agent proposes; a person accepts (goals.accept)
			m.Status = &status
		}
		res, err := s.SaveGoal(p)
		return res, err, true
	case wire.MethodGoalsRemove:
		var p wire.IDParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		return struct{}{}, s.RemoveGoal(p.ID), true
	case wire.MethodGoalsItem:
		var p wire.LoopItemParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.MilestoneItem(p)
		return res, err, true
	case wire.MethodGoalsFile:
		var p wire.GoalFileParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.File(p)
		return res, err, true
	case wire.MethodGoalsAccept:
		var p wire.GoalAcceptParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.Accept(p)
		return res, err, true
	}
	return nil, wire.Errorf(wire.CodeInvalid, "unknown method %s", method), true
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// callerLoop is the loop an agent caller works in: Caller.Loop, else the
// loop of its ref or id.
func (s *Store) callerLoop(c agents.Caller) string {
	ref := c.Ref
	if ref == "" {
		ref = s.refOf(c.Agent)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Loop != "" {
		return s.resolveLocked(c.Loop)
	}
	return s.loopOfLocked(ref)
}

// refOf is the ref of an agent id as this Mac names it (no lock held:
// it may ask the names).
func (s *Store) refOf(id string) string {
	s.mu.Lock()
	node, nodeOf := s.node, s.nodeOf
	s.mu.Unlock()
	return refOf(id, node, nodeOf)
}

// --- loops ---

// List is loops.list.
func (s *Store) List(p wire.LoopsListParams) []wire.Loop {
	var only string
	if p.Agent != "" {
		if only = s.LoopOf(s.refOf(p.Agent)); only == "" {
			return []wire.Loop{}
		}
	}
	out := []wire.Loop{}
	s.mu.Lock()
	sn := s.snapshotLocked(s.listedLocked())
	s.mu.Unlock()
	for _, l := range sn.build().listed() {
		switch {
		case only != "" && l.ID != only:
		case p.Status == "" && !(l.Status == wire.LoopOpen || l.Status == wire.LoopParked):
		case p.Status != "" && p.Status != "all" && l.Status != p.Status:
		case p.Milestone != "" && l.Milestone != p.Milestone:
		case p.Project != "" && !contains(l.Projects, p.Project):
		default:
			out = append(out, l)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Get is loops.get (merged loops too).
func (s *Store) Get(id string) (wire.Loop, error) {
	l, ok := s.snapshotOf(id).build().loops[id]
	if !ok {
		return wire.Loop{}, wire.Errorf(wire.CodeNotFound, "no loop %s", id)
	}
	return l, nil
}

// loopLocked is the loop of id, merges followed.
func (s *Store) loopLocked(id string) (*LoopRecord, error) {
	if r := s.resolveLocked(id); r != "" {
		return s.loops[r], nil
	}
	return nil, wire.Errorf(wire.CodeNotFound, "no loop %s", id)
}

var loopStatuses = []string{wire.LoopOpen, wire.LoopParked, wire.LoopDone, wire.LoopDropped}

// Save is loops.save: a new loop (no ID) or the fields given.
func (s *Store) Save(p wire.LoopSaveParams) (wire.Loop, error) {
	if p.Status != nil && !oneOf(*p.Status, loopStatuses...) {
		return wire.Loop{}, wire.Errorf(wire.CodeInvalid, "unknown status %q", *p.Status)
	}
	for _, v := range []*string{p.Title, p.Goal, p.DoneWhen, p.NextText} {
		if v != nil && len(*v) > maxText {
			return wire.Loop{}, wire.Errorf(wire.CodeInvalid, "text too long")
		}
	}
	s.mu.Lock()
	if p.Milestone != nil && *p.Milestone != "" {
		if m := s.milestones[*p.Milestone]; m == nil || m.Deleted.V {
			s.mu.Unlock()
			return wire.Loop{}, wire.Errorf(wire.CodeNotFound, "no milestone %s", *p.Milestone)
		}
	}
	var l *LoopRecord
	if p.ID == "" {
		title := "Untitled loop"
		if p.Title != nil && strings.TrimSpace(*p.Title) != "" {
			title = *p.Title
		}
		l = s.newLoopLocked(title, "")
	} else {
		var err error
		if l, err = s.loopLocked(p.ID); err != nil {
			s.mu.Unlock()
			return wire.Loop{}, err
		}
		if p.Title != nil && strings.TrimSpace(*p.Title) != "" {
			l.Title.Set(*p.Title, s.clock.Tick())
		}
	}
	set := func(f *field[string], v *string) {
		if v != nil {
			f.Set(*v, s.clock.Tick())
		}
	}
	set(&l.Goal, p.Goal)
	set(&l.DoneWhen, p.DoneWhen)
	set(&l.NextText, p.NextText)
	set(&l.Milestone, p.Milestone)
	if p.ParkedUntil != nil {
		l.ParkedUntil.Set(p.ParkedUntil.UTC(), s.clock.Tick())
	}
	if p.Status != nil && l.Status.Set(*p.Status, s.clock.Tick()) {
		closed := time.Time{}
		if *p.Status == wire.LoopDone || *p.Status == wire.LoopDropped {
			closed = s.opt.Now().UTC()
		}
		l.Closed.Set(closed, s.clock.Tick())
	}
	id := l.ID
	s.commitLocked("l:" + id)
	s.mu.Unlock()
	return s.Get(id)
}

func defaultRel(kind string) string {
	switch kind {
	case wire.LinkProject:
		return wire.RelTouches
	case wire.LinkLoop:
		return wire.RelBlockedBy
	}
	return wire.RelMember
}

// Link is loops.link: add or remove a link. An agent link may name the
// agent by its id here (Link.Agent).
func (s *Store) Link(p wire.LoopLinkParams) (wire.Loop, error) {
	v := p.Link
	if v.Kind == wire.LinkAgent && v.Ref == "" && v.Agent != "" {
		if v.Ref = s.refOf(v.Agent); v.Ref == "" {
			return wire.Loop{}, wire.Errorf(wire.CodeNotFound, "no machine for agent %s", v.Agent)
		}
	}
	if v.Rel == "" {
		v.Rel = defaultRel(v.Kind)
	}
	k := &LinkRecord{Kind: field[string]{V: v.Kind}, Rel: field[string]{V: v.Rel}, Ref: field[string]{V: v.Ref}, From: field[string]{V: v.From},
		Label: field[string]{V: v.Label}}
	if strings.TrimSpace(v.Ref) == "" || !validLink(k) {
		return wire.Loop{}, wire.Errorf(wire.CodeInvalid, "a link needs a known kind and rel and a ref")
	}
	s.mu.Lock()
	l, err := s.loopLocked(p.ID)
	if err != nil {
		s.mu.Unlock()
		return wire.Loop{}, err
	}
	if v.Kind == wire.LinkLoop {
		if target := s.resolveLocked(v.Ref); target == "" || target == l.ID {
			s.mu.Unlock()
			return wire.Loop{}, wire.Errorf(wire.CodeInvalid, "loop %s cannot link loop %s", l.ID, v.Ref)
		}
	}
	var changed bool
	var others []string // loops the agent leaves (newest membership wins)
	if p.Remove {
		rel := p.Link.Rel // empty: every relation to the ref
		changed = s.removeLinksLocked(l, v.Kind, v.Ref, rel)
		if !changed {
			s.mu.Unlock()
			return wire.Loop{}, wire.Errorf(wire.CodeNotFound, "loop %s has no such link", l.ID)
		}
	} else {
		var k *LinkRecord
		k, changed = s.addLinkLocked(l, v)
		if member(k) {
			others = s.leaveOthersLocked(l, v.Ref)
		}
	}
	id := l.ID
	if changed || len(others) > 0 {
		s.commitLocked(append([]string{"l:" + id}, others...)...)
	}
	s.mu.Unlock()
	return s.Get(id)
}

// Item is loops.item: add, change or remove an item of a loop.
func (s *Store) Item(p wire.LoopItemParams) (wire.Loop, error) {
	s.mu.Lock()
	l, err := s.loopLocked(p.ID)
	if err != nil {
		s.mu.Unlock()
		return wire.Loop{}, err
	}
	if p.Item.Kind == wire.ItemCondition {
		s.mu.Unlock()
		return wire.Loop{}, wire.Errorf(wire.CodeInvalid, "conditions belong to milestones (goals.item)")
	}
	var changed bool
	if p.Remove {
		changed, err = s.removeItemLocked(l.Items, p.Item.ID)
	} else {
		_, changed, err = s.itemLocked(l.Items, p.Item, wire.ItemStep)
	}
	id := l.ID
	if err == nil && changed {
		s.commitLocked("l:" + id)
	}
	s.mu.Unlock()
	if err != nil {
		return wire.Loop{}, err
	}
	return s.Get(id)
}

// MergeLoops is loops.merge: From's links and items move to Into, and
// From points at Into.
func (s *Store) MergeLoops(p wire.LoopMergeParams) (wire.Loop, error) {
	s.mu.Lock()
	into, err := s.loopLocked(p.Into)
	if err == nil {
		var from *LoopRecord
		from, err = s.loopLocked(p.From)
		if err == nil && from.ID == into.ID {
			err = wire.Errorf(wire.CodeInvalid, "loop %s is already %s", p.From, p.Into)
		}
		if err == nil {
			s.moveLocked(from, into, func(*LinkRecord) bool { return true }, func(*ItemRecord) bool { return true })
			from.MergedInto.Set(into.ID, s.clock.Tick())
			s.commitLocked("l:"+from.ID, "l:"+into.ID)
		}
	}
	var id string
	if into != nil {
		id = into.ID
	}
	s.mu.Unlock()
	if err != nil {
		return wire.Loop{}, err
	}
	return s.Get(id)
}

// moveLocked moves the live links and items of from that match into to
// (their copies in from are tombstoned).
func (s *Store) moveLocked(from, to *LoopRecord, link func(*LinkRecord) bool, item func(*ItemRecord) bool) {
	for key, k := range from.Links {
		if k.Deleted.V || !link(k) {
			continue
		}
		cp := *k
		if cur := to.Links[key]; cur != nil {
			cur.merge(&cp)
			cur.Deleted.Set(false, s.clock.Tick())
		} else {
			cp.Ref.S, cp.Deleted = s.clock.Tick(), field[bool]{V: false, S: s.clock.Tick()}
			to.Links[key] = &cp
		}
		k.Deleted.Set(true, s.clock.Tick())
	}
	for id, it := range from.Items {
		if it.Deleted.V || !item(it) {
			continue
		}
		cp := it.clone()
		cp.Deleted = field[bool]{V: false, S: s.clock.Tick()}
		to.Items[id] = cp
		it.Deleted.Set(true, s.clock.Tick())
	}
}

// Split is loops.split: the link Ref (an agent with every agent it
// started or that continued it, transitively) and the items they brought
// move to a new loop.
func (s *Store) Split(p wire.LoopSplitParams) (wire.Loop, error) {
	s.mu.Lock()
	l, err := s.loopLocked(p.ID)
	if err != nil {
		s.mu.Unlock()
		return wire.Loop{}, err
	}
	refs := map[string]bool{p.Ref: true}
	found := false
	for _, k := range l.Links {
		found = found || !k.Deleted.V && k.Ref.V == p.Ref
	}
	if !found {
		s.mu.Unlock()
		return wire.Loop{}, wire.Errorf(wire.CodeNotFound, "loop %s has no link %s", l.ID, p.Ref)
	}
	for grew := true; grew; {
		grew = false
		for _, k := range l.Links {
			if member(k) && !refs[k.Ref.V] && refs[k.From.V] && (k.Rel.V == wire.RelStarted || k.Rel.V == wire.RelContinued) {
				refs[k.Ref.V], grew = true, true
			}
		}
	}
	title := p.Title
	if strings.TrimSpace(title) == "" {
		title = l.Title.V
		for _, k := range l.Links {
			if !k.Deleted.V && k.Ref.V == p.Ref && k.Label.V != "" {
				title = k.Label.V
			}
		}
	}
	n := s.newLoopLocked(title, l.Milestone.V)
	s.moveLocked(l, n, func(k *LinkRecord) bool { return refs[k.Ref.V] && k.Kind.V != wire.LinkProject },
		func(it *ItemRecord) bool { return refs[it.Source.V] })
	// The new loop touches the projects the old one did.
	for _, k := range l.Links {
		if !k.Deleted.V && k.Kind.V == wire.LinkProject {
			s.addLinkLocked(n, wire.LoopLink{Kind: wire.LinkProject, Ref: k.Ref.V, Rel: k.Rel.V})
		}
	}
	id := n.ID
	s.commitLocked("l:"+l.ID, "l:"+id)
	s.mu.Unlock()
	return s.Get(id)
}

// --- settings ---

// Settings is loops.settings.get.
func (s *Store) Settings() wire.LoopSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return settingsView(&s.settings)
}

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// SetSettings is loops.settings.set: the non-zero fields change.
func (s *Store) SetSettings(p wire.LoopSettings) (wire.LoopSettings, error) {
	if p.LoopLimit < 0 || p.StallDays < 0 || p.MaxWakesPerDay < 0 || p.LoopLimit > 1000 || p.StallDays > 3650 || p.MaxWakesPerDay > 1440 {
		return wire.LoopSettings{}, wire.Errorf(wire.CodeInvalid, "setting out of range")
	}
	if p.EveningAt != "" && !hhmm.MatchString(p.EveningAt) {
		return wire.LoopSettings{}, wire.Errorf(wire.CodeInvalid, "eveningAt %q is not HH:MM", p.EveningAt)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &s.settings
	changed := false
	if p.LoopLimit > 0 {
		changed = st.LoopLimit.Set(p.LoopLimit, s.clock.Tick()) || changed
	}
	if p.StallDays > 0 {
		changed = st.StallDays.Set(p.StallDays, s.clock.Tick()) || changed
	}
	if p.EveningAt != "" {
		changed = st.EveningAt.Set(p.EveningAt, s.clock.Tick()) || changed
	}
	if p.MaxWakesPerDay > 0 {
		changed = st.MaxWakesPerDay.Set(p.MaxWakesPerDay, s.clock.Tick()) || changed
	}
	if changed {
		s.commitLocked(keySettings)
	}
	return settingsView(st), nil
}

// --- goals and milestones ---

// Goals is goals.list (scope "": every product's), sorted by order.
func (s *Store) Goals(scope string) wire.GoalsList {
	b := s.snapshotOf().build()
	out := wire.GoalsList{Goals: []wire.Goal{}, Milestones: []wire.Milestone{}}
	for _, g := range b.goals {
		if scope == "" || g.Scope == scope {
			out.Goals = append(out.Goals, g)
		}
	}
	for _, m := range b.milestones {
		if scope == "" || m.Scope == scope {
			out.Milestones = append(out.Milestones, m)
		}
	}
	sort.Slice(out.Goals, func(i, j int) bool {
		if out.Goals[i].Order != out.Goals[j].Order {
			return out.Goals[i].Order < out.Goals[j].Order
		}
		return out.Goals[i].ID < out.Goals[j].ID
	})
	sort.Slice(out.Milestones, func(i, j int) bool {
		if out.Milestones[i].Order != out.Milestones[j].Order {
			return out.Milestones[i].Order < out.Milestones[j].Order
		}
		return out.Milestones[i].ID < out.Milestones[j].ID
	})
	return out
}

func (s *Store) goal(id string) (wire.GoalChanged, error) {
	b := s.snapshotOf().build()
	if g, ok := b.goals[id]; ok {
		return wire.GoalChanged{Goal: &g}, nil
	}
	if m, ok := b.milestones[id]; ok {
		return wire.GoalChanged{Milestone: &m}, nil
	}
	return wire.GoalChanged{}, wire.Errorf(wire.CodeNotFound, "no goal or milestone %s", id)
}

var goalStatuses = []string{wire.LoopOpen, wire.LoopDone, wire.LoopDropped}

// SaveGoal is goals.save: a new goal or milestone (no ID) or the fields
// given. The result has the one saved.
func (s *Store) SaveGoal(p wire.GoalSaveParams) (wire.GoalChanged, error) {
	if (p.Goal == nil) == (p.Milestone == nil) {
		return wire.GoalChanged{}, wire.Errorf(wire.CodeInvalid, "goals.save takes a goal or a milestone")
	}
	s.mu.Lock()
	var id string
	var err error
	if p.Goal != nil {
		id, err = s.saveGoalLocked(p.Goal)
	} else {
		id, err = s.saveMilestoneLocked(p.Milestone)
	}
	s.mu.Unlock()
	if err != nil {
		return wire.GoalChanged{}, err
	}
	return s.goal(id)
}

func checkText(vs ...*string) error {
	for _, v := range vs {
		if v != nil && len(*v) > maxText {
			return wire.Errorf(wire.CodeInvalid, "text too long")
		}
	}
	return nil
}

func (s *Store) saveGoalLocked(p *wire.GoalPatch) (string, error) {
	if err := checkText(p.Scope, p.Title); err != nil {
		return "", err
	}
	if p.Status != nil && !oneOf(*p.Status, goalStatuses...) {
		return "", wire.Errorf(wire.CodeInvalid, "unknown status %q", *p.Status)
	}
	g := s.goals[p.ID]
	if p.ID == "" {
		if p.Scope == nil || *p.Scope == "" || p.Title == nil || strings.TrimSpace(*p.Title) == "" {
			return "", wire.Errorf(wire.CodeInvalid, "a new goal needs a scope and a title")
		}
		g = &GoalRecord{ID: newID("gl-")}
		g.Status.Set(wire.LoopOpen, s.clock.Tick())
		s.goals[g.ID] = g
	} else if g == nil || g.Deleted.V {
		return "", wire.Errorf(wire.CodeNotFound, "no goal %s", p.ID)
	}
	if p.Scope != nil && *p.Scope != "" {
		g.Scope.Set(*p.Scope, s.clock.Tick())
	}
	if p.Title != nil && strings.TrimSpace(*p.Title) != "" {
		g.Title.Set(*p.Title, s.clock.Tick())
	}
	if p.Order != nil {
		g.Order.Set(*p.Order, s.clock.Tick())
	}
	if p.Status != nil {
		g.Status.Set(*p.Status, s.clock.Tick())
	}
	s.commitLocked("g:" + g.ID)
	return g.ID, nil
}

func (s *Store) saveMilestoneLocked(p *wire.MilestonePatch) (string, error) {
	if err := checkText(p.Scope, p.Title); err != nil {
		return "", err
	}
	if p.Status != nil && !oneOf(*p.Status, wire.MilestoneProposed, wire.LoopOpen, wire.LoopDone, wire.LoopDropped) {
		return "", wire.Errorf(wire.CodeInvalid, "unknown status %q", *p.Status)
	}
	if p.Goal != nil && *p.Goal != "" {
		if g := s.goals[*p.Goal]; g == nil || g.Deleted.V {
			return "", wire.Errorf(wire.CodeNotFound, "no goal %s", *p.Goal)
		}
	}
	m := s.milestones[p.ID]
	if p.ID == "" {
		if p.Scope == nil || *p.Scope == "" || p.Title == nil || strings.TrimSpace(*p.Title) == "" {
			return "", wire.Errorf(wire.CodeInvalid, "a new milestone needs a scope and a title")
		}
		m = &MilestoneRecord{ID: newID("ms-"), Items: map[string]*ItemRecord{}}
		m.Status.Set(wire.LoopOpen, s.clock.Tick())
		m.Created.Set(s.opt.Now().UTC(), s.clock.Tick())
		s.milestones[m.ID] = m
	} else if m == nil || m.Deleted.V {
		return "", wire.Errorf(wire.CodeNotFound, "no milestone %s", p.ID)
	}
	if p.Scope != nil && *p.Scope != "" {
		m.Scope.Set(*p.Scope, s.clock.Tick())
	}
	if p.Title != nil && strings.TrimSpace(*p.Title) != "" {
		m.Title.Set(*p.Title, s.clock.Tick())
	}
	if p.Goal != nil {
		m.Goal.Set(*p.Goal, s.clock.Tick())
	}
	if p.Status != nil {
		m.Status.Set(*p.Status, s.clock.Tick())
	}
	if p.Order != nil {
		m.Order.Set(*p.Order, s.clock.Tick())
	}
	if p.Due != nil {
		m.Due.Set(p.Due.UTC(), s.clock.Tick())
	}
	if p.AutoFile != nil {
		m.AutoFile.Set(*p.AutoFile, s.clock.Tick())
	}
	s.commitLocked("m:" + m.ID)
	return m.ID, nil
}

// RemoveGoal is goals.remove: a goal (its milestones keep theirs) or a
// milestone (its loops are unfiled).
func (s *Store) RemoveGoal(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g := s.goals[id]; g != nil && !g.Deleted.V {
		g.Deleted.Set(true, s.clock.Tick())
		s.commitLocked("g:" + id)
		return nil
	}
	m := s.milestones[id]
	if m == nil || m.Deleted.V {
		return wire.Errorf(wire.CodeNotFound, "no goal or milestone %s", id)
	}
	m.Deleted.Set(true, s.clock.Tick())
	keys := []string{"m:" + id}
	for lid, l := range s.loops {
		if l.Milestone.V == id {
			l.Milestone.Set("", s.clock.Tick())
			keys = append(keys, "l:"+lid)
		}
	}
	s.commitLocked(keys...)
	return nil
}

// MilestoneItem is goals.item: add, change or remove a milestone's
// condition.
func (s *Store) MilestoneItem(p wire.LoopItemParams) (wire.Milestone, error) {
	s.mu.Lock()
	m := s.milestones[p.ID]
	if m == nil || m.Deleted.V {
		s.mu.Unlock()
		return wire.Milestone{}, wire.Errorf(wire.CodeNotFound, "no milestone %s", p.ID)
	}
	var changed bool
	var err error
	if p.Remove {
		changed, err = s.removeItemLocked(m.Items, p.Item.ID)
	} else {
		_, changed, err = s.itemLocked(m.Items, p.Item, wire.ItemCondition)
	}
	if err == nil && changed {
		s.commitLocked("m:" + m.ID)
	}
	s.mu.Unlock()
	if err != nil {
		return wire.Milestone{}, err
	}
	return s.milestone(p.ID)
}

// File is goals.file: file a loop under a milestone, or unfile it. The
// result is the milestone.
func (s *Store) File(p wire.GoalFileParams) (wire.Milestone, error) {
	s.mu.Lock()
	l, err := s.loopLocked(p.Loop)
	if err == nil {
		if m := s.milestones[p.Milestone]; m == nil || m.Deleted.V {
			err = wire.Errorf(wire.CodeNotFound, "no milestone %s", p.Milestone)
		} else if p.Remove && l.Milestone.V != p.Milestone {
			err = wire.Errorf(wire.CodeNotFound, "loop %s is not filed under %s", l.ID, p.Milestone)
		}
	}
	if err != nil {
		s.mu.Unlock()
		return wire.Milestone{}, err
	}
	v := p.Milestone
	if p.Remove {
		v = ""
	}
	if l.Milestone.Set(v, s.clock.Tick()) {
		s.commitLocked("l:" + l.ID)
	}
	s.mu.Unlock()
	return s.milestone(p.Milestone)
}

func (s *Store) milestone(id string) (wire.Milestone, error) {
	g, err := s.goal(id)
	if err != nil {
		return wire.Milestone{}, err
	}
	if g.Milestone == nil {
		return wire.Milestone{}, wire.Errorf(wire.CodeNotFound, "no milestone %s", id)
	}
	return *g.Milestone, nil
}

// Accepted is goals.accept's result: how many milestones it opened.
type Accepted struct {
	Accepted int `json:"accepted"`
}

// Accept is goals.accept: the proposed milestones of a scope (or only
// IDs) open.
func (s *Store) Accept(p wire.GoalAcceptParams) (Accepted, error) {
	if p.Scope == "" {
		return Accepted{}, wire.Errorf(wire.CodeInvalid, "goals.accept needs a scope")
	}
	only := map[string]bool{}
	for _, id := range p.IDs {
		only[id] = true
	}
	s.mu.Lock()
	var keys []string
	for id, m := range s.milestones {
		if m.Deleted.V || m.Scope.V != p.Scope || m.Status.V != wire.MilestoneProposed || len(only) > 0 && !only[id] {
			continue
		}
		m.Status.Set(wire.LoopOpen, s.clock.Tick())
		keys = append(keys, "m:"+id)
	}
	s.commitLocked(keys...)
	s.mu.Unlock()
	return Accepted{Accepted: len(keys)}, nil
}
