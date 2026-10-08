package handoff

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Checkpoints (move work): an agent's Git folder as a commit that is on
// no branch, refs/hesper/checkpoints/<local id> in its repository. It has
// the shape of a handoff commit (base <- staged <- every file, untracked
// included, ignored ones left out), so a move carries it as is, and it is
// built through a copy of the index: the user's index, branches, stash
// and files are never touched.

// CheckpointPrefix is where checkpoints live in a repository.
const CheckpointPrefix = "refs/hesper/checkpoints/"

// CheckpointInfo is a checkpoint taken (or found unchanged).
type CheckpointInfo struct {
	Ref, Commit, Branch string
	// Repo is the repository's main folder (pruning walks these).
	Repo    string
	Changed int
	At      time.Time
	// Unchanged: the folder was as the checkpoint already there.
	Unchanged bool
}

// TakeCheckpoint checkpoints dir for the agent local. It returns nil
// (and no error) when dir is not a Git repository with a commit.
func TakeCheckpoint(ctx context.Context, dir, local string, p Paths) (*CheckpointInfo, error) {
	if !localRE.MatchString(local) {
		return nil, Errorf("invalid", "bad agent id %q", local)
	}
	g := p.git(ctx)
	r := g.repoInfo(dir)
	if r == nil || r.base == "" {
		return nil, nil
	}
	ref := CheckpointPrefix + local
	indexTree, workTree, err := g.snapshotTrees(r.top)
	if err != nil {
		return nil, err
	}
	info := &CheckpointInfo{Ref: ref, Branch: r.branch, Repo: r.path}
	old := g.try(r.top, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if old != "" {
		got := strings.Split(g.try(r.top, "rev-parse", old+"^{tree}", old+"^1^{tree}", old+"^1^1"), "\n")
		if len(got) == 3 && got[0] == workTree && got[1] == indexTree && got[2] == r.base {
			info.Commit, info.Unchanged = old, true
			info.At = commitTime(g, r.top, old)
			info.Changed = changedFiles(g, r.top, r.base, old)
			return info, nil
		}
	}
	env := g.commitEnv(r.top)
	staged, err := g.runEnv(r.top, env, "commit-tree", "--no-gpg-sign", indexTree, "-p", r.base, "-m", "Hesper checkpoint "+local+": staged changes")
	if err != nil {
		return nil, err
	}
	commit, err := g.runEnv(r.top, env, "commit-tree", "--no-gpg-sign", workTree, "-p", staged, "-m", "Hesper checkpoint "+local)
	if err != nil {
		return nil, err
	}
	if old != "" {
		g.try(r.top, "update-ref", ref+"-prev", old)
	}
	if _, err := g.run(r.top, "update-ref", ref, commit); err != nil {
		return nil, err
	}
	info.Commit = commit
	info.At = commitTime(g, r.top, commit)
	info.Changed = changedFiles(g, r.top, r.base, commit)
	return info, nil
}

func commitTime(g git, dir, commit string) time.Time {
	sec, err := strconv.ParseInt(g.try(dir, "show", "-s", "--format=%ct", commit), 10, 64)
	if err != nil {
		return time.Now().UTC()
	}
	return time.Unix(sec, 0).UTC()
}

// changedFiles counts the files a checkpoint changes against its base.
func changedFiles(g git, dir, base, commit string) int {
	out := g.try(dir, "diff", "--no-renames", "--name-only", "-z", base, commit)
	n := 0
	for _, name := range strings.Split(out, "\x00") {
		if name != "" {
			n++
		}
	}
	return n
}

// PruneCheckpoints deletes the repository's checkpoints made before
// before; left is how many remain.
func PruneCheckpoints(ctx context.Context, repo string, before time.Time, p Paths) (left int, err error) {
	g := p.git(ctx)
	if !g.isRepoAt(repo) {
		return 0, nil
	}
	out, err := g.run(repo, "for-each-ref", "--format=%(refname) %(committerdate:unix)", CheckpointPrefix)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		ref, at, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.HasPrefix(ref, CheckpointPrefix) {
			continue
		}
		sec, _ := strconv.ParseInt(at, 10, 64)
		if time.Unix(sec, 0).Before(before) {
			if _, err := g.run(repo, "update-ref", "-d", ref); err != nil {
				return left, err
			}
			continue
		}
		left++
	}
	return left, nil
}

// IsCheckpoint reports whether commit is a checkpoint in the repository
// of dir: ref names it (or ref-prev does) and it has a checkpoint's shape.
func IsCheckpoint(ctx context.Context, dir, ref, commit string, p Paths) bool {
	g := p.git(ctx)
	if !strings.HasPrefix(ref, CheckpointPrefix) || !shaRE.MatchString(commit) {
		return false
	}
	full := g.try(dir, "rev-parse", "--verify", "-q", commit+"^{commit}")
	if full == "" || g.try(dir, "rev-parse", "--verify", "-q", full+"^1^1") == "" {
		return false
	}
	for _, r := range []string{ref, ref + "-prev"} {
		if g.try(dir, "rev-parse", "--verify", "-q", r+"^{commit}") == full {
			return true
		}
	}
	return false
}

// RestoreCheckpoint makes a worktree at worktree from a checkpoint: on
// branch when that is free and at the checkpoint's HEAD (or missing: then
// made there), else on a new branch "<branch>-restored[-N]" from it; the
// checkpoint's changes restored as uncommitted (staged ones staged). The
// repository's other checkouts are not touched. It returns the branch.
func RestoreCheckpoint(ctx context.Context, dir, commit, branch, worktree string, p Paths) (string, error) {
	g := p.git(ctx)
	r := g.repoInfo(dir)
	if r == nil {
		return "", Errorf("missing_project", "%s is not a Git repository", dir)
	}
	base := g.try(r.path, "rev-parse", "--verify", "-q", commit+"^1^1")
	if base == "" {
		return "", Errorf("invalid", "%.12s is not a checkpoint", commit)
	}
	if _, err := os.Stat(worktree); err == nil {
		return "", Errorf("path_conflict", "%s exists", worktree)
	}
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		return "", err
	}
	args := []string{"worktree", "add", "-q"}
	head := ""
	if branch != "" {
		head = g.try(r.path, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	}
	switch {
	case branch != "" && head == "":
		args = append(args, "-b", branch, worktree, base)
	case branch != "" && head == base && g.checkedOut(r.path, branch) == "":
		args = append(args, worktree, branch)
	default:
		stem := branch
		if stem == "" {
			stem = "checkpoint"
		}
		name := stem + "-restored"
		for n := 2; g.ok(r.path, "rev-parse", "--verify", "-q", "refs/heads/"+name); n++ {
			name = stem + "-restored-" + strconv.Itoa(n)
		}
		branch = name
		args = append(args, "-b", branch, worktree, base)
	}
	if _, err := g.run(r.path, args...); err != nil {
		return "", err
	}
	if err := g.restoreChanges(worktree, base, commit); err != nil {
		return "", err
	}
	return branch, nil
}

// usableCheckpoint is commit when it is a checkpoint of base (base <-
// staged <- files) the bundle can carry as the handoff commit.
func (g git) usableCheckpoint(top, base, commit string) bool {
	if commit == "" || !shaRE.MatchString(commit) {
		return false
	}
	return g.try(top, "rev-parse", "--verify", "-q", commit+"^1^1") == base
}
