package session

import "encoding/json"

// AgentRoles are the terminal roles that count as agents.
var AgentRoles = map[string]bool{"claude": true, "codex": true, "opencode": true, "gemini": true}

// Probe is what a host reports about a project before a handoff: whether the
// repository exists at the path, its remote, its branch heads and which of
// the asked commits it already has.
type Probe struct {
	Exists bool            `json:"exists"`
	Remote string          `json:"remote"`
	Heads  json.RawMessage `json:"heads,omitempty"`
	Has    map[string]bool `json:"has"`
}

// Job is a spawn started on a host. Result is the import's
// result once the job is done.
type Job struct {
	ID      string          `json:"job"`
	State   string          `json:"state"` // queued, running, done, failed
	Step    string          `json:"step"`
	StartAt int64           `json:"startAt,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JobError       `json:"error,omitempty"`
}

type JobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Finished reports whether the job reached done or failed.
func (j Job) Finished() bool { return j.State == "done" || j.State == "failed" }
