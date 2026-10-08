package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The agent tree (docs/rebuild-contract.md, "As built — agent tree"): an
// agent that runs hesperctl new starts a child; hesperd records its
// parent and depth and lets an agent steer only its descendants. hesperctl
// tells hesperd who calls with "caller" (HESPER_AGENT_ID) on every
// agents.* call (dialDaemon); hesperd prefers the agent it finds the
// caller's process in.
//
//	new … --let-parent-answer --wait [--timeout D]
//	ls --tree | ls --children [ID]
//	result ID

func init() {
	if c := findCommand("new"); c != nil {
		c.Usage = "new [flags] TASK… [--let-parent-answer] [--wait [--timeout D]]"
		c.Help += "\n\nInside an agent the new agent is its child (at most 3 deep, at most 8 live children; settings.json maxAgentDepth, maxAgentChildren). --let-parent-answer lets the parent answer the child's approvals and questions (hesperctl approve/deny, or send while it waits); otherwise a person does. --wait waits until the child is done, idle, exited or needs you (approval, question, error) and prints its result as hesperctl result does; with --json {agent: Agent, result: {id, state, message, summary, at}} instead of the Agent. A child in error exits 1, --timeout running out exits 6."
		c.Examples = append(c.Examples, "hesperctl new --let-parent-answer --wait --timeout 30m run the test suite and fix failures")
	}
	if c := findCommand("ls"); c != nil {
		c.Usage = "ls [--tree | --children [ID]] [--json]"
		c.Help += " --tree shows children indented under their parents; --children lists ID's children (default: the agent this runs in)."
		c.Examples = append(c.Examples, "hesperctl ls --tree", "hesperctl ls --children --json | jq -r '.[] | select(.state==\"done\") | .id'")
		c.Run = lsCommand
	}
	register(Command{Name: "result", ReadOnly: true, Group: groupAgents, Summary: "Print an agent's last result (its final message)",
		Usage:    "result ID [--json]",
		Help:     idHelp + " Prints the final message of the agent's last turn (from its Stop or notify hook), else its summary. Nothing yet exits 1.",
		Output:   "{id, state, message, summary, at}",
		Examples: []string{"hesperctl result a7f3k2", "hesperctl result a7f3k2 --json | jq -r .message"},
		Run:      resultCommand})
}

// envAgentID is the agent hesperctl runs in: HESPER_AGENT_ID, completed
// with HESPER_MACHINE when it is a local id; "" outside agents.
func envAgentID() string {
	id := strings.TrimSpace(os.Getenv("HESPER_AGENT_ID"))
	if m := os.Getenv("HESPER_MACHINE"); id != "" && m != "" && !strings.Contains(id, "/") {
		id = m + "/" + id
	}
	return id
}

// newTreeFlags are new's agent tree flags.
type newTreeFlags struct {
	letParentAnswer, wait *bool
	timeout               *time.Duration
}

func addNewTreeFlags(f *flag.FlagSet) newTreeFlags {
	return newTreeFlags{
		letParentAnswer: f.Bool("let-parent-answer", false, "Let the agent that starts this one answer its approvals and questions"),
		wait:            f.Bool("wait", false, "Wait until the agent is done, idle, exited or needs you, then print its result"),
		timeout:         f.Duration("timeout", 0, "With --wait: give up after this long (exit 6); 0 waits forever"),
	}
}

// settled: states in which an agent no longer works on its own.
func settled(state string) bool {
	switch state {
	case wire.StateDone, wire.StateIdle, wire.StateExited, wire.StateError, wire.StateApproval, wire.StateQuestion:
		return true
	}
	return false
}

// waitAgent waits until agent id has settled (timeout 0: no limit).
func waitAgent(ctx context.Context, socket, id string, timeout time.Duration) (wire.Agent, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	c, err := dialDaemon(ctx, socket)
	if err != nil {
		return wire.Agent{}, err
	}
	defer c.Close()
	if err := c.Call(ctx, "agents.subscribe", nil, nil); err != nil {
		return wire.Agent{}, err
	}
	var last wire.Agent
	for {
		select {
		case n, ok := <-c.Notifications():
			if !ok {
				return last, failf(wire.CodeUnavailable, "hesperd closed the connection")
			}
			switch n.Method {
			case wire.NoteChanged:
				var ch wire.Changed
				if json.Unmarshal(n.Params, &ch) != nil || ch.Agent.ID != id {
					continue
				}
				last = ch.Agent
				if settled(last.State) {
					return last, nil
				}
			case wire.NoteRemoved:
				var rm wire.Removed
				if json.Unmarshal(n.Params, &rm) == nil && rm.ID == id {
					return last, failf(wire.CodeNotFound, "%s was closed (%s)", id, rm.Reason)
				}
			}
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return last, failf(codeTimeout, "%s is still %s after %s", id, last.State, timeout)
			}
			return last, ctx.Err()
		}
	}
}

// waitAndPrint is new --wait: wait, then print the result.
func waitAndPrint(ctx context.Context, socket, id string, timeout time.Duration, asJSON bool) error {
	a, err := waitAgent(ctx, socket, id, timeout)
	if err != nil {
		return err
	}
	res, err := fetchResult(ctx, socket, id)
	if err != nil {
		return err
	}
	if asJSON {
		if err := output(struct {
			Agent  wire.Agent       `json:"agent"`
			Result wire.AgentResult `json:"result"`
		}{a, res}); err != nil {
			return err
		}
	} else {
		printResult(os.Stdout, a, res)
	}
	if a.State == wire.StateError {
		detail := ""
		if a.Attention != nil {
			detail = ": " + strings.TrimSpace(a.Attention.Title+": "+a.Attention.Detail)
		}
		return failf(codeError, "%s ended in error%s", id, detail)
	}
	return nil
}

// printResult prints what an agent came to: what it waits for when it
// needs you, else its final message (or summary).
func printResult(w io.Writer, a wire.Agent, res wire.AgentResult) {
	if (a.State == wire.StateApproval || a.State == wire.StateQuestion) && a.Attention != nil {
		fmt.Fprintf(w, "%s needs you (%s): %s\n", a.ID, a.State, strings.TrimSpace(a.Attention.Title+": "+a.Attention.Detail))
		return
	}
	text := res.Message
	if strings.TrimSpace(text) == "" {
		text = res.Summary
	}
	if text != "" {
		fmt.Fprintln(w, strings.TrimRight(text, "\n"))
	}
}

func fetchResult(ctx context.Context, socket, id string) (wire.AgentResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := dialDaemon(ctx, socket)
	if err != nil {
		return wire.AgentResult{}, err
	}
	defer c.Close()
	var res wire.AgentResult
	err = c.Call(ctx, "agents.result", wire.IDParams{ID: id}, &res)
	return res, err
}

func resultCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl result ID")
		}
		id, err := resolveAgent(ctx, c, positional[0])
		if err != nil {
			return err
		}
		var res wire.AgentResult
		if err := c.Call(ctx, "agents.result", wire.IDParams{ID: id}, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		if strings.TrimSpace(res.Message) == "" && strings.TrimSpace(res.Summary) == "" {
			return failf(codeError, "%s has no result yet (%s)", id, res.State)
		}
		printResult(os.Stdout, wire.Agent{ID: res.ID, State: res.State}, res)
		return nil
	})
}

// lsCommand is ls: every agent, as a tree, or one agent's children.
func lsCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	asTree := f.Bool("tree", false, "Show children indented under their parents")
	children := f.Bool("children", false, "List the children of ID (default: the agent this runs in)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 0 && !*children {
			return usagef("usage: hesperctl ls [--tree | --children [ID]]")
		}
		var list []wire.Agent
		if err := c.Call(ctx, "agents.list", nil, &list); err != nil {
			return err
		}
		levels := map[string]int{}
		switch {
		case *children:
			parent := envAgentID()
			if len(positional) > 0 {
				id, err := resolveAgent(ctx, c, positional[0])
				if err != nil {
					return err
				}
				parent = id
			}
			if parent == "" {
				return usagef("not inside a Hesper agent: hesperctl ls --children ID")
			}
			list = childrenOf(list, parent)
		case *asTree:
			list, levels = treeOrder(list)
		}
		if *asJSON {
			if list == nil {
				list = []wire.Agent{}
			}
			return output(list)
		}
		if *asTree && !*children {
			return printAgentTree(os.Stdout, list, levels)
		}
		return printAgents(os.Stdout, list)
	})
}

func childrenOf(list []wire.Agent, parent string) []wire.Agent {
	var out []wire.Agent
	for _, a := range list {
		if a.Parent == parent {
			out = append(out, a)
		}
	}
	return out
}

// treeOrder puts children right after their parents (in list order) and
// tells each agent's level; an agent whose parent is not listed is a root.
func treeOrder(list []wire.Agent) ([]wire.Agent, map[string]int) {
	known := map[string]bool{}
	for _, a := range list {
		known[a.ID] = true
	}
	kids := map[string][]wire.Agent{}
	var roots []wire.Agent
	for _, a := range list {
		if a.Parent != "" && a.Parent != a.ID && known[a.Parent] {
			kids[a.Parent] = append(kids[a.Parent], a)
		} else {
			roots = append(roots, a)
		}
	}
	out := make([]wire.Agent, 0, len(list))
	levels := map[string]int{}
	seen := map[string]bool{}
	var walk func(a wire.Agent, level int)
	walk = func(a wire.Agent, level int) {
		if seen[a.ID] {
			return
		}
		seen[a.ID] = true
		out = append(out, a)
		levels[a.ID] = level
		for _, k := range kids[a.ID] {
			walk(k, level+1)
		}
	}
	for _, a := range roots {
		walk(a, 0)
	}
	// A parent cycle (never made by hesperd) has no root: list it anyway.
	rest := []wire.Agent{}
	for _, a := range list {
		if !seen[a.ID] {
			rest = append(rest, a)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].ID < rest[j].ID })
	for _, a := range rest {
		walk(a, 0)
	}
	return out, levels
}

// printAgentTree is printAgents with children indented under parents.
func printAgentTree(w io.Writer, list []wire.Agent, levels map[string]int) error {
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
		id := a.ID
		if l := levels[a.ID]; l > 0 {
			id = strings.Repeat("  ", l-1) + "└ " + id
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", id, a.State, a.Kind, a.Name, dir, note)
	}
	return tw.Flush()
}
