package wire

import (
	"encoding/json"
	"time"
)

// Move work across Macs (docs/rebuild-contract.md, "As built — move
// work"): checkpoints of an agent's folder, and agents.move with its
// preflight errors and progress events.

// Checkpoint is the last checkpoint of an agent's Git folder: a commit
// (never on a branch) of HEAD, the index and the working tree with
// untracked files (ignored files left out), kept at Ref
// (refs/hesper/checkpoints/<local id>) in the folder's repository.
// Changed is how many files differ from HEAD.
type Checkpoint struct {
	Ref     string    `json:"ref"`
	Commit  string    `json:"commit"`
	At      time.Time `json:"at"`
	Changed int       `json:"changed"`
	Branch  string    `json:"branch,omitempty"`
}

// CheckpointResult is agents.checkpoint's: null for a folder that is not
// a Git repository (with a commit).
type CheckpointResult struct {
	Checkpoint *Checkpoint `json:"checkpoint"`
}

// Process is a process an agent started that would not move with it
// (agents.move error "processes").
type Process struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

// MoveResult is agents.move's: Agent is the new agent's id. The moved
// agent's own fields follow at the top level as well (what agents.move
// answered before: an Agent).
type MoveResult struct {
	Agent string `json:"-"`
	Moved Agent  `json:"-"`
}

func (m MoveResult) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(m.Moved)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	fields["agent"], _ = json.Marshal(m.Agent)
	return json.Marshal(fields)
}

func (m *MoveResult) UnmarshalJSON(data []byte) error {
	var head struct {
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &m.Moved); err != nil {
		return err
	}
	m.Agent = head.Agent
	if m.Agent == "" {
		m.Agent = m.Moved.ID
	}
	return nil
}

// Moving is agents.moving's params: a move's progress. Step is
// "checkpoint", "transfer" (Percent 0–100), "worktree", "resume", then
// "done" (Agent: the new agent's id) or "failed" (Error).
type Moving struct {
	ID      string `json:"id"`
	Step    string `json:"step"`
	To      string `json:"to"`
	Percent int    `json:"percent,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Fork    bool   `json:"fork,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// Move steps (Moving.Step).
const (
	MoveCheckpoint = "checkpoint"
	MoveTransfer   = "transfer"
	MoveWorktree   = "worktree"
	MoveResume     = "resume"
	MoveDone       = "done"
	MoveFailed     = "failed"
)

// NoteMoving is the agents.subscribe notification of a move's progress.
const NoteMoving = "agents.moving"

// RemovedData is agents.removed's data: To is the agent a moved one
// became (reason "moved").
type RemovedData struct {
	To string `json:"to,omitempty"`
}

// ReasonMoved: the agent continues on another machine (agents.move).
const ReasonMoved = "moved"

// agents.move's preflight error codes (data.code); an unreachable target
// is CodeOffline (-32010).
const (
	CodeBusy        = "busy"         // the agent is working (params.interrupt moves it anyway)
	CodeProcesses   = "processes"    // it started processes that would not move (data.processes; params.leaveProcesses)
	CodeToolMissing = "tool-missing" // the target has no claude / codex
	CodeNoRemote    = "no-remote"    // the project is not on the target and has no Git remote to clone
	CodeTooLarge    = "too-large"    // the bundle is over the cap (200 MB)
)

// MoveCodes are the codes a move's error keeps across machines.
var MoveCodes = []string{CodeBusy, CodeProcesses, CodeToolMissing, CodeNoRemote, CodeTooLarge, CodeOffline}
