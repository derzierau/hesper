package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
		{Name: "ls", Summary: "List the agents of every machine", Usage: "ls [--json]",
			Help:     "A table of id, state, kind, name, folder and what the agent waits for (attention) or last said (summary).",
			Output:   "[Agent]",
			Examples: []string{"hesperctl ls", "hesperctl ls --json | jq -r '.[] | select(.state==\"approval\") | .id'"}},
		{Name: "new", Summary: "Start an agent with a task", Usage: "new [flags] TASK…",
			Help:     "Starts a Claude, Codex or shell agent in a project folder and prints its id. TASK is its first prompt; - reads it from stdin.",
			Output:   "Agent",
			Examples: []string{"hesperctl new --project ~/src/app fix the flaky login test", "hesperctl new --kind codex --branch fix-login fix the login", "echo 'review the diff' | hesperctl new --json -"}},
		{Name: "send", Summary: "Type text into an agent and press Enter", Usage: "send ID TEXT… [--no-submit]",
			Help:     idHelp + " TEXT - reads stdin. The text is pasted, then submitted unless --no-submit.",
			Examples: []string{"hesperctl send a7f3k2 now run the tests", "git diff | hesperctl send mini/a7f3k2 -"}},
		{Name: "approve", Summary: "Allow what an agent waits for (approval)", Usage: "approve ID [--always]",
			Help:     idHelp + " Answers an agent in state approval with allow (always: and don't ask again). Without an agent of that id, or with device flags, approves a device instead (see approve-device).",
			Examples: []string{"hesperctl approve a7f3k2", "hesperctl approve push-provider --always"}},
		{Name: "deny", Summary: "Deny what an agent waits for (approval)", Usage: "deny ID [--message TEXT]",
			Help:     idHelp + " Without --message the agent goes idle; with it, the message tells the agent what to do instead.",
			Examples: []string{"hesperctl deny a7f3k2 --message 'push to a branch, not main'"}},
		{Name: "stop", Summary: "Stop an agent (it stays, exited, and can be resumed)", Usage: "stop ID",
			Help: idHelp + " Sends SIGHUP, then SIGKILL after 5 seconds."},
		{Name: "resume", Summary: "Restart an exited agent with its conversation", Usage: "resume ID [--json]",
			Help: idHelp, Output: "Agent"},
		{Name: "attach", Summary: "Attach this terminal to an agent", Usage: "attach ID [--ro] [--owner=false]",
			Help: idHelp + " Ctrl-] detaches. Needs a terminal; not for scripts."},
		{Name: "mv", Summary: "Move an agent with its conversation to another machine", Usage: "mv ID MACHINE [--json]",
			Help: idHelp + " MACHINE is a short name (see machines).", Output: "Agent",
			Examples: []string{"hesperctl mv a7f3k2 mini"}},
		{Name: "rm", Summary: "Forget an exited agent", Usage: "rm ID", Help: idHelp},
		{Name: "rename", Summary: "Rename an agent", Usage: "rename ID NAME… [--json]", Help: idHelp, Output: "Agent"},
	} {
		c.Group, c.Run = groupAgents, agentRun(c.Name)
		if c.Name == "approve" {
			c.Run = approveOrDevice
		}
		register(c)
	}
	register(Command{Name: "self", Group: groupAgents, Summary: "Show the agent this command runs in",
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
	)
	switch command {
	case "new":
		kind = f.String("kind", "", "claude, codex or shell (default: the project's or settings' default)")
		profile = f.String("profile", "", "Launch profile")
		cwd, _ := os.Getwd()
		project = f.String("project", cwd, "Project directory")
		name = f.String("name", "", "Display name (default: from the task)")
		worktree = f.Bool("worktree", false, "Run in a new worktree on a new branch")
		worktreePath = f.String("worktree-path", "", "Run in this worktree (created when missing)")
		branch = f.String("branch", "", "Branch of the worktree (implies --worktree)")
		machine = f.String("machine", "", "Machine (short name; default this Mac)")
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
	positional, err := parseInterspersed(f, args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
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
		p := wire.SpawnParams{Machine: *machine, Profile: *profile, Kind: *kind, Project: *project, Task: task, Name: *name, Branch: *branch}
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
	case "mv":
		if err := need(2, "mv ID MACHINE"); err != nil {
			return err
		}
		target, err := id()
		if err != nil {
			return err
		}
		var a wire.Agent
		if err := c.Call(ctx, "agents.move", wire.MoveParams{ID: target, To: positional[1]}, &a); err != nil {
			return err
		}
		return print(a)
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
