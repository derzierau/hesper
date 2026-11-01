package loops

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// --- fixtures ---

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func openTest(t *testing.T, dir, node string, clock *fakeClock) *Store {
	t.Helper()
	opt := Options{StateDir: dir, Node: node, Machine: "mac-" + node, Logf: t.Logf}
	if clock != nil {
		opt.Now = clock.now
	}
	s := Open(opt)
	t.Cleanup(s.Close)
	return s
}

// all are every record of s (what a full pull would carry).
func all(s *Store) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, k := range s.keysLocked() {
		if r := s.recordLocked(k); r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// roundTrip passes records through JSON, as the wire does.
func roundTrip(t *testing.T, recs []Record) []Record {
	t.Helper()
	data, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	var out []Record
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func exchange(t *testing.T, a, b *Store) {
	t.Helper()
	b.Merge(roundTrip(t, all(a)))
	a.Merge(roundTrip(t, all(b)))
}

func newLoop(s *Store, title string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.newLoopLocked(title, "")
	s.commitLocked("l:" + l.ID)
	return l.ID
}

func addItem(t *testing.T, s *Store, loop, text string) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	id, _, err := s.itemLocked(s.loops[loop].Items, wire.LoopItem{Text: text}, wire.ItemStep)
	if err != nil {
		t.Fatal(err)
	}
	s.commitLocked("l:" + loop)
	return id
}

func addLink(s *Store, loop string, v wire.LoopLink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addLinkLocked(s.loops[loop], v)
	s.commitLocked("l:" + loop)
}

func view(t *testing.T, s *Store, id string) wire.Loop {
	t.Helper()
	l, ok := s.snapshot().build().loops[id]
	if !ok {
		t.Fatalf("no loop %s", id)
	}
	return l
}

// --- tests ---

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	s := Open(Options{StateDir: dir, Node: "n-a", Logf: t.Logf})
	id := newLoop(s, "Ship the parser")
	item := addItem(t, s, id, "Review the PR")
	addLink(s, id, wire.LoopLink{Kind: wire.LinkAgent, Ref: "n-a/x1", Rel: wire.RelMember, Label: "parser"})
	s.Close()
	fi, err := os.Stat(filepath.Join(dir, "loops.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("loops.json: %v %v", fi, err)
	}
	s2 := openTest(t, dir, "n-a", nil)
	l := view(t, s2, id)
	if l.Title != "Ship the parser" || len(l.Items) != 1 || l.Items[0].ID != item || len(l.Links) != 1 || l.Links[0].Label != "parser" {
		t.Fatalf("reloaded %+v", l)
	}
	if s2.LoopOf("n-a/x1") != id {
		t.Fatal("membership lost")
	}
	if s2.epoch != s.epoch || s2.seq != s.seq {
		t.Fatalf("epoch/seq %s/%d, want %s/%d", s2.epoch, s2.seq, s.epoch, s.seq)
	}
}

func TestBrokenFileIsMovedAside(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "loops.json"), []byte("{not json"), 0o600)
	s := openTest(t, dir, "n-a", nil)
	if len(s.loops) != 0 {
		t.Fatal("loaded a broken file")
	}
	if _, err := os.Stat(filepath.Join(dir, "loops.json.broken")); err != nil {
		t.Fatal(err)
	}
}

func TestMergeConverges(t *testing.T) {
	clock := &fakeClock{time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	a, b := openTest(t, "", "n-a", clock), openTest(t, "", "n-b", clock)
	id := newLoop(a, "One loop")
	exchange(t, a, b)
	// Concurrent item adds on both Macs survive.
	ia, ib := addItem(t, a, id, "from a"), addItem(t, b, id, "from b")
	// A tombstone wins over the older copy.
	gone := addItem(t, a, id, "to remove")
	exchange(t, a, b)
	a.mu.Lock()
	a.removeItemLocked(a.loops[id].Items, gone)
	a.commitLocked("l:" + id)
	a.mu.Unlock()
	b.Merge(roundTrip(t, all(a)))
	stale := roundTrip(t, all(b))
	a.Merge(stale)
	for _, s := range []*Store{a, b} {
		l := view(t, s, id)
		got := map[string]bool{}
		for _, it := range l.Items {
			got[it.ID] = true
		}
		if !got[ia] || !got[ib] || got[gone] || len(l.Items) != 2 {
			t.Fatalf("%s: items %+v", s.node, l.Items)
		}
	}
	// Last writer wins per field, the same on both.
	clock.t = clock.t.Add(time.Second)
	a.mu.Lock()
	a.loops[id].Title.Set("older", a.clock.Tick())
	a.mu.Unlock()
	clock.t = clock.t.Add(time.Second)
	b.mu.Lock()
	b.loops[id].Title.Set("newer", b.clock.Tick())
	b.mu.Unlock()
	exchange(t, a, b)
	if view(t, a, id).Title != "newer" || view(t, b, id).Title != "newer" {
		t.Fatal("titles diverged")
	}
	// Merging what is there changes nothing.
	if a.Merge(roundTrip(t, all(b))) {
		t.Fatal("a merge of the same state changed something")
	}
}

// TestMergeCycleBreaks: A merged into B on one Mac and B into A on the
// other at once: on both, the lower id stays (unmerged, listed), the
// other resolves to it, and every link and item ends in it.
func TestMergeCycleBreaks(t *testing.T) {
	clock := &fakeClock{time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	a, b := openTest(t, "", "n-a", clock), openTest(t, "", "n-b", clock)
	x, y := newLoop(a, "x"), newLoop(a, "y")
	low, high := min(x, y), max(x, y)
	addLink(a, x, wire.LoopLink{Kind: wire.LinkAgent, Ref: "n-a/ax", Rel: wire.RelMember})
	addLink(a, y, wire.LoopLink{Kind: wire.LinkAgent, Ref: "n-a/ay", Rel: wire.RelMember})
	addItem(t, a, x, "item x")
	addItem(t, a, y, "item y")
	exchange(t, a, b)
	if _, err := a.MergeLoops(wire.LoopMergeParams{Into: y, From: x}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.MergeLoops(wire.LoopMergeParams{Into: x, From: y}); err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	exchange(t, a, b)
	for _, s := range []*Store{a, b} {
		for _, ref := range []string{"n-a/ax", "n-a/ay"} {
			if got := s.LoopOf(ref); got != low {
				t.Fatalf("%s: LoopOf(%s) = %s, want %s", s.node, ref, got, low)
			}
		}
		listed := s.snapshot().build().listed()
		if len(listed) != 1 || listed[0].ID != low || listed[0].MergedInto != "" {
			t.Fatalf("%s: listed %+v", s.node, listed)
		}
		if l := listed[0]; len(l.Links) != 2 || len(l.Items) != 2 {
			t.Fatalf("%s: %s holds %d links, %d items", s.node, low, len(l.Links), len(l.Items))
		}
		if v := view(t, s, high); v.MergedInto != low {
			t.Fatalf("%s: %s merged into %q", s.node, high, v.MergedInto)
		}
	}
	if a.Merge(roundTrip(t, all(b))) || b.Merge(roundTrip(t, all(a))) {
		t.Fatal("the stores did not converge")
	}
}

func TestMergeRejectsInsaneRecords(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	bad := []Record{
		{Loop: &LoopRecord{ID: "lp-nothex!!!!!"}},
		{Loop: &LoopRecord{ID: "lp-0123456789ab", Status: field[string]{V: "weird"}}},
		{Goal: &GoalRecord{ID: "gl-0123456789ab"}, Milestone: &MilestoneRecord{ID: "ms-0123456789ab"}},
		{Loop: &LoopRecord{ID: "lp-0123456789ab", Links: map[string]*LinkRecord{"k": {Kind: field[string]{V: "pipe"}}}}},
	}
	if s.Merge(bad) || len(s.loops) != 0 || len(s.goals) != 0 {
		t.Fatal("merged an insane record")
	}
}

func TestLoopOfFollowsMerges(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	a, b, c := newLoop(s, "a"), newLoop(s, "b"), newLoop(s, "c")
	addLink(s, a, wire.LoopLink{Kind: wire.LinkAgent, Ref: "n-a/x1", Rel: wire.RelMember})
	s.mu.Lock()
	s.loops[a].MergedInto.Set(b, s.clock.Tick())
	s.loops[b].MergedInto.Set(c, s.clock.Tick())
	s.commitLocked("l:"+a, "l:"+b)
	s.mu.Unlock()
	if got := s.LoopOf("n-a/x1"); got != c {
		t.Fatalf("LoopOf = %s, want %s", got, c)
	}
	// A cycle (two Macs merging both ways at once) ends.
	s.mu.Lock()
	s.loops[c].MergedInto.Set(a, s.clock.Tick())
	s.commitLocked("l:" + c)
	s.mu.Unlock()
	s.LoopOf("n-a/x1")
}

func TestOldResultsArePruned(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s := Open(Options{StateDir: dir, Node: "n-a", Now: clock.now, Logf: t.Logf})
	id := newLoop(s, "old")
	addLink(s, id, wire.LoopLink{Kind: wire.LinkAgent, Ref: "n-a/x1", Rel: wire.RelMember})
	s.mu.Lock()
	for _, k := range s.loops[id].Links {
		k.Closed.Set(clock.t, s.clock.Tick())
		k.Result.Set("the final words", s.clock.Tick())
	}
	s.commitLocked("l:" + id)
	s.mu.Unlock()
	s.Close()
	clock.t = clock.t.Add(91 * 24 * time.Hour)
	s2 := openTest(t, dir, "n-a", clock)
	l := view(t, s2, id)
	if l.Links[0].Result != "" || l.Links[0].Closed.IsZero() {
		t.Fatalf("link %+v", l.Links[0])
	}
	// The prune is a write: it reaches a Mac with the old copy.
	other := openTest(t, "", "n-b", clock)
	other.Merge(roundTrip(t, all(s2)))
	if view(t, other, id).Links[0].Result != "" {
		t.Fatal("prune did not replicate")
	}
}

func TestViewsCapAndJoinLive(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	id := newLoop(s, "big")
	for i := range 70 {
		addLink(s, id, wire.LoopLink{Kind: wire.LinkAgent, Ref: fmt.Sprintf("n-a/x%02d", i), Rel: wire.RelMember})
	}
	s.SetNames(func(m string) string {
		if m == "studio" {
			return "n-b"
		}
		return ""
	}, func(n string) string { return map[string]string{"n-b": "studio"}[n] })
	addLink(s, id, wire.LoopLink{Kind: wire.LinkAgent, Ref: "n-b/y1", Rel: wire.RelMember})
	// The lister calls back into the store: views never hold its lock.
	s.SetAgents(func() []wire.Agent {
		s.LoopOf("n-a/x00")
		return []wire.Agent{{ID: "x00", Name: "first", State: wire.StateWorking}, {ID: "studio/y1", State: wire.StateIdle}}
	})
	l := view(t, s, id)
	if len(l.Links) != maxViewLinks {
		t.Fatalf("%d links", len(l.Links))
	}
	var first, remote bool
	for _, k := range l.Links {
		if k.Ref == "n-a/x00" {
			first = k.State == wire.StateWorking && k.Agent == "x00" && k.Label == "first" && k.Machine == "mac-n-a"
		}
		if k.Ref == "n-b/y1" {
			remote = k.Agent == "studio/y1" && k.Machine == "studio" && k.State == wire.StateIdle
		}
	}
	if !first || !remote {
		t.Fatalf("live join first=%v remote=%v", first, remote)
	}
	s.mu.Lock()
	for range 120 {
		s.itemLocked(s.loops[id].Items, wire.LoopItem{Text: "step", State: wire.ItemDone}, wire.ItemStep)
	}
	s.itemLocked(s.loops[id].Items, wire.LoopItem{Text: "still open"}, wire.ItemStep)
	s.commitLocked("l:" + id)
	s.mu.Unlock()
	l = view(t, s, id)
	open := false
	for _, it := range l.Items {
		open = open || it.Text == "still open"
	}
	if len(l.Items) != maxViewItems || !open {
		t.Fatalf("%d items, open kept %v", len(l.Items), open)
	}
}

func TestWatchFirstOpenLoopsThenChanges(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	open := newLoop(s, "open")
	done := newLoop(s, "done")
	s.mu.Lock()
	s.loops[done].Status.Set(wire.LoopDone, s.clock.Tick())
	s.commitLocked("l:" + done)
	s.mu.Unlock()
	s.emit()
	stop := make(chan struct{})
	got := make(chan string, 16)
	go s.Watch(stop, func(method string, params any) error {
		data, _ := json.Marshal(params)
		var n wire.LoopChanged
		json.Unmarshal(data, &n)
		got <- method + " " + n.Loop.ID
		return nil
	})
	defer close(stop)
	if g := <-got; g != wire.NoteLoopChanged+" "+open {
		t.Fatalf("first note %q", g)
	}
	later := newLoop(s, "later")
	select {
	case g := <-got:
		if g != wire.NoteLoopChanged+" "+later {
			t.Fatalf("change note %q", g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change note")
	}
}

// TestViewsRebuildWhatChanged: a view build rebuilds the loops that
// changed, those linking an agent whose live state changed and those
// waiting for a changed loop; the others keep their views; milestones
// follow their loops.
func TestViewsRebuildWhatChanged(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	a, b, c := newLoop(s, "a"), newLoop(s, "b"), newLoop(s, "c")
	addLink(s, a, wire.LoopLink{Kind: wire.LinkAgent, Ref: "n-a/x1", Rel: wire.RelMember})
	addLink(s, a, wire.LoopLink{Kind: wire.LinkLoop, Ref: b, Rel: wire.RelBlockedBy})
	state := wire.StateIdle
	var mu sync.Mutex
	s.SetAgents(func() []wire.Agent {
		mu.Lock()
		defer mu.Unlock()
		return []wire.Agent{{ID: "x1", State: state}}
	})
	ms := must[wire.GoalChanged](t, s, wire.MethodGoalsSave, wire.GoalSaveParams{Milestone: &wire.MilestonePatch{Scope: ptr("p-app"), Title: ptr("Beta")}}).Milestone
	s.emit()
	note := func(id string) wire.Loop {
		t.Helper()
		s.mu.Lock()
		defer s.mu.Unlock()
		var n wire.LoopChanged
		if err := json.Unmarshal(s.views["l:"+id], &n); err != nil {
			t.Fatalf("no view of %s", id)
		}
		return n.Loop
	}
	if l := note(a); l.NextText != "Waits for b" {
		t.Fatalf("a: %q", l.NextText)
	}
	s.mu.Lock()
	cView := string(s.views["l:"+c])
	s.mu.Unlock()
	// A live change rebuilds the loop linking the agent.
	mu.Lock()
	state = wire.StateWorking
	mu.Unlock()
	s.emit()
	if l := note(a); l.Next != wire.NextAgents {
		t.Fatalf("a after its agent works: %+v", l.Next)
	}
	// Its blocker closing rebuilds it.
	mu.Lock()
	state = wire.StateIdle
	mu.Unlock()
	must[wire.Loop](t, s, wire.MethodLoopsSave, wire.LoopSaveParams{ID: b, Status: ptr(wire.LoopDone)})
	s.emit()
	if l := note(a); strings.Contains(l.NextText, "Waits for") {
		t.Fatalf("a still waits: %q", l.NextText)
	}
	// Filing c rebuilds it and its milestone.
	must[wire.Milestone](t, s, wire.MethodGoalsFile, wire.GoalFileParams{Milestone: ms.ID, Loop: c})
	s.emit()
	s.mu.Lock()
	same := string(s.views["l:"+c]) == cView
	s.mu.Unlock()
	if l := note(c); l.Milestone != ms.ID || same {
		t.Fatalf("c: %+v", l)
	}
	s.mu.Lock()
	var g wire.GoalChanged
	json.Unmarshal(s.views["m:"+ms.ID], &g)
	s.mu.Unlock()
	if g.Milestone == nil || len(g.Milestone.Loops) != 1 || g.Milestone.Loops[0] != c {
		t.Fatalf("milestone %+v", g.Milestone)
	}
}
