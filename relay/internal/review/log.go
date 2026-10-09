package review

import "time"

// Log is what hesperd keeps per agent for its review (persisted, removed
// with the agent): the reviewed point review.sendBack records, and its
// hook events (evidence.go, the last MaxEvents) with the turns counted
// per session.
type Log struct {
	Reviewed *Reviewed      `json:"reviewed,omitempty"`
	Turns    map[string]int `json:"turns,omitempty"`
	Events   []Event        `json:"events,omitempty"`
}

// Reviewed is a reviewed point: the folder's tree when the reviewer sent
// the work back, kept as Commit at Ref (for an interdiff later).
type Reviewed struct {
	Commit string    `json:"commit"`
	Tree   string    `json:"tree"`
	Ref    string    `json:"ref"`
	At     time.Time `json:"at"`
}
