package agents

import (
	"encoding/json"
	"strings"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// loopsDispatch runs a loops.*, goals.* or steward.* method of control
// connection c: who calls it first (an agent or a person), then the loops
// store or the Steward. The agent tree's rules for these methods (which
// are a person's only, what the Steward may do at its level) live here.
//
// Policy, when an agent calls (a person may do everything):
//   - reads (loopReads): anything;
//   - a person's only (loopPerson), and any loops.save or goals.save that
//     marks a loop, goal or milestone done or dropped: never;
//   - loops.merge and loops.split: only the Steward at level act;
//   - steward.plan: only the Steward, from level suggest on;
//   - other writes: a service agent (the Steward, a planner) may; any
//     other agent only loops.item and loops.link of its own loop.
//
// Every write by an agent, allowed or refused, goes to the audit log.
func (s *Server) loopsDispatch(c *ctrl, method string, params json.RawMessage) (any, error) {
	caller, t, err := s.callerOf(c, method, params)
	if err != nil {
		return nil, err
	}
	who := s.callerInfo(caller, t)
	params = withoutCaller(params) // the store and the Steward decode strictly
	reg := s.reg
	write := !loopReads[method]
	if caller != "" && write {
		if refused := s.loopsAllowed(who, method, params); refused != nil {
			s.auditCall(caller, method, "", refused, nil)
			return nil, refused
		}
	}
	var res any
	if strings.HasPrefix(method, "steward.") {
		if reg.opt.Steward == nil {
			err = wire.Errorf(wire.CodeUnavailable, "the Steward is not available")
		} else {
			res, err, _ = reg.opt.Steward.Call(method, params, who)
		}
	} else if reg.opt.Loops == nil {
		err = wire.Errorf(wire.CodeUnavailable, "loops are not available")
	} else {
		res, err, _ = reg.opt.Loops.Call(method, params, who)
	}
	if caller != "" && write {
		s.auditCall(caller, method, "", nil, err)
	}
	return res, err
}

// callerInfo is who calls (caller an agent's full id, "" a person): its
// ref (one of this Mac's agents), the loop it works in, its service.
func (s *Server) callerInfo(caller string, t treeView) Caller {
	who := Caller{Agent: caller}
	if a, ok := t[caller]; ok {
		who.Loop, who.Service = a.Loop, a.Service
		if !s.reg.IsRemote(caller) {
			_, local := s.reg.split(caller)
			who.Ref = s.reg.ref(local)
		}
	}
	return who
}

// loopReads are the loops, goals and steward methods that change nothing
// (steward.inbox is not one: it hands the Steward its events).
var loopReads = map[string]bool{
	wire.MethodLoopsList: true, wire.MethodLoopsGet: true, wire.MethodLoopsSettingsGet: true,
	wire.MethodGoalsList: true, wire.MethodStewardStatus: true,
}

// loopPerson are a person's only: they turn the Steward on or off, accept
// a plan, remove goals, change the settings.
var loopPerson = map[string]string{
	wire.MethodStewardSet:       "hesperctl steward on|off|level",
	wire.MethodGoalsAccept:      "hesperctl goals accept, or ⌘↩ in Hesper",
	wire.MethodGoalsRemove:      "hesperctl goals rm",
	wire.MethodLoopsSettingsSet: "hesperctl loops settings",
}

// stewardLevel is the Steward's level ("" without a loops store: watch,
// the most careful).
func (s *Server) stewardLevel() string {
	if l := s.reg.opt.Loops; l != nil {
		if level := l.StewardConfig().Level; level != "" {
			return level
		}
	}
	return wire.StewardWatch
}

// loopsAllowed is nil when agent who may run the write method, else the
// refusal, naming the rule.
func (s *Server) loopsAllowed(who Caller, method string, params json.RawMessage) error {
	caller := who.Agent
	if how, ok := loopPerson[method]; ok {
		return wire.Errorf(wire.CodeForbidden, "agent %s may not %s: a person does (%s)", caller, method, how)
	}
	if what := closes(method, params); what != "" {
		return wire.Errorf(wire.CodeForbidden, "agent %s may not mark a %s done or dropped: a person does (hesperctl loops close, goals save)", caller, what)
	}
	switch method {
	case wire.MethodLoopsMerge, wire.MethodLoopsSplit:
		if who.Service == wire.ServiceSteward && s.stewardLevel() == wire.StewardAct {
			return nil
		}
		return wire.Errorf(wire.CodeForbidden, "agent %s may not %s loops: a person does, or the Steward at level act (hesperctl loops %s)",
			caller, strings.TrimPrefix(method, "loops."), strings.TrimPrefix(method, "loops."))
	}
	if method == wire.MethodStewardPlan {
		// a planner starts agents: only the Steward, from level suggest on
		if who.Service == wire.ServiceSteward && s.stewardLevel() != wire.StewardWatch {
			return nil
		}
		return wire.Errorf(wire.CodeForbidden, "agent %s may not start planning sessions: a person does, or the Steward from level suggest on (hesperctl goals plan)", caller)
	}
	if who.Service != "" {
		return nil
	}
	if method == wire.MethodLoopsItem || method == wire.MethodLoopsLink {
		var p struct {
			ID string `json:"id"`
		}
		json.Unmarshal(params, &p)
		if who.Loop != "" && p.ID == who.Loop {
			return nil
		}
		return wire.Errorf(wire.CodeForbidden, "agent %s may only change items and links of its own loop (%s); %s is not it", caller, orDefault(who.Loop, "none"), orDefault(p.ID, "no loop"))
	}
	return wire.Errorf(wire.CodeForbidden, "agent %s may not %s: a person or the Steward does; agents change items and links of their own loop only", caller, method)
}

// closes is what a loops.save or goals.save marks done or dropped ("loop",
// "goal", "milestone"; "" nothing).
func closes(method string, params json.RawMessage) string {
	final := func(status *string) bool {
		return status != nil && (*status == wire.LoopDone || *status == wire.LoopDropped)
	}
	switch method {
	case wire.MethodLoopsSave:
		var p wire.LoopSaveParams
		if json.Unmarshal(params, &p) == nil && final(p.Status) {
			return "loop"
		}
	case wire.MethodGoalsSave:
		var p wire.GoalSaveParams
		if json.Unmarshal(params, &p) != nil {
			return ""
		}
		if p.Goal != nil && final(p.Goal.Status) {
			return "goal"
		}
		if p.Milestone != nil && final(p.Milestone.Status) {
			return "milestone"
		}
	}
	return ""
}

// serviceSpawn checks a spawn or session start by service agent a (the
// Steward, a planner): never with letParentAnswer; only the Steward at
// level act starts anything, and only a draft (the planner it runs is
// started in hesperd, SpawnService, not through the socket).
func (s *Server) serviceSpawn(a wire.Agent, method string, p wire.SpawnParams) error {
	if p.LetParentAnswer {
		return wire.Errorf(wire.CodeForbidden, "service agent %s may not start agents with --let-parent-answer: a person answers them", a.ID)
	}
	if method == "agents.spawn" && p.Draft != "" && a.Service == wire.ServiceSteward && s.stewardLevel() == wire.StewardAct {
		return nil
	}
	return wire.Errorf(wire.CodeForbidden, "service agent %s may not %s: only the Steward at level act starts drafts", a.ID, verb(method))
}

// draftsAllowed: the Steward saves drafts from level suggest on.
func (s *Server) draftsAllowed(a wire.Agent) error {
	if a.Service == wire.ServiceSteward && s.stewardLevel() == wire.StewardWatch {
		return wire.Errorf(wire.CodeForbidden, "the Steward %s may not save drafts at level watch (hesperctl steward level suggest)", a.ID)
	}
	return nil
}
