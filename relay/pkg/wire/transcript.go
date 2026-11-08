package wire

// agents.transcript and agents.transcriptStream: an agent's Claude Code
// or Codex transcript as parsed items, for a view that draws the
// conversation natively instead of the terminal (the phone).

// TranscriptItem is one item of a transcript. Seq is the byte offset of
// the item's line in the transcript file: monotonic, stable across daemon
// restarts, what pages are cut at. At is the line's timestamp in Unix ms
// when it has one. Kind is one of the Item constants. Text is a message's
// text, or a result's clipped output (Truncated when clipped). Tool and
// Summary name a tool call and say in one short line what it works on;
// ID is the call's id, Of a result's call. OK, for results only, is false
// when the tool reported an error.
type TranscriptItem struct {
	Seq       int64  `json:"seq"`
	At        int64  `json:"at,omitempty"`
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Summary   string `json:"summary,omitempty"`
	ID        string `json:"id,omitempty"`
	Of        string `json:"of,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	OK        *bool  `json:"ok,omitempty"`
}

// TranscriptItem kinds.
const (
	ItemUser      = "user"
	ItemAssistant = "assistant"
	ItemTool      = "tool"
	ItemResult    = "result"
	ItemSystem    = "system"
)

// TranscriptParams are agents.transcript's: Limit items (0: the default)
// before the item at Before (0: the end of the transcript).
type TranscriptParams struct {
	ID     string `json:"id"`
	Before int64  `json:"before,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// TranscriptPage is agents.transcript's result: Items oldest first; More,
// there are older items before them.
type TranscriptPage struct {
	Items []TranscriptItem `json:"items"`
	More  bool             `json:"more"`
}

// TranscriptStreamParams are agents.transcriptStream's: the stream runs
// on channel Ch of the caller's link Link, like agents.attach. It sends
// the newest Limit items (0: the default) after the item at After (0:
// from the start), then every new item as the transcript grows.
type TranscriptStreamParams struct {
	Link  string `json:"link"`
	Ch    uint32 `json:"ch"`
	ID    string `json:"id"`
	After int64  `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// TranscriptDefaultLimit is a page's or a stream's first items when the
// params give no limit; TranscriptMaxLimit caps the limit.
const (
	TranscriptDefaultLimit = 200
	TranscriptMaxLimit     = 1000
)
