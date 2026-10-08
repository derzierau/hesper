package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/internal/ptyhost"
	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Moves (agents.move, part R; move work): the registry's side. The
// controller (internal/remote) asks the source's plan (Plan: commits, the
// agent's processes) and the target's probe (ProbeMove: where the project
// is there, its commits, the tool), has the source pack (Pack: a fresh
// checkpoint as the handoff commit, at most MaxMoveBytes), carries the
// bundle over and imports it on the target (Import: the project found or
// cloned, a worktree on the branch with the uncommitted work, the
// conversation placed, the agent resumed, the handover note typed in);
// then it closes the source's agent (CloseAs, reason "moved") unless it
// forks.

// MaxMoveBytes caps a move's bundle (error "too-large").
var MaxMoveBytes int64 = 200 << 20

// HandoffPaths are this machine's homes for moves.
func (r *Registry) HandoffPaths() handoff.Paths {
	return handoff.Paths{Home: r.opt.Home, ClaudeHome: r.opt.ClaudeHome, CodexHome: r.opt.CodexHome, Env: r.env}
}

// Plan reports the commits a move's target may already have, and the
// processes the agent started (Processes).
func (r *Registry) Plan(ctx context.Context, id string) (handoff.Plan, error) {
	a, err := r.Get(id)
	if err != nil {
		return handoff.Plan{}, err
	}
	plan := handoff.PlanFor(ctx, a, r.HandoffPaths())
	if sp := r.scratch(); sp != nil && plan.Git {
		_, _, _, plan.Scratch = sp.ScratchOf(a.ProjectID)
	}
	if procs, err := r.Processes(id); err != nil {
		r.opt.Logf("hesperd: processes of %s: %v", id, err)
	} else {
		plan.Processes = procs
	}
	return plan, nil
}

// Probe answers a move's plan as its target.
func (r *Registry) Probe(ctx context.Context, path, home string, commits []string) handoff.Probe {
	return handoff.ProbeRepo(ctx, path, home, commits, r.HandoffPaths())
}

// ProbeMove is Probe for agents.move: the project where a move puts it
// here (projectID's folder on this machine, else path mapped from the
// source's home) and, with kind, whether the agent's tool is here.
func (r *Registry) ProbeMove(ctx context.Context, path, home string, commits []string, projectID, kind string) handoff.Probe {
	probe := handoff.ProbeAt(ctx, r.movePath(path, home, projectID), commits, r.HandoffPaths())
	if kind != "" {
		ok := r.HasTool(kind)
		probe.Tool = &ok
	}
	return probe
}

// movePath is where a moved agent's project is on this machine: the
// project's folder here (the shared projects), else the source's path
// mapped to this home.
func (r *Registry) movePath(path, sourceHome, projectID string) string {
	if projectID != "" && !strings.HasPrefix(projectID, "scratch:") {
		if po, ok := r.opt.Projects.(interface{ PathOn(string) string }); ok {
			if p := po.PathOn(projectID); p != "" && filepath.IsAbs(p) {
				if st, err := os.Stat(p); err == nil && st.IsDir() {
					return p
				}
			}
		}
	}
	return handoff.MapPath(path, sourceHome, r.opt.Home)
}

// HasTool reports whether the command of kind's profile is here (on the
// agents' PATH).
func (r *Registry) HasTool(kind string) bool {
	r.mu.Lock()
	profiles, defaults := r.profiles, r.settings.Defaults
	r.mu.Unlock()
	_, profile, err := resolveProfile(profiles, defaults, "", kind, "")
	if err != nil || len(profile.Argv) == 0 {
		return false
	}
	name := strings.ReplaceAll(profile.Argv[0], "{loginShell}", r.opt.LoginShell)
	if strings.Contains(name, "/") {
		st, err := os.Stat(name)
		return err == nil && !st.IsDir()
	}
	_, err = ptyhost.LookPath(name, r.env)
	return err == nil
}

// Pack writes an agent's bundle into dir: a fresh checkpoint of its
// folder (checkpoint.go) is the handoff commit. Over MaxMoveBytes it is
// "too-large".
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
	checkpoint := ""
	if checkpointable(snap.Kind) {
		cp, err := r.takeCheckpoint(snap)
		if err != nil {
			return nil, asMoveError(err)
		}
		if cp != nil {
			checkpoint, snap.Checkpoint = cp.Commit, cp
		}
	}
	m, err := handoff.PackCheckpoint(ctx, snap, r.machine, have, dir, r.HandoffPaths(), checkpoint)
	if err != nil {
		return nil, asMoveError(err)
	}
	if sp := r.scratch(); sp != nil && m.Project.Bundle != "" {
		// A scratch project: the target makes it from the bundle.
		if local, name, created, ok := sp.ScratchOf(snap.ProjectID); ok {
			m.Project.Scratch = &handoff.ScratchInfo{Local: local, Name: name, Created: created}
			if err := handoff.WriteManifest(dir, m); err != nil {
				return nil, err
			}
		}
	}
	if size := dirSize(dir); size > MaxMoveBytes {
		for _, name := range []string{handoff.ManifestFile, handoff.TranscriptFile, handoff.BundleFile} {
			os.Remove(filepath.Join(dir, name))
		}
		return nil, wire.Errorf(wire.CodeTooLarge, "%s's code and conversation are %d MB, over the %d MB a move carries", id, size>>20, MaxMoveBytes>>20)
	}
	return m, nil
}

func dirSize(dir string) int64 {
	var n int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
	}
	return n
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

// moveOptions (move work) are where an agents.move bundle lands: the
// project found here or cloned from its remote ("no-remote" without one),
// and a worktree for an agent that ran in its main checkout.
func (r *Registry) moveOptions(ctx context.Context, m *handoff.Manifest) (handoff.UnpackOptions, error) {
	var opt handoff.UnpackOptions
	if m.Move == nil || m.Project.Bundle == "" {
		return opt, nil
	}
	paths := r.HandoffPaths()
	path := r.movePath(m.Project.Path, m.Source.Home, m.Project.ProjectID)
	scratch := m.Project.Scratch != nil && r.scratch() != nil
	if scratch && !handoff.ProbeAt(ctx, path, nil, paths).Exists {
		// A scratch project: its folder made here from the full bundle,
		// in this Mac's scratch folder under the same name.
		opt.Project = r.scratchMovePath(path, m.Project.Path)
		return opt, nil
	}
	if !handoff.ProbeAt(ctx, path, nil, paths).Exists {
		if m.Project.Remote == "" {
			return opt, wire.Errorf(wire.CodeNoRemote, "%s is not on %s and has no Git remote to clone it from", filepath.Base(m.Project.Path), r.machine)
		}
		cloned, err := r.cloneForMove(ctx, m.Project.Remote, path)
		if err != nil {
			return opt, err
		}
		path = cloned
	}
	opt.Project = path
	if m.Project.Branch != "" && !scratch {
		opt.NewWorktree = filepath.Join(r.opt.WorktreeRoot, projectKey(path, r.opt.ProjectsRoot), Slugify(m.Project.Branch, 60))
	}
	return opt, nil
}

// scratchMovePath is where a moved scratch project's folder is made
// here: <scratch root>/<its folder's name> (-2, -3, … when taken by
// something else), else mapped (the path the move found).
func (r *Registry) scratchMovePath(mapped, source string) string {
	root := r.scratch().ScratchRoot()
	if root == "" {
		return mapped
	}
	base := filepath.Base(source)
	for i := 1; i < 1000; i++ {
		p := filepath.Join(root, base)
		if i > 1 {
			p = filepath.Join(root, fmt.Sprintf("%s-%d", base, i))
		}
		if entries, err := os.ReadDir(p); errors.Is(err, os.ErrNotExist) || err == nil && len(entries) == 0 {
			return p
		}
	}
	return mapped
}

// cloneForMove clones a moved agent's repository: into path when that is
// free (missing or an empty folder), else into the projects root
// (projects.clone).
func (r *Registry) cloneForMove(ctx context.Context, url, path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) > 0 {
		res, err := r.Clone(ctx, CloneParams{URL: url})
		return res.Path, err
	}
	if strings.HasPrefix(url, "-") || strings.ContainsAny(url, "\n\r\x00") {
		return "", wire.Errorf(wire.CodeInvalid, "bad Git remote %q", url)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, CloneTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "-q", "--", url, path)
	cmd.Env = append(append([]string(nil), r.env...), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		if entries == nil {
			os.RemoveAll(path)
		}
		return "", wire.Errorf(wire.CodeInvalid, "cannot clone %s: %s", url, orDefault(msg, err.Error()))
	}
	r.mu.Lock()
	r.touchProject(path, time.Now().UTC())
	r.mu.Unlock()
	return path, nil
}

// Import unpacks a moved agent's bundle (dir) and starts it here,
// resuming its conversation. It keeps the agent's local id when it is
// free here. An agents.move bundle (manifest "move") lands in the
// project's folder here (moveOptions), gets its folder trusted as a
// spawn's does, and the resumed agent gets the handover note.
func (r *Registry) Import(ctx context.Context, dir string) (wire.Agent, error) {
	m, err := handoff.LoadManifest(dir)
	if err != nil {
		return wire.Agent{}, asMoveError(err)
	}
	opt, err := r.moveOptions(ctx, m)
	if err != nil {
		return wire.Agent{}, err
	}
	m, placed, err := handoff.UnpackManifest(ctx, m, dir, r.HandoffPaths(), opt)
	if err != nil {
		return wire.Agent{}, asMoveError(err)
	}
	workdir := dirOf(placed.Project, placed.Worktree)
	if sc := m.Project.Scratch; sc != nil && m.Move != nil && r.scratch() != nil {
		// A scratch project's folder here; a move (not a fork) makes this
		// Mac its home.
		r.scratch().ScratchArrived(sc.Local, sc.Name, sc.Created, placed.Project, !m.Move.Fork)
	}
	projectID := r.projectOf(workdir) // projects step 1
	r.mu.Lock()
	settings := r.settings
	r.mu.Unlock()
	if m.Move != nil && settings.trustProjects() {
		if _, err := r.pretrust(m.Agent.Kind, workdir); err != nil {
			r.opt.Logf("hesperd: trusting %s for %s: %v (the agent will ask)", workdir, m.Agent.Kind, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "hesperd is stopping")
	}
	profileName, profile, err := resolveProfile(r.profiles, r.settings.Defaults, "", m.Agent.Kind, placed.Project)
	if pn, p, ok := lookupProfile(r.profiles, m.Agent.Profile); ok && p.Kind == m.Agent.Kind {
		profileName, profile, err = pn, p, nil
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
		Track: m.Agent.Track && m.Agent.Kind == wire.KindShell,
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
	if m.Move != nil && m.Move.Note && resume {
		go r.deliverNote(local, a.gen, handoverNote(m, placed, r.machine))
	}
	r.touchProject(placed.Project, now)
	r.changed(a)
	return a.Agent, nil
}

// handoverNote is the first message a moved agent gets.
func handoverNote(m *handoff.Manifest, placed handoff.Placed, here string) string {
	from, to := m.Move.From, m.Move.To
	if from == "" {
		from = m.Source.Machine
	}
	if to == "" {
		to = here
	}
	verb := "moved"
	if m.Move.Fork {
		verb = "forked"
	}
	note := fmt.Sprintf("You were %s from %s to %s.", verb, from, to)
	dir := dirOf(placed.Project, placed.Worktree)
	switch {
	case m.Project.Bundle != "" && placed.Branch != "":
		note += fmt.Sprintf(" Your worktree is now %s on branch %s, with the same uncommitted changes.", dir, placed.Branch)
	case m.Project.Bundle != "":
		note += fmt.Sprintf(" Your worktree is now %s, with the same uncommitted changes.", dir)
	default:
		note += fmt.Sprintf(" Your folder is now %s.", dir)
	}
	note += fmt.Sprintf(" Processes you started on %s did not move.", from)
	if m.Move.Fork {
		note += fmt.Sprintf(" The original agent keeps running on %s.", from)
	}
	return note
}

// noteTimeout bounds how long a handover note waits to be typed in.
var noteTimeout = 2 * time.Minute

// deliverNote types a note into a resumed agent once its screen shows no
// question and stayed the same for a poll (it has drawn its input).
func (r *Registry) deliverNote(local string, gen int, note string) {
	deadline := time.Now().Add(noteTimeout)
	last := ""
	for time.Now().Before(deadline) {
		time.Sleep(r.opt.DeliverPoll)
		r.mu.Lock()
		a := r.agents[local]
		if a == nil || a.gen != gen || a.term == nil || a.Exit != nil {
			r.mu.Unlock()
			return
		}
		term := a.term
		r.mu.Unlock()
		var text string
		term.WithScreen(func(s *vt.Screen) { text = strings.Join(screenRows(s, 1000), "\n") })
		if strings.TrimSpace(text) == "" || asking.MatchString(text) || text != last {
			last = text
			continue
		}
		typeText(term, note, true, true)
		return
	}
	r.opt.Logf("hesperd: the handover note did not reach %s/%s (its screen never settled)", r.machine, local)
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
