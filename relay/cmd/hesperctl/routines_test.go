package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestRoutineTables(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	hourly := wire.Routine{ID: "r-floors", Name: "floors", Every: "1h", Offset: "5m", NextRun: now.Add(12 * time.Minute),
		Spawn:   wire.SpawnParams{Task: "sweep the floors\nand mop"},
		LastRun: &wire.RoutineRun{Seq: 3, Outcome: wire.RunDone, Started: now.Add(-50 * time.Minute), Ended: now.Add(-48 * time.Minute)}}
	daily := wire.Routine{ID: "r-night", Name: "nightly", Machine: "mini", At: []string{"07:30", "18:00"}, Paused: true,
		Spawn:   wire.SpawnParams{Task: strings.Repeat("x", 80)},
		LastRun: &wire.RoutineRun{Seq: 1, Outcome: wire.RunRunning, Started: now.Add(-3 * time.Minute)}}
	fresh := wire.Routine{ID: "r-new", Name: "new", Every: "24h", NextRun: now.Add(-time.Second)}
	var out bytes.Buffer
	if err := printRoutines(&out, []wire.Routine{hourly, daily, fresh}, now); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "ID") {
		t.Fatalf("table:\n%s", out.String())
	}
	for i, want := range []string{
		"r-floors  floors   -        every 1h +5m        in 12m  done 48m ago  sweep the floors and mop",
		"r-night   nightly  mini     daily 07:30, 18:00  paused  running       " + strings.Repeat("x", 49) + "…",
		"r-new     new      -        every 24h           due     -",
	} {
		if got := strings.TrimRight(lines[i+1], " "); got != want {
			t.Errorf("row %d:\n got %q\nwant %q", i+1, got, want)
		}
	}
	if c := cadence(wire.Routine{}); c != "-" {
		t.Errorf("no cadence: %q", c)
	}
	if c := cadence(wire.Routine{Every: "15m", Offset: "0s"}); c != "every 15m" {
		t.Errorf("zero offset: %q", c)
	}
	for d, want := range map[time.Duration]string{30 * time.Second: "now", 90 * time.Second: "1m", 47 * time.Hour: "47h", 49 * time.Hour: "2d", -5 * time.Minute: "5m"} {
		if got := relative(d); got != want {
			t.Errorf("relative(%s) = %q, want %q", d, got, want)
		}
	}

	runs := []wire.RoutineRun{
		{Seq: 2, Started: now.Add(-2 * time.Minute), Outcome: wire.RunRunning, Agent: "L/ab12cd"},
		{Seq: 1, Started: now.Add(-time.Hour), Ended: now.Add(-time.Hour + 90*time.Second), Outcome: wire.RunDone, State: wire.StateDone, Agent: "L/zz99yy", Message: "Swept.\nNothing else."},
		{Seq: 0, Started: now.Add(-50 * time.Hour), Ended: now.Add(-50 * time.Hour), Outcome: wire.RunFailed, Error: "no such folder"},
	}
	out.Reset()
	if err := printRuns(&out, runs, now); err != nil {
		t.Fatal(err)
	}
	lines = strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	for i, want := range []string{
		"SEQ  STARTED  DURATION  OUTCOME  STATE  AGENT     MESSAGE",
		"2    2m ago   -         running  -      L/ab12cd",
		"1    1h ago   1m30s     done     done   L/zz99yy  Swept. Nothing else.",
		"0    2d ago   0s        failed   -      -         no such folder",
	} {
		if got := strings.TrimRight(lines[i], " "); got != want {
			t.Errorf("runs row %d:\n got %q\nwant %q", i, got, want)
		}
	}

	// show: every field, then the runs.
	out.Reset()
	hourly.Spawn.Project, hourly.Spawn.Kind, hourly.Spawn.Worktree = "/work/app", "codex", []byte("true")
	hourly.Policy = wire.RunPolicy{Timeout: "1h", Overlap: "skip", CatchUp: "once", KeepRuns: 200, Foreground: true}
	if err := printRoutine(&out, hourly, runs[:1], now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"id        r-floors", "cadence   every 1h +5m", "(in 12m)", "kind      codex", "project   /work/app", "worktree  new",
		"task      sweep the floors and mop", "policy    timeout 1h, overlap skip, catch-up once, keep 200 runs, foreground", "last      done 48m ago", "\nSEQ  ", "L/ab12cd"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("show lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRoutineFlags(t *testing.T) {
	parse := func(t *testing.T, args ...string) (*routineFlags, map[string]bool) {
		t.Helper()
		f := newFlagSet("routine save")
		fl := addRoutineFlags(f)
		if _, err := parseInterspersed(f, args); err != nil {
			t.Fatal(err)
		}
		return fl, setFlags(f)
	}
	var r wire.Routine
	fl, set := parse(t, "--name", "n", "--kind", "codex", "--profile", "p", "--worktree", "--branch", "b", "--every", "1h", "--offset", "5m",
		"--timeout", "30m", "--overlap", "replace", "--catch-up", "none", "--foreground", "--keep-runs", "10")
	if err := applyRoutineFlags(&r, fl, set); err != nil {
		t.Fatal(err)
	}
	want := wire.Routine{Name: "n", Spawn: wire.SpawnParams{Kind: "codex", Profile: "p", Worktree: []byte("true"), Branch: "b"}, Every: "1h", Offset: "5m",
		Policy: wire.RunPolicy{Timeout: "30m", Overlap: wire.OverlapReplace, CatchUp: wire.CatchUpNone, Foreground: true, KeepRuns: 10}}
	if r.Name != want.Name || r.Spawn.Kind != want.Spawn.Kind || r.Spawn.Profile != want.Spawn.Profile || string(r.Spawn.Worktree) != "true" || r.Spawn.Branch != "b" ||
		r.Every != "1h" || r.Offset != "5m" || r.Policy != want.Policy || len(r.At) != 0 {
		t.Fatalf("flags: %+v", r)
	}
	// --at repeats (and takes a comma list); an empty --every clears the
	// interval; flags not given leave the routine alone; a worktree path
	// replaces the worktree.
	fl, set = parse(t, "--at", "07:30", "--at", "18:00,23:15", "--every", "", "--worktree-path", "/w/t")
	if err := applyRoutineFlags(&r, fl, set); err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.At, " ") != "07:30 18:00 23:15" || r.Every != "" || r.Offset != "5m" || r.Name != "n" || string(r.Spawn.Worktree) != `"/w/t"` || r.Policy != want.Policy {
		t.Fatalf("change: %+v", r)
	}
	fl, set = parse(t, "--at", "", "--foreground=false")
	if err := applyRoutineFlags(&r, fl, set); err != nil {
		t.Fatal(err)
	}
	if len(r.At) != 0 || r.Policy.Foreground {
		t.Fatalf("clear: %+v", r)
	}
	for _, bad := range [][]string{{"--overlap", "lisp"}, {"--catch-up", "always"}} {
		fl, set = parse(t, bad...)
		if err := applyRoutineFlags(&r, fl, set); exitCode(err) != exitUsage {
			t.Errorf("%v: %v", bad, err)
		}
	}
	// On the command line: usage before hesperd is dialled.
	none := filepath.Join(t.TempDir(), "none.sock")
	for _, args := range [][]string{
		{"routine", "save", "--overlap", "lisp", "x"},
		{"routine", "save", "--every", "1h"}, // no task
		{"routine", "save", "--task-file", "f", "x"},
	} {
		if exit, code, _ := jsonError(t, append(args, "--json", "--daemon-socket", none)...); exit != exitUsage || code != codeUsage {
			t.Errorf("%v: exit %d %s", args, exit, code)
		}
	}
	for task, want := range map[string]string{"Sweep the floors: run the linter, fix findings": "sweep-the-floors-run", "  ": "", "Ünïcode -- dash": "ncode-dash"} {
		if got := slugName(task); got != want {
			t.Errorf("slugName(%q) = %q, want %q", task, got, want)
		}
	}
}

// The group, its tools and the reference.
func TestRoutineRegistered(t *testing.T) {
	tools := map[string]*mcpTool{}
	for _, tool := range mcpTools() {
		tools[tool.Name] = tool
	}
	for name, want := range map[string]struct{ readOnly, destructive bool }{
		"hesper_routine_ls": {true, false}, "hesper_routine_show": {true, false}, "hesper_routine_runs": {true, false},
		"hesper_routine_rm": {false, true}, "hesper_routine_run": {false, false}, "hesper_routine_save": {false, false},
		"hesper_routine_pause": {false, false}, "hesper_routine_resume": {false, false},
	} {
		tool := tools[name]
		if tool == nil {
			t.Errorf("no tool %s", name)
			continue
		}
		if tool.Command.ReadOnly != want.readOnly || tool.Command.Destructive != want.destructive || tool.Command.Group != groupRoutines {
			t.Errorf("%s: read-only %v, destructive %v, group %s", name, tool.Command.ReadOnly, tool.Command.Destructive, tool.Command.Group)
		}
	}
	// save's --at is a list, its task the positional argument.
	kinds := map[string]string{}
	for _, p := range tools["hesper_routine_save"].Params {
		kinds[p.Name] = p.Kind
	}
	if kinds["at"] != "list" || kinds["task"] != "list" || kinds["every"] != "string" || kinds["keep-runs"] != "integer" || tools["hesper_routine_save"].Args {
		t.Errorf("save params %v", kinds)
	}
	ctx := context.Background()
	overview := capture(t, func() { execute(ctx, nil, io.Discard) })
	if i, j := strings.Index(overview, "Review:"), strings.Index(overview, "Routines:"); i < 0 || j < i || !strings.Contains(overview, "  routine save ") {
		t.Errorf("overview:\n%s", overview)
	}
	md := capture(t, func() { execute(ctx, []string{"reference"}, io.Discard) })
	if !strings.Contains(md, "\n### Routines\n") || !strings.Contains(md, "\n#### routine save\n") || !strings.Contains(md, "`--catch-up string`") {
		t.Error("the reference has no Routines group")
	}
}

func TestRoutineCommands(t *testing.T) {
	e := fullDaemon(t)
	var p wire.ProjectInfo
	e.okJSON(t, &p, "projects", "promote", e.project, "--name", "app")

	// A new routine: the folder absolute, the policy's defaults filled,
	// the clock set.
	t.Chdir(e.dir)
	var r wire.Routine
	e.okJSON(t, &r, "routine", "save", "--every", "1h", "--offset", "5m", "--project", filepath.Join("home", "app"), "--name", "floors", "do", "the", "thing")
	if !strings.HasPrefix(r.ID, "r-") || r.Name != "floors" || r.Spawn.Project != e.project || r.Spawn.Task != "do the thing" || r.Every != "1h" || r.Offset != "5m" ||
		r.Machine != "" || r.Paused || r.NextRun.IsZero() || r.Created.IsZero() {
		t.Fatalf("save: %+v", r)
	}
	if d, err := time.ParseDuration(r.Policy.Timeout); err != nil || d != time.Hour || r.Policy.Overlap != wire.OverlapSkip || r.Policy.CatchUp != wire.CatchUpOnce || r.Policy.KeepRuns != 200 {
		t.Fatalf("policy defaults: %+v", r.Policy)
	}
	if out := e.ok(t, "routine", "ls"); !strings.Contains(out, r.ID) || !strings.Contains(out, "floors") || !strings.Contains(out, "every 1h +5m") || !strings.Contains(out, "in ") {
		t.Errorf("ls:\n%s", out)
	}
	// By name, by id; the project by name and a task from a file, the
	// name from the task.
	var s wire.Routine
	e.okJSON(t, &s, "routine", "show", "floors")
	if s.ID != r.ID {
		t.Fatalf("show by name: %+v", s)
	}
	if out := e.ok(t, "routine", "show", r.ID); !strings.Contains(out, "every 1h +5m") || !strings.Contains(out, "do the thing") {
		t.Errorf("show:\n%s", out)
	}
	file := filepath.Join(e.dir, "task.md")
	os.WriteFile(file, []byte("Check the nightly build\n\nand report.\n"), 0o600)
	var n wire.Routine
	e.okJSON(t, &n, "routine", "save", "--project", "app", "--at", "07:30", "--at", "18:00", "--kind", "codex", "--worktree", "--task-file", file)
	if n.ID == r.ID || n.Name != "check-the-nightly-build" || n.Spawn.Project != e.project || n.Spawn.Task != "Check the nightly build\n\nand report." ||
		strings.Join(n.At, ",") != "07:30,18:00" || n.Spawn.Kind != wire.KindCodex || string(n.Spawn.Worktree) != "true" {
		t.Fatalf("daily: %+v", n)
	}
	if out := e.ok(t, "routine", "ls"); !strings.Contains(out, "daily 07:30, 18:00") {
		t.Errorf("ls daily:\n%s", out)
	}
	// Change: only the flags given; both cadences are refused by hesperd;
	// an empty --every makes room for --at.
	var c wire.Routine
	e.okJSON(t, &c, "routine", "save", "--id", r.ID, "--timeout", "30m", "--overlap", "replace")
	if c.ID != r.ID || c.Name != "floors" || c.Every != "1h" || c.Spawn.Task != "do the thing" || c.Policy.Overlap != wire.OverlapReplace || c.Policy.CatchUp != wire.CatchUpOnce {
		t.Fatalf("change: %+v", c)
	}
	if d, _ := time.ParseDuration(c.Policy.Timeout); d != 30*time.Minute {
		t.Fatalf("timeout: %+v", c.Policy)
	}
	e.fails(t, exitError, wire.CodeInvalid, "routine", "save", "--id", r.ID, "--at", "07:30", "--at", "18:00")
	e.okJSON(t, &c, "routine", "save", "--id", "floors", "--every", "", "--offset", "", "--at", "09:00")
	if c.Every != "" || c.Offset != "" || strings.Join(c.At, ",") != "09:00" || c.NextRun.IsZero() {
		t.Fatalf("to daily: %+v", c)
	}
	e.fails(t, exitError, wire.CodeInvalid, "routine", "save", "--project", e.project, "--every", "1h", "--at", "07:30", "both")
	e.fails(t, exitError, wire.CodeInvalid, "routine", "save", "--project", e.project, "no", "cadence")
	// Pause and resume.
	e.okJSON(t, &c, "routine", "pause", "floors")
	if !c.Paused || !c.NextRun.IsZero() {
		t.Fatalf("pause: %+v", c)
	}
	if out := e.ok(t, "routine", "ls"); !strings.Contains(out, "paused") {
		t.Errorf("ls paused:\n%s", out)
	}
	if out := e.ok(t, "routine", "resume", r.ID); strings.Contains(out, "paused") || !strings.Contains(out, "next") {
		t.Errorf("resume:\n%s", out)
	}
	e.okJSON(t, &c, "routine", "show", r.ID)
	if c.Paused || c.NextRun.IsZero() {
		t.Fatalf("resume: %+v", c)
	}
	// No runs yet.
	var runs []wire.RoutineRun
	e.okJSON(t, &runs, "routine", "runs", r.ID, "--limit", "5")
	if len(runs) != 0 {
		t.Fatalf("runs: %+v", runs)
	}
	if out := e.ok(t, "routine", "runs", "floors"); !strings.HasPrefix(out, "SEQ") {
		t.Errorf("runs:\n%s", out)
	}
	// Names: ambiguous and unknown.
	e.okJSON(t, &c, "routine", "save", "--id", n.ID, "--name", "floors")
	e.fails(t, exitUsage, codeAmbiguous, "routine", "show", "floors")
	e.fails(t, exitNotFound, wire.CodeNotFound, "routine", "show", "r-nope")
	e.fails(t, exitNotFound, wire.CodeNotFound, "routine", "save", "--id", "r-nope", "--every", "2h")
	e.fails(t, exitNotFound, wire.CodeNotFound, "routine", "save", "--project", "nope", "--every", "1h", "x")
	// Another Mac: this daemon has no remote.
	if exit, _, _ := jsonError(t, "routine", "save", "--machine", "other", "--project", "/tmp/x", "--every", "1h", "x", "--json", "--daemon-socket", e.sock); exit != exitUnavailable {
		t.Errorf("--machine other: exit %d", exit)
	}
	// Remove.
	e.ok(t, "routine", "rm", r.ID)
	e.fails(t, exitNotFound, wire.CodeNotFound, "routine", "rm", r.ID)
	e.ok(t, "routine", "rm", n.ID)
	var list []wire.Routine
	e.okJSON(t, &list, "routine", "ls")
	if len(list) != 0 {
		t.Fatalf("ls after rm: %+v", list)
	}
}
