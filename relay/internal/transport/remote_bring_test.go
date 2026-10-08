package transport_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Bring the folder along (docs/rebuild-contract.md, "As built — bring
// the folder"): agents.spawn on M with params.bring brings a folder of L
// (or the other way round) and starts the agent there.

// bringSpawn spawns on machine with bring; the error as a *wire.Error.
func bringSpawn(t *testing.T, on *node, p wire.SpawnParams) (wire.Agent, *wire.Error) {
	t.Helper()
	var a wire.Agent
	err := on.call(t, "agents.spawn", p, &a)
	if err == nil {
		return a, nil
	}
	we, ok := err.(*wire.Error)
	if !ok {
		t.Fatalf("spawn: %v", err)
	}
	return a, we
}

// collectBring collects the agents.bringing notes of draft until the
// bring ended (done or failed).
func collectBring(t *testing.T, sub *wire.Client, draft string) []wire.Bringing {
	t.Helper()
	var notes []wire.Bringing
	deadline := time.After(60 * time.Second)
	for {
		select {
		case n := <-sub.Notifications():
			if n.Method != wire.NoteBringing {
				continue
			}
			var b wire.Bringing
			json.Unmarshal(n.Params, &b)
			if b.Draft != draft {
				continue
			}
			notes = append(notes, b)
			if b.Step == wire.MoveDone || b.Step == wire.MoveFailed {
				return notes
			}
		case <-deadline:
			t.Fatalf("bring events of %s: %+v", draft, notes)
		}
	}
}

func bringSteps(notes []wire.Bringing) string {
	var steps []string
	for _, b := range notes {
		if len(steps) == 0 || steps[len(steps)-1] != b.Step {
			steps = append(steps, b.Step)
		}
	}
	return strings.Join(steps, ",")
}

// projectPaths is a project's paths (per machine) as on n.
func projectPaths(t *testing.T, n *node, id string) map[string]string {
	var list []wire.ProjectInfo
	n.call(t, "projects.list", wire.ProjectListParams{Archived: true}, &list)
	for _, p := range list {
		if p.ID == id {
			return p.Paths
		}
	}
	return nil
}

// A repository with a remote, on a feature branch with staged, unstaged
// and untracked work: M clones it from the remote at the same place
// under its home, on the branch, with the same work (ignored files
// stay); the agent starts there; L's checkout is untouched; the progress
// shows on L's subscription with the draft; the catalog has the folder
// as the project's on M. Then: M has it, so another bring is "exists"
// (with the path), nothing written.
func TestRemoteBringRepoWithRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	src := *L
	src.project = filepath.Join(L.home, "projects", "brought")
	os.MkdirAll(src.project, 0o755)
	appRepo(t, &src, true)
	before := checkoutState(t, src.project)
	L.waitLinked(t, "M")
	sub := L.client(t)
	if err := sub.Call(w.ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}

	a, werr := bringSpawn(t, L, wire.SpawnParams{Machine: "M", Task: "SECRET-BRING-TASK", Draft: "draft-1",
		Bring: &wire.Bring{Path: src.project, Changes: wire.BringWith}})
	if werr != nil {
		t.Fatal(werr)
	}
	target := filepath.Join(M.home, "projects", "brought")
	if a.Machine != "M" || !strings.HasPrefix(a.ID, "M/") || a.Project != target {
		t.Fatalf("agent %+v", a)
	}
	notes := collectBring(t, sub, "draft-1")
	if got := bringSteps(notes); got != "checkpoint,transfer,unpack,spawn,done" {
		t.Fatalf("steps %s (%+v)", got, notes)
	}
	last := notes[len(notes)-1]
	if last.Agent != a.ID || last.To != "M" || last.From != "L" || last.ID == "" || last.Path != target {
		t.Fatalf("done %+v", last)
	}
	for _, b := range notes {
		if b.Step == wire.BringTransfer && b.Percent == 100 {
			goto percentOK
		}
	}
	t.Fatalf("transfer never reached 100: %+v", notes)
percentOK:

	if after := checkoutState(t, src.project); after != before {
		t.Fatalf("L's checkout changed:\n%s\n---\n%s", before, after)
	}
	if origin := git(t, target, "remote", "get-url", "origin"); origin != filepath.Join(L.dir, "remote", "app.git") {
		t.Fatalf("M's clone: %s", origin)
	}
	if got, want := git(t, target, "rev-parse", "HEAD"), git(t, src.project, "rev-parse", "HEAD"); got != want {
		t.Fatalf("HEAD on M %s, want %s", got, want)
	}
	if branch := git(t, target, "symbolic-ref", "--short", "HEAD"); branch != "feature" {
		t.Fatalf("branch on M %s", branch)
	}
	status := git(t, target, "status", "--porcelain", "--untracked-files=all", "--ignored")
	for _, want := range []string{"A  b.txt", "M a.txt", "?? u.txt"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status on M lacks %q:\n%s", want, status)
		}
	}
	if _, err := os.Stat(filepath.Join(target, ".env")); err == nil {
		t.Fatal("an ignored file came along")
	}
	M.waitAgent(t, localOn(a.ID, "M"), func(x wire.Agent) bool { return x.State != wire.StateStarting })
	if a.ProjectID == "" {
		t.Fatal("the agent on M has no project")
	}
	eventually(t, "the catalog has M's folder", func() bool { return projectPaths(t, L, a.ProjectID)["M"] == target })
	if w.tap.sawAny("SECRET-BRING-TASK", "UNCOMMITTED-WORK") != "" {
		t.Fatal("the relay saw the bring in plaintext")
	}

	// Again: M has it now.
	_, werr = bringSpawn(t, L, wire.SpawnParams{Machine: "M", Task: "again", Draft: "draft-2", Bring: &wire.Bring{Path: src.project}})
	if werr == nil || werr.Code != wire.CodeExists || werr.Path != target {
		t.Fatalf("exists: %+v", werr)
	}
	if notes := collectBring(t, sub, "draft-2"); bringSteps(notes) != "failed" || notes[0].Error.Code != wire.CodeExists {
		t.Fatalf("failed notes %+v", notes)
	}
}

// A repository without a remote comes whole (no "no-remote"), clean:
// the last commit only. The folder M has already (its projects/app) is
// "exists"; L's own hidden folders never travel.
func TestRemoteBringRepoWithoutRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	src := *L
	src.project = filepath.Join(L.home, "work", "tool")
	os.MkdirAll(src.project, 0o755)
	appRepo(t, &src, false)
	L.waitLinked(t, "M")

	a, werr := bringSpawn(t, L, wire.SpawnParams{Machine: "M", Task: "t", Bring: &wire.Bring{Path: src.project, Changes: wire.BringClean}})
	if werr != nil {
		t.Fatal(werr)
	}
	target := filepath.Join(M.home, "work", "tool")
	if a.Project != target {
		t.Fatalf("agent %+v", a)
	}
	if got := git(t, target, "log", "--format=%s", "-1"); got != "feature commit" {
		t.Fatalf("HEAD on M %q", got)
	}
	if status := git(t, target, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("clean bring has changes:\n%s", status)
	}
	if remotes := git(t, target, "remote"); remotes != "" {
		t.Fatalf("remotes %q", remotes)
	}

	// M has projects/app already: exists, nothing written.
	os.WriteFile(filepath.Join(L.project, "x.txt"), []byte("x\n"), 0o644)
	if _, werr := bringSpawn(t, L, wire.SpawnParams{Machine: "M", Task: "t", Bring: &wire.Bring{Path: L.project}}); werr == nil ||
		werr.Code != wire.CodeExists || werr.Path != M.project {
		t.Fatalf("exists: %+v", werr)
	}
	if entries, _ := os.ReadDir(M.project); len(entries) != 0 {
		t.Fatalf("M's folder was written: %v", entries)
	}
	hidden := filepath.Join(L.home, ".ssh")
	os.MkdirAll(hidden, 0o700)
	if _, werr := bringSpawn(t, L, wire.SpawnParams{Machine: "M", Task: "t", Bring: &wire.Bring{Path: hidden}}); werr == nil || werr.Code != wire.CodeInvalid {
		t.Fatalf("hidden: %+v", werr)
	}
}

// A scratch project travels whole to M's scratch folder under the same
// name with its work; the catalog keeps L as its home and records M's
// copy.
func TestRemoteBringScratch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	var s wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Scratch: true, Task: "Scratch bring check"}, &s); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.Project, "notes.txt"), []byte("SCRATCH-WORK\n"), 0o644)
	a, werr := bringSpawn(t, L, wire.SpawnParams{Machine: "M", Task: "t", Bring: &wire.Bring{Path: s.Project}})
	if werr != nil {
		t.Fatal(werr)
	}
	target := filepath.Join(M.home, "scratch", filepath.Base(s.Project))
	if a.Project != target || a.ProjectID != s.ProjectID {
		t.Fatalf("agent %+v (scratch %s)", a, s.ProjectID)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "notes.txt")); string(got) != "SCRATCH-WORK\n" {
		t.Fatalf("notes on M: %q", got)
	}
	if remotes := git(t, target, "remote"); remotes != "" {
		t.Fatalf("remotes on M %q", remotes)
	}
	scratch := func(n *node) string {
		var list []wire.ProjectInfo
		n.call(t, "projects.list", wire.ProjectListParams{Archived: true}, &list)
		for _, p := range list {
			if p.ID == s.ProjectID && p.Scratch != nil {
				return p.Scratch.Home + " " + p.Paths["M"]
			}
		}
		return ""
	}
	eventually(t, "L stays the home; M's copy recorded", func() bool { return scratch(M) == "L "+target && scratch(L) == "L "+target })
}

// A folder outside Git comes as a tar without build and dependency
// folders, symlinks outside it left out; over the cap it is too-large.
// This one goes the other way: M's folder to L, asked on L.
func TestRemoteBringPlainFolder(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	src := filepath.Join(M.home, "Documents", "notes")
	for name, text := range map[string]string{"a.md": "A\n", "sub/b.md": "B\n", "node_modules/x/i.js": "x", "sub/.venv/y": "y", "dist/z": "z"} {
		os.MkdirAll(filepath.Dir(filepath.Join(src, name)), 0o755)
		os.WriteFile(filepath.Join(src, name), []byte(text), 0o644)
	}
	os.Symlink(filepath.Join(M.dir, "outside"), filepath.Join(src, "out"))
	os.Symlink("sub/b.md", filepath.Join(src, "in"))
	L.waitLinked(t, "M")
	sub := L.client(t)
	if err := sub.Call(w.ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}

	a, werr := bringSpawn(t, L, wire.SpawnParams{Task: "t", Draft: "d", Bring: &wire.Bring{From: "M", Path: src}})
	if werr != nil {
		t.Fatal(werr)
	}
	target := filepath.Join(L.home, "Documents", "notes")
	if a.Project != target || a.Machine != "L" {
		t.Fatalf("agent %+v", a)
	}
	if got := bringSteps(collectBring(t, sub, "d")); got != "checkpoint,transfer,unpack,spawn,done" {
		t.Fatalf("steps %s", got)
	}
	var got []string
	filepath.WalkDir(target, func(p string, d os.DirEntry, err error) error {
		if p != target {
			rel, _ := filepath.Rel(target, p)
			got = append(got, rel)
		}
		return nil
	})
	if strings.Join(got, ",") != "a.md,in,sub,sub/b.md" {
		t.Fatalf("brought %v", got)
	}

	// Over the cap.
	old := handoff.MaxFolderBytes
	handoff.MaxFolderBytes = 2
	t.Cleanup(func() { handoff.MaxFolderBytes = old })
	os.RemoveAll(target)
	if _, werr := bringSpawn(t, L, wire.SpawnParams{Task: "t", Bring: &wire.Bring{From: "M", Path: src}}); werr == nil || werr.Code != wire.CodeTooLarge {
		t.Fatalf("too-large: %+v", werr)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a too-large folder was made")
	}
}
