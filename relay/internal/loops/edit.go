package loops

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Writes: every one stamps the fields it changes with this Mac's clock;
// callers hold s.mu and commit the keys they changed.

func (s *Store) newLoopLocked(title, milestone string) *LoopRecord {
	now := s.opt.Now().UTC()
	l := &LoopRecord{ID: newID("lp-"), Links: map[string]*LinkRecord{}, Items: map[string]*ItemRecord{}}
	for s.loops[l.ID] != nil {
		l.ID = newID("lp-")
	}
	l.Title.Set(clip(title, maxShortText), s.clock.Tick())
	l.Status.Set(wire.LoopOpen, s.clock.Tick())
	l.Created.Set(now, s.clock.Tick())
	if milestone != "" {
		l.Milestone.Set(milestone, s.clock.Tick())
	}
	s.loops[l.ID] = l
	return l
}

// addLinkLocked adds (or revives) a link; an existing one gets the label
// when it has none.
func (s *Store) addLinkLocked(l *LoopRecord, v wire.LoopLink) (*LinkRecord, bool) {
	key := linkKey(v.Kind, v.Rel, v.Ref, v.From)
	k := l.Links[key]
	changed := false
	if k == nil {
		k = &LinkRecord{}
		k.Kind.Set(v.Kind, s.clock.Tick())
		k.Ref.Set(v.Ref, s.clock.Tick())
		k.Rel.Set(v.Rel, s.clock.Tick())
		k.From.Set(v.From, s.clock.Tick())
		k.Added.Set(s.opt.Now().UTC(), s.clock.Tick())
		l.Links[key], changed = k, true
	} else if k.Deleted.V {
		changed = k.Deleted.Set(false, s.clock.Tick())
	}
	if v.Label != "" && k.Label.V == "" {
		changed = k.Label.Set(clip(v.Label, maxShortText), s.clock.Tick()) || changed
	}
	return k, changed
}

// leaveOthersLocked tombstones ref's member links in every loop but l:
// the newest membership wins, an agent (or session) counts in one loop.
// It returns the keys of the loops it changed (to commit).
func (s *Store) leaveOthersLocked(l *LoopRecord, ref string) []string {
	var keys []string
	for id := range s.indexLocked().members[ref] {
		o := s.loops[id]
		if id == l.ID || o == nil {
			continue
		}
		changed := false
		for _, k := range o.Links {
			if member(k) && k.Ref.V == ref {
				changed = k.Deleted.Set(true, s.clock.Tick()) || changed
			}
		}
		if changed {
			keys = append(keys, "l:"+id)
		}
	}
	return keys
}

// removeLinksLocked tombstones the links of a loop with this kind and
// ref (and rel, when given).
func (s *Store) removeLinksLocked(l *LoopRecord, kind, ref, rel string) bool {
	changed := false
	for _, k := range l.Links {
		if !k.Deleted.V && k.Kind.V == kind && k.Ref.V == ref && (rel == "" || k.Rel.V == rel) {
			changed = k.Deleted.Set(true, s.clock.Tick()) || changed
		}
	}
	return changed
}

// memberLink is the newest live membership link of ref in l.
func memberLink(l *LoopRecord, ref string) *LinkRecord {
	var best *LinkRecord
	for _, k := range l.Links {
		if member(k) && k.Ref.V == ref && (best == nil || best.Added.V.Before(k.Added.V)) {
			best = k
		}
	}
	return best
}

// itemLocked adds (v.ID empty) or changes an item of items: only the
// fields given (non-zero) change; a check with an empty kind clears the
// item's check. It returns the item's id.
func (s *Store) itemLocked(items map[string]*ItemRecord, v wire.LoopItem, defKind string) (string, bool, error) {
	if len(v.Text) > maxText || len(v.After) > maxAfter {
		return "", false, wire.Errorf(wire.CodeInvalid, "item too long")
	}
	if !oneOf(v.Owner, "", wire.OwnerYou, wire.OwnerAgent, wire.OwnerOthers) || !oneOf(v.State, "", wire.ItemOpen, wire.ItemDone, wire.ItemDropped) ||
		!oneOf(v.Kind, "", wire.ItemStep, wire.ItemLooseEnd, wire.ItemPromise, wire.ItemQuestion, wire.ItemOffer, wire.ItemCondition) {
		return "", false, wire.Errorf(wire.CodeInvalid, "unknown item owner, state or kind")
	}
	if c := v.Check; c != nil && !oneOf(c.Kind, "", wire.CheckPR, wire.CheckTag, wire.CheckURL, wire.CheckReview, wire.CheckLoop) {
		return "", false, wire.Errorf(wire.CodeInvalid, "unknown check kind %q", c.Kind)
	}
	now := s.opt.Now().UTC()
	it := items[v.ID]
	changed := false
	if v.ID == "" {
		if strings.TrimSpace(v.Text) == "" {
			return "", false, wire.Errorf(wire.CodeInvalid, "an item needs text")
		}
		v.ID = newID("it-")
		for items[v.ID] != nil {
			v.ID = newID("it-")
		}
		if v.Owner == "" {
			v.Owner = wire.OwnerYou
		}
		if v.State == "" {
			v.State = wire.ItemOpen
		}
		if v.Kind == "" {
			v.Kind = defKind
		}
		if v.Order == 0 {
			for _, o := range items {
				if !o.Deleted.V && o.Order.V >= v.Order {
					v.Order = o.Order.V + 1
				}
			}
			if v.Order == 0 {
				v.Order = 1
			}
		}
		it = &ItemRecord{}
		it.Created.Set(now, s.clock.Tick())
		items[v.ID], changed = it, true
	} else if it == nil || it.Deleted.V {
		return "", false, wire.Errorf(wire.CodeNotFound, "no item %s", v.ID)
	}
	if v.Text != "" {
		changed = it.Text.Set(v.Text, s.clock.Tick()) || changed
	}
	if v.Owner != "" {
		changed = it.Owner.Set(v.Owner, s.clock.Tick()) || changed
	}
	if v.State != "" && it.State.V != v.State {
		changed = it.State.Set(v.State, s.clock.Tick()) || changed
		done := time.Time{}
		if v.State != wire.ItemOpen {
			done = now
		}
		it.Done.Set(done, s.clock.Tick())
	}
	if v.Kind != "" {
		changed = it.Kind.Set(v.Kind, s.clock.Tick()) || changed
	}
	if v.Order != 0 {
		changed = it.Order.Set(v.Order, s.clock.Tick()) || changed
	}
	if v.After != nil {
		changed = it.After.Set(v.After, s.clock.Tick()) || changed
	}
	if v.Draft != "" {
		changed = it.Draft.Set(v.Draft, s.clock.Tick()) || changed
	}
	if v.Source != "" {
		changed = it.Source.Set(v.Source, s.clock.Tick()) || changed
	}
	switch {
	case v.Check != nil && v.Check.Kind == "":
		changed = it.Check.Set(nil, s.clock.Tick()) || changed
	case v.Check != nil:
		c := *v.Check
		changed = it.Check.Set(&c, s.clock.Tick()) || changed
	}
	return v.ID, changed, nil
}

func (s *Store) removeItemLocked(items map[string]*ItemRecord, id string) (bool, error) {
	it := items[id]
	if it == nil || it.Deleted.V {
		return false, wire.Errorf(wire.CodeNotFound, "no item %s", id)
	}
	return it.Deleted.Set(true, s.clock.Tick()), nil
}

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
