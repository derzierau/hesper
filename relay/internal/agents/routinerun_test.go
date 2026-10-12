package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/ptyhost"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// saveRoutine saves a routine on the harness's project.
func (h *harness) saveRoutine(r wire.Routine) wire.Routine {
	h.t.Helper()
	if r.Spawn.Project == "" {
		r.Spawn.Project = h.project
	}
	var saved wire.Routine
	if err := h.call("routines.save", wire.RoutineSaveParams{Routine: r}, &saved); err != nil {
		h.t.Fatal(err)
	}
	return saved
}

// stepRoutines runs the scheduler's step two hours ahead: every unpaused
// routine of the tests (hourly) is due.
func (h *harness) stepRoutines() {
	h.reg.routineSched.Step(context.Background(), time.Now().Add(2*time.Hour))
}

// routine is a routine as the store has it now.
func (h *harness) routine(id string) wire.Routine {
	h.t.Helper()
	r, err := h.reg.routines.Get(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

// runAgent is the agent of a routine's run (fatal when there is none).
func (h *harness) runAgent(id string, seq int) wire.Agent {
	h.t.Helper()
	for _, a := range h.reg.List() {
		if a.Routine == id && a.RoutineRun == seq {
			return a
		}
	}
	h.t.Fatalf("no agent runs %s/%d", id, seq)
	return wire.Agent{}
}

// waitRun waits for a routine's last run to end with outcome.
func (h *harness) waitRun(id, outcome string) wire.RoutineRun {
	h.t.Helper()
	var run wire.RoutineRun
	waitFor(h.t, func() bool {
		r := h.routine(id)
		if r.LastRun == nil || r.LastRun.Outcome != outcome {
			return false
		}
		run = *r.LastRun
		return true
	})
	return run
}

// waitGone waits for an agent to leave the registry.
func (h *harness) waitGone(id string) {
	h.t.Helper()
	waitFor(h.t, func() bool { _, err := h.reg.Get(id); return err != nil })
}

func TestRoutineRunIsABackgroundAgent(t *testing.T) {
	h := newHarness(t, "FAKE_PRINT_ENV=1")
	h.saveRoutine(wire.Routine{ID: "r-tidy", Name: "Tidy", Every: "1h", Spawn: wire.SpawnParams{Task: "keep working", Name: "ignored"}})
	h.stepRoutines()
	r := h.routine("r-tidy")
	if r.LastRun == nil || r.LastRun.Seq != 1 || r.LastRun.Outcome != wire.RunRunning || r.LastRun.Manual {
		t.Fatalf("last run %+v", r.LastRun)
	}
	a := h.runAgent("r-tidy", 1)
	if a.RoutineRun != 1 || !a.Background || a.Name != "Tidy · "+r.LastRun.Started.In(time.Local).Format("15:04") || a.Task != "keep working" || a.Project != h.project {
		t.Fatalf("run agent %+v", a)
	}
	h.waitState(a.ID, wire.StateWorking)
	if !(routineRunner{h.reg}).Live("r-tidy") {
		t.Fatal("not live")
	}
	// Its environment names the routine and the run.
	data, err := os.ReadFile(filepath.Join(h.logs, strings.ReplaceAll(a.ID, "/", "_")+".env"))
	if err != nil {
		t.Fatal(err)
	}
	env := string(data)
	if !strings.Contains(env, "\nHESPER_ROUTINE_ID=r-tidy\n") || !strings.Contains(env, "\nHESPER_ROUTINE_RUN=1\n") || !strings.Contains(env, "\nHESPER_AGENT_ID="+a.ID+"\n") {
		t.Fatalf("env:\n%s", env)
	}
	// The turn ends: the run is done with the final message, the agent
	// (finished in the background) is gone.
	if err := h.call("agents.input", wire.InputParams{ID: a.ID, Text: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	run := h.waitRun("r-tidy", wire.RunDone)
	if run.Seq != 1 || run.State != wire.StateDone || run.Message != "Interrupted" || run.Agent != a.ID || run.Session != a.SessionID || run.Ended.IsZero() || run.Error != "" {
		t.Fatalf("done run %+v", run)
	}
	h.waitGone(a.ID)
	if (routineRunner{h.reg}).Live("r-tidy") {
		t.Fatal("still live")
	}
	var runs []wire.RoutineRun
	if err := h.call("routines.runs", wire.RoutineRunsParams{ID: "r-tidy"}, &runs); err != nil || len(runs) != 1 || runs[0].Outcome != wire.RunDone || runs[0].Message != "Interrupted" {
		t.Fatalf("runs %+v %v", runs, err)
	}
}

func TestRoutineRunNeedsYou(t *testing.T) {
	h := newHarness(t)
	h.saveRoutine(wire.Routine{ID: "r-push", Every: "1h", Spawn: wire.SpawnParams{Task: "approve it"}})
	h.stepRoutines()
	a := h.runAgent("r-push", 1)
	got := h.waitState(a.ID, wire.StateApproval)
	run := h.waitRun("r-push", wire.RunNeedsYou)
	if run.State != wire.StateApproval || run.Agent != a.ID || run.Session != a.SessionID || run.Message != "" || run.Error != "" {
		t.Fatalf("run %+v", run)
	}
	// Back on the walls, still listed; the end of its turn later is not
	// a second record.
	if got.Background {
		t.Fatalf("still in the background: %+v", got)
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitState(a.ID, wire.StateDone)
	var runs []wire.RoutineRun
	if err := h.call("routines.runs", wire.RoutineRunsParams{ID: "r-push"}, &runs); err != nil || len(runs) != 1 || runs[0].Outcome != wire.RunNeedsYou {
		t.Fatalf("runs %+v %v", runs, err)
	}
	if r := h.routine("r-push"); r.LastRun.Outcome != wire.RunNeedsYou || r.Runs != 1 {
		t.Fatalf("routine %+v", r)
	}
	if _, err := h.reg.Get(a.ID); err != nil {
		t.Fatal("gone")
	}
}

func TestRoutineRunTimesOut(t *testing.T) {
	h := newHarness(t)
	was := routineTimeout
	routineTimeout = func(wire.RunPolicy) time.Duration { return 300 * time.Millisecond }
	t.Cleanup(func() { routineTimeout = was })
	h.saveRoutine(wire.Routine{ID: "r-slow", Every: "1h", Spawn: wire.SpawnParams{Task: "keep working"}})
	h.stepRoutines()
	a := h.runAgent("r-slow", 1)
	run := h.waitRun("r-slow", wire.RunTimedOut)
	if run.Error != "timed out after 300ms" || run.Agent != a.ID || run.State != wire.StateWorking {
		t.Fatalf("run %+v", run)
	}
	h.waitGone(a.ID)
}

func TestRoutineRunNow(t *testing.T) {
	h := newHarness(t)
	h.saveRoutine(wire.Routine{ID: "r-hand", Every: "1h", Paused: true, Spawn: wire.SpawnParams{Task: "keep working"}})
	var run wire.RoutineRun
	if err := h.call("routines.run", wire.IDParams{ID: "r-hand"}, &run); err != nil || run.Seq != 1 || !run.Manual || run.Outcome != wire.RunRunning {
		t.Fatalf("run %+v %v", run, err)
	}
	a := h.runAgent("r-hand", 1)
	h.waitState(a.ID, wire.StateWorking)
	err := h.call("routines.run", wire.IDParams{ID: "r-hand"}, nil)
	if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeExists {
		t.Fatalf("second run: %v", err)
	}
	err = h.call("routines.run", wire.IDParams{}, nil)
	if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeInvalid {
		t.Fatalf("without id: %v", err)
	}
	// Closed by hand before it settled: exited.
	if err := h.call("agents.close", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	ended := h.waitRun("r-hand", wire.RunExited)
	if ended.State != wire.StateExited || !ended.Manual || ended.Agent != a.ID {
		t.Fatalf("ended %+v", ended)
	}
	h.waitGone(a.ID)
}

func TestRoutineRunOverlapSkipped(t *testing.T) {
	h := newHarness(t)
	h.saveRoutine(wire.Routine{ID: "r-busy", Every: "1h", Spawn: wire.SpawnParams{Task: "keep working"}})
	h.stepRoutines()
	a := h.runAgent("r-busy", 1)
	h.waitState(a.ID, wire.StateWorking)
	h.reg.routineSched.Step(context.Background(), time.Now().Add(4*time.Hour))
	var runs []wire.RoutineRun
	if err := h.call("routines.runs", wire.RoutineRunsParams{ID: "r-busy"}, &runs); err != nil || len(runs) == 0 ||
		runs[0].Seq != 2 || runs[0].Outcome != wire.RunSkipped || runs[0].Error != "run 1 is still live" {
		t.Fatalf("runs %+v %v", runs, err)
	}
	// Run 1 goes on; its end is recorded without touching the skip.
	if list := h.reg.List(); len(list) != 1 || list[0].ID != a.ID || list[0].State != wire.StateWorking {
		t.Fatalf("agents %+v", list)
	}
	if err := h.call("agents.input", wire.InputParams{ID: a.ID, Text: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitGone(a.ID)
	waitFor(t, func() bool {
		h.call("routines.runs", wire.RoutineRunsParams{ID: "r-busy"}, &runs)
		for _, run := range runs {
			if run.Seq == 1 && run.Outcome == wire.RunDone {
				return len(runs) == 2
			}
		}
		return false
	})
	if r := h.routine("r-busy"); r.LastRun.Seq != 2 || r.LastRun.Outcome != wire.RunSkipped {
		t.Fatalf("last run %+v", r.LastRun)
	}
}

func TestRoutineRunAcrossRestart(t *testing.T) {
	h := newHarness(t)
	h.saveRoutine(wire.Routine{ID: "r-back", Every: "1h", Spawn: wire.SpawnParams{Task: "keep working"}})
	h.saveRoutine(wire.Routine{ID: "r-gone", Every: "1h", Spawn: wire.SpawnParams{Task: "keep working"}})
	h.stepRoutines()
	back, gone := h.runAgent("r-back", 1), h.runAgent("r-gone", 1)
	h.waitState(back.ID, wire.StateWorking)
	h.waitState(gone.ID, wire.StateWorking)
	h.close()
	// The second run's agent does not come back (it was being closed).
	editRecord(t, h, gone.ID, func(rec map[string]any) { rec["closing"] = wire.ReasonClosed })
	h.open()
	ended := h.routine("r-gone").LastRun
	if ended.Outcome != wire.RunExited || ended.Error != "hesperd restarted" || ended.State != wire.StateExited || ended.Ended.IsZero() {
		t.Fatalf("gone run %+v", ended)
	}
	// The first is respawned with its session and re-armed: still
	// running, idle after the resume is no end; its next turn's end is.
	if r := h.routine("r-back"); r.LastRun.Outcome != wire.RunRunning {
		t.Fatalf("back run %+v", r.LastRun)
	}
	a := h.waitState(back.ID, wire.StateIdle)
	if a.Routine != "r-back" || a.RoutineRun != 1 || !a.Background {
		t.Fatalf("respawned %+v", a)
	}
	h.reg.mu.Lock()
	rn := h.reg.agents[strings.TrimPrefix(back.ID, "L/")].run
	h.reg.mu.Unlock()
	if rn == nil || rn.ended || rn.timer == nil {
		t.Fatalf("not re-armed: %+v", rn)
	}
	if r := h.routine("r-back"); r.LastRun.Outcome != wire.RunRunning {
		t.Fatalf("back run after idle %+v", r.LastRun)
	}
	if err := h.call("agents.input", wire.InputParams{ID: back.ID, Text: "go", Submit: true}, nil); err != nil {
		t.Fatal(err)
	}
	run := h.waitRun("r-back", wire.RunDone)
	if run.Seq != 1 || run.Message != "Done: go" || run.Agent != back.ID || run.Session != back.SessionID {
		t.Fatalf("done run %+v", run)
	}
	h.waitGone(back.ID)
}

func TestRoutineSpawn(t *testing.T) {
	loc := time.FixedZone("x", 2*3600)
	r := wire.Routine{ID: "r-x", Name: "Floors", Spawn: wire.SpawnParams{Machine: "M", Project: "/p", Task: "sweep", Name: "n", Kind: "codex", Profile: "pr",
		Caller: "L/c", Parent: "L/p", Depth: 2, Bring: &wire.Bring{}, Draft: "d", Branch: "b", Scratch: true, LetParentAnswer: true}}
	p := routineSpawn(r, wire.RoutineRun{Seq: 3, Started: time.Date(2026, 1, 2, 7, 5, 0, 0, time.UTC)}, loc)
	if p.Machine != "" || p.Name != "Floors · 09:05" || p.Caller != "" || p.Parent != "" || p.Depth != 0 || p.Bring != nil || p.Draft != "" {
		t.Fatalf("spawn %+v", p)
	}
	if p.Project != "/p" || p.Task != "sweep" || p.Kind != "codex" || p.Profile != "pr" || p.Branch != "b" || !p.Scratch || !p.LetParentAnswer {
		t.Fatalf("spawn lost a field: %+v", p)
	}
}

func TestRunLive(t *testing.T) {
	term := ptyhost.Exited(80, 24, nil)
	code := 0
	for _, c := range []struct {
		state string
		term  *ptyhost.Term
		exit  *wire.Exit
		want  bool
	}{
		{wire.StateStarting, term, nil, true},
		{wire.StateWorking, term, nil, true},
		{wire.StateDone, term, nil, false},
		{wire.StateIdle, term, nil, false},
		{wire.StateApproval, term, nil, false},
		{wire.StateQuestion, term, nil, false},
		{wire.StateError, term, nil, false},
		{wire.StateWorking, term, &wire.Exit{Code: &code}, false},
		{wire.StateWorking, nil, nil, false},
	} {
		a := &agent{Agent: wire.Agent{State: c.state, Exit: c.exit}, term: c.term}
		if got := runLive(a); got != c.want {
			t.Errorf("%s term=%v exit=%v: live %v, want %v", c.state, c.term != nil, c.exit != nil, got, c.want)
		}
	}
	// liveRun finds by routine.
	r := &Registry{agents: map[string]*agent{
		"a": {Agent: wire.Agent{ID: "L/a", Routine: "r-x", State: wire.StateDone}, term: term},
		"b": {Agent: wire.Agent{ID: "L/b", Routine: "r-x", State: wire.StateWorking}, term: term},
		"c": {Agent: wire.Agent{ID: "L/c", Routine: "r-y", State: wire.StateWorking}, term: term},
	}}
	if a := r.liveRun("r-x"); a == nil || a.ID != "L/b" {
		t.Fatalf("live run of r-x: %+v", a)
	}
	if a := r.liveRun("r-z"); a != nil {
		t.Fatalf("live run of r-z: %+v", a)
	}
}
