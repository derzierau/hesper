package agents

import (
	"context"
	"fmt"
	"time"

	"github.com/derzierau/hesper/relay/internal/routines"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Routine runs (docs/rebuild-contract.md, "As built — routines"): the
// registry is the scheduler's Runner. A run is an ordinary agent spawned
// from the routine's spawn (Agent.Routine, Agent.RoutineRun), in the
// background unless the policy says otherwise. Its end is seen where the
// agent changes, under the registry's lock: setState when it settles the
// first time, removeLocked when it leaves before that; a run ends exactly
// once. The policy's timeout closes a run still working.

// routineRun is one run on its agent (agent.run), from the spawn until it
// ended.
type routineRun struct {
	seq     int
	started time.Time
	timeout time.Duration
	timer   *time.Timer // the timeout; nil once the run ended
	ended   bool
	// end takes the finished run (Agent, Session, Ended, Outcome, State,
	// Message, Error), once, outside the lock.
	end func(wire.RoutineRun)
}

// routineRunner is routines.Runner on the registry.
type routineRunner struct{ r *Registry }

// routineTimeout is a policy's timeout as a duration (tests shorten it).
var routineTimeout = func(p wire.RunPolicy) time.Duration {
	d, err := time.ParseDuration(p.Timeout)
	if err != nil || d <= 0 {
		return routines.DefaultTimeout
	}
	return d
}

// resumeMinTimeout is the least a run re-armed after a daemon restart
// gets before its timeout.
const resumeMinTimeout = 10 * time.Second

// routineSpawn is the spawn of run of r: r.Spawn on this Mac, named after
// the routine and the run's local start time; the fields that are the
// daemon's cleared.
func routineSpawn(r wire.Routine, run wire.RoutineRun, loc *time.Location) wire.SpawnParams {
	p := r.Spawn
	p.Machine, p.Name = "", r.Name+" · "+run.Started.In(loc).Format("15:04")
	p.Caller, p.Parent, p.Depth, p.Bring, p.Draft = "", "", 0, nil, ""
	return p
}

// Start spawns the run's agent as hesperctl new would (worktree, trust,
// scratch, profile), marked as the run before its process starts so its
// environment and its end hook are in place.
func (rr routineRunner) Start(ctx context.Context, r wire.Routine, run wire.RoutineRun) (string, <-chan wire.RoutineRun, error) {
	ch := make(chan wire.RoutineRun, 1)
	rn := &routineRun{seq: run.Seq, started: run.Started, timeout: routineTimeout(r.Policy), end: func(ended wire.RoutineRun) { ch <- ended }}
	a, err := rr.r.spawn(routineSpawn(r, run, rr.r.routines.Loc()), func(a *agent) {
		a.Routine, a.RoutineRun, a.run = r.ID, run.Seq, rn
		a.Background = !r.Policy.Foreground
	})
	if err != nil {
		return "", nil, err
	}
	_, local := rr.r.split(a.ID)
	rr.r.armRoutineTimeout(local, rn, rn.timeout)
	return a.ID, ch, nil
}

// Live: a run of routine id has an agent that has not settled.
func (rr routineRunner) Live(id string) bool {
	rr.r.mu.Lock()
	defer rr.r.mu.Unlock()
	return rr.r.liveRun(id) != nil
}

// Stop closes the live run's agent (Overlap "replace").
func (rr routineRunner) Stop(ctx context.Context, id string) error {
	rr.r.mu.Lock()
	a := rr.r.liveRun(id)
	aid := ""
	if a != nil {
		aid = a.ID
	}
	rr.r.mu.Unlock()
	if aid == "" {
		return nil
	}
	if _, err := rr.r.CloseAs(aid, wire.ReasonClosed, ""); err != nil {
		return fmt.Errorf("closing %s: %w", aid, err)
	}
	return nil
}

// liveRun is the agent of routine id's live run (the lock is held): one
// that runs and has not settled.
func (r *Registry) liveRun(id string) *agent {
	for _, a := range r.agents {
		if a.Routine == id && runLive(a) {
			return a
		}
	}
	return nil
}

// runLive: the agent runs and has not settled (starting or working).
func runLive(a *agent) bool {
	return a.term != nil && a.Exit == nil && (a.State == wire.StateStarting || a.State == wire.StateWorking)
}

// armRoutineTimeout starts a run's timeout, unless it ended already.
func (r *Registry) armRoutineTimeout(local string, rn *routineRun, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.agents[local]; a == nil || a.run != rn || rn.ended {
		return
	}
	rn.timer = time.AfterFunc(d, func() { r.routineTimedOut(local, rn) })
}

// routineTimedOut ends a run the policy's timeout caught still running
// and closes its agent (CloseAs takes the lock itself).
func (r *Registry) routineTimedOut(local string, rn *routineRun) {
	r.mu.Lock()
	a := r.agents[local]
	if r.closing || a == nil || a.run != rn || rn.ended {
		r.mu.Unlock()
		return
	}
	r.endRoutineRun(a, wire.RunTimedOut, a.State, fmt.Sprintf("timed out after %s", rn.timeout))
	id, routine := a.ID, a.Routine
	r.mu.Unlock()
	if _, err := r.CloseAs(id, wire.ReasonClosed, ""); err != nil {
		r.opt.Logf("hesperd: routine %s: closing the timed-out run %s: %v", routine, id, err)
	}
}

// routineSettled sees a run's agent change state (the lock is held; the
// new state is set): the first settled state ends the run. Done or idle
// counts only as a turn's end (turnEnded: a resume coming up idle is
// none); an approval, a question, an error or the exit always. An agent
// being closed (agents.close, Stop) ends its run when it leaves
// (removeLocked), whatever its interrupt makes of its state.
func (r *Registry) routineSettled(a *agent, from, to string) {
	if a.run == nil || a.run.ended || a.closeReason != "" {
		return
	}
	switch to {
	case wire.StateDone, wire.StateIdle:
		if turnEnded(from, to) {
			r.endRoutineRun(a, wire.RunDone, to, "")
		}
	case wire.StateApproval, wire.StateQuestion:
		// It needs someone: back on the walls and in the inbox (the
		// change goes out with this state's).
		a.Background = false
		r.endRoutineRun(a, wire.RunNeedsYou, to, "")
	case wire.StateError:
		r.endRoutineRun(a, wire.RunError, to, attentionText(a.Attention))
	case wire.StateExited:
		r.endRoutineRun(a, wire.RunExited, to, "")
	}
}

// attentionText is an attention's title and detail on one line.
func attentionText(att *wire.Attention) string {
	if att == nil {
		return ""
	}
	if att.Detail == "" {
		return att.Title
	}
	return att.Title + ": " + att.Detail
}

// endRoutineRun ends an agent's run (the lock is held) and hands the
// record to its end outside the lock. The final message of a turn that
// ended arrives with the same hook after the state (Hook: hookEvent, then
// setLastMessage), so a done run reads it under the next lock.
func (r *Registry) endRoutineRun(a *agent, outcome, state, errText string) {
	rn := a.run
	rn.ended = true
	if rn.timer != nil {
		rn.timer.Stop()
		rn.timer = nil
	}
	run := wire.RoutineRun{Seq: rn.seq, Started: rn.started, Agent: a.ID, Session: a.SessionID, Ended: time.Now().UTC(), Outcome: outcome, State: state, Error: errText}
	go func() {
		if outcome == wire.RunDone {
			r.mu.Lock()
			run.Message, run.Session = a.lastMessage, a.SessionID
			r.mu.Unlock()
		}
		rn.end(run)
	}()
}

// resumeRoutineRuns picks up the runs that were running when the daemon
// stopped (Open, after restore; every running record, not only LastRun:
// a skipped run may have followed a live one): a run whose agent came
// back live is re-armed with what is left of its timeout; one whose agent
// is gone or settled is ended in the store (the scheduler was not around).
func (r *Registry) resumeRoutineRuns() {
	for _, rt := range r.routines.List() {
		for _, run := range r.routines.Running(rt.ID) {
			ended, live := r.resumeRoutineRun(rt, run)
			if live {
				continue
			}
			if err := r.routines.EndRun(rt.ID, ended); err != nil {
				r.opt.Logf("hesperd: routine %s: recording run %d after the restart: %v", rt.ID, ended.Seq, err)
			}
		}
	}
}

// resumeRoutineRun re-arms one run (live is true), or says how it ended.
func (r *Registry) resumeRoutineRun(rt wire.Routine, run wire.RoutineRun) (ended wire.RoutineRun, live bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var a *agent
	for _, b := range r.agents {
		if b.Routine == rt.ID && b.RoutineRun == run.Seq {
			a = b
			break
		}
	}
	run.Ended = time.Now().UTC()
	if a == nil {
		run.Outcome, run.State, run.Error = wire.RunExited, wire.StateExited, "hesperd restarted"
		return run, false
	}
	run.Agent, run.Session = a.ID, a.SessionID
	if !runLive(a) {
		run.State = a.State
		switch a.State {
		case wire.StateDone, wire.StateIdle:
			run.Outcome, run.Message = wire.RunDone, a.lastMessage
		case wire.StateApproval, wire.StateQuestion:
			run.Outcome = wire.RunNeedsYou
		case wire.StateError:
			run.Outcome, run.Error = wire.RunError, attentionText(a.Attention)
		default:
			run.Outcome = wire.RunExited
		}
		return run, false
	}
	id := rt.ID
	rn := &routineRun{seq: run.Seq, started: run.Started, timeout: routineTimeout(rt.Policy)}
	rn.end = func(ended wire.RoutineRun) {
		if err := r.routines.EndRun(id, ended); err != nil {
			r.opt.Logf("hesperd: routine %s: recording run %d: %v", id, ended.Seq, err)
		}
	}
	a.run = rn
	local := a.local
	rn.timer = time.AfterFunc(max(rn.timeout-time.Since(run.Started), resumeMinTimeout), func() { r.routineTimedOut(local, rn) })
	return run, true
}
