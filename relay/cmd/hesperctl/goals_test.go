package main

import (
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestGoalsCommands(t *testing.T) {
	e := fullDaemon(t)
	e.ok(t, "projects", "promote", e.project, "--name", "app")
	gid := strings.TrimSpace(e.ok(t, "groups", "save", "--name", "suite", "--project", "app"))

	// A goal and a milestone under it, with conditions, for a project.
	var ch wire.GoalChanged
	e.okJSON(t, &ch, "goals", "new", "app", "Fewer", "support", "tickets")
	if ch.Goal == nil || ch.Goal.Title != "Fewer support tickets" || !strings.HasPrefix(ch.Goal.Scope, "p-") {
		t.Fatalf("new goal: %+v", ch)
	}
	goal := ch.Goal.ID
	e.okJSON(t, &ch, "goals", "new", "app", "1.4", "--milestone", "--goal", "fewer", "--due", "2026-12-01", "--when", "login fix merged", "--when", "tag pushed")
	m := ch.Milestone
	if m == nil || m.Goal != goal || m.Due.IsZero() || m.Total != 2 || len(m.Items) != 2 {
		t.Fatalf("new milestone: %+v", ch)
	}
	e.fails(t, exitUsage, codeUsage, "goals", "new", "app", "x", "--due", "3d")
	e.fails(t, exitNotFound, wire.CodeNotFound, "goals", "new", "nope", "x")
	// A group is a product too.
	e.okJSON(t, &ch, "goals", "new", "suite", "One", "login")
	if ch.Goal == nil || ch.Goal.Scope != gid {
		t.Fatalf("group goal: %+v", ch)
	}

	// check by number, file a loop, list.
	var ms wire.Milestone
	e.okJSON(t, &ms, "goals", "check", "1.4", "2")
	if ms.Done != 1 {
		t.Fatalf("check: %+v", ms)
	}
	e.okJSON(t, &ms, "goals", "check", "1.4", "2", "--undo")
	if ms.Done != 0 {
		t.Fatalf("undo: %+v", ms)
	}
	loop := strings.TrimSpace(e.ok(t, "loops", "new", "Login fix"))
	e.okJSON(t, &ms, "goals", "file", "1.4", "login fix")
	if len(ms.Loops) != 1 || ms.Loops[0] != loop {
		t.Fatalf("file: %+v", ms.Loops)
	}
	var list []wire.Loop
	e.okJSON(t, &list, "loops", "ls", "--milestone", "1.4")
	if len(list) != 1 || list[0].ID != loop {
		t.Fatalf("loops ls --milestone: %+v", list)
	}
	var gl wire.GoalsList
	e.okJSON(t, &gl, "goals", "ls", "app")
	if len(gl.Goals) != 1 || len(gl.Milestones) != 1 {
		t.Fatalf("ls app: %+v", gl)
	}
	if out := e.ok(t, "goals", "ls"); !strings.Contains(out, "app (") || !strings.Contains(out, "suite (") || !strings.Contains(out, "0/2") || !strings.Contains(out, "1 loops") {
		t.Errorf("ls:\n%s", out)
	}
	e.okJSON(t, &ms, "goals", "file", "1.4", loop, "--remove")
	if len(ms.Loops) != 0 {
		t.Fatalf("unfile: %+v", ms.Loops)
	}

	// save: title, due cleared, auto-file; proposed and accept.
	e.okJSON(t, &ch, "goals", "save", "1.4", "--title", "1.4 release", "--due", "", "--auto-file", "--when", "notes out")
	if ch.Milestone == nil || ch.Milestone.Title != "1.4 release" || !ch.Milestone.Due.IsZero() || !ch.Milestone.AutoFile {
		t.Fatalf("save: %+v", ch)
	}
	e.okJSON(t, &gl, "goals", "ls", "app")
	if gl.Milestones[0].Total != 3 {
		t.Fatalf("--when on save: %+v", gl.Milestones[0])
	}
	e.ok(t, "goals", "save", m.ID, "--status", wire.MilestoneProposed)
	var acc struct {
		Accepted int `json:"accepted"`
	}
	e.okJSON(t, &acc, "goals", "accept", "app")
	if acc.Accepted != 1 {
		t.Fatalf("accept: %+v", acc)
	}
	e.fails(t, exitUsage, codeUsage, "goals", "save", goal, "--due", "3d")
	e.fails(t, exitUsage, codeUsage, "goals", "save", goal, "--status", "proposed")

	// The Steward is not part of this daemon: plan is unavailable.
	e.fails(t, exitUnavailable, wire.CodeUnavailable, "goals", "plan", "app")

	// rm.
	e.ok(t, "goals", "rm", "1.4 release")
	e.ok(t, "goals", "rm", goal)
	e.fails(t, exitNotFound, wire.CodeNotFound, "goals", "rm", goal)
}
