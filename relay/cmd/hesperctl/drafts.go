package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Drafts (drafts.*: new agents not started yet, the draft tiles of
// Hesper's wall), launch profiles (profiles.list) and hesperd's status
// (hello).

func init() {
	for _, c := range []Command{
		{Name: "drafts ls", Summary: "List the drafts (new agents not started yet)", Usage: "drafts ls [--json]",
			Help:   "Drafts are Hesper's draft tiles: a task with its folder, machine and profile, kept by hesperd until started or removed. A table of id, machine, folder, profile, last change and the task's first line.",
			Output: "[{id, text, machine?, machineExplicit?, project? (folder), band? (project id), wall?, projectLocked?, profile?, worktree?, branch?, attachments?, after?, parked?, created, updated}]",
			Run:    draftsList},
		{Name: "drafts save", Summary: "Create or change a draft", Usage: "drafts save [TASK…|-] [--id D] [--project P] [--machine M] [--kind K] [--profile P] [--worktree] [--branch B] [--json]",
			Help: "Without --id: a new draft (shown in Hesper's wall, ready to start); with --id: the flags given (and TASK, when given) change that draft. TASK - reads stdin. " +
				"--project is a folder, or a project (id or unique name: the draft opens in its band, in its folder on --machine). --kind picks the kind's default profile unless --profile. Prints the draft's id.",
			Output:   "the draft (as in drafts ls)",
			Examples: []string{"hesperctl drafts save --project ~/src/app --kind codex fix the login redirect", "git diff | hesperctl drafts save --project app -", "hesperctl drafts save --id d-k2m9x0ab --worktree --branch fix-login"},
			Run:      draftsSave},
		{Name: "drafts rm", Summary: "Remove a draft", Usage: "drafts rm ID", Run: draftsRemove},
		{Name: "profiles", Summary: "List the launch profiles and the defaults", Usage: "profiles [--json]",
			Help:   "Profiles are how agents start (~/.config/hesper/profiles.json): kind and command line. The defaults (settings.json) name the kind a new agent gets, the profile per kind and per project folder.",
			Output: "{profiles:{name:{kind, argv}}, defaults:{kind, kinds:{kind: profile}, projects?:{folder: profile}}}",
			Run:    profilesCommand},
		{Name: "status", Summary: "Show hesperd and the machines it reaches", Usage: "status [--json]",
			Help:     "hesperd's version and this Mac's short name, then every machine: online, round trip and route (local, direct or relay). Exit 4 when hesperd is not running.",
			Output:   "{daemon, version, machine, machines:[{short, name, online, rttMs, route}]}",
			Examples: []string{"hesperctl status", "hesperctl status --json | jq -r '.machines[] | select(.online) | .short'"},
			Run:      statusCommand},
	} {
		c.Group = groupAgents
		register(c)
	}
}

func draftsList(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, _ []string) error {
		var list []wire.Draft
		if err := c.Call(ctx, "drafts.list", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			if list == nil {
				list = []wire.Draft{}
			}
			return output(list)
		}
		return printDrafts(os.Stdout, list)
	})
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printDrafts(w io.Writer, list []wire.Draft) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMACHINE\tFOLDER\tPROFILE\tUPDATED\tTASK")
	for _, d := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", d.ID, orDash(d.Machine), orDash(tilde(d.Project)), orDash(d.Profile), ago(d.Updated), oneLine(d.Text, 60))
	}
	return tw.Flush()
}

func draftsSave(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	id := f.String("id", "", "Change this draft (default: a new one)")
	project := f.String("project", "", "Folder, or project (id or unique name)")
	machine := f.String("machine", "", "Machine the agent will run on (short name; default this Mac or the project's)")
	kind := f.String("kind", "", "claude, codex or shell: the kind's default profile")
	profile := f.String("profile", "", "Launch profile (see profiles)")
	worktree := f.Bool("worktree", false, "Start it in a new worktree")
	branch := f.String("branch", "", "Branch of the worktree")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		set := setFlags(f)
		var d wire.Draft
		if *id != "" {
			var list []wire.Draft
			if err := c.Call(ctx, "drafts.list", nil, &list); err != nil {
				return err
			}
			found := false
			for _, o := range list {
				if o.ID == *id {
					d, found = o, true
				}
			}
			if !found {
				return failf(wire.CodeNotFound, "no draft %s (drafts ls lists them)", *id)
			}
		}
		if len(positional) > 0 {
			text := strings.Join(positional, " ")
			if text == "-" {
				data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
				if err != nil {
					return err
				}
				text = strings.TrimRight(string(data), "\n")
			}
			d.Text = text
		}
		local := localMachine(ctx, c)
		if set["machine"] {
			d.Machine, d.MachineExplicit = *machine, *machine != ""
			if d.Machine == local {
				d.Machine = ""
			}
		}
		var projectDefault string
		if set["project"] {
			d.Band, d.ProjectLocked = "", false
			var p wire.ProjectInfo
			var err error
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
				d.Project = ""
			case p.ID == "":
				dir, err := expandPath(*project)
				if err != nil {
					return err
				}
				d.Project = dir
			default:
				where := d.Machine
				if where == "" {
					where = local
				}
				folder, ok := p.Paths[where]
				if !ok && !set["machine"] {
					// Not on this Mac: the project's default machine, else
					// the first that has it (as the app places a draft).
					var machines []string
					for m := range p.Paths {
						machines = append(machines, m)
					}
					sort.Strings(machines)
					if dm := p.Defaults.Machine; dm != "" && p.Paths[dm] != "" {
						machines = []string{dm}
					}
					if len(machines) > 0 {
						where, folder, ok = machines[0], p.Paths[machines[0]], true
						if where != local {
							d.Machine = where
						}
					}
				}
				if !ok {
					return failf(wire.CodeNotFound, "project %s has no folder on %s", p.Name, where)
				}
				d.Project = folder
				if p.Kind != wire.ProjectScratch {
					d.Band = p.ID
				}
				projectDefault = p.Defaults.Profile
			}
		}
		switch {
		case set["profile"]:
			d.Profile = *profile
		case set["kind"]:
			var profiles wire.ProfilesResult
			if err := c.Call(ctx, "profiles.list", nil, &profiles); err != nil {
				return err
			}
			name, ok := profiles.Defaults.Kinds[*kind]
			if !ok {
				return usagef("--kind %q: no default profile for it (profiles lists them)", *kind)
			}
			d.Profile = name
		case projectDefault != "":
			d.Profile = projectDefault
		}
		if set["worktree"] {
			d.Worktree = worktree
		}
		if set["branch"] {
			d.Branch = *branch
		}
		var res wire.Draft
		if err := c.Call(ctx, "drafts.save", wire.DraftSaveParams{Draft: d}, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		fmt.Println(res.ID)
		return nil
	})
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func draftsRemove(ctx context.Context, f *flag.FlagSet, args []string) error {
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl drafts rm ID")
		}
		return c.Call(ctx, "drafts.remove", wire.IDParams{ID: positional[0]}, nil)
	})
}

func profilesCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, _ []string) error {
		var p wire.ProfilesResult
		if err := c.Call(ctx, "profiles.list", nil, &p); err != nil {
			return err
		}
		if *asJSON {
			return output(p)
		}
		defaultOf := map[string][]string{}
		for kind, name := range p.Defaults.Kinds {
			defaultOf[name] = append(defaultOf[name], kind)
		}
		names := make([]string, 0, len(p.Profiles))
		for name := range p.Profiles {
			names = append(names, name)
		}
		sort.Strings(names)
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "PROFILE\tKIND\tDEFAULT FOR\tCOMMAND")
		for _, name := range names {
			pr := p.Profiles[name]
			kinds := defaultOf[name]
			sort.Strings(kinds)
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, pr.Kind, orDash(strings.Join(kinds, ",")), strings.Join(pr.Argv, " "))
		}
		tw.Flush()
		fmt.Printf("\nDefault kind: %s\n", orDash(p.Defaults.Kind))
		if len(p.Defaults.Projects) > 0 {
			fmt.Println("Per folder:")
			folders := make([]string, 0, len(p.Defaults.Projects))
			for dir := range p.Defaults.Projects {
				folders = append(folders, dir)
			}
			sort.Strings(folders)
			for _, dir := range folders {
				fmt.Printf("  %s  %s\n", tilde(dir), p.Defaults.Projects[dir])
			}
		}
		return nil
	})
}

func statusCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, _ []string) error {
		var h wire.HelloResult
		if err := c.Call(ctx, "hello", wire.HelloParams{Client: "hesperctl", Version: "1"}, &h); err != nil {
			return err
		}
		if *asJSON {
			return output(h)
		}
		fmt.Printf("%s %s on %s\n\n", h.Daemon, h.Version, h.Machine)
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "MACHINE\tNAME\tONLINE\tRTT\tROUTE")
		for _, m := range h.Machines {
			online, rtt := "no", "-"
			if m.Online {
				online = "yes"
				if m.Route != "local" {
					rtt = fmt.Sprintf("%d ms", m.RTTMs)
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.Short, m.Name, online, rtt, orDash(m.Route))
		}
		return tw.Flush()
	})
}
