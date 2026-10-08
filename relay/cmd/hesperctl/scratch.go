package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Scratch projects (projects.scratch*, projects.promote {id}):
//
//	scratch ls | new | keep | archive | restore | promote | rm
//
// A scratch is named by its id, its name when unique, or its folder.

const scratchRefHelp = "SCRATCH is a scratch project's id (p-…), its name when unique, or its folder."

func init() {
	for _, c := range []Command{
		{Name: "scratch ls", ReadOnly: true, Summary: "List the scratch projects", Usage: "scratch ls [--all] [--json]",
			Help: "Scratch projects: lightweight projects in ~/scratch/<date>-<slug> on the Mac they were made on (their home), each a local Git repository. " +
				"active while agents run in one, resting without; after 14 days resting (settings.json scratch.archiveAfterDays) the home moves it to ~/scratch/.archive " +
				"(archived), after 30 more days (scratch.deleteAfterDays) deletes it; keep pins one. --all lists the archived ones too. " +
				"A table of id, state, name, home, folder and when an agent last started there.",
			Output:   "[{id, name, kind: scratch, paths, created, lastUsed, scratch: {state: active|resting|archived, keep, archivedAt?, home, git}, …}] (as projects ls)",
			Examples: []string{"hesperctl scratch ls", "hesperctl scratch ls --all --json | jq -r '.[] | select(.scratch.state==\"archived\") | .name'"},
			Run:      scratchList},
		{Name: "scratch new", Summary: "Make a scratch project", Usage: "scratch new NAME… [--machine M] [--json]",
			Help: "A new folder ~/scratch/<yyyy-mm-dd>-<slug of NAME> (-2, -3 when taken) on --machine (default this Mac), with git init and an empty first commit, " +
				"and its scratch project. Prints the folder. To start an agent in a new scratch project at once: new --scratch TASK.",
			Output:   "{project, path}",
			Examples: []string{"cd \"$(hesperctl scratch new csv cleanup)\"", "hesperctl scratch new spike --machine mini --json"},
			Run:      scratchNew},
		{Name: "scratch keep", Summary: "Keep a scratch project (never archived or deleted)", Usage: "scratch keep SCRATCH [--off] [--json]",
			Help: scratchRefHelp + " --off lets the lifecycle archive and delete it again.", Output: "the project (as in scratch ls)",
			Run: scratchKeep},
		{Name: "scratch archive", Summary: "Archive a scratch project now", Usage: "scratch archive SCRATCH [--json]",
			Help: scratchRefHelp + " Its home moves its folder to ~/scratch/.archive; refused (busy) while agents run in it.", Output: "the project",
			Run: scratchAction("projects.scratchArchive")},
		{Name: "scratch restore", Summary: "Bring an archived scratch project back", Usage: "scratch restore SCRATCH [--json]",
			Help: scratchRefHelp + " Its folder moves back to ~/scratch (resting, its 14 days start again).", Output: "the project",
			Run: scratchAction("projects.scratchRestore")},
		{Name: "scratch promote", Summary: "Make a scratch project a project in ~/projects", Usage: "scratch promote SCRATCH [--name N] [--github] [--json]",
			Help: scratchRefHelp + " Its folder moves to ~/projects/<slug of the name> (--name, default its own) and it becomes a repository project " +
				"with the same id, so its sessions stay linked. --github also runs gh repo create --private --source . --push there. " +
				"Refused (busy) while agents run in it.",
			Output:   "the project (as in projects ls)",
			Examples: []string{"hesperctl scratch promote csv-cleanup --name csv-tool --github"},
			Run:      scratchPromote},
		{Name: "scratch rm", Destructive: true, Summary: "Delete a scratch project and its folder", Usage: "scratch rm SCRATCH",
			Help: scratchRefHelp + " Deletes its folder on its home and the project on every Mac (History keeps its sessions, marked folder removed). " +
				"Refused (busy) while agents run in it.",
			Run: scratchAction("projects.scratchDelete")},
	} {
		c.Group = groupProjects
		register(c)
	}
}

// scratchProjects are the scratch projects of projects.list (archived
// ones with all).
func scratchProjects(ctx context.Context, c *wire.Client, all bool) ([]wire.ProjectInfo, error) {
	var list []wire.ProjectInfo
	if err := c.Call(ctx, "projects.list", wire.ProjectListParams{Archived: all}, &list); err != nil {
		return nil, err
	}
	out := []wire.ProjectInfo{}
	for _, p := range list {
		if p.Kind == wire.ProjectScratch && p.Scratch != nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// resolveScratch finds a scratch project (archived ones too) by id,
// unique name or folder.
func resolveScratch(ctx context.Context, c *wire.Client, ref string) (wire.ProjectInfo, error) {
	list, err := scratchProjects(ctx, c, true)
	if err != nil {
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
		return wire.ProjectInfo{}, failf(wire.CodeNotFound, "no scratch project in %s", path)
	}
	hits := matchByName(len(list), func(i int) string { return list[i].Name }, ref)
	if len(hits) == 0 {
		// The folder's name: 2026-10-08-csv-cleanup.
		hits = matchByName(len(list), func(i int) string {
			for _, folder := range list[i].Paths {
				return filepath.Base(folder)
			}
			return ""
		}, ref)
	}
	switch len(hits) {
	case 0:
		return wire.ProjectInfo{}, failf(wire.CodeNotFound, "no scratch project %s (scratch ls --all lists them)", ref)
	case 1:
		return list[hits[0]], nil
	}
	var ids []string
	for _, i := range hits {
		ids = append(ids, list[i].ID)
	}
	return wire.ProjectInfo{}, failf(codeAmbiguous, "%q names %d scratch projects: use an id (%s)", ref, len(hits), strings.Join(ids, ", "))
}

func printScratch(w io.Writer, list []wire.ProjectInfo, local string) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tNAME\tHOME\tFOLDER\tLAST USED")
	for _, p := range list {
		state := p.Scratch.State
		if p.Scratch.Keep {
			state += ",keep"
		}
		home := p.Scratch.Home
		if home == "" {
			home = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", p.ID, state, p.Name, home, projectFolder(p, local), ago(p.LastUsed))
	}
	return tw.Flush()
}

func scratchList(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	all := f.Bool("all", false, "Archived ones too")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 0 {
			return usagef("usage: hesperctl scratch ls [--all]")
		}
		list, err := scratchProjects(ctx, c, *all)
		if err != nil {
			return err
		}
		if *asJSON {
			return output(list)
		}
		return printScratch(os.Stdout, list, localMachine(ctx, c))
	})
}

func scratchNew(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	machine := f.String("machine", "", "Machine to make it on (short name; default this Mac)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		name := strings.TrimSpace(strings.Join(positional, " "))
		if name == "" {
			return usagef("usage: hesperctl scratch new NAME… [--machine M]")
		}
		var res wire.ScratchResult
		if err := c.Call(ctx, "projects.scratch", wire.ScratchParams{Name: name, Machine: *machine}, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		fmt.Println(res.Path)
		return nil
	})
}

func scratchKeep(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	off := f.Bool("off", false, "Stop keeping it")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl scratch keep SCRATCH [--off]")
		}
		p, err := resolveScratch(ctx, c, positional[0])
		if err != nil {
			return err
		}
		var res wire.ProjectInfo
		if err := c.Call(ctx, "projects.scratchKeep", wire.ScratchKeepParams{ID: p.ID, Keep: !*off}, &res); err != nil {
			return err
		}
		return printProject(res, *asJSON)
	})
}

// scratchAction is a command that names one scratch project to method:
// archive and restore print the project, rm nothing.
func scratchAction(method string) func(context.Context, *flag.FlagSet, []string) error {
	return func(ctx context.Context, f *flag.FlagSet, args []string) error {
		asJSON := jsonFlag(f)
		return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
			if len(positional) != 1 {
				verb := map[string]string{"projects.scratchArchive": "archive", "projects.scratchRestore": "restore", "projects.scratchDelete": "rm"}[method]
				return usagef("usage: hesperctl scratch %s SCRATCH", verb)
			}
			p, err := resolveScratch(ctx, c, positional[0])
			if err != nil {
				return err
			}
			if method == "projects.scratchDelete" {
				return c.Call(ctx, method, wire.IDParams{ID: p.ID}, nil)
			}
			var res wire.ProjectInfo
			if err := c.Call(ctx, method, wire.IDParams{ID: p.ID}, &res); err != nil {
				return err
			}
			return printProject(res, *asJSON)
		})
	}
}

func scratchPromote(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	name := f.String("name", "", "Name (default: the scratch project's); its folder is ~/projects/<slug>")
	github := f.Bool("github", false, "Also make it a private GitHub repository (gh repo create --private --source . --push)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl scratch promote SCRATCH [--name N] [--github]")
		}
		p, err := resolveScratch(ctx, c, positional[0])
		if err != nil {
			return err
		}
		params := wire.ProjectPromoteParams{ID: p.ID, Name: *name}
		if *github {
			params.CreateRepo = wire.CreateRepoGitHub
		}
		var res wire.ProjectInfo
		if err := c.Call(ctx, "projects.promote", params, &res); err != nil {
			return err
		}
		return printProject(res, *asJSON)
	})
}
