package agents

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Moves (agents.move, part R): the registry's side. The controller that
// moves an agent stops it on the source, packs it there (Pack), carries
// the bundle over and imports it on the target (Import), which resumes
// it; then it removes the source's.

// HandoffPaths are this machine's homes for moves.
func (r *Registry) HandoffPaths() handoff.Paths {
	return handoff.Paths{Home: r.opt.Home, ClaudeHome: r.opt.ClaudeHome, CodexHome: r.opt.CodexHome, Env: r.env}
}

// Plan reports the commits a move's target may already have.
func (r *Registry) Plan(ctx context.Context, id string) (handoff.Plan, error) {
	a, err := r.Get(id)
	if err != nil {
		return handoff.Plan{}, err
	}
	return handoff.PlanFor(ctx, a, r.HandoffPaths()), nil
}

// Probe answers a move's plan as its target.
func (r *Registry) Probe(ctx context.Context, path, home string, commits []string) handoff.Probe {
	return handoff.ProbeRepo(ctx, path, home, commits, r.HandoffPaths())
}

// Pack writes an agent's bundle into dir. The agent should have exited
// (StopWait), so its conversation is complete.
func (r *Registry) Pack(ctx context.Context, id string, have []string, dir string) (*handoff.Manifest, error) {
	r.mu.Lock()
	a, err := r.find(id)
	if err == nil {
		r.adoptCodexSession(a)
	}
	var snap wire.Agent
	if err == nil {
		snap = a.Agent
	}
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	m, err := handoff.Pack(ctx, snap, r.machine, have, dir, r.HandoffPaths())
	if err != nil {
		return nil, asMoveError(err)
	}
	return m, nil
}

func asMoveError(err error) error {
	if code := handoff.CodeOf(err); code != "" {
		switch code {
		case "manifest", "invalid":
			return wire.Errorf(wire.CodeInvalid, "%v", err)
		case "missing_project":
			return wire.Errorf(wire.CodeNotFound, "%v", err)
		default:
			return wire.Errorf(wire.CodeExists, "%v", err)
		}
	}
	return err
}

// StopWait stops an agent and waits until its process ended (at most
// timeout).
func (r *Registry) StopWait(id string, timeout time.Duration) error {
	r.mu.Lock()
	a, err := r.find(id)
	var term interface{ Done() <-chan struct{} }
	if err == nil && a.term != nil && a.Exit == nil {
		term = a.term
	}
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if term == nil {
		return nil
	}
	if err := r.Stop(id); err != nil {
		return err
	}
	select {
	case <-term.Done():
	case <-time.After(timeout):
		return wire.Errorf(wire.CodeUnavailable, "%s did not stop", id)
	}
	// The exit callback runs right after Done.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, err := r.Get(id); err != nil || got.Exit != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

// Import unpacks a moved agent's bundle (dir) and starts it here,
// resuming its conversation. It keeps the agent's local id when it is
// free here.
func (r *Registry) Import(ctx context.Context, dir string) (wire.Agent, error) {
	m, placed, err := handoff.Unpack(ctx, dir, r.HandoffPaths())
	if err != nil {
		return wire.Agent{}, asMoveError(err)
	}
	projectID := r.projectOf(dirOf(placed.Project, placed.Worktree)) // projects step 1
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "hesperd is stopping")
	}
	profileName, profile, err := resolveProfile(r.profiles, r.settings.Defaults, "", m.Agent.Kind, placed.Project)
	if p, ok := r.profiles[m.Agent.Profile]; ok && p.Kind == m.Agent.Kind {
		profileName, profile, err = m.Agent.Profile, p, nil
	}
	if err != nil {
		return wire.Agent{}, err
	}
	local := m.Agent.LocalID
	for local == "" || r.agents[local] != nil {
		local = newLocalID()
	}
	now := time.Now().UTC()
	created := m.Agent.Created
	if created.IsZero() {
		created = now
	}
	a := &agent{local: local, Agent: wire.Agent{
		ID: r.id(local), Machine: r.machine, Kind: m.Agent.Kind, Profile: profileName, Name: m.Agent.Name, Task: m.Agent.Task,
		Project: placed.Project, ProjectID: projectID, Worktree: placed.Worktree, Branch: placed.Branch, State: wire.StateStarting, StateSince: now,
		Created: created, Size: r.defaultSize(),
		Parent: m.Agent.Parent, Depth: m.Agent.Depth, LetParentAnswer: m.Agent.LetParentAnswer && m.Agent.Parent != "", // agent tree
	}}
	// A moved agent took its task where it ran: it is never sent again.
	a.engaged = true
	resume := placed.Resume
	if resume {
		a.SessionID = m.Agent.SessionID
	}
	r.agents[local] = a
	// Without its conversation the agent starts fresh, without repeating
	// the task (it is shown, not sent).
	task := a.Task
	if !resume {
		a.Task = ""
		if a.Kind == wire.KindClaude {
			a.SessionID = newUUID()
		}
	}
	err = r.start(a, profile, resume)
	a.Task = task
	if err != nil {
		delete(r.agents, local)
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "cannot start %s: %v", profile.Argv[0], err)
	}
	r.touchProject(placed.Project, now)
	r.changed(a)
	return a.Agent, nil
}

// SpawnKind is the kind of agent a spawn would start (its resolved
// profile's), so a host can tell a shell before it starts.
func (r *Registry) SpawnKind(p wire.SpawnParams) (string, error) {
	r.mu.Lock()
	profiles, defaults := r.profiles, r.settings.Defaults
	r.mu.Unlock()
	_, profile, err := resolveProfile(profiles, defaults, p.Profile, p.Kind, cleanPath(p.Project))
	if err != nil {
		return "", err
	}
	return profile.Kind, nil
}

func (r *Registry) defaultSize() wire.Size {
	if s := r.settings.Size; s != nil && s.Cols > 0 && s.Rows > 0 {
		return *s
	}
	return wire.Size{Cols: 120, Rows: 40}
}

func defaultHomes(o *Options) {
	home, _ := os.UserHomeDir()
	if o.Home == "" {
		o.Home = home
	}
	if real, err := filepath.EvalSymlinks(o.Home); err == nil {
		o.Home = real
	}
	if o.ClaudeHome == "" {
		if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
			o.ClaudeHome = d
		} else {
			o.ClaudeHome = filepath.Join(home, ".claude")
		}
	}
}
