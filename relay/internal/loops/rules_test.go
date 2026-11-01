package loops

import (
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func agentLink(ref, state, ending string) wire.LoopLink {
	return wire.LoopLink{Kind: wire.LinkAgent, Ref: ref, Rel: wire.RelMember, Label: ref, State: state, Ending: ending}
}

func item(id, owner, state, kind string, after ...string) wire.LoopItem {
	return wire.LoopItem{ID: id, Text: "do " + id, Owner: owner, State: state, Kind: kind, After: after}
}

func TestNext(t *testing.T) {
	blockerOpen := wire.Loop{ID: "lp-b", Title: "the API", Status: wire.LoopOpen}
	blockerDone := wire.Loop{ID: "lp-b", Title: "the API", Status: wire.LoopDone}
	for _, tc := range []struct {
		name      string
		loop      wire.Loop
		blockedBy []wire.Loop
		who, text string
		looksDone bool
	}{
		{name: "parked", loop: wire.Loop{Status: wire.LoopParked, NextText: "after the release",
			Items: []wire.LoopItem{item("a", wire.OwnerYou, wire.ItemOpen, wire.ItemStep)}}, who: wire.NextParked, text: "after the release"},
		{name: "done", loop: wire.Loop{Status: wire.LoopDone}, who: wire.NextClosed},
		{name: "dropped", loop: wire.Loop{Status: wire.LoopDropped}, who: wire.NextClosed},
		{name: "approval beats working", loop: wire.Loop{Status: wire.LoopOpen, Links: []wire.LoopLink{
			agentLink("n/w", wire.StateWorking, ""), agentLink("n/a", wire.StateApproval, "")}},
			who: wire.NextYou, text: "n/a waits for your approval"},
		{name: "question", loop: wire.Loop{Status: wire.LoopOpen, Links: []wire.LoopLink{agentLink("n/q", wire.StateQuestion, "")}},
			who: wire.NextYou, text: "n/q asks you something"},
		{name: "quiet agent asks", loop: wire.Loop{Status: wire.LoopOpen, Links: []wire.LoopLink{
			{Kind: wire.LinkAgent, Ref: "n/x", Rel: wire.RelStarted, Label: "x", State: wire.StateIdle, Ending: wire.EndingAsks, Summary: "Which port?"},
			agentLink("n/w", wire.StateWorking, "")}},
			who: wire.NextYou, text: "Which port?"},
		{name: "quiet agent your step", loop: wire.Loop{Status: wire.LoopOpen, Links: []wire.LoopLink{agentLink("n/x", wire.StateDone, wire.EndingYourStep)}},
			who: wire.NextYou, text: "n/x left a step for you"},
		{name: "gone agent's ending does not count", loop: wire.Loop{Status: wire.LoopOpen, Links: []wire.LoopLink{agentLink("n/x", "", wire.EndingAsks)}},
			who: wire.NextYou, text: "Looks done", looksDone: true},
		{name: "item for you, ready first", loop: wire.Loop{Status: wire.LoopOpen, Items: []wire.LoopItem{
			item("a", wire.OwnerYou, wire.ItemOpen, wire.ItemStep, "z"), item("b", wire.OwnerYou, wire.ItemOpen, wire.ItemStep),
			item("z", wire.OwnerAgent, wire.ItemOpen, wire.ItemStep)}},
			who: wire.NextYou, text: "do b"},
		{name: "offer is not your move", loop: wire.Loop{Status: wire.LoopOpen, Items: []wire.LoopItem{item("o", wire.OwnerYou, wire.ItemOpen, wire.ItemOffer)}},
			who: wire.NextYou, text: "Looks done", looksDone: true},
		{name: "one agent works", loop: wire.Loop{Status: wire.LoopOpen, Links: []wire.LoopLink{agentLink("n/w", wire.StateWorking, "")}},
			who: wire.NextAgents, text: "n/w works"},
		{name: "agents work", loop: wire.Loop{Status: wire.LoopOpen, Links: []wire.LoopLink{agentLink("n/w", wire.StateWorking, ""), agentLink("n/v", wire.StateStarting, "")}},
			who: wire.NextAgents, text: "2 agents work"},
		{name: "item for others", loop: wire.Loop{Status: wire.LoopOpen, Items: []wire.LoopItem{item("c", wire.OwnerOthers, wire.ItemOpen, wire.ItemLooseEnd)}},
			who: wire.NextOthers, text: "do c"},
		{name: "blocked by an open loop", loop: wire.Loop{Status: wire.LoopOpen}, blockedBy: []wire.Loop{blockerOpen},
			who: wire.NextOthers, text: "Waits for the API"},
		{name: "blocker done", loop: wire.Loop{Status: wire.LoopOpen}, blockedBy: []wire.Loop{blockerDone},
			who: wire.NextYou, text: "Looks done", looksDone: true},
		{name: "done items, quiet agents", loop: wire.Loop{Status: wire.LoopOpen, Links: []wire.LoopLink{agentLink("n/x", wire.StateIdle, wire.EndingDone)},
			Items: []wire.LoopItem{item("a", wire.OwnerYou, wire.ItemDone, wire.ItemStep)}},
			who: wire.NextYou, text: "Looks done", looksDone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			markReady(tc.loop.Items)
			who, text, looksDone := next(&tc.loop, tc.blockedBy)
			if who != tc.who || text != tc.text || looksDone != tc.looksDone {
				t.Fatalf("next = %q %q %v, want %q %q %v", who, text, looksDone, tc.who, tc.text, tc.looksDone)
			}
		})
	}
}

func TestReadyCountsProjects(t *testing.T) {
	items := []wire.LoopItem{
		item("a", wire.OwnerYou, wire.ItemDone, wire.ItemStep),
		item("b", wire.OwnerYou, wire.ItemOpen, wire.ItemStep, "a", "gone"),
		item("c", wire.OwnerAgent, wire.ItemOpen, wire.ItemStep, "b"),
		item("d", wire.OwnerOthers, wire.ItemOpen, wire.ItemLooseEnd),
		item("e", wire.OwnerAgent, wire.ItemOpen, wire.ItemOffer),
	}
	markReady(items)
	for i, want := range []bool{false, true, false, true, true} {
		if items[i].Ready != want {
			t.Errorf("%s ready = %v", items[i].ID, items[i].Ready)
		}
	}
	l := wire.Loop{Items: items, Links: []wire.LoopLink{
		agentLink("n/w", wire.StateWorking, ""), agentLink("n/i", wire.StateIdle, ""), agentLink("n/gone", "", ""),
		{Kind: wire.LinkSession, Ref: "s:claude:1", Rel: wire.RelMember},
		{Kind: wire.LinkProject, Ref: "p-2", Rel: wire.RelTouches}, {Kind: wire.LinkProject, Ref: "p-1", Rel: wire.RelTouches},
		{Kind: wire.LinkProject, Ref: wire.ScratchPrefix + "/tmp/x", Rel: wire.RelTouches},
	}}
	want := wire.LoopCounts{Agents: 2, Working: 1, OpenYou: 1, OpenAgents: 1, OpenOthers: 1, Sessions: 1}
	if got := counts(&l); got != want {
		t.Fatalf("counts %+v, want %+v", got, want)
	}
	if got := projectsOf(&l); len(got) != 2 || got[0] != "p-1" || got[1] != "p-2" {
		t.Fatalf("projects %v", got)
	}
}

func TestMilestoneRules(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	b := &built{
		loops: map[string]wire.Loop{
			"lp-1": {ID: "lp-1", Milestone: "ms-1", Status: wire.LoopOpen, Updated: now.Add(-10 * 24 * time.Hour)},
			"lp-2": {ID: "lp-2", Milestone: "ms-1", Status: wire.LoopOpen, MergedInto: "lp-1", Updated: now},
			"lp-3": {ID: "lp-3", Milestone: "ms-2", Status: wire.LoopOpen, Updated: now.Add(-time.Hour)},
		},
		milestones: map[string]wire.Milestone{
			"ms-1": {ID: "ms-1", Status: wire.LoopOpen, Updated: now.Add(-9 * 24 * time.Hour), Items: []wire.LoopItem{
				item("a", wire.OwnerYou, wire.ItemDone, wire.ItemCondition), item("b", wire.OwnerYou, wire.ItemOpen, wire.ItemCondition),
				item("c", wire.OwnerYou, wire.ItemDropped, wire.ItemCondition)}},
			"ms-2": {ID: "ms-2", Status: wire.LoopOpen, Updated: now.Add(-30 * 24 * time.Hour), Items: []wire.LoopItem{
				item("a", wire.OwnerYou, wire.ItemDone, wire.ItemCondition)}},
			"ms-3": {ID: "ms-3", Status: wire.MilestoneProposed, Updated: now.Add(-30 * 24 * time.Hour)},
		},
	}
	applyRules(b, wire.LoopSettings{StallDays: 7}, now)
	m1, m2, m3 := b.milestones["ms-1"], b.milestones["ms-2"], b.milestones["ms-3"]
	if len(m1.Loops) != 1 || m1.Loops[0] != "lp-1" || m1.Done != 1 || m1.Total != 2 || !m1.Stalled || m1.NextText != "do b" {
		t.Fatalf("ms-1 %+v", m1)
	}
	if m2.Stalled || m2.NextText != "Every condition is done" || m2.Done != 1 || m2.Total != 1 {
		t.Fatalf("ms-2 %+v (its loop changed an hour ago)", m2)
	}
	if m3.Stalled {
		t.Fatal("a proposed milestone stalls")
	}
}
