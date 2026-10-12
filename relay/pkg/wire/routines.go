package wire

import "time"

// Routines (internal/routines): a routine is a saved agents.spawn that
// hesperd starts on a cadence. Each run is an ordinary agent of this
// daemon (Agent.Routine names its routine), hidden in the background
// unless the routine says otherwise, closed by hesperd once it settles.
// Its session stays in the shared history, its diff in review. The
// daemon of the machine named in Machine owns the routine and its clock;
// routines.* on the local socket reach it (hesperctl routine).

// Routine is one saved, recurring spawn.
type Routine struct {
	// ID is "r-" and 1–40 of [a-z0-9-]; routines.save with an empty ID
	// gets one.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Machine is the daemon that owns the routine and runs it; empty:
	// this Mac.
	Machine string `json:"machine,omitempty"`
	// Spawn is what each run starts with. Its Machine, Name, Caller,
	// Parent, Depth, Bring and Draft are the daemon's: it names the run
	// after the routine and starts it on Machine. Project must be an
	// absolute path on Machine.
	Spawn SpawnParams `json:"spawn"`
	// Cadence: exactly one of Every and At.
	//
	// Every is a Go duration ("15m", "1h", "24h"; at least 1m, at most
	// 168h): the routine runs at local midnight + Offset + k·Every, k ≥
	// 0, and starts over at the next midnight. Offset (default 0) is a
	// duration shorter than Every: Every "1h" with Offset "5m" runs at
	// :05 past every hour.
	//
	// At lists local wall-clock times "HH:MM" the routine runs at daily.
	Every  string    `json:"every,omitempty"`
	Offset string    `json:"offset,omitempty"`
	At     []string  `json:"at,omitempty"`
	Policy RunPolicy `json:"policy"`
	// Paused: the clock does not run it (routines.pause); routines.run
	// still does. NextRun is zero while paused.
	Paused bool `json:"paused,omitempty"`
	// CreatedBy says who saved the routine ("local" for the local
	// socket; later a device's name for a signed request).
	CreatedBy string    `json:"createdBy,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
	// Runs counts every run begun so far (the next run's Seq is Runs+1).
	Runs int `json:"runs,omitempty"`
	// LastRun is the latest run (running or ended); the full list is
	// routines.runs.
	LastRun *RoutineRun `json:"lastRun,omitempty"`
	// NextRun is when the clock starts the next run (UTC; zero when
	// paused). The daemon computes it.
	NextRun time.Time `json:"nextRun,omitzero"`
}

// RunPolicy says how a routine's runs behave. Zero values are the
// defaults named here; routines.save fills them in.
type RunPolicy struct {
	// Timeout ends a run still working after this long (default "1h";
	// at least 1m, at most 24h): the agent is closed (its interrupt,
	// then the hangup) and the run's outcome is "timed-out".
	Timeout string `json:"timeout,omitempty"`
	// Overlap: what the clock does when the previous run is still
	// live. "skip" (default) records a skipped run; "replace" closes the
	// live run first.
	Overlap string `json:"overlap,omitempty"`
	// CatchUp: what happens when the clock fires late (the Mac slept,
	// hesperd was down). "once" (default) runs once now and resumes the
	// cadence; "none" skips the missed runs when the clock is more than
	// one cadence late (At: more than an hour).
	CatchUp string `json:"catchUp,omitempty"`
	// Foreground shows the run's tile on the walls like any agent and
	// leaves it there when it settles. Default: a background agent, hidden
	// until it needs someone, closed when it finishes (closing agents).
	Foreground bool `json:"foreground,omitempty"`
	// KeepRuns bounds the run records kept (default 200, at most 1000).
	KeepRuns int `json:"keepRuns,omitempty"`
}

// RunPolicy values.
const (
	OverlapSkip    = "skip"
	OverlapReplace = "replace"
	CatchUpOnce    = "once"
	CatchUpNone    = "none"
)

// RoutineRun is one run of a routine.
type RoutineRun struct {
	// Seq numbers the routine's runs from 1.
	Seq     int       `json:"seq"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	// Agent is the run's agent id (empty for a skipped or failed run);
	// Session its Claude or Codex session (shared history), once known.
	Agent   string `json:"agent,omitempty"`
	Session string `json:"session,omitempty"`
	// Outcome: RunRunning while the agent runs, then one of the Run*
	// values below.
	Outcome string `json:"outcome"`
	// State is the agent's state when the run ended (done, idle, exited,
	// approval, question, error).
	State string `json:"state,omitempty"`
	// Message is the agent's final message (agents.result) when it ended
	// its turn; Error says why a run failed, timed out or was skipped.
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
	// Manual: started by routines.run, not the clock.
	Manual bool `json:"manual,omitempty"`
}

// RoutineRun outcomes.
const (
	RunRunning  = "running"
	RunDone     = "done"      // the agent ended its turn (done or idle)
	RunNeedsYou = "needs-you" // it stopped at an approval or question
	RunExited   = "exited"    // its process ended on its own
	RunError    = "error"     // it ended in error
	RunTimedOut = "timed-out" // closed by the policy's timeout
	RunSkipped  = "skipped"   // not started: the previous run was live, or the clock was too late
	RunFailed   = "failed"    // agents.spawn failed (Error says why)
)

// RoutineSaveParams are routines.save's: the whole routine (created or
// replaced; Created, Runs, LastRun and NextRun are the daemon's).
type RoutineSaveParams struct {
	Routine Routine `json:"routine"`
}

// RoutinePauseParams are routines.pause's.
type RoutinePauseParams struct {
	ID     string `json:"id"`
	Paused bool   `json:"paused"`
}

// RoutineRunsParams are routines.runs's; the result is the runs, newest
// first, at most Limit (default 50).
type RoutineRunsParams struct {
	ID    string `json:"id"`
	Limit int    `json:"limit,omitempty"`
}

// Notification methods for routines, sent on agents.subscribe connections
// (first a routines.changed for every routine). A run beginning or ending
// is a routines.changed (LastRun, NextRun).
const (
	NoteRoutineChanged = "routines.changed"
	NoteRoutineRemoved = "routines.removed"
)

// RoutineChanged is routines.changed's params; routines.removed's is
// Removed.
type RoutineChanged struct {
	Routine Routine `json:"routine"`
}
