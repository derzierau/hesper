package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Goals and milestones (goals.*) of a product: a project or a group.
//
//	goals ls | new | save | rm | check | file | plan | accept

const scopeRefHelp = "SCOPE is the product: a project (an id p-…, its name when unique, or one of its folders) or a group (an id g-… or its name when unique)."
const goalRefHelp = "ID is a goal's or milestone's id (gl-…, ms-…), its title or a unique beginning of it."

func init() {
	for _, c := range []Command{
		{Name: "goals ls", ReadOnly: true, Summary: "List the goals and milestones of the products", Usage: "goals ls [SCOPE] [--json]",
			Help:     "Without SCOPE every product's. " + scopeRefHelp + " Goals in their order, then milestones: id, title, status (proposed ones wait for goals accept), conditions done of total, due date, the loops filed under it and what is next; stalled ones are marked.",
			Output:   "{goals:[{id, scope, title, order, status}], milestones:[{id, scope, goal?, title, status, order, due?, autoFile?, items:[LoopItem], loops:[loopId], done, total, stalled?, nextText?, created, updated}]}",
			Examples: []string{"hesperctl goals ls", "hesperctl goals ls app --json | jq -r '.milestones[] | select(.stalled) | .title'"},
			Run:      goalsList},
		{Name: "goals new", Summary: "Add a goal or a milestone to a product", Usage: "goals new SCOPE TITLE… [--milestone] [--goal G] [--due WHEN] [--auto-file] [--when TEXT]… [--json]",
			Help: scopeRefHelp + " A goal is an outcome the product works toward; with --milestone a milestone: a release or result, under --goal, due --due (a date 2026-12-01, or 3d, 2w), with \"done when\" conditions (--when, repeatable). " +
				"--auto-file lets the Steward file the loops it matches to it without asking. Prints the id.",
			Output:   "{goal} or {milestone}",
			Examples: []string{"hesperctl goals new app Fewer support tickets", "hesperctl goals new app 1.4 --milestone --goal 'fewer support' --due 2026-12-01 --when 'login fix merged' --when 'tag pushed'"},
			Run:      goalsNew},
		{Name: "goals save", Summary: "Change a goal or a milestone", Usage: "goals save ID [--title T] [--status S] [--order N] [--goal G] [--due WHEN] [--auto-file] [--when TEXT]… [--json]",
			Help:   goalRefHelp + " Only the flags given change. --status open, done or dropped (done and dropped are a person's; a milestone also proposed: back to planning); --due \"\" clears the date; --auto-file=false turns it off; --when adds conditions to a milestone.",
			Output: "{goal} or {milestone}", Run: goalsSave},
		{Name: "goals rm", Destructive: true, Summary: "Remove a goal or a milestone", Usage: "goals rm ID", Help: goalRefHelp + " A milestone's loops are unfiled, not closed. A person's only.", Run: goalsRemove},
		{Name: "goals check", Summary: "Tick off a milestone's \"done when\" condition", Usage: "goals check MILESTONE ITEM [--drop] [--undo] [--json]",
			Help:   milestoneRefHelp + " ITEM is the condition's number as goals ls --json lists them (1, 2, … by order) or its id. --drop drops it (it no longer counts), --undo opens it again.",
			Output: "Milestone", Examples: []string{"hesperctl goals check 1.4 2"}, Run: goalsCheck},
		{Name: "goals file", Summary: "File a loop under a milestone", Usage: "goals file MILESTONE LOOP [--remove] [--json]",
			Help:   milestoneRefHelp + " " + loopRefHelp + " --remove unfiles it.",
			Output: "Milestone", Examples: []string{"hesperctl goals file 1.4 'login fix'"}, Run: goalsFile},
		{Name: "goals plan", Summary: "Start a planning session for a product", Usage: "goals plan SCOPE [PROMPT…] [--json]",
			Help:   scopeRefHelp + " hesperd starts a planning agent that reads the product's loops and writes proposed milestones; goals accept opens them. Prints the agent's id. Needs the Steward's part of hesperd (else unavailable, exit 4).",
			Output: "Agent", Examples: []string{"hesperctl goals plan app plan the 1.4 release"}, Run: goalsPlan},
		{Name: "goals accept", Summary: "Accept a product's proposed milestones", Usage: "goals accept SCOPE [MILESTONE…] [--json]",
			Help:   scopeRefHelp + " Every proposed milestone of SCOPE, or only those named, becomes open. A person's only. Prints how many.",
			Output: "{accepted}", Run: goalsAccept},
	} {
		c.Group = groupLoops
		register(c)
	}
}

// resolveScope finds a product: a project or a group by id, a folder
// (a project), or a unique name among both.
func resolveScope(ctx context.Context, c *wire.Client, ref string) (id, name string, err error) {
	var projects []wire.ProjectInfo
	if err := c.Call(ctx, "projects.list", nil, &projects); err != nil {
		return "", "", err
	}
	var groups []wire.Group
	if err := c.Call(ctx, "groups.list", nil, &groups); err != nil {
		return "", "", err
	}
	for _, g := range groups {
		if g.ID == ref {
			return g.ID, g.Name, nil
		}
	}
	if looksLikePath(ref) || strings.HasPrefix(ref, "p-") {
		p, err := resolveProject(ctx, c, ref)
		return p.ID, p.Name, err
	}
	var real []wire.ProjectInfo
	for _, p := range projects {
		if p.Kind != wire.ProjectScratch {
			real = append(real, p)
		}
	}
	type hit struct{ id, name string }
	var hits []hit
	for _, i := range matchByName(len(real), func(i int) string { return real[i].Name }, ref) {
		hits = append(hits, hit{real[i].ID, real[i].Name})
	}
	for _, i := range matchByName(len(groups), func(i int) string { return groups[i].Name }, ref) {
		hits = append(hits, hit{groups[i].ID, groups[i].Name})
	}
	switch len(hits) {
	case 0:
		return "", "", failf(wire.CodeNotFound, "no project or group %s (projects ls, groups ls list them)", ref)
	case 1:
		return hits[0].id, hits[0].name, nil
	}
	var ids []string
	for _, h := range hits {
		ids = append(ids, h.id)
	}
	return "", "", failf(codeAmbiguous, "%q names %d projects and groups: use an id (%s)", ref, len(hits), strings.Join(ids, ", "))
}

// resolveGoal finds a goal or a milestone (exactly one is set) by id, title
// or a unique beginning of it, among every product's.
func resolveGoal(ctx context.Context, c *wire.Client, ref string) (*wire.Goal, *wire.Milestone, error) {
	var gl wire.GoalsList
	if err := c.Call(ctx, wire.MethodGoalsList, wire.GoalsListParams{}, &gl); err != nil {
		return nil, nil, err
	}
	for i := range gl.Goals {
		if gl.Goals[i].ID == ref {
			return &gl.Goals[i], nil, nil
		}
	}
	for i := range gl.Milestones {
		if gl.Milestones[i].ID == ref {
			return nil, &gl.Milestones[i], nil
		}
	}
	titles := make([]string, 0, len(gl.Goals)+len(gl.Milestones))
	for _, g := range gl.Goals {
		titles = append(titles, g.Title)
	}
	for _, m := range gl.Milestones {
		titles = append(titles, m.Title)
	}
	hits := matchTitle(len(titles), func(i int) string { return titles[i] }, ref)
	switch len(hits) {
	case 0:
		return nil, nil, failf(wire.CodeNotFound, "no goal or milestone %s (goals ls lists them)", ref)
	case 1:
		if i := hits[0]; i < len(gl.Goals) {
			return &gl.Goals[i], nil, nil
		}
		return nil, &gl.Milestones[hits[0]-len(gl.Goals)], nil
	}
	return nil, nil, failf(codeAmbiguous, "%q names %d goals and milestones: use an id", ref, len(hits))
}

// resolveGoalOnly is resolveGoal for a goal (--goal).
func resolveGoalOnly(ctx context.Context, c *wire.Client, ref string) (string, error) {
	g, _, err := resolveGoal(ctx, c, ref)
	if err != nil {
		return "", err
	}
	if g == nil {
		return "", failf(wire.CodeInvalid, "%s is a milestone, not a goal", ref)
	}
	return g.ID, nil
}

func goalsList(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 1 {
			return usagef("usage: hesperctl goals ls [SCOPE]")
		}
		var p wire.GoalsListParams
		if len(positional) == 1 {
			id, _, err := resolveScope(ctx, c, positional[0])
			if err != nil {
				return err
			}
			p.Scope = id
		}
		var gl wire.GoalsList
		if err := c.Call(ctx, wire.MethodGoalsList, p, &gl); err != nil {
			return err
		}
		if gl.Goals == nil {
			gl.Goals = []wire.Goal{}
		}
		if gl.Milestones == nil {
			gl.Milestones = []wire.Milestone{}
		}
		if *asJSON {
			return output(gl)
		}
		return printGoals(os.Stdout, gl, scopeNames(ctx, c))
	})
}

// scopeNames are the projects' and groups' names by id.
func scopeNames(ctx context.Context, c *wire.Client) map[string]string {
	names := map[string]string{}
	var projects []wire.ProjectInfo
	if c.Call(ctx, "projects.list", nil, &projects) == nil {
		for _, p := range projects {
			names[p.ID] = p.Name
		}
	}
	var groups []wire.Group
	if c.Call(ctx, "groups.list", nil, &groups) == nil {
		for _, g := range groups {
			names[g.ID] = g.Name
		}
	}
	return names
}

// printGoals prints each product's goals and milestones under its name.
func printGoals(w io.Writer, gl wire.GoalsList, names map[string]string) error {
	var scopes []string
	for _, g := range gl.Goals {
		if !contains(scopes, g.Scope) {
			scopes = append(scopes, g.Scope)
		}
	}
	for _, m := range gl.Milestones {
		if !contains(scopes, m.Scope) {
			scopes = append(scopes, m.Scope)
		}
	}
	for i, scope := range scopes {
		if i > 0 {
			fmt.Fprintln(w)
		}
		name := names[scope]
		if name == "" {
			name = scope
		}
		fmt.Fprintf(w, "%s (%s)\n", name, scope)
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		for _, g := range gl.Goals {
			if g.Scope == scope {
				fmt.Fprintf(tw, "  %s\tgoal\t%s\t%s\n", g.ID, oneLine(g.Title, 50), g.Status)
			}
		}
		for _, m := range gl.Milestones {
			if m.Scope != scope {
				continue
			}
			status := m.Status
			if m.Stalled {
				status += ", stalled"
			}
			due := "-"
			if !m.Due.IsZero() {
				due = "due " + m.Due.Local().Format("2006-01-02")
			}
			fmt.Fprintf(tw, "  %s\tmilestone\t%s\t%s\t%d/%d\t%s\t%d loops\t%s\n", m.ID, oneLine(m.Title, 50), status, m.Done, m.Total, due, len(m.Loops), oneLine(m.NextText, 50))
		}
		tw.Flush()
	}
	return nil
}

// printGoalChanged prints the new or changed goal's or milestone's id.
func printGoalChanged(asJSON bool, ch wire.GoalChanged) error {
	if asJSON {
		return output(ch)
	}
	if ch.Goal != nil {
		fmt.Println(ch.Goal.ID)
	} else if ch.Milestone != nil {
		fmt.Println(ch.Milestone.ID)
	}
	return nil
}

// milestoneFlags are goals new's and goals save's milestone flags.
type milestoneFlags struct {
	goal, due *string
	autoFile  *bool
	when      *listFlag
}

func addMilestoneFlags(f *flag.FlagSet) milestoneFlags {
	m := milestoneFlags{
		goal:     f.String("goal", "", "A milestone: the goal it serves"),
		due:      f.String("due", "", "A milestone: due `WHEN` (a date 2026-12-01, or 3d, 2w)"),
		autoFile: f.Bool("auto-file", false, "A milestone: the Steward files the loops it matches without asking"),
		when:     new(listFlag),
	}
	f.Var(m.when, "when", "A milestone: a \"done when\" condition (repeatable)")
	return m
}

// patch fills a milestone patch from the flags given.
func (m milestoneFlags) patch(ctx context.Context, c *wire.Client, set map[string]bool, p *wire.MilestonePatch) error {
	if set["goal"] {
		id := ""
		if *m.goal != "" {
			var err error
			if id, err = resolveGoalOnly(ctx, c, *m.goal); err != nil {
				return err
			}
		}
		p.Goal = &id
	}
	if set["due"] {
		var due time.Time
		if *m.due != "" {
			var err error
			if due, err = parseWhen("due", *m.due, time.Now(), 0); err != nil {
				return err
			}
		}
		p.Due = &due
	}
	if set["auto-file"] {
		p.AutoFile = m.autoFile
	}
	return nil
}

// addConditions adds the --when conditions to milestone id.
func (m milestoneFlags) addConditions(ctx context.Context, c *wire.Client, ms *wire.Milestone) error {
	for _, text := range *m.when {
		if err := c.Call(ctx, wire.MethodGoalsItem, wire.LoopItemParams{ID: ms.ID, Item: wire.LoopItem{Text: text, Kind: wire.ItemCondition}}, ms); err != nil {
			return err
		}
	}
	return nil
}

func goalsNew(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	milestone := f.Bool("milestone", false, "Add a milestone, not a goal")
	mf := addMilestoneFlags(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		title := ""
		if len(positional) >= 2 {
			title = strings.TrimSpace(strings.Join(positional[1:], " "))
		}
		if title == "" {
			return usagef("usage: hesperctl goals new SCOPE TITLE… [--milestone]")
		}
		set := setFlags(f)
		if !*milestone && (set["goal"] || set["due"] || set["auto-file"] || set["when"]) {
			return usagef("--goal, --due, --auto-file and --when go with --milestone")
		}
		scope, _, err := resolveScope(ctx, c, positional[0])
		if err != nil {
			return err
		}
		var p wire.GoalSaveParams
		if *milestone {
			p.Milestone = &wire.MilestonePatch{Scope: &scope, Title: &title}
			if err := mf.patch(ctx, c, set, p.Milestone); err != nil {
				return err
			}
		} else {
			p.Goal = &wire.GoalPatch{Scope: &scope, Title: &title}
		}
		var ch wire.GoalChanged
		if err := c.Call(ctx, wire.MethodGoalsSave, p, &ch); err != nil {
			return err
		}
		if ch.Milestone != nil {
			if err := mf.addConditions(ctx, c, ch.Milestone); err != nil {
				return err
			}
		}
		return printGoalChanged(*asJSON, ch)
	})
}

func goalsSave(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	title := f.String("title", "", "The new title")
	status := f.String("status", "", "open, done, dropped or (a milestone) proposed")
	order := f.Float64("order", 0, "Its place among the product's goals or milestones")
	mf := addMilestoneFlags(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl goals save ID [flags]")
		}
		set := setFlags(f)
		if set["status"] && !contains([]string{wire.LoopOpen, wire.LoopDone, wire.LoopDropped, wire.MilestoneProposed}, *status) {
			return usagef("--status %q: open, done, dropped or (a milestone) proposed", *status)
		}
		g, m, err := resolveGoal(ctx, c, positional[0])
		if err != nil {
			return err
		}
		if g != nil && *status == wire.MilestoneProposed {
			return usagef("%s is a goal: only milestones are proposed", g.ID)
		}
		if g != nil && (set["goal"] || set["due"] || set["auto-file"] || set["when"]) {
			return usagef("%s is a goal: --goal, --due, --auto-file and --when are a milestone's", g.ID)
		}
		var p wire.GoalSaveParams
		if g != nil {
			p.Goal = &wire.GoalPatch{ID: g.ID}
			if set["title"] {
				p.Goal.Title = title
			}
			if set["status"] {
				p.Goal.Status = status
			}
			if set["order"] {
				p.Goal.Order = order
			}
		} else {
			p.Milestone = &wire.MilestonePatch{ID: m.ID}
			if set["title"] {
				p.Milestone.Title = title
			}
			if set["status"] {
				p.Milestone.Status = status
			}
			if set["order"] {
				p.Milestone.Order = order
			}
			if err := mf.patch(ctx, c, set, p.Milestone); err != nil {
				return err
			}
		}
		var ch wire.GoalChanged
		if err := c.Call(ctx, wire.MethodGoalsSave, p, &ch); err != nil {
			return err
		}
		if ch.Milestone != nil {
			if err := mf.addConditions(ctx, c, ch.Milestone); err != nil {
				return err
			}
		}
		return printGoalChanged(*asJSON, ch)
	})
}

func goalsRemove(ctx context.Context, f *flag.FlagSet, args []string) error {
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl goals rm ID")
		}
		g, m, err := resolveGoal(ctx, c, positional[0])
		if err != nil {
			return err
		}
		id := ""
		if g != nil {
			id = g.ID
		} else {
			id = m.ID
		}
		return c.Call(ctx, wire.MethodGoalsRemove, wire.IDParams{ID: id}, nil)
	})
}

// printMilestone prints a milestone's id, or with --json the milestone.
func printMilestone(asJSON bool, m wire.Milestone) error {
	if asJSON {
		return output(m)
	}
	fmt.Println(m.ID)
	return nil
}

func goalsCheck(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	drop := f.Bool("drop", false, "Drop the condition (it no longer counts)")
	undo := f.Bool("undo", false, "Open it again")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 2 {
			return usagef("usage: hesperctl goals check MILESTONE ITEM [--drop] [--undo]")
		}
		state, err := itemState(*drop, *undo)
		if err != nil {
			return err
		}
		m, err := resolveMilestone(ctx, c, positional[0])
		if err != nil {
			return err
		}
		it, err := resolveItem(m.Items, positional[1])
		if err != nil {
			return err
		}
		if err := c.Call(ctx, wire.MethodGoalsItem, wire.LoopItemParams{ID: m.ID, Item: wire.LoopItem{ID: it.ID, State: state}}, &m); err != nil {
			return err
		}
		return printMilestone(*asJSON, m)
	})
}

func goalsFile(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	remove := f.Bool("remove", false, "Unfile the loop")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 2 {
			return usagef("usage: hesperctl goals file MILESTONE LOOP [--remove]")
		}
		m, err := resolveMilestone(ctx, c, positional[0])
		if err != nil {
			return err
		}
		l, err := resolveLoop(ctx, c, positional[1])
		if err != nil {
			return err
		}
		if err := c.Call(ctx, wire.MethodGoalsFile, wire.GoalFileParams{Milestone: m.ID, Loop: l.ID, Remove: *remove}, &m); err != nil {
			return err
		}
		return printMilestone(*asJSON, m)
	})
}

func goalsPlan(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) < 1 {
			return usagef("usage: hesperctl goals plan SCOPE [PROMPT…]")
		}
		scope, _, err := resolveScope(ctx, c, positional[0])
		if err != nil {
			return err
		}
		var a wire.Agent
		p := wire.StewardPlanParams{Scope: scope, Prompt: strings.TrimSpace(strings.Join(positional[1:], " "))}
		if err := c.Call(ctx, wire.MethodStewardPlan, p, &a); err != nil {
			return err
		}
		if *asJSON {
			return output(a)
		}
		fmt.Println(a.ID)
		return nil
	})
}

func goalsAccept(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) < 1 {
			return usagef("usage: hesperctl goals accept SCOPE [MILESTONE…]")
		}
		scope, _, err := resolveScope(ctx, c, positional[0])
		if err != nil {
			return err
		}
		p := wire.GoalAcceptParams{Scope: scope}
		for _, ref := range positional[1:] {
			m, err := resolveMilestone(ctx, c, ref)
			if err != nil {
				return err
			}
			p.IDs = append(p.IDs, m.ID)
		}
		var res struct {
			Accepted int `json:"accepted"`
		}
		if err := c.Call(ctx, wire.MethodGoalsAccept, p, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		fmt.Println(res.Accepted)
		return nil
	})
}
