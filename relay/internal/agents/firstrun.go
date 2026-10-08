package agents

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// First-run screens: before its first hook an agent may stop at a screen
// of its own: the trust question for the folder (when pre-trust is off or
// could not be recorded), or Claude Code's / Codex's first-run setup
// (theme, login, an API key in the environment). No hook reports those,
// so while an agent is starting the daemon looks at its screen (every
// DeliverPoll) and raises what it finds as attention instead of leaving
// the agent "starting" with nobody knowing why:
//
//	trust question   question "Trust folder", detail "Trust <folder>?",
//	                 options [trust, exit] (agents.answer: trust selects
//	                 the agent's "yes" choice, exit stops the agent)
//	setup screens    question with a title and what to do (open the
//	                 agent), no options
//
// When the screen is gone the agent is "starting" again until its hooks
// say more. Screen text only ever raises these; the state still comes
// from hooks.

type firstRunScreen struct {
	id     string
	match  *regexp.Regexp
	title  string
	detail string // %s: the folder
}

var firstRunScreens = map[string][]firstRunScreen{
	wire.KindClaude: {
		// Claude Code 2.1: "Quick safety check: Is this a project you
		// created or one you trust? …  ❯ 1. Yes, I trust this folder
		// 2. No, exit"
		{"trust", regexp.MustCompile(`(?i)quick safety check|yes, i trust this folder|do you trust the files in this folder`), "Trust folder", "Trust %s?"},
		{"login", regexp.MustCompile(`(?i)select login method|browser didn't open|paste code here if prompted`), "Log in", "Claude Code needs you to log in: open the agent to finish it."},
		{"apikey", regexp.MustCompile(`(?i)detected a custom api key in your environment`), "API key", "Claude Code asks whether to use the API key from the environment: open the agent to answer."},
		{"theme", regexp.MustCompile(`(?i)choose the text style that looks best with your terminal`), "Claude setup", "Claude Code's first-run setup (theme) is waiting: open the agent to finish it."},
		{"setup", regexp.MustCompile(`(?i)^\s*security notes:|use claude code's terminal setup`), "Claude setup", "Claude Code's first-run setup is waiting: open the agent to finish it."},
	},
	wire.KindCodex: {
		// Codex 0.160 at start when a newer release exists: "Update
		// available · 0.160.0 → 0.160.1 … › 1. Update now (runs `brew
		// upgrade --cask codex`) 2. Skip 3. Skip until next version …
		// enter continue · esc skip" (not the "✨ Update available!" box
		// it leaves in the history after a skip).
		{"update", regexp.MustCompile(`(?is)update now.*skip until next version`), "Codex update available", "Update Codex now (%s) or skip it and go on."},
		{"trust", regexp.MustCompile(`(?i)trust this folder\?|do you trust the (files|contents) (in|of) this (folder|directory)|allow codex to work in this folder`), "Trust folder", "Trust %s?"},
		{"login", regexp.MustCompile(`(?i)sign in with chatgpt|provide your own api key`), "Log in", "Codex needs you to sign in: open the agent to finish it."},
	},
}

// findFirstRun is the first-run screen text shows, if any.
func findFirstRun(kind, text string) *firstRunScreen {
	for i, s := range firstRunScreens[kind] {
		if s.match.MatchString(text) {
			return &firstRunScreens[kind][i]
		}
	}
	return nil
}

func (s *firstRunScreen) attention(dir, home, text string) *wire.Attention {
	att := &wire.Attention{Kind: "question", Title: s.title, Detail: s.detail}
	switch s.id {
	case "trust":
		att.Detail = strings.Replace(s.detail, "%s", tildePath(dir, home), 1)
		att.Options = []string{wire.Trust, wire.Leave}
	case "update":
		if m := codexUpdateVersions.FindStringSubmatch(text); m != nil {
			att.Title += " (" + m[1] + " → " + m[2] + ")"
		}
		how := "Update now"
		if m := codexUpdateHow.FindStringSubmatch(text); m != nil {
			how = "runs " + m[1]
		}
		att.Detail = strings.Replace(s.detail, "%s", how, 1)
		att.Options = []string{wire.Skip, wire.Update}
	}
	return att
}

var (
	codexUpdateVersions = regexp.MustCompile(`(?i)update available\W+v?(\d[\w.+-]*)\s*(?:→|->)\s*v?(\d[\w.+-]*)`)
	codexUpdateHow      = regexp.MustCompile("(?i)update now \\(runs `([^`]+)`\\)")
)

// tildePath shows a path below home as ~/….
func tildePath(p, home string) string {
	if home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return p
}

// screenText is the agent's whole screen as text.
func screenText(t interface{ WithScreen(func(*vt.Screen)) }) string {
	var text string
	t.WithScreen(func(s *vt.Screen) { text = strings.Join(screenRows(s, 1000), "\n") })
	return text
}

// watchFirstRun looks at a starting agent's screen until hooks take over,
// the agent ends, or deliverTimeout.
func (r *Registry) watchFirstRun(local string, gen int) {
	deadline := time.Now().Add(deliverTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(r.opt.DeliverPoll)
		r.mu.Lock()
		a := r.agents[local]
		if a == nil || a.gen != gen || a.term == nil || a.Exit != nil {
			r.mu.Unlock()
			return
		}
		if a.State != wire.StateStarting && a.screenAsk == "" {
			r.mu.Unlock()
			return // the hooks have it
		}
		term, kind, dir := a.term, a.Kind, a.Dir()
		r.mu.Unlock()
		text := screenText(term)
		found := findFirstRun(kind, text)
		r.mu.Lock()
		if a.gen != gen || a.Exit != nil {
			r.mu.Unlock()
			return
		}
		switch {
		case found != nil && found.id == "update" && r.settings.codexUpdatePrompt() == CodexUpdateSkip:
			// settings.json codexUpdatePrompt "skip": Esc, as "esc skip"
			// says (the -c check_for_update_on_startup=false the agent
			// got should keep the screen away in the first place).
			if time.Since(a.skippedAt) > 2*time.Second {
				a.skippedAt = time.Now()
				term.Input([]byte{0x1b})
			}
		case found != nil && found.id != a.screenAsk && (a.State == wire.StateStarting || a.screenAsk != ""):
			r.setState(a, wire.StateQuestion, found.attention(dir, r.opt.Home, text))
			a.screenAsk = found.id
		case found == nil && a.screenAsk != "":
			a.screenAsk = ""
			r.setState(a, wire.StateStarting, nil)
		case found == nil && kind == wire.KindCodex && a.State == wire.StateStarting && !a.expectPrompt && a.pendingTask == "" &&
			a.watch != nil && time.Since(a.watch.lastOutput) >= codexSettle && codexComposer(text):
			// Codex sends no hook after a resume (codex.go): its quiet
			// composer says it is up and waiting.
			r.setState(a, wire.StateIdle, nil)
		}
		r.mu.Unlock()
	}
}

// answerFirstRun answers a first-run screen's question (the lock is held).
func (r *Registry) answerFirstRun(a *agent, decision string) error {
	if a.Attention == nil || !contains(a.Attention.Options, decision) {
		return wire.Errorf(wire.CodeInvalid, "%s asks something else: open the agent", a.ID)
	}
	switch decision {
	case wire.Leave:
		a.stopping, a.running = true, false
		a.term.Stop(r.opt.StopGrace)
		r.scheduleSave()
		return nil
	case wire.Trust, wire.Update:
		// The agent's first choice is "yes" / "Update now": its number
		// picks it; Enter after it when the screen is still there (a list
		// that only moves the cursor on a digit).
		term, kind, id := a.term, a.Kind, a.screenAsk
		if err := term.Input([]byte("1")); err != nil {
			return wire.Errorf(wire.CodeInvalid, "%v", err)
		}
		go func() {
			time.Sleep(400 * time.Millisecond)
			if s := findFirstRun(kind, screenText(term)); s != nil && s.id == id {
				term.Input([]byte("\r"))
			}
		}()
		a.screenAsk, a.answered = "", true
		r.setState(a, wire.StateStarting, nil)
		return nil
	case wire.Skip:
		// Codex's update screen: Esc, as it says ("esc skip"), whatever
		// its cursor is on.
		if err := a.term.Input([]byte{0x1b}); err != nil {
			return wire.Errorf(wire.CodeInvalid, "%v", err)
		}
		a.screenAsk = ""
		r.setState(a, wire.StateStarting, nil)
		return nil
	}
	return wire.Errorf(wire.CodeInvalid, "decision must be one of %s", strings.Join(a.Attention.Options, ", "))
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// lastLines is the last n non-blank lines of an agent's screen, joined
// with " · " (why it ended, for an error's detail).
func lastLines(t interface{ WithScreen(func(*vt.Screen)) }, n int) string {
	rows := strings.Split(screenText(t), "\n")
	var out []string
	for i := len(rows) - 1; i >= 0 && len(out) < n; i-- {
		if line := strings.TrimSpace(rows[i]); line != "" {
			out = append([]string{line}, out...)
		}
	}
	return strings.Join(out, " · ")
}

// Sessions that exist: a resume of a session the agent never saved fails
// at once (claude --resume of an unknown id exits 1), so the daemon
// resumes only a session a hook confirmed (sessionSeen) or whose
// transcript (Claude: <ClaudeHome>/projects/*/<id>.jsonl) or rollout
// (Codex: <CodexHome>/sessions/*/*/*/rollout-*-<id>.jsonl) is on disk;
// otherwise it starts the agent fresh (Claude with the same --session-id)
// and delivers its task again.
func (r *Registry) resumable(a *agent) bool {
	if a.SessionID == "" || !sessionIDRe.MatchString(a.SessionID) {
		return false
	}
	if a.sessionSeen {
		return true
	}
	var pattern string
	switch a.Kind {
	case wire.KindClaude:
		pattern = filepath.Join(r.opt.ClaudeHome, "projects", "*", a.SessionID+".jsonl")
	case wire.KindCodex:
		pattern = filepath.Join(r.opt.CodexHome, "sessions", "*", "*", "*", "rollout-*-"+a.SessionID+".jsonl")
	default:
		return false
	}
	matches, _ := filepath.Glob(pattern)
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.Size() > 0 {
			return true
		}
	}
	return false
}

// confirmsSession: hook events that only come from a conversation that
// has been saved (a prompt, a tool, a turn's end; SessionStart of an
// existing one). A fresh SessionStart does not: Claude Code writes the
// transcript with the first message.
func confirmsSession(event string, data map[string]any) bool {
	switch event {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Stop", "notify", "PreCompact":
		return true
	case "SessionStart":
		src := str(data, "source")
		return src == "resume" || src == "compact" || src == "clear"
	}
	return false
}
