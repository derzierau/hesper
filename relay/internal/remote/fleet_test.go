package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// testHost is a host this Mac calls L2 that calls itself L, linked.
func testHost() (*machine, *link) {
	m := &machine{short: "L2", agents: map[string]wire.Agent{}}
	l := &link{m: m}
	m.link = l
	return m, l
}

// A host names the parents of the agents its agents start in its own
// naming: those are renamed with the agent; a parent this Mac sent (in
// its own naming, both Macs may be "L") is kept.
func TestFleetNamesHostParents(t *testing.T) {
	f := testFleet(t)
	m, l := testHost()
	f.event(l, agentlink.Event{Full: true, Machine: "L", List: []wire.Agent{
		{ID: "L/child1", Parent: "L/root01"}, // its parent comes later in the list
		{ID: "L/root01"},
		{ID: "L/sent01", Parent: "L/ctrl01"}, // started by this Mac's agent
	}})
	f.event(l, agentlink.Event{Changed: &wire.Agent{ID: "L/child2", Parent: "L/child1"}})
	for local, want := range map[string]string{"child1": "L2/root01", "root01": "", "sent01": "L/ctrl01", "child2": "L2/child1"} {
		if a := m.agents[local]; a.ID != "L2/"+local || a.Parent != want {
			t.Errorf("%s: %s with parent %q, want parent %q", local, a.ID, a.Parent, want)
		}
	}
	f.renameLocked(m, "L3")
	for local, want := range map[string]string{"child1": "L3/root01", "sent01": "L/ctrl01", "child2": "L3/child1"} {
		if a := m.agents[local]; a.Parent != want {
			t.Errorf("renamed %s: parent %q, want %q", local, a.Parent, want)
		}
	}
}

// A move from a host carries the parent in this Mac's naming.
func TestMoveCarriesHostParent(t *testing.T) {
	m, _ := testHost()
	m.hostShort = "L"
	m.agents["root01"] = wire.Agent{ID: "L2/root01"}
	for parent, want := range map[string]string{"L/root01": "L2/root01", "L/ctrl01": "L/ctrl01", "": ""} {
		bundle := t.TempDir()
		data, _ := json.Marshal(handoff.Manifest{Agent: handoff.AgentInfo{LocalID: "child1", Parent: parent, Depth: 1}})
		if err := os.WriteFile(filepath.Join(bundle, handoff.ManifestFile), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := markMove(bundle, handoff.MoveInfo{From: "L2", To: "L"}, m.hostParent); err != nil {
			t.Fatal(err)
		}
		data, _ = os.ReadFile(filepath.Join(bundle, handoff.ManifestFile))
		var got handoff.Manifest
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Agent.Parent != want || got.Agent.LocalID != "child1" || got.Agent.Depth != 1 || got.Move == nil || got.Move.From != "L2" {
			t.Errorf("parent %q: carried %+v, move %+v; want parent %q", parent, got.Agent, got.Move, want)
		}
	}
}
