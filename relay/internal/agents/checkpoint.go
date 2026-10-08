package agents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Checkpoints (move work, docs/rebuild-contract.md "As built — move
// work"): a Claude or Codex agent's Git folder is checkpointed
// (internal/handoff) when its turn ends (at most every CheckpointEvery),
// when it is closed, when it moves and on agents.checkpoint. Agent.
// Checkpoint shows the last one; the shared history keeps it with the
// session (OnCheckpoint). Checkpoints older than CheckpointKeep are
// pruned when hesperd starts and daily; checkpoints.json in the state
// directory lists the repositories that have some.

var (
	// CheckpointEvery debounces the checkpoints taken at turn ends.
	CheckpointEvery = 60 * time.Second
	// CheckpointKeep is how long checkpoints are kept.
	CheckpointKeep = 14 * 24 * time.Hour
	// checkpointTimeout bounds one checkpoint.
	checkpointTimeout = 2 * time.Minute
)

// checkpointable: the kinds whose folders are checkpointed (a shell may
// sit in any folder, the home included).
func checkpointable(kind string) bool {
	return kind == wire.KindClaude || kind == wire.KindCodex
}

// OnCheckpoint registers fn, called with the agent (its Checkpoint set)
// after every checkpoint taken, also of agents already closed.
func (r *Registry) OnCheckpoint(fn func(a wire.Agent)) {
	r.cpMu.Lock()
	r.cpHooks = append(r.cpHooks, fn)
	r.cpMu.Unlock()
}

// Checkpoint checkpoints an agent's folder now (agents.checkpoint): nil
// when it is not a Git repository with a commit.
func (r *Registry) Checkpoint(id string) (*wire.Checkpoint, error) {
	r.mu.Lock()
	a, err := r.find(id)
	var snap wire.Agent
	if err == nil {
		snap = a.Agent
	}
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !checkpointable(snap.Kind) {
		return nil, wire.Errorf(wire.CodeInvalid, "%s is a shell: shells are not checkpointed", id)
	}
	cp, err := r.takeCheckpoint(snap)
	if err != nil {
		return nil, asMoveError(err)
	}
	return cp, nil
}

// takeCheckpoint checkpoints snap's folder (without the lock), keeps it
// on the agent when it is still there, and tells the hooks.
func (r *Registry) takeCheckpoint(snap wire.Agent) (*wire.Checkpoint, error) {
	_, local := r.split(snap.ID)
	ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
	defer cancel()
	r.cpRun.Lock() // one at a time: they are cheap, and never race on a ref
	info, err := handoff.TakeCheckpoint(ctx, snap.Dir(), local, r.HandoffPaths())
	r.cpRun.Unlock()
	if err != nil {
		return nil, err
	}
	var cp *wire.Checkpoint
	if info != nil {
		cp = &wire.Checkpoint{Ref: info.Ref, Commit: info.Commit, At: info.At, Changed: info.Changed, Branch: info.Branch}
		if !info.Unchanged {
			r.noteCheckpointRepo(info.Repo)
		}
	}
	r.mu.Lock()
	if a := r.agents[local]; a != nil && a.ID == snap.ID && a.Created.Equal(snap.Created) && !sameCheckpoint(a.Checkpoint, cp) {
		a.Checkpoint = cp
		r.changed(a)
	}
	r.mu.Unlock()
	if cp != nil {
		snap.Checkpoint = cp
		r.cpMu.Lock()
		hooks := append([]func(wire.Agent){}, r.cpHooks...)
		r.cpMu.Unlock()
		for _, fn := range hooks {
			fn(snap)
		}
	}
	return cp, nil
}

func sameCheckpoint(a, b *wire.Checkpoint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// turnCheckpoint schedules the checkpoint of an agent whose turn ended
// (the lock is held): at once, or once CheckpointEvery passed since the
// last one.
func (r *Registry) turnCheckpoint(a *agent) {
	if !checkpointable(a.Kind) || a.cpPending || r.closing {
		return
	}
	a.cpPending = true
	wait := max(CheckpointEvery-time.Since(a.cpLast), 0)
	local, created := a.local, a.Created
	time.AfterFunc(wait, func() {
		r.mu.Lock()
		a := r.agents[local]
		if a == nil || !a.Created.Equal(created) || r.closing {
			r.mu.Unlock()
			return
		}
		a.cpPending, a.cpLast = false, time.Now()
		snap := a.Agent
		r.mu.Unlock()
		if _, err := r.takeCheckpoint(snap); err != nil {
			r.opt.Logf("hesperd: checkpoint of %s: %v", snap.ID, err)
		}
	})
}

// closeCheckpoint checkpoints an agent being closed (without the lock).
func (r *Registry) closeCheckpoint(snap wire.Agent) {
	if !checkpointable(snap.Kind) {
		return
	}
	if _, err := r.takeCheckpoint(snap); err != nil {
		r.opt.Logf("hesperd: checkpoint of %s at its close: %v", snap.ID, err)
	}
}

// RestoreCheckpoint (checkpoints.restore) makes a new worktree from the
// checkpoint commit (ref names it) of the repository of dir: on its
// branch when free, else a new "<branch>-restored" one, the uncommitted
// changes restored. It returns the worktree and its branch.
func (r *Registry) RestoreCheckpoint(ctx context.Context, dir, ref, commit, branch string) (string, string, error) {
	paths := r.HandoffPaths()
	if !handoff.IsCheckpoint(ctx, dir, ref, commit, paths) {
		return "", "", wire.Errorf(wire.CodeNotFound, "no checkpoint %.12s at %s in %s (pruned after %d days?)", commit, ref, dir, int(CheckpointKeep.Hours()/24))
	}
	root := r.g.mainRoot(dir)
	stem := Slugify(branch, 60)
	if branch == "" {
		stem = "checkpoint"
	}
	worktree := filepath.Join(r.opt.WorktreeRoot, projectKey(root, r.opt.ProjectsRoot), stem+"-restored")
	for n := 2; exists(worktree); n++ {
		worktree = filepath.Join(r.opt.WorktreeRoot, projectKey(root, r.opt.ProjectsRoot), stem+"-restored-"+strconv.Itoa(n))
	}
	r.cpRun.Lock()
	used, err := handoff.RestoreCheckpoint(ctx, dir, commit, branch, worktree, paths)
	r.cpRun.Unlock()
	if err != nil {
		return "", "", asMoveError(err)
	}
	r.mu.Lock()
	r.touchProject(root, time.Now().UTC())
	r.mu.Unlock()
	return worktree, used, nil
}

// checkpointsFile lists the repositories with checkpoints (pruning).
type checkpointsFile struct {
	Repos map[string]time.Time `json:"repos"`
}

func (r *Registry) checkpointsPath() string { return filepath.Join(r.opt.StateDir, "checkpoints.json") }

func (r *Registry) loadCheckpointRepos() checkpointsFile {
	var f checkpointsFile
	if data, err := os.ReadFile(r.checkpointsPath()); err == nil {
		json.Unmarshal(data, &f)
	}
	if f.Repos == nil {
		f.Repos = map[string]time.Time{}
	}
	return f
}

func (r *Registry) saveCheckpointRepos(f checkpointsFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(r.checkpointsPath(), append(data, '\n'), 0o600)
}

// noteCheckpointRepo records a repository that has checkpoints.
func (r *Registry) noteCheckpointRepo(repo string) {
	if repo == "" {
		return
	}
	r.cpFile.Lock()
	defer r.cpFile.Unlock()
	f := r.loadCheckpointRepos()
	f.Repos[repo] = time.Now().UTC()
	if err := r.saveCheckpointRepos(f); err != nil {
		r.opt.Logf("hesperd: %s: %v", r.checkpointsPath(), err)
	}
}

// PruneCheckpoints deletes checkpoints older than CheckpointKeep in every
// repository that has some; a repository without any leaves the list.
func (r *Registry) PruneCheckpoints() {
	r.cpFile.Lock()
	defer r.cpFile.Unlock()
	f := r.loadCheckpointRepos()
	if len(f.Repos) == 0 {
		return
	}
	repos := make([]string, 0, len(f.Repos))
	for repo := range f.Repos {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	before := time.Now().Add(-CheckpointKeep)
	for _, repo := range repos {
		ctx, cancel := context.WithTimeout(context.Background(), checkpointTimeout)
		left, err := handoff.PruneCheckpoints(ctx, repo, before, r.HandoffPaths())
		cancel()
		if err != nil {
			r.opt.Logf("hesperd: pruning checkpoints in %s: %v", repo, err)
			continue
		}
		if left == 0 {
			delete(f.Repos, repo)
		}
	}
	if err := r.saveCheckpointRepos(f); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.opt.Logf("hesperd: %s: %v", r.checkpointsPath(), err)
	}
}

// pruneLoop prunes at the start and daily until the registry closes.
func (r *Registry) pruneLoop() {
	r.PruneCheckpoints()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-r.quit:
			return
		case <-t.C:
			r.PruneCheckpoints()
		}
	}
}
