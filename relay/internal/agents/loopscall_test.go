package agents

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The loops policy (loopscall.go): what agents may call.

func ptr[T any](v T) *T { return &v }

// newStewardHarness is a harness with a fakeLoops and a fakeSteward.
func newStewardHarness(t *testing.T) (*harness, *fakeLoops) {
	h := newHarness(t)
	h.close()
	l := newFakeLoops()
	h.opt.Loops, h.opt.Steward = l, &fakeSteward{}
	h.open()
	return h, l
}

func TestLoopsPolicyPerson(t *testing.T) {
	h, l := newStewardHarness(t)
	for _, m := range []string{wire.MethodStewardSet, wire.MethodGoalsAccept, wire.MethodGoalsRemove, wire.MethodLoopsSettingsSet, wire.MethodLoopsMerge,
		wire.MethodStewardInbox} {
		allowed(t, m, h.call(m, map[string]any{}, nil))
	}
	allowed(t, "close a loop", h.call(wire.MethodLoopsSave, wire.LoopSaveParams{ID: "lp-1", Status: ptr(wire.LoopDone)}, nil))
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.calls {
		if c != (Caller{}) {
			t.Fatalf("a person's call as %+v", c)
		}
	}
}

func TestLoopsPolicyAgent(t *testing.T) {
	h, l := newStewardHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	loop := a.Loop
	// Reads: anything; the store learns who calls.
	allowed(t, "list", h.callAs(a.ID, wire.MethodLoopsList, wire.LoopsListParams{}, nil))
	l.mu.Lock()
	got := l.calls[len(l.calls)-1]
	l.mu.Unlock()
	if got != (Caller{Agent: a.ID, Ref: "n1/" + strings.TrimPrefix(a.ID, "L/"), Loop: loop}) {
		t.Fatalf("caller %+v", got)
	}
	allowed(t, "goals.list", h.callAs(a.ID, wire.MethodGoalsList, wire.GoalsListParams{}, nil))
	// Its own loop's items and links.
	allowed(t, "own item", h.callAs(a.ID, wire.MethodLoopsItem, wire.LoopItemParams{ID: loop, Item: wire.LoopItem{Text: "push"}}, nil))
	allowed(t, "own link", h.callAs(a.ID, wire.MethodLoopsLink, wire.LoopLinkParams{ID: loop}, nil))
	forbidden(t, "another loop's item", h.callAs(a.ID, wire.MethodLoopsItem, wire.LoopItemParams{ID: "lp-other"}, nil))
	forbidden(t, "another loop's link", h.callAs(a.ID, wire.MethodLoopsLink, wire.LoopLinkParams{ID: "lp-other"}, nil))
	// A person's only, and what only the Steward does.
	for _, m := range []string{wire.MethodStewardSet, wire.MethodGoalsAccept, wire.MethodGoalsRemove, wire.MethodLoopsSettingsSet,
		wire.MethodLoopsMerge, wire.MethodLoopsSplit, wire.MethodLoopsSave, wire.MethodGoalsSave, wire.MethodGoalsItem, wire.MethodGoalsFile,
		wire.MethodStewardPlan, wire.MethodStewardInbox} {
		forbidden(t, m, h.callAs(a.ID, m, map[string]any{"id": loop}, nil))
	}
	err := h.callAs(a.ID, wire.MethodLoopsSave, wire.LoopSaveParams{ID: loop, Status: ptr(wire.LoopDone)}, nil)
	forbidden(t, "close its loop", err)
	if !strings.Contains(err.Error(), "a person does") {
		t.Fatalf("message %q", err)
	}
	// Audited.
	var refused, ok int
	for _, line := range auditLines(t, h) {
		if !strings.HasPrefix(line["method"].(string), "loops.") && !strings.HasPrefix(line["method"].(string), "goals.") && !strings.HasPrefix(line["method"].(string), "steward.") {
			continue
		}
		switch line["event"] {
		case "agent.refused":
			refused++
		case "agent.request":
			ok++
		}
	}
	if refused != 15 || ok != 2 {
		t.Fatalf("audit: %d refused, %d requests (reads are not audited)", refused, ok)
	}
}

func TestLoopsPolicySteward(t *testing.T) {
	h, l := newStewardHarness(t)
	s, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "small job"}, wire.ServiceSteward)
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "small job"}, wire.ServicePlanner)
	if err != nil {
		t.Fatal(err)
	}
	// Writes, but nothing a person's only.
	allowed(t, "save a loop", h.callAs(s.ID, wire.MethodLoopsSave, wire.LoopSaveParams{ID: "lp-1", Title: ptr("Ship it")}, nil))
	allowed(t, "an item anywhere", h.callAs(s.ID, wire.MethodLoopsItem, wire.LoopItemParams{ID: "lp-7"}, nil))
	allowed(t, "a milestone", h.callAs(p.ID, wire.MethodGoalsSave, wire.GoalSaveParams{Milestone: &wire.MilestonePatch{Title: ptr("Beta"), Status: ptr(wire.MilestoneProposed)}}, nil))
	allowed(t, "its inbox", h.callAs(s.ID, wire.MethodStewardInbox, nil, nil))
	forbidden(t, "close a loop", h.callAs(s.ID, wire.MethodLoopsSave, wire.LoopSaveParams{ID: "lp-1", Status: ptr(wire.LoopDropped)}, nil))
	forbidden(t, "finish a goal", h.callAs(s.ID, wire.MethodGoalsSave, wire.GoalSaveParams{Goal: &wire.GoalPatch{ID: "gl-1", Status: ptr(wire.LoopDone)}}, nil))
	forbidden(t, "finish a milestone", h.callAs(s.ID, wire.MethodGoalsSave, wire.GoalSaveParams{Milestone: &wire.MilestonePatch{ID: "ms-1", Status: ptr(wire.LoopDone)}}, nil))
	forbidden(t, "accept a milestone", h.callAs(p.ID, wire.MethodGoalsSave, wire.GoalSaveParams{Milestone: &wire.MilestonePatch{ID: "ms-1", Status: ptr(wire.LoopOpen)}}, nil))
	forbidden(t, "create an open milestone", h.callAs(s.ID, wire.MethodGoalsSave, wire.GoalSaveParams{Milestone: &wire.MilestonePatch{Title: ptr("Beta"), Status: ptr(wire.LoopOpen)}}, nil))
	forbidden(t, "reopen a goal", h.callAs(s.ID, wire.MethodGoalsSave, wire.GoalSaveParams{Goal: &wire.GoalPatch{ID: "gl-1", Status: ptr(wire.LoopOpen)}}, nil))
	allowed(t, "send a milestone back", h.callAs(p.ID, wire.MethodGoalsSave, wire.GoalSaveParams{Milestone: &wire.MilestonePatch{ID: "ms-1", Status: ptr(wire.MilestoneProposed)}}, nil))
	for _, m := range []string{wire.MethodStewardSet, wire.MethodGoalsAccept, wire.MethodGoalsRemove, wire.MethodLoopsSettingsSet} {
		forbidden(t, m, h.callAs(s.ID, m, map[string]any{}, nil))
	}
	// Merge and split: the Steward at level act only.
	forbidden(t, "merge at suggest", h.callAs(s.ID, wire.MethodLoopsMerge, wire.LoopMergeParams{Into: "lp-1", From: "lp-2"}, nil))
	l.setLevel(wire.StewardAct)
	allowed(t, "merge at act", h.callAs(s.ID, wire.MethodLoopsMerge, wire.LoopMergeParams{Into: "lp-1", From: "lp-2"}, nil))
	allowed(t, "split at act", h.callAs(s.ID, wire.MethodLoopsSplit, wire.LoopSplitParams{ID: "lp-1", Ref: "n1/x"}, nil))
	forbidden(t, "a planner merges", h.callAs(p.ID, wire.MethodLoopsMerge, wire.LoopMergeParams{Into: "lp-1", From: "lp-2"}, nil))
	// Drafts: from level suggest on.
	l.setLevel(wire.StewardWatch)
	forbidden(t, "a draft at watch", h.callAs(s.ID, "drafts.save", wire.DraftSaveParams{Draft: wire.Draft{Text: "follow up"}}, nil))
	l.setLevel(wire.StewardSuggest)
	allowed(t, "a draft at suggest", h.callAs(s.ID, "drafts.save", wire.DraftSaveParams{Draft: wire.Draft{Text: "follow up"}}, nil))
	allowed(t, "a person's draft", h.call("drafts.save", wire.DraftSaveParams{Draft: wire.Draft{Text: "mine"}}, nil))
	// Starting agents: only drafts, only at act, never let-parent-answer.
	_, err = h.spawnAs(s.ID, wire.SpawnParams{Task: "small job"})
	forbidden(t, "a spawn", err)
	_, err = h.spawnAs(s.ID, wire.SpawnParams{Task: "small job", Draft: "d-abc"})
	forbidden(t, "a draft start at suggest", err)
	l.setLevel(wire.StewardAct)
	_, err = h.spawnAs(s.ID, wire.SpawnParams{Task: "small job", Draft: "d-abc", LetParentAnswer: true})
	forbidden(t, "let-parent-answer", err)
	child, err := h.spawnAs(s.ID, wire.SpawnParams{Task: "small job", Draft: "d-abc"})
	allowed(t, "a draft start at act", err)
	if child.Parent != s.ID || child.LetParentAnswer {
		t.Fatalf("draft start %+v", child)
	}
	_, err = h.spawnAs(p.ID, wire.SpawnParams{Task: "small job", Draft: "d-abc"})
	forbidden(t, "a planner starts a draft", err)
	forbidden(t, "a resume", h.callAs(s.ID, "sessions.resume", map[string]any{"id": "x"}, nil))
	// The Steward's own calls reach it as the Steward.
	allowed(t, "steward.plan", h.callAs(s.ID, wire.MethodStewardPlan, wire.StewardPlanParams{Scope: "p-1"}, nil))
}

// TestStewardPlanPolicy: a person plans, and the Steward from level
// suggest on; never a planner nor any other agent.
func TestStewardPlanPolicy(t *testing.T) {
	h, l := newStewardHarness(t)
	s, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "small job"}, wire.ServiceSteward)
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "small job"}, wire.ServicePlanner)
	if err != nil {
		t.Fatal(err)
	}
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	plan := wire.StewardPlanParams{Scope: "p-1"}
	allowed(t, "a person", h.call(wire.MethodStewardPlan, plan, nil))
	allowed(t, "the Steward at suggest", h.callAs(s.ID, wire.MethodStewardPlan, plan, nil))
	forbidden(t, "a planner", h.callAs(p.ID, wire.MethodStewardPlan, plan, nil))
	forbidden(t, "an agent", h.callAs(a.ID, wire.MethodStewardPlan, plan, nil))
	l.setLevel(wire.StewardWatch)
	forbidden(t, "the Steward at watch", h.callAs(s.ID, wire.MethodStewardPlan, plan, nil))
	l.setLevel(wire.StewardAct)
	allowed(t, "the Steward at act", h.callAs(s.ID, wire.MethodStewardPlan, plan, nil))
}

// TestSpawnServiceTreeLimits: a service agent's children count against
// maxAgentChildren and maxAgentDepth like any agent's.
func TestSpawnServiceTreeLimits(t *testing.T) {
	h := newHarness(t)
	h.close()
	writeJSON(t, filepath.Join(h.config, "settings.json"), map[string]any{
		"defaults":      map[string]any{"kind": "claude", "kinds": map[string]string{"claude": "fake-claude", "codex": "fake-codex", "shell": "fake-shell"}},
		"maxAgentDepth": 1, "maxAgentChildren": 1,
	})
	h.open()
	s, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "small job"}, wire.ServiceSteward)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "small job", Parent: s.ID, Depth: 1}, wire.ServicePlanner)
	allowed(t, "planner 1", err)
	_, err = h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "small job", Parent: s.ID, Depth: 1}, wire.ServicePlanner)
	forbidden(t, "planner 2", err)
	if !strings.Contains(err.Error(), "maxAgentChildren") {
		t.Fatalf("message %q", err)
	}
	_, err = h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "small job", Parent: p1.ID, Depth: 2}, wire.ServicePlanner)
	forbidden(t, "depth 2", err)
}

// TestHistoryAndDraftsPolicy: an agent never deletes or archives history,
// and removes or replaces only the drafts it saved.
func TestHistoryAndDraftsPolicy(t *testing.T) {
	h, _ := newStewardHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	b := h.spawn(wire.SpawnParams{Task: "small job"})
	forbidden(t, "delete history", h.callAs(a.ID, "sessions.delete", wire.SessionDeleteParams{ID: "s-1"}, nil))
	forbidden(t, "archive history", h.callAs(a.ID, "sessions.archive", wire.SessionArchiveParams{ID: "s-1", Archived: true}, nil))
	var mine wire.Draft
	allowed(t, "save a draft", h.callAs(a.ID, "drafts.save", wire.DraftSaveParams{Draft: wire.Draft{ID: "d-mine", Text: "follow up"}}, &mine))
	if mine.SavedBy != a.ID {
		t.Fatalf("saved by %q", mine.SavedBy)
	}
	allowed(t, "a person's draft", h.call("drafts.save", wire.DraftSaveParams{Draft: wire.Draft{ID: "d-person", Text: "mine"}}, nil))
	forbidden(t, "remove a person's draft", h.callAs(a.ID, "drafts.remove", wire.IDParams{ID: "d-person"}, nil))
	forbidden(t, "replace a person's draft", h.callAs(a.ID, "drafts.save", wire.DraftSaveParams{Draft: wire.Draft{ID: "d-person", Text: "gone"}}, nil))
	forbidden(t, "remove another agent's draft", h.callAs(b.ID, "drafts.remove", wire.IDParams{ID: "d-mine"}, nil))
	allowed(t, "remove its own draft", h.callAs(a.ID, "drafts.remove", wire.IDParams{ID: "d-mine"}, nil))
	allowed(t, "a person removes", h.call("drafts.remove", wire.IDParams{ID: "d-person"}, nil))
}

// TestLinkAgentsPolicy: an ordinary agent links only itself and the
// agents it started (descendants) to its loop.
func TestLinkAgentsPolicy(t *testing.T) {
	h, _ := newStewardHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	other := h.spawn(wire.SpawnParams{Task: "small job"})
	child, err := h.spawnAs(a.ID, wire.SpawnParams{Task: "small job"})
	allowed(t, "child", err)
	refOf := func(id string) string { return "n1/" + strings.TrimPrefix(id, "L/") }
	link := func(v wire.LoopLink, remove bool) error {
		return h.callAs(a.ID, wire.MethodLoopsLink, wire.LoopLinkParams{ID: a.Loop, Link: v, Remove: remove}, nil)
	}
	allowed(t, "itself", link(wire.LoopLink{Kind: wire.LinkAgent, Agent: a.ID}, false))
	allowed(t, "its child by id", link(wire.LoopLink{Kind: wire.LinkAgent, Agent: child.ID}, false))
	allowed(t, "its child by ref", link(wire.LoopLink{Kind: wire.LinkAgent, Ref: refOf(child.ID)}, false))
	forbidden(t, "another agent by id", link(wire.LoopLink{Kind: wire.LinkAgent, Agent: other.ID}, false))
	forbidden(t, "another agent by ref", link(wire.LoopLink{Kind: wire.LinkAgent, Ref: refOf(other.ID)}, false))
	forbidden(t, "another agent's link removed", link(wire.LoopLink{Kind: wire.LinkAgent, Ref: refOf(other.ID)}, true))
	allowed(t, "a URL", link(wire.LoopLink{Kind: wire.LinkURL, Ref: "https://example.com/pr/1"}, false))
}
