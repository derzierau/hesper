package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Shared history (internal/sessions), hooked in here: the sessions.*
// methods and notifications on the local socket, and starting an agent
// on an existing conversation (resume or fork; sessions.resume /
// sessions.fork). Nil (tests): no sessions methods.
type Sessions interface {
	// Call runs a sessions method (handled false: not one).
	Call(method string, params json.RawMessage) (result any, err error, handled bool)
	// Watch sends the sessions notifications of an agents.subscribe
	// connection until done closes or send fails.
	Watch(done <-chan struct{}, send func(method string, params any) error)
}

// SessionSpawn starts an agent on an existing conversation.
type SessionSpawn struct {
	Kind      string // claude | codex
	SessionID string // the conversation
	Dir       string // where it ran (its cwd)
	Name      string // display name (the session's title)
	Task      string // shown on the tile (never sent: the agent took it)
	Branch    string
	// Fork: a new conversation with the same history (claude --resume
	// <id> --fork-session, codex fork <id>); the original stays.
	Fork bool
	// Parent and Depth (agent tree): set by the server when an agent
	// resumes or forks a session (its child), or by a controller.
	Parent string
	Depth  int
}

// SpawnSession starts an agent that resumes (or forks) a conversation.
func (r *Registry) SpawnSession(p SessionSpawn) (wire.Agent, error) {
	if p.Kind != wire.KindClaude && p.Kind != wire.KindCodex {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "kind must be claude or codex")
	}
	dir := cleanPath(p.Dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "the session's folder must be an absolute path")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return wire.Agent{}, wire.Errorf(wire.CodeNotFound, "no directory %s", dir)
	}
	r.mu.Lock()
	profiles, defaults := r.profiles, r.settings.Defaults
	r.mu.Unlock()
	profileName, profile, err := resolveProfile(profiles, defaults, "", p.Kind, dir)
	if err != nil {
		return wire.Agent{}, err
	}
	if err := r.waitCommand(profile); err != nil {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "cannot start %s: %v", profile.Argv[0], err)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = filepath.Base(dir)
	}
	if p.Fork {
		name = "fork: " + name
	}
	now := time.Now().UTC()
	projectID := r.projectOf(dir)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "hesperd is stopping")
	}
	for _, a := range r.agents {
		if !p.Fork && a.Kind == p.Kind && a.SessionID == p.SessionID && a.term != nil && a.Exit == nil {
			return wire.Agent{}, &wire.Error{Code: wire.CodeLive, Message: p.SessionID + " runs in " + a.ID, AgentID: a.ID}
		}
	}
	local := newLocalID()
	for r.agents[local] != nil {
		local = newLocalID()
	}
	a := &agent{local: local, Agent: wire.Agent{
		ID: r.id(local), Machine: r.machine, Kind: p.Kind, Profile: profileName, Name: name, Task: p.Task,
		Project: dir, ProjectID: projectID, Branch: p.Branch, State: wire.StateStarting, StateSince: now, Created: now,
		Size: r.defaultSize(), Parent: p.Parent,
	}}
	if p.Parent != "" {
		a.Depth = p.Depth
	}
	// The conversation took its task long ago: never sent again.
	a.engaged = true
	resume := true
	if p.Fork {
		a.forkFrom = p.SessionID
		if p.Kind == wire.KindClaude {
			a.SessionID = newUUID() // the fork's id (--session-id)
		}
	} else {
		a.SessionID, a.sessionSeen = p.SessionID, true
	}
	r.agents[local] = a
	task := a.Task
	a.Task = "" // shown, not sent
	err = r.start(a, profile, resume)
	a.Task, a.forkFrom = task, ""
	if err != nil {
		delete(r.agents, local)
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "cannot start %s: %v", profile.Argv[0], err)
	}
	r.touchProject(dir, now)
	r.changed(a)
	return a.Agent, nil
}

// forkArgv is the command line of a fork (argv: the profile's, filled).
func (r *Registry) forkArgv(a *agent, argv []string) []string {
	switch a.Kind {
	case wire.KindClaude:
		argv = append(argv, "--resume", a.forkFrom, "--fork-session")
		if a.SessionID != "" {
			argv = append(argv, "--session-id", a.SessionID)
		}
	case wire.KindCodex:
		// codex fork [OPTIONS] ID
		head := append([]string{argv[0], "fork"}, argv[1:]...)
		argv = append(append(head, r.codexSessionArgs()...), a.forkFrom)
	}
	return argv
}

// watchSessions sends the shared history's notifications on a subscribed
// control connection (server.go's subscribe) until it closes.
func (c *ctrl) watchSessions() {
	h := c.s.reg.opt.Sessions
	if h == nil {
		return
	}
	h.Watch(c.done, func(method string, params any) error {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		return c.write(wire.Notification{JSONRPC: "2.0", Method: method, Params: data})
	})
}
