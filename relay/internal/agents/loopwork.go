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
	return r.spawn(p, func(a *agent) { a.Service = service })
}
