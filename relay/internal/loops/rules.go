package loops

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Rules: what views compute from the records and the live agents, in one
// place (the app only sorts and draws). Pure functions of a built view.

// applyRules fills every view's computed fields. A loop that was not
// built (b.brief) still counts as a blocker and under its milestone.
func applyRules(b *built, settings wire.LoopSettings, now time.Time) {
	others := b.loops
	if len(b.brief) > 0 {
		others = make(map[string]wire.Loop, len(b.loops)+len(b.brief))
		for id, l := range b.brief {
			others[id] = l
		}
		for id, l := range b.loops {
			others[id] = l
		}
	}
	for id, l := range b.loops {
		markReady(l.Items)
		l.Counts = counts(&l)
		l.Projects = projectsOf(&l)
		l.Next, l.NextText, l.LooksDone = next(&l, blockers(others, &l))
		b.loops[id] = l
	}
	for id, m := range b.milestones {
		markReady(m.Items)
		m.Loops = milestoneLoops(others, id)
		m.Done, m.Total = progress(m.Items)
		m.NextText = milestoneNext(&m)
		m.Stalled = stalled(&m, others, settings.StallDays, now)
		b.milestones[id] = m
	}
}

// markReady: an open item is ready when every item it comes after is
// done (or gone).
func markReady(items []wire.LoopItem) {
	state := make(map[string]string, len(items))
	for _, it := range items {
		state[it.ID] = it.State
	}
	for i := range items {
		it := &items[i]
		it.Ready = it.State == wire.ItemOpen
		for _, a := range it.After {
			if st, ok := state[a]; ok && st != wire.ItemDone {
				it.Ready = false
			}
		}
	}
}

// memberAgent: an agent link that puts the agent in the loop.
func memberAgent(k *wire.LoopLink) bool {
	return k.Kind == wire.LinkAgent && (k.Rel == wire.RelMember || k.Rel == wire.RelStarted || k.Rel == wire.RelContinued)
}

func working(state string) bool { return state == wire.StateWorking || state == wire.StateStarting }

func counts(l *wire.Loop) wire.LoopCounts {
	var c wire.LoopCounts
	for i := range l.Links {
		k := &l.Links[i]
		switch {
		case memberAgent(k) && k.State != "":
			c.Agents++
			if working(k.State) {
				c.Working++
			}
		case k.Kind == wire.LinkSession && (k.Rel == wire.RelMember || k.Rel == wire.RelContinued):
			c.Sessions++
		}
	}
	for _, it := range l.Items {
		if it.State != wire.ItemOpen || it.Kind == wire.ItemOffer {
			continue // offers are dimmed, never counted
		}
		switch it.Owner {
		case wire.OwnerYou:
			c.OpenYou++
		case wire.OwnerAgent:
			c.OpenAgents++
		case wire.OwnerOthers:
			c.OpenOthers++
		}
	}
	return c
}

// projectsOf are the projects a loop touches (sorted, scratch folders
// left out).
func projectsOf(l *wire.Loop) []string {
	set := map[string]bool{}
	for _, k := range l.Links {
		if k.Kind == wire.LinkProject && k.Rel == wire.RelTouches && k.Ref != "" && !strings.HasPrefix(k.Ref, wire.ScratchPrefix) {
			set[k.Ref] = true
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// blockers are the loops l waits for (blockedBy links), merges followed.
func blockers(loops map[string]wire.Loop, l *wire.Loop) []wire.Loop {
	var out []wire.Loop
	for _, k := range l.Links {
		if k.Kind != wire.LinkLoop || k.Rel != wire.RelBlockedBy {
			continue
		}
		id := k.Ref
		for range 16 {
			o, ok := loops[id]
			if !ok || o.MergedInto == "" {
				break
			}
			id = o.MergedInto
		}
		if o, ok := loops[id]; ok && id != l.ID {
			out = append(out, o)
		}
	}
	return out
}

func open(status string) bool {
	return status == wire.LoopOpen || status == wire.LoopParked || status == ""
}

// firstItem is the first open item of owner (ready ones first; offers
// never count).
func firstItem(items []wire.LoopItem, owner string) (wire.LoopItem, bool) {
	var found wire.LoopItem
	ok := false
	for _, it := range items {
		if it.State != wire.ItemOpen || it.Owner != owner || it.Kind == wire.ItemOffer {
			continue
		}
		if it.Ready {
			return it, true
		}
		if !ok {
			found, ok = it, true
		}
	}
	return found, ok
}

func labelOf(k *wire.LoopLink) string {
	if k.Label != "" {
		return k.Label
	}
	if k.Agent != "" {
		return k.Agent
	}
	return "An agent"
}

// next is who acts next on a loop and what, in this order:
//  1. parked, or closed (done, dropped);
//  2. the person, when a member agent waits in approval or question, a
//     quiet member's last turn asks them something or names their step,
//     or an item is open for them;
//  3. agents, when a member works;
//  4. others, when an item is open for others or the loop waits for an
//     open loop;
//  5. otherwise the person, with looksDone.
//
// A NextText written on the loop (loops.save) stands in for the text of
// a parked loop and when nothing else says what.
func next(l *wire.Loop, blockedBy []wire.Loop) (who, text string, looksDone bool) {
	or := func(s string) string {
		if s == "" {
			return l.NextText
		}
		return s
	}
	switch l.Status {
	case wire.LoopParked:
		return wire.NextParked, l.NextText, false
	case wire.LoopDone, wire.LoopDropped:
		return wire.NextClosed, l.NextText, false
	}
	var workers []*wire.LoopLink
	for i := range l.Links {
		k := &l.Links[i]
		if !memberAgent(k) || k.State == "" {
			continue // gone agents never act
		}
		switch {
		case k.State == wire.StateApproval:
			return wire.NextYou, labelOf(k) + " waits for your approval", false
		case k.State == wire.StateQuestion:
			return wire.NextYou, labelOf(k) + " asks you something", false
		case working(k.State):
			workers = append(workers, k)
		}
	}
	for i := range l.Links {
		k := &l.Links[i]
		if !memberAgent(k) || k.State == "" || working(k.State) {
			continue
		}
		if k.Ending == wire.EndingAsks || k.Ending == wire.EndingYourStep {
			if k.Summary != "" {
				return wire.NextYou, k.Summary, false
			}
			if k.Ending == wire.EndingAsks {
				return wire.NextYou, labelOf(k) + " asks you something", false
			}
			return wire.NextYou, labelOf(k) + " left a step for you", false
		}
	}
	if it, ok := firstItem(l.Items, wire.OwnerYou); ok {
		return wire.NextYou, it.Text, false
	}
	switch len(workers) {
	case 0:
	case 1:
		return wire.NextAgents, or(labelOf(workers[0]) + " works"), false
	default:
		return wire.NextAgents, or(strconv.Itoa(len(workers)) + " agents work"), false
	}
	if it, ok := firstItem(l.Items, wire.OwnerOthers); ok {
		return wire.NextOthers, it.Text, false
	}
	for _, o := range blockedBy {
		if open(o.Status) {
			return wire.NextOthers, "Waits for " + o.Title, false
		}
	}
	return wire.NextYou, "Looks done", true
}

// milestoneLoops are the loops filed under a milestone (merged ones left
// out), sorted.
func milestoneLoops(loops map[string]wire.Loop, ms string) []string {
	out := []string{}
	for id, l := range loops {
		if l.Milestone == ms && l.MergedInto == "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// progress: conditions done of those not dropped.
func progress(items []wire.LoopItem) (done, total int) {
	for _, it := range items {
		if it.State == wire.ItemDropped {
			continue
		}
		total++
		if it.State == wire.ItemDone {
			done++
		}
	}
	return done, total
}

func milestoneNext(m *wire.Milestone) string {
	for _, it := range m.Items {
		if it.State == wire.ItemOpen && it.Ready {
			return it.Text
		}
	}
	for _, it := range m.Items {
		if it.State == wire.ItemOpen {
			return it.Text
		}
	}
	if m.Total > 0 && m.Done == m.Total && m.Status != wire.LoopDone {
		return "Every condition is done"
	}
	return ""
}

// stalled: an open milestone where nothing (itself, its loops) changed in
// stallDays.
func stalled(m *wire.Milestone, loops map[string]wire.Loop, stallDays int, now time.Time) bool {
	if m.Status != wire.LoopOpen || stallDays <= 0 {
		return false
	}
	last := m.Updated
	for _, id := range m.Loops {
		if u := loops[id].Updated; u.After(last) {
			last = u
		}
	}
	return !last.IsZero() && now.Sub(last) > time.Duration(stallDays)*24*time.Hour
}
