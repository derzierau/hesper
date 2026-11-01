package loops

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func call[T any](t *testing.T, s *Store, method string, params any, who agents.Caller) (T, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err, handled := s.Call(method, raw, who)
	if !handled {
		t.Fatalf("%s not handled", method)
	}
	var out T
	if err == nil {
		data, _ := json.Marshal(res)
		if e := json.Unmarshal(data, &out); e != nil {
			t.Fatalf("%s result: %v", method, e)
		}
	}
	return out, err
}

func must[T any](t *testing.T, s *Store, method string, params any) T {
	t.Helper()
	out, err := call[T](t, s, method, params, agents.Caller{})
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

func code(err error) string {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Code
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

func linkOf(l wire.Loop, ref string) (wire.LoopLink, bool) {
	for _, k := range l.Links {
		if k.Ref == ref {
			return k, true
		}
	}
	return wire.LoopLink{}, false
}

func TestAssign(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	s.SetSessionsNode(func() string { return "h-a" })
	ms := must[wire.GoalChanged](t, s, wire.MethodGoalsSave, wire.GoalSaveParams{Milestone: &wire.MilestonePatch{Scope: ptr("p-1"), Title: ptr("v1")}}).Milestone.ID

	root := s.Assign(agents.AssignInfo{Ref: "n-a/r1", Agent: wire.Agent{Name: "parser", ProjectID: "p-1", Kind: wire.KindClaude, SessionID: "s1"}})
	l := view(t, s, root)
	if l.Title != "parser" {
		t.Fatalf("title %q", l.Title)
	}
	if k, ok := linkOf(l, "n-a/r1"); !ok || k.Rel != wire.RelMember {
		t.Fatalf("member link %+v", l.Links)
	}
	if _, ok := linkOf(l, "p-1"); !ok {
		t.Fatal("no touches link")
	}
	if _, ok := linkOf(l, "h-a:claude:s1"); !ok || s.LoopOf("h-a:claude:s1") != root {
		t.Fatal("no session link")
	}

	child := s.Assign(agents.AssignInfo{Ref: "n-a/c1", ParentRef: "n-a/r1", Agent: wire.Agent{Name: "child"}})
	if k, _ := linkOf(view(t, s, root), "n-a/c1"); child != root || k.Rel != wire.RelStarted || k.From != "n-a/r1" {
		t.Fatalf("child in %s, link %+v", child, k)
	}
	resumed := s.Assign(agents.AssignInfo{Ref: "n-a/x2", SourceSession: "h-a:claude:s1", Agent: wire.Agent{Name: "again"}})
	if k, _ := linkOf(view(t, s, root), "n-a/x2"); resumed != root || k.Rel != wire.RelContinued || k.From != "h-a:claude:s1" {
		t.Fatalf("resume in %s, link %+v", resumed, k)
	}
	other := newLoop(s, "other")
	explicit := s.Assign(agents.AssignInfo{Ref: "n-a/e1", Loop: other, ParentRef: "n-a/r1", Agent: wire.Agent{Name: "e"}})
	filed := s.Assign(agents.AssignInfo{Ref: "n-a/m1", Milestone: ms, Agent: wire.Agent{Task: "Write the docs\nand more"}})
	if explicit != other || filed == root || filed == other || view(t, s, filed).Milestone != ms || view(t, s, filed).Title != "Write the docs" {
		t.Fatalf("explicit %s filed %s", explicit, filed)
	}
	if again := s.Assign(agents.AssignInfo{Ref: "n-a/r1", Agent: wire.Agent{Name: "parser"}}); again != root {
		t.Fatal("a re-inserted agent changed loops")
	}
	if s.Assign(agents.AssignInfo{Ref: "n-a/st", Agent: wire.Agent{Service: wire.ServiceSteward}}) != "" {
		t.Fatal("a service agent got a loop")
	}
}

// TestNewestMembershipWins: an agent joining another loop (Assign with a
// loop, loops.link) leaves the one it was in: it counts in one loop.
func TestNewestMembershipWins(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	first := s.Assign(agents.AssignInfo{Ref: "n-a/r1", Agent: wire.Agent{Name: "parser"}})
	second := newLoop(s, "second")
	if got := s.Assign(agents.AssignInfo{Ref: "n-a/r1", Loop: second, Agent: wire.Agent{Name: "parser"}}); got != second {
		t.Fatalf("assigned to %s", got)
	}
	if _, ok := linkOf(view(t, s, first), "n-a/r1"); ok {
		t.Fatal("still a member of its first loop")
	}
	third := newLoop(s, "third")
	must[wire.Loop](t, s, wire.MethodLoopsLink, wire.LoopLinkParams{ID: third, Link: wire.LoopLink{Kind: wire.LinkAgent, Ref: "n-a/r1"}})
	if _, ok := linkOf(view(t, s, second), "n-a/r1"); ok || s.LoopOf("n-a/r1") != third {
		t.Fatalf("in %s; the second loop kept its link", s.LoopOf("n-a/r1"))
	}
	// A touches link is not membership: it stays.
	must[wire.Loop](t, s, wire.MethodLoopsLink, wire.LoopLinkParams{ID: second, Link: wire.LoopLink{Kind: wire.LinkAgent, Ref: "n-a/r1", Rel: wire.RelTouches}})
	if s.LoopOf("n-a/r1") != third {
		t.Fatal("a touches link moved the agent")
	}
}

func TestSettledAndClosed(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	s.SetClassify(func(msg string) (string, []string) {
		if strings.Contains(msg, "?") {
			return wire.EndingAsks, []string{"Should I push the branch?", "Which port do you want?"}
		}
		return wire.EndingOffer, []string{"I can also add tests."}
	})
	id := s.Assign(agents.AssignInfo{Ref: "n-a/a1", Agent: wire.Agent{Name: "worker"}})
	s.AgentSettled(agents.AgentEvent{Ref: "n-a/a1", Agent: wire.Agent{Name: "worker"}, Message: "Done. Should I push the branch?"})
	s.AgentSettled(agents.AgentEvent{Ref: "n-a/a1", Agent: wire.Agent{Name: "worker"}, Message: "Again? should i push the  branch?"})
	l := view(t, s, id)
	k, _ := linkOf(l, "n-a/a1")
	if k.Ending != wire.EndingAsks || k.Summary != "Should I push the branch?" {
		t.Fatalf("link %+v", k)
	}
	if len(l.Items) != 2 || l.Items[0].Owner != wire.OwnerYou || l.Items[0].Kind != wire.ItemQuestion || l.Items[0].Source != "n-a/a1" {
		t.Fatalf("items %+v", l.Items)
	}
	s.AgentSettled(agents.AgentEvent{Ref: "n-a/a1", Message: "All set."})
	l = view(t, s, id)
	if last := l.Items[len(l.Items)-1]; len(l.Items) != 3 || last.Kind != wire.ItemOffer || last.Owner != wire.OwnerAgent {
		t.Fatalf("offer %+v", l.Items)
	}
	if l.Next != wire.NextYou || l.Counts.OpenYou != 2 {
		t.Fatalf("next %s counts %+v", l.Next, l.Counts)
	}

	long := strings.Repeat("é", 1000)
	s.AgentClosed(agents.AgentEvent{Ref: "n-a/a1", Reason: wire.ReasonClosed, Message: long})
	k, _ = linkOf(view(t, s, id), "n-a/a1")
	if k.ClosedReason != wire.ReasonClosed || k.Closed.IsZero() || len(k.Result) > resultMax || !strings.HasPrefix(long, k.Result) {
		t.Fatalf("closed link %+v", k)
	}

	// A move rewrites the ref; the imported agent's own link goes.
	s.SetNames(func(m string) string { return map[string]string{"studio": "n-b"}[m] }, nil)
	mv := s.Assign(agents.AssignInfo{Ref: "n-a/m1", Agent: wire.Agent{Name: "mover"}})
	s.Assign(agents.AssignInfo{Ref: "n-b/m2", Loop: mv, Agent: wire.Agent{Name: "mover"}})
	s.AgentClosed(agents.AgentEvent{Ref: "n-a/m1", Reason: wire.ReasonMoved, To: "studio/m2"})
	l = view(t, s, mv)
	n := 0
	for _, k := range l.Links {
		if k.Kind == wire.LinkAgent {
			n++
			if k.Ref != "n-b/m2" || k.ClosedReason != "" {
				t.Fatalf("moved link %+v", k)
			}
		}
	}
	if n != 1 || s.LoopOf("n-b/m2") != mv || s.LoopOf("n-a/m1") != "" {
		t.Fatalf("%d agent links after the move", n)
	}
}

func TestAgentCallerRule(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	own := s.Assign(agents.AssignInfo{Ref: "n-a/a1", Agent: wire.Agent{Name: "a"}})
	other := newLoop(s, "someone else's")
	agent := agents.Caller{Agent: "a1"}
	if _, err := call[wire.Loop](t, s, wire.MethodLoopsItem, wire.LoopItemParams{ID: own, Item: wire.LoopItem{Text: "mine"}}, agent); err != nil {
		t.Fatalf("own item: %v", err)
	}
	if _, err := call[wire.Loop](t, s, wire.MethodLoopsLink, wire.LoopLinkParams{ID: own, Link: wire.LoopLink{Kind: wire.LinkURL, Ref: "https://example.com/pr/1"}}, agent); err != nil {
		t.Fatalf("own link: %v", err)
	}
	for _, c := range []struct {
		method string
		params any
	}{
		{wire.MethodLoopsItem, wire.LoopItemParams{ID: other, Item: wire.LoopItem{Text: "theirs"}}},
		{wire.MethodLoopsSave, wire.LoopSaveParams{ID: own, Title: ptr("renamed")}},
		{wire.MethodLoopsMerge, wire.LoopMergeParams{Into: own, From: other}},
		{wire.MethodGoalsSave, wire.GoalSaveParams{Goal: &wire.GoalPatch{Scope: ptr("p"), Title: ptr("g")}}},
		{wire.MethodLoopsSettingsSet, wire.LoopSettings{LoopLimit: 9}},
	} {
		if _, err := call[json.RawMessage](t, s, c.method, c.params, agent); code(err) != wire.CodeForbidden {
			t.Errorf("%s by an agent: %v", c.method, err)
		}
	}
	if got, err := call[[]wire.Loop](t, s, wire.MethodLoopsList, wire.LoopsListParams{}, agent); err != nil || len(got) != 2 {
		t.Fatalf("read: %v %d", err, len(got))
	}
	// A service agent (the Steward) is the dispatcher's to judge.
	if _, err := call[wire.Loop](t, s, wire.MethodLoopsSave, wire.LoopSaveParams{ID: other, Title: ptr("tidied")}, agents.Caller{Agent: "st", Service: wire.ServiceSteward}); err != nil {
		t.Fatal(err)
	}
}

func TestMergeSplitAndList(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	a := s.Assign(agents.AssignInfo{Ref: "n-a/a1", Agent: wire.Agent{Name: "a", ProjectID: "p-1"}})
	b := s.Assign(agents.AssignInfo{Ref: "n-a/b1", Agent: wire.Agent{Name: "b"}})
	s.Assign(agents.AssignInfo{Ref: "n-a/b2", ParentRef: "n-a/b1", Agent: wire.Agent{Name: "b child"}})
	must[wire.Loop](t, s, wire.MethodLoopsItem, wire.LoopItemParams{ID: b, Item: wire.LoopItem{Text: "from b2", Source: "n-a/b2"}})
	into := must[wire.Loop](t, s, wire.MethodLoopsMerge, wire.LoopMergeParams{Into: a, From: b})
	if into.ID != a || len(into.Items) != 1 || s.LoopOf("n-a/b2") != a {
		t.Fatalf("merged %+v", into)
	}
	if got := must[wire.Loop](t, s, wire.MethodLoopsGet, wire.IDParams{ID: b}); got.MergedInto != a || len(got.Links) != 0 {
		t.Fatalf("from %+v", got)
	}
	if list := must[[]wire.Loop](t, s, wire.MethodLoopsList, wire.LoopsListParams{}); len(list) != 1 {
		t.Fatalf("list shows merged loops: %d", len(list))
	}
	// Items added to the merged loop land in the loop it went into.
	if l := must[wire.Loop](t, s, wire.MethodLoopsItem, wire.LoopItemParams{ID: b, Item: wire.LoopItem{Text: "late"}}); l.ID != a {
		t.Fatalf("late item went to %s", l.ID)
	}
	split := must[wire.Loop](t, s, wire.MethodLoopsSplit, wire.LoopSplitParams{ID: a, Ref: "n-a/b1"})
	if split.Title != "b" || s.LoopOf("n-a/b1") != split.ID || s.LoopOf("n-a/b2") != split.ID || s.LoopOf("n-a/a1") != a {
		t.Fatalf("split %+v", split)
	}
	if len(split.Items) != 1 || split.Items[0].Text != "from b2" || len(split.Projects) != 1 {
		t.Fatalf("split items %+v projects %v", split.Items, split.Projects)
	}
	byAgent := must[[]wire.Loop](t, s, wire.MethodLoopsList, wire.LoopsListParams{Agent: "b2"})
	byProject := must[[]wire.Loop](t, s, wire.MethodLoopsList, wire.LoopsListParams{Project: "p-1"})
	if len(byAgent) != 1 || byAgent[0].ID != split.ID || len(byProject) != 2 {
		t.Fatalf("filters %d %d", len(byAgent), len(byProject))
	}
	done := must[wire.Loop](t, s, wire.MethodLoopsSave, wire.LoopSaveParams{ID: split.ID, Status: ptr(wire.LoopDone)})
	if done.Closed.IsZero() || done.Next != wire.NextClosed {
		t.Fatalf("done %+v", done)
	}
	if n := len(must[[]wire.Loop](t, s, wire.MethodLoopsList, wire.LoopsListParams{})); n != 1 {
		t.Fatalf("open loops %d", n)
	}
	if n := len(must[[]wire.Loop](t, s, wire.MethodLoopsList, wire.LoopsListParams{Status: "all"})); n != 2 {
		t.Fatalf("all loops %d", n)
	}
	if _, err := call[wire.Loop](t, s, wire.MethodLoopsSave, wire.LoopSaveParams{ID: a, Status: ptr("finished")}, agents.Caller{}); code(err) != wire.CodeInvalid {
		t.Fatalf("bad status: %v", err)
	}
}

func TestGoalsAndMilestones(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	g := must[wire.GoalChanged](t, s, wire.MethodGoalsSave, wire.GoalSaveParams{Goal: &wire.GoalPatch{Scope: ptr("g-tools"), Title: ptr("Fast start")}}).Goal
	m := must[wire.GoalChanged](t, s, wire.MethodGoalsSave, wire.GoalSaveParams{Milestone: &wire.MilestonePatch{
		Scope: ptr("g-tools"), Goal: &g.ID, Title: ptr("v2"), Status: ptr(wire.MilestoneProposed)}}).Milestone
	ms := must[wire.Milestone](t, s, wire.MethodGoalsItem, wire.LoopItemParams{ID: m.ID, Item: wire.LoopItem{Text: "Release tagged"}})
	if len(ms.Items) != 1 || ms.Items[0].Kind != wire.ItemCondition || ms.Total != 1 {
		t.Fatalf("milestone %+v", ms)
	}
	loop := newLoop(s, "work")
	if got := must[wire.Milestone](t, s, wire.MethodGoalsFile, wire.GoalFileParams{Milestone: m.ID, Loop: loop}); len(got.Loops) != 1 || got.Loops[0] != loop {
		t.Fatalf("not filed: %+v", got)
	}
	if got := must[Accepted](t, s, wire.MethodGoalsAccept, wire.GoalAcceptParams{Scope: "g-tools"}); got.Accepted != 1 {
		t.Fatalf("accepted %+v", got)
	}
	list := must[wire.GoalsList](t, s, wire.MethodGoalsList, wire.GoalsListParams{Scope: "g-tools"})
	if len(list.Goals) != 1 || len(list.Milestones) != 1 || list.Milestones[0].Status != wire.LoopOpen || len(list.Milestones[0].Loops) != 1 {
		t.Fatalf("after accept %+v", list)
	}
	if got := must[Accepted](t, s, wire.MethodGoalsAccept, wire.GoalAcceptParams{Scope: "g-tools"}); got.Accepted != 0 {
		t.Fatalf("accepted twice %+v", got)
	}
	unfiled := must[wire.Milestone](t, s, wire.MethodGoalsFile, wire.GoalFileParams{Milestone: m.ID, Loop: loop, Remove: true})
	must[wire.Milestone](t, s, wire.MethodGoalsFile, wire.GoalFileParams{Milestone: m.ID, Loop: loop})
	if len(unfiled.Loops) != 0 {
		t.Fatalf("unfile %+v", unfiled)
	}
	must[struct{}](t, s, wire.MethodGoalsRemove, wire.IDParams{ID: m.ID})
	if l := view(t, s, loop); l.Milestone != "" {
		t.Fatal("removing a milestone left its loop filed")
	}
	if got := must[wire.GoalsList](t, s, wire.MethodGoalsList, wire.GoalsListParams{Scope: "g-tools"}); len(got.Milestones) != 0 || len(got.Goals) != 1 {
		t.Fatalf("after remove %+v", got)
	}
	set := must[wire.LoopSettings](t, s, wire.MethodLoopsSettingsSet, wire.LoopSettings{LoopLimit: 3, EveningAt: "19:30"})
	if set.LoopLimit != 3 || set.EveningAt != "19:30" || set.StallDays != 7 || set.MaxWakesPerDay != 40 {
		t.Fatalf("settings %+v", set)
	}
	if _, err := call[wire.LoopSettings](t, s, wire.MethodLoopsSettingsSet, wire.LoopSettings{EveningAt: "25:00"}, agents.Caller{}); code(err) != wire.CodeInvalid {
		t.Fatalf("bad time: %v", err)
	}
}

// TestAgentsProposeMilestones: a milestone an agent creates is proposed
// (a person accepts it); a person's is open.
func TestAgentsProposeMilestones(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	planner := agents.Caller{Agent: "L/pl", Service: wire.ServicePlanner}
	got, err := call[wire.GoalChanged](t, s, wire.MethodGoalsSave, wire.GoalSaveParams{Milestone: &wire.MilestonePatch{Scope: ptr("p-app"), Title: ptr("Beta")}}, planner)
	if err != nil || got.Milestone == nil || got.Milestone.Status != wire.MilestoneProposed {
		t.Fatalf("an agent's milestone %+v %v", got.Milestone, err)
	}
	mine := must[wire.GoalChanged](t, s, wire.MethodGoalsSave, wire.GoalSaveParams{Milestone: &wire.MilestonePatch{Scope: ptr("p-app"), Title: ptr("GA")}})
	if mine.Milestone.Status != wire.LoopOpen {
		t.Fatalf("a person's milestone %+v", mine.Milestone)
	}
}

func TestStewardConfig(t *testing.T) {
	s := openTest(t, "", "n-a", nil)
	s.SetMachine("mini")
	s.SetNames(func(m string) string { return map[string]string{"studio": "n-b"}[m] }, func(n string) string { return map[string]string{"n-b": "studio"}[n] })
	if c := s.StewardConfig(); c.Enabled || c.Level != wire.StewardSuggest || c.Node != "" {
		t.Fatalf("default %+v", c)
	}
	c, err := s.SetStewardConfig(wire.StewardSetParams{Enabled: ptr(true)})
	if err != nil || c.Node != "n-a" || c.Machine != "mini" {
		t.Fatalf("on: %+v %v", c, err)
	}
	changed := s.StewardChanges()
	c, err = s.SetStewardConfig(wire.StewardSetParams{Machine: ptr("studio"), Level: ptr(wire.StewardAct)})
	if err != nil || c.Node != "n-b" || c.Machine != "studio" || c.Level != wire.StewardAct {
		t.Fatalf("moved: %+v %v", c, err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("no Steward change signal")
	}
	if _, err := s.SetStewardConfig(wire.StewardSetParams{Machine: ptr("nowhere")}); code(err) != wire.CodeNotFound {
		t.Fatalf("unknown machine: %v", err)
	}
	if _, err := s.SetStewardConfig(wire.StewardSetParams{Level: ptr("boss")}); code(err) != wire.CodeInvalid {
		t.Fatalf("bad level: %v", err)
	}
}

// TestLoopCreatedHook: loops.save without an id and loops.split tell the
// creation hook which service (if any) made the loop, so the Steward
// never wakes on loops it or its planners created.
func TestLoopCreatedHook(t *testing.T) {
	s := openTest(t, t.TempDir(), "n-a", nil)
	var mu sync.Mutex
	var got []string
	s.SetCreated(func(id, service string) {
		mu.Lock()
		got = append(got, id+" "+service)
		mu.Unlock()
	})
	steward := agents.Caller{Agent: "L/st", Service: wire.ServiceSteward}
	mine, err := call[wire.Loop](t, s, wire.MethodLoopsSave, wire.LoopSaveParams{Title: ptr("by the Steward")}, steward)
	if err != nil {
		t.Fatal(err)
	}
	person := must[wire.Loop](t, s, wire.MethodLoopsSave, wire.LoopSaveParams{Title: ptr("by a person")})
	must[wire.Loop](t, s, wire.MethodLoopsSave, wire.LoopSaveParams{ID: person.ID, Title: ptr("renamed")}) // no new loop
	must[wire.Loop](t, s, wire.MethodLoopsLink, wire.LoopLinkParams{ID: person.ID, Link: wire.LoopLink{Kind: wire.LinkURL, Ref: "https://example.com/pr/1"}})
	split, err := call[wire.Loop](t, s, wire.MethodLoopsSplit, wire.LoopSplitParams{ID: person.ID, Ref: "https://example.com/pr/1", Title: "the PR"},
		agents.Caller{Agent: "L/pl", Service: wire.ServicePlanner})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{mine.ID + " " + wire.ServiceSteward, person.ID + " ", split.ID + " " + wire.ServicePlanner}
	if !slices.Equal(got, want) {
		t.Fatalf("created %q, want %q", got, want)
	}
}
