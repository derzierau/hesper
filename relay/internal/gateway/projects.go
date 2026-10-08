package gateway

import (
	"context"
	"encoding/json"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// wireProjects connects the project registry (projects step 1) to the
// agents: this Mac's name, re-resolving agents when projects change, the
// agents for scratch projects, and promote on other machines.
func (d *Daemon) wireProjects() {
	store, reg, fleet := d.Projects, d.Registry, d.Fleet
	store.SetMachine(reg.Machine())
	store.SetReproject(reg.Reproject)
	store.SetAgents(func() []wire.Agent {
		list := reg.List()
		if fleet != nil {
			list = append(list, fleet.Agents()...)
		}
		return list
	})
	if fleet != nil {
		store.SetForward(func(ctx context.Context, machine, method string, params any) (json.RawMessage, error) {
			raw, err := json.Marshal(params)
			if err != nil {
				return nil, err
			}
			return fleet.Call(ctx, machine, method, raw)
		})
	}
	// Agents restored before the store knew this Mac's name keep their
	// project; anything resolved meanwhile is settled once more.
	go reg.Reproject()
	// Scratch projects: adoption and the lifecycle, once the agents are
	// known (a scratch with agents is never archived).
	store.StartScratch()
}
