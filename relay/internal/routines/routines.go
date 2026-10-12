// Package routines keeps hesperd's routines (wire.Routine: a saved spawn
// run on a cadence) and runs their clock. The Store holds them
// (routines.json and the run records, atomic writes, subscriptions, as
// drafts); the Scheduler starts a run when one is due and records how it
// ended; a Runner, given by internal/agents, is what turns a due routine
// into an agent of the registry. Everything here is pure: tests drive it
// with a fake clock and a fake Runner and never start an agent.
package routines

import (
	"context"
	"regexp"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Limits and defaults (wire.Routine, wire.RunPolicy).
const (
	// RoutineLimit bounds the routines a daemon keeps.
	RoutineLimit = 100
	// TaskLimit bounds Spawn.Task.
	TaskLimit = 256 << 10

	MinEvery = time.Minute
	MaxEvery = 7 * 24 * time.Hour

	DefaultTimeout = time.Hour
	MinTimeout     = time.Minute
	MaxTimeout     = 24 * time.Hour

	DefaultKeepRuns = 200
	MaxKeepRuns     = 1000

	// LateAt is how late the clock may fire an At cadence before CatchUp
	// "none" skips the run.
	LateAt = time.Hour
)

// ID is the shape of a routine id.
var ID = regexp.MustCompile(`^r-[a-z0-9-]{1,40}$`)

// Runner starts a routine's runs as agents and reports how they end.
// internal/agents implements it on the registry.
type Runner interface {
	// Start spawns run.Seq of r: the agent named after the routine, in
	// the background unless r.Policy.Foreground, with HESPER_ROUTINE_ID
	// and HESPER_ROUTINE_RUN in its environment. It returns the agent's
	// id once the agent exists; the error is agents.spawn's. The channel
	// yields exactly one value when the run ended: run with Agent,
	// Session, Ended, Outcome, State, Message (and Error) filled in. The
	// Runner enforces r.Policy.Timeout itself (outcome RunTimedOut).
	Start(ctx context.Context, r wire.Routine, run wire.RoutineRun) (agent string, done <-chan wire.RoutineRun, err error)
	// Live reports whether a run of routine id is still live (its agent
	// exists and has not settled).
	Live(id string) bool
	// Stop closes the live run of routine id (Overlap "replace"); no live
	// run is not an error.
	Stop(ctx context.Context, id string) error
}
