package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// `hesperctl reference`: the whole CLI as Markdown for LLM agents (skills,
// agents inside Hesper driving other agents), generated from the registry
// and wire's types so it never drifts.

// commandDoc is a command as help --json and reference --json print it.
type commandDoc struct {
	Name     string    `json:"name"`
	Aliases  []string  `json:"aliases,omitempty"`
	Group    string    `json:"group"`
	Summary  string    `json:"summary"`
	Usage    string    `json:"usage"`
	Help     string    `json:"help,omitempty"`
	Flags    []flagDoc `json:"flags,omitempty"`
	Output   string    `json:"output,omitempty"`
	Examples []string  `json:"examples,omitempty"`
}

func describe(c *Command) commandDoc {
	return commandDoc{Name: c.Name, Aliases: c.Aliases, Group: c.Group, Summary: c.Summary, Usage: "hesperctl " + c.Usage,
		Help: strings.TrimSpace(c.Help), Flags: commandFlags(c), Output: c.Output, Examples: c.Examples}
}

func describeAll() []commandDoc {
	names, by := groups()
	var list []commandDoc
	for _, g := range names {
		for _, c := range by[g] {
			list = append(list, describe(c))
		}
	}
	return list
}

// sampleAgent is the Agent object shown in the reference (marshalled, so
// its fields are wire.Agent's).
func sampleAgent() wire.Agent {
	at := time.Date(2026, 10, 6, 19, 2, 11, 0, time.UTC)
	return wire.Agent{
		ID: "mini/a7f3k2", Machine: "mini", Kind: wire.KindClaude, Profile: "claude-auto-rc", Name: "push-provider",
		Task: "Add the FCM push provider", Project: "/Users/me/src/app", ProjectID: "github.com/me/app",
		Worktree: "/Users/me/worktrees/app/push-provider", Branch: "push-provider",
		State: wire.StateApproval, StateSince: at,
		Attention: &wire.Attention{Kind: "approval", Title: "Bash", Detail: "git push origin push-provider", Options: []string{wire.Allow, wire.Always, wire.Deny}},
		Summary:   "Opened PR #482 (draft)", Activity: "Bash: git push origin push-provider", SessionID: "6f1c…",
		Size: wire.Size{Cols: 120, Rows: 40}, Created: at.Add(-time.Hour), PID: 12345,
	}
}

const concepts = `## Concepts

**hesperd** is the daemon on each Mac that owns the agents (their terminals,
states and history). Hesper.app and hesperctl are clients of the same
socket ($HESPER_SOCKET, default ~/.local/state/hesper/hesperd.sock), so
everything the app shows is reachable here. When hesperd is not running,
agent commands exit 4.

**Agent**: one Claude Code, Codex or shell session in a terminal hesperd
runs, on this Mac or another of the owner's Macs (a *machine*). Agents keep
running when the app closes.

**Agent ids** are machine/local, e.g. ` + "`mini/a7f3k2`" + `: the machine's short name
and a six-character local id. Commands taking ID accept the full id, the
local id alone (` + "`a7f3k2`" + `) or the agent's name when exactly one agent has it
(else exit 2, code ambiguous).

**States** (` + "`state`" + `):

| State | Meaning |
|---|---|
| starting | process starting, no first hook yet |
| working | running a turn or a tool |
| approval | waits for a permission decision: answer with approve or deny |
| question | asks the user something (a choice, trust folder, login); see attention |
| done | finished its turn; ` + "`summary`" + ` holds the last line of its answer |
| idle | waits for a prompt |
| error | failed (StopFailure, or the process ended non-zero); see attention |
| exited | the process ended (0, or stopped); resume restarts it with its conversation |

**Attention** (` + "`attention`" + `, only in approval, question and error):
` + "`{kind, title, detail, options}`" + `: kind is approval, question or error; title
the tool or question ("Bash", "Trust folder"); detail the command, file or
question; options the answers approve/deny accept (allow, always, deny;
trust, exit; skip, update).

**Projects**: an agent works in a project folder (` + "`project`" + `), possibly in a
git worktree of it (` + "`worktree`" + `, ` + "`branch`" + `). ` + "`projectId`" + ` is the project's identity
(its repository, shared between machines); folders outside every project
are "scratch:<folder>". **Scratch projects** (kind scratch) are throwaway
projects in ~/scratch/<date>-<slug> on the Mac that made them (their home),
each a Git repository: ` + "`new --scratch TASK`" + ` starts an agent in a new one;
they are archived after 14 days without agents and deleted 30 days later,
unless kept (see the scratch commands); promote one to keep it as a project.

**Machines**: the owner's Macs, connected through the relay. A machine has
an id, a short name (used in agent ids and --machine) and a full name.
hesperctl machines lists them; agents on other machines are reached
through the local hesperd.

**Inside an agent**: hesperd sets HESPER_AGENT_ID (the agent's full id) and
HESPER_SOCKET in every agent's environment, so an agent can run
` + "`hesperctl self --json`" + ` to find itself and drive other agents with the
commands below.
`

// writeReference writes the Markdown reference.
func writeReference(w io.Writer) error {
	fmt.Fprint(w, "# hesperctl reference\n\nhesperctl is the command line of Hesper. Everything the app does is meant to be doable here, with stable JSON (--json) and exit codes, for scripts and LLM agents.\n\n")
	fmt.Fprint(w, concepts)
	agent, _ := json.MarshalIndent(sampleAgent(), "", "  ")
	fmt.Fprintf(w, "\n**Agent object** (what --json prints for an agent; optional fields are left out when empty, `exit` is null while running or `{code, signal}`):\n\n```json\n%s\n```\n", agent)
	fmt.Fprint(w, "\n## Output, errors and exit codes\n\nWith --json (accepted by every command) a command prints JSON on stdout. Commands that only act print nothing and exit 0. Errors go to stderr, with --json as:\n\n```json\n{\"error\":{\"code\":\"not_found\",\"message\":\"no agent a7f3k2\"}}\n```\n\n`code` is the daemon's or relay's error code (not_found, invalid, exists, unavailable, offline, remote, forbidden, live; move: busy, processes, tool-missing, no-remote, too-large; …) or the CLI's (usage, ambiguous, timeout, error).\n\n| Exit | Name | When |\n|---|---|---|\n")
	for _, e := range exitCodes {
		fmt.Fprintf(w, "| %d | %s | %s |\n", e.Code, e.Name, e.Meaning)
	}
	names, by := groups()
	fmt.Fprint(w, "\n## Commands\n")
	for _, g := range names {
		fmt.Fprintf(w, "\n### %s\n", g)
		for _, c := range by[g] {
			d := describe(c)
			fmt.Fprintf(w, "\n#### %s\n\n%s.\n\n```\n%s\n```\n", d.Name, d.Summary, d.Usage)
			if len(d.Aliases) > 0 {
				fmt.Fprintf(w, "\nAliases: %s\n", strings.Join(d.Aliases, ", "))
			}
			if d.Help != "" {
				fmt.Fprintf(w, "\n%s\n", d.Help)
			}
			if len(d.Flags) > 0 {
				fmt.Fprint(w, "\n| Flag | Default | Meaning |\n|---|---|---|\n")
				for _, fl := range d.Flags {
					def := fl.Default
					if def != "" {
						def = "`" + def + "`"
					}
					fmt.Fprintf(w, "| `%s` | %s | %s |\n", fl, def, strings.ReplaceAll(fl.Usage, "|", `\|`))
				}
			}
			if d.Output != "" {
				fmt.Fprintf(w, "\nOutput (--json): %s\n", d.Output)
			}
			if len(d.Examples) > 0 {
				fmt.Fprintf(w, "\n```sh\n%s\n```\n", strings.Join(d.Examples, "\n"))
			}
		}
	}
	return nil
}

func referenceCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	if err := f.Parse(args); err != nil {
		return err
	}
	if *asJSON {
		return output(map[string]any{"concepts": concepts, "agent": sampleAgent(), "exitCodes": exitCodes, "commands": describeAll()})
	}
	return writeReference(os.Stdout)
}

func init() {
	register(Command{
		Name: "reference", ReadOnly: true, Group: groupHelp,
		Summary: "Print the whole CLI as Markdown, for LLM agents",
		Usage:   "reference [--json]",
		Help:    "Concepts (agents, ids, states, attention, projects, machines), the exit codes and every command with its flags, output and examples. Generated from the commands themselves.",
		Output:  "{concepts, agent, exitCodes:[{code, name, meaning}], commands:[the help --json of each]}",
		Examples: []string{
			"hesperctl reference > hesperctl.md",
		},
		Run: referenceCommand,
	})
}
