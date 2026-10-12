package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Routines (routines.*: a saved `new` hesperd runs on a cadence; each run
// is a background agent whose session is in History):
//
//	routine ls | show | save | rm | pause | resume | run | runs
//
// A routine is named by its id (r-…) or its name when unique.

const routineRefHelp = "ID is the routine's id (r-…) or its name when unique."

func init() {
	for _, c := range []Command{
		{Name: "routine ls", ReadOnly: true, Summary: "List the routines (saved agents run on a cadence)", Usage: "routine ls [--json]",
			Help: "A routine is a saved new that hesperd starts on a cadence (every D, or daily at HH:MM); each run is an agent of the routine's machine, in the background unless the routine says otherwise, " +
				"closed when it settles (its session stays in History, its changes in review). A table of id, name, machine, cadence, the next run, the last run's outcome and the task's first line.",
			Output:   "[Routine]: [{id, name, machine?, spawn: {project, task, kind?, profile?, worktree?, branch?}, every?, offset?, at?, policy: {timeout, overlap, catchUp, foreground?, keepRuns}, paused?, createdBy?, created, updated, runs?, lastRun?: Run, nextRun?}]",
			Examples: []string{"hesperctl routine ls", "hesperctl routine ls --json | jq -r '.[] | select(.lastRun.outcome==\"needs-you\") | .id'"},
			Run:      routineList},
		{Name: "routine show", ReadOnly: true, Summary: "Show a routine and its last runs", Usage: "routine show ID [--json]",
			Help:     routineRefHelp + " Every field of the routine, then its last 10 runs (routine runs lists more).",
			Output:   "the routine (as in routine ls)",
			Examples: []string{"hesperctl routine show floors"},
			Run:      routineShow},
		{Name: "routine save", Summary: "Create or change a routine", Usage: "routine save [TASK…|-] [--id R] [--name N] [--project P] [--machine M] [--kind K] [--profile P] [--worktree] [--worktree-path PATH] [--branch B] [--every D] [--offset D] [--at HH:MM]… [--timeout D] [--overlap skip|replace] [--catch-up once|none] [--foreground] [--keep-runs N] [--task-file FILE] [--json]",
			Help: "Without --id: a new routine from TASK (the arguments, - for stdin, or --task-file; the text is stored), named --name or the task's first words, with exactly one cadence: --every D (a duration, 1m to 168h: at local midnight + --offset + k·D) or --at HH:MM (local, repeatable). " +
				"With --id: the flags given (and TASK, when given) change that routine; an empty value clears a field, so --every \"\" clears the interval before --at sets times, and --at \"\" clears the times. " +
				"--project is a folder here (~ and relative paths are made absolute; with --machine of another Mac, a path there as given), or a project (id or unique name: its folder on the routine's machine). " +
				"--machine is the Mac that keeps the routine and runs it (default this Mac). --kind, --profile, --worktree, --worktree-path and --branch are new's. " +
				"Policy: --timeout ends a run still working after D (default 1h); --overlap says what the clock does while the previous run is live (skip, the default, or replace it); " +
				"--catch-up says what happens when the clock fires late, the Mac asleep (once, the default: run once now; none: skip); --foreground shows the run's tile like any agent and keeps it when it settles; " +
				"--keep-runs bounds the run records kept (default 200). hesperd validates the cadence and the policy (exit 1, invalid). Prints the routine's id.",
			Output: "the routine (as in routine ls)",
			Examples: []string{"hesperctl routine save --project ~/src/app --every 1h --offset 5m --name floors \"sweep the floors: run the linter and fix what it finds\"",
				"hesperctl routine save --project app --at 07:30 --at 18:00 --kind codex --worktree --task-file nightly.md",
				"hesperctl routine save --id r-floors --every 2h --timeout 30m", "hesperctl routine save --id r-floors --every \"\" --at 09:00"},
			Run: routineSave},
		{Name: "routine rm", Destructive: true, Summary: "Remove a routine", Usage: "routine rm ID",
			Help: routineRefHelp + " Its run records go with it; a live run keeps running as an ordinary agent.",
			Run:  routineRemove},
		{Name: "routine pause", Summary: "Pause a routine: the clock stops starting it", Usage: "routine pause ID [--json]",
			Help:   routineRefHelp + " A live run is left alone; routine run still starts one by hand. Prints the routine.",
			Output: "the routine (as in routine ls)",
			Run:    routinePause(true)},
		{Name: "routine resume", Summary: "Resume a paused routine", Usage: "routine resume ID [--json]",
			Help:   routineRefHelp + " The clock runs it again from its next time. Prints the routine.",
			Output: "the routine (as in routine ls)",
			Run:    routinePause(false)},
		{Name: "routine run", Summary: "Run a routine now", Usage: "routine run ID [--json]",
			Help:     routineRefHelp + " Starts a run by hand (also while paused), as the clock would; its agent is an ordinary agent (ls shows it when it needs someone). Prints the run.",
			Output:   "Run: {seq, started, ended?, agent?, session?, outcome, state?, message?, error?, manual?}",
			Examples: []string{"hesperctl routine run floors"},
			Run:      routineRun},
		{Name: "routine runs", ReadOnly: true, Summary: "List a routine's runs, newest first", Usage: "routine runs ID [--limit N] [--json]",
			Help:     routineRefHelp + " Each run's number, start, duration, outcome (running, done, needs-you, exited, error, timed-out, skipped, failed), the agent's state when it ended, its agent and its final message or the error.",
			Output:   "[Run] (as in routine run)",
			Examples: []string{"hesperctl routine runs floors --limit 5", "hesperctl routine runs floors --json | jq -r '.[] | select(.outcome!=\"done\") | \"\\(.seq) \\(.outcome) \\(.error // .message)\"'"},
			Run:      routineRuns},
	} {
		c.Group = groupRoutines
		register(c)
	}
}

func routineList(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 0 {
			return usagef("usage: hesperctl routine ls")
		}
		var list []wire.Routine
		if err := c.Call(ctx, "routines.list", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			if list == nil {
				list = []wire.Routine{}
			}
			return output(list)
		}
		return printRoutines(os.Stdout, list, time.Now())
	})
}

// resolveRoutine finds a routine by id or unique name.
func resolveRoutine(ctx context.Context, c *wire.Client, ref string) (wire.Routine, error) {
	var list []wire.Routine
	if err := c.Call(ctx, "routines.list", nil, &list); err != nil {
		return wire.Routine{}, err
	}
	for _, r := range list {
		if r.ID == ref {
			return r, nil
		}
	}
	hits := matchByName(len(list), func(i int) string { return list[i].Name }, ref)
	switch len(hits) {
	case 1:
		return list[hits[0]], nil
	case 0:
		return wire.Routine{}, &wire.Error{Code: wire.CodeNotFound, Message: "no routine " + ref + " (routine ls lists them)"}
	}
	var ids []string
	for _, i := range hits {
		ids = append(ids, list[i].ID)
	}
	return wire.Routine{}, failf(codeAmbiguous, "%q names %d routines: use an id (%s)", ref, len(hits), strings.Join(ids, ", "))
}

func routineShow(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl routine show ID")
		}
		r, err := resolveRoutine(ctx, c, positional[0])
		if err != nil {
			return err
		}
		if *asJSON {
			return output(r)
		}
		var runs []wire.RoutineRun
		if err := c.Call(ctx, "routines.runs", wire.RoutineRunsParams{ID: r.ID, Limit: 10}, &runs); err != nil {
			return err
		}
		return printRoutine(os.Stdout, r, runs, time.Now())
	})
}

// routineFlags are routine save's flags that map onto the routine
// itself (applyRoutineFlags); the task, the project and the machine take
// the daemon.
type routineFlags struct {
	name, kind, profile, worktreePath, branch, every, offset *string
	timeout, overlap, catchUp                                *string
	worktree, foreground                                     *bool
	keepRuns                                                 *int
	at                                                       listFlag
}

func addRoutineFlags(f *flag.FlagSet) *routineFlags {
	fl := &routineFlags{
		name:         f.String("name", "", "Name (default: the task's first words); runs are agents of this name"),
		kind:         f.String("kind", "", "claude, codex or shell (default: the project's or settings' default)"),
		profile:      f.String("profile", "", "Launch profile"),
		worktree:     f.Bool("worktree", false, "Run each run in a new worktree on a new branch"),
		worktreePath: f.String("worktree-path", "", "Run in this worktree (created when missing)"),
		branch:       f.String("branch", "", "Branch of the worktree (implies --worktree)"),
		every:        f.String("every", "", "Run every `D` (1m to 168h) from local midnight; \"\" clears it"),
		offset:       f.String("offset", "", "With --every: start `D` after midnight (shorter than --every)"),
		timeout:      f.String("timeout", "", "End a run still working after `D` (1m to 24h; default 1h)"),
		overlap:      f.String("overlap", "", "While the previous run is live: skip (default) or replace"),
		catchUp:      f.String("catch-up", "", "When the clock fires late: once (default) or none"),
		foreground:   f.Bool("foreground", false, "Show each run's tile like any agent and keep it when it settles"),
		keepRuns:     f.Int("keep-runs", 0, "Run records kept (default 200, at most 1000)"),
	}
	f.Var(&fl.at, "at", "Run daily at `HH:MM` (local; repeatable); \"\" clears the times")
	return fl
}

// applyRoutineFlags puts the flags given (set) on r.
func applyRoutineFlags(r *wire.Routine, fl *routineFlags, set map[string]bool) error {
	if set["overlap"] && *fl.overlap != "" && *fl.overlap != wire.OverlapSkip && *fl.overlap != wire.OverlapReplace {
		return usagef("--overlap %q: skip or replace", *fl.overlap)
	}
	if set["catch-up"] && *fl.catchUp != "" && *fl.catchUp != wire.CatchUpOnce && *fl.catchUp != wire.CatchUpNone {
		return usagef("--catch-up %q: once or none", *fl.catchUp)
	}
	if set["name"] {
		r.Name = *fl.name
	}
	if set["kind"] {
		r.Spawn.Kind = *fl.kind
	}
	if set["profile"] {
		r.Spawn.Profile = *fl.profile
	}
	if set["branch"] {
		r.Spawn.Branch = *fl.branch
	}
	switch {
	case set["worktree-path"] && *fl.worktreePath != "":
		r.Spawn.Worktree, _ = json.Marshal(*fl.worktreePath)
	case set["worktree-path"]:
		r.Spawn.Worktree = nil
	case set["worktree"] && *fl.worktree:
		r.Spawn.Worktree = json.RawMessage("true")
	case set["worktree"]:
		r.Spawn.Worktree = nil
	}
	if set["every"] {
		r.Every = *fl.every
	}
	if set["offset"] {
		r.Offset = *fl.offset
	}
	if set["at"] {
		r.At = []string(fl.at)
	}
	if set["timeout"] {
		r.Policy.Timeout = *fl.timeout
	}
	if set["overlap"] {
		r.Policy.Overlap = *fl.overlap
	}
	if set["catch-up"] {
		r.Policy.CatchUp = *fl.catchUp
	}
	if set["foreground"] {
		r.Policy.Foreground = *fl.foreground
	}
	if set["keep-runs"] {
		r.Policy.KeepRuns = *fl.keepRuns
	}
	return nil
}

// slugName names a routine from its task: its first words, as a slug.
func slugName(task string) string {
	var words []string
	for _, w := range strings.Fields(strings.ToLower(task)) {
		w = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, w)
		if w != "" {
			words = append(words, w)
		}
		if len(words) == 4 {
			break
		}
	}
	return oneLine(strings.Join(words, "-"), 40)
}

func routineSave(ctx context.Context, f *flag.FlagSet, args []string) error {
	socket := daemonSocket(f)
	asJSON := jsonFlag(f)
	id := f.String("id", "", "Change this routine (default: a new one)")
	project := f.String("project", "", "Folder, or project (id or unique name)")
	machine := f.String("machine", "", "Machine that keeps and runs the routine (short name; default this Mac)")
	taskFile := f.String("task-file", "", "Read TASK from this file")
	fl := addRoutineFlags(f)
	positional, err := parseInterspersed(f, args)
	if err != nil {
		return err
	}
	set := setFlags(f)
	// A new routine takes the flags now (closed sets are usage errors
	// before hesperd is dialled); with --id they go on the fetched one.
	var r wire.Routine
	if err := applyRoutineFlags(&r, fl, set); err != nil {
		return err
	}
	var task string
	switch {
	case len(positional) > 0 && *taskFile != "":
		return usagef("TASK or --task-file, not both")
	case *taskFile != "":
		data, err := os.ReadFile(*taskFile)
		if err != nil {
			return err
		}
		task = string(data)
	case len(positional) > 0:
		task = strings.Join(positional, " ")
		if task == "-" {
			data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			if err != nil {
				return err
			}
			task = string(data)
		}
	}
	task = strings.TrimRight(task, "\n")
	if *id == "" && task == "" {
		return usagef("a new routine needs TASK (or --task-file)\nusage: hesperctl %s", findCommand("routine save").Usage)
	}
	c, err := dialDaemon(ctx, *socket)
	if err != nil {
		return err
	}
	defer c.Close()
	if *id != "" {
		if r, err = resolveRoutine(ctx, c, *id); err != nil {
			return err
		}
		if err := applyRoutineFlags(&r, fl, set); err != nil {
			return err
		}
	}
	if task != "" {
		r.Spawn.Task = task
	}
	if r.Name == "" {
		r.Name = slugName(task)
	}
	local := localMachine(ctx, c)
	if set["machine"] {
		r.Machine = *machine
		if r.Machine == local {
			r.Machine = ""
		}
	}
	if set["project"] {
		var p wire.ProjectInfo
		if *project != "" && !looksLikePath(*project) {
			// A project, else a folder here of that name.
			p, err = resolveProject(ctx, c, *project)
			if errorCode(err) == wire.CodeNotFound && isDir(*project) {
				p, err = wire.ProjectInfo{}, nil
			}
			if err != nil {
				return err
			}
		}
		switch {
		case *project == "":
			r.Spawn.Project = ""
		case p.ID == "":
			// This Mac's folders: absolute (hesperd wants them so).
			// Another Mac's: as given.
			r.Spawn.Project = *project
			if r.Machine == "" {
				if r.Spawn.Project, err = absPath(*project); err != nil {
					return err
				}
			}
		default:
			where := r.Machine
			if where == "" {
				where = local
			}
			folder, ok := p.Paths[where]
			if !ok {
				return failf(wire.CodeNotFound, "project %s has no folder on %s", p.Name, where)
			}
			r.Spawn.Project = folder
		}
	}
	var res wire.Routine
	if err := c.Call(ctx, "routines.save", wire.RoutineSaveParams{Routine: r}, &res); err != nil {
		return err
	}
	if *asJSON {
		return output(res)
	}
	fmt.Println(res.ID)
	return nil
}

func routineRemove(ctx context.Context, f *flag.FlagSet, args []string) error {
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl routine rm ID")
		}
		r, err := resolveRoutine(ctx, c, positional[0])
		if err != nil {
			return err
		}
		return c.Call(ctx, "routines.remove", wire.IDParams{ID: r.ID}, nil)
	})
}

// routinePause is the Run of routine pause (paused) and routine resume.
func routinePause(paused bool) func(context.Context, *flag.FlagSet, []string) error {
	return func(ctx context.Context, f *flag.FlagSet, args []string) error {
		asJSON := jsonFlag(f)
		return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
			if len(positional) != 1 {
				return usagef("usage: hesperctl routine %s ID", map[bool]string{true: "pause", false: "resume"}[paused])
			}
			r, err := resolveRoutine(ctx, c, positional[0])
			if err != nil {
				return err
			}
			var res wire.Routine
			if err := c.Call(ctx, "routines.pause", wire.RoutinePauseParams{ID: r.ID, Paused: paused}, &res); err != nil {
				return err
			}
			if *asJSON {
				return output(res)
			}
			return printRoutine(os.Stdout, res, nil, time.Now())
		})
	}
}

func routineRun(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl routine run ID")
		}
		r, err := resolveRoutine(ctx, c, positional[0])
		if err != nil {
			return err
		}
		var run wire.RoutineRun
		if err := c.Call(ctx, "routines.run", wire.IDParams{ID: r.ID}, &run); err != nil {
			return err
		}
		if *asJSON {
			return output(run)
		}
		fmt.Printf("started run %d of %s as %s\n", run.Seq, r.Name, orDash(run.Agent))
		return nil
	})
}

func routineRuns(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	limit := f.Int("limit", 0, "At most `N` runs (default 50)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl routine runs ID [--limit N]")
		}
		r, err := resolveRoutine(ctx, c, positional[0])
		if err != nil {
			return err
		}
		var runs []wire.RoutineRun
		if err := c.Call(ctx, "routines.runs", wire.RoutineRunsParams{ID: r.ID, Limit: *limit}, &runs); err != nil {
			return err
		}
		if *asJSON {
			if runs == nil {
				runs = []wire.RoutineRun{}
			}
			return output(runs)
		}
		return printRuns(os.Stdout, runs, time.Now())
	})
}

// cadence describes a routine's: "every 1h +5m", "daily 07:30, 18:00".
func cadence(r wire.Routine) string {
	switch {
	case r.Every != "":
		s := "every " + r.Every
		if r.Offset != "" && r.Offset != "0" && r.Offset != "0s" {
			s += " +" + r.Offset
		}
		return s
	case len(r.At) > 0:
		return "daily " + strings.Join(r.At, ", ")
	}
	return "-"
}

// relative is a duration as the tables show it: "now" under a minute,
// then "12m", "3h", "2d".
func relative(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// nextRun is the NEXT column: "in 12m", "due", "paused" or "-".
func nextRun(r wire.Routine, now time.Time) string {
	switch {
	case r.Paused:
		return "paused"
	case r.NextRun.IsZero():
		return "-"
	case !r.NextRun.After(now):
		return "due"
	}
	return "in " + relative(r.NextRun.Sub(now))
}

// lastRun is the LAST column: "done 12m ago", "running" or "-".
func lastRun(run *wire.RoutineRun, now time.Time) string {
	switch {
	case run == nil:
		return "-"
	case run.Outcome == wire.RunRunning:
		return wire.RunRunning
	}
	at := run.Ended
	if at.IsZero() {
		at = run.Started
	}
	return run.Outcome + " " + since(now.Sub(at))
}

// since is a past duration: "now" under a minute, else "12m ago".
func since(d time.Duration) string {
	if r := relative(d); r != "now" {
		return r + " ago"
	}
	return "now"
}

func printRoutines(w io.Writer, list []wire.Routine, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tMACHINE\tCADENCE\tNEXT\tLAST\tTASK")
	for _, r := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, orDash(r.Machine), cadence(r), nextRun(r, now), lastRun(r.LastRun, now), oneLine(r.Spawn.Task, 50))
	}
	return tw.Flush()
}

// printRoutine is routine show's text: every field, then the runs.
func printRoutine(w io.Writer, r wire.Routine, runs []wire.RoutineRun, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "%s\t%s\n", k, strings.ReplaceAll(v, "\n", " "))
		}
	}
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format("2006-01-02 15:04:05") + " (" + since(now.Sub(t)) + ")"
	}
	line("id", r.ID)
	line("name", r.Name)
	line("machine", r.Machine)
	line("cadence", cadence(r))
	switch {
	case r.Paused:
		line("paused", "yes")
	case !r.NextRun.IsZero():
		line("next", r.NextRun.Local().Format("2006-01-02 15:04:05")+" ("+nextRun(r, now)+")")
	}
	kind := r.Spawn.Kind
	if r.Spawn.Profile != "" && r.Spawn.Profile != kind {
		kind = strings.TrimSpace(kind + " (profile " + r.Spawn.Profile + ")")
	}
	line("kind", kind)
	line("project", tilde(r.Spawn.Project))
	switch wt := string(r.Spawn.Worktree); {
	case wt == "true":
		line("worktree", "new")
	case wt != "":
		var path string
		json.Unmarshal(r.Spawn.Worktree, &path)
		line("worktree", tilde(path))
	}
	line("branch", r.Spawn.Branch)
	line("task", r.Spawn.Task)
	policy := fmt.Sprintf("timeout %s, overlap %s, catch-up %s, keep %d runs", orDash(r.Policy.Timeout), orDash(r.Policy.Overlap), orDash(r.Policy.CatchUp), r.Policy.KeepRuns)
	if r.Policy.Foreground {
		policy += ", foreground"
	}
	line("policy", policy)
	line("runs", fmt.Sprint(r.Runs))
	line("last", lastRun(r.LastRun, now))
	line("created", stamp(r.Created))
	if cb := r.CreatedBy; cb != "" {
		line("createdBy", cb)
	}
	line("updated", stamp(r.Updated))
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	fmt.Fprintln(w)
	return printRuns(w, runs, now)
}

func printRuns(w io.Writer, runs []wire.RoutineRun, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SEQ\tSTARTED\tDURATION\tOUTCOME\tSTATE\tAGENT\tMESSAGE")
	for _, run := range runs {
		duration := "-"
		if !run.Ended.IsZero() {
			duration = run.Ended.Sub(run.Started).Round(time.Second).String()
		}
		message := run.Message
		if message == "" {
			message = run.Error
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", run.Seq, since(now.Sub(run.Started)), duration, run.Outcome, orDash(run.State), orDash(run.Agent), oneLine(message, 60))
	}
	return tw.Flush()
}
