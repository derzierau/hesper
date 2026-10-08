package transport_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/projects"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func gitInit(t *testing.T, dir, remote string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", remote}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

// eventually polls until ok, failing after 20 s.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (n *node) projectByID(t *testing.T, id string) (wire.ProjectInfo, bool) {
	t.Helper()
	var list []wire.ProjectInfo
	if err := n.call(t, "projects.list", nil, &list); err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.ID == id {
			return p, true
		}
	}
	return wire.ProjectInfo{}, false
}

// The same repository on L and M (different folders, ssh and https
// remotes) is one project with both paths; a remote agent in it has the
// projectId a local one has; renames, groups, promotes and removals made
// on either Mac reach the other; the relay sees no name, path, remote or
// project id.
func TestProjectsSharedBetweenMacs(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	M.waitLinked(t, "L")
	repoL := filepath.Join(L.home, "projects", "SECRETDIR-5150-l")
	repoM := filepath.Join(M.home, "src", "SECRETDIR-5150-m")
	gitInit(t, repoL, "git@github.com:acme/SECRETREPO-4411.git")
	gitInit(t, repoM, "https://github.com/Acme/SECRETREPO-4411")
	want := projects.ProjectID(wire.ProjectIdentity{Remote: "github.com/acme/secretrepo-4411"})

	sub := L.client(t)
	if err := sub.Call(w.ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	var nmu sync.Mutex
	seen := map[string][]string{}
	go func() {
		for n := range sub.Notifications() {
			nmu.Lock()
			seen[n.Method] = append(seen[n.Method], string(n.Params))
			nmu.Unlock()
		}
	}()
	saw := func(method, needle string) bool {
		nmu.Lock()
		defer nmu.Unlock()
		for _, p := range seen[method] {
			if strings.Contains(p, needle) {
				return true
			}
		}
		return false
	}

	// A local agent in a subfolder of L's clone.
	var local wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Project: filepath.Join(repoL, "src"), Task: "hello there"}, &local); err != nil {
		t.Fatal(err)
	}
	if local.ProjectID != want {
		t.Fatalf("local agent's project %q, want %q", local.ProjectID, want)
	}
	// A remote agent in M's clone, spawned through L: the same project.
	var remote wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Project: repoM, Task: "hello from M"}, &remote); err != nil {
		t.Fatal(err)
	}
	if remote.ProjectID != want {
		t.Fatalf("remote agent's project %q, want %q", remote.ProjectID, want)
	}
	L.waitAgent(t, remote.ID, func(a wire.Agent) bool { return a.ProjectID == want })
	// One project, both folders, on both Macs.
	for _, n := range []*node{L, M} {
		eventually(t, n.short+" lists both folders", func() bool {
			p, ok := n.projectByID(t, want)
			return ok && p.Paths["L"] == repoL && p.Paths["M"] == repoM && p.Kind == wire.ProjectRepo && p.Name == "SECRETREPO-4411"
		})
	}
	// Renamed on L → M; a group made on M → L.
	name := "SECRETNAME-8080"
	if err := L.call(t, "projects.update", wire.ProjectUpdateParams{ID: want, Name: &name}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "M sees the new name", func() bool { p, _ := M.projectByID(t, want); return p.Name == name })
	var g wire.Group
	if err := M.call(t, "groups.save", wire.GroupSaveParams{Group: wire.Group{Name: "SECRETGROUP-3030", ProjectIDs: []string{want}}}, &g); err != nil {
		t.Fatal(err)
	}
	eventually(t, "L has the group", func() bool {
		var gs []wire.Group
		L.call(t, "groups.list", nil, &gs)
		return len(gs) == 1 && gs[0].ID == g.ID && len(gs[0].ProjectIDs) == 1
	})
	eventually(t, "L's subscription saw it", func() bool {
		return saw(wire.NoteProjectChanged, name) && saw(wire.NoteGroupChanged, "SECRETGROUP-3030") && saw(wire.NoteProjectChanged, g.ID)
	})
	// A folder on M promoted from L.
	notes := filepath.Join(M.home, "SECRETDIR-5150-notes")
	os.MkdirAll(notes, 0o755)
	var promoted wire.ProjectInfo
	if err := L.call(t, "projects.promote", wire.ProjectPromoteParams{Machine: "M", Path: notes, Kind: "reference"}, &promoted); err != nil {
		t.Fatal(err)
	}
	if promoted.Paths["M"] != notes || promoted.Kind != wire.ProjectReference || promoted.Identity.Local == "" {
		t.Fatalf("promoted %+v", promoted)
	}
	// Removed on L: gone on M too; M's agents there fall back to scratch.
	var inNotes wire.Agent
	if err := M.call(t, "agents.spawn", wire.SpawnParams{Project: notes, Task: "notes agent"}, &inNotes); err != nil || inNotes.ProjectID != promoted.ID {
		t.Fatalf("agent in the promoted folder: %+v %v", inNotes, err)
	}
	if err := L.call(t, "projects.remove", wire.IDParams{ID: promoted.ID}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "M forgot the folder", func() bool { _, ok := M.projectByID(t, promoted.ID); return !ok })
	M.waitAgent(t, inNotes.ID, func(a wire.Agent) bool { return a.ProjectID == wire.ScratchPrefix+notes })
	eventually(t, "L lists M's scratch folder", func() bool {
		p, ok := L.projectByID(t, wire.ScratchPrefix+notes)
		return ok && p.Kind == wire.ProjectScratch && p.Paths["M"] == notes
	})
	if !saw(wire.NoteProjectRemoved, promoted.ID) {
		t.Fatal("no projects.removed on L")
	}
	// projects.recent: the project first.
	var recent []wire.Project
	if err := L.call(t, "projects.recent", nil, &recent); err != nil || len(recent) == 0 || recent[0].ProjectID != want || recent[0].Path != repoL {
		t.Fatalf("recent %+v %v", recent, err)
	}
	// The relay saw none of it.
	if leaked := w.tap.sawAny("SECRETREPO", "secretrepo", "SECRETDIR-5150", "SECRETNAME-8080", "SECRETGROUP-3030", want, promoted.ID, g.ID); leaked != "" {
		t.Fatalf("the relay saw %q", leaked)
	}
	if w.tap.count(`"method":"projects.`) != 0 {
		t.Fatal("a projects method went in plaintext")
	}
	// Persisted on both.
	for _, n := range []*node{L, M} {
		data, err := os.ReadFile(filepath.Join(n.state, "projects.json"))
		var f struct {
			Version  int               `json:"version"`
			Projects []json.RawMessage `json:"projects"`
		}
		if err != nil || json.Unmarshal(data, &f) != nil || f.Version != 2 || !strings.Contains(string(data), want) {
			t.Fatalf("%s projects.json: %v %s", n.short, err, data)
		}
	}
}
