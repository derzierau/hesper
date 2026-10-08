package handoff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func machine(t *testing.T, root, name string) Paths {
	home := filepath.Join(root, name)
	p := Paths{Home: home, ClaudeHome: filepath.Join(home, ".claude"), CodexHome: filepath.Join(home, ".codex")}
	os.MkdirAll(filepath.Join(home, "projects"), 0o755)
	return p
}

func write(t *testing.T, path, text string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A Claude agent in a worktree with staged, unstaged and untracked work
// moves to a machine with another home that has no copy of the project
// (full bundle), then back to the first one, which has it (incremental).
func TestMoveWithConversationAndUncommittedWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	src, dst := machine(t, root, "laptop"), machine(t, root, "mini")
	project := filepath.Join(src.Home, "projects", "app")
	os.MkdirAll(project, 0o755)
	run(t, project, "git", "init", "-q", "-b", "main")
	write(t, filepath.Join(project, "a.txt"), "one\n")
	run(t, project, "git", "add", "a.txt")
	run(t, project, "git", "commit", "-q", "-m", "first")
	worktree := filepath.Join(src.Home, "worktrees", "app", "fix")
	run(t, project, "git", "worktree", "add", "-q", "-b", "feature/fix", worktree)
	write(t, filepath.Join(worktree, "a.txt"), "one\ntwo\n")
	run(t, worktree, "git", "commit", "-q", "-am", "second")
	write(t, filepath.Join(worktree, "staged.txt"), "staged\n")
	run(t, worktree, "git", "add", "staged.txt")
	write(t, filepath.Join(worktree, "a.txt"), "one\ntwo\nthree (unstaged)\n")
	write(t, filepath.Join(worktree, "new.txt"), "untracked\n")
	session := "11111111-2222-4333-8444-555555555555"
	transcript := filepath.Join(src.ClaudeHome, "projects", ClaudeSlug(worktree), session+".jsonl")
	write(t, transcript, `{"type":"user","cwd":"`+worktree+`","message":{"content":"fix <it> & more"},"n":12345678901234567890}`+"\n"+`{"type":"assistant","text":"ok"}`+"\n")
	agent := wire.Agent{ID: "L/abc123", Kind: wire.KindClaude, Profile: "claude-auto-rc", Name: "fix", Task: "Fix it",
		Project: project, Worktree: worktree, Branch: "feature/fix", SessionID: session, Created: time.Now()}
	ctx := context.Background()

	dir := filepath.Join(root, "bundle1")
	plan := PlanFor(ctx, agent, src)
	probe := ProbeRepo(ctx, plan.Project, plan.Home, plan.Commits, dst)
	if probe.Exists || plan.Project != project || len(plan.Commits) != 2 {
		t.Fatalf("plan %+v probe %+v", plan, probe)
	}
	m, err := Pack(ctx, agent, "L", nil, dir, src)
	if err != nil {
		t.Fatal(err)
	}
	if m.Project.Bundle != "full" || m.Agent.Transcript == "" {
		t.Fatalf("manifest %+v", m)
	}
	// The source's checkout is untouched.
	if st := run(t, worktree, "git", "status", "--porcelain"); !strings.Contains(st, "A  staged.txt") || !strings.Contains(st, "M a.txt") || !strings.Contains(st, "?? new.txt") {
		t.Fatalf("source status changed: %q", st)
	}
	_, placed, err := Unpack(ctx, dir, dst)
	if err != nil {
		t.Fatal(err)
	}
	wantWT := filepath.Join(dst.Home, "worktrees", "app", "fix")
	if placed.Project != filepath.Join(dst.Home, "projects", "app") || placed.Worktree != wantWT || !placed.Resume || placed.Branch != "feature/fix" {
		t.Fatalf("placed %+v", placed)
	}
	if st := run(t, wantWT, "git", "status", "--porcelain"); !strings.Contains(st, "A  staged.txt") || !strings.Contains(st, "M a.txt") || !strings.Contains(st, "?? new.txt") {
		t.Fatalf("target status %q", st)
	}
	if b := run(t, wantWT, "git", "symbolic-ref", "--short", "HEAD"); b != "feature/fix" {
		t.Fatalf("branch %s", b)
	}
	data, err := os.ReadFile(filepath.Join(dst.ClaudeHome, "projects", ClaudeSlug(wantWT), session+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"cwd":"`+wantWT+`"`) || !strings.Contains(string(data), "fix <it> & more") || !strings.Contains(string(data), "12345678901234567890") {
		t.Fatalf("transcript %s", data)
	}

	// And back: the source has the project, so the bundle is incremental.
	write(t, filepath.Join(wantWT, "b.txt"), "from mini\n")
	back := agent
	back.Project, back.Worktree = placed.Project, placed.Worktree
	plan = PlanFor(ctx, back, dst)
	probe = ProbeRepo(ctx, plan.Project, plan.Home, plan.Commits, src)
	var have []string
	for c, ok := range probe.Has {
		if ok {
			have = append(have, c)
		}
	}
	if !probe.Exists || len(have) == 0 {
		t.Fatalf("probe %+v", probe)
	}
	// The source must be clean to take it back (the agent left it).
	run(t, worktree, "git", "reset", "-q", "--hard")
	run(t, worktree, "git", "clean", "-qfd")
	dir2 := filepath.Join(root, "bundle2")
	m, err = Pack(ctx, back, "M", have, dir2, dst)
	if err != nil {
		t.Fatal(err)
	}
	if m.Project.Bundle != "incremental" {
		t.Fatalf("bundle %s", m.Project.Bundle)
	}
	if _, placed, err = Unpack(ctx, dir2, src); err != nil {
		t.Fatal(err)
	}
	if placed.Worktree != worktree {
		t.Fatalf("placed %+v", placed)
	}
	if st := run(t, worktree, "git", "status", "--porcelain"); !strings.Contains(st, "?? b.txt") || !strings.Contains(st, "A  staged.txt") {
		t.Fatalf("back status %q", st)
	}
}

func TestCodexRolloutTravelsUnderItsDate(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	src, dst := machine(t, root, "a"), machine(t, root, "b")
	project := filepath.Join(src.Home, "projects", "plain")
	os.MkdirAll(project, 0o755)
	os.MkdirAll(MapPath(project, src.Home, dst.Home), 0o755)
	rollout := filepath.Join(src.CodexHome, "sessions", "2026", "10", "06", "rollout-2026-10-06T10-00-00-sess-1.jsonl")
	write(t, rollout, `{"payload":{"id":"sess-1","cwd":"`+project+`"}}`+"\n")
	a := wire.Agent{ID: "L/xyz789", Kind: wire.KindCodex, Project: project, SessionID: "sess-1"}
	dir := filepath.Join(root, "b1")
	if _, err := Pack(context.Background(), a, "L", nil, dir, src); err != nil {
		t.Fatal(err)
	}
	m, placed, err := Unpack(context.Background(), dir, dst)
	if err != nil || !placed.Resume || m.Project.Bundle != "" {
		t.Fatalf("%+v %+v %v", m, placed, err)
	}
	got := CodexRollout(dst.CodexHome, "sess-1")
	data, _ := os.ReadFile(got)
	if !strings.HasSuffix(got, "2026/10/06/rollout-2026-10-06T10-00-00-sess-1.jsonl") || !strings.Contains(string(data), dst.Home) {
		t.Fatalf("%s: %s", got, data)
	}
}

func TestManifestRefusesBadPaths(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ManifestFile), `{"version":2,"id":"ho-0123456789ab","agent":{"localId":"abc","kind":"claude"},"project":{"path":"relative/x"}}`)
	if _, err := LoadManifest(dir); CodeOf(err) != "manifest" {
		t.Fatalf("relative path: %v", err)
	}
	write(t, filepath.Join(dir, ManifestFile), `{"version":2,"id":"ho-0123456789ab","agent":{"localId":"abc","kind":"codex","sessionId":"x","transcript":"../../etc/passwd.jsonl"},"project":{"path":"/tmp"}}`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := placeTranscript(filepath.Join(dir, ManifestFile), m.Agent, nil, "/tmp", Paths{CodexHome: dir}); CodeOf(err) != "manifest" {
		t.Fatalf("escaping transcript: %v", err)
	}
}
