package main

import (
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestDraftCommands(t *testing.T) {
	e := fullDaemon(t)
	var p wire.ProjectInfo
	e.okJSON(t, &p, "projects", "promote", e.project, "--name", "app")
	e.ok(t, "projects", "update", "app", "--default-profile", "cat")

	// In a project: its band, its folder, its default profile.
	var d wire.Draft
	e.okJSON(t, &d, "drafts", "save", "--project", "app", "fix", "the", "login")
	if !strings.HasPrefix(d.ID, "d-") || d.Text != "fix the login" || d.Band != p.ID || d.Project != e.project || d.Profile != "cat" || d.Machine != "" || d.Created.IsZero() {
		t.Fatalf("save: %+v", d)
	}
	// Change it: only what is given.
	var c wire.Draft
	e.okJSON(t, &c, "drafts", "save", "--id", d.ID, "--kind", "codex", "--worktree", "--branch", "fix-login")
	if c.ID != d.ID || c.Text != d.Text || c.Profile != "catx" || c.Worktree == nil || !*c.Worktree || c.Branch != "fix-login" || c.Band != p.ID {
		t.Fatalf("change: %+v", c)
	}
	// A folder; this machine named is stored as none (explicit).
	id := strings.TrimSpace(e.ok(t, "drafts", "save", "--project", e.dir, "--machine", "L", "--profile", "cat", "look", "around"))
	var list []wire.Draft
	e.okJSON(t, &list, "drafts", "ls")
	if len(list) != 2 {
		t.Fatalf("ls: %+v", list)
	}
	for _, o := range list {
		if o.ID == id && (o.Project != e.dir || o.Band != "" || o.Machine != "" || !o.MachineExplicit || o.Profile != "cat") {
			t.Errorf("folder draft: %+v", o)
		}
	}
	if out := e.ok(t, "drafts", "ls"); !strings.Contains(out, "fix the login") || !strings.Contains(out, id) {
		t.Errorf("ls:\n%s", out)
	}
	e.fails(t, exitNotFound, wire.CodeNotFound, "drafts", "save", "--id", "d-nope", "x")
	e.fails(t, exitNotFound, wire.CodeNotFound, "drafts", "save", "--project", "nope", "x")
	e.fails(t, exitUsage, codeUsage, "drafts", "save", "--kind", "lisp", "x")
	e.ok(t, "drafts", "rm", id)
	e.fails(t, exitNotFound, wire.CodeNotFound, "drafts", "rm", id)

	// profiles and status.
	var profiles wire.ProfilesResult
	e.okJSON(t, &profiles, "profiles")
	if profiles.Profiles["catx"].Kind != wire.KindCodex || profiles.Defaults.Kinds["codex"] != "catx" {
		t.Fatalf("profiles: %+v", profiles)
	}
	if out := e.ok(t, "profiles"); !strings.Contains(out, "catx") || !strings.Contains(out, "Default kind: claude") {
		t.Errorf("profiles:\n%s", out)
	}
	var h wire.HelloResult
	e.okJSON(t, &h, "status")
	if h.Daemon != "hesperd" || h.Machine != "L" || len(h.Machines) != 1 || h.Machines[0].Route != "local" {
		t.Fatalf("status: %+v", h)
	}
	if out := e.ok(t, "status"); !strings.Contains(out, "hesperd") || !strings.Contains(out, "local") {
		t.Errorf("status:\n%s", out)
	}
}
