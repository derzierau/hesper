package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Loops in the registry (loopwork.go), against fakeLoops.

// fakeLoops is an in-memory loops store: Assign follows the contract's
// order (Loop, a new loop for Milestone, the parent's, the source
// session's, a new one).
type fakeLoops struct {
	mu       sync.Mutex
	n        int
	of       map[string]string // agent ref → loop
	sessions map[string]string // session ref → loop
	assigned []AssignInfo
	settled  []AgentEvent
	closed   []AgentEvent
	calls    []Caller
	config   wire.StewardConfig
}

func newFakeLoops() *fakeLoops {
	return &fakeLoops{of: map[string]string{}, sessions: map[string]string{}, config: wire.StewardConfig{Level: wire.StewardSuggest}}
}

func (f *fakeLoops) Call(method string, params json.RawMessage, caller Caller) (any, error, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, caller)
	return map[string]string{"method": method}, nil, true
}

func (f *fakeLoops) Watch(done <-chan struct{}, send func(string, any) error) { <-done }

func (f *fakeLoops) Assign(a AssignInfo) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assigned = append(f.assigned, a)
	loop := a.Loop
	switch {
	case loop != "":
	case a.Milestone != "":
	case f.of[a.ParentRef] != "":
		loop = f.of[a.ParentRef]
	case f.sessions[a.SourceSession] != "":
		loop = f.sessions[a.SourceSession]
	}
	if loop == "" {
		f.n++
		loop = fmt.Sprintf("lp-%d", f.n)
	}
	f.of[a.Ref] = loop
	return loop
}

func (f *fakeLoops) AgentSettled(e AgentEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settled = append(f.settled, e)
}

func (f *fakeLoops) AgentClosed(e AgentEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, e)
}

func (f *fakeLoops) LoopOf(ref string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.of[ref]
}

func (f *fakeLoops) Node() string { return "n1" }

func (f *fakeLoops) StewardConfig() wire.StewardConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.config
}

func (f *fakeLoops) setLevel(level string) {
	f.mu.Lock()
	f.config.Level = level
	f.mu.Unlock()
}

func (f *fakeLoops) assigns() []AssignInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AssignInfo(nil), f.assigned...)
}

// events is the settled (or closed) events of ref so far.
func (f *fakeLoops) events(ref string, closed bool) []AgentEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.settled
	if closed {
		list = f.closed
	}
	var out []AgentEvent
	for _, e := range list {
		if e.Ref == ref {
			out = append(out, e)
		}
	}
	return out
}

// newLoopsHarness is a harness whose registry has a fakeLoops.
func newLoopsHarness(t *testing.T) (*harness, *fakeLoops) {
	h := newHarness(t)
	h.close()
	l := newFakeLoops()
	h.opt.Loops = l
	h.open()
	return h, l
}

func eventuallyTrue(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAssignOnInsertion(t *testing.T) {
	h, l := newLoopsHarness(t)
	ref := func(id string) string { return "n1/" + strings.TrimPrefix(id, "L/") }
	parent := h.spawn(wire.SpawnParams{Task: "small job"})
	if parent.Loop != "lp-1" {
		t.Fatalf("parent's loop %q", parent.Loop)
	}
	child, err := h.spawnAs(parent.ID, wire.SpawnParams{Task: "small job"})
	allowed(t, "child", err)
	if child.Loop != "lp-1" {
		t.Fatalf("child's loop %q", child.Loop)
	}
	other := h.spawn(wire.SpawnParams{Task: "small job", Loop: "lp-9"})
	filed := h.spawn(wire.SpawnParams{Task: "small job", Milestone: "ms-1"})
	cont, err := h.reg.SpawnFrom(wire.SpawnParams{Project: h.project, Task: "small job"}, "n7:claude:s1")
	allowed(t, "continue", err)
	resumed, err := h.reg.SpawnSession(SessionSpawn{Kind: wire.KindClaude, SessionID: newUUID(), Dir: h.project, Source: "n7:claude:s2"})
	allowed(t, "resume", err)
	if _, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project}, wire.ServiceSteward); err != nil {
		t.Fatal(err)
	}
	got := l.assigns()
	if len(got) != 6 {
		t.Fatalf("%d assigns (service agents get none): %+v", len(got), got)
	}
	want := []AssignInfo{
		{Ref: ref(parent.ID)},
		{Ref: ref(child.ID), ParentRef: ref(parent.ID)},
		{Ref: ref(other.ID), Loop: "lp-9"},
		{Ref: ref(filed.ID), Milestone: "ms-1"},
		{Ref: ref(cont.ID), SourceSession: "n7:claude:s1"},
		{Ref: ref(resumed.ID), SourceSession: "n7:claude:s2"},
	}
	for i, w := range want {
		g := got[i]
		if g.Ref != w.Ref || g.ParentRef != w.ParentRef || g.Loop != w.Loop || g.Milestone != w.Milestone || g.SourceSession != w.SourceSession || g.Agent.ID == "" {
			t.Errorf("assign %d: %+v, want %+v", i, g, w)
		}
	}
	// agents.list shows the loops; agents.json never keeps them.
	var list []wire.Agent
	if err := h.call("agents.list", nil, &list); err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Service == "" && a.Loop == "" {
			t.Errorf("%s has no loop in agents.list", a.ID)
		}
	}
	h.close()
	data, _ := os.ReadFile(filepath.Join(h.state, "agents.json"))
	if strings.Contains(string(data), `"loop"`) {
		t.Fatalf("agents.json keeps loops: %s", data)
	}
}

// TestParentRefOnlyLocal: a parent on another Mac has no ref here.
func TestParentRefOnlyLocal(t *testing.T) {
	h, _ := newLoopsHarness(t)
	if got := h.reg.parentRef("L/abc"); got != "n1/abc" {
		t.Fatalf("local parent %q", got)
	}
	if got := h.reg.parentRef("M/abc"); got != "" {
		t.Fatalf("remote parent %q", got)
	}
}

func TestSpawnServiceStaysInBackground(t *testing.T) {
	h := newHarness(t)
	if _, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project}, "nope"); err == nil {
		t.Fatal("an unknown service started")
	}
	a, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "approve it"}, wire.ServiceSteward)
	if err != nil {
		t.Fatal(err)
	}
	if a.Service != wire.ServiceSteward {
		t.Fatalf("service %q", a.Service)
	}
	h.waitState(a.ID, wire.StateApproval)
	if err := h.call("agents.background", wire.BackgroundParams{ID: a.ID, Background: true}, nil); err != nil {
		t.Fatal(err)
	}
	// Its turn ends in the background: it stays (finishedInBackground).
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitState(a.ID, wire.StateDone)
	// Its process ends on its own: it stays, exited (restarted by the
	// Steward).
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "exit", Submit: true}, nil)
	if got := h.waitState(a.ID, wire.StateExited); got.Service != wire.ServiceSteward {
		t.Fatalf("after exit %+v", got)
	}
	// Persisted.
	h.close()
	h.open()
	if got, err := h.reg.Get(a.ID); err != nil || got.Service != wire.ServiceSteward {
		t.Fatalf("after restart %+v %v", got, err)
	}
}
