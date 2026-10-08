package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestProjectAndGroupCommands(t *testing.T) {
	e := fullDaemon(t)
	other := filepath.Join(e.dir, "home", "other")
	os.MkdirAll(other, 0o700)

	var p wire.ProjectInfo
	e.okJSON(t, &p, "projects", "promote", e.project, "--name", "app")
	if p.ID == "" || p.Name != "app" || p.Kind != wire.ProjectFolder || p.Paths["L"] != e.project {
		t.Fatalf("promote: %+v", p)
	}
	id := strings.TrimSpace(e.ok(t, "projects", "promote", other, "--kind", "reference"))
	if !strings.HasPrefix(id, "p-") || id == p.ID {
		t.Fatalf("promote other: %q", id)
	}
	var list []wire.ProjectInfo
	e.okJSON(t, &list, "projects", "ls")
	if len(list) != 2 {
		t.Fatalf("ls: %+v", list)
	}
	if out := e.ok(t, "projects", "ls"); !strings.Contains(out, p.ID) || !strings.Contains(out, "reference") || !strings.Contains(out, "FOLDER") {
		t.Errorf("ls:\n%s", out)
	}

	// update by name, by folder, by id; defaults merge.
	e.okJSON(t, &p, "projects", "update", "app", "--color", "#7aa2f7", "--default-profile", "cat")
	if p.Color != "#7aa2f7" || !p.ColorSet || p.Defaults.Profile != "cat" {
		t.Fatalf("update: %+v", p)
	}
	e.okJSON(t, &p, "projects", "update", e.project, "--default-machine", "L")
	if p.Defaults.Profile != "cat" || p.Defaults.Machine != "L" {
		t.Fatalf("defaults: %+v", p.Defaults)
	}
	e.okJSON(t, &p, "projects", "update", p.ID, "--color", "", "--name", "App")
	if p.ColorSet || p.Name != "App" {
		t.Fatalf("color reset: %+v", p)
	}
	e.fails(t, exitUsage, codeUsage, "projects", "update", "App")
	e.fails(t, exitNotFound, wire.CodeNotFound, "projects", "update", "nope", "--name", "x")
	e.fails(t, exitError, wire.CodeInvalid, "projects", "update", "App", "--kind", "planet")
	// Two projects of one name: ambiguous.
	e.ok(t, "projects", "update", id, "--name", "App")
	if msg := e.fails(t, exitUsage, codeAmbiguous, "projects", "update", "App", "--name", "x"); !strings.Contains(msg, p.ID) {
		t.Errorf("ambiguous: %s", msg)
	}
	e.ok(t, "projects", "update", id, "--name", "other")

	// groups: new, add, remove, rename, rm.
	gid := strings.TrimSpace(e.ok(t, "groups", "save", "--name", "work", "--project", "App", "--project", "other"))
	var groups []wire.Group
	e.okJSON(t, &groups, "groups", "ls")
	if len(groups) != 1 || groups[0].ID != gid || len(groups[0].ProjectIDs) != 2 {
		t.Fatalf("groups: %+v", groups)
	}
	var g wire.Group
	e.okJSON(t, &g, "groups", "save", "work", "--remove-project", "other", "--color", "#bb9af7")
	if len(g.ProjectIDs) != 1 || g.ProjectIDs[0] != p.ID || g.Color != "#bb9af7" {
		t.Fatalf("save: %+v", g)
	}
	second := strings.TrimSpace(e.ok(t, "groups", "save", "--name", "later"))
	e.okJSON(t, &groups, "groups", "ls")
	if len(groups) != 2 || groups[1].ID != second || groups[1].Order <= groups[0].Order {
		t.Fatalf("order: %+v", groups)
	}
	if out := e.ok(t, "groups", "ls"); !strings.Contains(out, "work") || !strings.Contains(out, "App") {
		t.Errorf("groups ls:\n%s", out)
	}
	e.okJSON(t, &list, "projects", "ls")
	for _, q := range list {
		if q.ID == p.ID && (len(q.Groups) != 1 || q.Groups[0] != gid) {
			t.Errorf("project groups %+v", q.Groups)
		}
	}
	e.fails(t, exitUsage, codeUsage, "groups", "save")
	e.fails(t, exitNotFound, wire.CodeNotFound, "groups", "save", "nope", "--name", "x")
	e.ok(t, "groups", "rm", "later")
	e.ok(t, "groups", "rm", gid)
	e.fails(t, exitNotFound, wire.CodeNotFound, "groups", "rm", gid)

	// The history knows the project now.
	var page wire.SessionSearchResult
	eventually(t, "sessions in the project", func() bool {
		e.okJSON(t, &page, "history", "search", "--project", "App")
		return len(page.Items) == 2
	})
	e.okJSON(t, &page, "history", "search", "--project", other)
	if len(page.Items) != 0 {
		t.Errorf("other project: %+v", page.Items)
	}

	// recent, rm, clone.
	var recent []wire.Project
	e.okJSON(t, &recent, "projects", "recent")
	e.ok(t, "projects", "recent")
	e.ok(t, "projects", "rm", "other")
	e.okJSON(t, &list, "projects", "ls")
	if len(list) != 1 {
		t.Fatalf("after rm: %+v", list)
	}
	src := filepath.Join(e.dir, "src", "tool")
	if out, err := exec.Command("git", "init", "-q", src).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if out := strings.TrimSpace(e.ok(t, "projects", "clone", src)); out != filepath.Join(e.root, "tool") {
		t.Fatalf("clone: %q", out)
	}
	e.fails(t, exitError, wire.CodeInvalid, "projects", "clone", "not a url")
	e.fails(t, exitNotFound, wire.CodeNotFound, "projects", "promote", filepath.Join(e.dir, "missing"))
}
