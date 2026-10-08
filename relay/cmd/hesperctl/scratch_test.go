package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Scratch projects: new --scratch, scratch ls|new|keep|archive|restore|
// promote|rm (a temporary home; never the user's ~/scratch).
func TestScratchCommands(t *testing.T) {
	e := fullDaemon(t)
	scratchRoot := filepath.Join(e.dir, "home", "scratch")
	date := time.Now().Format("2006-01-02")

	// new --scratch: the agent runs in a new scratch project named from
	// its task.
	var a wire.Agent
	e.okJSON(t, &a, "new", "--scratch", "Try the CSV parser on the export")
	want := filepath.Join(scratchRoot, date+"-try-the-csv-parser-on-the-export")
	if a.Project != want || !strings.HasPrefix(a.ProjectID, "p-") {
		t.Fatalf("new --scratch: project %s (%s), want %s", a.Project, a.ProjectID, want)
	}
	if _, err := os.Stat(filepath.Join(want, ".git")); err != nil {
		t.Fatalf("no repository: %v", err)
	}
	e.fails(t, exitUsage, codeUsage, "new", "--scratch", "--project", e.project, "x")

	// scratch new prints the folder; the same name twice gets -2.
	spike := strings.TrimSpace(e.ok(t, "scratch", "new", "spike"))
	if spike != filepath.Join(scratchRoot, date+"-spike") {
		t.Fatalf("scratch new: %q", spike)
	}
	var made wire.ScratchResult
	e.okJSON(t, &made, "scratch", "new", "spike")
	if made.Path != spike+"-2" || made.Project.Kind != wire.ProjectScratch || made.Project.Scratch == nil || made.Project.Scratch.Home != "L" {
		t.Fatalf("scratch new --json: %+v", made)
	}

	var list []wire.ProjectInfo
	e.okJSON(t, &list, "scratch", "ls")
	states := map[string]string{}
	for _, p := range list {
		states[p.Name] = p.Scratch.State
	}
	if len(list) != 3 || states["try the csv parser on the export"] != wire.ScratchActive || states["spike"] != wire.ScratchResting {
		t.Fatalf("scratch ls: %v", states)
	}
	if out := e.ok(t, "scratch", "ls"); !strings.Contains(out, "STATE") || !strings.Contains(out, "active") {
		t.Errorf("scratch ls:\n%s", out)
	}

	// keep, archive (hidden from ls, in ls --all), restore.
	var p wire.ProjectInfo
	e.okJSON(t, &p, "scratch", "keep", made.Project.ID)
	if !p.Scratch.Keep {
		t.Fatalf("keep: %+v", p.Scratch)
	}
	e.okJSON(t, &p, "scratch", "keep", made.Project.ID, "--off")
	if p.Scratch.Keep {
		t.Fatalf("keep --off: %+v", p.Scratch)
	}
	e.okJSON(t, &p, "scratch", "archive", made.Path)
	if p.Scratch.State != wire.ScratchArchived || p.Paths["L"] != filepath.Join(scratchRoot, ".archive", date+"-spike-2") {
		t.Fatalf("archive: %+v %v", p.Scratch, p.Paths)
	}
	e.okJSON(t, &list, "scratch", "ls")
	if len(list) != 2 {
		t.Fatalf("archived listed: %d", len(list))
	}
	e.okJSON(t, &list, "scratch", "ls", "--all")
	if len(list) != 3 {
		t.Fatalf("ls --all: %d", len(list))
	}
	e.okJSON(t, &p, "scratch", "restore", made.Project.ID)
	if p.Scratch.State != wire.ScratchResting || p.Paths["L"] != made.Path {
		t.Fatalf("restore: %+v %v", p.Scratch, p.Paths)
	}

	// promote: busy while an agent runs in it; to ~/projects, same id.
	e.fails(t, exitExists, wire.CodeBusy, "scratch", "promote", a.ProjectID)
	e.okJSON(t, &p, "scratch", "promote", made.Project.ID, "--name", "Spike Tool")
	if p.ID != made.Project.ID || p.Kind != wire.ProjectRepo || p.Name != "Spike Tool" || p.Paths["L"] != filepath.Join(e.root, "spike-tool") {
		t.Fatalf("promote: %+v", p)
	}
	e.fails(t, exitUnavailable, wire.CodeUnavailable, "scratch", "promote", spike, "--github") // no gh here
	e.fails(t, exitNotFound, wire.CodeNotFound, "scratch", "archive", "nope")

	// rm: busy while its agent runs; then the folder goes.
	e.fails(t, exitExists, wire.CodeBusy, "scratch", "rm", a.ProjectID)
	e.ok(t, "stop", a.ID)
	eventually(t, "stopped", func() bool {
		var agents []wire.Agent
		e.okJSON(t, &agents, "ls")
		for _, x := range agents {
			if x.ID == a.ID {
				return x.Exit != nil
			}
		}
		return false
	})
	e.ok(t, "scratch", "rm", a.ProjectID)
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("folder still there: %v", err)
	}
	e.okJSON(t, &list, "scratch", "ls", "--all")
	if len(list) != 1 || list[0].Name != "spike" {
		t.Fatalf("after rm: %+v", list)
	}
}
