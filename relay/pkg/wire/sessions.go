package wire

// Shared history (internal/sessions): every Claude and Codex conversation
// of every Mac of the owner, indexed by the Mac it ran on (its home) and
// replicated to the others, searchable offline on each. Sessions are
// resumed, forked, moved and continued from here.

// CodeLive is the error code of sessions.resume / sessions.delete on a
// session that runs right now (data.agentId names hesperd's agent when it
// is one): the app opens that agent instead.
const CodeLive = "live"

// Session is one conversation of the shared history.
type Session struct {
	// ID is "<machine>:<kind>:<sessionId>" (machine: its home, as this Mac
	// names it).
	ID        string `json:"id"`
	Kind      string `json:"kind"` // claude | codex
	SessionID string `json:"sessionId"`
	// Machine is the session's home (owner): where its transcript is
	// written and where it resumes by default.
	Machine       string        `json:"machine"`
	Cwd           string        `json:"cwd"`
	ProjectID     string        `json:"projectId"`
	Branch        string        `json:"branch"`
	Title         string        `json:"title"`
	FirstPrompt   string        `json:"firstPrompt"`
	LastUser      string        `json:"lastUser"`
	LastAssistant string        `json:"lastAssistant"`
	Todos         []SessionTodo `json:"todos"`
	Turns         int           `json:"turns"`
	Tokens        int64         `json:"tokens,omitempty"`
	StartedAt     string        `json:"startedAt"`    // RFC 3339
	LastActivity  string        `json:"lastActivity"` // RFC 3339
	Live          *SessionLive  `json:"live,omitempty"`
	// External: not started by hesperd (a terminal, the desktop apps, an
	// IDE extension, codex exec).
	External bool `json:"external"`
	Archived bool `json:"archived"`
	// Mirrored lists the machines holding a copy of the transcript (the
	// home first).
	Mirrored []string `json:"mirrored"`
	// Snippet: the search hit with [ and ] around the matched words.
	Snippet string `json:"snippet,omitempty"`

	// Additive refinements (see the contract's "As built — shared
	// history (data)"):
	// Origin is what wrote it: claude "cli" / "claude-desktop" /
	// "sdk-cli" …, codex "codex-tui" / "Codex Desktop" / "codex_exec" ….
	Origin string `json:"origin,omitempty"`
	// RemovedAt: the hesperd agent that ran it was removed then (the app
	// shows a ghost card for 24 h).
	RemovedAt string `json:"removedAt,omitempty"`
	// MovedTo: the session continued on that machine (this copy is
	// read-only history; sessions.resume goes there).
	MovedTo string `json:"movedTo,omitempty"`
	// Bytes is the transcript's size on its home.
	Bytes int64 `json:"bytes,omitempty"`
	// Checkpoint (move work): the last checkpoint of the hesperd agent
	// that ran it, on this Mac (its home's entries only).
	Checkpoint *Checkpoint `json:"checkpoint,omitempty"`
	// FolderRemoved (scratch projects): the session ran in a scratch
	// project whose folder hesperd deleted.
	FolderRemoved bool `json:"folderRemoved,omitempty"`
}

// SessionTodo is one item of the last todo list / plan.
type SessionTodo struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// SessionLive: the session runs now, in hesperd's agent AgentID, or in
// another process (External).
type SessionLive struct {
	AgentID  string `json:"agentId,omitempty"`
	External bool   `json:"external"`
}

// SessionChanges is sessions.show's git state of the session's folder on
// its home.
type SessionChanges struct {
	Files          []SessionFile `json:"files"`
	Uncommitted    bool          `json:"uncommitted"`
	Ahead          *int          `json:"ahead,omitempty"`
	Behind         *int          `json:"behind,omitempty"`
	WorktreeExists bool          `json:"worktreeExists"`
}

// SessionFile is one changed file (against the branch's upstream or main,
// plus uncommitted changes).
type SessionFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// SessionDetail is sessions.show's result.
type SessionDetail struct {
	Session
	Changes *SessionChanges `json:"changes,omitempty"`
}

// SessionSearchParams are sessions.search's.
type SessionSearchParams struct {
	Query     string   `json:"query,omitempty"`
	ProjectID string   `json:"projectId,omitempty"`
	Kinds     []string `json:"kinds,omitempty"`
	Machines  []string `json:"machines,omitempty"`
	Since     string   `json:"since,omitempty"` // RFC 3339: lastActivity at or after
	Live      *bool    `json:"live,omitempty"`
	External  *bool    `json:"external,omitempty"`
	// Archived: omitted or false lists the others, true only archived.
	Archived *bool  `json:"archived,omitempty"`
	Limit    int    `json:"limit,omitempty"` // default and at most 50
	Cursor   string `json:"cursor,omitempty"`
	// Moved (additive): include sessions continued elsewhere (MovedTo).
	Moved bool `json:"moved,omitempty"`
}

// SessionSearchResult is sessions.search's result.
type SessionSearchResult struct {
	Items  []Session `json:"items"`
	Cursor string    `json:"cursor,omitempty"`
}

// SessionIDParams name one session.
type SessionIDParams struct {
	ID string `json:"id"`
}

// SessionResumeParams are sessions.resume's and sessions.fork's.
type SessionResumeParams struct {
	ID      string `json:"id"`
	Machine string `json:"machine,omitempty"`
}

// SessionContinueParams are sessions.continueAs's.
type SessionContinueParams struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Machine string `json:"machine,omitempty"`
}

// SessionArchiveParams are sessions.archive's.
type SessionArchiveParams struct {
	ID       string `json:"id"`
	Archived bool   `json:"archived"`
}

// SessionDeleteParams are sessions.delete's; Undo (additive) takes a
// delete back within its 30 s.
type SessionDeleteParams struct {
	ID   string `json:"id"`
	Undo bool   `json:"undo,omitempty"`
}

// SessionBrief is sessions.brief's result.
type SessionBrief struct {
	Text string `json:"text"`
}

// SessionStats is sessions.stats's result.
type SessionStats struct {
	Count       int             `json:"count"`
	ByKind      map[string]int  `json:"byKind"`
	ByMachine   map[string]int  `json:"byMachine"`
	IndexBytes  int64           `json:"indexBytes"`
	MirrorBytes int64           `json:"mirrorBytes"`
	Indexing    SessionIndexing `json:"indexing"`
}

// SessionIndexing is the scanner's progress (sessions.indexing too).
type SessionIndexing struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// Notifications on agents.subscribe connections.
const (
	NoteSessionChanged  = "sessions.changed"
	NoteSessionRemoved  = "sessions.removed" // additive: {id}, a deleted session
	NoteSessionIndexing = "sessions.indexing"
)

// SessionChanged is sessions.changed's params.
type SessionChanged struct {
	Session Session `json:"session"`
}
