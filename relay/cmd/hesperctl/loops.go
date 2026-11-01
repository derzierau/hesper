package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Loops (loops.*): lasting records of each piece of work.
//
//	loops ls | show | new | rename | park | close | link | unlink | merge | split | graph | settings
//	loops item add | item check | item rm
//
// Loops are named by id, by their title (exactly, else a unique
// beginning of it) or self (the loop of the agent the command runs in).

const loopRefHelp = "LOOP is a loop's id (lp-…), its title or a unique beginning of it, or self (the loop of the agent this command runs in)."
const itemRefHelp = "ITEM is an item's number as loops show prints it (1, 2, …) or its id (it-…)."
const milestoneRefHelp = "MILESTONE is a milestone's id (ms-…), its title or a unique beginning of it."

// linkFlagsHelp is loops link's and unlink's flags.
const linkFlagsHelp = "Exactly one of --agent (an agent id; hesperd stores its ref), --session (a session ref as loops show prints it), --project, --url, --file ([MACHINE:]PATH; without MACHINE a folder on this Mac) and --loop (another loop: this one waits for it; --blocks: it waits for this one)."

func init() {
	for _, c := range []Command{
		{Name: "loops ls", ReadOnly: true, Summary: "List the loops and who acts next", Usage: "loops ls [--status S] [--milestone M] [--project P] [--json]",
			Help: "Open and parked loops, most recently changed first (--status open, parked, done, dropped or all). A table of id, title, NEXT (who acts next: you, agents, others, parked or closed, and what), the loop's agents (working/all) and its last change. " +
				"--milestone (" + milestoneRefHelp + ") and --project (" + projectRefHelp + ") narrow it.",
			Output:   "[Loop]: {id, title, goal?, doneWhen?, status, next, nextText?, looksDone?, milestone?, parkedUntil?, items:[{id, text, owner, state, kind, order, after?, ready?, source?}], links:[{kind, ref, rel, from?, label?, machine?, agent?, state?, ending?, closedReason?}], counts:{agents, working, openYou, openAgents, openOthers, sessions}, projects?, created, updated, closed?}",
			Examples: []string{"hesperctl loops ls", "hesperctl loops ls --json | jq -r '.[] | select(.next==\"you\") | .title'", "hesperctl loops ls --status all --project app"},
			Run:      loopsList},
		{Name: "loops show", ReadOnly: true, Summary: "Show a loop: who acts next, its items and links", Usage: "loops show LOOP [--json]",
			Help:   loopRefHelp + " Its status and who acts next, goal, done-when, milestone and projects, the items numbered (the numbers loops item check takes) and the links.",
			Output: "Loop (as in loops ls)", Examples: []string{"hesperctl loops show self", "hesperctl loops show 'login fix'"},
			Run: loopsShow},
		{Name: "loops new", Summary: "Start a loop by hand", Usage: "loops new TITLE… [--goal G] [--done-when D] [--milestone M] [--json]",
			Help:   "Loops form by themselves when agents start (an agent joins its parent's loop, or a new one); this makes one for work no agent has started yet. Prints its id. " + milestoneRefHelp,
			Output: "Loop", Examples: []string{"hesperctl loops new Release 1.4 --done-when 'tag pushed, notes out' --milestone 1.4"},
			Run: loopsNew},
		{Name: "loops rename", Summary: "Rename a loop", Usage: "loops rename LOOP TITLE… [--json]", Help: loopRefHelp, Output: "Loop", Run: loopsRename},
		{Name: "loops park", Summary: "Park a loop until later, or open it again", Usage: "loops park LOOP [--until WHEN] [--unpark] [--json]",
			Help:   loopRefHelp + " A parked loop leaves your move until --until (tomorrow, a duration 3d, 2w, 12h, or a date 2026-11-04: 09:00 that day); without --until until it is opened again. --unpark opens it again.",
			Output: "Loop", Examples: []string{"hesperctl loops park 'docs pass' --until tomorrow", "hesperctl loops park lp-1a2b3c4d5e6f --unpark"},
			Run: loopsPark},
		{Name: "loops close", Destructive: true, Summary: "Close a loop: done or dropped", Usage: "loops close LOOP --done|--drop [--json]",
			Help:   loopRefHelp + " Only a person closes a loop (an agent is refused: forbidden); closing, tidying or moving agents never does. Its agents keep running.",
			Output: "Loop", Examples: []string{"hesperctl loops close 'login fix' --done"},
			Run: loopsClose},
		{Name: "loops link", Summary: "Link an agent, session, project, URL, file or loop to a loop", Usage: "loops link LOOP [--agent ID] [--session REF] [--project P] [--url U] [--file F] [--loop L] [--blocks] [--rel R] [--label T] [--json]",
			Help: loopRefHelp + " " + linkFlagsHelp + " --rel sets the relation (member, started, continued, handoff, blocks, blockedBy, touches; the default fits the kind), --label a name for it. " +
				"An agent may change only its own loop's links and items.",
			Output: "Loop", Examples: []string{"hesperctl loops link self --url https://github.com/me/app/pull/482 --label 'PR #482'", "hesperctl loops link 'release 1.4' --loop 'login fix'"},
			Run: loopsLinkRun(false)},
		{Name: "loops unlink", Summary: "Remove a link from a loop", Usage: "loops unlink LOOP [--agent ID] [--session REF] [--project P] [--url U] [--file F] [--loop L] [--rel R] [--json]",
			Help:   loopRefHelp + " " + linkFlagsHelp + " Without --rel every relation to it goes.",
			Output: "Loop", Run: loopsLinkRun(true)},
		{Name: "loops item add", Summary: "Add a step, loose end, promise, question or offer to a loop", Usage: "loops item add LOOP TEXT… [--owner O] [--kind K] [--after ITEM]… [--json]",
			Help: loopRefHelp + " --owner says who does it: you (the person, the default), agent or others; --kind is step (the default), looseEnd, promise, question or offer. --after names items that must be done first (" + itemRefHelp + "). " +
				"An agent puts its follow-ups here: hesperctl loops item add self --owner agent ….",
			Output: "Loop", Examples: []string{"hesperctl loops item add self --owner you merge PR #482 once CI is green", "hesperctl loops item add self --owner agent --kind promise update the changelog after the merge"},
			Run: loopsItemAdd},
		{Name: "loops item check", Summary: "Tick off an item of a loop", Usage: "loops item check LOOP ITEM [--drop] [--undo] [--json]",
			Help:   loopRefHelp + " " + itemRefHelp + " --drop marks it dropped instead of done, --undo opens it again.",
			Output: "Loop", Examples: []string{"hesperctl loops item check self 2"},
			Run: loopsItemCheck},
		{Name: "loops item rm", Destructive: true, Summary: "Remove an item from a loop", Usage: "loops item rm LOOP ITEM [--json]", Help: loopRefHelp + " " + itemRefHelp, Output: "Loop", Run: loopsItemRemove},
		{Name: "loops merge", Summary: "Merge one loop into another", Usage: "loops merge INTO FROM [--json]",
			Help:   "INTO and FROM are loops (" + loopRefHelp + ") FROM's links and items move to INTO; FROM is listed no more (loops show FROM names INTO).",
			Output: "Loop (INTO)", Run: loopsMerge},
		{Name: "loops split", Summary: "Move an agent and what it started into a loop of its own", Usage: "loops split LOOP REF [--title T] [--json]",
			Help:   loopRefHelp + " REF is a link's ref as loops show prints it, or a member agent's id or name: it, the agents it started or that continued it, and their items move to a new loop titled --title.",
			Output: "Loop (the new one)", Run: loopsSplit},
		{Name: "loops graph", ReadOnly: true, Summary: "Print a loop's work graph as a tree", Usage: "loops graph LOOP [--json]",
			Help:   loopRefHelp + " Its agents and sessions as a tree by who started or continued whom, each with machine and state (or how it was closed) and how its last turn ended; the files handed over under the agent that wrote them; then the loops it blocks and waits for.",
			Output: "{id, title, nodes:[{ref, kind, rel, label?, machine?, agent?, state?, ending?, closedReason?, files?:[ref], children?:[node]}], blocks?:[{id, title?}], blockedBy?:[{id, title?}]}",
			Run:    loopsGraph},
		{Name: "loops settings", Summary: "Show or change the loop settings", Usage: "loops settings [--loop-limit N] [--stall-days N] [--evening HH:MM] [--max-wakes N] [--json]",
			Help:   "Shared by every Mac. --loop-limit: loops on you before the start check turns red (a hint, never a block); --stall-days: a milestone with no change for this long is stalled; --evening: the wrap-up time; --max-wakes: the Steward's wakes a day. Without flags it prints them; changing them is a person's.",
			Output: "{loopLimit, stallDays, eveningAt, maxWakesPerDay}", Run: loopsSettings},
	} {
		c.Group = groupLoops
		register(c)
	}
}

// resolveLoop finds a loop by id (merged loops too), self, exact title
// (case ignored) or a unique beginning of its title, among every loop.
func resolveLoop(ctx context.Context, c *wire.Client, ref string) (wire.Loop, error) {
	if ref == "self" {
		id, err := selfID()
		if err != nil {
			return wire.Loop{}, err
		}
		var list []wire.Loop
		if err := c.Call(ctx, wire.MethodLoopsList, wire.LoopsListParams{Agent: id, Status: "all"}, &list); err != nil {
			return wire.Loop{}, err
		}
		if len(list) == 0 {
			return wire.Loop{}, failf(wire.CodeNotFound, "agent %s is in no loop", id)
		}
		return list[0], nil
	}
	if strings.HasPrefix(ref, "lp-") {
		var l wire.Loop
		err := c.Call(ctx, wire.MethodLoopsGet, wire.IDParams{ID: ref}, &l)
		if err == nil || errorCode(err) != wire.CodeNotFound {
			return l, err
		}
	}
	var list []wire.Loop
	if err := c.Call(ctx, wire.MethodLoopsList, wire.LoopsListParams{Status: "all"}, &list); err != nil {
		return wire.Loop{}, err
	}
	hits := matchTitle(len(list), func(i int) string { return list[i].Title }, ref)
	switch len(hits) {
	case 0:
		return wire.Loop{}, failf(wire.CodeNotFound, "no loop %s (loops ls lists them)", ref)
	case 1:
		return list[hits[0]], nil
	}
	var ids []string
	for _, i := range hits {
		ids = append(ids, list[i].ID)
	}
	return wire.Loop{}, failf(codeAmbiguous, "%q names %d loops: use an id (%s)", ref, len(hits), strings.Join(ids, ", "))
}

// matchTitle is the indexes of the n items titled ref (matchByName), else
// those whose title begins with it, case ignored.
func matchTitle(n int, title func(int) string, ref string) []int {
	if hits := matchByName(n, title, ref); len(hits) > 0 {
		return hits
	}
	var prefix []int
	low := strings.ToLower(ref)
	for i := range n {
		if low != "" && strings.HasPrefix(strings.ToLower(title(i)), low) {
			prefix = append(prefix, i)
		}
	}
	return prefix
}

// resolveMilestone finds a milestone of any product by id, title or a
// unique beginning of it.
func resolveMilestone(ctx context.Context, c *wire.Client, ref string) (wire.Milestone, error) {
	var gl wire.GoalsList
	if err := c.Call(ctx, wire.MethodGoalsList, wire.GoalsListParams{}, &gl); err != nil {
		return wire.Milestone{}, err
	}
	list := gl.Milestones
	for _, m := range list {
		if m.ID == ref {
			return m, nil
		}
	}
	hits := matchTitle(len(list), func(i int) string { return list[i].Title }, ref)
	switch len(hits) {
	case 0:
		return wire.Milestone{}, failf(wire.CodeNotFound, "no milestone %s (goals ls lists them)", ref)
	case 1:
		return list[hits[0]], nil
	}
	var ids []string
	for _, i := range hits {
		ids = append(ids, list[i].ID)
	}
	return wire.Milestone{}, failf(codeAmbiguous, "%q names %d milestones: use an id (%s)", ref, len(hits), strings.Join(ids, ", "))
}

// sortedItems are a loop's or milestone's items in their order: the
// numbers loops show prints and ITEM takes.
func sortedItems(items []wire.LoopItem) []wire.LoopItem {
	list := append([]wire.LoopItem(nil), items...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].Order < list[j].Order })
	return list
}

// resolveItem finds an item by number (sortedItems, from 1) or id.
func resolveItem(items []wire.LoopItem, ref string) (wire.LoopItem, error) {
	list := sortedItems(items)
	if n, err := strconv.Atoi(ref); err == nil {
		if n < 1 || n > len(list) {
			return wire.LoopItem{}, failf(wire.CodeNotFound, "no item %d (there are %d)", n, len(list))
		}
		return list[n-1], nil
	}
	for _, it := range list {
		if it.ID == ref {
			return it, nil
		}
	}
	return wire.LoopItem{}, failf(wire.CodeNotFound, "no item %s", ref)
}

// parseWhen turns a future time (tomorrow, 3d, 2w, 12h, 2026-11-04,
// RFC 3339) into a time; a day is hour:00 local of it.
func parseWhen(name, s string, now time.Time, hour int) (time.Time, error) {
	at := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, time.Local)
	}
	if s == "tomorrow" {
		return at(now.AddDate(0, 0, 1)), nil
	}
	if m := dayDuration.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		if m[2] == "w" {
			n *= 7
		}
		return now.AddDate(0, 0, n), nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(d), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return at(t), nil
	}
	return time.Time{}, usagef("--%s %q: want tomorrow, a duration (12h, 3d, 2w) or a date (2026-11-04, RFC 3339)", name, s)
}

// nextColumn is who acts next and what, for a table.
func nextColumn(l wire.Loop) string {
	s := orDash(l.Next)
	switch {
	case l.Next == wire.NextParked && !l.ParkedUntil.IsZero():
		s += " until " + l.ParkedUntil.Local().Format("2006-01-02 15:04")
	case l.Next == wire.NextClosed:
		s = l.Status
	case l.NextText != "":
		s += ": " + l.NextText
	}
	return oneLine(s, 60)
}

// printLoop prints a loop's id, or with --json the loop.
func printLoop(asJSON bool, l wire.Loop) error {
	if asJSON {
		return output(l)
	}
	fmt.Println(l.ID)
	return nil
}

func loopsList(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	status := f.String("status", "", "open, parked, done, dropped or all (default open and parked)")
	milestone := f.String("milestone", "", "Only the loops filed under this milestone")
	project := f.String("project", "", "Only the loops working in this project")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 0 {
			return usagef("usage: hesperctl loops ls [--status S] [--milestone M] [--project P]")
		}
		if *status != "" && !contains([]string{wire.LoopOpen, wire.LoopParked, wire.LoopDone, wire.LoopDropped, "all"}, *status) {
			return usagef("--status %q: open, parked, done, dropped or all", *status)
		}
		p := wire.LoopsListParams{Status: *status}
		if *milestone != "" {
			m, err := resolveMilestone(ctx, c, *milestone)
			if err != nil {
				return err
			}
			p.Milestone = m.ID
		}
		if *project != "" {
			pr, err := resolveProject(ctx, c, *project)
			if err != nil {
				return err
			}
			p.Project = pr.ID
		}
		list := []wire.Loop{}
		if err := c.Call(ctx, wire.MethodLoopsList, p, &list); err != nil {
			return err
		}
		if *asJSON {
			if list == nil {
				list = []wire.Loop{}
			}
			return output(list)
		}
		return printLoops(os.Stdout, list)
	})
}

func printLoops(w io.Writer, list []wire.Loop) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTITLE\tNEXT\tAGENTS\tUPDATED")
	for _, l := range list {
		agents := "-"
		if l.Counts.Agents > 0 {
			agents = fmt.Sprintf("%d/%d", l.Counts.Working, l.Counts.Agents)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", l.ID, oneLine(l.Title, 40), nextColumn(l), agents, ago(l.Updated))
	}
	return tw.Flush()
}

func loopsShow(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl loops show LOOP")
		}
		l, err := resolveLoop(ctx, c, positional[0])
		if err != nil {
			return err
		}
		if *asJSON {
			return output(l)
		}
		names := map[string]string{}
		var projects []wire.ProjectInfo
		if c.Call(ctx, "projects.list", nil, &projects) == nil {
			for _, p := range projects {
				names[p.ID] = p.Name
			}
		}
		if l.Milestone != "" {
			if m, err := resolveMilestone(ctx, c, l.Milestone); err == nil {
				names[m.ID] = m.Title
			}
		}
		return printLoopDetail(os.Stdout, l, names)
	})
}

// printLoopDetail is loops show: names are project and milestone names
// by id.
func printLoopDetail(w io.Writer, l wire.Loop, names map[string]string) error {
	named := func(id string) string {
		if n := names[id]; n != "" {
			return n + " (" + id + ")"
		}
		return id
	}
	fmt.Fprintf(w, "%s  %s\n", l.ID, l.Title)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "status\t%s\n", l.Status)
	fmt.Fprintf(tw, "next\t%s\n", nextColumn(l))
	if l.MergedInto != "" {
		fmt.Fprintf(tw, "merged into\t%s\n", l.MergedInto)
	}
	if l.Goal != "" {
		fmt.Fprintf(tw, "goal\t%s\n", l.Goal)
	}
	if l.DoneWhen != "" {
		fmt.Fprintf(tw, "done when\t%s\n", l.DoneWhen)
	}
	if l.Milestone != "" {
		fmt.Fprintf(tw, "milestone\t%s\n", named(l.Milestone))
	}
	if len(l.Projects) > 0 {
		var ps []string
		for _, p := range l.Projects {
			ps = append(ps, named(p))
		}
		fmt.Fprintf(tw, "projects\t%s\n", strings.Join(ps, ", "))
	}
	fmt.Fprintf(tw, "updated\t%s\n", ago(l.Updated))
	tw.Flush()
	if items := sortedItems(l.Items); len(items) > 0 {
		fmt.Fprintln(w, "\nItems:")
		tw = tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		for i, it := range items {
			fmt.Fprintf(tw, "  %d.\t%s\t%s\t%s\t%s\n", i+1, itemBox(it.State), it.Owner, it.Kind, oneLine(it.Text, 80))
		}
		tw.Flush()
	}
	if len(l.Links) > 0 {
		fmt.Fprintln(w, "\nLinks:")
		tw = tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		for _, k := range l.Links {
			name := k.Ref
			if k.Kind == wire.LinkProject {
				name = named(k.Ref)
			} else if k.Label != "" {
				name = k.Label + " (" + k.Ref + ")"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", k.Kind, k.Rel, name, linkState(k))
		}
		tw.Flush()
	}
	return nil
}

func itemBox(state string) string {
	switch state {
	case wire.ItemDone:
		return "[x]"
	case wire.ItemDropped:
		return "[-]"
	}
	return "[ ]"
}

// linkState is an agent or session link's machine, state (or how it was
// closed) and ending: "L/a7f3k2 working", "closed · done".
func linkState(k wire.LoopLink) string {
	var parts []string
	switch {
	case k.Agent != "":
		parts = append(parts, k.Agent)
	case k.Machine != "":
		parts = append(parts, "on "+k.Machine)
	}
	switch {
	case k.State != "":
		parts = append(parts, k.State)
	case k.ClosedReason != "":
		parts = append(parts, k.ClosedReason)
	case k.Kind == wire.LinkAgent:
		parts = append(parts, "gone")
	}
	if k.Ending != "" {
		parts = append(parts, "· "+k.Ending)
	}
	return strings.Join(parts, " ")
}

func loopsNew(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	goal := f.String("goal", "", "What the loop is for")
	doneWhen := f.String("done-when", "", "When it is done")
	milestone := f.String("milestone", "", "File it under this milestone")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		title := strings.TrimSpace(strings.Join(positional, " "))
		if title == "" {
			return usagef("usage: hesperctl loops new TITLE…")
		}
		p := wire.LoopSaveParams{Title: &title}
		set := setFlags(f)
		if set["goal"] {
			p.Goal = goal
		}
		if set["done-when"] {
			p.DoneWhen = doneWhen
		}
		if *milestone != "" {
			m, err := resolveMilestone(ctx, c, *milestone)
			if err != nil {
				return err
			}
			p.Milestone = &m.ID
		}
		var l wire.Loop
		if err := c.Call(ctx, wire.MethodLoopsSave, p, &l); err != nil {
			return err
		}
		return printLoop(*asJSON, l)
	})
}

// saveLoop resolves positional[0] and saves p (its ID set) on it.
func saveLoop(ctx context.Context, c *wire.Client, ref string, p wire.LoopSaveParams, asJSON bool) error {
	l, err := resolveLoop(ctx, c, ref)
	if err != nil {
		return err
	}
	p.ID = l.ID
	if err := c.Call(ctx, wire.MethodLoopsSave, p, &l); err != nil {
		return err
	}
	return printLoop(asJSON, l)
}

func loopsRename(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) < 2 {
			return usagef("usage: hesperctl loops rename LOOP TITLE…")
		}
		title := strings.TrimSpace(strings.Join(positional[1:], " "))
		if title == "" {
			return usagef("loops rename: the title is empty")
		}
		return saveLoop(ctx, c, positional[0], wire.LoopSaveParams{Title: &title}, *asJSON)
	})
}

func loopsPark(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	until := f.String("until", "", "Until `WHEN`: tomorrow, a duration (3d, 2w, 12h) or a date")
	unpark := f.Bool("unpark", false, "Open the loop again")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl loops park LOOP [--until WHEN] [--unpark]")
		}
		if *unpark && *until != "" {
			return usagef("--unpark opens the loop: no --until")
		}
		status, when := wire.LoopParked, time.Time{}
		if *unpark {
			status = wire.LoopOpen
		} else if *until != "" {
			var err error
			if when, err = parseWhen("until", *until, time.Now(), 9); err != nil {
				return err
			}
		}
		return saveLoop(ctx, c, positional[0], wire.LoopSaveParams{Status: &status, ParkedUntil: &when}, *asJSON)
	})
}

func loopsClose(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	done := f.Bool("done", false, "The work is done")
	drop := f.Bool("drop", false, "The work is dropped")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 || *done == *drop {
			return usagef("usage: hesperctl loops close LOOP --done|--drop")
		}
		status := wire.LoopDone
		if *drop {
			status = wire.LoopDropped
		}
		return saveLoop(ctx, c, positional[0], wire.LoopSaveParams{Status: &status}, *asJSON)
	})
}

// loopsLinkRun is loops link (remove false) and loops unlink.
func loopsLinkRun(remove bool) func(context.Context, *flag.FlagSet, []string) error {
	return func(ctx context.Context, f *flag.FlagSet, args []string) error {
		asJSON := jsonFlag(f)
		agent := f.String("agent", "", "An agent (`ID`)")
		session := f.String("session", "", "A session (its `REF`)")
		project := f.String("project", "", "A project")
		url := f.String("url", "", "A URL (a PR, an artifact)")
		file := f.String("file", "", "A file: [MACHINE:]PATH")
		loop := f.String("loop", "", "Another loop")
		rel := f.String("rel", "", "The relation: member, started, continued, handoff, blocks, blockedBy, touches")
		var label *string
		var blocksFlag *bool
		if !remove {
			blocksFlag = f.Bool("blocks", false, "With --loop: that loop waits for this one (default: this one waits for it)")
			label = f.String("label", "", "A name for the link")
		}
		return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
			use := "usage: hesperctl loops link LOOP --agent ID|--session REF|--project P|--url U|--file F|--loop L"
			if remove {
				use = strings.Replace(use, "link", "unlink", 1)
			}
			set := setFlags(f)
			n := 0
			for _, name := range []string{"agent", "session", "project", "url", "file", "loop"} {
				if set[name] {
					n++
				}
			}
			if len(positional) != 1 || n != 1 {
				return usagef("%s (exactly one)", use)
			}
			l, err := resolveLoop(ctx, c, positional[0])
			if err != nil {
				return err
			}
			k := wire.LoopLink{Rel: *rel}
			if label != nil {
				k.Label = *label
			}
			switch {
			case set["agent"]:
				id, err := resolveAgent(ctx, c, *agent)
				if err != nil {
					return err
				}
				k.Kind, k.Agent = wire.LinkAgent, id
			case set["session"]:
				k.Kind, k.Ref = wire.LinkSession, *session
			case set["project"]:
				p, err := resolveProject(ctx, c, *project)
				if err != nil {
					return err
				}
				k.Kind, k.Ref = wire.LinkProject, p.ID
			case set["url"]:
				k.Kind, k.Ref = wire.LinkURL, *url
			case set["file"]:
				ref, err := fileRef(ctx, c, *file)
				if err != nil {
					return err
				}
				k.Kind, k.Ref = wire.LinkFile, ref
			case set["loop"]:
				other, err := resolveLoop(ctx, c, *loop)
				if err != nil {
					return err
				}
				k.Kind, k.Ref = wire.LinkLoop, other.ID
				if blocksFlag != nil && *blocksFlag {
					if *rel != "" {
						return usagef("--blocks is --rel blocks: not both")
					}
					k.Rel = wire.RelBlocks
				}
			}
			if blocksFlag != nil && *blocksFlag && k.Kind != wire.LinkLoop {
				return usagef("--blocks goes with --loop")
			}
			if err := c.Call(ctx, wire.MethodLoopsLink, wire.LoopLinkParams{ID: l.ID, Link: k, Remove: remove}, &l); err != nil {
				return err
			}
			return printLoop(*asJSON, l)
		})
	}
}

// fileRef is a file link's ref, "<machine>:<absolute path>"; a path
// without a machine is on this Mac.
func fileRef(ctx context.Context, c *wire.Client, s string) (string, error) {
	if machine, path, ok := strings.Cut(s, ":"); ok && machine != "" && !strings.Contains(machine, "/") {
		if !strings.HasPrefix(path, "/") {
			return "", usagef("--file %q: another Mac's file needs an absolute path", s)
		}
		return machine + ":" + path, nil
	}
	path, err := expandPath(s)
	if err != nil {
		return "", err
	}
	machine := localMachine(ctx, c)
	if machine == "" {
		return "", failf(wire.CodeUnavailable, "hesperd did not say this Mac's name")
	}
	return machine + ":" + path, nil
}

func loopsItemAdd(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	owner := f.String("owner", wire.OwnerYou, "Who does it: you, agent or others")
	kind := f.String("kind", wire.ItemStep, "step, looseEnd, promise, question or offer")
	var after listFlag
	f.Var(&after, "after", "An `ITEM` that must be done first (repeatable)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		text := ""
		if len(positional) >= 2 {
			text = strings.TrimSpace(strings.Join(positional[1:], " "))
		}
		if text == "" {
			return usagef("usage: hesperctl loops item add LOOP TEXT… [--owner O] [--kind K] [--after ITEM]…")
		}
		if !contains([]string{wire.OwnerYou, wire.OwnerAgent, wire.OwnerOthers}, *owner) {
			return usagef("--owner %q: you, agent or others", *owner)
		}
		if !contains([]string{wire.ItemStep, wire.ItemLooseEnd, wire.ItemPromise, wire.ItemQuestion, wire.ItemOffer}, *kind) {
			return usagef("--kind %q: step, looseEnd, promise, question or offer", *kind)
		}
		l, err := resolveLoop(ctx, c, positional[0])
		if err != nil {
			return err
		}
		it := wire.LoopItem{Text: text, Owner: *owner, Kind: *kind}
		for _, ref := range after {
			first, err := resolveItem(l.Items, ref)
			if err != nil {
				return err
			}
			it.After = append(it.After, first.ID)
		}
		if err := c.Call(ctx, wire.MethodLoopsItem, wire.LoopItemParams{ID: l.ID, Item: it}, &l); err != nil {
			return err
		}
		return printLoop(*asJSON, l)
	})
}

// itemState is the state loops item check and goals check set.
func itemState(drop, undo bool) (string, error) {
	switch {
	case drop && undo:
		return "", usagef("--drop or --undo, not both")
	case drop:
		return wire.ItemDropped, nil
	case undo:
		return wire.ItemOpen, nil
	}
	return wire.ItemDone, nil
}

func loopsItemCheck(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	drop := f.Bool("drop", false, "Mark it dropped instead of done")
	undo := f.Bool("undo", false, "Open it again")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 2 {
			return usagef("usage: hesperctl loops item check LOOP ITEM [--drop] [--undo]")
		}
		state, err := itemState(*drop, *undo)
		if err != nil {
			return err
		}
		l, err := resolveLoop(ctx, c, positional[0])
		if err != nil {
			return err
		}
		it, err := resolveItem(l.Items, positional[1])
		if err != nil {
			return err
		}
		if err := c.Call(ctx, wire.MethodLoopsItem, wire.LoopItemParams{ID: l.ID, Item: wire.LoopItem{ID: it.ID, State: state}}, &l); err != nil {
			return err
		}
		return printLoop(*asJSON, l)
	})
}

func loopsItemRemove(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 2 {
			return usagef("usage: hesperctl loops item rm LOOP ITEM")
		}
		l, err := resolveLoop(ctx, c, positional[0])
		if err != nil {
			return err
		}
		it, err := resolveItem(l.Items, positional[1])
		if err != nil {
			return err
		}
		if err := c.Call(ctx, wire.MethodLoopsItem, wire.LoopItemParams{ID: l.ID, Item: wire.LoopItem{ID: it.ID}, Remove: true}, &l); err != nil {
			return err
		}
		return printLoop(*asJSON, l)
	})
}

func loopsMerge(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 2 {
			return usagef("usage: hesperctl loops merge INTO FROM")
		}
		into, err := resolveLoop(ctx, c, positional[0])
		if err != nil {
			return err
		}
		from, err := resolveLoop(ctx, c, positional[1])
		if err != nil {
			return err
		}
		var l wire.Loop
		if err := c.Call(ctx, wire.MethodLoopsMerge, wire.LoopMergeParams{Into: into.ID, From: from.ID}, &l); err != nil {
			return err
		}
		return printLoop(*asJSON, l)
	})
}

func loopsSplit(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	title := f.String("title", "", "The new loop's title (default: from the agent)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 2 {
			return usagef("usage: hesperctl loops split LOOP REF [--title T]")
		}
		l, err := resolveLoop(ctx, c, positional[0])
		if err != nil {
			return err
		}
		ref, err := splitRef(ctx, c, l, positional[1])
		if err != nil {
			return err
		}
		var n wire.Loop
		if err := c.Call(ctx, wire.MethodLoopsSplit, wire.LoopSplitParams{ID: l.ID, Ref: ref, Title: *title}, &n); err != nil {
			return err
		}
		return printLoop(*asJSON, n)
	})
}

// splitRef is the link ref of a loop that ref names: the ref itself, or
// a member agent by id or name.
func splitRef(ctx context.Context, c *wire.Client, l wire.Loop, ref string) (string, error) {
	for _, k := range l.Links {
		if k.Ref == ref {
			return ref, nil
		}
	}
	id := ""
	if a, err := resolveAgent(ctx, c, ref); err == nil {
		id = a
	}
	var hits []string
	for _, k := range l.Links {
		if k.Kind == wire.LinkAgent && (k.Agent != "" && k.Agent == id || k.Label == ref) && !contains(hits, k.Ref) {
			hits = append(hits, k.Ref)
		}
	}
	switch len(hits) {
	case 0:
		return "", failf(wire.CodeNotFound, "loop %s has no link %s (loops show lists them)", l.ID, ref)
	case 1:
		return hits[0], nil
	}
	return "", failf(codeAmbiguous, "%q names %d links of loop %s: use a ref (%s)", ref, len(hits), l.ID, strings.Join(hits, ", "))
}

// graphNode is an agent or session of loops graph, with the files it
// handed over and the members it started or that continued it.
type graphNode struct {
	Ref          string       `json:"ref"`
	Kind         string       `json:"kind"`
	Rel          string       `json:"rel"`
	Label        string       `json:"label,omitempty"`
	Machine      string       `json:"machine,omitempty"`
	Agent        string       `json:"agent,omitempty"`
	State        string       `json:"state,omitempty"`
	Ending       string       `json:"ending,omitempty"`
	ClosedReason string       `json:"closedReason,omitempty"`
	Files        []string     `json:"files,omitempty"`
	Children     []*graphNode `json:"children,omitempty"`
}

type graphLoop struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

type loopGraph struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Nodes     []*graphNode `json:"nodes"`
	Blocks    []graphLoop  `json:"blocks,omitempty"`
	BlockedBy []graphLoop  `json:"blockedBy,omitempty"`
}

// buildGraph turns a loop's links into its work graph: a node per agent
// or session ref (the facts of all its links joined), children by
// started and continued links, handoff files under the agent that wrote
// them; members whose parent is not in the loop are roots. Files with no
// writer here are root nodes of kind file.
func buildGraph(l wire.Loop) loopGraph {
	g := loopGraph{ID: l.ID, Title: l.Title, Nodes: []*graphNode{}}
	nodes := map[string]*graphNode{}
	var order []string
	parent := map[string]string{}
	for _, k := range l.Links {
		if k.Kind != wire.LinkAgent && k.Kind != wire.LinkSession {
			continue
		}
		n := nodes[k.Ref]
		if n == nil {
			n = &graphNode{Ref: k.Ref, Kind: k.Kind, Rel: wire.RelMember}
			nodes[k.Ref] = n
			order = append(order, k.Ref)
		}
		for _, v := range []struct {
			to   *string
			from string
		}{{&n.Label, k.Label}, {&n.Machine, k.Machine}, {&n.Agent, k.Agent}, {&n.State, k.State}, {&n.Ending, k.Ending}, {&n.ClosedReason, k.ClosedReason}} {
			if *v.to == "" {
				*v.to = v.from
			}
		}
		if (k.Rel == wire.RelStarted || k.Rel == wire.RelContinued) && k.From != "" && k.From != k.Ref {
			parent[k.Ref] = k.From
			n.Rel = k.Rel
		}
	}
	// A parent chain that loops back is cut where it does.
	for _, ref := range order {
		seen := map[string]bool{ref: true}
		for p := parent[ref]; p != ""; p = parent[p] {
			if seen[p] {
				delete(parent, ref)
				break
			}
			seen[p] = true
		}
	}
	for _, ref := range order {
		n := nodes[ref]
		if p := nodes[parent[ref]]; p != nil {
			p.Children = append(p.Children, n)
		} else {
			g.Nodes = append(g.Nodes, n)
		}
	}
	for _, k := range l.Links {
		switch {
		case k.Kind == wire.LinkFile && k.Rel == wire.RelHandoff && nodes[k.From] != nil:
			nodes[k.From].Files = append(nodes[k.From].Files, k.Ref)
		case k.Kind == wire.LinkFile:
			g.Nodes = append(g.Nodes, &graphNode{Ref: k.Ref, Kind: k.Kind, Rel: k.Rel, Label: k.Label})
		case k.Kind == wire.LinkLoop && k.Rel == wire.RelBlocks:
			g.Blocks = append(g.Blocks, graphLoop{ID: k.Ref, Title: k.Label})
		case k.Kind == wire.LinkLoop && k.Rel == wire.RelBlockedBy:
			g.BlockedBy = append(g.BlockedBy, graphLoop{ID: k.Ref, Title: k.Label})
		}
	}
	return g
}

func printGraph(w io.Writer, g loopGraph) {
	fmt.Fprintf(w, "%s  %s\n", g.ID, g.Title)
	var walk func(n *graphNode, depth int)
	walk = func(n *graphNode, depth int) {
		indent := strings.Repeat("  ", depth+1)
		name := n.Ref
		if n.Label != "" {
			name = n.Label + " (" + n.Ref + ")"
		}
		k := wire.LoopLink{Kind: n.Kind, Agent: n.Agent, Machine: n.Machine, State: n.State, ClosedReason: n.ClosedReason, Ending: n.Ending}
		line := strings.TrimSpace(fmt.Sprintf("%s %s %s %s", n.Rel, n.Kind, name, linkState(k)))
		if n.Kind == wire.LinkFile {
			line = strings.TrimSpace(n.Rel + " file " + name)
		}
		fmt.Fprintf(w, "%s%s\n", indent, line)
		for _, f := range n.Files {
			fmt.Fprintf(w, "%s  handoff file %s\n", indent, f)
		}
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	for _, n := range g.Nodes {
		walk(n, 0)
	}
	for _, b := range g.Blocks {
		fmt.Fprintf(w, "  blocks %s %s\n", b.ID, b.Title)
	}
	for _, b := range g.BlockedBy {
		fmt.Fprintf(w, "  waits for %s %s\n", b.ID, b.Title)
	}
}

func loopsGraph(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl loops graph LOOP")
		}
		l, err := resolveLoop(ctx, c, positional[0])
		if err != nil {
			return err
		}
		g := buildGraph(l)
		for _, list := range [][]graphLoop{g.Blocks, g.BlockedBy} {
			for i := range list {
				var o wire.Loop
				if list[i].Title == "" && c.Call(ctx, wire.MethodLoopsGet, wire.IDParams{ID: list[i].ID}, &o) == nil {
					list[i].Title = o.Title
				}
			}
		}
		if *asJSON {
			return output(g)
		}
		printGraph(os.Stdout, g)
		return nil
	})
}

func loopsSettings(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	limit := f.Int("loop-limit", 0, "Loops on you before the start check turns red")
	stall := f.Int("stall-days", 0, "Days without change before a milestone is stalled")
	evening := f.String("evening", "", "The evening wrap-up time, `HH:MM` local")
	wakes := f.Int("max-wakes", 0, "The Steward's wakes a day at most")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 0 {
			return usagef("usage: hesperctl loops settings [--loop-limit N] [--stall-days N] [--evening HH:MM] [--max-wakes N]")
		}
		var s wire.LoopSettings
		if len(setFlags(f)) == 0 || len(setFlags(f)) == 1 && setFlags(f)["json"] {
			if err := c.Call(ctx, wire.MethodLoopsSettingsGet, nil, &s); err != nil {
				return err
			}
		} else {
			if *limit < 0 || *stall < 0 || *wakes < 0 {
				return usagef("loops settings: the numbers are positive")
			}
			if *evening != "" {
				if _, err := time.Parse("15:04", *evening); err != nil {
					return usagef("--evening %q: want HH:MM", *evening)
				}
			}
			p := wire.LoopSettings{LoopLimit: *limit, StallDays: *stall, EveningAt: *evening, MaxWakesPerDay: *wakes}
			if err := c.Call(ctx, wire.MethodLoopsSettingsSet, p, &s); err != nil {
				return err
			}
		}
		if *asJSON {
			return output(s)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintf(tw, "loop limit\t%d\nstall days\t%d\nevening\t%s\nmax wakes a day\t%d\n", s.LoopLimit, s.StallDays, s.EveningAt, s.MaxWakesPerDay)
		return tw.Flush()
	})
}

// spawnLoop puts new's --loop and --milestone into p (as ids), after it
// made sure hesperd knows loops: an older one would drop them and
// reportToParent silently (it decodes spawn params leniently), so they
// are refused instead (exit 2).
func spawnLoop(ctx context.Context, c *wire.Client, loop, milestone string, p *wire.SpawnParams) error {
	if loop == "" && milestone == "" && !p.ReportToParent {
		return nil
	}
	if loop != "" && milestone != "" {
		return usagef("--loop or --milestone, not both (a loop has its milestone)")
	}
	if p.ReportToParent && c.Caller == "" {
		return usagef("--report-to-parent tells the agent that starts this one: run it inside a Hesper agent")
	}
	var s wire.LoopSettings
	if err := c.Call(ctx, wire.MethodLoopsSettingsGet, nil, &s); err != nil {
		if errorCode(err) == wire.CodeNotFound {
			return usagef("this hesperd does not know loops (update it): --loop, --milestone and --report-to-parent would be dropped")
		}
		return err
	}
	if loop != "" {
		l, err := resolveLoop(ctx, c, loop)
		if err != nil {
			return err
		}
		p.Loop = l.ID
	}
	if milestone != "" {
		m, err := resolveMilestone(ctx, c, milestone)
		if err != nil {
			return err
		}
		p.Milestone = m.ID
	}
	return nil
}
