package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// Checkpoints: taken when a turn ends (Agent.checkpoint, persisted, the
// hook told), by agents.checkpoint (unchanged: the same one), and at
// close; a shell is never checkpointed; checkpoints.json lists the repo.
func TestCheckpointOnDoneManualAndClose(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	h := newHarness(t)
	gitIn(t, h.project, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(h.project, "a.txt"), []byte("one\n"), 0o644)
	gitIn(t, h.project, "add", ".")
	gitIn(t, h.project, "commit", "-q", "--no-gpg-sign", "-m", "first")
	os.WriteFile(filepath.Join(h.project, "a.txt"), []byte("one\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(h.project, "new.txt"), []byte("new\n"), 0o644)
	hooked := make(chan wire.Agent, 8)
	h.reg.OnCheckpoint(func(a wire.Agent) { hooked <- a })

	a := h.spawn(wire.SpawnParams{Task: "do it"})
	h.waitState(a.ID, wire.StateDone)
	var got wire.Agent
	waitFor(t, func() bool { got, _ = h.reg.Get(a.ID); return got.Checkpoint != nil })
	cp := got.Checkpoint
	if cp.Ref != "refs/hesper/checkpoints/"+strings.TrimPrefix(a.ID, "L/") || cp.Changed != 2 || cp.Branch != "main" {
		t.Fatalf("checkpoint %+v", cp)
	}
	if gitIn(t, h.project, "rev-parse", cp.Ref) != cp.Commit {
		t.Fatal("ref")
	}
	select {
	case ha := <-hooked:
		if ha.ID != a.ID || ha.Checkpoint == nil || ha.Checkpoint.Commit != cp.Commit || ha.SessionID != a.SessionID {
			t.Fatalf("hook %+v", ha)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no hook")
	}
	if st := gitIn(t, h.project, "status", "--porcelain"); !strings.Contains(st, "M a.txt") || !strings.Contains(st, "?? new.txt") {
		t.Fatalf("the checkout changed: %s", st)
	}
	data, _ := os.ReadFile(filepath.Join(h.state, "checkpoints.json"))
	if !strings.Contains(string(data), h.project) {
		t.Fatalf("checkpoints.json %s", data)
	}

	// Manual, unchanged: the same checkpoint.
	var res wire.CheckpointResult
	if err := h.call("agents.checkpoint", wire.IDParams{ID: a.ID}, &res); err != nil || res.Checkpoint == nil || res.Checkpoint.Commit != cp.Commit {
		t.Fatalf("manual %+v %v", res.Checkpoint, err)
	}
	// Changed, then closed: the close takes a new one (hook), the agent
	// leaves.
	os.WriteFile(filepath.Join(h.project, "b.txt"), []byte("b\n"), 0o644)
	if err := h.call("agents.close", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ha := <-hooked:
			if ha.Checkpoint.Commit == cp.Commit {
				continue
			}
			if ha.Checkpoint.Changed != 3 {
				t.Fatalf("close checkpoint %+v", ha.Checkpoint)
			}
			if gitIn(t, h.project, "rev-parse", cp.Ref+"-prev") != cp.Commit {
				t.Fatal("no -prev")
			}
			goto shell
		case <-deadline:
			t.Fatal("no checkpoint at close")
		}
	}
shell:
	sh := h.spawn(wire.SpawnParams{Kind: "shell"})
	if err := h.call("agents.checkpoint", wire.IDParams{ID: sh.ID}, nil); err == nil {
		t.Fatal("a shell was checkpointed")
	}
	// Outside Git: null.
	h2 := newHarness(t)
	b := h2.spawn(wire.SpawnParams{Task: "do it"})
	h2.waitState(b.ID, wire.StateDone)
	res = wire.CheckpointResult{}
	if err := h2.call("agents.checkpoint", wire.IDParams{ID: b.ID}, &res); err != nil || res.Checkpoint != nil {
		t.Fatalf("outside Git: %+v %v", res, err)
	}
}

// Turn-end checkpoints are debounced: at most one per CheckpointEvery.
func TestCheckpointDebounce(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	old := CheckpointEvery
	CheckpointEvery = 700 * time.Millisecond
	t.Cleanup(func() { CheckpointEvery = old })
	h := newHarness(t)
	gitIn(t, h.project, "init", "-q", "-b", "main")
	gitIn(t, h.project, "commit", "-q", "--allow-empty", "--no-gpg-sign", "-m", "first")
	var times []time.Time
	done := make(chan struct{}, 8)
	h.reg.OnCheckpoint(func(wire.Agent) { times = append(times, time.Now()); done <- struct{}{} })
	a := h.spawn(wire.SpawnParams{Task: "one"})
	h.waitState(a.ID, wire.StateDone)
	<-done
	os.WriteFile(filepath.Join(h.project, "x.txt"), []byte("x\n"), 0o644)
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "two", Submit: true}, nil)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no second checkpoint")
	}
	if gap := times[1].Sub(times[0]); gap < 600*time.Millisecond {
		t.Fatalf("second checkpoint after %v", gap)
	}
}

// The processes a move would leave behind: what runs under the tool's
// command shells, not its other children (MCP servers) nor hook calls.
func TestAgentProcesses(t *testing.T) {
	list := []proc{
		{1, 0, "launchd"},
		{100, 1, "/bin/zsh -l"}, // the profile's login shell
		{101, 100, "claude --resume x"},
		{102, 101, "node /mcp/server.js"},
		{103, 102, "python helper.py"},
		{104, 101, "/bin/zsh -c npm run dev"},
		{105, 104, "node vite"},
		{106, 101, "/bin/sh -c /usr/local/bin/hesperd hook claude Stop"},
		{107, 106, "/usr/local/bin/hesperd hook claude Stop"},
		{108, 101, "/bin/bash -c sleep 100"},
	}
	got := agentProcesses(list, 100, "/usr/local/bin/hesperd")
	if len(got) != 2 || got[0].PID != 105 || got[1].PID != 108 || got[0].Command != "node vite" {
		t.Fatalf("processes %+v", got)
	}
	if got := agentProcesses(list, 999, ""); len(got) != 0 {
		t.Fatalf("no such agent: %+v", got)
	}
}

// The handover note names both machines, the worktree, the branch and
// what stayed behind; a fork says the original continues.
func TestHandoverNote(t *testing.T) {
	m := &handoff.Manifest{Move: &handoff.MoveInfo{From: "laptop", To: "mini", Note: true}}
	m.Project.Bundle = "full"
	note := handoverNote(m, handoff.Placed{Project: "/p", Worktree: "/w/x", Branch: "fix"}, "mini")
	want := "You were moved from laptop to mini. Your worktree is now /w/x on branch fix, with the same uncommitted changes. Processes you started on laptop did not move."
	if note != want {
		t.Fatalf("note %q", note)
	}
	m.Move.Fork = true
	if note := handoverNote(m, handoff.Placed{Project: "/p", Branch: "fix"}, "mini"); !strings.HasPrefix(note, "You were forked from laptop to mini. Your worktree is now /p ") ||
		!strings.HasSuffix(note, "The original agent keeps running on laptop.") {
		t.Fatalf("fork note %q", note)
	}
}

// The tool check of a move's target: the profile's command on PATH.
func TestHasTool(t *testing.T) {
	h := newHarness(t)
	if !h.reg.HasTool(wire.KindClaude) || !h.reg.HasTool(wire.KindCodex) {
		t.Fatal("the fake tools are there")
	}
	writeJSON(t, filepath.Join(h.config, "profiles.json"), map[string]wire.Profile{
		"fake-claude": {Kind: wire.KindClaude, Argv: []string{"/nonexistent/claude"}},
		"fake-codex":  {Kind: wire.KindCodex, Argv: []string{"no-such-codex-here"}},
	})
	h.close()
	h.open()
	if h.reg.HasTool(wire.KindClaude) || h.reg.HasTool(wire.KindCodex) {
		t.Fatal("missing tools reported present")
	}
}
