package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Agent lifecycle and interaction: closing, killing, backgrounding and
// tidying agents; answering, choosing, typing keys; attaching files;
// reading the screen; following events and waiting for states; one
// agent's detail.
//
//	close | kill | background | tidy | answer | choose | attach-file |
//	screen | events | wait | show   (and send's --key / --raw)

func init() {
	for _, c := range []Command{
		{Name: "close", Destructive: true, Summary: "Close agents (gone from the wall; the conversation stays in History)", Usage: "close ID… [--no-wait] [--json]",
			Help:   idHelp + " A running agent is interrupted, then ended; it leaves the agent list. Waits (at most 10 s) until the agents are gone from the list, unless --no-wait. Prints each id and the session to resume it with (hesperctl history / sessions.resume). Closing an agent gives its children to its parent.",
			Output: "[{id, session}]", Examples: []string{"hesperctl close a7f3k2", "hesperctl close a7f3k2 b8c9d0 --json"},
			Run: closeCommand},
		{Name: "kill", Destructive: true, Summary: "End agents now (they stay, exited, killed)", Usage: "kill ID…",
			Help:     idHelp + " SIGTERM, then SIGKILL. The agent stays listed (state exited, ended \"killed\"): read it with screen, resume or close it.",
			Examples: []string{"hesperctl kill a7f3k2"}, Run: killCommand},
		{Name: "background", Summary: "Hide agents from the walls while they run, or bring them back", Usage: "background ID… [--off]",
			Help:     idHelp + " A background agent keeps running; once it finishes (done, idle, exited) hesperd closes it. --off brings it back to the wall.",
			Examples: []string{"hesperctl background a7f3k2", "hesperctl background a7f3k2 --off"}, Run: backgroundCommand},
		{Name: "tidy", Destructive: true, Summary: "Close every finished agent (done, idle, exited)", Usage: "tidy [--project DIR] [--dry-run] [--no-wait] [--json]",
			Help:   "What ⇧⌘W does in the app: closes the agents in state done, idle or exited, never working or waiting for you. Background agents close themselves and are left out. Waits (at most 10 s) until they are gone from the list, unless --no-wait. Prints the ids closed.",
			Output: "[{id, name, state, session}]", Examples: []string{"hesperctl tidy --dry-run", "hesperctl tidy --project ~/src/app"},
			Run: tidyCommand},
		{Name: "answer", Summary: "Answer what an agent waits for with a decision", Usage: "answer ID DECISION [--message M]",
			Help:     idHelp + " DECISION is one of the agent's attention options (hesperctl show ID): allow, always, deny for an approval; trust, exit for a trust question; skip, update for Codex's update screen. --message goes with deny: the agent is told what to do instead.",
			Examples: []string{"hesperctl answer a7f3k2 always", "hesperctl answer a7f3k2 deny --message 'use a branch'", "hesperctl answer a7f3k2 trust"},
			Run:      answerCommand},
		{Name: "choose", Summary: "Pick choice N of what an agent asks", Usage: "choose ID N",
			Help:     idHelp + " N counts from 1 in the choices hesperctl show lists: the decisions of an approval or trust question (as answer), or the numbered options of a question (its number key is typed).",
			Examples: []string{"hesperctl show a7f3k2", "hesperctl choose a7f3k2 2"}, Run: chooseCommand},
		{Name: "attach-file", Summary: "Give files to an agent (as a drop on its terminal does)", Usage: "attach-file ID PATH… [--upload] [--no-paste] [--json]",
			Help:     idHelp + " An agent on another machine gets an uploaded copy (50 MiB at most per file); this Mac's agents get the path itself unless --upload. The paths are pasted into the agent's input without Enter (PNG and JPEG become image attachments in Claude and Codex). Prints what the agent receives.",
			Output:   "{files:[{path, agentPath, uploaded}], pastes:[text]}",
			Examples: []string{"hesperctl attach-file a7f3k2 shot.png", "hesperctl attach-file mini/a7f3k2 spec.pdf notes.txt && hesperctl send mini/a7f3k2 read these"},
			Run:      attachFileCommand},
		{Name: "screen", ReadOnly: true, Summary: "Print an agent's terminal as plain text", Usage: "screen ID [--rows N] [--scrollback N] [--json]",
			Help:   idHelp + " The screen hesperd keeps for the agent (as a person sees it, no colors or escape codes); --rows N only its last N rows with text (blank rows below the output are dropped first, so a mostly empty screen still shows its output); --scrollback that many lines from above the screen first. Works for agents of every machine.",
			Output: "{text, rows, cols, cursor?:{col, row}, alt?}", Examples: []string{"hesperctl screen a7f3k2", "hesperctl screen a7f3k2 --rows 10 --scrollback 200"},
			Run: screenCommand},
		{Name: "events", NoMCP: true, Summary: "Stream agent, draft, project and history changes as JSON lines", Usage: "events [--agent ID]… [--kinds K,…]",
			Help:   "One JSON object per line, {\"method\", \"params\"}, until interrupted: agents.changed {agent}, agents.removed {id, reason, data?: {to}} (first one agents.changed per agent) and agents.moving {id, step, to, percent?, agent?} (a move's progress), drafts.*, projects.*, groups.*, sessions.*. --kinds keeps only agents, drafts, projects, groups or sessions; --agent only those agents' events.",
			Output: "a stream of {method, params}", Examples: []string{"hesperctl events --kinds agents", "hesperctl events --agent a7f3k2 | jq -r '.params.agent.state'"},
			Run: eventsCommand},
		{Name: "wait", ReadOnly: true, Summary: "Wait until agents reach a state", Usage: "wait ID… --until COND [--any|--all] [--next] [--timeout D] [--json]",
			Help:   idHelp + " COND: settled (done, idle, exited, approval, question or error: no longer working on its own, what new --wait waits for; a shell started without --track is always idle, so it is settled at once), needs-you (approval or question), done, idle, exited, working, finished (done, idle or exited), or state=S[,S…]. --all (default): every agent reached it once; --any: one did. --next ignores the state an agent is in when wait starts (after send, wait for the turn it starts). Prints the agents that matched. Exits 6 on --timeout, 3 when an agent goes away.",
			Output: "[Agent]", Examples: []string{"hesperctl wait a7f3k2 --until needs-you --timeout 10m", "hesperctl send a7f3k2 run the tests && hesperctl wait a7f3k2 --next --until settled --timeout 15m", "hesperctl wait a b c --any --until state=error,done"},
			Run: waitCommand},
		{Name: "show", ReadOnly: true, Summary: "Show one agent in full, with the choices it offers", Usage: "show ID [--json]",
			Help:   idHelp + " Every field of the agent; for an agent waiting for you also its question and numbered choices (for choose).",
			Output: "Agent with choices:[{n, title, decision?, keys?}]", Examples: []string{"hesperctl show a7f3k2", "hesperctl show a7f3k2 --json | jq .attention"},
			Run: showCommand},
	} {
		c.Group = groupAgents
		register(c)
	}
	// send (agents.go) gains keys and raw input here.
	if c := findCommand("send"); c != nil {
		c.Usage = "send ID TEXT… [--no-submit] [--raw] [--key NAME]…"
		c.Help = idHelp + " TEXT - reads stdin. The text is pasted (bracketed paste), then submitted with Enter unless --no-submit. --raw types it as it is instead (no paste, no Enter). --key presses keys after the text (TEXT is then optional and Enter is not pressed for you): " + keyNames + "; ctrl-A … ctrl-Z; any single character."
		c.Examples = []string{"hesperctl send a7f3k2 now run the tests", "git diff | hesperctl send mini/a7f3k2 -", "hesperctl send a7f3k2 --key esc", "hesperctl send a7f3k2 --key down --key enter", "hesperctl send a7f3k2 --raw y"}
		c.Run = sendCommand
	}
}

// keyFlag is send's repeatable --key (not split at commas: "," is a key).
type keyFlag []string

func (k *keyFlag) String() string { return strings.Join(*k, " ") }
func (k *keyFlag) Set(s string) error {
	*k = append(*k, s)
	return nil
}

// resolveAgents resolves every ref (resolveAgent).
func resolveAgents(ctx context.Context, c *wire.Client, refs []string) ([]string, error) {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		id, err := resolveAgent(ctx, c, ref)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// getAgent is one agent by full id.
func getAgent(ctx context.Context, c *wire.Client, id string) (wire.Agent, error) {
	var list []wire.Agent
	if err := c.Call(ctx, "agents.list", nil, &list); err != nil {
		return wire.Agent{}, err
	}
	for _, a := range list {
		if a.ID == id {
			return a, nil
		}
	}
	return wire.Agent{}, &wire.Error{Code: wire.CodeNotFound, Message: "no agent " + id}
}

// eachAgent runs do for every agent named by refs (at least one), all of
// them even when one fails; the first error is returned.
func eachAgent(ctx context.Context, c *wire.Client, use string, refs []string, do func(id string) error) error {
	if len(refs) == 0 {
		return usagef("usage: hesperctl %s", use)
	}
	var first error
	for _, ref := range refs {
		id, err := resolveAgent(ctx, c, ref)
		if err == nil {
			err = do(id)
		}
		if err != nil {
			if first == nil {
				first = err
			}
			if len(refs) > 1 {
				fmt.Fprintf(os.Stderr, "%s: %v\n", ref, err)
			}
		}
	}
	return first
}

type closed struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	State   string `json:"state,omitempty"`
	Session string `json:"session,omitempty"`
}

func printClosed(list []closed, asJSON bool) error {
	if asJSON {
		if list == nil {
			list = []closed{}
		}
		return output(list)
	}
	for _, c := range list {
		if c.Session != "" {
			fmt.Printf("%s\tsession %s\n", c.ID, c.Session)
		} else {
			fmt.Println(c.ID)
		}
	}
	return nil
}

// closeWait bounds how long close and tidy wait for the agents to leave
// the list.
var closeWait = 10 * time.Second

// removals follows agents.removed on a subscription of its own, so close
// and tidy can return once the agents are gone.
type removals struct {
	sub  *wire.Client
	mu   sync.Mutex
	gone map[string]bool
	news chan struct{}
}

func watchRemovals(ctx context.Context, f *flag.FlagSet) (*removals, error) {
	socket := wire.SocketPath()
	if fl := f.Lookup("daemon-socket"); fl != nil {
		socket = fl.Value.String()
	}
	sub, err := subscribe(ctx, socket)
	if err != nil {
		return nil, err
	}
	r := &removals{sub: sub, gone: map[string]bool{}, news: make(chan struct{}, 1)}
	go func() {
		for n := range sub.Notifications() {
			if n.Method != wire.NoteRemoved {
				continue
			}
			var rm wire.Removed
			if json.Unmarshal(n.Params, &rm) != nil {
				continue
			}
			r.mu.Lock()
			r.gone[rm.ID] = true
			r.mu.Unlock()
			select {
			case r.news <- struct{}{}:
			default:
			}
		}
	}()
	return r, nil
}

// wait waits (at most closeWait) until every id is removed; a note on
// stderr names those still there.
func (r *removals) wait(ctx context.Context, ids []string) {
	if r == nil {
		return
	}
	defer r.sub.Close()
	timer := time.NewTimer(closeWait)
	defer timer.Stop()
	for {
		var left []string
		r.mu.Lock()
		for _, id := range ids {
			if !r.gone[id] {
				left = append(left, id)
			}
		}
		r.mu.Unlock()
		if len(left) == 0 {
			return
		}
		select {
		case <-r.news:
		case <-timer.C:
			fmt.Fprintf(os.Stderr, "still closing after %s: %s\n", closeWait, strings.Join(left, ", "))
			return
		case <-ctx.Done():
			return
		}
	}
}

func closeCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	noWait := f.Bool("no-wait", false, "Return at once, before the agents have left the list")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, refs []string) error {
		var gone *removals
		if !*noWait && len(refs) > 0 {
			var err error
			if gone, err = watchRemovals(ctx, f); err != nil {
				return err
			}
		}
		var out []closed
		err := eachAgent(ctx, c, "close ID…", refs, func(id string) error {
			var res wire.CloseResult
			if err := c.Call(ctx, "agents.close", wire.IDParams{ID: id}, &res); err != nil {
				return err
			}
			out = append(out, closed{ID: id, Session: res.Session})
			return nil
		})
		var ids []string
		for _, c := range out {
			ids = append(ids, c.ID)
		}
		gone.wait(ctx, ids)
		if len(out) > 0 || err == nil {
			if perr := printClosed(out, *asJSON); err == nil {
				err = perr
			}
		}
		return err
	})
}

func killCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, refs []string) error {
		return eachAgent(ctx, c, "kill ID…", refs, func(id string) error {
			return c.Call(ctx, "agents.kill", wire.IDParams{ID: id}, nil)
		})
	})
}

func backgroundCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	off := f.Bool("off", false, "Bring the agents back to the wall")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, refs []string) error {
		return eachAgent(ctx, c, "background ID… [--off]", refs, func(id string) error {
			return c.Call(ctx, "agents.background", wire.BackgroundParams{ID: id, Background: !*off}, nil)
		})
	})
}

// finished: what tidy closes (the app's CloseRules.finishedStates).
func finished(state string) bool {
	return state == wire.StateDone || state == wire.StateIdle || state == wire.StateExited
}

func tidyCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	project := f.String("project", "", "Only agents working in this folder (or below it)")
	dry := f.Bool("dry-run", false, "List what would be closed, close nothing")
	noWait := f.Bool("no-wait", false, "Return at once, before the agents have left the list")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) > 0 {
			return usagef("usage: hesperctl tidy [--project DIR] [--dry-run] [--no-wait]")
		}
		var gone *removals
		if !*dry && !*noWait {
			var err error
			if gone, err = watchRemovals(ctx, f); err != nil {
				return err
			}
		}
		dir := ""
		if *project != "" {
			abs, err := filepath.Abs(expandHome(*project))
			if err != nil {
				return err
			}
			dir = abs
		}
		var list []wire.Agent
		if err := c.Call(ctx, "agents.list", nil, &list); err != nil {
			return err
		}
		var out []closed
		var first error
		for _, a := range list {
			if !finished(a.State) || a.Background || dir != "" && !within(a.Dir(), dir) && !within(a.Project, dir) {
				continue
			}
			rec := closed{ID: a.ID, Name: a.Name, State: a.State}
			if !*dry {
				var res wire.CloseResult
				if err := c.Call(ctx, "agents.close", wire.IDParams{ID: a.ID}, &res); err != nil {
					fmt.Fprintf(os.Stderr, "%s: %v\n", a.ID, err)
					if first == nil {
						first = err
					}
					continue
				}
				rec.Session = res.Session
			}
			out = append(out, rec)
		}
		if gone != nil {
			var ids []string
			for _, c := range out {
				ids = append(ids, c.ID)
			}
			gone.wait(ctx, ids)
		}
		if !*asJSON && len(out) == 0 && first == nil {
			fmt.Fprintln(os.Stderr, "Nothing finished to tidy up")
			return nil
		}
		if err := printClosed(out, *asJSON); err != nil {
			return err
		}
		return first
	})
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

// within: path is dir or below it.
func within(path, dir string) bool {
	return path != "" && (path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/"))
}

// decisions are what agents.answer takes.
var decisions = []string{wire.Allow, wire.Always, wire.Deny, wire.Trust, wire.Leave, wire.Skip, wire.Update}

func answerCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	message := f.String("message", "", "With deny: tell the agent what to do instead")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 2 {
			return usagef("usage: hesperctl answer ID DECISION [--message M] (DECISION: %s)", strings.Join(decisions, ", "))
		}
		decision := strings.ToLower(positional[1])
		if !contains(decisions, decision) {
			return usagef("unknown decision %q: one of %s", positional[1], strings.Join(decisions, ", "))
		}
		if *message != "" && decision != wire.Deny {
			return usagef("--message goes with deny")
		}
		id, err := resolveAgent(ctx, c, positional[0])
		if err != nil {
			return err
		}
		return c.Call(ctx, "agents.answer", wire.AnswerParams{ID: id, Decision: decision, Message: *message}, nil)
	})
}

// Choice is one answer an agent waiting for you offers (the app's inbox
// answers): a decision (agents.answer) or keys to type (a numbered
// option of a question).
type Choice struct {
	N        int    `json:"n"`
	Title    string `json:"title"`
	Decision string `json:"decision,omitempty"`
	Keys     string `json:"keys,omitempty"`
}

// choices are what a's attention offers, numbered from 1 (the app's
// AttentionInbox.answers): an approval's decisions (allow, always, deny
// as offered; allow and deny without options); a question's decisions
// (trust/exit, skip/update), else the numbered options in its detail
// (at most 9, their number key). Nil when there is nothing to choose.
func choices(a wire.Agent) []Choice {
	var out []Choice
	add := func(title, decision, keys string) {
		out = append(out, Choice{N: len(out) + 1, Title: title, Decision: decision, Keys: keys})
	}
	var opts []string
	if a.Attention != nil {
		opts = a.Attention.Options
	}
	switch a.State {
	case wire.StateApproval:
		for _, d := range []string{wire.Allow, wire.Always, wire.Deny} {
			if contains(opts, d) {
				add(d, d, "")
			}
		}
		if len(out) == 0 {
			add(wire.Allow, wire.Allow, "")
			add(wire.Deny, wire.Deny, "")
		}
	case wire.StateQuestion:
		if a.Attention != nil && a.Attention.Kind == wire.StateQuestion {
			for _, o := range opts {
				if contains(decisions, o) {
					add(o, o, "")
				}
			}
		}
		if len(out) > 0 || a.Attention == nil {
			break
		}
		if _, options, ok := numberedOptions(a.Attention.Detail); ok && len(options) <= 9 {
			for i, o := range options {
				add(o, "", strconv.Itoa(i+1))
			}
		}
	}
	return out
}

// numberedOptions splits a question into the question and its numbered
// options ("Which tone? 1. Formal 2. Casual", "1) A", "[1] A"): the
// longest run 1, 2, 3 … of markers, at least two (the app's
// AttentionInbox.numberedOptions). A marker is an optional ( or [, a
// digit 1–9, then . ) or ], at the start or after white space, followed
// by white space.
func numberedOptions(text string) (string, []string, bool) {
	type marker struct{ start, end, n int }
	var ms []marker
	space := func(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v' }
	for i := 0; i < len(text); i++ {
		j := i
		if text[j] == '(' || text[j] == '[' {
			j++
		}
		if j+2 >= len(text) || text[j] < '1' || text[j] > '9' || !strings.ContainsRune(".)]", rune(text[j+1])) || !space(text[j+2]) {
			continue
		}
		if i > 0 && !space(text[i-1]) {
			continue
		}
		ms = append(ms, marker{i, j + 2, int(text[j] - '0')})
		i = j + 1
	}
	var best []marker
	for i, m := range ms {
		if m.n != 1 {
			continue
		}
		run := []marker{m}
		for _, x := range ms[i+1:] {
			if x.n == len(run)+1 {
				run = append(run, x)
			}
		}
		if len(run) > len(best) {
			best = run
		}
	}
	if len(best) < 2 {
		return "", nil, false
	}
	trim := func(s string) string { return strings.Trim(s, " \t\n\r\f\v,;") }
	var options []string
	for k, m := range best {
		end := len(text)
		if k+1 < len(best) {
			end = best[k+1].start
		}
		o := trim(text[m.end:end])
		for _, tail := range []string{" or", ",or"} {
			if strings.HasSuffix(strings.ToLower(o), tail) {
				o = trim(o[:len(o)-len(tail)])
			}
		}
		if o == "" {
			return "", nil, false
		}
		options = append(options, o)
	}
	return trim(text[:best[0].start]), options, true
}

func chooseCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 2 {
			return usagef("usage: hesperctl choose ID N")
		}
		n, err := strconv.Atoi(positional[1])
		if err != nil || n < 1 {
			return usagef("N must be a number from 1 (hesperctl show ID lists the choices)")
		}
		id, err := resolveAgent(ctx, c, positional[0])
		if err != nil {
			return err
		}
		a, err := getAgent(ctx, c, id)
		if err != nil {
			return err
		}
		list := choices(a)
		if len(list) == 0 {
			return failf(wire.CodeInvalid, "%s offers no choices (state %s): read it with hesperctl screen %s, type with hesperctl send", id, a.State, id)
		}
		if n > len(list) {
			return usagef("%s offers %d choices (hesperctl show %s)", id, len(list), id)
		}
		ch := list[n-1]
		if ch.Decision != "" {
			return c.Call(ctx, "agents.answer", wire.AnswerParams{ID: id, Decision: ch.Decision}, nil)
		}
		return c.Call(ctx, "agents.input", wire.InputParams{ID: id, Text: ch.Keys}, nil)
	})
}

// keys are send --key's names and the bytes they type.
var keys = map[string]string{
	"esc": "\x1b", "escape": "\x1b",
	"enter": "\r", "return": "\r",
	"tab": "\t", "shift-tab": "\x1b[Z", "backtab": "\x1b[Z",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"home": "\x1b[H", "end": "\x1b[F", "pageup": "\x1b[5~", "pagedown": "\x1b[6~",
	"backspace": "\x7f", "delete": "\x1b[3~", "space": " ",
}

const keyNames = "esc, enter, tab, shift-tab, up, down, left, right, home, end, pageup, pagedown, backspace, delete, space"

// keyBytes is what key name types.
func keyBytes(name string) (string, bool) {
	if utf8.RuneCountInString(name) == 1 {
		return name, true
	}
	lower := strings.ToLower(name)
	if k, ok := keys[lower]; ok {
		return k, true
	}
	for _, prefix := range []string{"ctrl-", "ctrl+", "c-", "^"} {
		if rest, ok := strings.CutPrefix(lower, prefix); ok && len(rest) == 1 && rest[0] >= 'a' && rest[0] <= 'z' {
			return string(rune(rest[0] - 'a' + 1)), true
		}
	}
	return "", false
}

// keyGap separates keys (an Esc and the next key typed at once read as
// one Alt-key); pasteGap lets a TUI take a paste before the next input.
var (
	keyGap   = 50 * time.Millisecond
	pasteGap = 100 * time.Millisecond
)

func sendCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	noSubmit := f.Bool("no-submit", false, "Type the text without pressing Enter")
	raw := f.Bool("raw", false, "Type TEXT as it is: no bracketed paste, no Enter")
	var keyList keyFlag
	f.Var(&keyList, "key", "Press key `NAME` after the text (repeatable): "+keyNames+", ctrl-X, a character")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		var seq []string
		for _, k := range keyList {
			b, ok := keyBytes(k)
			if !ok {
				return usagef("unknown key %q: %s, ctrl-A … ctrl-Z or one character", k, keyNames)
			}
			seq = append(seq, b)
		}
		if len(positional) < 1 || len(positional) < 2 && len(seq) == 0 {
			return usagef("usage: hesperctl send ID TEXT… [--no-submit] [--raw] [--key NAME]…")
		}
		id, err := resolveAgent(ctx, c, positional[0])
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
		if len(positional) > 1 {
			p := wire.InputParams{ID: id, Text: text, Paste: !*raw, Submit: !*raw && !*noSubmit && len(seq) == 0}
			if err := c.Call(ctx, "agents.input", p, nil); err != nil {
				return err
			}
			if len(seq) > 0 {
				time.Sleep(pasteGap)
			}
		}
		for i, k := range seq {
			if i > 0 {
				time.Sleep(keyGap)
			}
			if err := c.Call(ctx, "agents.input", wire.InputParams{ID: id, Text: k}, nil); err != nil {
				return err
			}
		}
		return nil
	})
}

// files.put / files.chunk (internal/agents/files.go).
type filePut struct {
	Agent  string `json:"agent"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type filePutResult struct {
	Upload string `json:"upload"`
	Chunk  int    `json:"chunk"`
}

type fileChunk struct {
	Upload string `json:"upload"`
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
	Last   bool   `json:"last"`
}

type fileChunkResult struct {
	Path string `json:"path"`
}

const maxAttachment = 50 << 20

// upload stores a file on the agent's machine and returns its path there.
func upload(ctx context.Context, c *wire.Client, id, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", usagef("%s is not a file (only files go to another machine)", path)
	}
	if st.Size() > maxAttachment {
		return "", usagef("%s is larger than 50 MiB", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	var put filePutResult
	p := filePut{Agent: id, Name: filepath.Base(path), Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}
	if err := c.Call(ctx, "files.put", p, &put); err != nil {
		return "", err
	}
	if put.Chunk <= 0 {
		put.Chunk = 512 * 1024
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	buf := make([]byte, put.Chunk)
	var off int64
	for {
		n, err := io.ReadFull(file, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return "", err
		}
		last := off+int64(n) >= st.Size()
		var res fileChunkResult
		if err := c.Call(ctx, "files.chunk", fileChunk{Upload: put.Upload, Offset: off, Data: buf[:n], Last: last}, &res); err != nil {
			return "", err
		}
		off += int64(n)
		if last {
			if res.Path == "" {
				return "", fmt.Errorf("files.chunk returned no path")
			}
			return res.Path, nil
		}
	}
}

// shellQuote writes a path the way a terminal pastes a dropped file
// (the app's ShellEscape.quote): backslash escapes, ANSI-C quoting for
// control characters.
func shellQuote(path string) string {
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			var b strings.Builder
			b.WriteString("$'")
			for _, r := range path {
				switch {
				case r == '\n':
					b.WriteString(`\n`)
				case r == '\t':
					b.WriteString(`\t`)
				case r == '\r':
					b.WriteString(`\r`)
				case r == '\\':
					b.WriteString(`\\`)
				case r == '\'':
					b.WriteString(`\'`)
				case r < 0x20 || r == 0x7f:
					fmt.Fprintf(&b, `\x%02x`, r)
				default:
					b.WriteRune(r)
				}
			}
			b.WriteString("'")
			return b.String()
		}
	}
	var b strings.Builder
	for _, r := range path {
		if strings.ContainsRune(" \t\\'\"`$!#&;|*?()[]{}<>", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// dropPastes are the pastes for paths (the app's DropPlan.pastes): one
// per PNG/JPEG image (agents attach them), then the other paths in one;
// every paste after the first starts with a space.
func dropPastes(paths []string) []string {
	var images, others, out []string
	for _, p := range paths {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".png", ".jpg", ".jpeg":
			images = append(images, shellQuote(p))
		default:
			others = append(others, shellQuote(p))
		}
	}
	out = append(out, images...)
	if len(others) > 0 {
		out = append(out, strings.Join(others, " "))
	}
	for i := 1; i < len(out); i++ {
		out[i] = " " + out[i]
	}
	return out
}

type attached struct {
	Path      string `json:"path"`
	AgentPath string `json:"agentPath"`
	Uploaded  bool   `json:"uploaded"`
}

func attachFileCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	forceUpload := f.Bool("upload", false, "Upload a copy even for an agent on this Mac")
	noPaste := f.Bool("no-paste", false, "Only store the files; paste nothing")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) < 2 {
			return usagef("usage: hesperctl attach-file ID PATH…")
		}
		id, err := resolveAgent(ctx, c, positional[0])
		if err != nil {
			return err
		}
		var hello wire.HelloResult
		if err := c.Call(ctx, "hello", wire.HelloParams{Client: "hesperctl", Version: wire.Version}, &hello); err != nil {
			return err
		}
		machine, _, _ := strings.Cut(id, "/")
		local := machine == hello.Machine
		var files []attached
		for _, p := range positional[1:] {
			abs, err := filepath.Abs(expandHome(p))
			if err != nil {
				return err
			}
			if _, err := os.Stat(abs); err != nil {
				return failf(wire.CodeNotFound, "%v", err)
			}
			a := attached{Path: abs, AgentPath: abs}
			if !local || *forceUpload {
				if a.AgentPath, err = upload(ctx, c, id, abs); err != nil {
					return fmt.Errorf("%s: %w", p, err)
				}
				a.Uploaded = true
			}
			files = append(files, a)
		}
		var paths []string
		for _, a := range files {
			paths = append(paths, a.AgentPath)
		}
		pastes := []string{}
		if !*noPaste {
			pastes = dropPastes(paths)
			for i, text := range pastes {
				if i > 0 {
					time.Sleep(150 * time.Millisecond) // one attachment at a time
				}
				if err := c.Call(ctx, "agents.input", wire.InputParams{ID: id, Text: text, Paste: true}, nil); err != nil {
					return err
				}
			}
		}
		if *asJSON {
			return output(map[string]any{"files": files, "pastes": pastes})
		}
		if *noPaste {
			for _, p := range paths {
				fmt.Println(p)
			}
			return nil
		}
		fmt.Println(strings.Join(pastes, ""))
		return nil
	})
}

func screenCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	rows := f.Int("rows", 0, "Only the last N rows with text: blank rows at the bottom dropped first (default all)")
	scrollback := f.Int("scrollback", 0, "First N lines of scrollback from above the screen")
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl screen ID [--rows N] [--scrollback N]")
		}
		if *rows < 0 || *scrollback < 0 || *scrollback > wire.MaxScreenScrollback {
			return usagef("--rows and --scrollback must be 0 to %d", wire.MaxScreenScrollback)
		}
		id, err := resolveAgent(ctx, c, positional[0])
		if err != nil {
			return err
		}
		var res wire.ScreenResult
		if err := c.Call(ctx, "agents.screen", wire.ScreenParams{ID: id, Scrollback: *scrollback}, &res); err != nil {
			return err
		}
		res.Text = lastRows(res.Text, *rows)
		if *asJSON {
			return output(res)
		}
		if text := strings.TrimRight(res.Text, "\n"); text != "" {
			fmt.Println(text)
		}
		return nil
	})
}

// lastRows is text's last n lines once the blank lines at its end are
// dropped (n 0: all of it, the blank end dropped too): screen --rows.
// hesperd's rows are the grid's last rows, blank below output that
// stays at the top; this is what the agent shows last.
func lastRows(text string, n int) string {
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// eventKinds are events --kinds' names: a notification's method prefix.
var eventKinds = []string{"agents", "drafts", "projects", "groups", "sessions"}

// noteAgent is the agent a notification is about ("" for others).
func noteAgent(n wire.Notification) string {
	switch n.Method {
	case wire.NoteChanged:
		var ch wire.Changed
		json.Unmarshal(n.Params, &ch)
		return ch.Agent.ID
	case wire.NoteRemoved:
		var r wire.Removed
		json.Unmarshal(n.Params, &r)
		return r.ID
	case wire.NoteMoving:
		var m wire.Moving
		json.Unmarshal(n.Params, &m)
		return m.ID
	}
	return ""
}

// subscribe opens a subscription on a connection of its own.
func subscribe(ctx context.Context, socket string) (*wire.Client, error) {
	c, err := dialDaemon(ctx, socket)
	if err != nil {
		return nil, err
	}
	if err := c.Call(ctx, "agents.subscribe", nil, nil); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

var errDaemonGone = &wire.Error{Code: wire.CodeUnavailable, Message: "hesperd closed the connection"}

func eventsCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	var agentRefs, kindList listFlag
	f.Var(&agentRefs, "agent", "Only agent `ID`'s events (repeatable)")
	f.Var(&kindList, "kinds", "Only these `KINDS`: "+strings.Join(eventKinds, ","))
	socket := daemonSocket(f)
	positional, err := parseInterspersed(f, args)
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return usagef("usage: hesperctl events [--agent ID]… [--kinds K,…]")
	}
	kinds := []string(kindList)
	for _, k := range kinds {
		if !contains(eventKinds, k) {
			return usagef("unknown kind %q: %s", k, strings.Join(eventKinds, ", "))
		}
	}
	var ids []string
	if refs := []string(agentRefs); len(refs) > 0 {
		c, err := dialDaemon(ctx, *socket)
		if err != nil {
			return err
		}
		ids, err = resolveAgents(ctx, c, refs)
		c.Close()
		if err != nil {
			return err
		}
	}
	c, err := subscribe(ctx, *socket)
	if err != nil {
		return err
	}
	defer c.Close()
	for {
		select {
		case <-ctx.Done():
			return nil
		case n, ok := <-c.Notifications():
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errDaemonGone
			}
			kind, _, _ := strings.Cut(n.Method, ".")
			if len(kinds) > 0 && !contains(kinds, kind) {
				continue
			}
			if len(ids) > 0 && !contains(ids, noteAgent(n)) {
				continue
			}
			line, _ := json.Marshal(struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}{n.Method, n.Params})
			if _, err := fmt.Printf("%s\n", line); err != nil {
				return nil // stdout closed (a pipe's reader quit)
			}
		}
	}
}

// waitStates parses wait's --until into the states that match.
func waitStates(until string) ([]string, error) {
	switch until {
	case "needs-you":
		return []string{wire.StateApproval, wire.StateQuestion}, nil
	case "finished":
		return []string{wire.StateDone, wire.StateIdle, wire.StateExited}, nil
	case "settled":
		return settledStates, nil
	case wire.StateDone, wire.StateIdle, wire.StateExited, wire.StateWorking:
		return []string{until}, nil
	}
	all := []string{wire.StateStarting, wire.StateWorking, wire.StateApproval, wire.StateQuestion, wire.StateDone, wire.StateIdle, wire.StateError, wire.StateExited}
	if list, ok := strings.CutPrefix(until, "state="); ok {
		var out []string
		for _, s := range strings.Split(list, ",") {
			if s = strings.TrimSpace(s); !contains(all, s) {
				return nil, usagef("unknown state %q: %s", s, strings.Join(all, ", "))
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, usagef("--until: settled, needs-you, done, idle, exited, working, finished or state=S[,S…]")
}

func waitCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	until := f.String("until", "", "settled, needs-you, done, idle, exited, working, finished or state=S[,S…]")
	anyOf := f.Bool("any", false, "Done when one agent matched")
	allOf := f.Bool("all", false, "Done when every agent matched (the default)")
	next := f.Bool("next", false, "Ignore the state each agent is in now")
	timeout := f.Duration("timeout", 0, "Give up after this long (exit 6; default never)")
	socket := daemonSocket(f)
	positional, err := parseInterspersed(f, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 || *until == "" {
		return usagef("usage: hesperctl wait ID… --until COND [--any|--all] [--next] [--timeout D]")
	}
	if *anyOf && *allOf {
		return usagef("--any or --all, not both")
	}
	states, err := waitStates(*until)
	if err != nil {
		return err
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	c, err := dialDaemon(ctx, *socket)
	if err != nil {
		return err
	}
	ids, err := resolveAgents(ctx, c, positional)
	if err != nil {
		c.Close()
		return err
	}
	// The states now (for --next) come from the list, after subscribing
	// (no change is lost between).
	sub, err := subscribe(ctx, *socket)
	if err != nil {
		c.Close()
		return err
	}
	defer sub.Close()
	var list []wire.Agent
	err = c.Call(ctx, "agents.list", nil, &list)
	c.Close()
	if err != nil {
		return err
	}
	now := map[string]wire.Agent{}
	for _, a := range list {
		now[a.ID] = a
	}
	initial := map[string]string{}
	for _, id := range ids {
		a, ok := now[id]
		if !ok {
			return failf(wire.CodeNotFound, "%s is gone", id)
		}
		initial[id] = a.State
	}
	left := map[string]bool{} // --next: left its first state
	matched := map[string]wire.Agent{}
	var order []string
	see := func(a wire.Agent) {
		if _, waited := initial[a.ID]; !waited {
			return
		}
		now[a.ID] = a
		if a.State != initial[a.ID] {
			left[a.ID] = true
		}
		if _, done := matched[a.ID]; done || !contains(states, a.State) || *next && !left[a.ID] {
			return
		}
		matched[a.ID] = a
		order = append(order, a.ID)
	}
	done := func() bool {
		if *anyOf {
			return len(matched) > 0
		}
		return len(matched) == len(initial)
	}
	for _, id := range ids {
		see(now[id])
	}
	for !done() {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				var waiting []string
				for _, id := range ids {
					if _, ok := matched[id]; !ok {
						waiting = append(waiting, id+" is "+now[id].State)
					}
				}
				return failf(codeTimeout, "timed out after %s: %s", *timeout, strings.Join(waiting, ", "))
			}
			return ctx.Err()
		case n, ok := <-sub.Notifications():
			if !ok {
				return errDaemonGone
			}
			switch n.Method {
			case wire.NoteChanged:
				var ch wire.Changed
				if json.Unmarshal(n.Params, &ch) == nil {
					see(ch.Agent)
				}
			case wire.NoteRemoved:
				var r wire.Removed
				if json.Unmarshal(n.Params, &r) != nil {
					continue
				}
				if _, waited := initial[r.ID]; waited {
					if _, ok := matched[r.ID]; !ok {
						why := r.Reason
						if why == "" {
							why = "removed"
						}
						return failf(wire.CodeNotFound, "%s is gone (%s)", r.ID, why)
					}
				}
			}
		}
	}
	out := make([]wire.Agent, 0, len(order))
	for _, id := range order {
		out = append(out, matched[id])
	}
	if *asJSON {
		return output(out)
	}
	for _, a := range out {
		fmt.Printf("%s\t%s\n", a.ID, a.State)
	}
	return nil
}

// agentDetail is show's --json: the agent and its choices.
type agentDetail struct {
	wire.Agent
	Choices []Choice `json:"choices,omitempty"`
}

func showCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	asJSON := jsonFlag(f)
	return withDaemon(ctx, f, args, func(ctx context.Context, c *wire.Client, positional []string) error {
		if len(positional) != 1 {
			return usagef("usage: hesperctl show ID")
		}
		id, err := resolveAgent(ctx, c, positional[0])
		if err != nil {
			return err
		}
		a, err := getAgent(ctx, c, id)
		if err != nil {
			return err
		}
		if *asJSON {
			return output(agentDetail{Agent: a, Choices: choices(a)})
		}
		return printAgent(os.Stdout, a, time.Now())
	})
}

func printAgent(w io.Writer, a wire.Agent, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "%s\t%s\n", k, strings.ReplaceAll(v, "\n", " "))
		}
	}
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format("2006-01-02 15:04:05") + " (" + now.Sub(t).Round(time.Second).String() + " ago)"
	}
	home, _ := os.UserHomeDir()
	tilde := func(p string) string {
		if home != "" && strings.HasPrefix(p, home+"/") {
			return "~" + p[len(home):]
		}
		return p
	}
	line("id", a.ID)
	line("name", a.Name)
	state := a.State
	if s := stamp(a.StateSince); s != "" {
		state += " since " + s
	}
	line("state", state)
	kind := a.Kind
	if a.Profile != "" && a.Profile != a.Kind {
		kind += " (profile " + a.Profile + ")"
	}
	line("kind", kind)
	line("machine", a.Machine)
	line("project", tilde(a.Project))
	line("projectId", a.ProjectID)
	line("worktree", tilde(a.Worktree))
	line("branch", a.Branch)
	line("task", a.Task)
	line("activity", a.Activity)
	line("summary", a.Summary)
	if at := a.Attention; at != nil {
		line("attention", strings.TrimSpace(at.Kind+": "+at.Title))
		if at.Kind == wire.StateQuestion {
			if q, _, ok := numberedOptions(at.Detail); ok && len(at.Options) == 0 {
				line("question", q)
			} else {
				line("detail", at.Detail)
			}
		} else {
			line("detail", at.Detail)
		}
		line("options", strings.Join(at.Options, ", "))
	}
	var list []string
	for _, ch := range choices(a) {
		list = append(list, fmt.Sprintf("%d. %s", ch.N, ch.Title))
	}
	line("choices", strings.Join(list, "  "))
	line("session", a.SessionID)
	if a.PID != 0 {
		line("pid", strconv.Itoa(a.PID))
	}
	if a.Size.Cols > 0 {
		line("size", fmt.Sprintf("%dx%d", a.Size.Cols, a.Size.Rows))
	}
	line("created", stamp(a.Created))
	if a.Exit != nil {
		switch {
		case a.Exit.Signal != "":
			line("exit", a.Exit.Signal)
		case a.Exit.Code != nil:
			line("exit", "status "+strconv.Itoa(*a.Exit.Code))
		}
	}
	if a.Background {
		line("background", "yes")
	}
	line("ended", a.Ended)
	return tw.Flush()
}
