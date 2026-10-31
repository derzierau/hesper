package agents

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Loops is the loops store (internal/loops), hooked in here: it puts every
// agent in a loop when it is inserted (Assign), records how its turns end
// and how it was closed, and serves the loops.* and goals.* methods and
// notifications on the local socket. Nil (tests, older setups): agents
// get no loop and the methods answer unavailable.
//
// Lock order: the registry may call Assign, AgentSettled, AgentClosed and
// LoopOf with its lock held, so the store never calls back into the
// registry while holding its own lock (it snapshots its records, unlocks,
// then asks the registry for the live agents it joins into its views).
type Loops interface {
	// Call runs a loops.* or goals.* method for caller (handled false:
	// not one of them).
	Call(method string, params json.RawMessage, caller Caller) (result any, err error, handled bool)
	// Watch sends the loops/goals notifications of an agents.subscribe
	// connection until done closes or send fails.
	Watch(done <-chan struct{}, send func(method string, params any) error)
	// Assign puts a newly inserted agent in a loop and returns the loop's
	// id: a.Loop when given, else a new loop under a.Milestone, else the
	// parent's loop, else the source session's, else a new loop. Never
	// called for service agents.
	Assign(a AssignInfo) string
	// AgentSettled records how one of this Mac's agents' turn ended;
	// AgentClosed that it left the registry (Reason, To for a move).
	AgentSettled(e AgentEvent)
	AgentClosed(e AgentEvent)
	// LoopOf is the loop an agent ref belongs to ("" none).
	LoopOf(ref string) string
	// Node is this Mac's node id (the projects store's), the first half
	// of every agent ref this Mac writes.
	Node() string
	// StewardConfig is the Steward's shared configuration.
	StewardConfig() wire.StewardConfig
}

// Steward runs the Steward (internal/steward) when this Mac is its node:
// the steward.* methods and notifications, and the events that wake it.
// Nil: steward.* answer unavailable.
type Steward interface {
	Call(method string, params json.RawMessage, caller Caller) (result any, err error, handled bool)
	Watch(done <-chan struct{}, send func(method string, params any) error)
	AgentSettled(e AgentEvent)
	AgentClosed(e AgentEvent)
}

// Caller is who calls a loops, goals or steward method: Agent is the
// calling agent's full id ("" a person), Ref its loops ref, Loop the loop
// it works in and Service its Agent.Service. An agent that is not a
// service may change items and links of its own loop only (Call checks).
type Caller struct {
	Agent   string
	Ref     string
	Loop    string
	Service string
}

// AssignInfo is what Assign knows about a newly inserted agent.
type AssignInfo struct {
	// Ref is the agent's ref ("<node>/<local id>"); Agent the agent as
	// inserted (its Parent, Machine, ProjectID, SessionID).
	Ref   string
	Agent wire.Agent
	// ParentRef is the parent's ref ("" none); SourceSession the session
	// ref a resume, fork, continue-as or move started from ("" none).
	ParentRef     string
	SourceSession string
	// Loop and Milestone are what the spawn asked for (SpawnParams).
	Loop      string
	Milestone string
}

// AgentEvent is a settled turn or a closed agent of this Mac.
type AgentEvent struct {
	Ref   string
	Agent wire.Agent
	// Ending and Message (the final message, at most 64 KiB) for a
	// settled turn and a close; Reason and To for a close.
	Ending  string
	Message string
	Reason  string
	To      string
}

// isLoopMethod reports whether method is served by the loops store or the
// Steward (loopscall.go dispatches them).
func isLoopMethod(method string) bool {
	return strings.HasPrefix(method, "loops.") || strings.HasPrefix(method, "goals.") || strings.HasPrefix(method, "steward.")
}

// ref is the loops ref of one of this Mac's agents ("" without a store).
func (r *Registry) ref(local string) string {
	if r.opt.Loops == nil {
		return ""
	}
	return r.opt.Loops.Node() + "/" + local
}

// watchLoops sends the loops, goals and steward notifications on a
// subscribed control connection (server.go's subscribe) until it closes.
func (c *ctrl) watchLoops() {
	send := func(method string, params any) error {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		return c.write(wire.Notification{JSONRPC: "2.0", Method: method, Params: data})
	}
	var wg sync.WaitGroup
	if l := c.s.reg.opt.Loops; l != nil {
		wg.Add(1)
		go func() { defer wg.Done(); l.Watch(c.done, send) }()
	}
	if st := c.s.reg.opt.Steward; st != nil {
		st.Watch(c.done, send)
	}
	wg.Wait()
}
