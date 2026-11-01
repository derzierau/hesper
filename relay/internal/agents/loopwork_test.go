package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// TestParentRefOnlyLocal: a parent on another Mac has no ref here, also
// when that Mac calls itself what this one does (both "L").
func TestParentRefOnlyLocal(t *testing.T) {
	h, _ := newLoopsHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	ref := func(id string) string {
		h.reg.mu.Lock()
		defer h.reg.mu.Unlock()
		return h.reg.parentRef(id)
	}
	if got := ref(a.ID); got != "n1/"+strings.TrimPrefix(a.ID, "L/") {
		t.Fatalf("local parent %q", got)
	}
	if got := ref("M/abc"); got != "" {
		t.Fatalf("remote parent %q", got)
	}
	if got := ref("L/zz9zz9"); got != "" {
		t.Fatalf("another L's parent %q", got)
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

// resultHas waits until id's last message contains text.
func (h *harness) resultHas(id, text string) {
	h.t.Helper()
	eventuallyTrue(h.t, id+" was told "+text, func() bool {
		res, err := h.reg.Result(id)
		return err == nil && strings.Contains(res.Message, text)
	})
}

func TestReportToParent(t *testing.T) {
	h, _ := newLoopsHarness(t)
	parent := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(parent.ID, wire.StateDone)
	// A person's spawn has no parent to tell.
	if a := h.spawn(wire.SpawnParams{Task: "small job", ReportToParent: true}); a.ReportToParent {
		t.Fatal("reportToParent without a parent")
	}
	child, err := h.spawnAs(parent.ID, wire.SpawnParams{Task: "Should I go on?", ReportToParent: true})
	allowed(t, "child", err)
	if !child.ReportToParent {
		t.Fatalf("child %+v", child)
	}
	h.resultHas(parent.ID, reportLine(child.ID, wire.EndingAsks))
	// A working parent is told once its turn ended.
	h.waitState(parent.ID, wire.StateDone)
	h.call("agents.input", wire.InputParams{ID: parent.ID, Text: "approve it", Submit: true}, nil)
	h.waitState(parent.ID, wire.StateApproval)
	h.call("agents.input", wire.InputParams{ID: child.ID, Text: "all good", Submit: true}, nil)
	eventuallyTrue(t, "child settled", func() bool {
		g, _ := h.reg.Get(child.ID)
		return g.State == wire.StateDone && g.Ending == wire.EndingDone
	})
	time.Sleep(100 * time.Millisecond)
	if g, _ := h.reg.Get(parent.ID); g.State != wire.StateApproval {
		t.Fatalf("parent typed into while waiting: %s", g.State)
	}
	h.call("agents.answer", wire.AnswerParams{ID: parent.ID, Decision: wire.Allow}, nil)
	h.resultHas(parent.ID, reportLine(child.ID, wire.EndingDone))
	// Persisted.
	h.close()
	h.open()
	if g, _ := h.reg.Get(child.ID); !g.ReportToParent {
		t.Fatal("reportToParent not persisted")
	}
}

// TestReportOncePerTurn: a turn settled twice (Codex: its screen, then
// its notify) is reported once: a delivered line is not sent again, a
// waiting one is replaced.
func TestReportOncePerTurn(t *testing.T) {
	h, _ := newLoopsHarness(t)
	parent := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(parent.ID, wire.StateDone)
	child, err := h.spawnAs(parent.ID, wire.SpawnParams{Task: "small job", ReportToParent: true})
	allowed(t, "child", err)
	h.resultHas(parent.ID, reportLine(child.ID, wire.EndingDone))
	h.waitState(parent.ID, wire.StateDone)
	h.call("agents.input", wire.InputParams{ID: parent.ID, Text: "approve it", Submit: true}, nil)
	h.waitState(parent.ID, wire.StateApproval)
	pl, cl := strings.TrimPrefix(parent.ID, "L/"), strings.TrimPrefix(child.ID, "L/")
	resettle := func(ending string) *parentReports {
		h.reg.mu.Lock()
		defer h.reg.mu.Unlock()
		h.reg.settle(h.reg.agents[cl], ending)
		return h.reg.reports[pl]
	}
	if pr := resettle(wire.EndingAsks); pr != nil {
		t.Fatalf("a delivered turn queued again: %+v", pr.ending)
	}
	// A new turn waits (the parent is busy); settled again, it is replaced.
	h.call("agents.input", wire.InputParams{ID: child.ID, Text: "all good", Submit: true}, nil)
	eventuallyTrue(t, "child's new turn queued", func() bool {
		h.reg.mu.Lock()
		defer h.reg.mu.Unlock()
		return h.reg.reports[pl] != nil
	})
	if pr := resettle(wire.EndingWaits); len(pr.order) != 1 || pr.ending[child.ID] != wire.EndingWaits {
		t.Fatalf("queued %v %v", pr.order, pr.ending)
	}
}

// TestReportWaitsForTyping: a person typing into the parent (a read-write
// attach) holds the report back for reportQuiet.
func TestReportWaitsForTyping(t *testing.T) {
	old := reportQuietFor
	reportQuietFor = time.Second
	t.Cleanup(func() { reportQuietFor = old })
	h, _ := newLoopsHarness(t)
	parent := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(parent.ID, wire.StateDone)
	rw, err := wire.Attach(t.Context(), h.sock, wire.AttachRequest{Attach: parent.ID, Mode: wire.ModeRW, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if err := rw.Input([]byte(" ")); err != nil {
		t.Fatal(err)
	}
	term, _ := h.reg.Term(parent.ID)
	eventuallyTrue(t, "typed", func() bool { return !term.LastInput().IsZero() })
	child, err := h.spawnAs(parent.ID, wire.SpawnParams{Task: "small job", ReportToParent: true})
	allowed(t, "child", err)
	h.waitState(child.ID, wire.StateDone)
	if res, _ := h.reg.Result(parent.ID); strings.Contains(res.Message, "[hesper]") {
		t.Fatal("told while the person was typing")
	}
	h.resultHas(parent.ID, reportLine(child.ID, wire.EndingDone))
}

// TestReportsCoalesce: one line per child (its latest ending), the
// oldest dropped over maxReports; a removed parent's reports go.
func TestReportsCoalesce(t *testing.T) {
	h, _ := newLoopsHarness(t)
	parent := h.spawn(wire.SpawnParams{Task: "approve it"})
	h.waitState(parent.ID, wire.StateApproval)
	h.reg.mu.Lock()
	child := &agent{Agent: wire.Agent{ID: "L/kid001", Parent: parent.ID, ReportToParent: true}}
	child.Ending = wire.EndingAsks
	h.reg.queueReport(child)
	child.Ending = wire.EndingDone
	h.reg.queueReport(child)
	for i := range maxReports {
		h.reg.queueReport(&agent{Agent: wire.Agent{ID: fmt.Sprintf("L/kid%03d", i+2), Parent: parent.ID, Ending: wire.EndingDone}})
	}
	pr := h.reg.reports[strings.TrimPrefix(parent.ID, "L/")]
	n, first := len(pr.order), pr.order[0]
	h.reg.mu.Unlock()
	if n != maxReports || first != "L/kid002" {
		t.Fatalf("%d queued, first %s", n, first)
	}
	h.reg.mu.Lock()
	child.Ending = wire.EndingWaits
	pr.ending["L/kid002"] = wire.EndingWaits
	h.reg.mu.Unlock()
	h.call("agents.answer", wire.AnswerParams{ID: parent.ID, Decision: wire.Allow}, nil)
	h.resultHas(parent.ID, reportLine("L/kid002", wire.EndingWaits))
	if res, _ := h.reg.Result(parent.ID); strings.Contains(res.Message, "kid001") {
		t.Fatalf("dropped report delivered: %s", res.Message)
	}
}

// TestServiceArgv: a service agent's options are the hook's, never its
// profile's; other agents keep theirs.
func TestServiceArgv(t *testing.T) {
	h := newHarness(t)
	h.close()
	// "claude": the fake agent's mode (the test binary is the command).
	h.opt.ServiceArgv = func(service string) []string { return []string{"claude", "--allowedTools", "Read," + service} }
	h.open()
	s, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "small job"}, wire.ServiceSteward)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Background {
		t.Fatal("the Steward starts on a wall")
	}
	h.waitState(s.ID, wire.StateDone)
	argv := h.argvs(s.ID)[0]
	if len(argv) < 4 || argv[1] != "--allowedTools" || argv[2] != "Read,steward" || argv[3] != "--session-id" || slices.Contains(argv, "--permission-mode") {
		t.Fatalf("service argv %q", argv)
	}
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(a.ID, wire.StateDone)
	if argv := h.argvs(a.ID)[0]; !slices.Contains(argv, "--permission-mode") || slices.Contains(argv, "--allowedTools") {
		t.Fatalf("agent argv %q", argv)
	}
}

// Agents that ran before loops existed (an upgrade) get a loop when
// hesperd starts: parents first, so a child joins its parent's loop.
func TestBackfillLoopsOnOpen(t *testing.T) {
	h := newHarness(t)
	parent := h.spawn(wire.SpawnParams{Kind: "shell"})
	child := h.spawn(wire.SpawnParams{Kind: "shell"})
	other := h.spawn(wire.SpawnParams{Kind: "shell"})
	h.close()
	editRecord(t, h, child.ID, func(rec map[string]any) { rec["parent"], rec["depth"] = parent.ID, 1 })
	l := newFakeLoops()
	h.opt.Loops = l
	h.open()
	_, p := h.reg.split(parent.ID)
	_, c := h.reg.split(child.ID)
	_, o := h.reg.split(other.ID)
	pl, cl, ol := l.LoopOf("n1/"+p), l.LoopOf("n1/"+c), l.LoopOf("n1/"+o)
	if pl == "" || cl != pl || ol == "" || ol == pl {
		t.Fatalf("loops: parent %q child %q other %q", pl, cl, ol)
	}
	if n := len(l.assigns()); n != 3 {
		t.Fatalf("%d assigns, want 3", n)
	}
	// A second start assigns nothing again.
	h.close()
	h.open()
	if n := len(l.assigns()); n != 3 {
		t.Fatalf("%d assigns after a restart, want 3", n)
	}
}
