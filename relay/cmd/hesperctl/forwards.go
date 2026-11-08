package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Port forwards (forwards.*): a port of this Mac's loopback reaching a
// port on another machine's.

func init() {
	for _, c := range []Command{
		{Name: "forwards ls", ReadOnly: true, Summary: "List the port forwards to other machines", Usage: "forwards ls [--json]",
			Help:   "A table of local port, machine, remote port, open connections and why the last connection was refused (if it was).",
			Output: "[{id (the local port), machine, port, local, conns, error?}]",
			Run:    forwardsList},
		{Name: "forwards add", Summary: "Forward a local port to a port on another machine", Usage: "forwards add MACHINE:PORT [--local L] [--json]",
			Help: "Listens on localhost:L of this Mac (default PORT, or a free port when PORT is taken here) and carries each connection to localhost:PORT on MACHINE, " +
				"through the link to its hesperd (end-to-end sealed, on the direct path when there is one). The forward is kept across restarts. " +
				"MACHINE must run hesperd serve --allow-forward with PORT in its list and have approved this Mac with the shell right. " +
				"Only a person may add a forward, not an agent. Prints the local address.",
			Output:   "{id, machine, port, local, conns}",
			Examples: []string{"hesperctl forwards add dev:3000", "hesperctl forwards add dev:5432 --local 15432"},
			Run:      forwardsAdd},
		{Name: "forwards rm", Destructive: true, Summary: "Remove a port forward and close its connections", Usage: "forwards rm LOCAL|MACHINE:PORT",
			Help: "LOCAL is the forward's local port (its id); MACHINE:PORT works when only one forward goes there. Only a person may remove a forward.",
			Run:  forwardsRemove},
	} {
		c.Group = groupRelay
		register(c)
	}
}

func forwardsList(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, _ []string) error {
		var list []wire.Forward
		if err := c.Call(ctx, "forwards.list", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			if list == nil {
				list = []wire.Forward{}
			}
			return output(list)
		}
		return printForwards(os.Stdout, list)
	})
}

func printForwards(w io.Writer, list []wire.Forward) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "LOCAL\tMACHINE\tPORT\tCONNS\tERROR")
	for _, fw := range list {
		fmt.Fprintf(tw, "%d\t%s\t%d\t%d\t%s\n", fw.Local, fw.Machine, fw.Port, fw.Conns, orDash(oneLine(fw.Error, 80)))
	}
	return tw.Flush()
}

func forwardsAdd(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	local := f.Int("local", 0, "Local port (default the remote port, or a free one when that is taken)")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl forwards add MACHINE:PORT [--local L]")
		}
		machine, port, err := machinePort(positional[0])
		if err != nil {
			return err
		}
		if *local < 0 || *local > 65535 {
			return usagef("--local: a port is 1-65535")
		}
		var fw wire.Forward
		if err := c.Call(ctx, "forwards.add", wire.ForwardAddParams{Machine: machine, Port: port, Local: *local}, &fw); err != nil {
			return err
		}
		if *asJSON {
			return output(fw)
		}
		fmt.Printf("localhost:%d → %s:%d\n", fw.Local, fw.Machine, fw.Port)
		return nil
	})
}

// machinePort reads MACHINE:PORT.
func machinePort(s string) (string, int, error) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 {
		return "", 0, usagef("%q: want MACHINE:PORT, e.g. dev:3000", s)
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, usagef("%q: the port is 1-65535", s)
	}
	return s[:i], port, nil
}

func forwardsRemove(ctx context.Context, f *flag.FlagSet, args []string) error {
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl forwards rm LOCAL|MACHINE:PORT")
		}
		return c.Call(ctx, "forwards.remove", wire.IDParams{ID: positional[0]}, nil)
	})
}
