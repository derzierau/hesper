package wire

import "time"

// Review (docs/review/concept.md; docs/rebuild-contract.md "As built —
// review"): the finished work of every agent on every Mac, ready to be
// read, accepted, rejected or sent back. Computed on the agent's Mac
// (internal/review), routed there like the other agents.* calls.

// Review risks and evidence freshness.
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"

	EvidenceFresh   = "fresh"   // a test or build ended successfully after the last edit
	EvidenceStale   = "stale"   // edits after the last successful test or build
	EvidenceMissing = "missing" // no test or build ran
	EvidenceNone    = "none"    // no changes
)

// File statuses of a review diff.
const (
	ReviewAdded    = "A"
	ReviewModified = "M"
	ReviewDeleted  = "D"
	ReviewRenamed  = "R"
)

// NoteReviewChanged (agents.subscribe) tells that an agent became ready
// for review or its review changed (accepted, rejected, sent back): the
// app fetches review.list again.
const NoteReviewChanged = "review.changed"

// ReviewChanged is review.changed's params.
type ReviewChanged struct {
	ID string `json:"id"`
}

// ReviewItem is one entry of review.list: a settled agent (done, idle,
// exited) whose folder has changes against its review base.
type ReviewItem struct {
	ID        string   `json:"id"`
	Machine   string   `json:"machine"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Project   string   `json:"project"`
	Branch    string   `json:"branch,omitempty"`
	Worktree  string   `json:"worktree,omitempty"`
	State     string   `json:"state"`
	Files     int      `json:"files"`
	Added     int      `json:"added"`
	Removed   int      `json:"removed"`
	Risk      string   `json:"risk"`
	RiskNotes []string `json:"riskNotes"`
	Evidence  string   `json:"evidence"`
	// ReadyAt is when the agent settled; ReviewedAt when review.sendBack
	// last recorded a reviewed point.
	ReadyAt    time.Time `json:"readyAt"`
	ReviewedAt time.Time `json:"reviewedAt,omitzero"`
	// Base is the review base commit (additive).
	Base string `json:"base,omitempty"`
}

// ReviewDiffParams are review.diff's: Context lines around changes
// (default 3, at most MaxReviewContext).
type ReviewDiffParams struct {
	ID      string `json:"id"`
	Context *int   `json:"context,omitempty"`
}

// DefaultReviewContext and MaxReviewContext bound ReviewDiffParams.Context.
const (
	DefaultReviewContext = 3
	MaxReviewContext     = 1000
)

// ReviewDiff is review.diff's: the agent's folder (Head "worktree": its
// files now, untracked ones included, ignored ones not) against Base.
// Files come in reading order. Tree (additive) names the folder's state
// the diff shows; review.accept and review.reject take it back to refuse
// a folder that changed since.
type ReviewDiff struct {
	Base  string       `json:"base"`
	Head  string       `json:"head"`
	Tree  string       `json:"tree"`
	Files []ReviewFile `json:"files"`
}

// ReviewFile is one changed file. Order is its place in the reading
// order (its index in ReviewDiff.Files). Added and Removed count its
// lines (additive). TooLarge (additive): its hunks are left out (over
// MaxReviewFileLines diff lines); it is accepted or rejected whole.
type ReviewFile struct {
	Path           string       `json:"path"`
	OldPath        string       `json:"oldPath,omitempty"`
	Status         string       `json:"status"`
	Binary         bool         `json:"binary,omitempty"`
	FormattingOnly bool         `json:"formattingOnly,omitempty"`
	Generated      bool         `json:"generated,omitempty"`
	TooLarge       bool         `json:"tooLarge,omitempty"`
	Order          int          `json:"order"`
	Risk           string       `json:"risk"`
	Added          int          `json:"added"`
	Removed        int          `json:"removed"`
	Hunks          []ReviewHunk `json:"hunks"`
}

// MaxReviewFileLines caps the diff lines of one file a review diff
// carries.
const MaxReviewFileLines = 20000

// ReviewHunk is one hunk: ID is "<file index>:<hunk index>".
type ReviewHunk struct {
	ID             string       `json:"id"`
	OldStart       int          `json:"oldStart"`
	OldLines       int          `json:"oldLines"`
	NewStart       int          `json:"newStart"`
	NewLines       int          `json:"newLines"`
	FormattingOnly bool         `json:"formattingOnly,omitempty"`
	Moved          bool         `json:"moved,omitempty"`
	Lines          []ReviewLine `json:"lines"`
}

// ReviewLine is one line of a hunk: Kind " " (context), "+" or "-"; Old
// and New its line numbers (1-based; the side it is not on omitted);
// Words the changed ranges of a changed line, [start, end) in UTF-16
// code units of Text. NoNewline (additive): the line ends its side
// without a newline.
type ReviewLine struct {
	Kind      string   `json:"kind"`
	Text      string   `json:"text"`
	Old       int      `json:"old,omitempty"`
	New       int      `json:"new,omitempty"`
	Words     [][2]int `json:"words,omitempty"`
	NoNewline bool     `json:"noNewline,omitempty"`
}
