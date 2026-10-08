package transport_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Move work across Macs (docs/rebuild-contract.md, "As built — move
// work").

// appRepo makes L's project a repository on main (pushed to a bare
// "remote" when withRemote), then on branch feature with a commit and
// staged, unstaged, untracked and ignored files.
func appRepo(t *testing.T, L *node, withRemote bool) {
	t.Helper()
	git(t, L.project, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(L.project, ".gitignore"), []byte(".env\n"), 0o644)
	os.WriteFile(filepath.Join(L.project, "a.txt"), []byte("one\n"), 0o644)
	git(t, L.project, "add", ".")
	git(t, L.project, "commit", "-q", "--no-gpg-sign", "-m", "first")
	if withRemote {
		addRemote(t, L)
	}
	git(t, L.project, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(L.project, "c.txt"), []byte("feature\n"), 0o644)
	git(t, L.project, "add", "c.txt")
	git(t, L.project, "commit", "-q", "--no-gpg-sign", "-m", "feature commit")
	os.WriteFile(filepath.Join(L.project, "b.txt"), []byte("staged\n"), 0o644)
	git(t, L.project, "add", "b.txt")
	os.WriteFile(filepath.Join(L.project, "a.txt"), []byte("one\nUNCOMMITTED-WORK\n"), 0o644)
	os.WriteFile(filepath.Join(L.project, "u.txt"), []byte("untracked\n"), 0o644)
	os.WriteFile(filepath.Join(L.project, ".env"), []byte("SECRET=ignored\n"), 0o644)
}

// addRemote pushes L's main to a bare repository and names it origin.
func addRemote(t *testing.T, L *node) string {
	t.Helper()
	bare := filepath.Join(L.dir, "remote", "app.git")
	os.MkdirAll(filepath.Dir(bare), 0o755)
	git(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	git(t, L.project, "remote", "add", "origin", bare)
	git(t, L.project, "push", "-q", "origin", "main")
	git(t, L.project, "fetch", "-q", "origin")
	return bare
}

// checkoutState is what a move must never change in the source's
// checkout: index, branches, stash, HEAD, files.
func checkoutState(t *testing.T, dir string) string {
	t.Helper()
	return strings.Join([]string{git(t, dir, "ls-files", "-s"), git(t, dir, "for-each-ref", "refs/heads", "refs/stash"),
		git(t, dir, "rev-parse", "HEAD"), git(t, dir, "status", "--porcelain", "--untracked-files=all", "--ignored")}, "\n")
}

// moveEvents collects agents.moving steps and the removal of id on a
// subscription.
type moveEvents struct {
	steps   []string
	percent []int
	reason  string
	to      string
}

func collectMove(t *testing.T, sub *wire.Client, id string, until func(e *moveEvents) bool) *moveEvents {
	t.Helper()
	e := &moveEvents{}
	deadline := time.After(60 * time.Second)
	for !until(e) {
		select {
		case n := <-sub.Notifications():
			switch n.Method {
			case wire.NoteMoving:
				var m wire.Moving
				json.Unmarshal(n.Params, &m)
				if m.ID == id {
					if len(e.steps) == 0 || e.steps[len(e.steps)-1] != m.Step {
						e.steps = append(e.steps, m.Step)
					}
					if m.Step == wire.MoveTransfer {
						e.percent = append(e.percent, m.Percent)
					}
				}
			case wire.NoteRemoved:
				var r wire.Removed
				json.Unmarshal(n.Params, &r)
				if r.ID == id {
					e.reason = r.Reason
					if r.Data != nil {
						e.to = r.Data.To
					}
				}
			}
		case <-deadline:
			t.Fatalf("move events of %s: %+v", id, e)
		}
	}
	return e
}

// A move L → M: preflight passes, the folder is checkpointed, M (without
// the project) clones it from its remote, makes a worktree on the same
// branch with the same staged, unstaged and untracked files (ignored ones
// stay), places the conversation for that worktree and resumes the agent
// with the handover note; L's agent is closed with reason "moved"
// (data.to the new one), its checkout untouched; the progress shows on
// L's subscription; the relay sees nothing of it. Then undo: back to L.
func TestRemoteMoveWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	appRepo(t, L, true)
	L.waitLinked(t, "M")
	var a wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Project: L.project, Task: "SECRET-MOVE-TASK"}, &a); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, a.ID, inState(wire.StateDone))
	transcript := filepath.Join(L.home, ".claude", "projects", handoff.ClaudeSlug(L.project), a.SessionID+".jsonl")
	os.MkdirAll(filepath.Dir(transcript), 0o700)
	os.WriteFile(transcript, []byte(`{"cwd":"`+L.project+`","message":"SECRET-CONVERSATION"}`+"\n"), 0o600)
	before := checkoutState(t, L.project)
	sub := L.client(t)
	if err := sub.Call(w.ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}

	var res wire.MoveResult
	if err := L.call(t, "agents.move", wire.MoveParams{ID: a.ID, To: "M"}, &res); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(M.home, "worktrees", "app", "feature")
	moved := res.Moved
	if res.Agent != localOn(a.ID, "M") || moved.ID != res.Agent || moved.Project != M.project || moved.Worktree != worktree ||
		moved.Branch != "feature" || moved.SessionID != a.SessionID {
		t.Fatalf("moved %+v (agent %s)", moved, res.Agent)
	}
	ev := collectMove(t, sub, a.ID, func(e *moveEvents) bool {
		return e.reason != "" && len(e.steps) > 0 && e.steps[len(e.steps)-1] == wire.MoveDone
	})
	if ev.reason != wire.ReasonMoved || ev.to != res.Agent {
		t.Fatalf("removed %q to %q", ev.reason, ev.to)
	}
	if strings.Join(ev.steps, ",") != "checkpoint,transfer,worktree,resume,done" || ev.percent[len(ev.percent)-1] != 100 {
		t.Fatalf("steps %v percent %v", ev.steps, ev.percent)
	}

	// L: the checkout as it was, a checkpoint of it, the agent gone.
	if after := checkoutState(t, L.project); after != before {
		t.Fatalf("L's checkout changed:\n%s\n---\n%s", before, after)
	}
	ref := "refs/hesper/checkpoints/" + strings.TrimPrefix(a.ID, "L/")
	if files := git(t, L.project, "ls-tree", "-r", "--name-only", ref); !strings.Contains(files, "u.txt") || strings.Contains(files, ".env") {
		t.Fatalf("checkpoint files %s", files)
	}
	var list []wire.Agent
	L.call(t, "agents.list", nil, &list)
	for _, x := range list {
		if x.ID == a.ID {
			t.Fatal("the source still lists the moved agent")
		}
	}

	// M: a clone of the remote, the worktree with the same work.
	if origin := git(t, M.project, "remote", "get-url", "origin"); origin != filepath.Join(L.dir, "remote", "app.git") {
		t.Fatalf("M's clone: %s", origin)
	}
	if got, _ := os.ReadFile(filepath.Join(worktree, "a.txt")); string(got) != "one\nUNCOMMITTED-WORK\n" {
		t.Fatalf("a.txt on M: %q", got)
	}
	if got := git(t, worktree, "log", "-1", "--format=%s"); got != "feature commit" {
		t.Fatalf("HEAD on M: %s", got)
	}
	status := git(t, worktree, "status", "--porcelain", "--untracked-files=all", "--ignored")
	for _, want := range []string{"A  b.txt", "M a.txt", "?? u.txt"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status on M lacks %q:\n%s", want, status)
		}
	}
	if _, err := os.Stat(filepath.Join(worktree, ".env")); err == nil {
		t.Fatal("an ignored file moved")
	}
	placed := filepath.Join(M.home, ".claude", "projects", handoff.ClaudeSlug(worktree), a.SessionID+".jsonl")
	if data, err := os.ReadFile(placed); err != nil || !strings.Contains(string(data), `"cwd":"`+worktree+`"`) {
		t.Fatalf("transcript on M: %s %v", data, err)
	}
	eventually(t, "resumed on M", func() bool { return strings.Contains(M.argvs(res.Agent), `"--resume","`+a.SessionID+`"`) })
	eventually(t, "the handover note", func() bool {
		var r wire.AgentResult
		L.call(t, "agents.result", wire.IDParams{ID: res.Agent}, &r)
		return strings.Contains(r.Message, "You were moved from L to M. Your worktree is now "+worktree+" on branch feature, with the same uncommitted changes. Processes you started on L did not move.")
	})
	if leaked := w.tap.sawAny("SECRET-CONVERSATION", "SECRET-MOVE-TASK", "UNCOMMITTED-WORK"); leaked != "" {
		t.Fatalf("the relay saw %q", leaked)
	}

	// Undo: back to L, where the branch is checked out (clean now): the
	// agent lands there again with the work.
	M.waitAgent(t, res.Agent, inState(wire.StateDone))
	os.WriteFile(filepath.Join(worktree, "m.txt"), []byte("from M\n"), 0o644)
	git(t, L.project, "checkout", "-q", "--", ".")
	git(t, L.project, "reset", "-q")
	git(t, L.project, "clean", "-qfd")
	var back wire.MoveResult
	if err := L.call(t, "agents.move", wire.MoveParams{ID: res.Agent, To: "L"}, &back); err != nil {
		t.Fatal(err)
	}
	if back.Agent != a.ID || back.Moved.Project != L.project || back.Moved.Worktree != "" {
		t.Fatalf("back %+v", back)
	}
	if got, _ := os.ReadFile(filepath.Join(L.project, "m.txt")); string(got) != "from M\n" {
		t.Fatalf("work back on L: %q", got)
	}
	eventually(t, "M closed its agent", func() bool {
		var list []wire.Agent
		M.call(t, "agents.list", nil, &list)
		for _, x := range list {
			if x.ID == localOn(res.Agent, "M") {
				return false
			}
		}
		return true
	})
}

// Preflight errors leave the agent as it was: no-remote, busy (and
// interrupt), processes (listed; leaveProcesses), too-large,
// tool-missing, offline; fork keeps the source.
func TestRemoteMovePreflightAndFork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	w := newWorld(t, worldOptions{beforeStart: func(L, M *node) {
		exe, _ := os.Executable()
		writeJSON(t, filepath.Join(M.config, "profiles.json"), map[string]wire.Profile{
			"fake-claude": {Kind: wire.KindClaude, Argv: []string{exe, "claude", "--permission-mode", "auto", "--remote-control", "{name}"}},
			"fake-shell":  {Kind: wire.KindShell, Argv: []string{exe, "shell"}},
			"fake-codex":  {Kind: wire.KindCodex, Argv: []string{"/nonexistent/codex"}}, // M has no codex
		})
	}})
	L, M := w.L, w.M
	appRepo(t, L, false)
	L.waitLinked(t, "M")
	var a wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Project: L.project, Task: "first"}, &a); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, a.ID, inState(wire.StateDone))
	move := func(p wire.MoveParams) (wire.MoveResult, *wire.Error) {
		t.Helper()
		var res wire.MoveResult
		err := L.call(t, "agents.move", p, &res)
		if err == nil {
			return res, nil
		}
		we, ok := err.(*wire.Error)
		if !ok {
			t.Fatalf("move: %v", err)
		}
		return res, we
	}
	untouched := func() {
		t.Helper()
		got := L.waitAgent(t, a.ID, func(x wire.Agent) bool { return x.Exit == nil })
		if got.Project != L.project {
			t.Fatalf("source %+v", got)
		}
		if _, err := os.Stat(filepath.Join(M.project, ".git")); err == nil {
			t.Fatal("M got the project")
		}
	}

	// No remote and not on M.
	if _, err := move(wire.MoveParams{ID: a.ID, To: "M"}); err == nil || err.Code != wire.CodeNoRemote {
		t.Fatalf("no-remote: %v", err)
	}
	untouched()

	// Busy; with interrupt it is interrupted first (then no-remote).
	L.call(t, "agents.input", wire.InputParams{ID: a.ID, Text: "keep working", Submit: true}, nil)
	L.waitAgent(t, a.ID, inState(wire.StateWorking))
	if _, err := move(wire.MoveParams{ID: a.ID, To: "M"}); err == nil || err.Code != wire.CodeBusy {
		t.Fatalf("busy: %v", err)
	}
	L.waitAgent(t, a.ID, inState(wire.StateWorking))
	if _, err := move(wire.MoveParams{ID: a.ID, To: "M", Interrupt: true}); err == nil || err.Code != wire.CodeNoRemote {
		t.Fatalf("interrupted: %v", err)
	}
	L.waitAgent(t, a.ID, inState(wire.StateDone))

	// Processes it started: listed.
	L.call(t, "agents.input", wire.InputParams{ID: a.ID, Text: "start a dev server", Submit: true}, nil)
	var child int
	eventually(t, "the dev server", func() bool {
		data, _ := os.ReadFile(filepath.Join(L.logs, strings.ReplaceAll(a.ID, "/", "_")+".child"))
		child, _ = strconv.Atoi(string(data))
		return child > 0
	})
	t.Cleanup(func() { syscall.Kill(-child, syscall.SIGKILL); syscall.Kill(child, syscall.SIGKILL) })
	L.waitAgent(t, a.ID, inState(wire.StateDone))
	addRemote(t, L)
	_, err := move(wire.MoveParams{ID: a.ID, To: "M"})
	if err == nil || err.Code != wire.CodeProcesses || len(err.Processes) != 1 || !strings.Contains(err.Processes[0].Command, "sleep 600") {
		t.Fatalf("processes: %+v", err)
	}
	untouched()

	// Too large.
	old := agents.MaxMoveBytes
	agents.MaxMoveBytes = 16
	_, err = move(wire.MoveParams{ID: a.ID, To: "M", LeaveProcesses: true})
	agents.MaxMoveBytes = old
	if err == nil || err.Code != wire.CodeTooLarge {
		t.Fatalf("too-large: %v", err)
	}
	untouched()

	// No codex on M.
	var cx wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Project: L.project, Kind: wire.KindCodex, Task: "codex"}, &cx); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, cx.ID, inState(wire.StateDone))
	if _, err := move(wire.MoveParams{ID: cx.ID, To: "M"}); err == nil || err.Code != wire.CodeToolMissing {
		t.Fatalf("tool-missing: %v", err)
	}

	// Fork: both run, the source unchanged.
	transcript := filepath.Join(L.home, ".claude", "projects", handoff.ClaudeSlug(L.project), a.SessionID+".jsonl")
	os.MkdirAll(filepath.Dir(transcript), 0o700)
	os.WriteFile(transcript, []byte(`{"cwd":"`+L.project+`","message":"hi"}`+"\n"), 0o600)
	res, err := move(wire.MoveParams{ID: a.ID, To: "M", Fork: true, LeaveProcesses: true})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if res.Agent != localOn(a.ID, "M") || res.Moved.Branch != "feature" {
		t.Fatalf("fork %+v", res)
	}
	L.waitAgent(t, res.Agent, func(x wire.Agent) bool { return x.Exit == nil })
	untouchedSource := L.waitAgent(t, a.ID, func(x wire.Agent) bool { return x.Exit == nil })
	if untouchedSource.Project != L.project {
		t.Fatalf("fork source %+v", untouchedSource)
	}
	eventually(t, "the fork's note", func() bool {
		var r wire.AgentResult
		L.call(t, "agents.result", wire.IDParams{ID: res.Agent}, &r)
		return strings.Contains(r.Message, "You were forked from L to M.") && strings.Contains(r.Message, "The original agent keeps running on L.")
	})

	// Offline.
	M.stop()
	eventually(t, "M offline", func() bool {
		var hello wire.HelloResult
		L.call(t, "hello", wire.HelloParams{}, &hello)
		for _, m := range hello.Machines {
			if m.Short == "M" {
				return !m.Online
			}
		}
		return true
	})
	if _, err := move(wire.MoveParams{ID: a.ID, To: "M"}); err == nil || err.Code != wire.CodeOffline {
		t.Fatalf("offline: %v", err)
	}
	untouchedSource = L.waitAgent(t, a.ID, func(x wire.Agent) bool { return x.Exit == nil })
}
