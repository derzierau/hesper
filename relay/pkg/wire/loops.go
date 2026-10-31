package wire

import "time"

// Loops, goals and milestones (internal/loops). A loop is a lasting record
// of one piece of work: the agents, sessions, files, URLs and other loops
// that worked on it, its loose ends and steps, and who acts next. It
// outlives every agent in it: closing, tidying or moving an agent never
// closes its loop; only a person does (done or dropped). Goals and
// milestones belong to a product: a project or a group (Scope); loops are
// filed under a milestone. All of it is shared between the owner's Macs
// like projects are (per-field last-writer-wins registers, synced over
// the encrypted link; the relay sees none of it).

// Loop statuses (Loop.Status) and milestone/goal statuses (MilestoneProposed
// only for milestones a planning session wrote and a person has not
// accepted yet, goals.accept).
const (
	LoopOpen          = "open"
	LoopParked        = "parked"
	LoopDone          = "done"
	LoopDropped       = "dropped"
	MilestoneProposed = "proposed"
)

// Who acts next on a loop (Loop.Next), computed by hesperd when it builds
// a loop's view: parked or closed (done, dropped) first; NextYou when an
// item is open for the person, a member waits in approval or question, or
// a quiet member's ending is EndingAsks or EndingYourStep; NextAgents when
// a member works; NextOthers when an item is open for others or the loop
// is blocked by an open loop; otherwise NextYou with LooksDone.
const (
	NextYou    = "you"
	NextAgents = "agents"
	NextOthers = "others"
	NextParked = "parked"
	NextClosed = "closed"
)

// Item owners (LoopItem.Owner).
const (
	OwnerYou    = "you"
	OwnerAgent  = "agent"
	OwnerOthers = "others"
)

// Item states (LoopItem.State).
const (
	ItemOpen    = "open"
	ItemDone    = "done"
	ItemDropped = "dropped"
)

// Item kinds (LoopItem.Kind).
const (
	ItemStep      = "step"
	ItemLooseEnd  = "looseEnd"
	ItemPromise   = "promise"
	ItemQuestion  = "question"
	ItemOffer     = "offer"
	ItemCondition = "condition" // a milestone's "done when" item
)

// Endings: how an agent's last turn ended (Agent.Ending, LoopLink.Ending),
// from a rule-based reading of its final message (internal/loops,
// endings.go): it asks the person something, names a step that is the
// person's, offers more work, waits on someone else, or is done.
const (
	EndingAsks     = "asks"
	EndingYourStep = "yourStep"
	EndingOffer    = "offer"
	EndingWaits    = "waits"
	EndingDone     = "done"
)

// Link kinds (LoopLink.Kind) and relations (LoopLink.Rel).
const (
	LinkAgent   = "agent"   // Ref: an agent ref, "<node>/<local id>"
	LinkSession = "session" // Ref: a session ref, "<sessions node>:<kind>:<session id>"
	LinkProject = "project" // Ref: a project id
	LinkURL     = "url"     // Ref: an https URL (a PR, an artifact)
	LinkFile    = "file"    // Ref: "<machine>:<absolute path>" (a brief, a task file)
	LinkLoop    = "loop"    // Ref: another loop's id

	RelMember    = "member"    // an agent or session that worked on the loop
	RelStarted   = "started"   // From started Ref (the agent tree)
	RelContinued = "continued" // Ref continues From (resume, fork, continue-as, move)
	RelHandoff   = "handoff"   // From handed the work to Ref through this file
	RelBlocks    = "blocks"    // this loop blocks the loop Ref
	RelBlockedBy = "blockedBy" // this loop waits for the loop Ref
	RelTouches   = "touches"   // the loop's work touches the project Ref
)

// Service agents (Agent.Service): agents hesperd runs itself. They are
// never put in a loop, never closed by tidy or by finishing in the
// background, and their own writes never wake the Steward.
const (
	ServiceSteward = "steward"
	ServicePlanner = "planner"
)

// Steward levels (StewardConfig.Level): what the Steward may do by
// itself. Watch keeps the records; suggest (default) also saves drafts;
// act also starts ready drafts and merges or splits loops.
const (
	StewardWatch   = "watch"
	StewardSuggest = "suggest"
	StewardAct     = "act"
)

// Loop is one loop as loops.list, loops.get and loops.changed report it.
type Loop struct {
	ID       string `json:"id"` // "lp-" + 12 hex
	Title    string `json:"title"`
	Goal     string `json:"goal,omitempty"`
	DoneWhen string `json:"doneWhen,omitempty"`
	Status   string `json:"status"`
	// Next is who acts next (Next* constants) and NextText what: the
	// person's open item, the agent's question, "Looks done" (LooksDone).
	Next      string `json:"next"`
	NextText  string `json:"nextText,omitempty"`
	LooksDone bool   `json:"looksDone,omitempty"`
	// Milestone is the milestone the loop is filed under ("" unfiled).
	Milestone   string    `json:"milestone,omitempty"`
	ParkedUntil time.Time `json:"parkedUntil,omitzero"`
	// MergedInto: the loop was merged into that one (loops.merge); a
	// merged loop is listed only by loops.get.
	MergedInto string     `json:"mergedInto,omitempty"`
	Items      []LoopItem `json:"items"`
	Links      []LoopLink `json:"links"`
	Counts     LoopCounts `json:"counts"`
	// Projects are the projects the loop's members work in (from its
	// touches links and its members' projects).
	Projects []string  `json:"projects,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Closed   time.Time `json:"closed,omitzero"`
}

// LoopCounts summarize a loop for boards and chips.
type LoopCounts struct {
	Agents     int `json:"agents"`  // live member agents
	Working    int `json:"working"` // of them working or starting
	OpenYou    int `json:"openYou"` // open items owned by the person
	OpenAgents int `json:"openAgents"`
	OpenOthers int `json:"openOthers"`
	Sessions   int `json:"sessions"` // member sessions, live or not
}

// LoopItem is a loose end, step, promise, question or offer of a loop, or
// a "done when" condition of a milestone.
type LoopItem struct {
	ID    string `json:"id"` // "it-" + 8 hex
	Text  string `json:"text"`
	Owner string `json:"owner"`
	State string `json:"state"`
	Kind  string `json:"kind"`
	// Order sorts a loop's items (fractional, so an insert between two
	// changes one item).
	Order float64 `json:"order"`
	// After are the items that must be done first; Ready (computed): every
	// one of them is done.
	After []string `json:"after,omitempty"`
	Ready bool     `json:"ready,omitempty"`
	// Draft is a draft (drafts.*) that does this step once it is ready.
	Draft string `json:"draft,omitempty"`
	// Source is the link (an agent or session ref) the item came from.
	Source string `json:"source,omitempty"`
	// Check is something the Steward can check to tick the item off.
	Check   *ItemCheck `json:"check,omitempty"`
	Created time.Time  `json:"created"`
	Done    time.Time  `json:"done,omitzero"`
}

// ItemCheck kinds.
const (
	CheckPR     = "pr"     // Ref: a pull request URL; done when merged
	CheckTag    = "tag"    // Ref: "<repository URL>@<tag>"; done when pushed
	CheckURL    = "url"    // Ref: a URL; done when it answers 2xx
	CheckReview = "review" // Ref: an agent ref; done when its review is accepted
	CheckLoop   = "loop"   // Ref: a loop id; done when that loop is done
)

// ItemCheck says how an item can be ticked off without a person.
type ItemCheck struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// LoopLink is one thing a loop links: an agent, a session, a project, a
// URL, a file or another loop, and how it relates. Only settled facts are
// stored (what the agent was called, how its last turn ended, how it was
// closed); live state comes from the agent list (Agent, State).
type LoopLink struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Rel  string `json:"rel"`
	From string `json:"from,omitempty"`
	// Label is a person-facing name: the agent's name, a PR title.
	Label string `json:"label,omitempty"`
	// Machine (agents, sessions) is the machine's short name as this Mac
	// calls it; Agent its agent id here while it is live ("" once it is
	// gone); State its live state (agent links only, "" once gone).
	Machine string `json:"machine,omitempty"`
	Agent   string `json:"agent,omitempty"`
	State   string `json:"state,omitempty"`
	// Ending and Summary are from the member's last settled turn.
	Ending  string `json:"ending,omitempty"`
	Summary string `json:"summary,omitempty"`
	// ClosedReason (agents.removed's reason), Closed and Result (the
	// first KiB of its final message) are set once the agent is gone.
	ClosedReason string    `json:"closedReason,omitempty"`
	Closed       time.Time `json:"closed,omitzero"`
	Result       string    `json:"result,omitempty"`
	Added        time.Time `json:"added"`
}

// Goal is an outcome a product works toward.
type Goal struct {
	ID     string  `json:"id"` // "gl-" + 12 hex
	Scope  string  `json:"scope"`
	Title  string  `json:"title"`
	Order  float64 `json:"order"`
	Status string  `json:"status"`
}

// Milestone is a release or result of a product, with "done when"
// conditions (Items, kind ItemCondition), an optional due date and the
// loops filed under it.
type Milestone struct {
	ID     string    `json:"id"` // "ms-" + 12 hex
	Scope  string    `json:"scope"`
	Goal   string    `json:"goal,omitempty"`
	Title  string    `json:"title"`
	Status string    `json:"status"`
	Order  float64   `json:"order"`
	Due    time.Time `json:"due,omitzero"`
	// AutoFile: loops the Steward matches to this milestone are filed
	// without asking.
	AutoFile bool       `json:"autoFile,omitempty"`
	Items    []LoopItem `json:"items"`
	// Computed: the loops filed under it, conditions done of total, and
	// Stalled (nothing changed in LoopSettings.StallDays).
	Loops    []string  `json:"loops"`
	Done     int       `json:"done"`
	Total    int       `json:"total"`
	Stalled  bool      `json:"stalled,omitempty"`
	NextText string    `json:"nextText,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
}

// StewardConfig is the Steward's shared configuration (steward.status,
// steward.set): one record for all the owner's Macs, so exactly one Mac
// (Node) runs it.
type StewardConfig struct {
	Enabled bool   `json:"enabled"`
	Level   string `json:"level"`
	// Node is the projects-store node of the Mac that runs it; Machine
	// that Mac's short name as this Mac calls it.
	Node    string `json:"node,omitempty"`
	Machine string `json:"machine,omitempty"`
}

// StewardStatus is steward.status's result.
type StewardStatus struct {
	StewardConfig
	// Agent is the Steward's agent id while it runs here ("" elsewhere or
	// stopped); Queued the events waiting for it; Wakes and LastWake today's
	// wakes; Session its current session id.
	Agent    string    `json:"agent,omitempty"`
	Queued   int       `json:"queued"`
	Wakes    int       `json:"wakes"`
	LastWake time.Time `json:"lastWake,omitzero"`
	Session  string    `json:"session,omitempty"`
}

// StewardEvent is one event steward.inbox hands the Steward.
type StewardEvent struct {
	Kind string    `json:"kind"` // settled, closed, loopCreated, review, evening
	At   time.Time `json:"at"`
	// Agent and Loop it is about (either may be empty), Ending and Summary
	// for settled, Reason for closed.
	Agent   string `json:"agent,omitempty"`
	Loop    string `json:"loop,omitempty"`
	Ending  string `json:"ending,omitempty"`
	Summary string `json:"summary,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Steward event kinds.
const (
	StewardSettled     = "settled"
	StewardClosed      = "closed"
	StewardLoopCreated = "loopCreated"
	StewardReview      = "review"
	StewardEvening     = "evening"
)

// LoopSettings are the shared loop settings (loops.settings.get/set).
type LoopSettings struct {
	// LoopLimit: loops waiting on the person before the start check turns
	// red (a hint, never a block; default 5).
	LoopLimit int `json:"loopLimit"`
	// StallDays: a milestone with no change for this long is stalled
	// (default 7).
	StallDays int `json:"stallDays"`
	// EveningAt is the evening wrap-up time, "HH:MM" local (default
	// "18:00"); MaxWakesPerDay bounds the Steward's wakes (default 40).
	EveningAt      string `json:"eveningAt"`
	MaxWakesPerDay int    `json:"maxWakesPerDay"`
}

// Methods (local socket; agent callers per the agent tree's policy).
const (
	MethodLoopsList        = "loops.list"
	MethodLoopsGet         = "loops.get"
	MethodLoopsSave        = "loops.save"
	MethodLoopsLink        = "loops.link"
	MethodLoopsItem        = "loops.item"
	MethodLoopsMerge       = "loops.merge"
	MethodLoopsSplit       = "loops.split"
	MethodLoopsSettingsGet = "loops.settings.get"
	MethodLoopsSettingsSet = "loops.settings.set"
	MethodGoalsList        = "goals.list"
	MethodGoalsSave        = "goals.save"
	MethodGoalsRemove      = "goals.remove"
	MethodGoalsItem        = "goals.item"
	MethodGoalsFile        = "goals.file"
	MethodGoalsAccept      = "goals.accept"
	MethodStewardStatus    = "steward.status"
	MethodStewardSet       = "steward.set"
	MethodStewardInbox     = "steward.inbox"
	MethodStewardPlan      = "steward.plan"
)

// LoopsListParams filter loops.list (all empty: every open and parked
// loop; Status "all" includes done and dropped).
type LoopsListParams struct {
	Status    string `json:"status,omitempty"`
	Milestone string `json:"milestone,omitempty"`
	Project   string `json:"project,omitempty"`
	// Agent: only the loop of this agent (an id as agents.list names it).
	Agent string `json:"agent,omitempty"`
}

// LoopSaveParams create a loop (no ID) or change the given fields of one.
type LoopSaveParams struct {
	ID          string     `json:"id,omitempty"`
	Title       *string    `json:"title,omitempty"`
	Goal        *string    `json:"goal,omitempty"`
	DoneWhen    *string    `json:"doneWhen,omitempty"`
	Status      *string    `json:"status,omitempty"`
	NextText    *string    `json:"nextText,omitempty"`
	Milestone   *string    `json:"milestone,omitempty"`
	ParkedUntil *time.Time `json:"parkedUntil,omitempty"`
}

// LoopLinkParams add (or with Remove, remove) a link of loop ID. An agent
// link may name the agent by its id here (Agent); hesperd stores its ref.
type LoopLinkParams struct {
	ID     string   `json:"id"`
	Link   LoopLink `json:"link"`
	Remove bool     `json:"remove,omitempty"`
}

// LoopItemParams add (Item.ID empty), change or (Remove) remove an item of
// loop or milestone ID; only the item fields given change (Text, Owner,
// State, Kind, Order, After, Draft, Check).
type LoopItemParams struct {
	ID     string   `json:"id"`
	Item   LoopItem `json:"item"`
	Remove bool     `json:"remove,omitempty"`
}

// LoopMergeParams merge loop From into loop Into (links and items move;
// From gets MergedInto).
type LoopMergeParams struct {
	Into string `json:"into"`
	From string `json:"from"`
}

// LoopSplitParams move the link Ref (and, for an agent, its descendants)
// of loop ID into a new loop titled Title.
type LoopSplitParams struct {
	ID    string `json:"id"`
	Ref   string `json:"ref"`
	Title string `json:"title,omitempty"`
}

// GoalsListParams filter goals.list to one scope (a project or group id).
type GoalsListParams struct {
	Scope string `json:"scope,omitempty"`
}

// GoalsList is goals.list's result.
type GoalsList struct {
	Goals      []Goal      `json:"goals"`
	Milestones []Milestone `json:"milestones"`
}

// GoalSaveParams create (no ID) or change a goal or a milestone (exactly
// one of them); only the fields given change.
type GoalSaveParams struct {
	Goal      *GoalPatch      `json:"goal,omitempty"`
	Milestone *MilestonePatch `json:"milestone,omitempty"`
}

// GoalPatch is a goal create or change.
type GoalPatch struct {
	ID     string   `json:"id,omitempty"`
	Scope  *string  `json:"scope,omitempty"`
	Title  *string  `json:"title,omitempty"`
	Order  *float64 `json:"order,omitempty"`
	Status *string  `json:"status,omitempty"`
}

// MilestonePatch is a milestone create or change.
type MilestonePatch struct {
	ID       string     `json:"id,omitempty"`
	Scope    *string    `json:"scope,omitempty"`
	Goal     *string    `json:"goal,omitempty"`
	Title    *string    `json:"title,omitempty"`
	Status   *string    `json:"status,omitempty"`
	Order    *float64   `json:"order,omitempty"`
	Due      *time.Time `json:"due,omitempty"`
	AutoFile *bool      `json:"autoFile,omitempty"`
}

// GoalFileParams file loop Loop under Milestone (Remove: unfile it).
type GoalFileParams struct {
	Milestone string `json:"milestone"`
	Loop      string `json:"loop"`
	Remove    bool   `json:"remove,omitempty"`
}

// GoalAcceptParams accept every proposed milestone of Scope (or only IDs).
type GoalAcceptParams struct {
	Scope string   `json:"scope"`
	IDs   []string `json:"ids,omitempty"`
}

// StewardSetParams change the Steward's configuration; Machine moves it to
// that Mac.
type StewardSetParams struct {
	Enabled *bool   `json:"enabled,omitempty"`
	Level   *string `json:"level,omitempty"`
	Machine *string `json:"machine,omitempty"`
}

// StewardPlanParams start a planning session for Scope (a project or group
// id) with an optional first message.
type StewardPlanParams struct {
	Scope  string `json:"scope"`
	Prompt string `json:"prompt,omitempty"`
}

// Notifications on agents.subscribe connections (first a loops.changed for
// every open and parked loop, a goals.changed for every goal and
// milestone, and a steward.changed).
const (
	NoteLoopChanged    = "loops.changed"
	NoteLoopRemoved    = "loops.removed"
	NoteGoalChanged    = "goals.changed"
	NoteGoalRemoved    = "goals.removed"
	NoteStewardChanged = "steward.changed"
)

// LoopChanged is loops.changed's params; loops.removed's is Removed.
type LoopChanged struct {
	Loop Loop `json:"loop"`
}

// GoalChanged is goals.changed's params (exactly one of Goal, Milestone);
// goals.removed's is Removed.
type GoalChanged struct {
	Goal      *Goal      `json:"goal,omitempty"`
	Milestone *Milestone `json:"milestone,omitempty"`
}

// StewardChanged is steward.changed's params.
type StewardChanged struct {
	Steward StewardStatus `json:"steward"`
}
