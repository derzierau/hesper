package loops

import (
	"time"

	"github.com/derzierau/hesper/relay/internal/crdt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Records as stored and replicated. Every field is a last-writer-wins
// register (internal/crdt); a record's links, items and conditions are
// maps of registers keyed by id, each entry with its own Deleted register
// (a tombstone: keys are never removed, so a removal stays removed when an
// older copy arrives). Loops are never deleted: a person ends them (done,
// dropped) and loops.merge points one at another (MergedInto).

type (
	stamp        = crdt.Stamp
	field[T any] = crdt.Field[T]
)

// LoopRecord is a loop.
type LoopRecord struct {
	ID          string                 `json:"id"`
	Title       field[string]          `json:"title"`
	Goal        field[string]          `json:"goal"`
	DoneWhen    field[string]          `json:"doneWhen"`
	Status      field[string]          `json:"status"`
	NextText    field[string]          `json:"nextText"`
	Milestone   field[string]          `json:"milestone"`
	ParkedUntil field[time.Time]       `json:"parkedUntil"`
	AutoFile    field[bool]            `json:"autoFile"`
	MergedInto  field[string]          `json:"mergedInto"`
	Closed      field[time.Time]       `json:"closed"`
	Created     field[time.Time]       `json:"created"`
	Links       map[string]*LinkRecord `json:"links,omitempty"`
	Items       map[string]*ItemRecord `json:"items,omitempty"`
}

// LinkRecord is one link of a loop: only settled facts (its name, how its
// last turn ended, how it was closed); live state is joined in when views
// are built. Its key is linkKey of how it was first added; Ref is a
// register because a move rewrites it.
type LinkRecord struct {
	Kind         field[string]    `json:"kind"`
	Ref          field[string]    `json:"ref"`
	Rel          field[string]    `json:"rel"`
	From         field[string]    `json:"from"`
	Label        field[string]    `json:"label"`
	Ending       field[string]    `json:"ending"`
	Summary      field[string]    `json:"summary"`
	ClosedReason field[string]    `json:"closedReason"`
	Closed       field[time.Time] `json:"closed"`
	Result       field[string]    `json:"result"`
	Added        field[time.Time] `json:"added"`
	Deleted      field[bool]      `json:"deleted"`
}

// ItemRecord is an item of a loop or a condition of a milestone.
type ItemRecord struct {
	Text    field[string]          `json:"text"`
	Owner   field[string]          `json:"owner"`
	State   field[string]          `json:"state"`
	Kind    field[string]          `json:"kind"`
	Order   field[float64]         `json:"order"`
	After   field[[]string]        `json:"after"`
	Draft   field[string]          `json:"draft"`
	Source  field[string]          `json:"source"`
	Check   field[*wire.ItemCheck] `json:"check"`
	Created field[time.Time]       `json:"created"`
	Done    field[time.Time]       `json:"done"`
	Deleted field[bool]            `json:"deleted"`
}

// GoalRecord is a goal.
type GoalRecord struct {
	ID      string         `json:"id"`
	Scope   field[string]  `json:"scope"`
	Title   field[string]  `json:"title"`
	Order   field[float64] `json:"order"`
	Status  field[string]  `json:"status"`
	Deleted field[bool]    `json:"deleted"`
}

// MilestoneRecord is a milestone; Items are its "done when" conditions.
type MilestoneRecord struct {
	ID       string                 `json:"id"`
	Scope    field[string]          `json:"scope"`
	Goal     field[string]          `json:"goal"`
	Title    field[string]          `json:"title"`
	Status   field[string]          `json:"status"`
	Order    field[float64]         `json:"order"`
	Due      field[time.Time]       `json:"due"`
	AutoFile field[bool]            `json:"autoFile"`
	Created  field[time.Time]       `json:"created"`
	Deleted  field[bool]            `json:"deleted"`
	Items    map[string]*ItemRecord `json:"items,omitempty"`
}

// StewardRecord is the Steward's configuration: one for all Macs.
type StewardRecord struct {
	Enabled field[bool]   `json:"enabled"`
	Level   field[string] `json:"level"`
	Node    field[string] `json:"node"`
}

// SettingsRecord are the shared loop settings (zero: the default).
type SettingsRecord struct {
	LoopLimit      field[int]    `json:"loopLimit"`
	StallDays      field[int]    `json:"stallDays"`
	EveningAt      field[string] `json:"eveningAt"`
	MaxWakesPerDay field[int]    `json:"maxWakesPerDay"`
}

// Record is one replicated record (exactly one field set), what
// loops.pull and loops.sync carry.
type Record struct {
	Loop      *LoopRecord      `json:"loop,omitempty"`
	Goal      *GoalRecord      `json:"goal,omitempty"`
	Milestone *MilestoneRecord `json:"milestone,omitempty"`
	Steward   *StewardRecord   `json:"steward,omitempty"`
	Settings  *SettingsRecord  `json:"settings,omitempty"`
}

// --- merge ---

func (l *LinkRecord) merge(o *LinkRecord) bool {
	c := l.Kind.Merge(o.Kind)
	c = l.Ref.Merge(o.Ref) || c
	c = l.Rel.Merge(o.Rel) || c
	c = l.From.Merge(o.From) || c
	c = l.Label.Merge(o.Label) || c
	c = l.Ending.Merge(o.Ending) || c
	c = l.Summary.Merge(o.Summary) || c
	c = l.ClosedReason.Merge(o.ClosedReason) || c
	c = l.Closed.Merge(o.Closed) || c
	c = l.Result.Merge(o.Result) || c
	c = l.Added.Merge(o.Added) || c
	c = l.Deleted.Merge(o.Deleted) || c
	return c
}

func (l *LinkRecord) stamps(fn func(stamp)) {
	for _, s := range []stamp{l.Kind.S, l.Ref.S, l.Rel.S, l.From.S, l.Label.S, l.Ending.S, l.Summary.S,
		l.ClosedReason.S, l.Closed.S, l.Result.S, l.Added.S, l.Deleted.S} {
		fn(s)
	}
}

func (it *ItemRecord) merge(o *ItemRecord) bool {
	c := it.Text.Merge(o.Text)
	c = it.Owner.Merge(o.Owner) || c
	c = it.State.Merge(o.State) || c
	c = it.Kind.Merge(o.Kind) || c
	c = it.Order.Merge(o.Order) || c
	c = it.After.Merge(o.After) || c
	c = it.Draft.Merge(o.Draft) || c
	c = it.Source.Merge(o.Source) || c
	c = it.Check.Merge(o.Check) || c
	c = it.Created.Merge(o.Created) || c
	c = it.Done.Merge(o.Done) || c
	c = it.Deleted.Merge(o.Deleted) || c
	return c
}

func (it *ItemRecord) stamps(fn func(stamp)) {
	for _, s := range []stamp{it.Text.S, it.Owner.S, it.State.S, it.Kind.S, it.Order.S, it.After.S, it.Draft.S,
		it.Source.S, it.Check.S, it.Created.S, it.Done.S, it.Deleted.S} {
		fn(s)
	}
}

func (it *ItemRecord) clone() *ItemRecord {
	c := *it
	c.After.V = append([]string(nil), it.After.V...)
	if it.Check.V != nil {
		ck := *it.Check.V
		c.Check.V = &ck
	}
	return &c
}

func mergeItems(dst *map[string]*ItemRecord, src map[string]*ItemRecord) bool {
	if *dst == nil {
		*dst = map[string]*ItemRecord{}
	}
	changed := false
	for id, o := range src {
		if o == nil {
			continue
		}
		if cur := (*dst)[id]; cur != nil {
			changed = cur.merge(o) || changed
		} else {
			(*dst)[id], changed = o.clone(), true
		}
	}
	return changed
}

func (l *LoopRecord) merge(o *LoopRecord) bool {
	c := l.Title.Merge(o.Title)
	c = l.Goal.Merge(o.Goal) || c
	c = l.DoneWhen.Merge(o.DoneWhen) || c
	c = l.Status.Merge(o.Status) || c
	c = l.NextText.Merge(o.NextText) || c
	c = l.Milestone.Merge(o.Milestone) || c
	c = l.ParkedUntil.Merge(o.ParkedUntil) || c
	c = l.AutoFile.Merge(o.AutoFile) || c
	c = l.MergedInto.Merge(o.MergedInto) || c
	c = l.Closed.Merge(o.Closed) || c
	c = l.Created.Merge(o.Created) || c
	if l.Links == nil {
		l.Links = map[string]*LinkRecord{}
	}
	for k, ol := range o.Links {
		if ol == nil {
			continue
		}
		if cur := l.Links[k]; cur != nil {
			c = cur.merge(ol) || c
		} else {
			cp := *ol
			l.Links[k], c = &cp, true
		}
	}
	return mergeItems(&l.Items, o.Items) || c
}

func (l *LoopRecord) stamps(fn func(stamp)) {
	for _, s := range []stamp{l.Title.S, l.Goal.S, l.DoneWhen.S, l.Status.S, l.NextText.S, l.Milestone.S,
		l.ParkedUntil.S, l.AutoFile.S, l.MergedInto.S, l.Closed.S, l.Created.S} {
		fn(s)
	}
	for _, k := range l.Links {
		k.stamps(fn)
	}
	for _, it := range l.Items {
		it.stamps(fn)
	}
}

func (l *LoopRecord) clone() *LoopRecord {
	c := *l
	c.Links = make(map[string]*LinkRecord, len(l.Links))
	for k, v := range l.Links {
		cp := *v
		c.Links[k] = &cp
	}
	c.Items = make(map[string]*ItemRecord, len(l.Items))
	for k, v := range l.Items {
		c.Items[k] = v.clone()
	}
	return &c
}

func (g *GoalRecord) merge(o *GoalRecord) bool {
	c := g.Scope.Merge(o.Scope)
	c = g.Title.Merge(o.Title) || c
	c = g.Order.Merge(o.Order) || c
	c = g.Status.Merge(o.Status) || c
	c = g.Deleted.Merge(o.Deleted) || c
	return c
}

func (g *GoalRecord) stamps(fn func(stamp)) {
	for _, s := range []stamp{g.Scope.S, g.Title.S, g.Order.S, g.Status.S, g.Deleted.S} {
		fn(s)
	}
}

func (m *MilestoneRecord) merge(o *MilestoneRecord) bool {
	c := m.Scope.Merge(o.Scope)
	c = m.Goal.Merge(o.Goal) || c
	c = m.Title.Merge(o.Title) || c
	c = m.Status.Merge(o.Status) || c
	c = m.Order.Merge(o.Order) || c
	c = m.Due.Merge(o.Due) || c
	c = m.AutoFile.Merge(o.AutoFile) || c
	c = m.Created.Merge(o.Created) || c
	c = m.Deleted.Merge(o.Deleted) || c
	return mergeItems(&m.Items, o.Items) || c
}

func (m *MilestoneRecord) stamps(fn func(stamp)) {
	for _, s := range []stamp{m.Scope.S, m.Goal.S, m.Title.S, m.Status.S, m.Order.S, m.Due.S, m.AutoFile.S, m.Created.S, m.Deleted.S} {
		fn(s)
	}
	for _, it := range m.Items {
		it.stamps(fn)
	}
}

func (m *MilestoneRecord) clone() *MilestoneRecord {
	c := *m
	c.Items = make(map[string]*ItemRecord, len(m.Items))
	for k, v := range m.Items {
		c.Items[k] = v.clone()
	}
	return &c
}

func (s *StewardRecord) merge(o *StewardRecord) bool {
	c := s.Enabled.Merge(o.Enabled)
	c = s.Level.Merge(o.Level) || c
	c = s.Node.Merge(o.Node) || c
	return c
}

func (s *StewardRecord) stamps(fn func(stamp)) {
	fn(s.Enabled.S)
	fn(s.Level.S)
	fn(s.Node.S)
}

func (s *SettingsRecord) merge(o *SettingsRecord) bool {
	c := s.LoopLimit.Merge(o.LoopLimit)
	c = s.StallDays.Merge(o.StallDays) || c
	c = s.EveningAt.Merge(o.EveningAt) || c
	c = s.MaxWakesPerDay.Merge(o.MaxWakesPerDay) || c
	return c
}

func (s *SettingsRecord) stamps(fn func(stamp)) {
	fn(s.LoopLimit.S)
	fn(s.StallDays.S)
	fn(s.EveningAt.S)
	fn(s.MaxWakesPerDay.S)
}

func (r *Record) stamps(fn func(stamp)) {
	switch {
	case r.Loop != nil:
		r.Loop.stamps(fn)
	case r.Goal != nil:
		r.Goal.stamps(fn)
	case r.Milestone != nil:
		r.Milestone.stamps(fn)
	case r.Steward != nil:
		r.Steward.stamps(fn)
	case r.Settings != nil:
		r.Settings.stamps(fn)
	}
}

// updated is the newest write's time in a record (zero: none).
func updated(fn func(func(stamp))) time.Time {
	var t int64
	fn(func(s stamp) {
		if s.T > t {
			t = s.T
		}
	})
	if t == 0 {
		return time.Time{}
	}
	return time.UnixMilli(t).UTC()
}
