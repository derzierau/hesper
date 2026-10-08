package transport_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Scratch projects across Macs: an agent spawned with scratch: true on L
// runs in a new scratch project (L's home/scratch, a repository without
// a remote); agents.move to M carries the whole repository (no
// "no-remote"), makes the folder at M's scratch/<same name> with the
// uncommitted work, keeps the project id and makes M the scratch's home;
// L's daemon sees that. Archive from L is then forwarded to M.
func TestRemoteMoveScratch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	var a wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Scratch: true, Task: "Scratch move check"}, &a); err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(a.Project)
	if filepath.Dir(a.Project) != filepath.Join(L.home, "scratch") || !strings.HasSuffix(base, "-scratch-move-check") {
		t.Fatalf("scratch folder %s", a.Project)
	}
	if remotes := git(t, a.Project, "remote"); remotes != "" {
		t.Fatalf("remotes %q", remotes)
	}
	L.waitAgent(t, a.ID, inState(wire.StateDone))
	os.WriteFile(filepath.Join(a.Project, "notes.txt"), []byte("SCRATCH-WORK\n"), 0o644)
	transcript := filepath.Join(L.home, ".claude", "projects", handoff.ClaudeSlug(a.Project), a.SessionID+".jsonl")
	os.MkdirAll(filepath.Dir(transcript), 0o700)
	os.WriteFile(transcript, []byte(`{"cwd":"`+a.Project+`","message":"hi"}`+"\n"), 0o600)
	var list []wire.ProjectInfo
	eventually(t, "M knows the scratch", func() bool {
		M.call(t, "projects.list", nil, &list)
		for _, p := range list {
			if p.ID == a.ProjectID {
				return p.Scratch != nil && p.Scratch.Home == "L"
			}
		}
		return false
	})

	var res wire.MoveResult
	if err := L.call(t, "agents.move", wire.MoveParams{ID: a.ID, To: "M"}, &res); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(M.home, "scratch", base)
	if res.Moved.Project != target || res.Moved.Worktree != "" || res.Moved.ProjectID != a.ProjectID {
		t.Fatalf("moved %+v", res.Moved)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "notes.txt")); string(got) != "SCRATCH-WORK\n" {
		t.Fatalf("notes on M: %q", got)
	}
	if first := git(t, target, "log", "--format=%s", "-1", "main"); !strings.HasPrefix(first, "Start scratch: ") {
		t.Fatalf("history on M: %q", first)
	}
	if remotes := git(t, target, "remote"); remotes != "" {
		t.Fatalf("remotes on M %q", remotes)
	}
	home := func(n *node) string {
		var list []wire.ProjectInfo
		n.call(t, "projects.list", wire.ProjectListParams{Archived: true}, &list)
		for _, p := range list {
			if p.ID == a.ProjectID && p.Scratch != nil {
				return p.Scratch.Home + " " + p.Paths["M"]
			}
		}
		return ""
	}
	eventually(t, "M is the scratch's home, on both", func() bool {
		return home(M) == "M "+target && home(L) == "M "+target
	})

	// Archive asked on L: done by M (its home), once M's agent is gone.
	M.call(t, "agents.close", wire.IDParams{ID: res.Agent}, nil)
	eventually(t, "archived on M", func() bool {
		var p wire.ProjectInfo
		if err := L.call(t, "projects.scratchArchive", wire.IDParams{ID: a.ProjectID}, &p); err != nil {
			t.Logf("archive: %v", err)
			return false
		}
		return p.Scratch.State == wire.ScratchArchived
	})
	if _, err := os.Stat(filepath.Join(M.home, "scratch", ".archive", base, "notes.txt")); err != nil {
		t.Fatalf("not in M's archive: %v", err)
	}
}
