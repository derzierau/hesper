package wire

// Bring the folder along (docs/rebuild-contract.md, "As built — bring
// the folder"): agents.spawn on a machine that lacks the folder brings it
// from another machine first (params.bring), telling subscribers the
// progress (agents.bringing).

// Bring is agents.spawn's params.bring: the folder at Path on machine
// From (its short name; empty: this Mac) is made on the spawn's machine
// first, then the agent starts there. Changes is "with" (the default:
// uncommitted and untracked files come along) or "clean" (the last
// commit only; a folder that is not a Git repository always comes whole).
// Draft (optional) is the app's draft the spawn starts, echoed in
// agents.bringing.
type Bring struct {
	From    string `json:"from,omitempty"`
	Path    string `json:"path,omitempty"`
	Changes string `json:"changes,omitempty"`
	Draft   string `json:"draft,omitempty"`
}

// Bring changes (Bring.Changes).
const (
	BringWith  = "with"
	BringClean = "clean"
)

// Bringing is agents.bringing's params: a bring's progress. Step is
// "checkpoint" (the source packs the folder), "transfer" (Percent 0–100),
// "unpack" (the target makes the folder), "spawn" (Path: the folder
// there), then "done" (Agent: the new agent's id) or "failed" (Error).
// ID names the bring (one agents.spawn).
type Bringing struct {
	ID      string `json:"id"`
	Draft   string `json:"draft,omitempty"`
	Step    string `json:"step"`
	Percent int    `json:"percent,omitempty"`
	To      string `json:"to"`
	From    string `json:"from,omitempty"`
	Path    string `json:"path,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// Bring steps (Bringing.Step); it ends with MoveDone or MoveFailed.
const (
	BringCheckpoint = "checkpoint"
	BringTransfer   = "transfer"
	BringUnpack     = "unpack"
	BringSpawn      = "spawn"
)

// NoteBringing is the agents.subscribe notification of a bring's
// progress.
const NoteBringing = "agents.bringing"
