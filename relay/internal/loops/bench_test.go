package loops

import (
	"fmt"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// benchStore is a store of n loops, each with five member agents (one
// live), five items and a project, like a busy owner's after months.
func benchStore(b *testing.B, n int) (*Store, []string) {
	b.Helper()
	s := Open(Options{Node: "n-a", Machine: "L", Logf: func(string, ...any) {}})
	b.Cleanup(s.Close)
	var refs []string
	var live []wire.Agent
	s.mu.Lock()
	for i := range n {
		l := s.newLoopLocked(fmt.Sprintf("loop %d", i), "")
		for j := range 5 {
			ref := fmt.Sprintf("n-a/a%05d%d", i, j)
			s.addLinkLocked(l, wire.LoopLink{Kind: wire.LinkAgent, Ref: ref, Rel: wire.RelMember, Label: "agent"})
			refs = append(refs, ref)
			if _, _, err := s.itemLocked(l.Items, wire.LoopItem{Text: fmt.Sprintf("step %d", j)}, wire.ItemStep); err != nil {
				b.Fatal(err)
			}
		}
		s.addLinkLocked(l, wire.LoopLink{Kind: wire.LinkProject, Ref: "p-app", Rel: wire.RelTouches})
		live = append(live, wire.Agent{ID: fmt.Sprintf("a%05d0", i), State: wire.StateIdle})
		s.commitLocked("l:" + l.ID)
	}
	s.mu.Unlock()
	s.SetAgents(func() []wire.Agent { return live })
	s.emit()
	return s, refs
}

// BenchmarkLoopOf: the hook path's lookup right after a change (each
// iteration changes one loop first, as hooks do between lookups).
func BenchmarkLoopOf(b *testing.B) {
	s, refs := benchStore(b, 2000)
	loop := s.LoopOf(refs[0])
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		s.mu.Lock()
		s.loops[loop].Title.Set(fmt.Sprint(i), s.clock.Tick())
		s.commitLocked("l:" + loop)
		s.mu.Unlock()
		if s.LoopOf(refs[i%len(refs)]) == "" {
			b.Fatal("no loop")
		}
	}
}

// BenchmarkViewRebuild: one loop changed, the watchers' views rebuilt.
func BenchmarkViewRebuild(b *testing.B) {
	s, refs := benchStore(b, 2000)
	loop := s.LoopOf(refs[0])
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		s.mu.Lock()
		s.loops[loop].Title.Set(fmt.Sprint(i), s.clock.Tick())
		s.commitLocked("l:" + loop)
		s.mu.Unlock()
		s.emit()
	}
}

// BenchmarkGet: loops.get of one loop.
func BenchmarkGet(b *testing.B) {
	s, refs := benchStore(b, 2000)
	loop := s.LoopOf(refs[0])
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Get(loop); err != nil {
			b.Fatal(err)
		}
	}
}
