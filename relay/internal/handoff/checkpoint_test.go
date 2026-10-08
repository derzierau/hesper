package handoff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func agentIn(dir string) wire.Agent {
	return wire.Agent{ID: "L/abc123", Kind: wire.KindClaude, Name: "x", Project: dir, Branch: "main", Created: time.Now()}
}

// userState is everything of a checkout a checkpoint must never touch:
// the index file, branches, stash, HEAD and the files (git status).
func userState(t *testing.T, dir string) string {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(run(t, dir, "git", "rev-parse", "--path-format=absolute", "--git-path", "index")))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(index)
	return strings.Join([]string{hex.EncodeToString(sum[:]),
		run(t, dir, "git", "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/stash"),
		run(t, dir, "git", "rev-parse", "HEAD"), run(t, dir, "git", "symbolic-ref", "-q", "HEAD"),
		run(t, dir, "git", "status", "--porcelain=v2", "--untracked-files=all", "--ignored"),
		run(t, dir, "git", "stash", "list")}, "\n")
}

func checkpointRepo(t *testing.T) (string, Paths) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	p := machine(t, root, "laptop")
	dir := filepath.Join(p.Home, "projects", "app")
	os.MkdirAll(dir, 0o755)
	run(t, dir, "git", "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, ".gitignore"), ".env\n")
	write(t, filepath.Join(dir, "a.txt"), "one\n")
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-q", "-m", "first")
	write(t, filepath.Join(dir, "s.txt"), "stashed\n")
	run(t, dir, "git", "stash", "push", "-q", "-u", "-m", "user stash")
	write(t, filepath.Join(dir, "staged.txt"), "staged\n")
	run(t, dir, "git", "add", "staged.txt")
	write(t, filepath.Join(dir, "a.txt"), "one\ntwo\n")
	write(t, filepath.Join(dir, "new.txt"), "untracked\n")
	write(t, filepath.Join(dir, ".env"), "SECRET=1\n")
	return dir, p
}

// A checkpoint has tracked, staged and untracked files, never ignored
// ones, and leaves the index, branches, stash and files as they were;
// unchanged it is not taken again; a change makes a new one (the
// previous kept as -prev); pruning drops old ones; a restore makes a
// worktree with the same uncommitted work.
func TestCheckpointCapturesAndNeverTouches(t *testing.T) {
	dir, p := checkpointRepo(t)
	ctx := context.Background()
	before := userState(t, dir)
	info, err := TakeCheckpoint(ctx, dir, "abc123", p)
	if err != nil || info == nil {
		t.Fatalf("checkpoint %+v %v", info, err)
	}
	if after := userState(t, dir); after != before {
		t.Fatalf("the checkout changed:\n%s\n---\n%s", before, after)
	}
	if info.Ref != "refs/hesper/checkpoints/abc123" || info.Branch != "main" || info.Changed != 3 || info.Unchanged || info.At.IsZero() {
		t.Fatalf("info %+v", info)
	}
	if got := run(t, dir, "git", "rev-parse", info.Ref); got != info.Commit {
		t.Fatalf("ref %s, commit %s", got, info.Commit)
	}
	files := run(t, dir, "git", "ls-tree", "-r", "--name-only", info.Commit)
	for _, want := range []string{"a.txt", "staged.txt", "new.txt", ".gitignore"} {
		if !strings.Contains(files, want) {
			t.Fatalf("%s missing from %s", want, files)
		}
	}
	if strings.Contains(files, ".env") || strings.Contains(files, "s.txt") {
		t.Fatalf("ignored or stashed files in the checkpoint: %s", files)
	}
	if got := run(t, dir, "git", "show", info.Commit+":a.txt"); got != "one\ntwo" {
		t.Fatalf("a.txt %q", got)
	}
	if staged := run(t, dir, "git", "ls-tree", "-r", "--name-only", info.Commit+"^1"); !strings.Contains(staged, "staged.txt") || strings.Contains(staged, "new.txt") {
		t.Fatalf("staged part %s", staged)
	}

	again, err := TakeCheckpoint(ctx, dir, "abc123", p)
	if err != nil || !again.Unchanged || again.Commit != info.Commit {
		t.Fatalf("unchanged: %+v %v", again, err)
	}
	if run(t, dir, "git", "for-each-ref", "--format=%(refname)", CheckpointPrefix) != info.Ref {
		t.Fatal("a second ref for an unchanged folder")
	}

	write(t, filepath.Join(dir, "new.txt"), "untracked, changed\n")
	next, err := TakeCheckpoint(ctx, dir, "abc123", p)
	if err != nil || next.Unchanged || next.Commit == info.Commit {
		t.Fatalf("changed: %+v %v", next, err)
	}
	if run(t, dir, "git", "rev-parse", info.Ref+"-prev") != info.Commit {
		t.Fatal("no -prev")
	}
	if !IsCheckpoint(ctx, dir, info.Ref, info.Commit, p) || !IsCheckpoint(ctx, dir, info.Ref, next.Commit, p) || IsCheckpoint(ctx, dir, info.Ref, run(t, dir, "git", "rev-parse", "HEAD"), p) {
		t.Fatal("IsCheckpoint")
	}

	// Restore: a worktree on a new branch (main is checked out), the
	// uncommitted work as it was.
	wt := filepath.Join(p.Home, "worktrees", "app", "main-restored")
	branch, err := RestoreCheckpoint(ctx, dir, next.Commit, "main", wt, p)
	if err != nil || branch != "main-restored" {
		t.Fatalf("restore %q %v", branch, err)
	}
	if got, _ := os.ReadFile(filepath.Join(wt, "new.txt")); string(got) != "untracked, changed\n" {
		t.Fatalf("restored new.txt %q", got)
	}
	if st := run(t, wt, "git", "status", "--porcelain"); !strings.Contains(st, "A  staged.txt") || !strings.Contains(st, "M a.txt") || !strings.Contains(st, "?? new.txt") {
		t.Fatalf("restored status:\n%s", st)
	}
	if _, err := os.Stat(filepath.Join(wt, ".env")); err == nil {
		t.Fatal("an ignored file was restored")
	}
	if after := userState(t, dir); !strings.Contains(after, "refs/heads/main-restored") {
		t.Fatalf("no restored branch: %s", after)
	}

	// Prune: nothing is old yet; everything before the future goes.
	if left, err := PruneCheckpoints(ctx, dir, time.Now().Add(-time.Hour), p); err != nil || left != 2 {
		t.Fatalf("prune now: %d %v", left, err)
	}
	if left, err := PruneCheckpoints(ctx, dir, time.Now().Add(time.Hour), p); err != nil || left != 0 {
		t.Fatalf("prune all: %d %v", left, err)
	}
	if refs := run(t, dir, "git", "for-each-ref", CheckpointPrefix); refs != "" {
		t.Fatalf("left: %s", refs)
	}
}

// A folder outside Git has no checkpoint; a move packs the checkpoint as
// its handoff commit.
func TestCheckpointOutsideGitAndAsHandoff(t *testing.T) {
	dir, p := checkpointRepo(t)
	ctx := context.Background()
	plain := filepath.Join(p.Home, "plain")
	os.MkdirAll(plain, 0o755)
	if info, err := TakeCheckpoint(ctx, plain, "abc123", p); info != nil || err != nil {
		t.Fatalf("outside Git: %+v %v", info, err)
	}
	info, err := TakeCheckpoint(ctx, dir, "abc123", p)
	if err != nil {
		t.Fatal(err)
	}
	agent := agentIn(dir)
	m, err := PackCheckpoint(ctx, agent, "L", nil, filepath.Join(p.Home, "b"), p, info.Commit)
	if err != nil || m.Project.Handoff != info.Commit {
		t.Fatalf("pack %+v %v", m, err)
	}
}
