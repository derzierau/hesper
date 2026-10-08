package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/derzierau/hesper/relay/internal/attachtty"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Agent commands: hesperctl on hesperd's socket.
//
//	ls | new | send | approve | deny | stop | resume | attach | mv | rm | rename | self
//
// approve was a relay command before (approving a device); with its
// device flags (--rights, --deny, …), or as approve-device, it still is.
// Agents of other machines are reached through hesperd (part R).

const idHelp = "ID is the agent's full id (mini/a7f3k2), its local id (a7f3k2) or its name when unique."

func init() {
	for _, c := range []Command{
		{Name: "ls", ReadOnly: true, Summary: "List the agents of every machine", Usage: "ls [--json]",
			Help:     "A table of id, state, kind, name, folder and what the agent waits for (attention) or last said (summary).",
			Output:   "[Agent]",
			Examples: []string{"hesperctl ls", "hesperctl ls --json | jq -r '.[] | select(.state==\"approval\") | .id'"}},
		{Name: "new", Summary: "Start an agent with a task", Usage: "new [flags] TASK…",
			Help: "Starts a Claude, Codex or shell agent in a project folder and prints its id. TASK is its first prompt; - reads it from stdin. " +
				"--scratch starts it in a new scratch project named from the task (~/scratch/<date>-<slug>, a Git repository; see scratch) instead of a folder. " +
				"--bring (with --machine) brings --project, a folder on this Mac, to MACHINE first: a Git repository with its branch and " +
				"uncommitted (and untracked, not ignored) files (--clean: the last commit only), cloned there from its remote when it has one " +
				"(else carried whole); another folder copied without node_modules, .build, DerivedData, target, dist, .venv, __pycache__ " +
				"(at most 5 GB, settings.json maxTransferMB). It lands at the same place under MACHINE's home (a repository with a remote outside ~/projects and ~/scratch: " +
				"in its projects folder). Refused, nothing written: exists (MACHINE has that folder already: start there without --bring), " +
				"too-large, tool-missing, offline. Progress: agents.bringing events (see events).",
			Output: "Agent",
			Examples: []string{"hesperctl new --project ~/src/app fix the flaky login test", "hesperctl new --kind codex --branch fix-login fix the login", "echo 'review the diff' | hesperctl new --json -", "hesperctl new --scratch try the csv parser on the export",
				"hesperctl new --machine mini --bring --project ~/projects/app fix the flaky login test"}},
		{Name: "send", Summary: "Type text into an agent and press Enter", Usage: "send ID TEXT… [--no-submit]",
			Help:     idHelp + " TEXT - reads stdin. The text is pasted, then submitted unless --no-submit.",
			Examples: []string{"hesperctl send a7f3k2 now run the tests", "git diff | hesperctl send mini/a7f3k2 -"}},
		{Name: "approve", Summary: "Allow what an agent waits for (approval)", Usage: "approve ID [--always]",
			Help:     idHelp + " Answers an agent in state approval with allow (always: and don't ask again). Without an agent of that id, or with device flags, approves a device instead (see approve-device).",
			Examples: []string{"hesperctl approve a7f3k2", "hesperctl approve push-provider --always"}},
		{Name: "deny", Summary: "Deny what an agent waits for (approval)", Usage: "deny ID [--message TEXT]",
			Help:     idHelp + " Without --message the agent goes idle; with it, the message tells the agent what to do instead.",
			Examples: []string{"hesperctl deny a7f3k2 --message 'push to a branch, not main'"}},
		{Name: "stop", Destructive: true, Summary: "Stop an agent (it stays, exited, and can be resumed)", Usage: "stop ID",
			Help: idHelp + " Sends SIGHUP, then SIGKILL after 5 seconds."},
		{Name: "resume", Summary: "Restart an exited agent with its conversation", Usage: "resume ID [--json]",
			Help: idHelp, Output: "Agent"},
		{Name: "attach", NoMCP: true, Summary: "Attach this terminal to an agent", Usage: "attach ID [--ro] [--owner=false]",
			Help: idHelp + " Ctrl-] detaches. Needs a terminal; not for scripts."},
		{Name: "move", Aliases: []string{"mv"}, Summary: "Move an agent with its conversation and uncommitted work to another machine",
			Usage: "move ID --to MACHINE [--fork] [--interrupt] [--leave-processes] [--json]",
			Help: idHelp + " MACHINE is a short name (see machines); `move ID MACHINE` works too. The agent's folder is checkpointed, " +
				"its branch and uncommitted (and untracked, not ignored) files are carried to a worktree on MACHINE (the project is cloned from its " +
				"Git remote when it is not there), its conversation resumes there with a short handover note, and the agent here is closed " +
				"(--fork keeps it). Refused, the agent untouched: busy (it is working: --interrupt), processes (it started dev servers or the like " +
				"that would stay behind: listed; --leave-processes), tool-missing, no-remote, too-large (over 5 GB, settings.json maxTransferMB), offline. " +
				"Undo: move the new agent back. Prints the new agent's id.",
			Output:   "MoveResult = Agent (the new one) plus agent: its id",
			Examples: []string{"hesperctl move a7f3k2 --to mini", "hesperctl move push-provider --to mini --fork --json"}},
		{Name: "checkpoint", Summary: "Checkpoint an agent's Git folder now", Usage: "checkpoint ID [--json]",
			Help: idHelp + " A commit of HEAD, the index and every file (untracked included, ignored ones not) at refs/hesper/checkpoints/<id> " +
				"in its repository, on no branch; the index, branches, stash and files stay as they are. Taken on its own when a turn ends (at most " +
				"every 60 s), at close and move; kept 14 days. Prints the commit and the number of changed files (nothing for a folder outside Git).",
			Output: "{checkpoint: {ref, commit, at, changed, branch?} | null}"},
		{Name: "rm", Destructive: true, Summary: "Forget an exited agent", Usage: "rm ID", Help: idHelp},
		{Name: "rename", Summary: "Rename an agent", Usage: "rename ID NAME… [--json]", Help: idHelp, Output: "Agent"},
	} {
		c.Group, c.Run = groupAgents, agentRun(c.Name)
		if c.Name == "approve" {
			c.Run = approveOrDevice
		}
		register(c)
	}
	register(Command{Name: "self", ReadOnly: true, Group: groupAgents, Summary: "Show the agent this command runs in",
		Usage:    "self [--json]",
		Help:     "Inside a Hesper agent (HESPER_AGENT_ID set, with HESPER_MACHINE when the id has no machine) prints the agent's id. Elsewhere, or when hesperd does not know the agent, exits 3.",
		Output:   "Agent",
		Examples: []string{"me=$(hesperctl self)", "hesperctl self --json | jq -r .project"},
		Run:      selfCommand})
}

// deviceFlags mark the old `approve` (a device waiting for approval).
var deviceFlags = []string{"rights", "name", "state-dir", "allow-software-shell", "deny"}

func hasFlag(args []string, names []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name := strings.TrimLeft(a, "-")
		name, _, _ = strings.Cut(name, "=")
		for _, n := range names {
			if name == n {
				return true
			}
		}
	}
	return false
}

// isAgentCommand decides whether args run an agent command.
func isAgentCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if c := findCommand(args[0]); c == nil || c.Group != groupAgents {
		return false
	}
	if args[0] == "approve" {
		return !hasFlag(args[1:], deviceFlags)
	}
	return true
}

func daemonSocket(f *flag.FlagSet) *string {
	return f.String("daemon-socket", wire.SocketPath(), "hesperd socket ($HESPER_SOCKET)")
}

var errNoDaemon = errors.New("hesperd is not running")

func dialDaemon(ctx context.Context, socket string) (*wire.Client, error) {
	c, err := wire.Dial(ctx, socket)
	if err != nil {
		return nil, fmt.Errorf("%w (%s): %v", errNoDaemon, socket, err)
	}
	c.Caller = callerFor(socket) // agent tree (tree.go)
	return c, nil
}

// resolveAgent finds an agent by full id, local id or unique name.
func resolveAgent(ctx context.Context, c *wire.Client, ref string) (string, error) {
	if strings.Contains(ref, "/") {
		return ref, nil
	}
	var list []wire.Agent
	if err := c.Call(ctx, "agents.list", nil, &list); err != nil {
		return "", err
	}
	var byName []string
	for _, a := range list {
		_, local, _ := strings.Cut(a.ID, "/")
		if local == ref {
			return a.ID, nil
		}
		if a.Name == ref {
			byName = append(byName, a.ID)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return "", &wire.Error{Code: wire.CodeNotFound, Message: "no agent " + ref}
	}
	return "", failf(codeAmbiguous, "%q names %d agents: use an id (%s)", ref, len(byName), strings.Join(byName, ", "))
}

// selfCommand finds the agent it runs in: HESPER_AGENT_ID (a full id, or
// a local id completed with HESPER_MACHINE).
func selfCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, _ []string) error {
		id := os.Getenv("HESPER_AGENT_ID")
		if id == "" {
			return failf(wire.CodeNotFound, "not inside a Hesper agent (HESPER_AGENT_ID is not set)")
		}
		if m := os.Getenv("HESPER_MACHINE"); m != "" && !strings.Contains(id, "/") {
			id = m + "/" + id
		}
		var list []wire.Agent
		if err := c.Call(ctx, "agents.list", nil, &list); err != nil {
			return err
		}
		for _, a := range list {
			_, local, _ := strings.Cut(a.ID, "/")
			if a.ID == id || (!strings.Contains(id, "/") && local == id) {
				if *asJSON {
					return output(a)
				}
				fmt.Println(a.ID)
				return nil
			}
		}
		return failf(wire.CodeNotFound, "hesperd has no agent %s (HESPER_AGENT_ID)", id)
	})
}

// agentRun is the Run of the agent command `command`.
func agentRun(command string) func(context.Context, *flag.FlagSet, []string) error {
	return func(ctx context.Context, f *flag.FlagSet, args []string) error {
		return agentCommand(ctx, f, command, args)
	}
}

func agentCommand(ctx context.Context, f *flag.FlagSet, command string, args []string) error {
	socket := daemonSocket(f)
	asJSON := jsonFlag(f)
	var (
		kind, profile, project, name, branch, machine, worktreePath *string
		worktree, noSubmit, always, ro, owner                       *bool
		message                                                     *string
		tree                                                        newTreeFlags // agent tree (tree.go)
	)
	switch command {
	case "new":
		kind = f.String("kind", "", "claude, codex or shell (default: the project's or settings' default)")
		profile = f.String("profile", "", "Launch profile")
		cwd, _ := os.Getwd()
		project = f.String("project", cwd, "Project directory; relative to the current directory (with --machine of another Mac: a path there, as given)")
		name = f.String("name", "", "Display name (default: from the task)")
		worktree = f.Bool("worktree", false, "Run in a new worktree on a new branch")
		worktreePath = f.String("worktree-path", "", "Run in this worktree (created when missing; relative as --project)")
		branch = f.String("branch", "", "Branch of the worktree (implies --worktree)")
		machine = f.String("machine", "", "Machine (short name; default this Mac)")
		tree = addNewTreeFlags(f)
	case "send":
		noSubmit = f.Bool("no-submit", false, "Type the text without pressing Enter")
	case "approve":
		always = f.Bool("always", false, "Approve and don't ask again")
	case "deny":
		message = f.String("message", "", "Tell the agent what to do instead")
	case "attach":
		ro = f.Bool("ro", false, "Read only")
		owner = f.Bool("owner", true, "Resize the agent to this terminal")
	}
	var to *string
	var fork, interrupt, leave, scratch *bool
	bring, clean := new(bool), new(bool)
	if command == "new" {
		scratch = f.Bool("scratch", false, "Start in a new scratch project named from the task (not --project)")
		bring = f.Bool("bring", false, "Bring --project from this Mac to --machine first (with its uncommitted work)")
		clean = f.Bool("clean", false, "With --bring: the last commit only, without the uncommitted work")
	}
	if command == "move" {
		to = f.String("to", "", "Machine to move to (short name)")
		fork = f.Bool("fork", false, "Keep the agent here too (a copy continues there)")
		interrupt = f.Bool("interrupt", false, "Interrupt a working agent first (else it is refused: busy)")
		leave = f.Bool("leave-processes", false, "Move although processes it started stay behind here")
	}
	positional, err := parseInterspersed(f, args)
	if err != nil {
		return err
	}
	limit := 2 * time.Minute
	if command == "move" || *bring {
		// A move or a bring takes as long as its transfer: hesperd
		// bounds it (2 hours; a minute without progress fails it).
		limit = 2*time.Hour + 2*time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	c, err := dialDaemon(ctx, *socket)
	if err != nil {
		return err
	}
	defer c.Close()
	print := func(a wire.Agent) error {
		if *asJSON {
			return output(a)
		}
		fmt.Println(a.ID)
		return nil
	}
	need := func(n int, use string) error {
		if len(positional) < n {
			return usagef("usage: hesperctl %s", use)
		}
		return nil
	}
	id := func() (string, error) { return resolveAgent(ctx, c, positional[0]) }

	switch command {
	case "ls":
		var list []wire.Agent
		if err := c.Call(ctx, "agents.list", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			return output(list)
		}
		return printAgents(os.Stdout, list)
	case "new":
		task := strings.Join(positional, " ")
		if task == "-" {
			data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			if err != nil {
				return err
			}
			task = string(data)
		}
		local, err := isLocalMachine(ctx, c, *machine)
		if err != nil {
			return err
		}
		if *clean && !*bring {
			return usagef("--clean goes with --bring")
		}
		if *bring && (*scratch || *machine == "" || local) {
			return usagef("--bring brings --project from this Mac to another: --machine MACHINE, no --scratch")
		}
		if *scratch {
			if setFlags(f)["project"] || *worktree || *worktreePath != "" || *branch != "" {
				return usagef("--scratch makes its own folder: no --project, --worktree or --branch")
			}
			*project = ""
		} else if local || *bring {
			// This Mac's folders: relative to here (hesperd wants
			// absolute ones). Another Mac's: as given.
			if *project, err = absPath(*project); err != nil {
				return err
			}
			if *worktreePath != "" && local {
				if *worktreePath, err = absPath(*worktreePath); err != nil {
					return err
				}
			}
		}
		p := wire.SpawnParams{Machine: *machine, Profile: *profile, Kind: *kind, Project: *project, Task: task, Name: *name, Branch: *branch,
			LetParentAnswer: *tree.letParentAnswer, Track: *tree.track, Scratch: *scratch}
		if *bring {
			// bring the folder: from this Mac (the daemon's own).
			p.Bring = &wire.Bring{Path: *project, Changes: wire.BringWith}
			if *clean {
				p.Bring.Changes = wire.BringClean
			}
		}
		switch {
		case *worktreePath != "":
			p.Worktree, _ = json.Marshal(*worktreePath)
		case *worktree:
			p.Worktree = json.RawMessage("true")
		}
		var a wire.Agent
		if err := c.Call(ctx, "agents.spawn", p, &a); err != nil {
			return err
		}
		if *tree.wait {
			if !*asJSON {
				fmt.Println(a.ID) // the id first, then (once settled) the result
			}
			return waitAndPrint(context.WithoutCancel(ctx), *socket, a, *tree.timeout, *asJSON)
		}
		return print(a)
	case "send":
		if err := need(2, "send ID TEXT…"); err != nil {
			return err
		}
		target, err := id()
		if err != nil {
			return err
		}
		text := strings.Join(positional[1:], " ")
		if text == "-" {
			data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			if err != nil {
				return err
			}
			text = strings.TrimRight(string(data), "\n")
		}
		return c.Call(ctx, "agents.input", wire.InputParams{ID: target, Text: text, Paste: true, Submit: !*noSubmit}, nil)
	case "approve", "deny":
		if err := need(1, command+" ID"); err != nil {
			return err
		}
		target, err := id()
		if err != nil {
			return err
		}
		p := wire.AnswerParams{ID: target, Decision: wire.Allow}
		if command == "deny" {
			p.Decision, p.Message = wire.Deny, *message
		} else if *always {
			p.Decision = wire.Always
		}
		return c.Call(ctx, "agents.answer", p, nil)
	case "stop", "rm":
		if err := need(1, command+" ID"); err != nil {
			return err
		}
		target, err := id()
		if err != nil {
			return err
		}
		method := "agents.stop"
		if command == "rm" {
			method = "agents.remove"
		}
		return c.Call(ctx, method, wire.IDParams{ID: target}, nil)
	case "resume":
		if err := need(1, "resume ID"); err != nil {
			return err
		}
		target, err := id()
		if err != nil {
			return err
		}
		var a wire.Agent
		if err := c.Call(ctx, "agents.resume", wire.IDParams{ID: target}, &a); err != nil {
			return err
		}
		return print(a)
	case "rename":
		if err := need(2, "rename ID NAME"); err != nil {
			return err
		}
		target, err := id()
		if err != nil {
			return err
		}
		var a wire.Agent
		if err := c.Call(ctx, "agents.rename", wire.RenameParams{ID: target, Name: strings.Join(positional[1:], " ")}, &a); err != nil {
			return err
		}
		return print(a)
	case "move":
		dest := *to
		if dest == "" && len(positional) >= 2 {
			dest = positional[1] // move ID MACHINE (the former mv)
		}
		if err := need(1, "move ID --to MACHINE"); err != nil {
			return err
		}
		if dest == "" {
			return usagef("usage: hesperctl move ID --to MACHINE")
		}
		target, err := id()
		if err != nil {
			return err
		}
		var res wire.MoveResult
		p := wire.MoveParams{ID: target, To: dest, Fork: *fork, Interrupt: *interrupt, LeaveProcesses: *leave}
		if err := c.Call(ctx, "agents.move", p, &res); err != nil {
			var we *wire.Error
			if errors.As(err, &we) && len(we.Processes) > 0 && !*asJSON {
				for _, pr := range we.Processes {
					fmt.Fprintf(os.Stderr, "  %d  %s\n", pr.PID, pr.Command)
				}
			}
			return err
		}
		if *asJSON {
			return output(res)
		}
		fmt.Println(res.Agent)
		return nil
	case "checkpoint":
		if err := need(1, "checkpoint ID"); err != nil {
			return err
		}
		target, err := id()
		if err != nil {
			return err
		}
		var res wire.CheckpointResult
		if err := c.Call(ctx, "agents.checkpoint", wire.IDParams{ID: target}, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		if res.Checkpoint == nil {
			fmt.Fprintln(os.Stderr, "not a Git folder: no checkpoint")
			return nil
		}
		fmt.Printf("%s %d changed\n", res.Checkpoint.Commit, res.Checkpoint.Changed)
		return nil
	case "attach":
		if err := need(1, "attach ID [--ro] [--owner=false]"); err != nil {
			return err
		}
		target, err := id()
		if err != nil {
			return err
		}
		c.Close()
		cancel()
		fmt.Fprintf(os.Stderr, "Attached to %s (Ctrl-] detaches).\r\n", target)
		res, err := attachtty.Run(context.Background(), attachtty.Options{Socket: *socket, ID: target, RO: *ro, Owner: *owner && !*ro, DetachKey: 0x1d})
		if err != nil {
			return err
		}
		if note := attachtty.Describe(target, res); note != "" {
			fmt.Fprintln(os.Stderr, "\r\n"+note)
		}
		return nil
	}
	return fmt.Errorf("unknown command %q", command)
}

// isLocalMachine: machine is empty or this Mac's short name (hello).
func isLocalMachine(ctx context.Context, c *wire.Client, machine string) (bool, error) {
	if machine == "" {
		return true, nil
	}
	var hello wire.HelloResult
	if err := c.Call(ctx, "hello", wire.HelloParams{Client: "hesperctl", Version: wire.Version}, &hello); err != nil {
		return false, err
	}
	return machine == hello.Machine, nil
}

// absPath is a folder on this Mac made absolute (~ expanded, relative to
// the current directory).
func absPath(p string) (string, error) {
	if p == "" {
		return p, nil
	}
	return filepath.Abs(expandHome(p))
}

func printAgents(w io.Writer, list []wire.Agent) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tKIND\tNAME\tPROJECT\tNOTE")
	home, _ := os.UserHomeDir()
	for _, a := range list {
		note := a.Summary
		if a.Attention != nil {
			note = strings.TrimSpace(a.Attention.Title + ": " + a.Attention.Detail)
		}
		dir := a.Dir()
		if home != "" && strings.HasPrefix(dir, home+"/") {
			dir = "~" + dir[len(home):]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", a.ID, a.State, a.Kind, a.Name, dir, note)
	}
	return tw.Flush()
}

// approveOrDevice runs `hesperctl approve X`: an agent's approval, else
// (no such agent) a device waiting for approval, as before.
func approveOrDevice(ctx context.Context, f *flag.FlagSet, args []string) error {
	if hasFlag(args, deviceFlags) {
		return approveCommand(f, args)
	}
	err := agentCommand(ctx, f, "approve", args)
	var we *wire.Error
	notAgent := (errors.As(err, &we) && we.Code == wire.CodeNotFound) || errors.Is(err, errNoDaemon)
	if len(args) > 0 && notAgent && !strings.Contains(args[len(args)-1], "/") {
		var rest []string
		for i := 0; i < len(args); i++ {
			switch a := args[i]; {
			case a == "--daemon-socket" || a == "-daemon-socket":
				i++
			case strings.HasPrefix(strings.TrimLeft(a, "-"), "daemon-socket="):
			default:
				rest = append(rest, a)
			}
		}
		device := newFlagSet("approve")
		device.Usage = f.Usage
		return approveCommand(device, rest)
	}
	return err
}
