package agents

import (
	"sync"

	"github.com/derzierau/hesper/relay/internal/endings"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Loops in the registry (docs/rebuild-contract.md, "As built — loops
// (agents)"): service agents, putting agents in loops, how their turns
// end, the closed-work record and reports to a parent.

// SpawnService starts a service agent (wire.ServiceSteward,
// wire.ServicePlanner) for the Steward (internal/steward): Agent.Service
// is set before its process starts. A service agent is never put in a
// loop, never closed for finishing in the background nor by tidy, and
// its events are flagged (AgentEvent.Agent.Service) so the Steward never
// wakes on its own work.
func (r *Registry) SpawnService(p wire.SpawnParams, service string) (wire.Agent, error) {
	if service != wire.ServiceSteward && service != wire.ServicePlanner {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "service must be %s or %s", wire.ServiceSteward, wire.ServicePlanner)
	}
	return r.spawn(p, "", func(a *agent) { a.Service = service })
}

// view is an agent as the wire shows it (the lock is held): with the loop
// it works in, from the loops store (whose links are the truth; never
// kept in the registry nor persisted in agents.json).
func (r *Registry) view(a *agent) wire.Agent {
	v := a.Agent
	if l := r.opt.Loops; l != nil && v.Service == "" {
		v.Loop = l.LoopOf(r.ref(a.local))
	}
	return v
}

// assign puts a newly inserted agent in a loop (the lock is held): the
// three places an agent is inserted call it (Spawn, SpawnSession,
// Import). info carries what the insertion knows (the spawn's Loop and
// Milestone, the source session); the ref, the agent and its parent's
// ref (a parent on this Mac) are filled in here. Service agents get no
// loop.
func (r *Registry) assign(a *agent, info AssignInfo) {
	l := r.opt.Loops
	if l == nil || a.Service != "" {
		return
	}
	info.Ref, info.Agent, info.ParentRef = r.ref(a.local), a.Agent, r.parentRef(a.Parent)
	l.Assign(info)
}

// parentRef is the ref of parent (an agent id) when it is one of this
// Mac's agents' ids, else "": a parent on another Mac is linked by the
// controller.
func (r *Registry) parentRef(parent string) string {
	if parent == "" {
		return ""
	}
	if m, local := r.split(parent); m == r.machine {
		return r.ref(local)
	}
	return ""
}

// turnSettled: a's turn ended (setState; the lock is held). During a hook
// HookResult settles it once the hook's final message is in; otherwise
// (Codex's screen, a shell) now.
func (r *Registry) turnSettled(a *agent) {
	if r.hooking == a {
		a.settleDue = true
		return
	}
	r.settle(a, "")
}

// settle records how a's turn ended (the lock is held): ending from its
// hook's final message (HookResult classifies it before the lock), else
// from its last message when that came during this turn, else done (a
// turn that ended without words left nothing open). Agent.Ending is
// persisted; the loops store and the Steward hear of it off the lock.
func (r *Registry) settle(a *agent, ending string) {
	a.settleDue = false
	message := ""
	if a.lastMessageAt.After(a.turnStart) {
		message = a.lastMessage
		if ending == "" {
			ending, _ = endings.Classify(message)
		}
	}
	if ending == "" {
		ending = wire.EndingDone
	}
	if a.Ending != ending {
		a.Ending = ending
		r.changed(a)
	}
	r.events.push(loopEvent{AgentEvent: AgentEvent{Ref: r.ref(a.local), Agent: r.view(a), Ending: ending, Message: message}})
}

// closedWork records that a left the registry (removeLocked, the single
// exit; the lock is held): why, the agent it became (a move), its summary
// and last ending, and its final message.
func (r *Registry) closedWork(a *agent, reason, to string) {
	r.events.push(loopEvent{closed: true, AgentEvent: AgentEvent{Ref: r.ref(a.local), Agent: r.view(a), Ending: a.Ending,
		Message: a.lastMessage, Reason: reason, To: to}})
}

// maxLoopEvents bounds the queue of events for the loops store and the
// Steward: over it the oldest is dropped (and counted).
const maxLoopEvents = 256

type loopEvent struct {
	AgentEvent
	closed bool
}

// loopEvents hands settled and closed events to the loops store and the
// Steward from one goroutine, so that hooks (and the registry's lock)
// never wait on them. Its order is the registry's.
type loopEvents struct {
	loops   Loops
	steward Steward
	logf    func(string, ...any)

	mu      sync.Mutex
	q       []loopEvent
	dropped uint64
	wake    chan struct{}
	quit    chan struct{}
	done    chan struct{}
}

// newLoopEvents starts the queue's goroutine; with neither a store nor a
// Steward there is none and push does nothing.
func newLoopEvents(loops Loops, steward Steward, logf func(string, ...any)) *loopEvents {
	q := &loopEvents{loops: loops, steward: steward, logf: logf}
	if loops == nil && steward == nil {
		return q
	}
	q.wake, q.quit, q.done = make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	go q.run()
	return q
}

func (q *loopEvents) push(e loopEvent) {
	if q == nil || q.wake == nil {
		return
	}
	q.mu.Lock()
	if len(q.q) >= maxLoopEvents {
		q.q = q.q[1:]
		q.dropped++
		if q.dropped&(q.dropped-1) == 0 { // 1, 2, 4, …: never a line per event
			q.logf("hesperd: loops: %d events dropped (the loops store or the Steward is slow)", q.dropped)
		}
	}
	q.q = append(q.q, e)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Dropped is how many events the queue dropped.
func (q *loopEvents) Dropped() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

func (q *loopEvents) run() {
	defer close(q.done)
	for {
		stopping := false
		select {
		case <-q.wake:
		case <-q.quit:
			stopping = true
		}
		q.mu.Lock()
		batch := q.q
		q.q = nil
		q.mu.Unlock()
		for _, e := range batch {
			q.deliver(e)
		}
		if stopping {
			return
		}
	}
}

func (q *loopEvents) deliver(e loopEvent) {
	if e.closed {
		if q.loops != nil {
			q.loops.AgentClosed(e.AgentEvent)
		}
		if q.steward != nil {
			q.steward.AgentClosed(e.AgentEvent)
		}
		return
	}
	if q.loops != nil {
		q.loops.AgentSettled(e.AgentEvent)
	}
	if q.steward != nil {
		q.steward.AgentSettled(e.AgentEvent)
	}
}

// stop delivers what is queued and ends the goroutine (Registry.Close).
func (q *loopEvents) stop() {
	if q == nil || q.quit == nil {
		return
	}
	close(q.quit)
	<-q.done
}
