package steward

import (
	"strconv"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The wake queue: events wait here, one per agent (or loop), until the
// Steward is quiet; then hesperd types one fixed line into it, at most
// once a minute and LoopSettings.MaxWakesPerDay times a day (counted from
// local midnight). steward.inbox hands it the events and empties the
// queue. Events of service agents (the Steward's own work, its planners)
// never queue.

// WakeLine is what a wake types.
func WakeLine(n int) string {
	return "[hesper] " + strconv.Itoa(n) + " new events. Run: hesperctl steward inbox"
}

// AgentSettled queues a settled turn (agents.Steward).
func (s *Steward) AgentSettled(e agents.AgentEvent) {
	if e.Agent.Service != "" {
		s.poke() // the Steward's own turn ended: maybe wake it again
		return
	}
	s.queueEvent("agent:"+e.Agent.ID, wire.StewardEvent{Kind: wire.StewardSettled, Agent: e.Agent.ID, Loop: e.Agent.Loop,
		Ending: e.Ending, Summary: e.Agent.Summary})
}

// AgentClosed queues a closed agent (agents.Steward).
func (s *Steward) AgentClosed(e agents.AgentEvent) {
	if e.Agent.Service != "" {
		s.poke()
		return
	}
	s.queueEvent("agent:"+e.Agent.ID, wire.StewardEvent{Kind: wire.StewardClosed, Agent: e.Agent.ID, Loop: e.Agent.Loop,
		Ending: e.Ending, Summary: e.Agent.Summary, Reason: e.Reason})
}

// LoopCreated queues a new loop (the gateway watches the store).
func (s *Steward) LoopCreated(loop string) {
	s.queueEvent("loop:"+loop, wire.StewardEvent{Kind: wire.StewardLoopCreated, Loop: loop})
}

// ReviewChanged queues a review change of an agent's work (agent its id,
// loop its loop if known).
func (s *Steward) ReviewChanged(agent, loop string) {
	s.queueEvent("review:"+agent, wire.StewardEvent{Kind: wire.StewardReview, Agent: agent, Loop: loop})
}

// queueEvent adds e under key, replacing a waiting event of the same key
// (the latest wins, in the first one's place). Only while the Steward
// is on here: elsewhere its own Mac queues.
func (s *Steward) queueEvent(key string, e wire.StewardEvent) {
	e.At = s.opt.Now().UTC()
	s.mu.Lock()
	if !s.here {
		s.mu.Unlock()
		return
	}
	for i, k := range s.keys {
		if k == key {
			s.queue[i] = e
			s.mu.Unlock()
			s.poke()
			return
		}
	}
	if len(s.queue) >= maxEvents {
		s.queue, s.keys = s.queue[1:], s.keys[1:]
		s.dropped++
	}
	s.queue, s.keys = append(s.queue, e), append(s.keys, key)
	s.mu.Unlock()
	s.poke()
}

// Inbox is steward.inbox: the waiting events, oldest first; the queue is
// emptied.
func (s *Steward) Inbox() []wire.StewardEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queue
	if out == nil {
		out = []wire.StewardEvent{}
	}
	s.queue, s.keys = nil, nil
	s.notifyLocked()
	return out
}

// wake queues the evening event when its time came, rotates the session
// at the first wake of a new day and types the wake line when the
// Steward is quiet, a minute after the last wake and under the day's cap.
// It returns when to look again (zero: on the next event).
func (s *Steward) wake() time.Time {
	now := s.opt.Now()
	local := now.In(s.opt.Location)
	today := s.dayOf(now)
	settings := s.opt.Store.Settings()
	evening := eveningOn(local, settings.EveningAt)
	s.mu.Lock()
	if s.eveningDay == "" {
		// Turned on (or hesperd started) after this evening's time: the
		// first evening is tomorrow's.
		s.eveningDay = "-"
		if !local.Before(evening) {
			s.eveningDay = today
		}
	}
	due := !local.Before(evening) && s.eveningDay != today
	if due {
		s.eveningDay = today
	}
	s.mu.Unlock()
	if due {
		s.queueEvent("evening", wire.StewardEvent{Kind: wire.StewardEvening})
	}
	next := evening
	if !local.Before(evening) {
		next = eveningOn(local.AddDate(0, 0, 1), settings.EveningAt)
	}
	s.mu.Lock()
	if s.wakeDay != today {
		s.wakeDay, s.wakes = today, 0
	}
	n, quiet, id := len(s.queue), s.state == wire.StateDone || s.state == wire.StateIdle, s.agent
	rotate := n > 0 && s.day != "" && s.day != today
	limit := settings.MaxWakesPerDay
	if limit <= 0 {
		limit = 40
	}
	var at time.Time
	switch {
	case n == 0 || !quiet:
	case rotate:
	case now.Sub(s.lastWake) < wakeGap:
		at = s.lastWake.Add(wakeGap)
	case s.wakes >= limit:
		at = midnightAfter(local)
	default:
		s.wakes++
		s.lastWake = now
		s.state = wire.StateWorking // until the registry says otherwise
		s.notifyLocked()
	}
	reg := s.reg
	s.mu.Unlock()
	switch {
	case n == 0 || !quiet:
	case rotate:
		s.rotate(id) // brief.go
		return time.Time{}
	case at.IsZero():
		if err := reg.Input(wire.InputParams{ID: id, Text: WakeLine(n), Submit: true}); err != nil {
			s.opt.Logf("steward: waking %s: %v", id, err)
		}
	default:
		return earliest(at, next)
	}
	return next
}

// eveningOn is the evening time ("HH:MM", default 18:00) on day's date.
func eveningOn(day time.Time, at string) time.Time {
	h, m := 18, 0
	if hh, mm, ok := strings.Cut(at, ":"); ok {
		if x, err := strconv.Atoi(hh); err == nil && x >= 0 && x < 24 {
			if y, err := strconv.Atoi(mm); err == nil && y >= 0 && y < 60 {
				h, m = x, y
			}
		}
	}
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
}

func midnightAfter(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
}

func earliest(a, b time.Time) time.Time {
	if b.IsZero() || !a.IsZero() && a.Before(b) {
		return a
	}
	return b
}
