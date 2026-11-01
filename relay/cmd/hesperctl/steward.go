package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The Steward (steward.*): the background agent that keeps the loops
// up to date. One configuration for all the owner's Macs; one Mac runs
// it.
//
//	steward status | on | off | level | inbox

const stewardLevelsHelp = "Levels: watch keeps the records (links, endings, loose ends); suggest (the default) also saves drafts for the next steps; act also starts ready drafts and merges or splits loops."

func init() {
	for _, c := range []Command{
		{Name: "steward status", ReadOnly: true, Summary: "Show the Steward: on or off, level, machine, queue and wakes", Usage: "steward status [--json]",
			Help:   "The Steward is off until a person turns it on. Its configuration is shared by every Mac: whether it is on, its level, the Mac that runs it; then its agent there, the events waiting for it and today's wakes. " + stewardLevelsHelp,
			Output: "{enabled, level, node?, machine?, agent?, queued, wakes, lastWake?, session?}", Run: stewardStatus},
		{Name: "steward on", Summary: "Turn the Steward on", Usage: "steward on [--machine M] [--level L] [--json]",
			Help:   "It runs on --machine (default: the Mac set already, else this one), at --level (watch, suggest or act). A person's only. " + stewardLevelsHelp,
			Output: "the status (as in steward status)", Examples: []string{"hesperctl steward on", "hesperctl steward on --machine mini --level watch"},
			Run: stewardSetRun("on")},
		{Name: "steward off", Summary: "Turn the Steward off", Usage: "steward off [--json]", Help: "Its agent stops; the loops stay as they are. A person's only.",
			Output: "the status (as in steward status)", Run: stewardSetRun("off")},
		{Name: "steward level", Summary: "Set what the Steward may do by itself", Usage: "steward level LEVEL [--json]",
			Help:   "LEVEL is watch, suggest or act. " + stewardLevelsHelp + " At every level it never closes a loop, goal or milestone, never accepts a plan or changes its own settings, and never answers or types into an agent. A person's only.",
			Output: "the status (as in steward status)", Examples: []string{"hesperctl steward level act"}, Run: stewardSetRun("level")},
		{Name: "steward inbox", Summary: "Hand over the events waiting for the Steward", Usage: "steward inbox [--json]",
			Help:   "The Steward's own call: the events since its last call (settled turns, closed agents, new loops, reviews, the evening), which it then has. Called by anyone else, they are gone for the Steward.",
			Output: "[{kind: settled|closed|loopCreated|review|evening, at, agent?, loop?, ending?, summary?, reason?}]", Run: stewardInbox},
	} {
		c.Group = groupLoops
		register(c)
	}
}

func stewardStatus(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 0 {
			return usagef("usage: hesperctl steward status")
		}
		var s wire.StewardStatus
		if err := c.Call(ctx, wire.MethodStewardStatus, nil, &s); err != nil {
			return err
		}
		return printSteward(*asJSON, s)
	})
}

func printSteward(asJSON bool, s wire.StewardStatus) error {
	if asJSON {
		return output(s)
	}
	return writeSteward(os.Stdout, s)
}

func writeSteward(w io.Writer, s wire.StewardStatus) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	on := "off"
	if s.Enabled {
		on = "on"
	}
	fmt.Fprintf(tw, "steward\t%s\n", on)
	fmt.Fprintf(tw, "level\t%s\n", orDash(s.Level))
	fmt.Fprintf(tw, "machine\t%s\n", orDash(s.Machine))
	fmt.Fprintf(tw, "agent\t%s\n", orDash(s.Agent))
	fmt.Fprintf(tw, "queued\t%d\n", s.Queued)
	wakes := fmt.Sprintf("%d", s.Wakes)
	if !s.LastWake.IsZero() {
		wakes += " (last " + ago(s.LastWake) + ")"
	}
	fmt.Fprintf(tw, "wakes today\t%s\n", wakes)
	return tw.Flush()
}

var stewardLevels = []string{wire.StewardWatch, wire.StewardSuggest, wire.StewardAct}

// stewardSetRun is steward on, off and level: one steward.set.
func stewardSetRun(command string) func(context.Context, *flag.FlagSet, []string) error {
	return func(ctx context.Context, f *flag.FlagSet, args []string) error {
		asJSON := jsonFlag(f)
		var machine, level *string
		if command == "on" {
			machine = f.String("machine", "", "The Mac that runs it (short name)")
			level = f.String("level", "", "watch, suggest or act")
		}
		return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
			var p wire.StewardSetParams
			switch command {
			case "on":
				if len(positional) > 0 {
					return usagef("usage: hesperctl steward on [--machine M] [--level L]")
				}
				on := true
				p.Enabled = &on
				if *level != "" {
					if !contains(stewardLevels, *level) {
						return usagef("--level %q: %s", *level, strings.Join(stewardLevels, ", "))
					}
					p.Level = level
				}
				if *machine != "" {
					p.Machine = machine
				} else {
					var s wire.StewardStatus
					if err := c.Call(ctx, wire.MethodStewardStatus, nil, &s); err != nil {
						return err
					}
					if s.Node == "" {
						// No Mac runs it yet: this one.
						if here := localMachine(ctx, c); here != "" {
							p.Machine = &here
						}
					}
				}
			case "off":
				if len(positional) > 0 {
					return usagef("usage: hesperctl steward off")
				}
				off := false
				p.Enabled = &off
			case "level":
				if len(positional) != 1 || !contains(stewardLevels, positional[0]) {
					return usagef("usage: hesperctl steward level %s", strings.Join(stewardLevels, "|"))
				}
				p.Level = &positional[0]
			}
			var s wire.StewardStatus
			if err := c.Call(ctx, wire.MethodStewardSet, p, &s); err != nil {
				return err
			}
			return printSteward(*asJSON, s)
		})
	}
}

func stewardInbox(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 0 {
			return usagef("usage: hesperctl steward inbox")
		}
		list := []wire.StewardEvent{}
		if err := c.Call(ctx, wire.MethodStewardInbox, nil, &list); err != nil {
			return err
		}
		if *asJSON {
			if list == nil {
				list = []wire.StewardEvent{}
			}
			return output(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tAT\tAGENT\tLOOP\tDETAIL")
		for _, e := range list {
			detail := e.Reason
			if e.Ending != "" {
				detail = strings.TrimSpace(e.Ending + " " + e.Summary)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Kind, ago(e.At), orDash(e.Agent), orDash(e.Loop), oneLine(detail, 60))
		}
		return tw.Flush()
	})
}
