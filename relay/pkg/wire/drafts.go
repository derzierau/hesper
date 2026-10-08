package wire

import "time"

// Draft is a new agent not started yet: the app's draft tile (the composer
// in the wall). The local daemon keeps drafts so they survive app and
// daemon restarts; Machine is where the agent will run, so the app shows
// the draft among that machine's tiles. Everything but ID is the app's.
type Draft struct {
	// ID is "d-" and 1–40 of [a-z0-9-]; drafts.save with an empty ID gets
	// one. The app picks its own so it can show the draft before the reply.
	ID   string `json:"id"`
	Text string `json:"text"`
	// The resolved properties (tokens and chips); empty: the default.
	Machine string `json:"machine,omitempty"`
	// MachineExplicit: the user chose Machine (a chip, the palette, an
	// @machine token). Otherwise Machine follows the folder (the app
	// moves it to the machine that has the folder). Drafts saved before
	// this field have it false.
	MachineExplicit bool   `json:"machineExplicit,omitempty"`
	Project         string `json:"project,omitempty"`
	// Band is the project id the draft was opened in (a band's ＋, ⌘N on a
	// focused band heading, the sidebar's New Agent in …): the app shows
	// it in that project's band in every window and after restarts.
	// Empty: the "New" area.
	Band string `json:"band,omitempty"`
	// Wall is the id of the wall window the draft was made in (windows.json:
	// "main", "wall-…"): that window shows it. Empty, or a window that isn't
	// open: the main wall shows it (its "New" area or its project's band).
	Wall string `json:"wall,omitempty"`
	// ProjectLocked: made in a project window, the project is that window's
	// (a lock on the project chip) until the user picks another folder.
	ProjectLocked bool   `json:"projectLocked,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Worktree      *bool  `json:"worktree,omitempty"`
	Branch        string `json:"branch,omitempty"`
	// Attachments are files referenced by the task (paths on Machine).
	Attachments []string `json:"attachments,omitempty"`
	// After is the wall tile the draft sits right of (an agent or draft
	// id); empty: the end of its machine's tiles.
	After string `json:"after,omitempty"`
	// Parked: left with Esc, a quiet tile (the shelf) until edited again.
	Parked  bool      `json:"parked,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// DraftSaveParams are drafts.save's: the whole draft (created or replaced).
type DraftSaveParams struct {
	Draft Draft `json:"draft"`
}

// Notification methods for drafts, sent on agents.subscribe connections
// after the agents (first a drafts.changed for every draft).
const (
	NoteDraftChanged = "drafts.changed"
	NoteDraftRemoved = "drafts.removed"
)

// DraftChanged is drafts.changed's params; drafts.removed's is Removed.
type DraftChanged struct {
	Draft Draft `json:"draft"`
}
