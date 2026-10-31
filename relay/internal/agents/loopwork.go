package agents

import (
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
