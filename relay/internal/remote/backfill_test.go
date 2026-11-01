package remote

import (
	"testing"

	"github.com/derzierau/hesper/relay/internal/loops"
	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// A host without loops (an older hesperd) has its agents put in loops by
// this Mac once it is clear it keeps none itself: parents first, so a
// child shares its parent's loop; a host with loops is left alone.
func TestBackfillLoopsOfOlderHost(t *testing.T) {
	f := testFleet(t)
	f.opt.Loops = loops.Open(loops.Options{Node: "n-here", Machine: "L", Logf: t.Logf})
	t.Cleanup(f.opt.Loops.Close)
	m, l := testHost()
	m.node = "n-host"
	f.machines = map[string]*machine{"h": m}
	f.event(l, agentlink.Event{Full: true, Machine: "L", List: []wire.Agent{
		{ID: "L/child1", Parent: "L/root01", Depth: 1},
		{ID: "L/root01"},
	}})
	if got := f.opt.Loops.LoopOf("n-host/root01"); got != "" {
		t.Fatalf("assigned before the host had time to say it keeps loops: %q", got)
	}
	f.mu.Lock()
	f.startBackfillLocked(m)
	f.mu.Unlock()
	root, child := f.opt.Loops.LoopOf("n-host/root01"), f.opt.Loops.LoopOf("n-host/child1")
	if root == "" || child != root {
		t.Fatalf("root %q child %q", root, child)
	}
	if a := m.agents["child1"]; a.Loop != root {
		t.Fatalf("child's Agent.Loop %q, want %q", a.Loop, root)
	}
	// A new agent on that host gets a loop when it shows up.
	f.event(l, agentlink.Event{Changed: &wire.Agent{ID: "L/later1"}})
	if f.opt.Loops.LoopOf("n-host/later1") == "" {
		t.Fatal("a later agent of the older host got no loop")
	}

	// A host that keeps loops itself: nothing is assigned here.
	m2, l2 := testHost()
	m2.short, m2.node, m2.loops = "L3", "n-other", true
	f.machines["h2"] = m2
	f.event(l2, agentlink.Event{Full: true, Machine: "L", List: []wire.Agent{{ID: "L/own001"}}})
	f.mu.Lock()
	f.startBackfillLocked(m2)
	f.mu.Unlock()
	if got := f.opt.Loops.LoopOf("n-other/own001"); got != "" {
		t.Fatalf("assigned an agent of a host with loops: %q", got)
	}
}
