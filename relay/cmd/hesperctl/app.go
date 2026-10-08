package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// App commands: Hesper.app's windows through hesperd (docs/rebuild-
// contract.md "As built — app control"). hesperd forwards app.state,
// app.open, app.wall.set and app.desk to the app that registered; when
// none did, hesperctl opens Hesper.app and retries for up to 10 s
// (--no-launch: exit 4 at once).
//
//	open agent | wall | new | history | inbox   (open ID = open agent ID)
//	wall show | set                              (wall = wall show)
//	desk ls | save | switch | rename | rm        (desk = desk ls)

const groupApp = "App"

// appLaunchWait bounds the wait for a just-opened app to register.
var appLaunchWait = 10 * time.Second

// launchApp opens Hesper.app (tests replace it: they never launch it).
// background: without bringing it to the front.
var launchApp = func(ctx context.Context, background bool) error {
	flags := []string{}
	if background {
		flags = append(flags, "-g")
	}
	if err := exec.CommandContext(ctx, "open", append(flags, "-a", "Hesper")...).Run(); err == nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, "Applications", "Hesper.app")
	if err := exec.CommandContext(ctx, "open", append(flags, path)...).Run(); err != nil {
		return fmt.Errorf("open -a Hesper and open %s failed: %v", path, err)
	}
	return nil
}

const scopeHelp = "all | overflow | needs-you | working | project:<id|name> | group:<id|name> | filter:machine=M,kind=K,state=needsYou|working|quiet|ended,project=P,group=G"

const wallHelp = "WALL is a wall's id (hesperctl wall lists them), its position (1 = home), current (the frontmost, the default) or home."

const noLaunchHelp = "When Hesper.app is not running, hesperctl opens it and waits up to 10 s for it (--no-launch: exit 4 instead)."

const deskOutput = "[{id, name, current, automatic, thisDisplays, displays, walls, agentWindows}] (the desks after the change)"

const wallOutput = "{id, title, isMain, isHome, open, visible, scope, scopeName?, arrangement, grouping, minChars, density, collapsed, bandOrder, sidebar, ownWalls, mode, focusedAgent?, selectedAgent?, agents, bands:[{key, title, collapsed, agents, pointer}]}"

func init() {
	for _, c := range []Command{
		{Name: "open agent", Aliases: []string{"open"}, Summary: "Show an agent in Hesper.app: focus view, its own window or a tab",
			Usage: "open agent ID [--window|--tab|--select]",
			Help: idHelp + " (hesperctl open ID works too.) Without a flag: the focus view of the wall showing it (⌘↩), or its own window if it has one, like a notification click. " +
				"--window: its own window (⇧-click); --tab: a tab of the frontmost agent window (⌥⇧-click); --select: selected on the wall showing it, without the focus view. Brings the app to the front.\n\n" + noLaunchHelp,
			Output:   "{agent, mode}",
			Examples: []string{"hesperctl open agent a7f3k2", "hesperctl open push-provider --window"},
			Run:      openAgent},
		{Name: "open wall", Summary: "Bring a wall forward, give it a scope, or open a new wall window",
			Usage: "open wall [--wall W] [--scope S] [--new]",
			Help: "Without flags: the frontmost wall. --wall W: that wall. --scope S: a wall showing S comes forward, else a new wall window with it (the project sidebar's ⌥-click); with --wall: that wall takes the scope (the sidebar's click). --new: a new wall window (⌥⌘N). " +
				wallHelp + " Scopes: " + scopeHelp + ".\n\n" + noLaunchHelp,
			Output:   "the wall: " + wallOutput,
			Examples: []string{"hesperctl open wall --scope project:as-apps", "hesperctl open wall --new --scope needs-you"},
			Run:      openWall},
		{Name: "open new", Summary: "Open a new agent's composer in Hesper.app, filled in but not started",
			Usage: "open new [--project P] [--kind K] [--profile P] [--machine M] [--worktree] [--branch B] [--wall W] [TASK… | --task T]",
			Help: "A draft in the wall's composer (⌘N) with the task and choices filled in; the user reviews it and starts it with ⌘↩ (hesperctl new starts one at once). " +
				"--project is a folder (/…, ~/…) or a project's id or name; --kind picks the kind's default profile; --branch implies --worktree.\n\n" + noLaunchHelp,
			Output:   "{draft, wall}",
			Examples: []string{"hesperctl open new --project ~/projects/app --kind codex fix the login redirect"},
			Run:      openNew},
		{Name: "open history", Summary: "Open History in Hesper.app, searching QUERY", Usage: "open history [QUERY…]",
			Help: "History (⌘Y) on the frontmost wall with QUERY in its search field.\n\n" + noLaunchHelp, Output: "{wall, query}",
			Run: openHistory},
		{Name: "open inbox", Summary: "Open the inbox of agents that need you (⌘J)", Usage: "open inbox",
			Help: "On the frontmost wall; with nobody needing you the app says so.\n\n" + noLaunchHelp, Output: "{wall, needsYou}",
			Run: openInbox},
		{Name: "wall show", Aliases: []string{"wall"}, Summary: "Show the walls: scope, layout, grouping, density, bands, focus",
			Usage: "wall show [--wall W] [--json]",
			Help:  "Every wall in desk order (home first), the bands of walls that show bands, the current desk and what has the focus. --json prints the app's whole state (--wall W: that wall only). " + wallHelp + "\n\n" + noLaunchHelp,
			Output: "{active, focused:{wall?, agent?, agentWindow?}, currentWall, walls:[" + wallOutput + "], agentWindows:[{agent, title, key}], desks:" + deskOutput +
				", arrangements, groupings, ownWalls, densities, scopes}",
			Examples: []string{"hesperctl wall", "hesperctl wall --json | jq -r '.walls[] | select(.isHome) | .id'"},
			Run:      wallShow},
		{Name: "wall set", Summary: "Change a wall's layout, grouping, density, bands, scope or sidebar",
			Usage: "wall set [--wall W] [--arrangement A] [--grouping G] [--density D] [--collapse BAND]… [--expand BAND]… [--band-order B,…] [--scope S] [--sidebar on|off] [--own-walls M] [--home]",
			Help: "Changes one wall the way the View menu, the layout popover and band headers do; saved with the desk. " + wallHelp + "\n" +
				"  --arrangement shelf | columns | treemap | mainStack | grid\n" +
				"  --grouping auto | none | group | project | branch\n" +
				"  --density dense | normal | a minimum card width in characters\n" +
				"  --collapse / --expand BAND (repeatable, or comma-separated; all = every band): a band's key (p:<project>, g:<group>), title or project id\n" +
				"  --band-order: band keys or titles, first first\n" +
				"  --scope: " + scopeHelp + "\n" +
				"  --own-walls collapsed | full | hidden: projects / groups that have their own wall\n" +
				"  --home: make it the home wall\n\nAn unknown band changes nothing (exit 3).\n\n" + noLaunchHelp,
			Output: "the wall: " + wallOutput,
			Examples: []string{
				"hesperctl wall set --arrangement columns --density dense",
				"hesperctl wall set --wall home --grouping project --collapse all --expand as-apps",
				"hesperctl wall set --scope needs-you",
			},
			Run: wallSet},
		{Name: "desk ls", Aliases: []string{"desk"}, Summary: "List the desks (window arrangements per display setup)", Usage: "desk ls [--json]",
			Help:   "A desk is the whole arrangement of walls and agent windows, kept per display setup; * marks the current one, (these) the desks of the displays attached now.\n\n" + noLaunchHelp,
			Output: deskOutput, Run: deskRun("list", 0)},
		{Name: "desk save", Summary: "Save the windows as they are as a desk", Usage: "desk save NAME",
			Help: "For the displays attached now; a desk of that name is replaced. It becomes the current desk.\n\n" + noLaunchHelp, Output: deskOutput,
			Examples: []string{"hesperctl desk save 'Deep work'"}, Run: deskRun("save", 1)},
		{Name: "desk switch", Summary: "Switch to a desk", Usage: "desk switch NAME",
			Help: "NAME is a desk's name or id; a desk of other displays is copied to these first (as the ⌃⌘D picker does).\n\n" + noLaunchHelp, Output: deskOutput,
			Run: deskRun("switch", 1)},
		{Name: "desk rename", Summary: "Rename a desk", Usage: "desk rename OLD NEW", Help: noLaunchHelp, Output: deskOutput, Run: deskRun("rename", 2)},
		{Name: "desk rm", Summary: "Remove a named desk", Usage: "desk rm NAME",
			Help: "A display setup's automatic desk stays (exit 1, code invalid).\n\n" + noLaunchHelp, Output: deskOutput, Run: deskRun("remove", 1)},
	} {
		c.Group = groupApp
		register(c)
	}
}

func isUnavailable(err error) bool {
	var we *wire.Error
	return errors.As(err, &we) && we.Code == wire.CodeUnavailable
}

// appCall calls an app.* method; when no app is registered it opens
// Hesper.app (unless noLaunch) and retries until it answers or
// appLaunchWait passes.
func appCall(ctx context.Context, c *wire.Client, noLaunch, background bool, method string, params, result any) error {
	err := c.Call(ctx, method, params, result)
	if noLaunch || !isUnavailable(err) {
		return err
	}
	if lerr := launchApp(ctx, background); lerr != nil {
		return failf(wire.CodeUnavailable, "Hesper.app is not running and could not be opened: %v", lerr)
	}
	deadline := time.Now().Add(appLaunchWait)
	for isUnavailable(err) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		err = c.Call(ctx, method, params, result)
	}
	if isUnavailable(err) {
		return failf(wire.CodeUnavailable, "Hesper.app did not connect to hesperd within %s", appLaunchWait)
	}
	return err
}

func noLaunchFlag(f *flag.FlagSet) *bool {
	return f.Bool("no-launch", false, "Fail (exit 4) instead of opening Hesper.app when it is not running")
}

func setString(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

// appOpen sends app.open and prints line(result) (the result with --json).
func appOpen(ctx context.Context, c *wire.Client, asJSON, noLaunch bool, params map[string]any, line func(map[string]any) string) error {
	var res json.RawMessage
	if err := appCall(ctx, c, noLaunch, false, "app.open", params, &res); err != nil {
		return err
	}
	if asJSON {
		return output(res)
	}
	var r map[string]any
	json.Unmarshal(res, &r)
	fmt.Println(line(r))
	return nil
}

func openAgent(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	noLaunch := noLaunchFlag(f)
	window := f.Bool("window", false, "In its own window")
	tab := f.Bool("tab", false, "As a tab of the frontmost agent window")
	sel := f.Bool("select", false, "Selected on the wall showing it, without the focus view")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, pos []string) error {
		if len(pos) != 1 {
			return usagef("open agent ID (or open wall | new | history | inbox)")
		}
		mode, n := "focus", 0
		for _, m := range []struct {
			on   bool
			mode string
		}{{*window, "window"}, {*tab, "tab"}, {*sel, "wall"}} {
			if m.on {
				mode, n = m.mode, n+1
			}
		}
		if n > 1 {
			return usagef("--window, --tab and --select exclude each other")
		}
		return appOpen(ctx, c, *asJSON, *noLaunch, map[string]any{"agent": pos[0], "mode": mode}, func(r map[string]any) string {
			return fmt.Sprintf("%v %v", r["mode"], r["agent"])
		})
	})
}

func openWall(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	noLaunch := noLaunchFlag(f)
	wall := f.String("wall", "", "The wall (id, position, current, home)")
	scope := f.String("scope", "", "The scope ("+scopeHelp+")")
	newWall := f.Bool("new", false, "A new wall window")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, pos []string) error {
		if len(pos) > 0 {
			return usagef("open wall takes flags, not arguments")
		}
		params := map[string]any{"mode": "wall"}
		setString(params, "wall", *wall)
		setString(params, "scope", *scope)
		if *newWall {
			params["newWall"] = true
		}
		return appOpen(ctx, c, *asJSON, *noLaunch, params, func(r map[string]any) string {
			return fmt.Sprintf("wall %v (%v)", r["id"], r["scope"])
		})
	})
}

func openNew(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	noLaunch := noLaunchFlag(f)
	wall := f.String("wall", "", "The wall (id, position, current, home)")
	project := f.String("project", "", "A folder, or a project's id or name")
	task := f.String("task", "", "The task (or the words after open new)")
	kind := f.String("kind", "", "claude, codex or shell")
	profile := f.String("profile", "", "Launch profile")
	machine := f.String("machine", "", "Machine short name")
	worktree := f.Bool("worktree", false, "In a new worktree")
	branch := f.String("branch", "", "Branch (implies --worktree)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, pos []string) error {
		text := *task
		if len(pos) > 0 {
			if text != "" {
				return usagef("give the task as --task or as words, not both")
			}
			text = strings.Join(pos, " ")
		}
		comp := map[string]any{}
		setString(comp, "project", *project)
		setString(comp, "task", text)
		setString(comp, "kind", *kind)
		setString(comp, "profile", *profile)
		setString(comp, "machine", *machine)
		setString(comp, "branch", *branch)
		if *worktree {
			comp["worktree"] = true
		}
		params := map[string]any{"composer": comp}
		setString(params, "wall", *wall)
		return appOpen(ctx, c, *asJSON, *noLaunch, params, func(r map[string]any) string {
			return fmt.Sprintf("draft %v on %v (⌘↩ in Hesper starts it)", r["draft"], r["wall"])
		})
	})
}

func openHistory(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	noLaunch := noLaunchFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, pos []string) error {
		params := map[string]any{"history": map[string]any{"query": strings.Join(pos, " ")}}
		return appOpen(ctx, c, *asJSON, *noLaunch, params, func(r map[string]any) string {
			return fmt.Sprintf("history on %v", r["wall"])
		})
	})
}

func openInbox(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	noLaunch := noLaunchFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, pos []string) error {
		if len(pos) > 0 {
			return usagef("open inbox takes no arguments")
		}
		return appOpen(ctx, c, *asJSON, *noLaunch, map[string]any{"inbox": true}, func(r map[string]any) string {
			return fmt.Sprintf("inbox on %v: %v need you", r["wall"], r["needsYou"])
		})
	})
}

// appWall is a wall as app.state reports it.
type appWall struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	IsHome       bool     `json:"isHome"`
	Open         bool     `json:"open"`
	Scope        string   `json:"scope"`
	ScopeName    string   `json:"scopeName"`
	Arrangement  string   `json:"arrangement"`
	Grouping     string   `json:"grouping"`
	Density      string   `json:"density"`
	Sidebar      bool     `json:"sidebar"`
	Mode         string   `json:"mode"`
	FocusedAgent string   `json:"focusedAgent"`
	Agents       []string `json:"agents"`
	Bands        []struct {
		Key       string `json:"key"`
		Title     string `json:"title"`
		Collapsed bool   `json:"collapsed"`
		Agents    int    `json:"agents"`
		Pointer   bool   `json:"pointer"`
	} `json:"bands"`
}

type appDesk struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Current      bool   `json:"current"`
	Automatic    bool   `json:"automatic"`
	ThisDisplays bool   `json:"thisDisplays"`
	Displays     string `json:"displays"`
	Walls        int    `json:"walls"`
	AgentWindows int    `json:"agentWindows"`
}

func printWalls(walls []appWall) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "WALL\tSCOPE\tARRANGEMENT\tGROUPING\tDENSITY\tAGENTS\tMODE")
	for _, w := range walls {
		id := w.ID
		if w.IsHome {
			id += " (home)"
		}
		if !w.Open {
			id += " (closed)"
		}
		scope := w.Scope
		if w.ScopeName != "" {
			scope += " (" + w.ScopeName + ")"
		}
		mode := w.Mode
		if w.FocusedAgent != "" {
			mode += " " + w.FocusedAgent
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", id, scope, w.Arrangement, w.Grouping, w.Density, len(w.Agents), mode)
	}
	tw.Flush()
	for _, w := range walls {
		if len(w.Bands) == 0 {
			continue
		}
		fmt.Printf("\n%s bands:\n", w.ID)
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		for _, b := range w.Bands {
			state := ""
			if b.Collapsed {
				state = "collapsed"
			}
			if b.Pointer {
				state = "own wall"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\n", b.Key, b.Title, b.Agents, state)
		}
		tw.Flush()
	}
}

// pickWall finds a wall by id, position (1 = home), home or current.
func pickWall(walls []appWall, ref, current string) (int, error) {
	for i, w := range walls {
		if w.ID == ref || fmt.Sprint(i+1) == ref || (ref == "home" && w.IsHome) || (ref == "current" && w.ID == current) {
			return i, nil
		}
	}
	return 0, failf(wire.CodeNotFound, "no wall %s", ref)
}

func wallShow(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	noLaunch := noLaunchFlag(f)
	wall := f.String("wall", "", "Only this wall (id, position, current, home)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, pos []string) error {
		if len(pos) > 0 {
			return usagef("wall show takes flags, not arguments (wall set changes a wall)")
		}
		var state json.RawMessage
		if err := appCall(ctx, c, *noLaunch, true, "app.state", nil, &state); err != nil {
			return err
		}
		var s struct {
			Focused struct {
				Wall, Agent, AgentWindow string
			} `json:"focused"`
			Walls []appWall `json:"walls"`
			Desks []appDesk `json:"desks"`
		}
		if err := json.Unmarshal(state, &s); err != nil {
			return err
		}
		if *wall != "" {
			i, err := pickWall(s.Walls, *wall, s.Focused.Wall)
			if err != nil {
				return err
			}
			if *asJSON {
				var raw struct {
					Walls []json.RawMessage `json:"walls"`
				}
				json.Unmarshal(state, &raw)
				return output(raw.Walls[i])
			}
			s.Walls = s.Walls[i : i+1]
		} else if *asJSON {
			return output(state)
		}
		printWalls(s.Walls)
		for _, d := range s.Desks {
			if d.Current {
				fmt.Printf("\ndesk: %s\n", d.Name)
			}
		}
		switch {
		case s.Focused.AgentWindow != "":
			fmt.Printf("focused: agent window %s\n", s.Focused.AgentWindow)
		case s.Focused.Agent != "":
			fmt.Printf("focused: %s on %s\n", s.Focused.Agent, s.Focused.Wall)
		case s.Focused.Wall != "":
			fmt.Printf("focused: %s\n", s.Focused.Wall)
		}
		return nil
	})
}

func wallSet(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	noLaunch := noLaunchFlag(f)
	wall := f.String("wall", "", "The wall (id, position, current, home)")
	arrangement := f.String("arrangement", "", "shelf, columns, treemap, mainStack or grid")
	grouping := f.String("grouping", "", "auto, none, group, project or branch")
	density := f.String("density", "", "dense, normal or characters")
	var collapse, expand, order listFlag
	f.Var(&collapse, "collapse", "Collapse a `BAND` (repeatable; all)")
	f.Var(&expand, "expand", "Expand a `BAND` (repeatable; all)")
	f.Var(&order, "band-order", "Band order, comma-separated `BANDS`")
	scope := f.String("scope", "", "The scope ("+scopeHelp+")")
	sidebar := f.String("sidebar", "", "The project sidebar, on or off")
	ownWalls := f.String("own-walls", "", "collapsed, full or hidden")
	home := f.Bool("home", false, "Make it the home wall")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, pos []string) error {
		if len(pos) > 0 {
			return usagef("wall set takes flags, not arguments")
		}
		params := map[string]any{}
		setString(params, "arrangement", *arrangement)
		setString(params, "grouping", *grouping)
		setString(params, "density", *density)
		setString(params, "scope", *scope)
		setString(params, "ownWalls", *ownWalls)
		if len(collapse) > 0 {
			params["collapse"] = []string(collapse)
		}
		if len(expand) > 0 {
			params["expand"] = []string(expand)
		}
		if len(order) > 0 {
			params["bandOrder"] = []string(order)
		}
		switch strings.ToLower(*sidebar) {
		case "":
		case "on", "true", "yes", "show":
			params["sidebar"] = true
		case "off", "false", "no", "hide":
			params["sidebar"] = false
		default:
			return usagef("--sidebar on or off, not %q", *sidebar)
		}
		if *home {
			params["home"] = true
		}
		if len(params) == 0 {
			return usagef("wall set needs something to set (hesperctl help wall set)")
		}
		setString(params, "wall", *wall)
		var res json.RawMessage
		if err := appCall(ctx, c, *noLaunch, true, "app.wall.set", params, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		var w appWall
		json.Unmarshal(res, &w)
		printWalls([]appWall{w})
		return nil
	})
}

// deskRun is the Run of a desk subcommand: app.desk {action} with n names.
func deskRun(action string, n int) func(context.Context, *flag.FlagSet, []string) error {
	return func(ctx context.Context, f *flag.FlagSet, args []string) error {
		asJSON := jsonFlag(f)
		noLaunch := noLaunchFlag(f)
		return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, pos []string) error {
			if len(pos) != n {
				switch {
				case n == 0:
					return usagef("desk ls takes no arguments")
				case action == "rename":
					return usagef("desk rename OLD NEW")
				case action == "remove":
					return usagef("desk rm NAME")
				}
				return usagef("desk %s NAME", action)
			}
			params := map[string]any{"action": action}
			if n >= 1 {
				params["name"] = pos[0]
			}
			if n == 2 {
				params["newName"] = pos[1]
			}
			var res json.RawMessage
			if err := appCall(ctx, c, *noLaunch, action != "switch", "app.desk", params, &res); err != nil {
				return err
			}
			if *asJSON {
				return output(res)
			}
			var desks []appDesk
			if err := json.Unmarshal(res, &desks); err != nil {
				return err
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "DESK\tCURRENT\tDISPLAYS\tWALLS\tAGENT WINDOWS")
			for _, d := range desks {
				cur := ""
				if d.Current {
					cur = "*"
				}
				name := d.Name
				if d.Automatic {
					name += " (automatic)"
				}
				displays := d.Displays
				if d.ThisDisplays {
					displays += " (these)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\n", name, cur, displays, d.Walls, d.AgentWindows)
			}
			return tw.Flush()
		})
	}
}
