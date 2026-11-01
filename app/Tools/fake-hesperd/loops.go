package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Loops, goals, milestones and the Steward (TEST-ONLY imitation of
// relay/pkg/wire/loops.go, see docs/rebuild-contract.md "As built — loops
// (app core)"): every loops.*, goals.* and steward.* method, and
// loops.changed/removed, goals.changed/removed and steward.changed on
// agents.subscribe (first a snapshot, like projects). Who acts next is
// computed here by the wire's rule, simplified. --loops demo seeds an
// invented fixture set: every board column, a map over two machines, a
// product with milestones and proposed ones, offers, asks and yourStep
// endings.

type Loop struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Goal        string     `json:"goal,omitempty"`
	DoneWhen    string     `json:"doneWhen,omitempty"`
	Status      string     `json:"status"`
	Next        string     `json:"next"`
	NextText    string     `json:"nextText,omitempty"`
	LooksDone   bool       `json:"looksDone,omitempty"`
	Milestone   string     `json:"milestone,omitempty"`
	ParkedUntil time.Time  `json:"parkedUntil,omitzero"`
	MergedInto  string     `json:"mergedInto,omitempty"`
	Items       []LoopItem `json:"items"`
	Links       []LoopLink `json:"links"`
	Counts      LoopCounts `json:"counts"`
	Projects    []string   `json:"projects,omitempty"`
	Created     time.Time  `json:"created"`
	Updated     time.Time  `json:"updated"`
	Closed      time.Time  `json:"closed,omitzero"`
}

type LoopCounts struct {
	Agents     int `json:"agents"`
	Working    int `json:"working"`
	OpenYou    int `json:"openYou"`
	OpenAgents int `json:"openAgents"`
	OpenOthers int `json:"openOthers"`
	Sessions   int `json:"sessions"`
}

type LoopItem struct {
	ID      string     `json:"id"`
	Text    string     `json:"text"`
	Owner   string     `json:"owner"`
	State   string     `json:"state"`
	Kind    string     `json:"kind"`
	Order   float64    `json:"order"`
	After   []string   `json:"after,omitempty"`
	Ready   bool       `json:"ready,omitempty"`
	Draft   string     `json:"draft,omitempty"`
	Source  string     `json:"source,omitempty"`
	Check   *ItemCheck `json:"check,omitempty"`
	Created time.Time  `json:"created"`
	Done    time.Time  `json:"done,omitzero"`
}

type ItemCheck struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type LoopLink struct {
	Kind         string    `json:"kind"`
	Ref          string    `json:"ref"`
	Rel          string    `json:"rel"`
	From         string    `json:"from,omitempty"`
	Label        string    `json:"label,omitempty"`
	Machine      string    `json:"machine,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	State        string    `json:"state,omitempty"`
	Ending       string    `json:"ending,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	ClosedReason string    `json:"closedReason,omitempty"`
	Closed       time.Time `json:"closed,omitzero"`
	Result       string    `json:"result,omitempty"`
	Added        time.Time `json:"added"`
}

type Goal struct {
	ID     string  `json:"id"`
	Scope  string  `json:"scope"`
	Title  string  `json:"title"`
	Order  float64 `json:"order"`
	Status string  `json:"status"`
}

type Milestone struct {
	ID       string     `json:"id"`
	Scope    string     `json:"scope"`
	Goal     string     `json:"goal,omitempty"`
	Title    string     `json:"title"`
	Status   string     `json:"status"`
	Order    float64    `json:"order"`
	Due      time.Time  `json:"due,omitzero"`
	AutoFile bool       `json:"autoFile,omitempty"`
	Items    []LoopItem `json:"items"`
	Loops    []string   `json:"loops"`
	Done     int        `json:"done"`
	Total    int        `json:"total"`
	Stalled  bool       `json:"stalled,omitempty"`
	NextText string     `json:"nextText,omitempty"`
	Created  time.Time  `json:"created"`
	Updated  time.Time  `json:"updated"`
}

type StewardStatus struct {
	Enabled  bool      `json:"enabled"`
	Level    string    `json:"level"`
	Node     string    `json:"node,omitempty"`
	Machine  string    `json:"machine,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Queued   int       `json:"queued"`
	Wakes    int       `json:"wakes"`
	LastWake time.Time `json:"lastWake,omitzero"`
	Session  string    `json:"session,omitempty"`
}

type StewardEvent struct {
	Kind    string    `json:"kind"`
	At      time.Time `json:"at"`
	Agent   string    `json:"agent,omitempty"`
	Loop    string    `json:"loop,omitempty"`
	Ending  string    `json:"ending,omitempty"`
	Summary string    `json:"summary,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}

type LoopSettings struct {
	LoopLimit      int    `json:"loopLimit"`
	StallDays      int    `json:"stallDays"`
	EveningAt      string `json:"eveningAt"`
	MaxWakesPerDay int    `json:"maxWakesPerDay"`
}

// loopState is the fake's loops store (guarded by daemon.mu).
type loopState struct {
	loops      map[string]*Loop
	goals      map[string]*Goal
	milestones map[string]*Milestone
	steward    StewardStatus
	settings   LoopSettings
	inbox      []StewardEvent
	seq        int
}

func newLoopState() *loopState {
	return &loopState{loops: map[string]*Loop{}, goals: map[string]*Goal{}, milestones: map[string]*Milestone{},
		steward: StewardStatus{Level: "suggest"}, settings: LoopSettings{LoopLimit: 5, StallDays: 7, EveningAt: "18:00", MaxWakesPerDay: 40}}
}

// id makes a record id: prefix + 12 hex (8 for items).
func (s *loopState) id(prefix string, n int) string {
	s.seq++
	h := fmt.Sprintf("%0*x", n, s.seq)
	return prefix + h[len(h)-n:]
}

// view computes a loop's derived fields: live states joined from the
// agents, counts, ready, and who acts next (wire.Loop.Next's rule).
// Lock held.
func (d *daemon) loopView(l *Loop) *Loop {
	v := *l
	v.Items = append([]LoopItem{}, l.Items...)
	v.Links = append([]LoopLink{}, l.Links...)
	done := map[string]bool{}
	for _, it := range v.Items {
		if it.State == "done" {
			done[it.ID] = true
		}
	}
	var c LoopCounts
	you, agentAsks, working, others := "", "", false, false
	quiet := true
	for i := range v.Links {
		k := &v.Links[i]
		if k.Kind == "agent" && k.Agent != "" {
			if ap := d.agents[k.Agent]; ap != nil {
				k.State = ap.a.State
			}
		}
		switch {
		case k.Kind == "session":
			c.Sessions++
		case k.Kind == "agent" && k.Agent != "" && k.Closed.IsZero():
			c.Agents++
			switch k.State {
			case "working", "starting":
				c.Working++
				working, quiet = true, false
			case "approval", "question":
				quiet = false
				if agentAsks == "" {
					agentAsks = k.Label + " waits for you"
				}
			default:
				if (k.Ending == "asks" || k.Ending == "yourStep") && agentAsks == "" {
					agentAsks = k.Summary
				}
			}
		case k.Kind == "loop" && k.Rel == "blockedBy":
			if o := d.loops.loops[k.Ref]; o != nil && o.Status == "open" {
				others = true
			}
		}
	}
	for i := range v.Items {
		it := &v.Items[i]
		it.Ready = true
		for _, a := range it.After {
			if !done[a] {
				it.Ready = false
			}
		}
		if it.State != "open" || it.Kind == "offer" {
			continue
		}
		switch it.Owner {
		case "you":
			c.OpenYou++
			if you == "" {
				you = it.Text
			}
		case "agent":
			c.OpenAgents++
		case "others":
			c.OpenOthers++
			others = true
		}
	}
	v.Counts, v.LooksDone, v.NextText = c, false, l.NextText
	switch {
	case l.Status == "parked":
		v.Next = "parked"
	case l.Status == "done" || l.Status == "dropped":
		v.Next = "closed"
	case you != "" || agentAsks != "":
		v.Next = "you"
		if v.NextText == "" {
			v.NextText = you
			if v.NextText == "" {
				v.NextText = agentAsks
			}
		}
	case working:
		v.Next = "agents"
	case others:
		v.Next = "others"
	default:
		v.Next, v.LooksDone = "you", quiet && c.OpenAgents == 0
		if v.LooksDone && v.NextText == "" {
			v.NextText = "Looks done"
		}
	}
	return &v
}

// milestoneView fills a milestone's loops and counts (lock held).
func (d *daemon) milestoneView(m *Milestone) *Milestone {
	v := *m
	v.Items = append([]LoopItem{}, m.Items...)
	v.Loops = []string{}
	for _, l := range d.sortedLoops() {
		if l.Milestone == m.ID && l.MergedInto == "" {
			v.Loops = append(v.Loops, l.ID)
		}
	}
	v.Done, v.Total, v.NextText = 0, 0, ""
	for _, it := range v.Items {
		if it.State == "dropped" {
			continue
		}
		v.Total++
		if it.State == "done" {
			v.Done++
		} else if v.NextText == "" {
			v.NextText = it.Text
		}
	}
	return &v
}

func (d *daemon) sortedLoops() []*Loop {
	list := make([]*Loop, 0, len(d.loops.loops))
	for _, l := range d.loops.loops {
		list = append(list, l)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

func (d *daemon) goalLists(scope string) ([]*Goal, []*Milestone) {
	gs, ms := []*Goal{}, []*Milestone{}
	for _, g := range d.loops.goals {
		if scope == "" || g.Scope == scope {
			gs = append(gs, g)
		}
	}
	for _, m := range d.loops.milestones {
		if scope == "" || m.Scope == scope {
			ms = append(ms, d.milestoneView(m))
		}
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].ID < gs[j].ID })
	sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
	return gs, ms
}

func note(method string, params any) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	return b
}

// loopsSnapshot: loops.changed for every open and parked loop, goals.changed
// for every goal and milestone, steward.changed (the lock is held).
func (d *daemon) loopsSnapshot() [][]byte {
	var out [][]byte
	for _, l := range d.sortedLoops() {
		if (l.Status == "open" || l.Status == "parked") && l.MergedInto == "" {
			out = append(out, note("loops.changed", map[string]any{"loop": d.loopView(l)}))
		}
	}
	gs, ms := d.goalLists("")
	for _, g := range gs {
		out = append(out, note("goals.changed", map[string]any{"goal": g}))
	}
	for _, m := range ms {
		out = append(out, note("goals.changed", map[string]any{"milestone": m}))
	}
	out = append(out, note("steward.changed", map[string]any{"steward": d.loops.steward}))
	return out
}

// loopNotes collects notifications while the lock is held and sends them
// after it (notify takes the lock).
type loopNotes []struct {
	method string
	params map[string]any
}

func (n *loopNotes) add(method string, params map[string]any) {
	*n = append(*n, struct {
		method string
		params map[string]any
	}{method, params})
}

func (d *daemon) send(n loopNotes) {
	for _, x := range n {
		d.notify(x.method, x.params)
	}
}

func (d *daemon) loopChanged(n *loopNotes, l *Loop) {
	n.add("loops.changed", map[string]any{"loop": d.loopView(l)})
	if l.Milestone != "" {
		if m := d.loops.milestones[l.Milestone]; m != nil {
			n.add("goals.changed", map[string]any{"milestone": d.milestoneView(m)})
		}
	}
}

func (d *daemon) loopsCall(method string, params json.RawMessage) (any, *rpcError) {
	d.mu.Lock()
	var notes loopNotes
	res, err := d.loopsCallLocked(method, params, &notes)
	d.mu.Unlock()
	if err == nil {
		d.send(notes)
	}
	return res, err
}

func (d *daemon) loopsCallLocked(method string, params json.RawMessage, n *loopNotes) (any, *rpcError) {
	s := d.loops
	now := time.Now().UTC()
	decode := func(v any) *rpcError {
		if len(params) == 0 {
			return nil
		}
		if err := json.Unmarshal(params, v); err != nil {
			return errCode("invalid", "bad params: "+err.Error())
		}
		return nil
	}
	loop := func(id string) (*Loop, *rpcError) {
		l := s.loops[id]
		if l == nil {
			return nil, errCode("not_found", "no loop "+id)
		}
		return l, nil
	}
	switch method {
	case "loops.list":
		var p struct{ Status, Milestone, Project, Agent string }
		if e := decode(&p); e != nil {
			return nil, e
		}
		out := []*Loop{}
		for _, l := range d.sortedLoops() {
			if l.MergedInto != "" || (p.Status == "" && l.Status != "open" && l.Status != "parked") ||
				(p.Status != "" && p.Status != "all" && l.Status != p.Status) ||
				(p.Milestone != "" && l.Milestone != p.Milestone) || (p.Project != "" && !contains(l.Projects, p.Project)) {
				continue
			}
			if p.Agent != "" && !hasAgent(l, p.Agent) {
				continue
			}
			out = append(out, d.loopView(l))
		}
		return out, nil
	case "loops.get":
		var p struct{ ID string }
		if e := decode(&p); e != nil {
			return nil, e
		}
		l, e := loop(p.ID)
		if e != nil {
			return nil, e
		}
		return d.loopView(l), nil
	case "loops.save":
		var p struct {
			ID                                                 string
			Title, Goal, DoneWhen, Status, NextText, Milestone *string
			ParkedUntil                                        *time.Time
		}
		if e := decode(&p); e != nil {
			return nil, e
		}
		var l *Loop
		if p.ID == "" {
			l = &Loop{ID: s.id("lp-", 12), Status: "open", Items: []LoopItem{}, Links: []LoopLink{}, Created: now}
			s.loops[l.ID] = l
		} else if x, e := loop(p.ID); e != nil {
			return nil, e
		} else {
			l = x
		}
		old := l.Milestone
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		set(&l.Title, p.Title)
		set(&l.Goal, p.Goal)
		set(&l.DoneWhen, p.DoneWhen)
		set(&l.NextText, p.NextText)
		set(&l.Milestone, p.Milestone)
		if p.Status != nil {
			switch *p.Status {
			case "open", "parked", "done", "dropped":
				l.Status = *p.Status
			default:
				return nil, errCode("invalid", "bad status "+*p.Status)
			}
			if l.Status == "done" || l.Status == "dropped" {
				l.Closed = now
			}
		}
		if p.ParkedUntil != nil {
			l.ParkedUntil = *p.ParkedUntil
		}
		l.Updated = now
		if old != "" && old != l.Milestone {
			if m := s.milestones[old]; m != nil {
				n.add("goals.changed", map[string]any{"milestone": d.milestoneView(m)})
			}
		}
		d.loopChanged(n, l)
		return d.loopView(l), nil
	case "loops.link":
		var p struct {
			ID     string
			Link   LoopLink
			Remove bool
		}
		if e := decode(&p); e != nil {
			return nil, e
		}
		l, e := loop(p.ID)
		if e != nil {
			return nil, e
		}
		if p.Link.Kind == "" || p.Link.Ref == "" {
			return nil, errCode("invalid", "a link needs a kind and a ref")
		}
		if p.Link.Rel == "" {
			p.Link.Rel = "member"
		}
		kept := l.Links[:0]
		for _, k := range l.Links {
			if !(k.Kind == p.Link.Kind && k.Ref == p.Link.Ref && k.Rel == p.Link.Rel) {
				kept = append(kept, k)
			}
		}
		l.Links = kept
		if !p.Remove {
			p.Link.Added = now
			l.Links = append(l.Links, p.Link)
		}
		l.Updated = now
		d.loopChanged(n, l)
		return d.loopView(l), nil
	case "loops.item", "goals.item":
		var p struct {
			ID     string
			Item   map[string]json.RawMessage
			Remove bool
		}
		if e := decode(&p); e != nil {
			return nil, e
		}
		var items *[]LoopItem
		var l *Loop
		var m *Milestone
		if method == "loops.item" {
			x, e := loop(p.ID)
			if e != nil {
				return nil, e
			}
			l, items = x, &x.Items
		} else {
			m = s.milestones[p.ID]
			if m == nil {
				return nil, errCode("not_found", "no milestone "+p.ID)
			}
			items = &m.Items
		}
		var id string
		_ = json.Unmarshal(p.Item["id"], &id)
		idx := -1
		for i, it := range *items {
			if it.ID == id {
				idx = i
			}
		}
		switch {
		case p.Remove:
			if idx < 0 {
				return nil, errCode("not_found", "no item "+id)
			}
			*items = append((*items)[:idx], (*items)[idx+1:]...)
		case idx < 0:
			if id != "" {
				return nil, errCode("not_found", "no item "+id)
			}
			it := LoopItem{ID: s.id("it-", 8), Owner: "you", State: "open", Kind: "step", Order: float64(len(*items) + 1), Created: now}
			if m != nil {
				it.Kind = "condition"
			}
			if e := patchItem(&it, p.Item, now); e != nil {
				return nil, e
			}
			*items = append(*items, it)
		default:
			if e := patchItem(&(*items)[idx], p.Item, now); e != nil {
				return nil, e
			}
		}
		if l != nil {
			l.Updated = now
			d.loopChanged(n, l)
			return d.loopView(l), nil
		}
		m.Updated = now
		v := d.milestoneView(m)
		n.add("goals.changed", map[string]any{"milestone": v})
		return v, nil
	case "loops.merge":
		var p struct{ Into, From string }
		if e := decode(&p); e != nil {
			return nil, e
		}
		into, e := loop(p.Into)
		if e != nil {
			return nil, e
		}
		from, e := loop(p.From)
		if e != nil {
			return nil, e
		}
		if into == from {
			return nil, errCode("invalid", "a loop can't merge into itself")
		}
		into.Links = append(into.Links, from.Links...)
		into.Items = append(into.Items, from.Items...)
		from.Links, from.Items, from.MergedInto, from.Updated, into.Updated = []LoopLink{}, []LoopItem{}, into.ID, now, now
		// As hesperd: the merged loop changes (mergedInto set), it is not removed.
		d.loopChanged(n, from)
		d.loopChanged(n, into)
		return d.loopView(into), nil
	case "loops.split":
		var p struct{ ID, Ref, Title string }
		if e := decode(&p); e != nil {
			return nil, e
		}
		l, e := loop(p.ID)
		if e != nil {
			return nil, e
		}
		// The ref and, for an agent, every link it started (transitively).
		move := map[string]bool{p.Ref: true}
		for changed := true; changed; {
			changed = false
			for _, k := range l.Links {
				if move[k.From] && !move[k.Ref] {
					move[k.Ref], changed = true, true
				}
			}
		}
		title := p.Title
		if title == "" {
			title = "Split from " + l.Title
		}
		nl := &Loop{ID: s.id("lp-", 12), Title: title, Status: "open", Items: []LoopItem{}, Links: []LoopLink{}, Projects: l.Projects, Created: now, Updated: now}
		kept := []LoopLink{}
		for _, k := range l.Links {
			if move[k.Ref] {
				nl.Links = append(nl.Links, k)
			} else {
				kept = append(kept, k)
			}
		}
		if len(nl.Links) == 0 {
			return nil, errCode("not_found", "no link "+p.Ref)
		}
		l.Links, l.Updated = kept, now
		s.loops[nl.ID] = nl
		d.loopChanged(n, l)
		d.loopChanged(n, nl)
		return d.loopView(nl), nil
	case "loops.settings.get":
		return s.settings, nil
	case "loops.settings.set":
		var p struct {
			LoopLimit, StallDays, MaxWakesPerDay *int
			EveningAt                            *string
		}
		if e := decode(&p); e != nil {
			return nil, e
		}
		if p.LoopLimit != nil && *p.LoopLimit > 0 {
			s.settings.LoopLimit = *p.LoopLimit
		}
		if p.StallDays != nil && *p.StallDays > 0 {
			s.settings.StallDays = *p.StallDays
		}
		if p.MaxWakesPerDay != nil && *p.MaxWakesPerDay > 0 {
			s.settings.MaxWakesPerDay = *p.MaxWakesPerDay
		}
		if p.EveningAt != nil {
			if _, err := time.Parse("15:04", *p.EveningAt); err != nil {
				return nil, errCode("invalid", "eveningAt is HH:MM")
			}
			s.settings.EveningAt = *p.EveningAt
		}
		return s.settings, nil
	case "goals.list":
		var p struct{ Scope string }
		if e := decode(&p); e != nil {
			return nil, e
		}
		gs, ms := d.goalLists(p.Scope)
		return map[string]any{"goals": gs, "milestones": ms}, nil
	case "goals.save":
		var p struct {
			Goal *struct {
				ID                   string
				Scope, Title, Status *string
				Order                *float64
			}
			Milestone *struct {
				ID                         string
				Scope, Goal, Title, Status *string
				Order                      *float64
				Due                        *time.Time
				AutoFile                   *bool
			}
		}
		if e := decode(&p); e != nil {
			return nil, e
		}
		set := func(dst *string, v *string) {
			if v != nil {
				*dst = *v
			}
		}
		switch {
		case p.Goal != nil && p.Milestone == nil:
			g := s.goals[p.Goal.ID]
			if g == nil {
				if p.Goal.ID != "" {
					return nil, errCode("not_found", "no goal "+p.Goal.ID)
				}
				g = &Goal{ID: s.id("gl-", 12), Status: "open"}
				s.goals[g.ID] = g
			}
			set(&g.Scope, p.Goal.Scope)
			set(&g.Title, p.Goal.Title)
			set(&g.Status, p.Goal.Status)
			if p.Goal.Order != nil {
				g.Order = *p.Goal.Order
			}
			n.add("goals.changed", map[string]any{"goal": g})
			return map[string]any{"goal": g}, nil
		case p.Milestone != nil && p.Goal == nil:
			mp := p.Milestone
			m := s.milestones[mp.ID]
			if m == nil {
				if mp.ID != "" {
					return nil, errCode("not_found", "no milestone "+mp.ID)
				}
				m = &Milestone{ID: s.id("ms-", 12), Status: "open", Items: []LoopItem{}, Created: now}
				s.milestones[m.ID] = m
			}
			set(&m.Scope, mp.Scope)
			set(&m.Goal, mp.Goal)
			set(&m.Title, mp.Title)
			set(&m.Status, mp.Status)
			if mp.Order != nil {
				m.Order = *mp.Order
			}
			if mp.Due != nil {
				m.Due = *mp.Due
			}
			if mp.AutoFile != nil {
				m.AutoFile = *mp.AutoFile
			}
			m.Updated = now
			v := d.milestoneView(m)
			n.add("goals.changed", map[string]any{"milestone": v})
			return map[string]any{"milestone": v}, nil
		}
		return nil, errCode("invalid", "give exactly one of goal and milestone")
	case "goals.remove":
		var p struct{ ID string }
		if e := decode(&p); e != nil {
			return nil, e
		}
		if s.goals[p.ID] == nil && s.milestones[p.ID] == nil {
			return nil, errCode("not_found", "no goal or milestone "+p.ID)
		}
		delete(s.goals, p.ID)
		delete(s.milestones, p.ID)
		for _, l := range s.loops {
			if l.Milestone == p.ID {
				l.Milestone, l.Updated = "", now
				d.loopChanged(n, l)
			}
		}
		n.add("goals.removed", map[string]any{"id": p.ID})
		return map[string]any{}, nil
	case "goals.file":
		var p struct {
			Milestone, Loop string
			Remove          bool
		}
		if e := decode(&p); e != nil {
			return nil, e
		}
		l, e := loop(p.Loop)
		if e != nil {
			return nil, e
		}
		m := s.milestones[p.Milestone]
		if m == nil {
			return nil, errCode("not_found", "no milestone "+p.Milestone)
		}
		old := s.milestones[l.Milestone]
		if p.Remove {
			if l.Milestone == m.ID {
				l.Milestone = ""
			}
		} else {
			l.Milestone = m.ID
		}
		l.Updated = now
		if old != nil && old != m {
			n.add("goals.changed", map[string]any{"milestone": d.milestoneView(old)})
		}
		n.add("goals.changed", map[string]any{"milestone": d.milestoneView(m)})
		n.add("loops.changed", map[string]any{"loop": d.loopView(l)})
		return d.loopView(l), nil
	case "goals.accept":
		var p struct {
			Scope string
			IDs   []string
		}
		if e := decode(&p); e != nil {
			return nil, e
		}
		ids := []string{}
		for _, m := range s.milestones {
			if m.Scope == p.Scope && m.Status == "proposed" && (len(p.IDs) == 0 || contains(p.IDs, m.ID)) {
				m.Status, m.Updated = "open", now
				ids = append(ids, m.ID)
				n.add("goals.changed", map[string]any{"milestone": d.milestoneView(m)})
			}
		}
		sort.Strings(ids)
		return map[string]any{"accepted": ids}, nil
	case "steward.status":
		return s.steward, nil
	case "steward.set":
		var p struct {
			Enabled        *bool
			Level, Machine *string
		}
		if e := decode(&p); e != nil {
			return nil, e
		}
		if p.Enabled != nil {
			s.steward.Enabled = *p.Enabled
		}
		if p.Level != nil {
			switch *p.Level {
			case "watch", "suggest", "act":
				s.steward.Level = *p.Level
			default:
				return nil, errCode("invalid", "bad level "+*p.Level)
			}
		}
		if p.Machine != nil {
			s.steward.Machine, s.steward.Node = *p.Machine, "n-"+strings.ToLower(*p.Machine)
		}
		s.steward.Agent = ""
		if s.steward.Enabled && (s.steward.Machine == "" || s.steward.Machine == d.self) {
			s.steward.Machine, s.steward.Node, s.steward.Agent = d.self, "n-"+strings.ToLower(d.self), d.self+"/steward"
		}
		n.add("steward.changed", map[string]any{"steward": s.steward})
		return s.steward, nil
	case "steward.inbox":
		out := s.inbox
		s.inbox, s.steward.Queued = []StewardEvent{}, 0
		n.add("steward.changed", map[string]any{"steward": s.steward})
		if out == nil {
			out = []StewardEvent{}
		}
		return out, nil
	case "steward.plan":
		var p struct{ Scope, Prompt string }
		if e := decode(&p); e != nil {
			return nil, e
		}
		if p.Scope == "" {
			return nil, errCode("invalid", "a plan needs a scope")
		}
		// A planner proposes two milestones right away (the real one
		// writes them while it works).
		for i, t := range []string{"First usable version", "Polish and release"} {
			m := &Milestone{ID: s.id("ms-", 12), Scope: p.Scope, Title: t, Status: "proposed", Order: float64(100 + i), Items: []LoopItem{}, Created: now, Updated: now}
			s.milestones[m.ID] = m
			n.add("goals.changed", map[string]any{"milestone": d.milestoneView(m)})
		}
		return map[string]any{"id": d.self + "/planner"}, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + method, Data: map[string]any{"code": "not_found"}}
}

// patchItem changes the item fields given (loops.item, goals.item).
func patchItem(it *LoopItem, f map[string]json.RawMessage, now time.Time) *rpcError {
	str := func(k string, dst *string) {
		if raw, ok := f[k]; ok {
			_ = json.Unmarshal(raw, dst)
		}
	}
	str("text", &it.Text)
	str("owner", &it.Owner)
	str("kind", &it.Kind)
	str("draft", &it.Draft)
	str("source", &it.Source)
	before := it.State
	str("state", &it.State)
	if raw, ok := f["order"]; ok {
		_ = json.Unmarshal(raw, &it.Order)
	}
	if raw, ok := f["after"]; ok {
		_ = json.Unmarshal(raw, &it.After)
	}
	if raw, ok := f["check"]; ok {
		// An empty kind clears the check (the trigger "none").
		var c ItemCheck
		if json.Unmarshal(raw, &c) == nil {
			if c.Kind == "" {
				it.Check = nil
			} else {
				it.Check = &c
			}
		}
	}
	switch it.State {
	case "open", "done", "dropped":
	default:
		return errCode("invalid", "bad item state "+it.State)
	}
	if it.State == "done" && before != "done" {
		it.Done = now
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func hasAgent(l *Loop, id string) bool {
	for _, k := range l.Links {
		if k.Kind == "agent" && (k.Agent == id || k.Ref == id) {
			return true
		}
	}
	return false
}

// seedDemoLoops: an invented fixture set (--loops demo) over the demo
// projects (--projects demo), machines L and M. Times are relative to
// now, so "today" in the wrap-up holds whenever it runs.
func (d *daemon) seedDemoLoops() {
	s := d.loops
	now := time.Now().UTC()
	ago := func(h float64) time.Time { return now.Add(-time.Duration(h * float64(time.Hour))) }
	agent := func(ref, rel, from, label, machine, state, ending, summary string, added time.Time) LoopLink {
		local := ref[strings.LastIndex(ref, "/")+1:]
		k := LoopLink{Kind: "agent", Ref: ref, Rel: rel, From: from, Label: label, Machine: machine, State: state, Ending: ending, Summary: summary, Added: added}
		if state != "" {
			k.Agent = machine + "/" + local
		}
		return k
	}
	gone := func(k LoopLink, reason, result string, at time.Time) LoopLink {
		k.Agent, k.State, k.ClosedReason, k.Result, k.Closed = "", "", reason, result, at
		return k
	}
	item := func(id, text, owner, kind, source string, order float64, created time.Time) LoopItem {
		return LoopItem{ID: id, Text: text, Owner: owner, State: "open", Kind: kind, Order: order, Source: source, Created: created}
	}
	add := func(l *Loop) {
		if l.Items == nil {
			l.Items = []LoopItem{}
		}
		if l.Links == nil {
			l.Links = []LoopLink{}
		}
		if l.Status == "" {
			l.Status = "open"
		}
		s.loops[l.ID] = l
	}

	// You: a step from a child on the mini, a map over both machines (a
	// brief handed over, a child, a parent nobody knows), a loose end, an
	// offer, a PR and a session.
	add(&Loop{ID: "lp-0000000push", Title: "Push provider for the acme app", Goal: "Push on iOS and Android", DoneWhen: "PR merged and tagged",
		Milestone: "ms-00000000beta", Projects: []string{"p-acme-apps", "p-ios-app"}, Created: ago(30), Updated: ago(1),
		Items: []LoopItem{
			item("it-0000push1", "Approve the FCM sender ID in the console", "you", "step", "n-m/fcm001", 1, ago(1)),
			item("it-0000push2", "Rotate the APNs signing key", "agent", "looseEnd", "n-l/push01", 2, ago(5)),
			item("it-0000push3", "Want me to add Android notification channels too?", "agent", "offer", "n-m/fcm001", 3, ago(1)),
		},
		Links: []LoopLink{
			agent("n-l/push01", "member", "", "push-provider", "L", "idle", "done", "Wrote the brief for the FCM part", ago(30)),
			{Kind: "file", Ref: "L:" + fakeRoot + "/acme-apps/docs/push-brief.md", Rel: "handoff", From: "n-l/push01", Added: ago(26)},
			agent("n-m/fcm001", "handoff", "L:"+fakeRoot+"/acme-apps/docs/push-brief.md", "fcm sender", "M", "done", "yourStep", "Approve the FCM sender ID, then I can finish", ago(25)),
			gone(agent("n-l/apns01", "started", "n-l/push01", "apns key", "L", "", "done", "Checked the APNs key expiry", ago(20)), "closed", "The key expires in 40 days.", ago(18)),
			agent("n-m/test01", "started", "L/gone01", "push tests", "M", "working", "", "", ago(3)),
			{Kind: "url", Ref: "https://example.com/acme/acme-apps/pull/42", Rel: "member", Label: "PR #42 push provider", Added: ago(10)},
			{Kind: "session", Ref: "n-l:claude:00000000-0000-4000-8000-000000000001", Rel: "member", Machine: "L", Added: ago(30)},
			{Kind: "project", Ref: "p-ios-app", Rel: "touches", Added: ago(20)},
		}})
	// You: a quiet agent asks.
	add(&Loop{ID: "lp-00000badges", Title: "Flaky badges test", Projects: []string{"p-acme-apps"}, Created: ago(6), Updated: ago(0.5),
		Links: []LoopLink{agent("n-l/badge1", "member", "", "flaky badges test", "L", "done", "asks", "Should the badge test retry twice or fail fast?", ago(6))}})
	// You: a named step with a command.
	add(&Loop{ID: "lp-00000deploy", Title: "Release the design tokens", Projects: []string{"p-edition"}, Milestone: "ms-00000launch", Created: ago(50), Updated: ago(2),
		Links: []LoopLink{agent("n-m/rel001", "member", "", "token release", "M", "idle", "yourStep", "Run `make release` on the mini to publish 2.4.0", ago(50))}})
	// You: looks done.
	add(&Loop{ID: "lp-0000000copy", Title: "Draft PR copy", Projects: []string{"p-edition"}, Created: ago(8), Updated: ago(4),
		Links: []LoopLink{agent("n-l/copy01", "member", "", "draft PR copy", "L", "done", "done", "PR description written", ago(8))}})
	// Agents: working on both machines.
	add(&Loop{ID: "lp-00000widget", Title: "iOS home screen widgets", Projects: []string{"p-ios-app"}, Milestone: "ms-00000000beta", Created: ago(4), Updated: ago(0.2),
		Items: []LoopItem{item("it-000widget", "Snapshot tests for the small widget", "agent", "promise", "n-l/wid001", 1, ago(3))},
		Links: []LoopLink{
			agent("n-l/wid001", "member", "", "ios widgets", "L", "working", "", "", ago(4)),
			agent("n-m/wid002", "started", "n-l/wid001", "widget previews", "M", "working", "", "", ago(2)),
		}})
	// Others: a review from people outside.
	add(&Loop{ID: "lp-00000tokens", Title: "Colour tokens for dark mode", Projects: []string{"p-edition"}, Milestone: "ms-00000launch", Created: ago(70), Updated: ago(20),
		Items: []LoopItem{item("it-00tokens1", "Design review from the brand team", "others", "step", "", 1, ago(20))},
		Links: []LoopLink{gone(agent("n-l/tok001", "member", "", "dark tokens", "L", "", "waits", "Waiting for the brand review", ago(70)), "closed", "Tokens drafted.", ago(20))}})
	// Others: blocked by the push loop.
	add(&Loop{ID: "lp-0000000auth", Title: "Auth renewal before push", Projects: []string{"p-acme-apps"}, Milestone: "ms-00000launch", Created: ago(28), Updated: ago(6),
		Links: []LoopLink{
			gone(agent("n-l/auth01", "member", "", "auth renewal", "L", "", "waits", "Needs the push provider first", ago(28)), "closed", "", ago(6)),
			{Kind: "loop", Ref: "lp-0000000push", Rel: "blockedBy", Added: ago(6)},
		}})
	// Parked: back in two days; parked long ago.
	add(&Loop{ID: "lp-0000000vtcp", Title: "VT screen copy spike", Status: "parked", ParkedUntil: now.Add(48 * time.Hour), Projects: []string{"p-hesper"}, Created: ago(200), Updated: ago(100),
		Links: []LoopLink{gone(agent("n-l/vt0001", "member", "", "vt screen copy", "L", "", "offer", "I could try a ring buffer next", ago(200)), "finished-in-background", "Spike notes in the PR.", ago(150))}})
	// Debris: only failed agents.
	add(&Loop{ID: "lp-0000debris", Title: "Relay latency probe", Projects: []string{"p-hesper"}, Created: ago(3), Updated: ago(2.5),
		Links: []LoopLink{agent("n-l/lat001", "member", "", "relay latency", "L", "error", "", "", ago(3)), gone(agent("n-l/lat002", "started", "n-l/lat001", "latency retry", "L", "", "", "", ago(2.8)), "removed", "", ago(2.5))}})
	// Stale: no change in four days.
	add(&Loop{ID: "lp-00000stale1", Title: "Edition picker cleanup", Projects: []string{"p-edition"}, Created: ago(150), Updated: ago(100),
		Links: []LoopLink{agent("n-m/ed0001", "member", "", "edition picker", "M", "idle", "waits", "Waiting for the API change", ago(150))}})
	// Closed today (the wrap-up's tally) and long ago.
	add(&Loop{ID: "lp-0000000done", Title: "Onboarding copy", Status: "done", Projects: []string{"p-acme-apps"}, Milestone: "ms-0000000alpha", Created: ago(40), Updated: ago(1), Closed: ago(1)})

	s.goals["gl-0000stores"] = &Goal{ID: "gl-0000stores", Scope: "g-acme", Title: "acme apps in both app stores", Order: 1, Status: "open"}
	s.goals["gl-00000tools"] = &Goal{ID: "gl-00000tools", Scope: "p-hesper", Title: "A calmer wall", Order: 1, Status: "open"}
	cond := func(id, text, owner, state string, order float64) LoopItem {
		it := LoopItem{ID: id, Text: text, Owner: owner, State: state, Kind: "condition", Order: order, Created: ago(300)}
		if state == "done" {
			it.Done = ago(100)
		}
		return it
	}
	ms := []*Milestone{
		{ID: "ms-0000000alpha", Scope: "g-acme", Goal: "gl-0000stores", Title: "Internal alpha", Status: "done", Order: 1,
			Items: []LoopItem{cond("it-0000alph1", "Sign-in works", "agent", "done", 1)}},
		{ID: "ms-00000000beta", Scope: "g-acme", Goal: "gl-0000stores", Title: "Public beta", Status: "open", Order: 2, Due: now.Add(10 * 24 * time.Hour), AutoFile: true,
			Items: []LoopItem{cond("it-00000beta1", "Push works on iOS and Android", "agent", "open", 1), cond("it-00000beta2", "Crash-free sessions above 99 %", "agent", "done", 2)}},
		{ID: "ms-00000launch", Scope: "g-acme", Goal: "gl-0000stores", Title: "Store launch", Status: "open", Order: 3, Due: now.Add(30 * 24 * time.Hour),
			Items: []LoopItem{cond("it-0000laun1", "Store listing approved", "you", "open", 1),
				{ID: "it-0000laun2", Text: "Push loop done", Owner: "agent", State: "open", Kind: "condition", Order: 2, Check: &ItemCheck{Kind: "loop", Ref: "lp-0000000push"}, Created: ago(300)}}},
		{ID: "ms-000proposed1", Scope: "g-acme", Goal: "gl-0000stores", Title: "TestFlight open beta", Status: "proposed", Order: 4},
		{ID: "ms-000proposed2", Scope: "g-acme", Goal: "gl-0000stores", Title: "Android open testing", Status: "proposed", Order: 5},
		{ID: "ms-0000calmwall", Scope: "p-hesper", Goal: "gl-00000tools", Title: "Quiet notifications", Status: "open", Order: 1, Stalled: true,
			Items: []LoopItem{cond("it-0000calm1", "No sound for offers", "agent", "open", 1)}},
	}
	for _, m := range ms {
		if m.Items == nil {
			m.Items = []LoopItem{}
		}
		m.Created, m.Updated = ago(300), ago(24)
		s.milestones[m.ID] = m
	}
	s.milestones["ms-0000calmwall"].Updated = ago(24 * 9)

	s.steward = StewardStatus{Enabled: true, Level: "suggest", Node: "n-l", Machine: "L", Agent: "L/steward", Queued: 2, Wakes: 6, LastWake: ago(0.3), Session: "s-0000steward"}
	s.inbox = []StewardEvent{
		{Kind: "settled", At: ago(0.5), Agent: "L/badge1", Loop: "lp-00000badges", Ending: "asks", Summary: "Should the badge test retry twice or fail fast?"},
		{Kind: "closed", At: ago(2.5), Agent: "L/lat002", Loop: "lp-0000debris", Reason: "removed"},
	}
}
