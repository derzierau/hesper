package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fakeSteward answers steward.* like internal/steward, on a config in
// memory; steward.inbox hands out its events once.
type fakeSteward struct {
	mu     sync.Mutex
	status wire.StewardStatus
	inbox  []wire.StewardEvent
	plans  []wire.StewardPlanParams
}

func (s *fakeSteward) Call(method string, params json.RawMessage, _ agents.Caller) (any, error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch method {
	case wire.MethodStewardStatus:
		return s.status, nil, true
	case wire.MethodStewardSet:
		var p wire.StewardSetParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, wire.Errorf(wire.CodeInvalid, "invalid params: %v", err), true
		}
		if p.Enabled != nil {
			s.status.Enabled = *p.Enabled
		}
		if p.Level != nil {
			s.status.Level = *p.Level
		}
		if p.Machine != nil {
			s.status.Machine, s.status.Node = *p.Machine, "node-"+*p.Machine
		}
		return s.status, nil, true
	case wire.MethodStewardInbox:
		list := s.inbox
		s.inbox = nil
		if list == nil {
			list = []wire.StewardEvent{}
		}
		return list, nil, true
	case wire.MethodStewardPlan:
		var p wire.StewardPlanParams
		json.Unmarshal(params, &p)
		s.plans = append(s.plans, p)
		return wire.Agent{ID: "L/plan01", Service: wire.ServicePlanner}, nil, true
	}
	return nil, nil, false
}

func (s *fakeSteward) Watch(done <-chan struct{}, _ func(string, any) error) { <-done }
func (s *fakeSteward) AgentSettled(agents.AgentEvent)                        {}
func (s *fakeSteward) AgentClosed(agents.AgentEvent)                         {}

func TestStewardCommands(t *testing.T) {
	st := &fakeSteward{status: wire.StewardStatus{StewardConfig: wire.StewardConfig{Level: wire.StewardSuggest}}}
	st.inbox = []wire.StewardEvent{{Kind: wire.StewardSettled, At: time.Now(), Agent: "L/a1", Loop: "lp-000000000001", Ending: wire.EndingAsks, Summary: "Merge it?"}}
	e := fullDaemon(t, st)

	var s wire.StewardStatus
	e.okJSON(t, &s, "steward", "status")
	if s.Enabled || s.Level != wire.StewardSuggest {
		t.Fatalf("status: %+v", s)
	}
	if out := e.ok(t, "steward", "status"); !strings.Contains(out, "off") || !strings.Contains(out, "suggest") || !strings.Contains(out, "wakes today") {
		t.Errorf("status:\n%s", out)
	}
	// on without --machine: this Mac, since none runs it yet.
	e.okJSON(t, &s, "steward", "on")
	if !s.Enabled || s.Machine != "L" {
		t.Fatalf("on: %+v", s)
	}
	e.okJSON(t, &s, "steward", "on", "--machine", "mini", "--level", "watch")
	if s.Machine != "mini" || s.Level != wire.StewardWatch {
		t.Fatalf("on --machine: %+v", s)
	}
	e.okJSON(t, &s, "steward", "on")
	if s.Machine != "mini" {
		t.Fatalf("on again moved it: %+v", s)
	}
	e.okJSON(t, &s, "steward", "level", "act")
	if s.Level != wire.StewardAct {
		t.Fatalf("level: %+v", s)
	}
	e.fails(t, exitUsage, codeUsage, "steward", "level", "boss")
	e.fails(t, exitUsage, codeUsage, "steward", "on", "--level", "boss")
	e.okJSON(t, &s, "steward", "off")
	if s.Enabled {
		t.Fatalf("off: %+v", s)
	}

	if out := e.ok(t, "steward", "inbox"); !strings.Contains(out, "settled") || !strings.Contains(out, "asks Merge it?") {
		t.Errorf("inbox:\n%s", out)
	}
	var events []wire.StewardEvent
	e.okJSON(t, &events, "steward", "inbox")
	if events == nil || len(events) != 0 {
		t.Fatalf("inbox again: %+v", events)
	}

	// goals plan goes to steward.plan with the scope's id.
	e.ok(t, "projects", "promote", e.project, "--name", "app")
	if out := strings.TrimSpace(e.ok(t, "goals", "plan", "app", "plan", "1.4")); out != "L/plan01" {
		t.Fatalf("plan: %q", out)
	}
	if len(st.plans) != 1 || !strings.HasPrefix(st.plans[0].Scope, "p-") || st.plans[0].Prompt != "plan 1.4" {
		t.Fatalf("plan params: %+v", st.plans)
	}

	// An agent may not turn it on (a person's only).
	var a wire.Agent
	e.okJSON(t, &a, "new", "--project", e.project, "hi")
	t.Setenv("HESPER_AGENT_ID", a.ID)
	t.Setenv("HESPER_SOCKET", e.sock)
	e.fails(t, exitForbidden, wire.CodeForbidden, "steward", "on", "--machine", "L")
}

// Without the Steward in hesperd, its commands are unavailable (exit 4).
func TestStewardUnavailable(t *testing.T) {
	e := fullDaemon(t)
	e.fails(t, exitUnavailable, wire.CodeUnavailable, "steward", "status")
	e.fails(t, exitUnavailable, wire.CodeUnavailable, "steward", "off")
}
