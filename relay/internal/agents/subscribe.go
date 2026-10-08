package agents

import (
	"sync"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Subscriber receives agents.changed / agents.removed. Changes coalesce per
// agent (the latest state wins), so a slow client costs one entry per agent
// and never holds the registry up.
type Subscriber struct {
	mu      sync.Mutex
	pending map[string]*wire.Agent // nil: removed
	reasons map[string]string      // a removal's reason (closing agents)
	tos     map[string]string      // a moved agent's new id (move work)
	order   []string
	closed  bool
	wake    chan struct{}
	// moves (move work): agents.moving notes, for subscribers that take
	// them (SubscribeMoves); at most maxMoves, the oldest dropped.
	takesMoves bool
	moves      []wire.Moving
}

// maxMoves bounds a subscriber's queued agents.moving notes.
const maxMoves = 64

// Note is one notification: Agent set for agents.changed, Moving for
// agents.moving, else Removed with its Reason (wire.ReasonClosed, …; may
// be empty) and To (reason "moved").
type Note struct {
	Agent   *wire.Agent
	Moving  *wire.Moving
	Removed string
	Reason  string
	To      string
}

func (s *Subscriber) push(id string, a *wire.Agent) {
	s.pushReason(id, a, "", "")
}

// pushRemoved notes a removal with its reason.
func (s *Subscriber) pushRemoved(id, reason string) {
	s.pushReason(id, nil, reason, "")
}

// pushRemovedTo notes a removal with its reason and, for a move, the new
// agent's id.
func (s *Subscriber) pushRemovedTo(id, reason, to string) {
	s.pushReason(id, nil, reason, to)
}

// pushMoving queues a move's progress (subscribers that take them).
func (s *Subscriber) pushMoving(m wire.Moving) {
	s.mu.Lock()
	if s.closed || !s.takesMoves {
		s.mu.Unlock()
		return
	}
	if len(s.moves) >= maxMoves {
		s.moves = s.moves[1:]
	}
	s.moves = append(s.moves, m)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscriber) pushReason(id string, a *wire.Agent, reason, to string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if _, ok := s.pending[id]; !ok {
		s.order = append(s.order, id)
	}
	s.pending[id] = a
	if a == nil && reason != "" {
		s.reasons[id] = reason
	} else {
		delete(s.reasons, id)
	}
	if a == nil && to != "" {
		s.tos[id] = to
	} else {
		delete(s.tos, id)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscriber) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Wait returns the next notifications, nil once the subscriber is closed.
func (s *Subscriber) Wait(done <-chan struct{}) []Note {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil
		}
		if len(s.order) > 0 || len(s.moves) > 0 {
			notes := make([]Note, 0, len(s.order)+len(s.moves))
			for i := range s.moves {
				notes = append(notes, Note{Moving: &s.moves[i]})
			}
			for _, id := range s.order {
				if a := s.pending[id]; a != nil {
					notes = append(notes, Note{Agent: a})
				} else {
					notes = append(notes, Note{Removed: id, Reason: s.reasons[id], To: s.tos[id]})
				}
			}
			s.order, s.pending, s.reasons, s.tos, s.moves = nil, map[string]*wire.Agent{}, map[string]string{}, map[string]string{}, nil
			s.mu.Unlock()
			return notes
		}
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-done:
			return nil
		}
	}
}

// Subscribe registers a subscriber; its first notifications are every agent.
func (r *Registry) Subscribe() *Subscriber {
	return r.subscribe(false)
}

// SubscribeMoves is Subscribe with agents.moving notes (Note.Moving)
// too.
func (r *Registry) SubscribeMoves() *Subscriber {
	return r.subscribe(true)
}

// NoteMoving tells the subscribers that take them a move's progress
// (agents.moving; the controller's internal/remote sends them).
func (r *Registry) NoteMoving(m wire.Moving) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for s := range r.subs {
		s.pushMoving(m)
	}
}

func (r *Registry) subscribe(moves bool) *Subscriber {
	s := &Subscriber{pending: map[string]*wire.Agent{}, reasons: map[string]string{}, tos: map[string]string{}, wake: make(chan struct{}, 1),
		takesMoves: moves}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.listLocked() {
		s.push(a.ID, &a)
	}
	if r.closing {
		s.closed = true
		return s
	}
	r.subs[s] = struct{}{}
	return s
}

// Unsubscribe ends a subscription.
func (r *Registry) Unsubscribe(s *Subscriber) {
	r.mu.Lock()
	delete(r.subs, s)
	r.mu.Unlock()
	s.close()
}
