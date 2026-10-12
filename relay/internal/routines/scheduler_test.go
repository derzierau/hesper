package routines

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fakeRunner is a Runner the tests drive: every Start records its run
// and hands back a channel the test ends when it likes.
type fakeRunner struct {
	mu       sync.Mutex
	starts   []wire.RoutineRun
	routines []wire.Routine
	ends     map[string]chan wire.RoutineRun // by id/seq
	live     map[string]bool
	stops    []string
	startErr error
	stopErr  error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{ends: map[string]chan wire.RoutineRun{}, live: map[string]bool{}}
}

func runKey(id string, seq int) string { return fmt.Sprintf("%s/%d", id, seq) }

// Start names the agent "L/<id>-<seq>".
func (f *fakeRunner) Start(ctx context.Context, r wire.Routine, run wire.RoutineRun) (string, <-chan wire.RoutineRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", nil, f.startErr
	}
	f.starts = append(f.starts, run)
	f.routines = append(f.routines, r)
	ch := make(chan wire.RoutineRun, 1)
	f.ends[runKey(r.ID, run.Seq)] = ch
	f.live[r.ID] = true
	return "L/" + runKey(r.ID, run.Seq), ch, nil
}

func (f *fakeRunner) Live(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[id]
}

func (f *fakeRunner) Stop(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, id)
	if f.stopErr != nil {
		return f.stopErr
	}
	delete(f.live, id)
	return nil
}

// end finishes run seq of id: the run as started, with the given end
// fields filled in (as the Runner contract says).
func (f *fakeRunner) end(id string, seq int, end wire.RoutineRun) {
	f.mu.Lock()
	ch := f.ends[runKey(id, seq)]
	delete(f.live, id)
	var run wire.RoutineRun
	for i, r := range f.routines {
		if r.ID == id && f.starts[i].Seq == seq {
			run = f.starts[i]
		}
	}
	f.mu.Unlock()
	run.Agent, run.Session, run.Ended, run.Outcome, run.State, run.Message, run.Error = end.Agent, end.Session, end.Ended, end.Outcome, end.State, end.Message, end.Error
	ch <- run
}

func (f *fakeRunner) started() []wire.RoutineRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]wire.RoutineRun(nil), f.starts...)
}

type fixture struct {
	t      *testing.T
	c      *clock
	store  *Store
	runner *fakeRunner
	sched  *Scheduler
}

func newFixture(t *testing.T, start string) *fixture {
	t.Helper()
	c := newClock(t, start)
	store := openStore(t, "", c)
	runner := newFakeRunner()
	sched := NewScheduler(store, runner, Options{Now: c.now, Logf: t.Logf})
	return &fixture{t: t, c: c, store: store, runner: runner, sched: sched}
}

func (f *fixture) save(r wire.Routine) wire.Routine {
	f.t.Helper()
	if r.Spawn.Task == "" {
		r.Spawn = wire.SpawnParams{Task: "tidy", Project: "/srv/app"}
	}
	saved, err := f.store.Save(r)
	if err != nil {
		f.t.Fatal(err)
	}
	return saved
}

func (f *fixture) get(id string) wire.Routine {
	f.t.Helper()
	r, err := f.store.Get(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

// waitLastRun waits for the store to record the end of run seq.
func (f *fixture) waitEnded(id string, seq int) wire.RoutineRun {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r := f.get(id); r.LastRun != nil && r.LastRun.Seq == seq && r.LastRun.Outcome != wire.RunRunning {
			return *r.LastRun
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatalf("run %d of %s did not end", seq, id)
	return wire.RoutineRun{}
}

func TestSchedulerStartsDueRun(t *testing.T) {
	f := newFixture(t, "2026-03-10 09:00")
	r := f.save(wire.Routine{ID: "r-a", Every: "1h"})
	ctx := context.Background()
	// Not due yet: nothing happens.
	f.c.set(at(t, time.UTC, "2026-03-10 09:59"))
	f.sched.Step(ctx, f.c.now())
	if len(f.runner.started()) != 0 || f.get("r-a").Runs != 0 {
		t.Fatal("started early")
	}
	// Due: started, the clock advanced before the start, the run running.
	f.c.set(r.NextRun)
	f.sched.Step(ctx, f.c.now())
	starts := f.runner.started()
	if len(starts) != 1 || starts[0].Seq != 1 || starts[0].Manual || !starts[0].Started.Equal(f.c.now()) {
		t.Fatalf("starts %+v", starts)
	}
	if f.runner.routines[0].ID != "r-a" || f.runner.routines[0].Spawn.Task != "tidy" {
		t.Fatalf("routine given to Start %+v", f.runner.routines[0])
	}
	got := f.get("r-a")
	if got.Runs != 1 || got.LastRun.Outcome != wire.RunRunning || got.LastRun.Agent != "L/r-a/1" || !got.NextRun.Equal(at(t, time.UTC, "2026-03-10 11:00")) {
		t.Fatalf("after start %+v %+v", got, got.LastRun)
	}
	// The running run is a record with its agent from the start.
	if runs, _ := f.store.Runs("r-a", 0); len(runs) != 1 || runs[0].Outcome != wire.RunRunning || runs[0].Agent != "L/r-a/1" {
		t.Fatalf("running record %+v", runs)
	}
	// The same instant again: nothing more (the clock moved on).
	f.sched.Step(ctx, f.c.now())
	if len(f.runner.started()) != 1 {
		t.Fatal("started twice")
	}
	// The end is recorded with what the runner yields.
	f.c.add(10 * time.Minute)
	f.runner.end("r-a", 1, wire.RoutineRun{Agent: "L/a1", Session: "s-1", Ended: f.c.now(), Outcome: wire.RunDone, State: wire.StateDone, Message: "tidy now"})
	last := f.waitEnded("r-a", 1)
	if last.Agent != "L/a1" || last.Session != "s-1" || !last.Ended.Equal(f.c.now()) || last.State != wire.StateDone || last.Message != "tidy now" {
		t.Fatalf("ended %+v", last)
	}
	if runs, _ := f.store.Runs("r-a", 0); len(runs) != 1 || runs[0].Outcome != wire.RunDone {
		t.Fatalf("runs %+v", runs)
	}
}

func TestSchedulerOverlap(t *testing.T) {
	f := newFixture(t, "2026-03-10 09:00")
	skip := f.save(wire.Routine{ID: "r-skip", Every: "1h"})
	f.save(wire.Routine{ID: "r-replace", Every: "1h", Policy: wire.RunPolicy{Overlap: wire.OverlapReplace}})
	ctx := context.Background()
	f.c.set(skip.NextRun)
	f.sched.Step(ctx, f.c.now())
	if len(f.runner.started()) != 2 {
		t.Fatalf("starts %+v", f.runner.started())
	}
	// Both still live an hour later: skip records a skipped run and moves
	// on; replace stops the live run and starts a new one.
	f.c.add(time.Hour)
	f.sched.Step(ctx, f.c.now())
	got := f.get("r-skip")
	if got.Runs != 2 || got.LastRun.Outcome != wire.RunSkipped || got.LastRun.Error != "run 1 is still live" || !got.NextRun.Equal(f.c.t.Add(time.Hour)) {
		t.Fatalf("skip %+v %+v", got, got.LastRun)
	}
	if len(f.runner.stops) != 1 || f.runner.stops[0] != "r-replace" {
		t.Fatalf("stops %v", f.runner.stops)
	}
	got = f.get("r-replace")
	starts := f.runner.started()
	if got.Runs != 2 || got.LastRun.Outcome != wire.RunRunning || len(starts) != 3 || starts[2].Seq != 2 {
		t.Fatalf("replace %+v %+v", got, starts)
	}
	// The replaced run's late end goes to the records without touching
	// the new LastRun.
	f.runner.end("r-replace", 1, wire.RoutineRun{Outcome: wire.RunExited, Ended: f.c.now()})
	deadline := time.Now().Add(5 * time.Second)
	for {
		runs, _ := f.store.Runs("r-replace", 0)
		if len(runs) == 2 && runs[1].Seq == 1 && runs[1].Outcome == wire.RunExited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runs %+v", runs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got = f.get("r-replace"); got.LastRun.Seq != 2 || got.LastRun.Outcome != wire.RunRunning {
		t.Fatalf("last run after the old end %+v", got.LastRun)
	}
	// A Stop that fails is logged; the run still starts.
	f.runner.stopErr = errors.New("no")
	f.c.add(time.Hour)
	f.sched.Step(ctx, f.c.now())
	if got = f.get("r-replace"); got.Runs != 3 || len(f.runner.started()) != 4 {
		t.Fatalf("after a failed stop %+v", got)
	}
}

func TestSchedulerCatchUp(t *testing.T) {
	f := newFixture(t, "2026-03-10 09:00")
	once := f.save(wire.Routine{ID: "r-once", Every: "1h"})
	none := f.save(wire.Routine{ID: "r-none", Every: "1h", Policy: wire.RunPolicy{CatchUp: wire.CatchUpNone}})
	f.save(wire.Routine{ID: "r-daily", At: []string{"10:00"}, Policy: wire.RunPolicy{CatchUp: wire.CatchUpNone}})
	ctx := context.Background()
	// Three hours late (the Mac slept): once runs exactly once and resumes
	// the cadence from now; none skips and resumes; the daily At is more
	// than an hour late: skipped too.
	f.c.set(once.NextRun.Add(3 * time.Hour))
	f.sched.Step(ctx, f.c.now())
	starts := f.runner.started()
	if len(starts) != 1 {
		t.Fatalf("starts %+v", starts)
	}
	got := f.get("r-once")
	want, _ := Next(once, f.c.now(), time.UTC)
	if got.Runs != 1 || got.LastRun.Outcome != wire.RunRunning || !got.NextRun.Equal(want) {
		t.Fatalf("once %+v", got)
	}
	got = f.get("r-none")
	if got.Runs != 1 || got.LastRun.Outcome != wire.RunSkipped || got.LastRun.Error != "late by 3h0m0s" || !got.NextRun.Equal(want) {
		t.Fatalf("none %+v %+v", got, got.LastRun)
	}
	got = f.get("r-daily")
	if got.Runs != 1 || got.LastRun.Outcome != wire.RunSkipped || !got.NextRun.Equal(at(t, time.UTC, "2026-03-11 10:00")) {
		t.Fatalf("daily %+v %+v", got, got.LastRun)
	}
	// Slightly late (half a cadence) is fine for none: it runs.
	f.runner.end("r-once", 1, wire.RoutineRun{Outcome: wire.RunDone})
	f.waitEnded("r-once", 1)
	none = f.get("r-none")
	f.c.set(none.NextRun.Add(30 * time.Minute))
	f.sched.Step(ctx, f.c.now())
	if got = f.get("r-none"); got.Runs != 2 || got.LastRun.Outcome != wire.RunRunning {
		t.Fatalf("slightly late %+v %+v", got, got.LastRun)
	}
}

func TestSchedulerStartError(t *testing.T) {
	f := newFixture(t, "2026-03-10 09:00")
	r := f.save(wire.Routine{ID: "r-a", Every: "1h"})
	f.runner.startErr = errors.New("no such profile")
	f.c.set(r.NextRun)
	f.sched.Step(context.Background(), f.c.now())
	got := f.get("r-a")
	if got.Runs != 1 || got.LastRun.Outcome != wire.RunFailed || got.LastRun.Error != "no such profile" || !got.LastRun.Ended.Equal(f.c.now()) {
		t.Fatalf("failed %+v %+v", got, got.LastRun)
	}
	if !got.NextRun.Equal(f.c.t.Add(time.Hour)) {
		t.Fatalf("not advanced %v", got.NextRun)
	}
	if runs, _ := f.store.Runs("r-a", 0); len(runs) != 1 || runs[0].Outcome != wire.RunFailed {
		t.Fatalf("runs %+v", runs)
	}
	// RunNow reports the error too.
	if _, err := f.sched.RunNow(context.Background(), "r-a"); err == nil || !errors.Is(err, f.runner.startErr) {
		t.Fatalf("RunNow: %v", err)
	}
}

func TestSchedulerRunNow(t *testing.T) {
	f := newFixture(t, "2026-03-10 09:00")
	f.save(wire.Routine{ID: "r-a", Every: "1h"})
	f.store.SetPaused("r-a", true)
	ctx := context.Background()
	run, err := f.sched.RunNow(ctx, "r-a")
	if err != nil || run.Seq != 1 || !run.Manual || run.Outcome != wire.RunRunning || run.Agent != "L/r-a/1" {
		t.Fatalf("run now %+v %v", run, err)
	}
	// Paused stays paused, the clock untouched; the run is live.
	got := f.get("r-a")
	if !got.Paused || !got.NextRun.IsZero() || got.Runs != 1 || got.LastRun.Seq != 1 || !got.LastRun.Manual {
		t.Fatalf("after run now %+v", got)
	}
	if _, err := f.sched.RunNow(ctx, "r-a"); code(err) != wire.CodeExists {
		t.Fatalf("while live: %v", err)
	}
	if _, err := f.sched.RunNow(ctx, "r-nope"); code(err) != wire.CodeNotFound {
		t.Fatalf("unknown: %v", err)
	}
	f.runner.end("r-a", 1, wire.RoutineRun{Outcome: wire.RunNeedsYou, State: wire.StateApproval})
	if last := f.waitEnded("r-a", 1); last.Outcome != wire.RunNeedsYou || !last.Manual {
		t.Fatalf("ended %+v", last)
	}
	// Resumed: a manual run does not move the clock either.
	resumed, _ := f.store.SetPaused("r-a", false)
	if _, err := f.sched.RunNow(ctx, "r-a"); err != nil {
		t.Fatal(err)
	}
	if got = f.get("r-a"); !got.NextRun.Equal(resumed.NextRun) || got.Runs != 2 {
		t.Fatalf("clock moved %+v", got)
	}
}

func TestSchedulerRunLoop(t *testing.T) {
	c := &clock{t: time.Now().UTC()}
	store := openStore(t, "", c)
	runner := newFakeRunner()
	sched := NewScheduler(store, runner, Options{Now: c.now, Tick: 20 * time.Millisecond, Logf: t.Logf})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sched.Run(ctx) }()
	// Nothing due: the loop idles. A routine saved as due (its NextRun is
	// an hour on, then the clock jumps past it) is picked up by a tick;
	// one whose save makes it due within the tick wakes the loop early.
	r, _ := store.Save(wire.Routine{ID: "r-a", Every: "1h", Spawn: wire.SpawnParams{Task: "x", Project: "/p"}})
	c.set(r.NextRun.Add(time.Second))
	deadline := time.Now().Add(time.Second)
	for len(runner.started()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if starts := runner.started(); len(starts) != 1 || starts[0].Seq != 1 {
		t.Fatalf("starts %+v", starts)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}
