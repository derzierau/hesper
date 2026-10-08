package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The shared history (sessions.*): every Claude and Codex conversation of
// every Mac, searchable, resumable, forkable.
//
//	history search | show | brief | stats | archive | delete | undelete |
//	        resume | fork | continue-as

const sessionRefHelp = "ID is a session's id as history search prints it: machine:kind:sessionId (e.g. mini:claude:5b0c1d2e-…)."

const sessionOutput = "Session = {id, kind, sessionId, machine (its home), cwd, projectId, branch, title, firstPrompt, lastUser, lastAssistant, todos:[{text, done}], turns, tokens?, startedAt, lastActivity, live?:{agentId?, external}, external, archived, mirrored:[machine], snippet?, origin?, removedAt?, movedTo?, bytes?}"

func init() {
	for _, c := range []Command{
		{Name: "history search", Summary: "Search the Claude and Codex sessions of every Mac", Usage: "history search [QUERY…] [flags]",
			Help: "The shared history: every Claude and Codex conversation of the owner's Macs (started in Hesper or not), newest activity first. QUERY matches prompts, answers and titles (every word; the last one as a prefix); without it, the newest sessions. " +
				"The table shows id, last activity, flags (live, ext = started outside Hesper, archived, moved), turns, folder and title (or the matching snippet, matches in [ ]). " +
				"A page has at most 50 sessions; when there are more, the last line (stderr) gives the --cursor of the next page.",
			Output: "{items:[Session], cursor?}; " + sessionOutput,
			Examples: []string{
				"hesperctl history search flaky test",
				"hesperctl history search --project . --since 7d",
				"hesperctl history search --kind codex --machine mini --live",
				"hesperctl history search --json migration | jq -r '.items[0].id'",
			},
			Run: historySearch},
		{Name: "history show", Summary: "Show a session: prompts, answer, todos and changed files", Usage: "history show ID [--json]",
			Help:   sessionRefHelp + " Also the git state of its folder on its home: changed files, uncommitted work, ahead/behind.",
			Output: sessionOutput + " plus changes?: {files:[{path, added, removed}], uncommitted, ahead?, behind?, worktreeExists}",
			Run:    historyShow},
		{Name: "history brief", Summary: "Print a session's brief (to hand it to another agent)", Usage: "history brief ID [--json]",
			Help:     sessionRefHelp + " The task, the prompts in between, the last request and answer, the todos and changed files; built without a model. continue-as starts an agent with it.",
			Output:   "{text}",
			Examples: []string{"hesperctl history brief mini:claude:5b0c1d2e-… | hesperctl new --kind codex -"},
			Run:      historyBrief},
		{Name: "history stats", Summary: "Show the size of the shared history and the indexing progress", Usage: "history stats [--json]",
			Output: "{count, byKind:{kind: n}, byMachine:{machine: n}, indexBytes, mirrorBytes, indexing:{done, total}}",
			Run:    historyStats},
		{Name: "history archive", Summary: "Archive a session (or take it out with --undo)", Usage: "history archive ID [--undo] [--json]",
			Help:   sessionRefHelp + " Archived sessions are left out of history search unless --archived (on every Mac).",
			Output: "Session",
			Run:    historyArchive},
		{Name: "history delete", Summary: "Delete a session and its transcript", Usage: "history delete ID",
			Help: sessionRefHelp + " Hidden on every Mac at once; its home removes the transcript 30 s later (Codex: moves it to its archive). history undelete within those 30 s takes it back. A running session exits 7 (code live).",
			Run:  historyDelete},
		{Name: "history undelete", Summary: "Take a delete back (within 30 s)", Usage: "history undelete ID",
			Help: sessionRefHelp + " After the transcript is gone: exit 1 (code invalid).",
			Run:  historyUndelete},
		{Name: "history resume", Summary: "Resume a session in a new agent", Usage: "history resume ID [--machine M] [--json]",
			Help: sessionRefHelp + " Starts claude --resume / codex resume on the session's home, in its folder (a missing worktree is recreated from its branch), and prints the agent's id. --machine another Mac moves the session there (branch, uncommitted work, transcript). " +
				"A note on stderr says what did not come along. When the session runs already: exit 7 (code live; --json adds agentId, the agent running it).",
			Output:   "Agent plus note?",
			Examples: []string{"hesperctl history resume mini:claude:5b0c1d2e-…", "hesperctl history resume L:codex:019a… --machine mini"},
			Run:      historyStart("sessions.resume")},
		{Name: "history fork", Summary: "Start a new agent from a copy of a session", Usage: "history fork ID [--machine M] [--json]",
			Help:   sessionRefHelp + " A new conversation with the same history (claude --fork-session, codex fork); the original stays. Allowed while it runs. Prints the agent's id.",
			Output: "Agent plus note?",
			Run:    historyStart("sessions.fork")},
		{Name: "history continue-as", Summary: "Continue a session in the other agent kind", Usage: "history continue-as ID --kind claude|codex [--machine M] [--json]",
			Help:     sessionRefHelp + " Starts --kind in the session's folder with the session's brief as its first prompt (e.g. a Claude session continued by Codex). Prints the agent's id.",
			Output:   "Agent",
			Examples: []string{"hesperctl history continue-as mini:claude:5b0c1d2e-… --kind codex"},
			Run:      historyStart("sessions.continueAs")},
	} {
		c.Group = groupHistory
		register(c)
	}
}

var dayDuration = regexp.MustCompile(`^(\d+)([dw])$`)

// parseSince turns --since (24h, 7d, 2w, 2026-10-01, RFC 3339) into RFC 3339.
func parseSince(s string, now time.Time) (string, error) {
	if m := dayDuration.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		if m[2] == "w" {
			n *= 7
		}
		return now.AddDate(0, 0, -n).UTC().Format(time.RFC3339), nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(-d).UTC().Format(time.RFC3339), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(time.RFC3339Nano), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	return "", usagef("--since %q: want a duration (90m, 24h, 7d, 2w) or a date (2026-10-01, RFC 3339)", s)
}

// historyProject is --project as a project id: a project by id or name;
// a folder's deepest project containing it, else its scratch id.
func historyProject(ctx context.Context, c *wire.Client, ref string) (string, error) {
	if !looksLikePath(ref) {
		p, err := resolveProject(ctx, c, ref)
		return p.ID, err
	}
	path, err := expandPath(ref)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	var list []wire.ProjectInfo
	if err := c.Call(ctx, "projects.list", nil, &list); err != nil {
		return "", err
	}
	best, bestLen := "", 0
	for _, p := range list {
		if p.Kind == wire.ProjectScratch {
			continue
		}
		for _, dir := range p.Paths {
			if (path == dir || strings.HasPrefix(path, dir+"/")) && len(dir) > bestLen {
				best, bestLen = p.ID, len(dir)
			}
		}
	}
	if best != "" {
		return best, nil
	}
	return wire.ScratchPrefix + path, nil
}

func historySearch(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	project := f.String("project", "", "Only this project: id, name or folder (. is the current folder)")
	var kinds, machines listFlag
	f.Var(&kinds, "kind", "Only claude or codex sessions")
	f.Var(&machines, "machine", "Only sessions whose home is this machine (short name; repeatable)")
	since := f.String("since", "", "Only sessions active since: 90m, 24h, 7d, 2w, or a date (2026-10-01, RFC 3339)")
	live := f.Bool("live", false, "Only sessions running now")
	external := f.Bool("external", false, "Only sessions started outside Hesper (terminal, desktop apps, IDEs)")
	archived := f.Bool("archived", false, "Only archived sessions (instead of the others)")
	moved := f.Bool("moved", false, "Include sessions continued on another machine")
	limit := f.Int("limit", 50, "Sessions per page (at most 50)")
	cursor := f.String("cursor", "", "Next page: the cursor the previous page ended with")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		p := wire.SessionSearchParams{Query: strings.Join(positional, " "), Kinds: kinds, Machines: machines, Cursor: *cursor, Moved: *moved}
		for _, k := range kinds {
			if k != wire.KindClaude && k != wire.KindCodex {
				return usagef("--kind %q: want claude or codex", k)
			}
		}
		if *limit < 1 {
			return usagef("--limit must be at least 1")
		}
		p.Limit = min(*limit, 50)
		if *project != "" {
			id, err := historyProject(ctx, c, *project)
			if err != nil {
				return err
			}
			p.ProjectID = id
		}
		if *since != "" {
			s, err := parseSince(*since, time.Now())
			if err != nil {
				return err
			}
			p.Since = s
		}
		yes := true
		if *live {
			p.Live = &yes
		}
		if *external {
			p.External = &yes
		}
		if *archived {
			p.Archived = &yes
		}
		var res wire.SessionSearchResult
		if err := c.Call(ctx, "sessions.search", p, &res); err != nil {
			return err
		}
		if *asJSON {
			if res.Items == nil {
				res.Items = []wire.Session{}
			}
			return output(res)
		}
		printSessions(os.Stdout, res.Items)
		if res.Cursor != "" {
			fmt.Fprintf(os.Stderr, "more: --cursor %s\n", res.Cursor)
		}
		return nil
	})
}

// sessionFlags are a session's marks for the table.
func sessionFlags(s wire.Session) string {
	var f []string
	if s.Live != nil {
		f = append(f, "live")
	}
	if s.External {
		f = append(f, "ext")
	}
	if s.Archived {
		f = append(f, "archived")
	}
	if s.MovedTo != "" {
		f = append(f, "moved:"+s.MovedTo)
	}
	if len(f) == 0 {
		return "-"
	}
	return strings.Join(f, ",")
}

// oneLine is s on one line, at most n characters.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func printSessions(w io.Writer, list []wire.Session) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tLAST\tFLAGS\tTURNS\tFOLDER\tTITLE")
	for _, s := range list {
		title := s.Title
		if s.Snippet != "" {
			title = s.Snippet
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", s.ID, ago(parseTime(s.LastActivity)), sessionFlags(s), s.Turns, tilde(s.Cwd), oneLine(title, 80))
	}
	return tw.Flush()
}

// sessionCall is the body of the commands taking one session ID.
func sessionCall(ctx context.Context, f *flag.FlagSet, args []string, usage string, do func(ctx context.Context, c *wire.Client, id string) error) error {
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl %s", usage)
		}
		return do(ctx, c, positional[0])
	})
}

func historyShow(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return sessionCall(ctx, f, args, "history show ID", func(ctx context.Context, c *wire.Client, id string) error {
		var d wire.SessionDetail
		if err := c.Call(ctx, "sessions.show", wire.SessionIDParams{ID: id}, &d); err != nil {
			return err
		}
		if *asJSON {
			return output(d)
		}
		return printSession(os.Stdout, d)
	})
}

func printSession(w io.Writer, d wire.SessionDetail) error {
	s := d.Session
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "%s\t%s\n", k, v)
		}
	}
	row("id", s.ID)
	row("title", s.Title)
	kind := s.Kind
	if s.Origin != "" {
		kind += " (" + s.Origin + ")"
	}
	row("kind", kind)
	row("machine", s.Machine)
	row("folder", tilde(s.Cwd))
	row("branch", s.Branch)
	row("project", s.ProjectID)
	row("flags", strings.TrimPrefix(sessionFlags(s), "-"))
	if s.Live != nil && s.Live.AgentID != "" {
		row("agent", s.Live.AgentID)
	}
	row("started", s.StartedAt)
	row("last", s.LastActivity+" ("+ago(parseTime(s.LastActivity))+")")
	turns := strconv.Itoa(s.Turns)
	if s.Tokens > 0 {
		turns += fmt.Sprintf(", %d tokens", s.Tokens)
	}
	row("turns", turns)
	if len(s.Mirrored) > 0 {
		row("mirrored", strings.Join(s.Mirrored, ", "))
	}
	tw.Flush()
	section := func(title, text string) {
		if text = strings.TrimSpace(text); text != "" {
			fmt.Fprintf(w, "\n%s:\n%s\n", title, text)
		}
	}
	section("First prompt", s.FirstPrompt)
	if s.LastUser != s.FirstPrompt {
		section("Last prompt", s.LastUser)
	}
	section("Last answer", s.LastAssistant)
	if len(s.Todos) > 0 {
		fmt.Fprintln(w, "\nTodos:")
		for _, t := range s.Todos {
			mark := "[ ]"
			if t.Done {
				mark = "[x]"
			}
			fmt.Fprintf(w, "  %s %s\n", mark, t.Text)
		}
	}
	if ch := d.Changes; ch != nil {
		var state []string
		if !ch.WorktreeExists {
			state = append(state, "folder gone")
		}
		if ch.Uncommitted {
			state = append(state, "uncommitted changes")
		}
		if ch.Ahead != nil {
			state = append(state, fmt.Sprintf("%d ahead", *ch.Ahead))
		}
		if ch.Behind != nil {
			state = append(state, fmt.Sprintf("%d behind", *ch.Behind))
		}
		fmt.Fprintf(w, "\nChanges (%d files", len(ch.Files))
		if len(state) > 0 {
			fmt.Fprintf(w, "; %s", strings.Join(state, ", "))
		}
		fmt.Fprintln(w, "):")
		for _, file := range ch.Files {
			fmt.Fprintf(w, "  +%d -%d %s\n", file.Added, file.Removed, file.Path)
		}
	}
	return nil
}

func historyBrief(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return sessionCall(ctx, f, args, "history brief ID", func(ctx context.Context, c *wire.Client, id string) error {
		var b wire.SessionBrief
		if err := c.Call(ctx, "sessions.brief", wire.SessionIDParams{ID: id}, &b); err != nil {
			return err
		}
		if *asJSON {
			return output(b)
		}
		fmt.Println(strings.TrimRight(b.Text, "\n"))
		return nil
	})
}

// bytesString is a size for people: 12.4 MB.
func bytesString(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// counts is "a 3, b 2", largest first.
func counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func historyStats(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, _ []string) error {
		var s wire.SessionStats
		if err := c.Call(ctx, "sessions.stats", nil, &s); err != nil {
			return err
		}
		if *asJSON {
			return output(s)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintf(tw, "sessions\t%d\n", s.Count)
		fmt.Fprintf(tw, "by kind\t%s\n", counts(s.ByKind))
		fmt.Fprintf(tw, "by machine\t%s\n", counts(s.ByMachine))
		fmt.Fprintf(tw, "index\t%s\n", bytesString(s.IndexBytes))
		fmt.Fprintf(tw, "mirror\t%s\n", bytesString(s.MirrorBytes))
		indexing := "done"
		if s.Indexing.Total > 0 && s.Indexing.Done < s.Indexing.Total {
			indexing = fmt.Sprintf("%d of %d transcripts", s.Indexing.Done, s.Indexing.Total)
		}
		fmt.Fprintf(tw, "indexing\t%s\n", indexing)
		return tw.Flush()
	})
}

func historyArchive(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	undo := f.Bool("undo", false, "Take it out of the archive")
	return sessionCall(ctx, f, args, "history archive ID [--undo]", func(ctx context.Context, c *wire.Client, id string) error {
		var s wire.Session
		if err := c.Call(ctx, "sessions.archive", wire.SessionArchiveParams{ID: id, Archived: !*undo}, &s); err != nil {
			return err
		}
		if *asJSON {
			return output(s)
		}
		return nil
	})
}

func historyDelete(ctx context.Context, f *flag.FlagSet, args []string) error {
	return sessionCall(ctx, f, args, "history delete ID", func(ctx context.Context, c *wire.Client, id string) error {
		return c.Call(ctx, "sessions.delete", wire.SessionDeleteParams{ID: id}, nil)
	})
}

func historyUndelete(ctx context.Context, f *flag.FlagSet, args []string) error {
	return sessionCall(ctx, f, args, "history undelete ID", func(ctx context.Context, c *wire.Client, id string) error {
		return c.Call(ctx, "sessions.delete", wire.SessionDeleteParams{ID: id, Undo: true}, nil)
	})
}

// startedAgent is what sessions.resume / fork / continueAs answer.
type startedAgent struct {
	wire.Agent
	Note string `json:"note,omitempty"`
}

// historyStart is the Run of resume, fork and continue-as.
func historyStart(method string) func(context.Context, *flag.FlagSet, []string) error {
	return func(ctx context.Context, f *flag.FlagSet, args []string) error {
		asJSON := jsonFlag(f)
		machine := f.String("machine", "", "Run it on this machine (short name; default the session's home)")
		var kind *string
		usage := "history resume ID [--machine M]"
		switch method {
		case "sessions.fork":
			usage = "history fork ID [--machine M]"
		case "sessions.continueAs":
			kind = f.String("kind", "", "The agent kind to continue in: claude or codex")
			usage = "history continue-as ID --kind claude|codex [--machine M]"
		}
		return sessionCall(ctx, f, args, usage, func(ctx context.Context, c *wire.Client, id string) error {
			var params any = wire.SessionResumeParams{ID: id, Machine: *machine}
			if kind != nil {
				if *kind != wire.KindClaude && *kind != wire.KindCodex {
					return usagef("--kind claude or --kind codex is required")
				}
				params = wire.SessionContinueParams{ID: id, Kind: *kind, Machine: *machine}
			}
			ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			var a startedAgent
			if err := c.Call(ctx, method, params, &a); err != nil {
				return err
			}
			if *asJSON {
				return output(a)
			}
			fmt.Println(a.ID)
			if a.Note != "" {
				fmt.Fprintln(os.Stderr, "note: "+a.Note)
			}
			return nil
		})
	}
}
