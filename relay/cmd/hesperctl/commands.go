package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The command registry. Every command is one Command, registered from the
// init of the file that implements it:
//
//	func init() {
//		register(Command{Name: "self", Group: groupAgents, Summary: "…", Usage: "self", Run: selfCommand})
//	}
//
// run dispatches on it; the overview, `help CMD` and `reference` are
// generated from it, flags included (help runs the command with -h).
//
// Subcommands are commands whose name has two words ("history search"):
// `hesperctl history search …` runs it; `hesperctl history` and `help
// history` list the parent's subcommands.

// Command is one hesperctl command.
type Command struct {
	Name    string
	Aliases []string
	Group   string
	// Summary is one line without a period.
	Summary string
	// Usage follows "hesperctl ": "send ID TEXT… [--no-submit]".
	Usage string
	// Help is plain text, paragraphs separated by blank lines.
	Help string
	// Output is what the command prints on stdout with --json.
	Output   string
	Examples []string
	// Run runs the command with the arguments after its name. It defines
	// its flags on f (which has --json already; read it with jsonFlag)
	// and parses them before it does anything else: help calls it with
	// -h and a cancelled context to list them.
	Run func(ctx context.Context, f *flag.FlagSet, args []string) error
}

// Groups, in the order the overview shows them; groups not listed here
// come before Help.
const (
	groupAgents   = "Agents"
	groupProjects = "Projects"
	groupHistory  = "History"
	groupRelay    = "Relay and devices"
	groupHelp     = "Help"
)

var groupOrder = []string{groupAgents, groupProjects, groupHistory, groupRelay}

var commands []*Command

// register adds a command; a name or alias taken twice panics.
func register(c Command) {
	for _, name := range append([]string{c.Name}, c.Aliases...) {
		if findCommand(name) != nil {
			panic("hesperctl: command " + name + " registered twice")
		}
	}
	commands = append(commands, &c)
}

// lookup finds the command args start with, a subcommand ("history
// search") before a command; n is how many args name it.
func lookup(args []string) (c *Command, n int) {
	if len(args) >= 2 && !strings.HasPrefix(args[1], "-") {
		if c := findCommand(args[0] + " " + args[1]); c != nil {
			return c, 2
		}
	}
	if len(args) >= 1 {
		if c := findCommand(args[0]); c != nil {
			return c, 1
		}
	}
	return nil, 0
}

// subcommands are the commands named "parent …".
func subcommands(parent string) []*Command {
	var list []*Command
	for _, c := range commands {
		if strings.HasPrefix(c.Name, parent+" ") {
			list = append(list, c)
		}
	}
	return list
}

// printSubcommands is `hesperctl history` (and `help history`): the
// parent's subcommands.
func printSubcommands(w io.Writer, parent string, subs []*Command, asJSON bool) error {
	if asJSON {
		var list []commandDoc
		for _, c := range subs {
			list = append(list, describe(c))
		}
		return output(list)
	}
	fmt.Fprintf(w, "hesperctl %s: its commands\n\n", parent)
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, c := range subs {
		fmt.Fprintf(tw, "  %s\t%s\n", c.Name, c.Summary)
	}
	tw.Flush()
	fmt.Fprintf(w, "\nhesperctl help %s SUBCOMMAND shows one's flags and examples.\n", parent)
	return nil
}

// unknownCommand is the usage error for args that name no command.
func unknownCommand(args []string) error {
	if len(args) >= 2 && len(subcommands(args[0])) > 0 {
		return usagef("unknown command %q (hesperctl help %s lists them)", args[0]+" "+args[1], args[0])
	}
	return usagef("unknown command %q (hesperctl help lists them)", args[0])
}

func findCommand(name string) *Command {
	for _, c := range commands {
		if c.Name == name {
			return c
		}
		for _, a := range c.Aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

// groups returns the commands by group, groups in overview order.
func groups() ([]string, map[string][]*Command) {
	by := map[string][]*Command{}
	for _, c := range commands {
		by[c.Group] = append(by[c.Group], c)
	}
	var names, extra []string
	for _, g := range groupOrder {
		if len(by[g]) > 0 {
			names = append(names, g)
		}
	}
	for g := range by {
		if g != groupHelp && !contains(groupOrder, g) {
			extra = append(extra, g)
		}
	}
	sort.Strings(extra)
	names = append(names, extra...)
	if len(by[groupHelp]) > 0 {
		names = append(names, groupHelp)
	}
	return names, by
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// boolFlag is the global --json flag's value.
type boolFlag bool

func (b *boolFlag) String() string   { return strconv.FormatBool(bool(*b)) }
func (b *boolFlag) IsBoolFlag() bool { return true }
func (b *boolFlag) Set(s string) error {
	v, err := strconv.ParseBool(s)
	*b = boolFlag(v)
	return err
}

const jsonUsage = "Print JSON; errors go to stderr as {\"error\":{\"code\",\"message\"}}"

// newFlagSet is a command's flag set: it prints nothing (run reports
// errors) and has the global --json.
func newFlagSet(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.Usage = func() {}
	f.Var(new(boolFlag), "json", jsonUsage)
	return f
}

// jsonFlag is f's --json.
func jsonFlag(f *flag.FlagSet) *bool {
	if fl := f.Lookup("json"); fl != nil {
		if b, ok := fl.Value.(*boolFlag); ok {
			return (*bool)(b)
		}
	}
	return f.Bool("json", false, jsonUsage)
}

// wantsJSON tells whether args ask for JSON (before parsing, for errors).
func wantsJSON(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "json" {
			continue
		}
		if !hasValue {
			return true
		}
		v, _ := strconv.ParseBool(value)
		return v
	}
	return false
}

// execute runs a command line and returns the exit code; errors go to
// stderr (as JSON with --json).
func execute(ctx context.Context, args []string, stderr io.Writer) int {
	err := run(ctx, args)
	if err != nil {
		printError(stderr, err, wantsJSON(args))
	}
	return exitCode(err)
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		return printOverview(os.Stdout)
	}
	c, n := lookup(args)
	if c == nil {
		if subs := subcommands(args[0]); len(subs) > 0 && (len(args) == 1 || strings.HasPrefix(args[1], "-")) {
			return printSubcommands(os.Stdout, args[0], subs, wantsJSON(args[1:]))
		}
		return unknownCommand(args)
	}
	if hasFlag(args[n:], []string{"h", "help"}) {
		if wantsJSON(args[n:]) {
			return output(describe(c))
		}
		return printHelp(os.Stdout, c)
	}
	f := newFlagSet(c.Name)
	badFlags := false
	f.Usage = func() { badFlags = true }
	err := c.Run(ctx, f, args[n:])
	if err != nil && badFlags {
		return usagef("%v\nusage: hesperctl %s", err, c.Usage)
	}
	return err
}

func isHelpFlag(a string) bool { return a == "-h" || a == "--help" || a == "-help" }

// withDaemon is the usual body of a command on hesperd: it adds
// --daemon-socket, parses f (flags may follow arguments), dials hesperd
// and calls do with the positional arguments.
func withDaemon(ctx context.Context, f *flag.FlagSet, args []string, do func(ctx context.Context, c *wire.Client, positional []string) error) error {
	socket := daemonSocket(f)
	positional, err := parseInterspersed(f, args)
	if err != nil {
		return err
	}
	c, err := dialDaemon(ctx, *socket)
	if err != nil {
		return err
	}
	defer c.Close()
	return do(ctx, c, positional)
}

// flagDoc is one flag of a command, for help and reference.
type flagDoc struct {
	Name    string `json:"name"`
	Type    string `json:"type,omitempty"`
	Default string `json:"default,omitempty"`
	Usage   string `json:"usage"`
}

// commandFlags runs c with -h to collect its flags (--json is global and
// left out).
func commandFlags(c *Command) []flagDoc {
	f := newFlagSet(c.Name)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Run(ctx, f, []string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		// A command that does not parse first lists no flags (a test
		// catches it).
		return nil
	}
	var list []flagDoc
	f.VisitAll(func(fl *flag.Flag) {
		if fl.Name == "json" {
			return
		}
		typ, usage := flag.UnquoteUsage(fl)
		d := flagDoc{Name: fl.Name, Type: typ, Usage: usage}
		if fl.DefValue != "" && fl.DefValue != "false" && fl.DefValue != "0" && fl.DefValue != "0s" {
			d.Default = portableDefault(fl.DefValue)
		}
		list = append(list, d)
	})
	return list
}

// portableDefault shows a default that depends on where hesperctl runs
// the same everywhere: the working directory as such, home as ~.
func portableDefault(value string) string {
	if cwd, _ := os.Getwd(); cwd != "" && value == cwd {
		return "the current directory"
	}
	if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(value, home+"/") {
		return "~" + value[len(home):]
	}
	return value
}

func (d flagDoc) String() string {
	s := "--" + d.Name
	if d.Type != "" {
		s += " " + d.Type
	}
	return s
}

const helpFooter = `Global flags: --json (JSON output; errors on stderr as {"error":{"code","message"}}), -h/--help.
Exit codes: 0 ok, 1 error, 2 usage, 3 not found, 4 hesperd or machine unavailable, 5 forbidden, 6 timeout, 7 exists.`

// printOverview lists the commands by group.
func printOverview(w io.Writer) error {
	fmt.Fprint(w, "hesperctl drives Hesper: agents on hesperd, machines and devices on the relay.\n\nUsage: hesperctl COMMAND [flags] [arguments]\n")
	names, by := groups()
	for _, g := range names {
		fmt.Fprintf(w, "\n%s:\n", g)
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		for _, c := range by[g] {
			fmt.Fprintf(tw, "  %s\t%s\n", c.Name, c.Summary)
		}
		tw.Flush()
	}
	fmt.Fprintf(w, "\n%s\n\nhesperctl help COMMAND shows a command's flags and examples; hesperctl reference prints everything as Markdown.\n", helpFooter)
	return nil
}

// printHelp is `hesperctl help CMD`.
func printHelp(w io.Writer, c *Command) error {
	fmt.Fprintf(w, "hesperctl %s: %s\n\nUsage: hesperctl %s\n", c.Name, c.Summary, c.Usage)
	if len(c.Aliases) > 0 {
		fmt.Fprintf(w, "Aliases: %s\n", strings.Join(c.Aliases, ", "))
	}
	if c.Help != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(c.Help))
	}
	if flags := commandFlags(c); len(flags) > 0 {
		fmt.Fprintln(w, "\nFlags:")
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		for _, d := range flags {
			usage := d.Usage
			if d.Default != "" {
				usage += " (default " + d.Default + ")"
			}
			fmt.Fprintf(tw, "  %s\t%s\n", d, usage)
		}
		tw.Flush()
	}
	if c.Output != "" {
		fmt.Fprintf(w, "\nWith --json: %s\n", c.Output)
	}
	if len(c.Examples) > 0 {
		fmt.Fprintln(w, "\nExamples:")
		for _, e := range c.Examples {
			fmt.Fprintf(w, "  %s\n", e)
		}
	}
	fmt.Fprintf(w, "\n%s\n", helpFooter)
	return nil
}

func helpCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	positional, err := parseInterspersed(f, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		if *asJSON {
			return output(describeAll())
		}
		return printOverview(os.Stdout)
	}
	c, n := lookup(positional)
	if c == nil {
		if subs := subcommands(positional[0]); len(subs) > 0 && len(positional) == 1 {
			return printSubcommands(os.Stdout, positional[0], subs, *asJSON)
		}
		return unknownCommand(positional)
	}
	if n < len(positional) {
		return usagef("unknown command %q (hesperctl help lists them)", strings.Join(positional, " "))
	}
	if *asJSON {
		return output(describe(c))
	}
	return printHelp(os.Stdout, c)
}

func init() {
	register(Command{
		Name: "help", Group: groupHelp,
		Summary: "Show the commands, or one command's flags and examples",
		Usage:   "help [COMMAND] [--json]",
		Help:    "Without a command: the commands by group. hesperctl COMMAND --help shows the same as hesperctl help COMMAND.",
		Output:  "the command's description: {name, aliases, group, summary, usage, help, flags:[{name, type, default, usage}], output, examples}",
		Examples: []string{
			"hesperctl help send",
			"hesperctl help new --json",
		},
		Run: helpCommand,
	})
}
