package gateway

import (
	"context"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// wireLoops connects the loops store (internal/loops) to the agents:
// this Mac's name, the live agents its views join in (local and remote),
// machine names to nodes and back (the linked hosts' own, else the
// project registry's, which every Mac shares), the shared history's node for session refs, and a rebuild
// of its views whenever an agent changes.
func (d *Daemon) wireLoops(ctx context.Context) {
	store, reg, fleet, proj := d.Loops, d.Registry, d.Fleet, d.Projects
	store.SetMachine(reg.Machine())
	nodeOf, machineOf := proj.NodeOf, proj.MachineOf
	if fleet != nil {
		nodeOf = func(machine string) string {
			if n := fleet.NodeOf(machine); n != "" {
				return n
			}
			return proj.NodeOf(machine)
		}
		machineOf = func(node string) string {
			if m := fleet.MachineOf(node); m != "" {
				return m
			}
			return proj.MachineOf(node)
		}
	}
	store.SetNames(nodeOf, machineOf)
	if d.History != nil {
		store.SetSessionsNode(d.History.Node)
	}
	store.SetAgents(func() []wire.Agent {
		list := reg.List()
		if fleet != nil {
			list = append(list, fleet.Agents()...)
		}
		return list
	})
	sub := reg.Subscribe()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer reg.Unsubscribe(sub)
		for sub.Wait(ctx.Done()) != nil {
			store.AgentsChanged()
		}
	}()
	if fleet != nil {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			fleet.Watch(ctx, func(wire.Agent) { store.AgentsChanged() }, func(string, string, string) { store.AgentsChanged() })
		}()
	}
}

func (d *Daemon) closeLoops() {
	if d.Loops != nil {
		d.Loops.Close()
	}
}
