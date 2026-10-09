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

// Review (review.*: docs/rebuild-contract.md "As built — review"): the
// finished work of every agent on every Mac, its changes, what it ran,
// and what the reviewer does with it.

const reviewIDHelp = idHelp + " Hunk ids (--hunk) are review diff's: \"<file>:<hunk>\" (file and hunk index, as `review diff` shows them) or \"<file>\" for the whole file; they hold for one state of the folder (--tree, from the diff's tree, refuses a folder that changed since) and one --context."

func init() {
	for _, c := range []Command{
		{Name: "review ls", ReadOnly: true, Summary: "List the agents whose finished work is ready for review", Usage: "review ls [--json]",
			Help: "Agents on every Mac that settled (done, idle, exited) with changes in their folder against the commit they started at (untracked files included): " +
				"id, state, files, lines added and removed, risk (low, medium, high: simple rules), evidence (fresh: a test or build passed after the last edit; stale; missing; none) and when it settled, the most recent first.",
			Output:   "[{id, machine, name, kind, project, branch?, worktree?, state, files, added, removed, risk, riskNotes, evidence, readyAt, reviewedAt?, base}]",
			Examples: []string{"hesperctl review ls", "hesperctl review ls --json | jq -r '.[] | select(.risk==\"low\" and .evidence==\"fresh\") | .id'"},
			Run:      reviewList},
		{Name: "review show", ReadOnly: true, Summary: "Show an agent's work: its files in reading order, risk and evidence", Usage: "review show ID [--json]",
			Help:     reviewIDHelp + " The changed files in reading order (risky and depended-on first, tests after their code, formatting-only and generated last) with status, lines, risk and flags, the risk notes and the evidence.",
			Output:   "{item: (as in review ls, null when not ready), files: [{path, oldPath?, status, binary?, formattingOnly?, generated?, tooLarge?, order, risk, added, removed, hunks: n}], evidence: (as in review evidence)}",
			Examples: []string{"hesperctl review show a7f3k2"},
			Run:      reviewShow},
		{Name: "review diff", ReadOnly: true, Summary: "Print an agent's changes in reading order, with hunk ids", Usage: "review diff ID [--context N] [--json]",
			Help:     reviewIDHelp + " Each file's header names its index, status and risk; each hunk its id and flags (formatting-only, moved).",
			Output:   "{base, head: \"worktree\", tree, files: [{path, oldPath?, status, binary?, formattingOnly?, generated?, tooLarge?, order, risk, added, removed, hunks: [{id, oldStart, oldLines, newStart, newLines, formattingOnly?, moved?, lines: [{kind, text, old?, new?, words?: [[start, end]] (UTF-8 bytes), noNewline?}]}]}]}",
			Examples: []string{"hesperctl review diff a7f3k2", "hesperctl review diff a7f3k2 --json | jq -r .tree"},
			Run:      reviewDiff},
		{Name: "review accept", Summary: "Commit an agent's work (all of it, or hunks) in its folder", Usage: "review accept ID [--hunk H]… [--message M] [--tree T] [--context N] [--json]",
			Help: reviewIDHelp + " Without --hunk everything is accepted. The commit goes on the folder's HEAD with your Git configuration; what is not accepted stays as uncommitted changes. " +
				"The message defaults to the agent's summary. Refused while the agent works (exit 7). Prints the commit.",
			Output:   "{commit}",
			Examples: []string{"hesperctl review accept a7f3k2", "hesperctl review accept a7f3k2 --hunk 0:0 --hunk 2 -m 'Add the push provider'"},
			Run:      reviewAccept},
		{Name: "review reject", Destructive: true, Summary: "Revert hunks of an agent's work in its folder", Usage: "review reject ID --hunk H… [--tree T] [--context N]",
			Help:     reviewIDHelp + " The hunks (or whole files) go back to how they were when the agent started, in the working tree. Refused while the agent works (exit 7).",
			Examples: []string{"hesperctl review reject a7f3k2 --hunk 1:0 --hunk 3"},
			Run:      reviewReject},
		{Name: "review send-back", Summary: "Send an agent's work back with review notes", Usage: "review send-back ID [--note NOTE]… [--message M]",
			Help: idHelp + " NOTE is \"PATH[:LINE] TEXT\" (LINE on the new side; :-LINE on the old side). The notes and the message are typed into the agent as one instruction, " +
				"and the folder as it is now is kept as the reviewed point. Refused while the agent works (exit 7).",
			Examples: []string{"hesperctl review send-back a7f3k2 --note 'src/push.go:42 handle the error' --message 'Then run the tests.'"},
			Run:      reviewSendBack},
		{Name: "review evidence", ReadOnly: true, Summary: "Show what an agent ran and whether its tests are fresh", Usage: "review evidence ID [--json]",
			Help: idHelp + " From the agent's hooks: its commands (test, build, lint, run, other) with exit codes and times, and freshness: fresh (a test or build passed after the last edit), " +
				"stale (edits since, or it failed), missing (none ran), none (no changes). Attachments are the changed images, videos and logs in its folder.",
			Output:   "{freshness, lastEditAt?, commands: [{command, kind, exitCode?, startedAt, endedAt?}], attachments: [{path, kind}]}",
			Examples: []string{"hesperctl review evidence a7f3k2"},
			Run:      reviewEvidence},
		{Name: "review provenance", ReadOnly: true, Summary: "Show which turn and tool last wrote a line", Usage: "review provenance ID LOCATION [--json]",
			Help:     idHelp + " LOCATION is PATH[:LINE]: PATH as review diff shows it, LINE on the new side. The session (as history show takes it), the turn (its prompt) and the tool of the last edit of that line, else of the file.",
			Output:   "{sessionId?, turn?, tool?, prompt?, at?} ({} when no edit is known)",
			Examples: []string{"hesperctl review provenance a7f3k2 src/push.go:42"},
			Run:      reviewProvenance},
	} {
		c.Group = groupReview
		register(c)
	}
}

func reviewList(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 0 {
			return usagef("usage: hesperctl review ls")
		}
		var list []wire.ReviewItem
		if err := c.Call(ctx, "review.list", nil, &list); err != nil {
			return err
		}
		if *asJSON {
			if list == nil {
				list = []wire.ReviewItem{}
			}
			return output(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tSTATE\tFILES\tLINES\tRISK\tEVIDENCE\tREADY\tNAME")
		for _, it := range list {
			fmt.Fprintf(tw, "%s\t%s\t%d\t+%d -%d\t%s\t%s\t%s\t%s\n", it.ID, it.State, it.Files, it.Added, it.Removed, it.Risk, it.Evidence, ago(it.ReadyAt), oneLine(it.Name, 40))
		}
		return tw.Flush()
	})
}

// reviewOne resolves the one ID argument.
func reviewOne(ctx context.Context, c *wire.Client, positional []string, usage string) (string, error) {
	if len(positional) != 1 {
		return "", usagef("usage: hesperctl %s", usage)
	}
	return resolveAgent(ctx, c, positional[0])
}

func reviewShow(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		id, err := reviewOne(ctx, c, positional, "review show ID")
		if err != nil {
			return err
		}
		var list []wire.ReviewItem
		if err := c.Call(ctx, "review.list", nil, &list); err != nil {
			return err
		}
		var item *wire.ReviewItem
		for i := range list {
			if list[i].ID == id {
				item = &list[i]
			}
		}
		var d wire.ReviewDiff
		if err := c.Call(ctx, "review.diff", wire.ReviewDiffParams{ID: id}, &d); err != nil {
			return err
		}
		var ev wire.ReviewEvidence
		if err := c.Call(ctx, "review.evidence", wire.IDParams{ID: id}, &ev); err != nil {
			return err
		}
		type fileSummary struct {
			wire.ReviewFile
			Hunks int `json:"hunks"`
		}
		files := make([]fileSummary, len(d.Files))
		for i, file := range d.Files {
			files[i] = fileSummary{ReviewFile: file, Hunks: len(file.Hunks)}
			files[i].ReviewFile.Hunks = nil
		}
		if *asJSON {
			return output(map[string]any{"item": item, "files": files, "evidence": ev})
		}
		if item != nil {
			fmt.Printf("%s  %s  %s  %d files +%d -%d  risk %s  evidence %s\n", item.ID, item.Name, item.State, item.Files, item.Added, item.Removed, item.Risk, item.Evidence)
			for _, note := range item.RiskNotes {
				fmt.Printf("  ! %s\n", note)
			}
		} else {
			fmt.Printf("%s is not ready for review (working, or no changes)\n", id)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "\nFILE\tSTATUS\tLINES\tRISK\tPATH")
		for _, file := range files {
			fmt.Fprintf(tw, "%d\t%s\t+%d -%d\t%s\t%s%s\n", file.Order, file.Status, file.Added, file.Removed, file.Risk, describePath(file.ReviewFile), fileFlags(file.ReviewFile))
		}
		tw.Flush()
		printEvidence(os.Stdout, ev)
		return nil
	})
}

func describePath(f wire.ReviewFile) string {
	if f.OldPath != "" {
		return f.OldPath + " → " + f.Path
	}
	return f.Path
}

func fileFlags(f wire.ReviewFile) string {
	var flags []string
	for _, fl := range []struct {
		on   bool
		name string
	}{{f.Binary, "binary"}, {f.FormattingOnly, "formatting only"}, {f.Generated, "generated"}, {f.TooLarge, "too large"}} {
		if fl.on {
			flags = append(flags, fl.name)
		}
	}
	if len(flags) == 0 {
		return ""
	}
	return "  (" + strings.Join(flags, ", ") + ")"
}

func contextFlag(f *flag.FlagSet) *int {
	return f.Int("context", wire.DefaultReviewContext, "Lines of context around changes")
}

func reviewDiff(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	lines := contextFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		id, err := reviewOne(ctx, c, positional, "review diff ID [--context N]")
		if err != nil {
			return err
		}
		var d wire.ReviewDiff
		if err := c.Call(ctx, "review.diff", wire.ReviewDiffParams{ID: id, Context: lines}, &d); err != nil {
			return err
		}
		if *asJSON {
			return output(d)
		}
		printDiff(os.Stdout, d)
		return nil
	})
}

func printDiff(w io.Writer, d wire.ReviewDiff) {
	fmt.Fprintf(w, "base %.12s  tree %.12s  %d files\n", d.Base, d.Tree, len(d.Files))
	for _, f := range d.Files {
		fmt.Fprintf(w, "\n=== %d %s %s  +%d -%d  risk %s%s\n", f.Order, f.Status, describePath(f), f.Added, f.Removed, f.Risk, fileFlags(f))
		for _, h := range f.Hunks {
			var flags []string
			if h.FormattingOnly {
				flags = append(flags, "formatting only")
			}
			if h.Moved {
				flags = append(flags, "moved")
			}
			extra := ""
			if len(flags) > 0 {
				extra = "  (" + strings.Join(flags, ", ") + ")"
			}
			fmt.Fprintf(w, "@@ %s  -%d,%d +%d,%d @@%s\n", h.ID, h.OldStart, h.OldLines, h.NewStart, h.NewLines, extra)
			for _, l := range h.Lines {
				fmt.Fprintf(w, "%s%s\n", l.Kind, l.Text)
			}
		}
	}
}

func reviewAccept(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	var hunks listFlag
	f.Var(&hunks, "hunk", "Accept only this hunk (\"<file>:<hunk>\") or file (\"<file>\"); repeatable")
	var message string
	f.StringVar(&message, "message", "", "Commit message (default: the agent's summary)")
	f.StringVar(&message, "m", "", "Commit message (same as --message)")
	tree := f.String("tree", "", "Refuse unless the folder is still this tree (review diff's tree)")
	lines := contextFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		id, err := reviewOne(ctx, c, positional, "review accept ID [--hunk H]… [--message M]")
		if err != nil {
			return err
		}
		p := wire.ReviewAcceptParams{ID: id, Message: message, Tree: *tree, Context: lines}
		if len(hunks) > 0 {
			p.Hunks = hunks
		}
		var res wire.ReviewAcceptResult
		if err := c.Call(ctx, "review.accept", p, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		fmt.Println(res.Commit)
		return nil
	})
}

func reviewReject(ctx context.Context, f *flag.FlagSet, args []string) error {
	jsonFlag(f)
	var hunks listFlag
	f.Var(&hunks, "hunk", "Reject this hunk (\"<file>:<hunk>\") or file (\"<file>\"); repeatable, at least one")
	tree := f.String("tree", "", "Refuse unless the folder is still this tree (review diff's tree)")
	lines := contextFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		id, err := reviewOne(ctx, c, positional, "review reject ID --hunk H…")
		if err != nil {
			return err
		}
		if len(hunks) == 0 {
			return usagef("review reject needs --hunk (accept takes everything, reject only what is named)")
		}
		return c.Call(ctx, "review.reject", wire.ReviewRejectParams{ID: id, Hunks: hunks, Tree: *tree, Context: lines}, nil)
	})
}

// parseNote reads "PATH[:LINE] TEXT" (":-LINE": the old side).
func parseNote(s string) (wire.ReviewNote, error) {
	where, text, ok := strings.Cut(strings.TrimSpace(s), " ")
	if !ok || strings.TrimSpace(text) == "" {
		return wire.ReviewNote{}, usagef("a note is \"PATH[:LINE] TEXT\": %q", s)
	}
	n := wire.ReviewNote{Path: where, Text: strings.TrimSpace(text)}
	if i := strings.LastIndexByte(where, ':'); i > 0 {
		if line, err := strconv.Atoi(where[i+1:]); err == nil {
			n.Path, n.Line, n.Side = where[:i], line, "new"
			if line < 0 {
				n.Line, n.Side = -line, "old"
			}
		}
	}
	return n, nil
}

func reviewSendBack(ctx context.Context, f *flag.FlagSet, args []string) error {
	jsonFlag(f)
	var notes listNotes
	f.Var(&notes, "note", "A note \"PATH[:LINE] TEXT\" (:-LINE: the old side); repeatable")
	var message string
	f.StringVar(&message, "message", "", "What to do, after the notes")
	f.StringVar(&message, "m", "", "Same as --message")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		id, err := reviewOne(ctx, c, positional, "review send-back ID [--note NOTE]… [--message M]")
		if err != nil {
			return err
		}
		p := wire.ReviewSendBackParams{ID: id, Message: message, Notes: []wire.ReviewNote{}}
		for _, s := range notes {
			n, err := parseNote(s)
			if err != nil {
				return err
			}
			p.Notes = append(p.Notes, n)
		}
		if len(p.Notes) == 0 && strings.TrimSpace(message) == "" {
			return usagef("review send-back needs --note or --message")
		}
		return c.Call(ctx, "review.sendBack", p, nil)
	})
}

// listNotes is a repeatable flag whose values may hold commas.
type listNotes []string

func (l *listNotes) String() string     { return strings.Join(*l, "; ") }
func (l *listNotes) Set(s string) error { *l = append(*l, s); return nil }

func reviewEvidence(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		id, err := reviewOne(ctx, c, positional, "review evidence ID")
		if err != nil {
			return err
		}
		var ev wire.ReviewEvidence
		if err := c.Call(ctx, "review.evidence", wire.IDParams{ID: id}, &ev); err != nil {
			return err
		}
		if *asJSON {
			return output(ev)
		}
		printEvidence(os.Stdout, ev)
		return nil
	})
}

func printEvidence(w io.Writer, ev wire.ReviewEvidence) {
	fmt.Fprintf(w, "\nEvidence: %s (last edit %s)\n", ev.Freshness, ago(ev.LastEditAt))
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, cmd := range ev.Commands {
		exit := "?"
		if cmd.ExitCode != nil {
			exit = strconv.Itoa(*cmd.ExitCode)
		}
		fmt.Fprintf(tw, "  %s\texit %s\t%s\t%s\n", cmd.Kind, exit, ago(cmd.StartedAt), oneLine(cmd.Command, 80))
	}
	tw.Flush()
	for _, a := range ev.Attachments {
		fmt.Fprintf(w, "  %s: %s\n", a.Kind, a.Path)
	}
}

func reviewProvenance(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 2 {
			return usagef("usage: hesperctl review provenance ID PATH[:LINE]")
		}
		id, err := resolveAgent(ctx, c, positional[0])
		if err != nil {
			return err
		}
		p := wire.ReviewProvenanceParams{ID: id, Path: positional[1]}
		if i := strings.LastIndexByte(p.Path, ':'); i > 0 {
			if line, err := strconv.Atoi(p.Path[i+1:]); err == nil && line >= 0 {
				p.Path, p.Line = p.Path[:i], line
			}
		}
		var res wire.ReviewProvenance
		if err := c.Call(ctx, "review.provenance", p, &res); err != nil {
			return err
		}
		if *asJSON {
			return output(res)
		}
		if res.At.IsZero() {
			fmt.Fprintf(os.Stderr, "no edit of %s known\n", p.Path)
			return nil
		}
		fmt.Printf("%s turn %d (%s, %s)\n", orDash(res.SessionID), res.Turn, res.Tool, ago(res.At))
		if res.Prompt != "" {
			fmt.Printf("  %s\n", oneLine(res.Prompt, 200))
		}
		return nil
	})
}
