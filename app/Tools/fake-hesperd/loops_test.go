package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func newLoopsDaemon(t *testing.T) (*daemon, chan []byte) {
	t.Helper()
	d := &daemon{agents: map[string]*agentProc{}, subs: map[chan []byte]bool{}, self: "L", projects: map[string]*Project{}, groups: map[string]*Group{}, loops: newLoopState()}
	d.seedDemoProjects()
	d.seedDemoLoops()
	sub := make(chan []byte, 256)
	d.subs[sub] = true
	return d, sub
}

func call(t *testing.T, d *daemon, method string, params any) json.RawMessage {
	t.Helper()
	b, _ := json.Marshal(params)
	res, err := d.loopsCall(method, b)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	out, _ := json.Marshal(res)
	return out
}

func drain(sub chan []byte) []string {
	var out []string
	for {
		select {
		case b := <-sub:
			var m struct{ Method string }
			_ = json.Unmarshal(b, &m)
			out = append(out, m.Method)
		default:
			return out
		}
	}
}

func TestDemoLoopsCoverEveryColumn(t *testing.T) {
	d, _ := newLoopsDaemon(t)
	var list []Loop
	_ = json.Unmarshal(call(t, d, "loops.list", map[string]any{}), &list)
	next := map[string]int{}
	looksDone, machines := 0, map[string]bool{}
	for _, l := range list {
		next[l.Next]++
		if l.LooksDone {
			looksDone++
		}
		for _, k := range l.Links {
			if k.Machine != "" {
				machines[k.Machine] = true
			}
		}
	}
	for _, c := range []string{"you", "agents", "others", "parked"} {
		if next[c] == 0 {
			t.Errorf("no loop in column %s: %v", c, next)
		}
	}
	if next["closed"] != 0 || looksDone < 2 || !machines["L"] || !machines["M"] {
		t.Errorf("closed %d, looks done %d, machines %v", next["closed"], looksDone, machines)
	}
	var all []Loop
	_ = json.Unmarshal(call(t, d, "loops.list", map[string]any{"status": "all"}), &all)
	if len(all) != len(list)+1 {
		t.Errorf("status all: %d loops, want %d", len(all), len(list)+1)
	}
	var push Loop
	_ = json.Unmarshal(call(t, d, "loops.get", map[string]any{"id": "lp-0000000push"}), &push)
	if push.Next != "you" || push.NextText != "Approve the FCM sender ID in the console" || push.Counts.OpenYou != 1 || push.Counts.Sessions != 1 {
		t.Errorf("push: %+v", push)
	}
	var auth Loop
	_ = json.Unmarshal(call(t, d, "loops.get", map[string]any{"id": "lp-0000000auth"}), &auth)
	if auth.Next != "others" {
		t.Errorf("auth is blocked by an open loop: next %q", auth.Next)
	}
	var goals struct {
		Goals      []Goal
		Milestones []Milestone
	}
	_ = json.Unmarshal(call(t, d, "goals.list", map[string]any{"scope": "g-acme"}), &goals)
	proposed := 0
	for _, m := range goals.Milestones {
		if m.Status == "proposed" {
			proposed++
		}
		if m.ID == "ms-00000000beta" && (m.Done != 1 || m.Total != 2 || len(m.Loops) != 2) {
			t.Errorf("beta: %+v", m)
		}
	}
	if len(goals.Goals) != 1 || proposed != 2 {
		t.Errorf("goals %d, proposed %d", len(goals.Goals), proposed)
	}
}

func TestLoopsMethodsNotify(t *testing.T) {
	d, sub := newLoopsDaemon(t)
	asLoop := func(raw json.RawMessage) (l Loop) { _ = json.Unmarshal(raw, &l); return l }
	l := asLoop(call(t, d, "loops.save", map[string]any{"title": "New loop"}))
	if !strings.HasPrefix(l.ID, "lp-") || l.Status != "open" || l.Next != "you" || !l.LooksDone {
		t.Fatalf("created: %+v", l)
	}
	l = asLoop(call(t, d, "loops.item", map[string]any{"id": l.ID, "item": map[string]any{"text": "Check the logs"}}))
	if len(l.Items) != 1 || l.Counts.OpenYou != 1 || l.LooksDone {
		t.Fatalf("item: %+v", l)
	}
	l = asLoop(call(t, d, "loops.item", map[string]any{"id": l.ID, "item": map[string]any{"id": l.Items[0].ID, "state": "done"}}))
	if l.Items[0].State != "done" || l.Items[0].Done.IsZero() {
		t.Fatalf("done: %+v", l.Items[0])
	}
	call(t, d, "loops.link", map[string]any{"id": l.ID, "link": map[string]any{"kind": "url", "ref": "https://example.com/x"}})
	call(t, d, "goals.file", map[string]any{"milestone": "ms-00000launch", "loop": l.ID})
	call(t, d, "loops.save", map[string]any{"id": l.ID, "status": "parked"})
	call(t, d, "loops.merge", map[string]any{"into": "lp-00000badges", "from": l.ID})
	got := drain(sub)
	// save, item, item, link; file (the milestone, the loop); park (the
	// loop, its milestone); merge (the merged one goes, the other changes).
	want := []string{"loops.changed", "loops.changed", "loops.changed", "loops.changed", "goals.changed", "loops.changed",
		"loops.changed", "goals.changed", "loops.removed", "loops.changed"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("notes:\n got %v\nwant %v", got, want)
	}
	var split Loop
	_ = json.Unmarshal(call(t, d, "loops.split", map[string]any{"id": "lp-0000000push", "ref": "n-l/push01", "title": "Brief"}), &split)
	// The root, the file it handed over, the agents it (or the file) started.
	if len(split.Links) != 4 {
		t.Errorf("split moved %d links: %+v", len(split.Links), split.Links)
	}
	var accepted struct{ Accepted []string }
	_ = json.Unmarshal(call(t, d, "goals.accept", map[string]any{"scope": "g-acme", "ids": []string{"ms-000proposed1"}}), &accepted)
	if strings.Join(accepted.Accepted, ",") != "ms-000proposed1" {
		t.Errorf("accepted %v", accepted.Accepted)
	}
	call(t, d, "steward.plan", map[string]any{"scope": "p-hesper"})
	var st StewardStatus
	_ = json.Unmarshal(call(t, d, "steward.set", map[string]any{"enabled": false, "level": "watch"}), &st)
	if st.Enabled || st.Level != "watch" || st.Agent != "" {
		t.Errorf("steward: %+v", st)
	}
	var set LoopSettings
	_ = json.Unmarshal(call(t, d, "loops.settings.set", map[string]any{"loopLimit": 3, "eveningAt": "19:00"}), &set)
	if set.LoopLimit != 3 || set.EveningAt != "19:00" || set.StallDays != 7 {
		t.Errorf("settings %+v", set)
	}
	call(t, d, "goals.remove", map[string]any{"id": "ms-000proposed2"})
	if _, err := d.loopsCall("loops.get", json.RawMessage(`{"id":"lp-nope"}`)); err == nil || err.Data["code"] != "not_found" {
		t.Errorf("missing loop: %v", err)
	}
	if _, err := d.loopsCall("loops.save", json.RawMessage(`{"id":"lp-00000badges","status":"weird"}`)); err == nil {
		t.Error("bad status accepted")
	}
	if _, err := d.loopsCall("loops.nope", nil); err == nil || err.Code != -32601 {
		t.Errorf("unknown method: %v", err)
	}
}

func TestLoopsSnapshot(t *testing.T) {
	d, _ := newLoopsDaemon(t)
	var methods []string
	for _, b := range d.loopsSnapshot() {
		var m struct {
			Method string
			Params map[string]json.RawMessage
		}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, m.Method)
	}
	n := map[string]int{}
	for _, m := range methods {
		n[m]++
	}
	// Open and parked loops only (not the closed one), every goal and
	// milestone, the Steward last.
	if n["loops.changed"] != len(d.loops.loops)-1 || n["goals.changed"] != len(d.loops.goals)+len(d.loops.milestones) || methods[len(methods)-1] != "steward.changed" {
		t.Errorf("snapshot %v", n)
	}
}
