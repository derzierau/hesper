package transport_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func (n *node) loop(t *testing.T, id string) (wire.Loop, bool) {
	t.Helper()
	var l wire.Loop
	if err := n.call(t, wire.MethodLoopsGet, wire.IDParams{ID: id}, &l); err != nil {
		return wire.Loop{}, false
	}
	return l, true
}

func itemTexts(l wire.Loop) string {
	var out []string
	for _, it := range l.Items {
		out = append(out, it.Text)
	}
	return strings.Join(out, "|")
}

// A loop made on either Mac reaches the other; items added on both at
// once both survive; a removal stays removed; a merge on one Mac points
// the merged loop at the other there too; goals and the Steward's
// configuration travel the same way; the relay sees no title, text, id
// or loops method.
func TestLoopsSharedBetweenMacs(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	M.waitLinked(t, "L")

	var one wire.Loop
	title := "SECRETLOOP-7070"
	if err := L.call(t, wire.MethodLoopsSave, wire.LoopSaveParams{Title: &title}, &one); err != nil {
		t.Fatal(err)
	}
	eventually(t, "M has L's loop", func() bool { l, ok := M.loop(t, one.ID); return ok && l.Title == title })

	// Concurrent item adds on both Macs.
	var onL, onM wire.Loop
	if err := L.call(t, wire.MethodLoopsItem, wire.LoopItemParams{ID: one.ID, Item: wire.LoopItem{Text: "SECRETITEM-L"}}, &onL); err != nil {
		t.Fatal(err)
	}
	if err := M.call(t, wire.MethodLoopsItem, wire.LoopItemParams{ID: one.ID, Item: wire.LoopItem{Text: "SECRETITEM-M"}}, &onM); err != nil {
		t.Fatal(err)
	}
	for _, n := range []*node{L, M} {
		eventually(t, n.short+" has both items", func() bool {
			l, _ := n.loop(t, one.ID)
			s := itemTexts(l)
			return len(l.Items) == 2 && strings.Contains(s, "SECRETITEM-L") && strings.Contains(s, "SECRETITEM-M")
		})
	}
	// A tombstone: removed on M, gone on L, and it stays gone.
	var mItem string
	for _, it := range onM.Items {
		if it.Text == "SECRETITEM-M" {
			mItem = it.ID
		}
	}
	if err := M.call(t, wire.MethodLoopsItem, wire.LoopItemParams{ID: one.ID, Item: wire.LoopItem{ID: mItem}, Remove: true}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "L lost M's item", func() bool { l, _ := L.loop(t, one.ID); return itemTexts(l) == "SECRETITEM-L" })

	// A loop made on M, merged into the first on L.
	var two wire.Loop
	title2 := "SECRETLOOP-8080"
	if err := M.call(t, wire.MethodLoopsSave, wire.LoopSaveParams{Title: &title2}, &two); err != nil {
		t.Fatal(err)
	}
	if err := M.call(t, wire.MethodLoopsLink, wire.LoopLinkParams{ID: two.ID, Link: wire.LoopLink{Kind: wire.LinkURL, Ref: "https://example.com/SECRETPR-9090"}}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "L has M's loop and link", func() bool { l, ok := L.loop(t, two.ID); return ok && len(l.Links) == 1 })
	if err := L.call(t, wire.MethodLoopsMerge, wire.LoopMergeParams{Into: one.ID, From: two.ID}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "M sees the merge", func() bool {
		merged, _ := M.loop(t, two.ID)
		into, _ := M.loop(t, one.ID)
		return merged.MergedInto == one.ID && len(merged.Links) == 0 && len(into.Links) == 1
	})
	var listed []wire.Loop
	if err := M.call(t, wire.MethodLoopsList, wire.LoopsListParams{}, &listed); err != nil || len(listed) != 1 || listed[0].ID != one.ID {
		t.Fatalf("M lists %+v %v", listed, err)
	}

	// Goals and the Steward's configuration.
	var g wire.GoalChanged
	scope, gtitle := "g-secret", "SECRETGOAL-6060"
	if err := M.call(t, wire.MethodGoalsSave, wire.GoalSaveParams{Goal: &wire.GoalPatch{Scope: &scope, Title: &gtitle}}, &g); err != nil || g.Goal == nil {
		t.Fatalf("goal %+v %v", g, err)
	}
	eventually(t, "L has the goal", func() bool {
		var list wire.GoalsList
		L.call(t, wire.MethodGoalsList, wire.GoalsListParams{Scope: scope}, &list)
		return len(list.Goals) == 1 && list.Goals[0].ID == g.Goal.ID
	})
	on := true
	if _, err := L.d.Loops.SetStewardConfig(wire.StewardSetParams{Enabled: &on, Machine: new(string)}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "M knows L runs the Steward", func() bool {
		c := M.d.Loops.StewardConfig()
		return c.Enabled && c.Node == L.d.Projects.Node() && c.Machine == "L"
	})

	// The relay saw none of it.
	if leaked := w.tap.sawAny("SECRETLOOP", "SECRETITEM", "SECRETPR", "SECRETGOAL", one.ID, two.ID, g.Goal.ID); leaked != "" {
		t.Fatalf("the relay saw %q", leaked)
	}
	if w.tap.count(`"method":"loops.`) != 0 || w.tap.count(`"method":"goals.`) != 0 {
		t.Fatal("a loops method went in plaintext")
	}
	// Persisted on both.
	for _, n := range []*node{L, M} {
		n.d.Loops.Close() // writes now
		data, err := os.ReadFile(filepath.Join(n.state, "loops.json"))
		var f struct {
			Loops []json.RawMessage `json:"loops"`
		}
		if err != nil || json.Unmarshal(data, &f) != nil || len(f.Loops) != 2 {
			t.Fatalf("%s loops.json: %v %d", n.short, err, len(f.Loops))
		}
	}
}

// A child an agent on L starts on M joins its parent's loop: L names the
// loop in the spawn (M cannot know L's parent), on both Macs.
func TestRemoteChildJoinsParentsLoop(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	M.waitLinked(t, "L")
	var parent wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Project: L.project, Task: "small job"}, &parent); err != nil {
		t.Fatal(err)
	}
	parent = L.waitAgent(t, parent.ID, func(a wire.Agent) bool { return a.Loop != "" })
	eventually(t, "M has the parent's loop", func() bool { _, ok := M.loop(t, parent.Loop); return ok })
	c := L.client(t)
	c.Caller = parent.ID
	var child wire.Agent
	if err := c.Call(w.ctx, "agents.spawn", wire.SpawnParams{Machine: "M", Project: M.project, Task: "small job"}, &child); err != nil {
		t.Fatal(err)
	}
	ref := M.d.Loops.Node() + "/" + strings.TrimPrefix(child.ID, "M/")
	for _, n := range []*node{L, M} {
		eventually(t, n.short+" puts the child in its parent's loop", func() bool { return n.d.Loops.LoopOf(ref) == parent.Loop })
	}
	L.waitAgent(t, child.ID, func(a wire.Agent) bool { return a.Loop == parent.Loop })
}
