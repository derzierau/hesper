package loops

import "github.com/derzierau/hesper/relay/pkg/wire"

// Merging another Mac's records (sync.go): per record, per field the
// newer write wins; tombstones stay. Records that are not sane are
// dropped whole.

// Merge folds records from another Mac in and reports whether anything
// changed.
func (s *Store) Merge(recs []Record) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for i := range recs {
		r := &recs[i]
		if !validRecord(r) {
			continue
		}
		r.stamps(s.clock.Observe)
		if key, ok := s.mergeLocked(r); ok {
			keys = append(keys, key)
		}
	}
	keys = append(keys, s.breakCyclesLocked()...)
	s.commitLocked(keys...)
	return len(keys) > 0
}

func (s *Store) mergeLocked(r *Record) (string, bool) {
	switch {
	case r.Loop != nil:
		o := r.Loop
		cur := s.loops[o.ID]
		if cur == nil {
			c := o.clone()
			c.normalize()
			s.loops[o.ID] = c
			return "l:" + o.ID, true
		}
		return "l:" + o.ID, cur.merge(o)
	case r.Goal != nil:
		o := r.Goal
		cur := s.goals[o.ID]
		if cur == nil {
			c := *o
			s.goals[o.ID] = &c
			return "g:" + o.ID, true
		}
		return "g:" + o.ID, cur.merge(o)
	case r.Milestone != nil:
		o := r.Milestone
		cur := s.milestones[o.ID]
		if cur == nil {
			s.milestones[o.ID] = o.clone()
			return "m:" + o.ID, true
		}
		return "m:" + o.ID, cur.merge(o)
	case r.Steward != nil:
		return keySteward, s.steward.merge(r.Steward)
	case r.Settings != nil:
		return keySettings, s.settings.merge(r.Settings)
	}
	return "", false
}

// recordLocked is the record of a key (nil: none).
func (s *Store) recordLocked(key string) *Record {
	switch {
	case key == keySteward:
		c := s.steward
		return &Record{Steward: &c}
	case key == keySettings:
		c := s.settings
		return &Record{Settings: &c}
	case len(key) < 3:
		return nil
	case key[0] == 'l':
		if l := s.loops[key[2:]]; l != nil {
			return &Record{Loop: l.clone()}
		}
	case key[0] == 'g':
		if g := s.goals[key[2:]]; g != nil {
			c := *g
			return &Record{Goal: &c}
		}
	case key[0] == 'm':
		if m := s.milestones[key[2:]]; m != nil {
			return &Record{Milestone: m.clone()}
		}
	}
	return nil
}

// Bounds of what a peer may send.
const (
	maxText      = 4 << 10
	maxRefLen    = 4 << 10
	maxEntries   = 10000 // links or items of one record
	maxAfter     = 64
	maxShortText = 200
)

// validRecord: exactly one record, a well-formed id, bounded strings and
// maps, known enum values.
func validRecord(r *Record) bool {
	n := 0
	for _, set := range []bool{r.Loop != nil, r.Goal != nil, r.Milestone != nil, r.Steward != nil, r.Settings != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return false
	}
	switch {
	case r.Loop != nil:
		l := r.Loop
		if !validID(l.ID, "lp-") || len(l.Title.V) > maxText || len(l.Goal.V) > maxText || len(l.DoneWhen.V) > maxText ||
			len(l.NextText.V) > maxText || !oneOf(l.Status.V, "", wire.LoopOpen, wire.LoopParked, wire.LoopDone, wire.LoopDropped) ||
			l.Milestone.V != "" && !validID(l.Milestone.V, "ms-") || l.MergedInto.V != "" && !validID(l.MergedInto.V, "lp-") ||
			len(l.Links) > maxEntries || len(l.Items) > maxEntries {
			return false
		}
		for k, v := range l.Links {
			if v == nil || len(k) > 64 || !validLink(v) {
				return false
			}
		}
		return validItems(l.Items)
	case r.Goal != nil:
		g := r.Goal
		return validID(g.ID, "gl-") && len(g.Scope.V) <= maxShortText && len(g.Title.V) <= maxText && len(g.Status.V) <= 32
	case r.Milestone != nil:
		m := r.Milestone
		if !validID(m.ID, "ms-") || len(m.Scope.V) > maxShortText || len(m.Title.V) > maxText || m.Goal.V != "" && !validID(m.Goal.V, "gl-") ||
			!oneOf(m.Status.V, "", wire.MilestoneProposed, wire.LoopOpen, wire.LoopDone, wire.LoopDropped) || len(m.Items) > maxEntries {
			return false
		}
		return validItems(m.Items)
	case r.Steward != nil:
		return oneOf(r.Steward.Level.V, "", wire.StewardWatch, wire.StewardSuggest, wire.StewardAct) && len(r.Steward.Node.V) <= 64
	}
	st := r.Settings
	return st.LoopLimit.V >= 0 && st.StallDays.V >= 0 && st.MaxWakesPerDay.V >= 0 && len(st.EveningAt.V) <= 5
}

func validLink(k *LinkRecord) bool {
	return oneOf(k.Kind.V, wire.LinkAgent, wire.LinkSession, wire.LinkProject, wire.LinkURL, wire.LinkFile, wire.LinkLoop) &&
		oneOf(k.Rel.V, wire.RelMember, wire.RelStarted, wire.RelContinued, wire.RelHandoff, wire.RelBlocks, wire.RelBlockedBy, wire.RelTouches) &&
		len(k.Ref.V) <= maxRefLen && len(k.From.V) <= maxRefLen && len(k.Label.V) <= maxText && len(k.Summary.V) <= maxText &&
		len(k.Result.V) <= resultMax && len(k.Ending.V) <= 32 && len(k.ClosedReason.V) <= 64
}

func validItems(items map[string]*ItemRecord) bool {
	for id, it := range items {
		if it == nil || !validID(id, "it-") || !validItem(it) {
			return false
		}
	}
	return true
}

func validItem(it *ItemRecord) bool {
	if len(it.Text.V) > maxText || len(it.Draft.V) > maxShortText || len(it.Source.V) > maxRefLen || len(it.After.V) > maxAfter ||
		!oneOf(it.Owner.V, "", wire.OwnerYou, wire.OwnerAgent, wire.OwnerOthers) ||
		!oneOf(it.State.V, "", wire.ItemOpen, wire.ItemDone, wire.ItemDropped) ||
		!oneOf(it.Kind.V, "", wire.ItemStep, wire.ItemLooseEnd, wire.ItemPromise, wire.ItemQuestion, wire.ItemOffer, wire.ItemCondition) {
		return false
	}
	if c := it.Check.V; c != nil && (len(c.Ref) > maxRefLen ||
		!oneOf(c.Kind, wire.CheckPR, wire.CheckTag, wire.CheckURL, wire.CheckReview, wire.CheckLoop)) {
		return false
	}
	return true
}

func oneOf(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}
