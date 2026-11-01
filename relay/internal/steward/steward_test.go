package steward

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fakeStore is the loops store's side the Steward uses.
type fakeStore struct {
	mu       sync.Mutex
	node     string
	cfg      wire.StewardConfig
	ch       chan struct{}
	settings wire.LoopSettings
	loops    []wire.Loop
	goals    wire.GoalsList
}

func newFakeStore() *fakeStore {
	return &fakeStore{node: "n-here", cfg: wire.StewardConfig{Level: wire.StewardSuggest}, ch: make(chan struct{}),
		settings: wire.LoopSettings{EveningAt: "18:00", MaxWakesPerDay: 40}}
}

func (f *fakeStore) Node() string { return f.node }
func (f *fakeStore) StewardConfig() wire.StewardConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}
func (f *fakeStore) SetStewardConfig(p wire.StewardSetParams) (wire.StewardConfig, error) {
	f.mu.Lock()
	if p.Enabled != nil {
		f.cfg.Enabled = *p.Enabled
		if f.cfg.Node == "" {
			f.cfg.Node = f.node
		}
	}
	if p.Machine != nil {
		f.cfg.Node = "n-" + *p.Machine
	}
	if p.Level != nil {
		f.cfg.Level = *p.Level
	}
	close(f.ch)
	f.ch = make(chan struct{})
	f.mu.Unlock()
	return f.StewardConfig(), nil
}
func (f *fakeStore) StewardChanges() <-chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ch
}
func (f *fakeStore) Settings() wire.LoopSettings           { return f.settings }
func (f *fakeStore) List(wire.LoopsListParams) []wire.Loop { return f.loops }
func (f *fakeStore) Goals(string) wire.GoalsList           { return f.goals }

// fakeRegistry records what the Steward asks of the agents.
type fakeRegistry struct {
	mu      sync.Mutex
	n       int
	agents  map[string]wire.Agent
	spawns  []wire.SpawnParams
	closed  []string
	resumed []string
	inputs  []string
}

func newFakeRegistry() *fakeRegistry { return &fakeRegistry{agents: map[string]wire.Agent{}} }

func (f *fakeRegistry) SpawnService(p wire.SpawnParams, service string) (wire.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	a := wire.Agent{ID: fmt.Sprintf("L/st%04d", f.n), Kind: p.Kind, Name: p.Name, Project: p.Project, Task: p.Task, Service: service,
		Parent: p.Parent, Depth: p.Depth, State: wire.StateStarting, SessionID: fmt.Sprintf("s-%d", f.n), Created: time.Now()}
	f.agents[a.ID] = a
	f.spawns = append(f.spawns, p)
	return a, nil
}
func (f *fakeRegistry) Get(id string) (wire.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	if !ok {
		return wire.Agent{}, wire.Errorf(wire.CodeNotFound, "no agent %s", id)
	}
	return a, nil
}
func (f *fakeRegistry) List() []wire.Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []wire.Agent
	for _, a := range f.agents {
		out = append(out, a)
	}
	return out
}
func (f *fakeRegistry) Resume(id string) (wire.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[id]
	a.Exit, a.State = nil, wire.StateStarting
	f.agents[id] = a
	f.resumed = append(f.resumed, id)
	return a, nil
}
func (f *fakeRegistry) CloseAgent(id string) (wire.CloseResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.agents, id)
	f.closed = append(f.closed, id)
	return wire.CloseResult{}, nil
}
func (f *fakeRegistry) Input(p wire.InputParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, p.ID+": "+p.Text)
	return nil
}

// set changes an agent (state, exit) as the registry would.
func (f *fakeRegistry) set(id, state string, exited bool) wire.Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[id]
	a.State, a.Exit = state, nil
	if exited {
		code := 1
		a.Exit = &wire.Exit{Code: &code}
	}
	f.agents[id] = a
	return a
}

func (f *fakeRegistry) count() (spawns, closed, resumed, inputs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.spawns), len(f.closed), len(f.resumed), len(f.inputs)
}

type world struct {
	t     *testing.T
	store *fakeStore
	reg   *fakeRegistry
	st    *Steward
	now   time.Time
	dir   string
}

// newWorld: a Steward on a fake store and registry at 10:00 local
// (UTC), driven by step (no goroutine).
func newWorld(t *testing.T) *world {
	w := &world{t: t, store: newFakeStore(), reg: newFakeRegistry(), dir: t.TempDir(),
		now: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)}
	w.st = New(Options{StateDir: w.dir, Store: w.store, Now: func() time.Time { return w.now }, Location: time.UTC, Logf: t.Logf})
	w.st.reg = w.reg
	return w
}

func (w *world) on() {
	w.t.Helper()
	tr := true
	if _, err := w.st.Set(wire.StewardSetParams{Enabled: &tr}); err != nil {
		w.t.Fatal(err)
	}
}

// running starts the Steward and makes its agent quiet.
func (w *world) running() string {
	w.t.Helper()
	w.on()
	w.st.step()
	id := w.st.Status().Agent
	if id == "" {
		w.t.Fatal("not started")
	}
	w.quiet(id)
	return id
}

func (w *world) quiet(id string) {
	w.st.AgentChanged(w.reg.set(id, wire.StateIdle, false))
}

func settled(id, ending string) agents.AgentEvent {
	return agents.AgentEvent{Ref: "n/" + id, Agent: wire.Agent{ID: id, Loop: "lp-1", Summary: "did " + id}, Ending: ending}
}

func TestConfigStartStopByNode(t *testing.T) {
	w := newWorld(t)
	w.st.step()
	if s, _, _, _ := w.reg.count(); s != 0 {
		t.Fatal("started while off")
	}
	// On, on another Mac: not here.
	other := "M"
	tr := true
	w.st.Set(wire.StewardSetParams{Enabled: &tr, Machine: &other})
	w.st.step()
	if s, _, _, _ := w.reg.count(); s != 0 {
		t.Fatal("started for another Mac")
	}
	// Here.
	here := "here"
	w.st.Set(wire.StewardSetParams{Machine: &here})
	w.st.step()
	if s, _, _, _ := w.reg.count(); s != 1 {
		t.Fatalf("%d spawns", s)
	}
	p := w.reg.spawns[0]
	if p.Kind != wire.KindClaude || p.Name != "Steward" || p.Project != filepath.Join(w.dir, "steward") || !strings.Contains(p.Task, "Open loops") {
		t.Fatalf("spawn %+v", p)
	}
	if fi, err := os.Stat(p.Project); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("steward folder %v %v", fi, err)
	}
	st := w.st.Status()
	if st.Agent == "" || !st.Enabled || st.Session == "" {
		t.Fatalf("status %+v", st)
	}
	// A step again does not start a second one.
	w.st.step()
	if s, _, _, _ := w.reg.count(); s != 1 {
		t.Fatal("second Steward")
	}
	// Moved to another Mac: closed here.
	w.st.Set(wire.StewardSetParams{Machine: &other})
	w.st.step()
	if _, c, _, _ := w.reg.count(); c != 1 || w.st.Status().Agent != "" {
		t.Fatalf("not stopped: %+v", w.st.Status())
	}
	// Events queue only while it is on here.
	w.st.AgentSettled(settled("L/a1", wire.EndingDone))
	if w.st.Status().Queued != 0 {
		t.Fatal("queued for another Mac's Steward")
	}
}

func TestAdoptRestored(t *testing.T) {
	w := newWorld(t)
	restored, _ := w.reg.SpawnService(wire.SpawnParams{Kind: wire.KindClaude, Name: "Steward"}, wire.ServiceSteward)
	w.reg.spawns = nil
	w.on()
	w.st.step()
	if s, _, _, _ := w.reg.count(); s != 0 || w.st.Status().Agent != restored.ID {
		t.Fatalf("not adopted: %d spawns, %+v", s, w.st.Status())
	}
	// Off: a restored Steward of a Steward that is off is closed.
	w2 := newWorld(t)
	gone, _ := w2.reg.SpawnService(wire.SpawnParams{Kind: wire.KindClaude}, wire.ServiceSteward)
	w2.st.step()
	if _, c, _, _ := w2.reg.count(); c != 1 || w2.reg.closed[0] != gone.ID {
		t.Fatal("restored Steward kept while off")
	}
}

func TestRestartBackoffCap(t *testing.T) {
	w := newWorld(t)
	id := w.running()
	for i, delay := range restartDelays {
		w.st.AgentChanged(w.reg.set(id, wire.StateExited, true))
		next := w.st.step()
		if _, _, r, _ := w.reg.count(); r != i {
			t.Fatalf("restart %d before its delay", i+1)
		}
		if want := w.now.Add(delay); !next.Equal(want) {
			t.Fatalf("restart %d at %v, want %v", i+1, next, want)
		}
		w.now = next
		w.st.step()
		if _, _, r, _ := w.reg.count(); r != i+1 {
			t.Fatalf("restart %d did not happen", i+1)
		}
	}
	// A fourth end within the hour: it stays stopped.
	w.st.AgentChanged(w.reg.set(id, wire.StateExited, true))
	w.st.step()
	w.now = w.now.Add(10 * time.Minute)
	w.st.step()
	if _, _, r, _ := w.reg.count(); r != maxRestarts {
		t.Fatalf("%d restarts", r)
	}
	if st := w.st.Status(); st.Stopped == "" {
		t.Fatalf("status %+v", st)
	}
	// Turning it on again retries at once.
	w.on()
	w.st.step()
	if _, _, r, _ := w.reg.count(); r != maxRestarts+1 || w.st.Status().Stopped != "" {
		t.Fatalf("not retried: %d, %+v", r, w.st.Status())
	}
	// A person closing it: a new one, counted like a restart.
	w.reg.CloseAgent(id)
	next := w.st.step()
	w.now = next
	w.st.step()
	if s, _, _, _ := w.reg.count(); s != 2 {
		t.Fatalf("%d spawns after a close", s)
	}
}

func TestWakeGating(t *testing.T) {
	w := newWorld(t)
	w.store.settings.MaxWakesPerDay = 2
	id := w.running()
	// Working: no wake.
	w.st.AgentChanged(w.reg.set(id, wire.StateWorking, false))
	w.st.AgentSettled(settled("L/a1", wire.EndingAsks))
	w.st.step()
	if _, _, _, n := w.reg.count(); n != 0 {
		t.Fatal("woken while working")
	}
	// Quiet: woken with the fixed line.
	w.quiet(id)
	w.st.step()
	if got := w.reg.inputs; len(got) != 1 || got[0] != id+": "+WakeLine(1) {
		t.Fatalf("inputs %q", got)
	}
	// Not again within a minute, even quiet with new events.
	w.st.Inbox()
	w.quiet(id)
	w.st.AgentSettled(settled("L/a2", wire.EndingDone))
	next := w.st.step()
	if _, _, _, n := w.reg.count(); n != 1 || !next.Equal(w.now.Add(wakeGap)) {
		t.Fatalf("woken within a minute (%d), next %v", n, next)
	}
	w.now = next
	w.st.step()
	if _, _, _, n := w.reg.count(); n != 2 {
		t.Fatal("not woken after a minute")
	}
	// The day's cap: held until midnight.
	w.st.Inbox()
	w.quiet(id)
	w.st.AgentSettled(settled("L/a3", wire.EndingDone))
	w.now = w.now.Add(2 * time.Minute)
	next = w.st.step()
	midnight := time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)
	if _, _, _, n := w.reg.count(); n != 2 || next.After(midnight) {
		t.Fatalf("over the cap (%d), next %v", n, next)
	}
	w.now = midnight.Add(-time.Minute) // the evening event came meanwhile
	w.st.step()
	if _, _, _, n := w.reg.count(); n != 2 {
		t.Fatal("woken over the cap")
	}
	next = midnight
	if st := w.st.Status(); st.Wakes != 2 || st.Queued != 2 {
		t.Fatalf("status %+v", st)
	}
	// After midnight: the first wake rotates the session, then the new
	// one is woken (the count starts again).
	w.now = next.Add(time.Minute)
	w.st.step()
	newID := w.st.Status().Agent
	if newID == id || newID == "" || !slices.Contains(w.reg.closed, id) {
		t.Fatalf("not rotated: %s (closed %v)", newID, w.reg.closed)
	}
	w.quiet(newID)
	w.st.step()
	if got := w.reg.inputs; len(got) != 3 {
		t.Fatalf("inputs %q", got)
	}
	if got := w.reg.inputs; got[2] != newID+": "+WakeLine(2) {
		t.Fatalf("inputs %q", got)
	}
	if st := w.st.Status(); st.Wakes != 1 || st.Queued != 2 {
		t.Fatalf("status after midnight %+v", st)
	}
}

func TestQueueCoalesceSkipInbox(t *testing.T) {
	w := newWorld(t)
	w.running()
	w.st.AgentSettled(settled("L/a1", wire.EndingAsks))
	w.st.AgentSettled(settled("L/a2", wire.EndingDone))
	w.st.AgentSettled(settled("L/a1", wire.EndingWaits)) // coalesced: a1's latest
	w.st.AgentClosed(agents.AgentEvent{Agent: wire.Agent{ID: "L/a2"}, Reason: wire.ReasonClosed})
	w.st.LoopCreated("lp-2")
	w.st.ReviewChanged("L/a1", "lp-1")
	// The Steward's own work and its planners never queue.
	w.st.AgentSettled(agents.AgentEvent{Agent: wire.Agent{ID: "L/st0001", Service: wire.ServiceSteward}, Ending: wire.EndingDone})
	w.st.AgentClosed(agents.AgentEvent{Agent: wire.Agent{ID: "L/pl", Service: wire.ServicePlanner}})
	got := w.st.Inbox()
	if len(got) != 4 {
		t.Fatalf("inbox %+v", got)
	}
	if got[0].Kind != wire.StewardSettled || got[0].Agent != "L/a1" || got[0].Ending != wire.EndingWaits || got[0].Summary != "did L/a1" || got[0].Loop != "lp-1" {
		t.Fatalf("first %+v", got[0])
	}
	if got[1].Kind != wire.StewardClosed || got[1].Reason != wire.ReasonClosed || got[2].Kind != wire.StewardLoopCreated || got[3].Kind != wire.StewardReview {
		t.Fatalf("inbox %+v", got)
	}
	if again := w.st.Inbox(); again == nil || len(again) != 0 {
		t.Fatalf("inbox not emptied: %v", again)
	}
	// Bounded: the oldest dropped, counted.
	for i := range maxEvents + 44 {
		w.st.LoopCreated(fmt.Sprintf("lp-%d", i))
	}
	if st := w.st.Status(); st.Queued != maxEvents || st.Dropped != 44 {
		t.Fatalf("status %+v", st)
	}
	if first := w.st.Inbox()[0]; first.Loop != "lp-44" {
		t.Fatalf("first kept %s", first.Loop)
	}
}

func TestEvening(t *testing.T) {
	w := newWorld(t)
	w.running()
	w.st.step()
	if w.st.Status().Queued != 0 {
		t.Fatal("evening at 10:00")
	}
	w.now = time.Date(2026, 3, 2, 18, 1, 0, 0, time.UTC)
	w.st.step()
	if got := w.st.Inbox(); len(got) != 1 || got[0].Kind != wire.StewardEvening {
		t.Fatalf("inbox %+v", got)
	}
	w.st.step()
	if w.st.Status().Queued != 0 {
		t.Fatal("two evenings in a day")
	}
	// Turned on after this evening's time: the first evening is tomorrow.
	w2 := newWorld(t)
	w2.now = time.Date(2026, 3, 2, 20, 0, 0, 0, time.UTC)
	w2.running()
	w2.st.step()
	if w2.st.Status().Queued != 0 {
		t.Fatal("evening right after turning it on")
	}
}

func TestBrief(t *testing.T) {
	w := newWorld(t)
	w.store.loops = []wire.Loop{
		{ID: "lp-1", Title: "Fix the flaky badge test", Status: wire.LoopOpen, Next: wire.NextYou, NextText: "Approve the push"},
		{ID: "lp-2", Title: "Release notes", Status: wire.LoopParked, Next: wire.NextParked},
	}
	w.store.goals = wire.GoalsList{Milestones: []wire.Milestone{
		{ID: "ms-1", Title: "Beta", Status: wire.LoopOpen, Done: 1, Total: 3, Loops: []string{"lp-1"}, Stalled: true},
		{ID: "ms-2", Title: "Old", Status: wire.LoopDone},
	}}
	b := w.st.Brief()
	for _, want := range []string{"2026-03-02", "lp-1 Fix the flaky badge test [open, next: you] Approve the push", "lp-2 Release notes [parked",
		"ms-1 Beta [open, 1/3 done, 1 loops, stalled]", "hesperctl steward inbox"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief lacks %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "Old") {
		t.Errorf("brief lists a done milestone:\n%s", b)
	}
}

func TestPlan(t *testing.T) {
	w := newWorld(t)
	project := t.TempDir()
	w.st.opt.ProjectPath = func(id string) string {
		if id == "p-app" {
			return project
		}
		return ""
	}
	steward := w.running()
	a, err := w.st.Plan(wire.StewardPlanParams{Scope: "p-app", Prompt: "Focus on the beta."})
	if err != nil {
		t.Fatal(err)
	}
	if a.Service != wire.ServicePlanner || a.Parent != steward || a.Depth != 1 || a.Project != project || a.Kind != wire.KindClaude ||
		!strings.Contains(a.Task, "p-app") || !strings.Contains(a.Task, "Focus on the beta.") {
		t.Fatalf("planner %+v", a)
	}
	if p := w.reg.spawns[len(w.reg.spawns)-1]; p.LetParentAnswer {
		t.Fatal("planner answers to its parent")
	}
	// A group (no folder here): a folder of its own.
	g, err := w.st.Plan(wire.StewardPlanParams{Scope: "g-tools"})
	if err != nil || g.Project != filepath.Join(w.dir, "steward", "plans", "g-tools") {
		t.Fatalf("group planner %+v %v", g, err)
	}
	for _, bad := range []string{"", "..", "../x", "a/b"} {
		if _, err := w.st.Plan(wire.StewardPlanParams{Scope: bad}); err == nil {
			t.Errorf("scope %q accepted", bad)
		}
	}
}

func TestArgv(t *testing.T) {
	s, p := strings.Join(Argv(wire.ServiceSteward), " "), strings.Join(Argv(wire.ServicePlanner), " ")
	for _, argv := range []string{s, p} {
		if !strings.Contains(argv, "--permission-mode default") || strings.Contains(argv, "dangerously") || !strings.Contains(argv, "Bash(hesperctl steward:*)") {
			t.Fatalf("argv %s", argv)
		}
	}
	if strings.Contains(s, "git log") || !strings.Contains(p, "Bash(git log:*)") || !strings.Contains(s, "You are the Steward") || !strings.Contains(p, "planning session") {
		t.Fatal("planner and Steward options mixed up")
	}
}

// TestWatchAndCall: steward.changed first and on a change; the methods.
func TestWatchAndCall(t *testing.T) {
	w := newWorld(t)
	done := make(chan struct{})
	sent := make(chan wire.StewardChanged, 4)
	go w.st.Watch(done, func(method string, params any) error {
		if method == wire.NoteStewardChanged {
			sent <- params.(wire.StewardChanged)
		}
		return nil
	})
	defer close(done)
	if first := <-sent; first.Steward.Enabled {
		t.Fatalf("first %+v", first)
	}
	if _, err, ok := w.st.Call(wire.MethodStewardSet, []byte(`{"enabled":true}`), agents.Caller{}); err != nil || !ok {
		t.Fatal(err)
	}
	select {
	case ch := <-sent:
		if !ch.Steward.Enabled {
			t.Fatalf("change %+v", ch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no steward.changed")
	}
	if _, err, _ := w.st.Call(wire.MethodStewardSet, []byte(`{"bogus":1}`), agents.Caller{}); err == nil {
		t.Fatal("unknown field accepted")
	}
	if res, _, _ := w.st.Call(wire.MethodStewardInbox, nil, agents.Caller{}); res == nil {
		t.Fatal("inbox")
	}
	if _, _, ok := w.st.Call("steward.nope", nil, agents.Caller{}); ok {
		t.Fatal("unknown method handled")
	}
}

// TestPromptMirrored: the skill's copy of the Steward's instructions is
// the embedded prompt.
func TestPromptMirrored(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "skills", "hesper", "references", "steward.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n---\n\n"+Prompt) {
		t.Fatal("skills/hesper/references/steward.md does not end with prompt.md: copy it again")
	}
}
