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

// fakeSteward records the events it hears.
type fakeSteward struct {
	mu      sync.Mutex
	settled []AgentEvent
	closed  []AgentEvent
}

func (f *fakeSteward) Call(string, json.RawMessage, Caller) (any, error, bool) {
	return struct{}{}, nil, true
}
func (f *fakeSteward) Watch(done <-chan struct{}, _ func(string, any) error) { <-done }
func (f *fakeSteward) AgentSettled(e AgentEvent) {
	f.mu.Lock()
	f.settled = append(f.settled, e)
	f.mu.Unlock()
}
func (f *fakeSteward) AgentClosed(e AgentEvent) {
	f.mu.Lock()
	f.closed = append(f.closed, e)
	f.mu.Unlock()
}

func TestEndingsOnTurnEnd(t *testing.T) {
	h := newHarness(t)
	h.close()
	l, st := newFakeLoops(), &fakeSteward{}
	h.opt.Loops, h.opt.Steward = l, st
	h.open()
	a := h.spawn(wire.SpawnParams{Task: "Should I push it?"})
	ref := "n1/" + strings.TrimPrefix(a.ID, "L/")
	eventuallyTrue(t, "settled", func() bool { return len(l.events(ref, false)) == 1 })
	e := l.events(ref, false)[0]
	if e.Ending != wire.EndingAsks || e.Message != "Done: Should I push it?" || e.Agent.Ending != wire.EndingAsks || e.Agent.Loop == "" {
		t.Fatalf("settled %+v", e)
	}
	if got, _ := h.reg.Get(a.ID); got.Ending != wire.EndingAsks {
		t.Fatalf("ending %q", got.Ending)
	}
	eventuallyTrue(t, "the Steward heard it", func() bool { st.mu.Lock(); defer st.mu.Unlock(); return len(st.settled) == 1 })
	// The next turn ends done.
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "tidy up", Submit: true}, nil)
	eventuallyTrue(t, "second turn", func() bool { return len(l.events(ref, false)) == 2 })
	if e := l.events(ref, false)[1]; e.Ending != wire.EndingDone {
		t.Fatalf("second turn %+v", e)
	}
	// Persisted.
	h.close()
	h.open()
	if got, _ := h.reg.Get(a.ID); got.Ending != wire.EndingDone {
		t.Fatalf("after restart: %q", got.Ending)
	}
}

// TestEndingWithoutHooks: a shell's turn (no hook, no message) ends done,
// and an old message is not classified again.
func TestEndingWithoutHooks(t *testing.T) {
	h, l := newLoopsHarness(t)
	a := h.spawn(wire.SpawnParams{Kind: wire.KindShell, Profile: "real-shell", Track: true})
	ref := "n1/" + strings.TrimPrefix(a.ID, "L/")
	h.waitState(a.ID, wire.StateIdle)
	h.reg.mu.Lock()
	h.reg.agents[strings.TrimPrefix(a.ID, "L/")].lastMessage = "Should I go on?"
	h.reg.agents[strings.TrimPrefix(a.ID, "L/")].lastMessageAt = time.Now().Add(-time.Hour)
	h.reg.mu.Unlock()
	if err := h.call("agents.input", wire.InputParams{ID: a.ID, Text: "sleep 0.3", Submit: true}, nil); err != nil {
		t.Fatal(err)
	}
	eventuallyTrue(t, "settled", func() bool { return len(l.events(ref, false)) >= 1 })
	if e := l.events(ref, false)[0]; e.Ending != wire.EndingDone || e.Message != "" {
		t.Fatalf("settled %+v", e)
	}
}

func TestClosedWorkRecord(t *testing.T) {
	h, l := newLoopsHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "Want me to add tests?"})
	ref := "n1/" + strings.TrimPrefix(a.ID, "L/")
	h.waitState(a.ID, wire.StateDone)
	if err := h.call("agents.close", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	eventuallyTrue(t, "closed", func() bool { return len(l.events(ref, true)) == 1 })
	e := l.events(ref, true)[0]
	if e.Reason != wire.ReasonClosed || e.Ending != wire.EndingAsks || e.Message != "Done: Want me to add tests?" || e.Agent.Summary == "" || e.To != "" {
		t.Fatalf("closed %+v", e)
	}
}

// blockingLoops blocks AgentSettled until release closes.
type blockingLoops struct {
	*fakeLoops
	release chan struct{}
}

func (b *blockingLoops) AgentSettled(e AgentEvent) {
	<-b.release
	b.fakeLoops.AgentSettled(e)
}

// TestLoopEventsDropOldest: a slow store never blocks the pusher; the
// queue keeps the newest maxLoopEvents and counts what it dropped.
func TestLoopEventsDropOldest(t *testing.T) {
	b := &blockingLoops{fakeLoops: newFakeLoops(), release: make(chan struct{})}
	q := newLoopEvents(b, nil, t.Logf)
	q.push(loopEvent{AgentEvent: AgentEvent{Ref: "first"}}) // taken by the goroutine, which blocks
	eventuallyTrue(t, "taken", func() bool { q.mu.Lock(); defer q.mu.Unlock(); return len(q.q) == 0 })
	for i := range maxLoopEvents + 10 {
		q.push(loopEvent{AgentEvent: AgentEvent{Ref: fmt.Sprint(i)}})
	}
	if d := q.Dropped(); d != 10 {
		t.Fatalf("dropped %d", d)
	}
	close(b.release)
	q.stop()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.settled) != maxLoopEvents+1 || b.settled[1].Ref != "10" || b.settled[len(b.settled)-1].Ref != fmt.Sprint(maxLoopEvents+9) {
		t.Fatalf("%d delivered, first queued %q", len(b.settled), b.settled[1].Ref)
	}
}
