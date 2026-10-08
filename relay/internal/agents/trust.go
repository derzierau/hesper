package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/vt"
)

// Claude Code drops its first prompt when it asks to trust the folder
// first. The daemon reads (never writes) Claude's trust records: in an
// untrusted folder the task is not passed on the command line but typed in
// once Claude is past its question (as the former bin/ghosty-handoff deliver did).

// claudeTrusted reports whether Claude Code trusts dir: the main
// worktree's root, the directory or a parent with hasTrustDialogAccepted.
func claudeTrusted(config, dir string, g git) bool {
	data, err := os.ReadFile(config)
	if err != nil {
		return false
	}
	var c struct {
		Projects map[string]struct {
			Accepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if json.Unmarshal(data, &c) != nil {
		return false
	}
	accepted := func(p string) bool { return p != "" && c.Projects[p].Accepted }
	candidates := func(p string) []string {
		out := []string{p}
		if real, err := filepath.EvalSymlinks(p); err == nil && real != p {
			out = append(out, real)
		}
		return out
	}
	if root := g.mainRoot(dir); root != "" {
		for _, p := range candidates(root) {
			if accepted(p) {
				return true
			}
		}
	}
	for _, p := range candidates(dir) {
		for {
			if accepted(p) {
				return true
			}
			parent := filepath.Dir(p)
			if parent == p {
				break
			}
			p = parent
		}
	}
	return false
}

// asking is Claude's trust question, or any other choice it shows first (a
// numbered list with its cursor, "Enter to confirm"): never type into those.
var asking = regexp.MustCompile(`(?im)trust this folder|trust the files|enter to confirm|^\s*[❯>›]\s*\d+\.`)

// deliverTimeout bounds how long a task waits to be typed in.
var deliverTimeout = 30 * time.Minute

// deliverLater types the agent's pending task in once its screen shows no
// question and stayed the same for a poll (it has drawn its input). It
// runs until then, the timeout, or the agent's end.
func (r *Registry) deliverLater(local string, gen int) {
	deadline := time.Now().Add(deliverTimeout)
	var last string
	seenAsk := false
	for time.Now().Before(deadline) {
		time.Sleep(r.opt.DeliverPoll)
		r.mu.Lock()
		a := r.agents[local]
		if a == nil || a.gen != gen || a.term == nil || a.Exit != nil || a.pendingTask == "" {
			r.mu.Unlock()
			return
		}
		term, dir := a.term, a.Dir()
		r.mu.Unlock()
		var text string
		var paste bool
		term.WithScreen(func(s *vt.Screen) {
			text = strings.Join(screenRows(s, 1000), "\n")
			paste = s.Modes().BracketedPaste
		})
		if asking.MatchString(text) {
			seenAsk, last = true, ""
			continue
		}
		if strings.TrimSpace(text) == "" || !seenAsk && !claudeTrusted(r.opt.ClaudeConfig, dir, r.g) {
			last = ""
			continue
		}
		if text != last {
			last = text
			continue
		}
		r.mu.Lock()
		if a.gen != gen || a.pendingTask == "" {
			r.mu.Unlock()
			return
		}
		task := a.pendingTask
		a.pendingTask = ""
		a.expectPrompt = true
		r.scheduleSave()
		r.mu.Unlock()
		body := task
		if paste {
			body = "\x1b[200~" + task + "\x1b[201~"
		}
		term.Input([]byte(body))
		time.Sleep(300 * time.Millisecond)
		term.Input([]byte("\r"))
		return
	}
}
