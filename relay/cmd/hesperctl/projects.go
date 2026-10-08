package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Projects and groups (projects.*, groups.*):
//
//	projects ls | recent | update | rm | promote | clone
//	groups ls | save | rm
//
// Projects and groups are named by id or by their name when unique; a
// project also by one of its folders.

const projectRefHelp = "PROJECT is a project's id (p-…), its name when unique, or one of its folders."
const groupRefHelp = "GROUP is a group's id (g-…) or its name when unique."

func init() {
	for _, c := range []Command{
		{Name: "projects ls", ReadOnly: true, Summary: "List the projects", Usage: "projects ls [--json]",
			Help:     "Every project (shared by the owner's Macs), most recently used first, then the scratch folders agents run in. A table of id, kind, name, groups, the folder on this Mac (else machine:folder) and when an agent last started there.",
			Output:   "[{id, name, color, colorSet?, kind: repo|package|folder|reference|scratch, identity:{remote?, package?, local?}, parentId?, paths:{machine: folder}, groups:[groupId], defaults:{profile?, machine?}, detectedPackages?, lastUsed}]",
			Examples: []string{"hesperctl projects ls", "hesperctl projects ls --json | jq -r '.[] | select(.kind==\"repo\") | .name'"},
			Run:      projectsList},
		{Name: "projects recent", ReadOnly: true, Summary: "List the folders agents started in recently", Usage: "projects recent [--json]",
			Help:   "The projects with a folder on this Mac first (most recently used first), then other folders agents started in (at most 30). What Hesper's composer offers.",
			Output: "[{path, name, lastUsed, projectId?}]",
			Run:    projectsRecent},
		{Name: "projects update", Summary: "Rename a project, or change its color, kind or defaults", Usage: "projects update PROJECT [--name N] [--color C] [--kind K] [--default-profile P] [--default-machine M] [--json]",
			Help:     projectRefHelp + " Only the flags given change. --color \"\" goes back to the automatic color; --default-profile \"\" and --default-machine \"\" clear a default (new agents in the project start with them).",
			Output:   "the project (as in projects ls)",
			Examples: []string{"hesperctl projects update app --name 'App (iOS)' --color '#7aa2f7'", "hesperctl projects update p-1a2b3c4d5e6f7a8b --default-profile claude-unattended --default-machine mini"},
			Run:      projectsUpdate},
		{Name: "projects rm", Destructive: true, Summary: "Remove a project", Usage: "projects rm PROJECT",
			Help: projectRefHelp + " Removes it on every Mac (its folders stay). Its agents fall back to the next project containing their folder, else scratch. projects promote brings it back.",
			Run:  projectsRemove},
		{Name: "projects promote", Summary: "Make a folder a project", Usage: "projects promote PATH [--machine M] [--name N] [--kind K] [--json]",
			Help:     "A repository's folder becomes its repository project, a folder inside a repository a package or folder project, any other folder a folder project (the same project again when it already is one; a removed one comes back). PATH is on --machine (default this Mac; relative paths only here). Prints the project's id.",
			Output:   "the project (as in projects ls)",
			Examples: []string{"hesperctl projects promote ~/notes --kind reference", "hesperctl projects promote /Users/me/src/tool --machine mini --name tool"},
			Run:      projectsPromote},
		{Name: "projects clone", Summary: "Clone a git repository into the projects folder", Usage: "projects clone URL [--machine M] [--json]",
			Help:     "git clone into the projects root (~/projects, $HESPER_PROJECT_ROOT) of --machine (default this Mac) and print the folder. The repository becomes a project when an agent starts there.",
			Output:   "{path}",
			Examples: []string{"hesperctl projects clone git@github.com:me/app.git", "dir=$(hesperctl projects clone https://github.com/me/app --machine mini)"},
			Run:      projectsClone},
		{Name: "groups ls", ReadOnly: true, Summary: "List the project groups", Usage: "groups ls [--json]",
			Help:   "Groups in their order: id, name, color and projects.",
			Output: "[{id, name, projectIds, order, color?}]",
			Run:    groupsList},
		{Name: "groups save", Summary: "Create a group, or change one", Usage: "groups save [GROUP] [--name N] [--color C] [--project P]… [--remove-project P]… [--order N] [--json]",
			Help:     "Without GROUP: a new group (--name required) after the others. With GROUP (" + groupRefHelp + "): the flags given change it. --project adds a project (" + projectRefHelp + "), --remove-project takes one out; both repeat. --color \"\" clears the color. Prints the group's id.",
			Output:   "the group: {id, name, projectIds, order, color?}",
			Examples: []string{"hesperctl groups save --name work --project app --project api", "hesperctl groups save work --remove-project api --color '#bb9af7'"},
			Run:      groupsSave},
		{Name: "groups rm", Destructive: true, Summary: "Remove a group (its projects stay)", Usage: "groups rm GROUP", Help: groupRefHelp, Run: groupsRemove},
	} {
		c.Group = groupProjects
		register(c)
	}
}

// listFlag is a flag that repeats; values may also be comma-separated.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error {
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			*l = append(*l, v)
		}
	}
	return nil
}

// setFlags are the flags given on the command line.
func setFlags(f *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	f.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	return set
}

// tilde shortens a folder under home to ~/….
func tilde(path string) string {
	if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}

// expandPath makes a folder given here absolute (~ is home).
func expandPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = home + path[1:]
	}
	return filepath.Abs(path)
}

// ago is a time as an age: "5m ago", "3d ago"; "-" for none.
func ago(t time.Time) string {
	if t.IsZero() || t.Year() < 2000 {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Local().Format("2006-01-02")
}

// localMachine is this Mac's short name (hello).
func localMachine(ctx context.Context, c *wire.Client) string {
	var h wire.HelloResult
	if err := c.Call(ctx, "hello", wire.HelloParams{Client: "hesperctl", Version: "1"}, &h); err != nil {
		return ""
	}
	return h.Machine
}

// looksLikePath: a project reference that is a folder rather than a name.
func looksLikePath(ref string) bool {
	return ref == "." || ref == ".." || ref == "~" || strings.ContainsRune(ref, '/')
}

// matchByName is the indexes of the n items whose name is ref: exactly,
// else ignoring case.
func matchByName(n int, name func(int) string, ref string) []int {
	var exact, fold []int
	for i := range n {
		switch nm := name(i); {
		case nm == ref:
			exact = append(exact, i)
		case strings.EqualFold(nm, ref):
			fold = append(fold, i)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return fold
}

// resolveProject finds a project by id, unique name or folder (any
// machine's; a scratch folder too).
func resolveProject(ctx context.Context, c *wire.Client, ref string) (wire.ProjectInfo, error) {
	var list []wire.ProjectInfo
	if err := c.Call(ctx, "projects.list", nil, &list); err != nil {
		return wire.ProjectInfo{}, err
	}
	for _, p := range list {
		if p.ID == ref {
			return p, nil
		}
	}
	if looksLikePath(ref) {
		path, err := expandPath(ref)
		if err != nil {
			return wire.ProjectInfo{}, err
		}
		real, _ := filepath.EvalSymlinks(path)
		for _, p := range list {
			for _, folder := range p.Paths {
				if folder == path || folder == real {
					return p, nil
				}
			}
		}
		return wire.ProjectInfo{}, failf(wire.CodeNotFound, "no project in %s (projects promote makes it one)", path)
	}
	hits := matchByName(len(list), func(i int) string { return list[i].Name }, ref)
	// A real project wins a tie with scratch folders of the same name.
	var real []int
	for _, i := range hits {
		if list[i].Kind != wire.ProjectScratch {
			real = append(real, i)
		}
	}
	if len(real) > 0 {
		hits = real
	}
	switch len(hits) {
	case 0:
		return wire.ProjectInfo{}, failf(wire.CodeNotFound, "no project %s (projects ls lists them)", ref)
	case 1:
		return list[hits[0]], nil
	}
	var ids []string
	for _, i := range hits {
		ids = append(ids, list[i].ID)
	}
	return wire.ProjectInfo{}, failf(codeAmbiguous, "%q names %d projects: use an id (%s)", ref, len(hits), strings.Join(ids, ", "))
}

// resolveGroup finds a group by id or unique name.
func resolveGroup(ctx context.Context, c *wire.Client, ref string) (wire.Group, []wire.Group, error) {
	var list []wire.Group
	if err := c.Call(ctx, "groups.list", nil, &list); err != nil {
		return wire.Group{}, nil, err
	}
	for _, g := range list {
		if g.ID == ref {
			return g, list, nil
		}
	}
	hits := matchByName(len(list), func(i int) string { return list[i].Name }, ref)
	switch len(hits) {
	case 0:
		return wire.Group{}, list, failf(wire.CodeNotFound, "no group %s (groups ls lists them)", ref)
	case 1:
		return list[hits[0]], list, nil
	}
	return wire.Group{}, list, failf(codeAmbiguous, "%q names %d groups: use an id", ref, len(hits))
}

// projectFolder is where a project is, for a table: the folder on this
// Mac, else machine:folder of each.
func projectFolder(p wire.ProjectInfo, local string) string {
	if dir, ok := p.Paths[local]; ok {
		return tilde(dir)
	}
	var machines []string
	for m := range p.Paths {
		machines = append(machines, m)
	}
	sort.Strings(machines)
	var out []string
	for _, m := range machines {
		out = append(out, m+":"+tilde(p.Paths[m]))
	}
	return strings.Join(out, " ")
}

func printProjects(w io.Writer, list []wire.ProjectInfo, groups []wire.Group, local string) error {
	names := map[string]string{}
	for _, g := range groups {
		names[g.ID] = g.Name
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tNAME\tGROUPS\tFOLDER\tLAST USED")
	for _, p := range list {
		var gs []string
		for _, id := range p.Groups {
			if n := names[id]; n != "" {
				gs = append(gs, n)
			}
		}
		group := strings.Join(gs, ",")
		if group == "" {
			group = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, p.Kind, p.Name, group, projectFolder(p, local), ago(p.LastUsed))
	}
	return tw.Flush()
}

func projectsList(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 0 {
			return usagef("usage: hesperctl projects ls [--json]")
		}
		var list []wire.ProjectInfo
		if err := c.Call(ctx, "projects.list", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			return output(list)
		}
		var groups []wire.Group
		c.Call(ctx, "groups.list", nil, &groups)
		return printProjects(os.Stdout, list, groups, localMachine(ctx, c))
	})
}

func projectsRecent(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, _ []string) error {
		var list []wire.Project
		if err := c.Call(ctx, "projects.recent", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			return output(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "FOLDER\tNAME\tPROJECT\tLAST USED")
		for _, p := range list {
			id := p.ProjectID
			if id == "" {
				id = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", tilde(p.Path), p.Name, id, ago(p.LastUsed))
		}
		return tw.Flush()
	})
}

// printProject prints a project's id, or with --json the project.
func printProject(p wire.ProjectInfo, asJSON bool) error {
	if asJSON {
		return output(p)
	}
	fmt.Println(p.ID)
	return nil
}

func projectsUpdate(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	name := f.String("name", "", "New name")
	color := f.String("color", "", "Color #rrggbb (\"\": the automatic one)")
	kind := f.String("kind", "", "repo, package, folder or reference")
	profile := f.String("default-profile", "", "Profile new agents in it start with (\"\": none)")
	machine := f.String("default-machine", "", "Machine new agents in it start on (\"\": none)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl projects update PROJECT [--name N] [--color C] [--kind K] [--default-profile P] [--default-machine M]")
		}
		set := setFlags(f)
		p, err := resolveProject(ctx, c, positional[0])
		if err != nil {
			return err
		}
		u := wire.ProjectUpdateParams{ID: p.ID}
		if set["name"] {
			u.Name = name
		}
		if set["color"] {
			u.Color = color
		}
		if set["kind"] {
			u.Kind = kind
		}
		if set["default-profile"] || set["default-machine"] {
			// defaults are replaced whole: keep the one not given.
			d := p.Defaults
			if set["default-profile"] {
				d.Profile = *profile
			}
			if set["default-machine"] {
				d.Machine = *machine
			}
			u.Defaults = &d
		}
		if u.Name == nil && u.Color == nil && u.Kind == nil && u.Defaults == nil {
			return usagef("nothing to change: give --name, --color, --kind, --default-profile or --default-machine")
		}
		var res wire.ProjectInfo
		if err := c.Call(ctx, "projects.update", u, &res); err != nil {
			return err
		}
		return printProject(res, *asJSON)
	})
}

func projectsRemove(ctx context.Context, f *flag.FlagSet, args []string) error {
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl projects rm PROJECT")
		}
		p, err := resolveProject(ctx, c, positional[0])
		if err != nil {
			return err
		}
		return c.Call(ctx, "projects.remove", wire.IDParams{ID: p.ID}, nil)
	})
}

func projectsPromote(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	machine := f.String("machine", "", "Machine the folder is on (short name; default this Mac)")
	name := f.String("name", "", "Name (default: the repository's, package's or folder's)")
	kind := f.String("kind", "", "repo, package, folder or reference (default: detected)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl projects promote PATH [--machine M] [--name N] [--kind K]")
		}
		path := positional[0]
		if *machine == "" || *machine == localMachine(ctx, c) {
			var err error
			if path, err = expandPath(path); err != nil {
				return err
			}
		} else if !filepath.IsAbs(path) {
			return usagef("PATH on another machine must be absolute")
		}
		var res wire.ProjectInfo
		if err := c.Call(ctx, "projects.promote", wire.ProjectPromoteParams{Machine: *machine, Path: path, Name: *name, Kind: *kind}, &res); err != nil {
			return err
		}
		return printProject(res, *asJSON)
	})
}

func projectsClone(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	machine := f.String("machine", "", "Machine to clone on (short name; default this Mac)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl projects clone URL [--machine M]")
		}
		var res struct {
			Path string `json:"path"`
		}
		params := map[string]string{"url": positional[0]}
		if *machine != "" {
			params["machine"] = *machine
		}
		if err := c.Call(ctx, "projects.clone", params, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		fmt.Println(res.Path)
		return nil
	})
}

func groupsList(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, _ []string) error {
		var list []wire.Group
		if err := c.Call(ctx, "groups.list", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			return output(list)
		}
		var projects []wire.ProjectInfo
		c.Call(ctx, "projects.list", nil, &projects)
		names := map[string]string{}
		for _, p := range projects {
			names[p.ID] = p.Name
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tCOLOR\tPROJECTS")
		for _, g := range list {
			var ps []string
			for _, id := range g.ProjectIDs {
				if n := names[id]; n != "" {
					ps = append(ps, n)
				} else {
					ps = append(ps, id)
				}
			}
			color := g.Color
			if color == "" {
				color = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", g.ID, g.Name, color, strings.Join(ps, ", "))
		}
		return tw.Flush()
	})
}

func groupsSave(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	name := f.String("name", "", "Name (required for a new group)")
	color := f.String("color", "", "Color #rrggbb (\"\": none)")
	order := f.Int("order", 0, "Position among the groups (default: a new group goes last)")
	var add, remove listFlag
	f.Var(&add, "project", "Add this project (repeatable)")
	f.Var(&remove, "remove-project", "Take this project out (repeatable)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 1 {
			return usagef("usage: hesperctl groups save [GROUP] [--name N] [--color C] [--project P]… [--remove-project P]…")
		}
		set := setFlags(f)
		var g wire.Group
		if len(positional) == 1 {
			found, _, err := resolveGroup(ctx, c, positional[0])
			if err != nil {
				return err
			}
			g = found
		} else {
			if strings.TrimSpace(*name) == "" {
				return usagef("a new group needs --name (or name a GROUP to change)")
			}
			var list []wire.Group
			if err := c.Call(ctx, "groups.list", nil, &list); err != nil {
				return err
			}
			// After the others, as the app's New Group.
			for _, o := range list {
				g.Order = max(g.Order, o.Order+1)
			}
		}
		if set["name"] {
			g.Name = *name
		}
		if set["color"] {
			g.Color = *color
		}
		if set["order"] {
			g.Order = *order
		}
		for _, ref := range add {
			p, err := resolveProject(ctx, c, ref)
			if err != nil {
				return err
			}
			if !contains(g.ProjectIDs, p.ID) {
				g.ProjectIDs = append(g.ProjectIDs, p.ID)
			}
		}
		for _, ref := range remove {
			id := ref
			if !contains(g.ProjectIDs, ref) {
				p, err := resolveProject(ctx, c, ref)
				if err != nil {
					return err
				}
				id = p.ID
			}
			kept := g.ProjectIDs[:0:0]
			for _, pid := range g.ProjectIDs {
				if pid != id {
					kept = append(kept, pid)
				}
			}
			g.ProjectIDs = kept
		}
		if g.ProjectIDs == nil {
			g.ProjectIDs = []string{}
		}
		var res wire.Group
		if err := c.Call(ctx, "groups.save", wire.GroupSaveParams{Group: g}, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		fmt.Println(res.ID)
		return nil
	})
}

func groupsRemove(ctx context.Context, f *flag.FlagSet, args []string) error {
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl groups rm GROUP")
		}
		g, _, err := resolveGroup(ctx, c, positional[0])
		if err != nil {
			return err
		}
		return c.Call(ctx, "groups.remove", wire.IDParams{ID: g.ID}, nil)
	})
}
