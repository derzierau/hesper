// Package wire is hesperd's local protocol (docs/rebuild-contract.md): the
// agent model, the JSON-RPC methods of control connections, the frames of
// attach connections, and a Go client for them (hesperctl, hesperd attach
// and hook, tests).
package wire

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Version is the protocol version hello reports.
const Version = "1"

// Agent kinds.
const (
	KindClaude = "claude"
	KindCodex  = "codex"
	KindShell  = "shell"
)

// Agent states.
const (
	StateStarting = "starting"
	StateWorking  = "working"
	StateApproval = "approval"
	StateQuestion = "question"
	StateDone     = "done"
	StateIdle     = "idle"
	StateError    = "error"
	StateExited   = "exited"
)

// Decisions of agents.answer.
const (
	Allow  = "allow"
	Always = "always"
	Deny   = "deny"
	// Trust and Leave ("exit") answer a first-run trust question the daemon found on
	// the agent's screen (attention options ["trust", "exit"]).
	Trust = "trust"
	Leave = "exit"
	// Skip and Update answer Codex's "Update available" screen (attention
	// options ["skip", "update"]).
	Skip   = "skip"
	Update = "update"
)

// Agent is one agent as the daemon reports it.
type Agent struct {
	ID      string `json:"id"`
	Machine string `json:"machine"`
	Kind    string `json:"kind"`
	Profile string `json:"profile"`
	Name    string `json:"name"`
	Task    string `json:"task"`
	Project string `json:"project"`
	// ProjectID is the project the agent works in (the deepest project
	// containing its folder; "scratch:<folder>" outside every project;
	// wire/projects.go).
	ProjectID  string     `json:"projectId,omitempty"`
	Worktree   string     `json:"worktree,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	State      string     `json:"state"`
	StateSince time.Time  `json:"stateSince"`
	Attention  *Attention `json:"attention,omitempty"`
	Summary    string     `json:"summary,omitempty"`
	// Activity is what the agent does right now (the running tool, e.g.
	// "Bash: git push"), from hooks; empty between turns.
	Activity  string    `json:"activity,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	Size      Size      `json:"size"`
	Created   time.Time `json:"created"`
	PID       int       `json:"pid"`
	Exit      *Exit     `json:"exit"`
	// Background (closing agents): the agent runs hidden from every wall
	// (agents.background); persisted. Once it finishes (done, idle,
	// exited) the daemon closes it (agents.removed reason
	// "finished-in-background").
	Background bool `json:"background,omitempty"`
	// Ended (closing agents): how a deliberately ended agent ended:
	// "killed" (agents.kill); cleared when it runs again.
	Ended string `json:"ended,omitempty"`
	// Parent (agent tree): the full id of the agent that started this
	// one (hesperctl new inside an agent); empty for one a person started.
	// Depth is 0 for a person's agent, the parent's depth + 1 otherwise.
	// LetParentAnswer: the parent may answer this agent's approvals and
	// questions (hesperctl new --let-parent-answer). All three persisted.
	Parent          string `json:"parent,omitempty"`
	Depth           int    `json:"depth,omitempty"`
	LetParentAnswer bool   `json:"letParentAnswer,omitempty"`
	// Track (shells only): hesperd follows the shell's commands, working
	// while one runs (hesperctl new --kind shell --track). Persisted.
	Track bool `json:"track,omitempty"`
	// Checkpoint (move work) is the agent's last checkpoint (Git folders
	// only; omitted before the first and for other folders). Persisted.
	Checkpoint *Checkpoint `json:"checkpoint,omitempty"`
}

// EndedKilled is Agent.Ended after agents.kill.
const EndedKilled = "killed"

// Dir is where the agent runs: its worktree, else its project.
func (a *Agent) Dir() string {
	if a.Worktree != "" {
		return a.Worktree
	}
	return a.Project
}

// Attention is what an agent in approval, question or error waits for.
type Attention struct {
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Detail  string   `json:"detail,omitempty"`
	Options []string `json:"options,omitempty"`
}

// Size is a terminal grid.
type Size struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Exit is how an agent's process ended: its exit code, or the signal that
// ended it (code then null).
type Exit struct {
	Code   *int   `json:"code"`
	Signal string `json:"signal,omitempty"`
}

// Machine is one entry of hello's machines.
type Machine struct {
	Short  string `json:"short"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
	RTTMs  int    `json:"rttMs"`
	Route  string `json:"route"`
}

// HelloParams and HelloResult are hello's.
type HelloParams struct {
	Client  string `json:"client"`
	Version string `json:"version"`
}

type HelloResult struct {
	Daemon   string    `json:"daemon"`
	Version  string    `json:"version"`
	Machine  string    `json:"machine"`
	Machines []Machine `json:"machines"`
}

// SpawnParams are agents.spawn's. Worktree is true (a new worktree) or a
// path (an existing worktree or where to create one).
type SpawnParams struct {
	Machine  string          `json:"machine,omitempty"`
	Profile  string          `json:"profile,omitempty"`
	Kind     string          `json:"kind,omitempty"`
	Project  string          `json:"project"`
	Task     string          `json:"task"`
	Name     string          `json:"name,omitempty"`
	Worktree json.RawMessage `json:"worktree,omitempty"`
	Branch   string          `json:"branch,omitempty"`
	// LetParentAnswer (agent tree): when an agent spawns this one, it may
	// answer its approvals and questions.
	LetParentAnswer bool `json:"letParentAnswer,omitempty"`
	// Track (shells only): working while a command runs, idle at the
	// prompt; without it a shell is always idle. Other kinds ignore it.
	Track bool `json:"track,omitempty"`
	// Caller (agent tree): the agent making the call (HESPER_AGENT_ID;
	// see Client.Caller). Advisory: hesperd prefers the agent it finds
	// the caller's process in.
	Caller string `json:"caller,omitempty"`
	// Parent and Depth (agent tree) are what a controller's hesperd
	// sends a host for a spawn an agent asked for; hesperd ignores them
	// from its local socket and derives them from the caller.
	Parent string `json:"parent,omitempty"`
	Depth  int    `json:"depth,omitempty"`
}

// AgentResult is agents.result's (agent tree): the agent's last turn's
// final message (Message, from its Stop or notify hook; empty before its
// first turn ended or when the hook carried none), else its Summary.
type AgentResult struct {
	ID      string    `json:"id"`
	State   string    `json:"state"`
	Message string    `json:"message,omitempty"`
	Summary string    `json:"summary,omitempty"`
	At      time.Time `json:"at,omitzero"`
}

// IDParams name one agent.
type IDParams struct {
	ID string `json:"id"`
}

// InputParams are agents.input's. Paste and Submit are hesperd additions:
// Paste wraps Text in bracketed paste when the agent has it on, Submit
// sends \r after it.
type InputParams struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Paste  bool   `json:"paste,omitempty"`
	Submit bool   `json:"submit,omitempty"`
}

// AnswerParams are agents.answer's.
type AnswerParams struct {
	ID       string `json:"id"`
	Decision string `json:"decision"`
	Message  string `json:"message,omitempty"`
}

// RenameParams are agents.rename's.
type RenameParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BackgroundParams are agents.background's.
type BackgroundParams struct {
	ID         string `json:"id"`
	Background bool   `json:"background"`
}

// CloseResult is agents.close's: the session the agent ran (to resume it
// with sessions.resume), when it has one.
type CloseResult struct {
	Session string `json:"session,omitempty"`
}

// MoveParams are agents.move's.
type MoveParams struct {
	ID string `json:"id"`
	To string `json:"to"`
	// Fork (move work): the source agent stays; Interrupt: a working
	// agent is interrupted first (else error "busy"); LeaveProcesses:
	// processes it started stay behind (else error "processes").
	Fork           bool `json:"fork,omitempty"`
	Interrupt      bool `json:"interrupt,omitempty"`
	LeaveProcesses bool `json:"leaveProcesses,omitempty"`
}

// HookParams are hook's: what `hesperd hook` sends. Agent is the hook
// process's HESPER_AGENT_ID (empty when the agent runs its hooks elsewhere,
// as Codex does); the daemon then finds the agent by session and directory.
type HookParams struct {
	Agent   string          `json:"agent,omitempty"`
	Source  string          `json:"source"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Project is one entry of projects.recent: the projects (their folder on
// this machine, ProjectID set) first, then other folders agents started in.
type Project struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	LastUsed  time.Time `json:"lastUsed"`
	ProjectID string    `json:"projectId,omitempty"`
}

// Profile is a launch profile.
type Profile struct {
	Kind string   `json:"kind"`
	Argv []string `json:"argv"`
}

// Defaults are the default profiles: Kind is the kind a spawn without
// profile and kind gets, Kinds the profile per kind, Projects the profile
// per project path.
type Defaults struct {
	Kind     string            `json:"kind"`
	Kinds    map[string]string `json:"kinds"`
	Projects map[string]string `json:"projects,omitempty"`
}

// ProfilesResult is profiles.list's.
type ProfilesResult struct {
	Profiles map[string]Profile `json:"profiles"`
	Defaults Defaults           `json:"defaults"`
}

// Notification methods of agents.subscribe.
const (
	NoteChanged = "agents.changed"
	NoteRemoved = "agents.removed"
)

// Changed and Removed are their params.
type Changed struct {
	Agent Agent `json:"agent"`
}

type Removed struct {
	ID string `json:"id"`
	// Reason (closing agents; old clients ignore it): why the agent left
	// the registry: "closed" (agents.close), "finished-in-background",
	// "removed" (agents.remove); empty when the daemon dropped it (its
	// machine gone, a machine renamed).
	Reason string `json:"reason,omitempty"`
	// Data (move work): with reason "moved", the agent it became.
	Data *RemovedData `json:"data,omitempty"`
}

// Removal reasons.
const (
	ReasonClosed               = "closed"
	ReasonFinishedInBackground = "finished-in-background"
	ReasonRemoved              = "removed"
)

// Error codes (data.code of a JSON-RPC error).
const (
	CodeNotFound    = "not_found"
	CodeInvalid     = "invalid"
	CodeExists      = "exists"
	CodeUnavailable = "unavailable"
	CodeForbidden   = "forbidden"
	CodeRemote      = "remote"
	// CodeOffline (closing agents): the agent's machine is not reachable
	// right now; its JSON-RPC error number is RPCOffline (-32010). Sent
	// by agents.close, agents.kill and agents.background (the app queues
	// them); other methods keep "unavailable".
	CodeOffline = "offline"
)

// Error is a daemon error: a code and a human message.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// AgentID (shared history): the agent a "live" session runs in.
	AgentID string `json:"agentId,omitempty"`
	// Processes (move work): error "processes" lists them.
	Processes []Process `json:"processes,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Errorf makes an Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// StateDir is $HESPER_STATE_DIR, else ~/.local/state/hesper.
func StateDir() string {
	if dir := os.Getenv("HESPER_STATE_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/state/hesper")
}

// SocketPath is the daemon's socket: $HESPER_SOCKET, else hesperd.sock in
// the state directory.
func SocketPath() string {
	if path := os.Getenv("HESPER_SOCKET"); path != "" {
		return path
	}
	return filepath.Join(StateDir(), "hesperd.sock")
}

// ConfigDir is $HESPER_CONFIG_DIR, else ~/.config/hesper.
func ConfigDir() string {
	if dir := os.Getenv("HESPER_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config/hesper")
}
