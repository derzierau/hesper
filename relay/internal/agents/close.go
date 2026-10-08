package agents

import (
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Closing agents (docs/rebuild-contract.md, "As built — closing agents"):
// an agent is on a wall, in the background (running, hidden from the walls:
// Agent.Background), or closed (gone from the registry; its session stays
// in the history, resumable with sessions.resume).

// interrupts are the keys that interrupt a turn, per kind: Esc stops
// Claude Code's and Codex's turn (and declines an approval or question);
// a shell gets ^C.
var interrupts = map[string][]byte{
	wire.KindClaude: {0x1b},
	wire.KindCodex:  {0x1b},
	wire.KindShell:  {0x03},
}

// closeSettle: after the interrupt, the tool gets at least this long to
// write its transcript before it is ended.
var closeSettle = 200 * time.Millisecond

// CloseAgent closes an agent (agents.close). An ended one leaves the registry
// at once; a running one gets its tool's interrupt, up to CloseWait to
// leave working, then the hangup Stop gives (SIGHUP, SIGKILL after
// StopGrace), and leaves once its process ended. Either way subscribers
// get agents.removed with reason "closed". The result names the session
// to resume it with.
func (r *Registry) CloseAgent(id string) (wire.CloseResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(id)
	if err != nil {
		return wire.CloseResult{}, err
	}
	r.adoptCodexSession(a)
	res := wire.CloseResult{Session: a.SessionID}
	if a.closeReason != "" {
		return res, nil // being closed already
	}
	if a.term == nil || a.Exit != nil {
		r.removeLocked(a, wire.ReasonClosed)
		return res, nil
	}
	a.closeReason, a.respawn = wire.ReasonClosed, false
	interrupted := false
	switch a.State {
	case wire.StateWorking, wire.StateApproval, wire.StateQuestion:
		if key := interrupts[a.Kind]; key != nil && a.term.Input(key) == nil {
			interrupted = true
		}
	default:
		if a.Kind == wire.KindShell {
			// A shell's foreground command, if any (its state does not
			// tell).
			interrupted = a.term.Input(interrupts[wire.KindShell]) == nil
		}
	}
	r.scheduleSave()
	go r.endClosed(a.local, a.gen, interrupted)
	return res, nil
}

// endClosed ends a closed agent's process: once it left working (at most
// CloseWait), with the hangup agents.stop sends.
func (r *Registry) endClosed(local string, gen int, interrupted bool) {
	start := time.Now()
	for {
		r.mu.Lock()
		a := r.agents[local]
		if a == nil || a.gen != gen || a.term == nil || a.Exit != nil {
			r.mu.Unlock()
			return // ended (and removed) meanwhile
		}
		waited := time.Since(start)
		settled := !interrupted || waited >= closeSettle
		if settled && a.State != wire.StateWorking || waited >= r.opt.CloseWait {
			a.stopping, a.running = true, false
			a.term.Stop(r.opt.StopGrace)
			r.scheduleSave()
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
}

// Kill ends an agent now (agents.kill): SIGTERM, SIGKILL after KillGrace.
// It stays listed, exited, with Ended "killed" (until it runs again).
func (r *Registry) Kill(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(id)
	if err != nil {
		return err
	}
	if a.term == nil || a.Exit != nil {
		if a.respawn {
			// One that did not come back stays down now.
			a.respawn = false
			r.scheduleSave()
		}
		return nil
	}
	a.Ended = wire.EndedKilled
	a.stopping, a.running, a.respawn = true, false, false
	a.term.Kill(r.opt.KillGrace)
	r.changed(a)
	return nil
}

// SetBackground moves an agent to the background or back
// (agents.background). It never touches the process.
func (r *Registry) SetBackground(id string, background bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(id)
	if err != nil {
		return err
	}
	if a.Background == background {
		return nil
	}
	a.Background = background
	r.changed(a)
	return nil
}

// turnEnded: a state change that finishes an agent's work (a turn ended:
// from working, an approval or a question to done or idle). A start that
// comes up idle (a resume, a respawn) is not one.
func turnEnded(from, to string) bool {
	switch from {
	case wire.StateWorking, wire.StateApproval, wire.StateQuestion:
		return to == wire.StateDone || to == wire.StateIdle
	}
	return false
}

// finishedInBackground closes a background agent that finished (the lock
// is held): its process ends (graceful is moot), then it leaves with
// reason "finished-in-background".
func (r *Registry) finishedInBackground(a *agent) {
	if a.closeReason != "" || a.Ended != "" || r.agents[a.local] != a || a.term == nil || a.Exit != nil {
		return // an ended one: exited closes it
	}
	a.closeReason = wire.ReasonFinishedInBackground
	a.stopping, a.running, a.respawn = true, false, false
	a.term.Stop(r.opt.StopGrace)
	r.scheduleSave()
}

// removeLocked forgets an agent and tells subscribers why (the lock is
// held).
func (r *Registry) removeLocked(a *agent, reason string) {
	delete(r.agents, a.local)
	if reason == wire.ReasonClosed || reason == wire.ReasonFinishedInBackground {
		r.adoptChildren(a)
	}
	for s := range r.subs {
		s.pushRemoved(a.ID, reason)
	}
	r.scheduleSave()
}

// adoptChildren gives the children of a closed agent to its parent (the
// lock is held): the grandparent still controls them (agent tree).
// Depths follow (a closed root's children become roots, depth 0);
// letParentAnswer is cleared, as the new parent never chose to answer
// for them. Only this machine's agents: a child on another machine keeps
// the closed parent. agents.remove does not re-parent: a move removes
// the agent it moved, and Reparent points its children at the moved one.
func (r *Registry) adoptChildren(gone *agent) {
	depth := 0
	if gone.Parent != "" {
		depth = gone.Depth
	}
	for _, c := range r.agents {
		if c.Parent != gone.ID {
			continue
		}
		c.Parent, c.LetParentAnswer = gone.Parent, false
		r.shiftDepth(c, depth-c.Depth)
	}
}

// shiftDepth moves an agent and its descendants (this machine's) by delta
// levels (the lock is held).
func (r *Registry) shiftDepth(a *agent, delta int) {
	seen := map[string]bool{}
	var walk func(a *agent)
	walk = func(a *agent) {
		if seen[a.ID] {
			return
		}
		seen[a.ID] = true
		a.Depth = max(a.Depth+delta, 0)
		r.changed(a)
		for _, c := range r.agents {
			if c.Parent == a.ID {
				walk(c)
			}
		}
	}
	walk(a)
}
