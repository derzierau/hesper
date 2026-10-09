// Package agents is hesperd's registry: the agents of this Mac in their
// PTYs (internal/ptyhost), their state from hooks and process events,
// launch profiles, spawning (task as first prompt, worktrees), answers to
// approvals, persistence and respawn with resume, and the local socket
// server (server.go) that speaks pkg/wire.
package agents

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/ptyhost"
	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Options configure a Registry. Zero values take the defaults.
type Options struct {
	StateDir  string // agents.json, projects.json, the socket; wire.StateDir()
	ConfigDir string // profiles.json, settings.json, machines.json; wire.ConfigDir()
	Socket    string // HESPER_SOCKET given to agents; StateDir/hesperd.sock
	Machine   string // this Mac's short name; settings, HESPER_MACHINE, machines.json, host name
	// Env is the agents' base environment; nil resolves the login
	// environment once (LoginEnv).
	Env        []string
	LoginShell string
	// LoginTimeout bounds each of the two tries of the login shell (10 s).
	LoginTimeout time.Duration
	// CommandWait is how long a start waits for an agent's command that
	// is not on its PATH right now to appear (15 s): Claude Code's npm
	// auto-updater reinstalls the package, and for a few seconds there is
	// no claude.
	CommandWait time.Duration
	// WorktreeRoot ($HESPER_WORKTREE_ROOT, ~/worktrees) holds new
	// worktrees, by project key below ProjectsRoot ($HESPER_PROJECT_ROOT,
	// ~/projects).
	WorktreeRoot, ProjectsRoot string
	// ClaudeConfig is Claude Code's ~/.claude.json (trust, read only);
	// CodexHome is $CODEX_HOME (~/.codex; rollouts, read only).
	ClaudeConfig, CodexHome string
	// Home (the user's home: moved agents' paths map to it) and ClaudeHome
	// (Claude Code's data, ~/.claude or $CLAUDE_CONFIG_DIR: session files
	// travel with a move).
	Home, ClaudeHome string
	// StopGrace is how long agents.stop waits after SIGHUP before SIGKILL.
	StopGrace time.Duration
	// CloseWait is how long agents.close waits after the tool's interrupt
	// for the agent to leave working before it ends it (5 s); KillGrace
	// how long agents.kill waits after SIGTERM before SIGKILL (3 s).
	CloseWait, KillGrace time.Duration
	// DeliverPoll is how often a task waiting to be typed in (Claude's
	// trust question) looks at the screen; 500 ms.
	DeliverPoll time.Duration
	// HookBin is the hesperd that Codex agents' -c hooks call
	// (codexargs.go); the running executable by default.
	HookBin string
	// Remote reaches other machines' daemons (part R); nil: none.
	Remote Remote
	Logf   func(format string, args ...any)

	// Projects is the project registry (projecthook.go; projects step 1);
	// nil: none.
	Projects Projects
	// Sessions (optional; shared history) serves sessions.* and their
	// notifications (sessionhook.go).
	Sessions Sessions
}

func (o *Options) defaults() {
	home, _ := os.UserHomeDir()
	if o.StateDir == "" {
		o.StateDir = wire.StateDir()
	}
	if o.ConfigDir == "" {
		o.ConfigDir = wire.ConfigDir()
	}
	if o.Socket == "" {
		o.Socket = filepath.Join(o.StateDir, "hesperd.sock")
	}
	if o.LoginShell == "" {
		o.LoginShell = LoginShell()
	}
	if o.WorktreeRoot == "" {
		o.WorktreeRoot = os.Getenv("HESPER_WORKTREE_ROOT")
		if o.WorktreeRoot == "" {
			o.WorktreeRoot = filepath.Join(home, "worktrees")
		}
	}
	if o.ProjectsRoot == "" {
		o.ProjectsRoot = os.Getenv("HESPER_PROJECT_ROOT")
		if o.ProjectsRoot == "" {
			o.ProjectsRoot = filepath.Join(home, "projects")
		}
	}
	if o.ClaudeConfig == "" {
		if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
			o.ClaudeConfig = filepath.Join(d, ".claude.json")
		} else {
			o.ClaudeConfig = filepath.Join(home, ".claude.json")
		}
	}
	if o.CodexHome == "" {
		o.CodexHome = os.Getenv("CODEX_HOME")
		if o.CodexHome == "" {
			o.CodexHome = filepath.Join(home, ".codex")
		}
	}
	defaultHomes(o)
	if o.HookBin == "" {
		if self, err := os.Executable(); err == nil {
			if real, err := filepath.EvalSymlinks(self); err == nil {
				self = real
			}
			o.HookBin = self
		}
	}
	if o.DeliverPoll <= 0 {
		o.DeliverPoll = 500 * time.Millisecond
	}
	if o.StopGrace <= 0 {
		o.StopGrace = 5 * time.Second
	}
	if o.CloseWait <= 0 {
		o.CloseWait = 5 * time.Second
	}
	if o.KillGrace <= 0 {
		o.KillGrace = 3 * time.Second
	}
	if o.LoginTimeout <= 0 {
		o.LoginTimeout = 10 * time.Second
	}
	if o.CommandWait <= 0 {
		o.CommandWait = 15 * time.Second
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
}

// Registry holds this Mac's agents.
type Registry struct {
	opt     Options
	machine string
	env     []string
	g       git

	mu       sync.Mutex
	agents   map[string]*agent // by local id
	subs     map[*Subscriber]struct{}
	profiles map[string]wire.Profile
	settings Settings

	files     *FileStore // files.go
	filesOnce sync.Once
	projects  map[string]wire.Project
	closing   bool
	saveCh    chan struct{}
	saverEnd  chan struct{}
	quit      chan struct{} // closed by Close (pruneLoop)

	// checkpoints (checkpoint.go): hooks, one checkpoint at a time, the
	// list of repositories with checkpoints.
	cpMu    sync.Mutex
	cpHooks []func(wire.Agent)
	cpRun   sync.Mutex
	cpFile  sync.Mutex

	// review (review.go): the agents' review logs (reviewlog.go); one
	// accept, reject or send-back at a time.
	reviewOnce sync.Once
	reviewLogs *reviewStore
	reviewRun  sync.Mutex
}

type agent struct {
	wire.Agent
	local string
	term  *ptyhost.Term
	gen   int // spawn generation: callbacks of an older process are stale
	// running: the process runs, or ran when the daemon stopped (respawn).
	running  bool
	stopping bool
	started  time.Time
	// expectPrompt: the first prompt is on its way (stay starting).
	expectPrompt bool
	pendingTask  string // typed in once Claude is past its trust question
	approvalKey  string // the tool call an approval asks about
	fromScreen   bool   // the approval came from the Codex screen fallback
	endedTurn    string // Codex: the last turn that ended
	codexHooks   bool   // Codex: a PermissionRequest hook arrived
	watch        *codexWatch
	// screenAsk: the first-run screen (firstrun.go) shown as attention.
	screenAsk string
	// skippedAt: when Codex's update screen was last skipped by setting.
	skippedAt time.Time
	// sessionSeen: a hook showed the session was saved (resumable).
	sessionSeen bool
	// engaged: the agent got past starting (a prompt, a tool, a turn's
	// end, idle): it has taken its task, so a fresh start never sends the
	// task again (comeBack).
	engaged bool
	// respawn: the daemon's stop ended it and it has not come back yet
	// (its respawn failed): the next daemon start tries again.
	respawn bool
	// forkFrom (shared history): this start forks that session.
	forkFrom string
	// cameBack: this process is a respawn or resume (comeBack); answered:
	// a first-run screen was answered (an update may end it).
	cameBack, answered bool
	// closeReason (closing agents, close.go): the agent is being closed;
	// once its process ends it leaves the registry with this reason.
	closeReason string
	// lastMessage (agent tree): the final message of its last turn
	// (Stop / notify hooks), for agents.result.
	lastMessage   string
	lastMessageAt time.Time
	// shell (shell.go): the shell process's watch; shellTask the command
	// to type once its prompt is ready (spawns only, never persisted).
	shell     *shellWatch
	shellTask string
	// checkpoint.go: a turn's checkpoint is scheduled; the last one ran.
	cpPending bool
	cpLast    time.Time
	// closeTo (move work): the agent a moved one became (reason "moved").
	closeTo string
}

// Open loads the registry from the state directory and respawns the agents
// that were running (with resume).
func Open(opt Options) (*Registry, error) {
	opt.defaults()
	if err := os.MkdirAll(opt.StateDir, 0o700); err != nil {
		return nil, err
	}
	r := &Registry{opt: opt, agents: map[string]*agent{}, subs: map[*Subscriber]struct{}{}, projects: map[string]wire.Project{},
		saveCh: make(chan struct{}, 1), saverEnd: make(chan struct{}), quit: make(chan struct{})}
	var err error
	if r.settings, err = loadSettings(opt.ConfigDir); err != nil {
		opt.Logf("hesperd: %v (defaults used)", err)
	}
	if r.profiles, err = loadProfiles(opt.ConfigDir); err != nil {
		opt.Logf("hesperd: %v", err)
	}
	r.machine = opt.Machine
	if r.machine == "" {
		r.machine = machineShort(r.settings, opt.StateDir, opt.ConfigDir)
	}
	source := "the given environment"
	r.env = opt.Env
	if r.env == nil {
		r.env, source = ResolveEnv(opt.LoginShell, opt.LoginTimeout, 2)
	} else {
		r.env = completePath(r.env, os.Getenv("PATH"))
	}
	opt.Logf("hesperd: agents' environment from %s; PATH=%s", source, envValue(r.env, "PATH"))
	r.g = git{env: r.env}
	r.loadProjects()
	records, err := r.loadAgents()
	if err != nil {
		return nil, err
	}
	go r.saver()
	for _, rec := range records {
		r.restore(rec)
	}
	go r.pruneLoop() // checkpoint.go
	return r, nil
}

// Machine is this Mac's short name.
func (r *Registry) Machine() string { return r.machine }

// ID is an agent's full id.
func (r *Registry) id(local string) string { return r.machine + "/" + local }

// split parses an agent id: "<machine>/<local>" or a bare local id.
func (r *Registry) split(id string) (machine, local string) {
	if m, l, ok := strings.Cut(id, "/"); ok {
		return m, l
	}
	return r.machine, id
}

// IsRemote reports whether id names an agent of another machine.
func (r *Registry) IsRemote(id string) bool {
	m, _ := r.split(id)
	return m != r.machine
}

func (r *Registry) find(id string) (*agent, error) {
	m, local := r.split(id)
	if m != r.machine {
		return nil, wire.Errorf(wire.CodeUnavailable, "%s is on another machine (%s): remote agents are not connected yet", id, m)
	}
	a := r.agents[local]
	if a == nil {
		return nil, wire.Errorf(wire.CodeNotFound, "no agent %s", id)
	}
	return a, nil
}

func newLocalID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b [6]byte
	rand.Read(b[:])
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:])
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// List is every agent, oldest first.
func (r *Registry) List() []wire.Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listLocked()
}

func (r *Registry) listLocked() []wire.Agent {
	out := make([]wire.Agent, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, a.Agent)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get is one agent.
func (r *Registry) Get(id string) (wire.Agent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(id)
	if err != nil {
		return wire.Agent{}, err
	}
	return a.Agent, nil
}

// Term is an agent's terminal (for attach).
func (r *Registry) Term(id string) (*ptyhost.Term, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(id)
	if err != nil {
		return nil, err
	}
	if a.term == nil {
		a.term = ptyhost.Exited(a.Size.Cols, a.Size.Rows, a.Exit)
	}
	return a.term, nil
}

// setState changes an agent's state and attention and tells subscribers.
func (r *Registry) setState(a *agent, state string, att *wire.Attention) {
	if state != wire.StateApproval {
		a.approvalKey, a.fromScreen = "", false
	}
	if state != wire.StateQuestion {
		a.screenAsk = ""
	}
	switch state {
	case wire.StateWorking, wire.StateApproval, wire.StateDone:
		a.engaged = true
	case wire.StateIdle:
		// Idle with the task still to be typed in is not past it.
		a.engaged = a.engaged || a.pendingTask == ""
	}
	if a.State == state && attentionEqual(a.Attention, att) {
		return
	}
	if a.State != state {
		a.StateSince = time.Now().UTC()
	}
	finished := a.Background && turnEnded(a.State, state)
	if state == wire.StateDone && a.State != state {
		r.turnCheckpoint(a) // checkpoint.go
	}
	if readyForReview(state) && !readyForReview(a.State) {
		r.settledForReview(a) // review.go
	}
	a.State, a.Attention = state, att
	r.changed(a)
	if finished {
		r.finishedInBackground(a) // close.go
	}
}

func attentionEqual(x, y *wire.Attention) bool {
	if x == nil || y == nil {
		return x == y
	}
	return x.Kind == y.Kind && x.Title == y.Title && x.Detail == y.Detail && strings.Join(x.Options, ",") == strings.Join(y.Options, ",")
}

// changed tells subscribers and schedules a save (the lock is held).
func (r *Registry) changed(a *agent) {
	snap := a.Agent
	for s := range r.subs {
		s.push(snap.ID, &snap)
	}
	r.scheduleSave()
}

// Spawn starts a new agent.
func (r *Registry) Spawn(p wire.SpawnParams) (wire.Agent, error) {
	if p.Machine != "" && p.Machine != r.machine {
		return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", p.Machine)
	}
	if p.Scratch && strings.TrimSpace(p.Project) == "" {
		// scratch projects: a new one, named from the task.
		dir, err := r.newScratch(p.Name, p.Task)
		if err != nil {
			return wire.Agent{}, err
		}
		p.Project = dir
	}
	project := cleanPath(p.Project)
	if project == "" || !filepath.IsAbs(project) {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "project must be an absolute path")
	}
	if st, err := os.Stat(project); err != nil || !st.IsDir() {
		return wire.Agent{}, wire.Errorf(wire.CodeNotFound, "no directory %s", project)
	}
	r.mu.Lock()
	profiles, defaults, size, settings := r.profiles, r.settings.Defaults, r.settings.Size, r.settings
	r.mu.Unlock()
	profileName, profile, err := resolveProfile(profiles, defaults, p.Profile, p.Kind, project)
	if err != nil {
		return wire.Agent{}, err
	}
	task := strings.TrimSpace(p.Task)
	name := strings.TrimSpace(p.Name)
	switch {
	case name != "":
	case task != "":
		name = NameFor(task)
	case profile.Kind == wire.KindShell:
		name = "shell"
	default:
		name = filepath.Base(project)
	}
	slug := Slugify(name, 40)
	if task != "" && p.Name == "" {
		slug = NameFor(task)
	}
	worktree, branch, err := r.g.makeWorktree(project, p.Worktree, strings.TrimSpace(p.Branch), slug, r.opt.WorktreeRoot, r.opt.ProjectsRoot)
	if err != nil {
		var we *wire.Error
		if errors.As(err, &we) {
			return wire.Agent{}, we
		}
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "%v", err)
	}
	if (profile.Kind == wire.KindClaude || profile.Kind == wire.KindCodex) && settings.trustProjects() {
		dir := project
		if worktree != "" {
			dir = worktree
		}
		// The user chose this folder for the agent: trust it the way
		// accepting the agent's own question would (trustwrite.go).
		if res, err := r.pretrust(profile.Kind, dir); err != nil {
			r.opt.Logf("hesperd: trusting %s for %s: %v (the agent will ask)", dir, profile.Kind, err)
		} else if res == trustMarked {
			r.opt.Logf("hesperd: trusted %s for %s", trustKey(dir, r.g), profile.Kind)
		}
	}
	if err := r.waitCommand(profile); err != nil {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "cannot start %s: %v", profile.Argv[0], err)
	}
	now := time.Now().UTC()
	projectID := r.projectOf(dirOf(project, worktree)) // projects step 1
	reviewBase := r.reviewBaseAt(profile.Kind, dirOf(project, worktree))
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "hesperd is stopping")
	}
	local := newLocalID()
	for r.agents[local] != nil {
		local = newLocalID()
	}
	a := &agent{local: local, Agent: wire.Agent{
		ID: r.id(local), Machine: r.machine, Kind: profile.Kind, Profile: profileName, Name: name, Task: task,
		Project: project, ProjectID: projectID, Worktree: worktree, Branch: branch, State: wire.StateStarting, StateSince: now, Created: now,
		Size: wire.Size{Cols: ptyhost.DefaultCols, Rows: ptyhost.DefaultRows},
		// agent tree (tree.go): set by the server from the caller, or by
		// a controller for a host
		Parent: p.Parent, Depth: p.Depth, LetParentAnswer: p.LetParentAnswer && p.Parent != "",
		Track:      p.Track && profile.Kind == wire.KindShell, // shell.go
		ReviewBase: reviewBase,                                // review.go
	}}
	if a.Parent == "" {
		a.Depth = 0
	}
	if size != nil && size.Cols > 0 && size.Rows > 0 {
		a.Size = *size
	}
	if a.Kind == wire.KindClaude {
		a.SessionID = newUUID() // --session-id: resumable from the start
	}
	if a.Kind == wire.KindShell {
		a.shellTask = task // typed once the prompt is ready (shell.go)
	}
	r.agents[local] = a
	if err := r.start(a, profile, false); err != nil {
		delete(r.agents, local)
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "cannot start %s: %v", profile.Argv[0], err)
	}
	r.touchProject(project, now)
	r.changed(a)
	return a.Agent, nil
}

// argv is the command line of a profile for an agent: placeholders
// filled, the first prompt or the session to resume added as each kind
// takes them (as the former bin/ghosty-pane did).
func (r *Registry) argv(a *agent, profile wire.Profile, resume, deliverLater bool) []string {
	vars := strings.NewReplacer("{name}", sessionName(a.Task, a.Name), "{loginShell}", r.opt.LoginShell,
		"{project}", a.Project, "{dir}", a.Dir(), "{id}", a.ID)
	argv := make([]string, 0, len(profile.Argv)+4)
	for _, s := range profile.Argv {
		argv = append(argv, vars.Replace(s))
	}
	if a.forkFrom != "" { // shared history: sessions.fork
		return r.forkArgv(a, argv)
	}
	prompt := func() {
		if a.Task == "" || deliverLater {
			return
		}
		if strings.HasPrefix(a.Task, "-") {
			argv = append(argv, "--") // a prompt is never an option
		}
		argv = append(argv, a.Task)
	}
	switch a.Kind {
	case wire.KindClaude:
		if resume {
			argv = append(argv, "--resume", a.SessionID)
		} else {
			if a.SessionID != "" {
				argv = append(argv, "--session-id", a.SessionID)
			}
			prompt()
		}
	case wire.KindCodex:
		// hesperd's hooks (codexargs.go) after the profile's options.
		extra := r.codexSessionArgs()
		if resume {
			// codex resume [OPTIONS] ID
			head := append([]string{argv[0], "resume"}, argv[1:]...)
			argv = append(append(head, extra...), a.SessionID)
		} else {
			argv = append(argv, extra...)
			prompt()
		}
	}
	return argv
}

// start runs an agent's process (the lock is held).
func (r *Registry) start(a *agent, profile wire.Profile, resume bool) error {
	deliverLater := false
	if a.Kind == wire.KindClaude && !resume && a.Task != "" && !claudeTrusted(r.opt.ClaudeConfig, a.Dir(), r.g) {
		deliverLater = true
	}
	argv := r.argv(a, profile, resume, deliverLater)
	env := agentEnv(r.env, map[string]string{
		"TERM": "xterm-256color", "COLORTERM": "truecolor", "TERM_PROGRAM": "hesperd",
		"HESPER_AGENT_ID": a.ID, "HESPER_SOCKET": r.opt.Socket,
	})
	a.gen++
	gen, local := a.gen, a.local
	cfg := ptyhost.Config{
		Argv: argv, Env: env, Dir: a.Dir(), Cols: a.Size.Cols, Rows: a.Size.Rows,
		OnResize: func(cols, rows int) { r.resized(local, gen, cols, rows) },
		OnExit:   func(e *wire.Exit) { r.exited(local, gen, e) },
	}
	a.watch, a.shell = nil, nil
	switch a.Kind {
	case wire.KindCodex:
		a.watch = &codexWatch{}
		cfg.OnOutput = func(p []byte) { r.codexOutput(local, gen, p) }
	case wire.KindShell:
		a.shell = newShellWatch()
		cfg.OnOutput = a.shell.saw
	}
	term, err := ptyhost.Start(cfg)
	if err != nil {
		return err
	}
	a.term, a.PID, a.Exit, a.running, a.stopping = term, term.PID(), nil, true, false
	a.Ended = "" // runs again: no longer "killed"
	a.started, a.respawn, a.cameBack, a.answered = time.Now(), false, false, false
	a.endedTurn, a.codexHooks, a.fromScreen, a.approvalKey = "", false, false, ""
	a.expectPrompt = !resume && a.Task != "" && a.Kind != wire.KindShell && !deliverLater
	a.pendingTask, a.screenAsk = "", ""
	if a.Kind == wire.KindClaude || a.Kind == wire.KindCodex {
		go r.watchFirstRun(local, gen)
	}
	if deliverLater {
		a.pendingTask = a.Task
		go r.deliverLater(local, gen)
	}
	if a.Kind == wire.KindShell {
		go r.watchShell(local, gen, a.shell)
		// starting until its task is typed (shell.go), else idle
		state := wire.StateIdle
		if a.shellTask != "" && a.Track {
			state = wire.StateStarting
		}
		a.State, a.StateSince, a.Attention, a.Activity = state, time.Now().UTC(), nil, ""
	} else {
		a.State, a.StateSince, a.Attention = wire.StateStarting, time.Now().UTC(), nil
	}
	return nil
}

func (r *Registry) resized(local string, gen, cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.agents[local]; a != nil && a.gen == gen {
		a.Size = wire.Size{Cols: cols, Rows: rows}
		r.changed(a)
	}
}

func (r *Registry) exited(local string, gen int, e *wire.Exit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.agents[local]
	if a == nil || a.gen != gen {
		return
	}
	if a.watch != nil && a.watch.timer != nil {
		a.watch.timer.Stop()
		a.watch.timer = nil
	}
	r.adoptCodexSession(a)
	a.Exit = e
	a.shellTask = ""
	if r.closing {
		// The daemon stops: the agent stays "running" for the respawn.
		r.scheduleSave()
		return
	}
	a.running = false
	a.Activity = ""
	if a.closeReason != "" {
		// Closed (agents.close, a background agent that finished): it
		// leaves the registry; its session stays in the history.
		r.removeLocked(a, a.closeReason)
		return
	}
	quick := time.Since(a.started) < quickExit
	failed := !a.stopping && (e.Signal != "" || (e.Code != nil && *e.Code != 0))
	// A respawned or resumed agent that ends at once, even "successfully",
	// did not come back (seen on a Mac mini: Claude resumed after a daemon
	// restart ended with status 0 two seconds later): an error to look at
	// and resume, never a quiet "exited".
	notBack := !a.stopping && !failed && quick && a.cameBack && !a.answered && a.Kind != wire.KindShell
	if failed || notBack {
		title, detail := "Exited", e.Signal
		if e.Code != nil {
			detail = fmt.Sprintf("exit status %d", *e.Code)
		}
		if notBack {
			title, detail = "Not resumed", "ended at once after coming back ("+detail+")"
		}
		if a.term != nil && quick {
			// It ended at once (a resume of a lost session, a bad flag):
			// what it said is the reason.
			if why := lastLines(a.term, 3); why != "" {
				detail = firstLine(detail+": "+why, 300)
			}
		}
		r.setState(a, wire.StateError, &wire.Attention{Kind: "error", Title: title, Detail: detail})
	} else {
		if a.Background && a.Ended == "" {
			// A background agent that ended on its own (or was stopped)
			// has finished: closed (close.go).
			r.removeLocked(a, wire.ReasonFinishedInBackground)
			return
		}
		r.setState(a, wire.StateExited, nil)
	}
	r.changed(a)
}

// quickExit: an agent that fails this soon after its start gets its last
// screen lines in the error's detail.
var quickExit = 3 * time.Second

// Input writes text to an agent's terminal. paste wraps it in bracketed
// paste when the agent has it on; submit sends \r after it.
func (r *Registry) Input(p wire.InputParams) error {
	r.mu.Lock()
	a, err := r.find(p.ID)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	term, shell := a.term, a.shell
	alive := term != nil && a.Exit == nil
	r.mu.Unlock()
	if !alive {
		return wire.Errorf(wire.CodeInvalid, "%s is not running", p.ID)
	}
	if shell != nil {
		// A shell just started: its prompt first (shell.go).
		shell.waitReady()
		if p.Submit {
			r.mu.Lock()
			if a.term == term {
				r.shellSubmitted(a, p.Text)
			}
			r.mu.Unlock()
		}
	}
	return typeText(term, p.Text, p.Paste, p.Submit)
}

func typeText(term *ptyhost.Term, text string, paste, submit bool) error {
	body := text
	if paste {
		on := false
		term.WithScreen(func(s *vt.Screen) { on = s.Modes().BracketedPaste })
		if on {
			body = "\x1b[200~" + text + "\x1b[201~"
		}
	}
	if err := term.Input([]byte(body)); err != nil {
		return wire.Errorf(wire.CodeInvalid, "%v", err)
	}
	if submit {
		if paste && body != "" {
			// Let the TUI take the paste before Enter.
			go func() {
				time.Sleep(100 * time.Millisecond)
				term.Input([]byte("\r"))
			}()
			return nil
		}
		return term.Input([]byte("\r"))
	}
	return nil
}

// ApprovalKeys are the keys that answer an approval, per agent kind: Claude
// Code's numbered choices, Codex's letters, Escape to decline.
var ApprovalKeys = map[string]map[string]string{
	wire.KindClaude: {wire.Allow: "1", wire.Always: "2", wire.Deny: "\x1b"},
	wire.KindCodex:  {wire.Allow: "y", wire.Always: "a", wire.Deny: "\x1b"},
}

// claudeChoiceRow is one numbered choice of Claude Code's permission
// prompt: "❯ 1. Yes", "  2. Yes, and don't ask again for git push
// commands in …", "  3. No, and tell Claude what to do differently (esc)"
// (2.1.29x, internal/fakeagent/testdata/claude-permission.txt).
var claudeChoiceRow = regexp.MustCompile(`^\s*[❯>]?\s*(\d)\.\s+(.+)$`)

var claudeQuestionRow = regexp.MustCompile(`(?i)\bdo you want to\b`)

var claudeAlwaysChoice = regexp.MustCompile(`(?i)^yes, (and )?(don't ask again|allow all)`)

// claudeAlwaysKey is the key of the "don't ask again" choice on a Claude
// permission prompt: its number; "2" when no numbered choices show (the
// screen not drawn yet); ok false when the choices have none.
func claudeAlwaysKey(screen string) (key string, ok bool) {
	rows := strings.Split(screen, "\n")
	// The prompt's question ("Do you want to proceed?", "… make this
	// edit …?"): its choices follow; numbered lists above it are output.
	for i := len(rows) - 1; i >= 0; i-- {
		if claudeQuestionRow.MatchString(rows[i]) {
			rows = rows[i:]
			break
		}
	}
	choices := false
	for _, row := range rows {
		m := claudeChoiceRow.FindStringSubmatch(row)
		if m == nil {
			continue
		}
		if claudeAlwaysChoice.MatchString(m[2]) {
			return m[1], true
		}
		if strings.HasPrefix(strings.ToLower(m[2]), "no") {
			choices = true
		}
	}
	if choices {
		return "", false
	}
	return ApprovalKeys[wire.KindClaude][wire.Always], true
}

// Answer answers an agent's approval; a message after deny tells the agent
// what to do instead.
func (r *Registry) Answer(p wire.AnswerParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(p.ID)
	if err != nil {
		return err
	}
	if a.screenAsk != "" && a.State == wire.StateQuestion && a.term != nil && a.Exit == nil {
		return r.answerFirstRun(a, p.Decision)
	}
	keys, ok := ApprovalKeys[a.Kind]
	if !ok {
		return wire.Errorf(wire.CodeInvalid, "%s agents have no approvals", a.Kind)
	}
	key, ok := keys[p.Decision]
	if !ok {
		return wire.Errorf(wire.CodeInvalid, "decision must be allow, always or deny")
	}
	if a.State != wire.StateApproval || a.term == nil || a.Exit != nil {
		return wire.Errorf(wire.CodeInvalid, "%s is not waiting for an approval", p.ID)
	}
	if a.Kind == wire.KindClaude && p.Decision == wire.Always {
		// "Don't ask again" is not always choice 2 (some prompts have
		// none: 2 is "No" there): its number on the screen.
		term, gen := a.term, a.gen
		r.mu.Unlock()
		k, ok := claudeAlwaysKey(screenText(term))
		r.mu.Lock()
		if a.gen != gen || a.State != wire.StateApproval || a.Exit != nil {
			return wire.Errorf(wire.CodeInvalid, "%s is not waiting for an approval", p.ID)
		}
		if !ok {
			return wire.Errorf(wire.CodeInvalid, "%s offers no \"don't ask again\" here: answer allow or deny", p.ID)
		}
		key = k
	}
	if err := a.term.Input([]byte(key)); err != nil {
		return wire.Errorf(wire.CodeInvalid, "%v", err)
	}
	message := strings.TrimSpace(p.Message)
	if p.Decision == wire.Deny && message != "" {
		term := a.term
		go func() {
			time.Sleep(300 * time.Millisecond)
			typeText(term, message, true, true)
		}()
	}
	if p.Decision == wire.Deny && message == "" {
		r.setState(a, wire.StateIdle, nil)
	} else {
		r.setState(a, wire.StateWorking, nil)
	}
	return nil
}

// Stop hangs up an agent (SIGHUP, SIGKILL after the grace); it stays
// listed as exited.
func (r *Registry) Stop(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(id)
	if err != nil {
		return err
	}
	if a.term == nil || a.Exit != nil {
		if a.respawn {
			// One that did not come back stays down now.
			a.respawn = false
			r.scheduleSave()
		}
		return nil
	}
	a.stopping, a.running = true, false
	a.term.Stop(r.opt.StopGrace)
	r.scheduleSave()
	return nil
}

// Resume respawns an exited agent with its session.
func (r *Registry) Resume(id string) (wire.Agent, error) {
	projectID := "" // projects step 1: a resumed agent's project exists
	if got, err := r.Get(id); err == nil {
		projectID = r.projectOf(got.Dir())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(id)
	if err != nil {
		return wire.Agent{}, err
	}
	if projectID != "" {
		a.ProjectID = projectID
	}
	if a.term != nil && a.Exit == nil {
		return wire.Agent{}, wire.Errorf(wire.CodeExists, "%s is running", id)
	}
	r.adoptCodexSession(a)
	profile, err := r.profileOf(a)
	if err != nil {
		return wire.Agent{}, err
	}
	// Its command may be gone for a moment (an update): wait without the
	// lock, then look again.
	r.mu.Unlock()
	waitErr := r.waitCommand(profile)
	r.mu.Lock()
	if a, err = r.find(id); err != nil {
		return wire.Agent{}, err
	}
	if a.term != nil && a.Exit == nil {
		return wire.Agent{}, wire.Errorf(wire.CodeExists, "%s is running", id)
	}
	if r.closing {
		return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "hesperd is stopping")
	}
	if waitErr == nil {
		_, err = r.comeBack(a, profile)
	} else {
		err = waitErr
	}
	if err != nil {
		r.notResumed(a, err)
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "cannot start %s: %v", profile.Argv[0], err)
	}
	r.changed(a)
	return a.Agent, nil
}

// notResumed: an agent that could not be started again is an error with
// the reason, for the app to show (the lock is held).
func (r *Registry) notResumed(a *agent, err error) {
	a.running = false
	if a.Exit == nil {
		code := -1
		a.Exit = &wire.Exit{Code: &code}
	}
	r.setState(a, wire.StateError, &wire.Attention{Kind: "error", Title: "Not resumed", Detail: firstLine(err.Error(), 300)})
}

// waitCommand waits up to CommandWait for the profile's command to be on
// the agents' PATH (without the lock).
func (r *Registry) waitCommand(profile wire.Profile) error {
	if len(profile.Argv) == 0 {
		return errors.New("the profile has no command")
	}
	name := strings.ReplaceAll(profile.Argv[0], "{loginShell}", r.opt.LoginShell)
	deadline := time.Now().Add(r.opt.CommandWait)
	for {
		_, err := ptyhost.LookPath(name, r.env)
		if err == nil || !errors.Is(err, exec.ErrNotFound) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// NotResumedNote is the summary of an agent that had to start over
// without its conversation (and without its task, which it already ran).
const NotResumedNote = "Previous conversation could not be resumed"

// comeBack starts an agent again (agents.resume, daemon restart; the lock
// is held): with its session when that exists (resumeOrFresh). Else fresh,
// and its task goes along only when the agent never got past starting: a
// task it already ran ("push the branch") must never run twice. An agent
// that took its task starts with no prompt and a note in its summary; its
// hooks (or Codex's idle composer) make it idle. fresh: it started anew.
func (r *Registry) comeBack(a *agent, profile wire.Profile) (fresh bool, err error) {
	resume := r.resumeOrFresh(a)
	if resume || a.Kind == wire.KindShell || !a.engaged || a.Task == "" {
		err := r.start(a, profile, resume)
		a.cameBack = err == nil
		return !resume && a.Kind != wire.KindShell, err
	}
	r.opt.Logf("hesperd: %s: starting fresh without its task (it ran it already)", a.ID)
	task := a.Task
	a.Task = ""
	err = r.start(a, profile, false)
	a.Task = task
	if err == nil {
		a.Summary = NotResumedNote
		a.cameBack = true
	}
	return true, err
}

// resumeOrFresh decides how an agent comes back (the lock is held): with
// its session when that exists, else fresh (Claude keeps its session id,
// Codex gets a new one).
func (r *Registry) resumeOrFresh(a *agent) bool {
	if a.Kind == wire.KindShell {
		return false
	}
	if r.resumable(a) {
		return true
	}
	if a.SessionID != "" {
		r.opt.Logf("hesperd: %s: session %s was never saved; starting it fresh", a.ID, a.SessionID)
	}
	if a.Kind == wire.KindCodex {
		a.SessionID = ""
	}
	a.sessionSeen = false
	return false
}

func (r *Registry) profileOf(a *agent) (wire.Profile, error) {
	if _, p, ok := lookupProfile(r.profiles, a.Profile); ok && p.Kind == a.Kind {
		return p, nil
	}
	_, p, err := resolveProfile(r.profiles, r.settings.Defaults, "", a.Kind, a.Project)
	return p, err
}

// Remove forgets an agent that is not running.
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(id)
	if err != nil {
		return err
	}
	if a.term != nil && a.Exit == nil {
		return wire.Errorf(wire.CodeInvalid, "%s is running: stop it first", id)
	}
	r.removeLocked(a, wire.ReasonRemoved)
	return nil
}

// Rename gives an agent a new display name.
func (r *Registry) Rename(id, name string) (wire.Agent, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "\n\r") {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "a name is one non-empty line")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(id)
	if err != nil {
		return wire.Agent{}, err
	}
	a.Name = name
	r.changed(a)
	return a.Agent, nil
}

// Profiles are the launch profiles and defaults.
func (r *Registry) Profiles() wire.ProfilesResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Defaults naming a legacy profile show the profile they resolve to.
	d := r.settings.Defaults
	current := func(m map[string]string) map[string]string {
		out := make(map[string]string, len(m))
		for k, name := range m {
			if pn, _, ok := lookupProfile(r.profiles, name); ok {
				name = pn
			}
			out[k] = name
		}
		return out
	}
	d.Kinds, d.Projects = current(d.Kinds), current(d.Projects)
	return wire.ProfilesResult{Profiles: r.profiles, Defaults: d}
}

// Hook takes one hook event.
func (r *Registry) Hook(p wire.HookParams) error {
	var data map[string]any
	if len(p.Payload) > 0 {
		if err := json.Unmarshal(p.Payload, &data); err != nil {
			// Codex's notify passes the payload as a JSON string argument.
			var s string
			if json.Unmarshal(p.Payload, &s) != nil || json.Unmarshal([]byte(s), &data) != nil {
				return wire.Errorf(wire.CodeInvalid, "payload is not a JSON object")
			}
		}
	}
	if data == nil {
		data = map[string]any{}
	}
	event := p.Event
	if event == "" {
		event = str(data, "hook_event_name")
	}
	if p.Source != wire.KindClaude && p.Source != wire.KindCodex {
		return wire.Errorf(wire.CodeInvalid, "source must be claude or codex")
	}
	summary, message := "", ""
	if event == "Stop" || (event == "notify" && str(data, "type") == "agent-turn-complete") {
		message = lastMessage(data)
		summary = summarize(message)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.hookAgent(p, event, data)
	if a == nil || a.term == nil || a.Exit != nil {
		return nil // not one of ours (another claude, a nested one): ignored
	}
	if !a.sessionSeen && a.SessionID != "" && confirmsSession(event, data) {
		if sid := payloadSessionID(data); sid == "" || sid == a.SessionID {
			a.sessionSeen = true
		}
	}
	r.hookEvent(a, p.Source, event, data, summary)
	if message = strings.TrimSpace(message); message != "" {
		r.setLastMessage(a, message) // agent tree: agents.result
	}
	return nil
}

// hookAgent finds the agent a hook is about: HESPER_AGENT_ID, else (Codex
// runs hooks in its own server) the session, else the directory.
func (r *Registry) hookAgent(p wire.HookParams, event string, data map[string]any) *agent {
	sid := payloadSessionID(data)
	if p.Agent != "" {
		m, local := r.split(p.Agent)
		a := r.agents[local]
		if m != r.machine || a == nil || a.Kind != p.Source {
			return nil
		}
		if sid != "" && a.SessionID != "" && sid != a.SessionID {
			// Another conversation in this agent's environment: its own
			// /clear or resume (a new session id), or a nested run.
			if event != "SessionStart" || str(data, "source") == "startup" {
				return nil
			}
			a.SessionID = sid
			r.changed(a)
		}
		if a.SessionID == "" && sid != "" {
			a.SessionID = sid
			r.changed(a)
		}
		return a
	}
	if sid != "" {
		for _, a := range r.agents {
			if a.SessionID == sid && a.Kind == p.Source {
				return a
			}
		}
	}
	cwd := str(data, "cwd")
	if p.Source != wire.KindCodex || cwd == "" {
		return nil
	}
	var here []*agent
	for _, a := range r.agents {
		if a.Kind == wire.KindCodex && a.term != nil && a.Exit == nil && sameDir(a.Dir(), cwd) {
			here = append(here, a)
		}
	}
	if len(here) > 1 && sid != "" {
		// A new conversation belongs to the one that has none yet.
		var fresh []*agent
		for _, a := range here {
			if a.SessionID == "" {
				fresh = append(fresh, a)
			}
		}
		here = fresh
	}
	if len(here) != 1 {
		return nil
	}
	a := here[0]
	if sid != "" && a.SessionID == "" {
		a.SessionID = sid
		r.changed(a)
	}
	return a
}

// Close stops the agents (they are respawned with resume next time) and
// saves the registry.
func (r *Registry) Close() {
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return
	}
	r.closing = true
	close(r.quit)
	var terms []*ptyhost.Term
	for _, a := range r.agents {
		if a.term != nil && a.Exit == nil {
			terms = append(terms, a.term)
			a.term.Stop(2 * time.Second)
		}
	}
	for s := range r.subs {
		s.close()
	}
	r.mu.Unlock()
	deadline := time.After(3 * time.Second)
	for _, t := range terms {
		select {
		case <-t.Done():
		case <-deadline:
		}
	}
	close(r.saveCh)
	<-r.saverEnd
	r.writeState()
	r.reviews().flush() // reviewlog.go
}
