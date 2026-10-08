package agents

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Codex specifics.
//
// Approvals: Codex's PermissionRequest hook is the source. Hooks only run
// once the user trusted them in Codex (/hooks), and they run late, in
// Codex's own app server. The documented fallback, isolated here: Codex
// rings the bell (a BEL outside any OSC string) when it asks; on a bell,
// and on output while no PermissionRequest hook has been seen for the
// agent, the daemon checks the bottom rows of the agent's own screen copy
// (bounded: at most every 250 ms, Codex agents only) for Codex's approval overlay.
// An approval found that way ends when the overlay is gone.
//
// Sessions: when no hook named the conversation, the rollout files in
// $CODEX_HOME/sessions started in the agent's directory since its launch
// do.

// bellScanner finds a BEL that is not an OSC terminator in output split
// across reads.
type bellScanner struct {
	inOSC  bool
	escEnd bool // the last byte was ESC
	oscLen int
}

const oscLimit = 64 << 10

func (b *bellScanner) feed(p []byte) bool {
	bell := false
	for _, c := range p {
		if b.inOSC {
			switch {
			case c == 0x07:
				b.inOSC = false
			case b.escEnd && c == '\\':
				b.inOSC = false
			default:
				b.oscLen++
				if b.oscLen > oscLimit {
					b.inOSC = false // never terminated: give up on it
				}
			}
			b.escEnd = c == 0x1b
			continue
		}
		switch {
		case b.escEnd && c == ']':
			b.inOSC, b.oscLen = true, 0
		case c == 0x07:
			bell = true
		}
		b.escEnd = c == 0x1b
	}
	return bell
}

// codexWatch is one Codex agent's fallback state.
type codexWatch struct {
	bells     bellScanner
	timer     *time.Timer
	lastCheck time.Time
	again     bool // output arrived while a check was pending
	// lastOutput: when the agent last wrote (a quiet screen settles).
	lastOutput time.Time
}

// Codex sends no hook after `codex resume` (nor on a fresh start without
// a prompt) until the next prompt, so such an agent would stay
// "starting". The rule: a starting Codex agent that expects no prompt is
// idle once its process is up and either its SessionStart hook arrives
// (if Codex sends one) or its screen shows Codex's composer ("› …" with
// the "? for shortcuts" / "context left" footer, nothing running, no
// first-run screen) and has been quiet for codexSettle.
var codexSettle = time.Second

var (
	codexComposerRow = regexp.MustCompile(`^\s*› `)
	codexFooter      = regexp.MustCompile(`(?i)\? for shortcuts|\d+% context left|ctrl\+j for newline`)
	codexBusy        = regexp.MustCompile(`(?i)esc to interrupt|^\s*• working`)
)

// codexComposer reports whether a screen shows Codex's idle composer.
func codexComposer(text string) bool {
	prompt, footer := false, false
	for _, row := range strings.Split(text, "\n") {
		if codexBusy.MatchString(row) {
			return false
		}
		prompt = prompt || codexComposerRow.MatchString(row)
		footer = footer || codexFooter.MatchString(row)
	}
	return prompt && footer
}

const (
	codexCheckEvery = 250 * time.Millisecond
	codexBellDelay  = 120 * time.Millisecond // the overlay draws after the bell
	codexRows       = 200                    // the whole screen of any sane size
)

var codexAsk = regexp.MustCompile(`(?i)would you like to (run the following command|make the following edits|apply|grant|allow)|allow command\?`)
var codexChoice = regexp.MustCompile(`(?i)yes, proceed|don't ask again|tell codex what to do differently|press enter to confirm`)

// codexPrompt looks for Codex's approval overlay in the rows of a screen:
// a question and its choices. title is what is asked for, detail the
// command or file.
func codexPrompt(rows []string) (found bool, title, detail string) {
	ask := -1
	choices := false
	for i, row := range rows {
		if ask < 0 && codexAsk.MatchString(row) {
			ask = i
		}
		if codexChoice.MatchString(row) {
			choices = true
		}
	}
	if ask < 0 || !choices {
		return false, "", ""
	}
	title = "Command"
	if strings.Contains(strings.ToLower(rows[ask]), "edit") {
		title = "Edit"
	}
	for _, row := range rows[ask+1:] {
		line := strings.TrimSpace(strings.Trim(strings.TrimSpace(row), "│┃|"))
		if line == "" || codexChoice.MatchString(line) {
			continue
		}
		detail = firstLine(strings.TrimPrefix(line, "$ "), 300)
		break
	}
	return true, title, detail
}

// screenRows is the bottom n rows of the screen as text.
func screenRows(s *vt.Screen, n int) []string {
	_, h := s.Size()
	from := max(h-n, 0)
	rows := make([]string, 0, h-from)
	for y := from; y < h; y++ {
		rows = append(rows, s.Text(y))
	}
	return rows
}

// codexOutput runs on the agent's output (its reading goroutine).
func (r *Registry) codexOutput(local string, gen int, p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.agents[local]
	if a == nil || a.gen != gen || a.watch == nil {
		return
	}
	w := a.watch
	w.lastOutput = time.Now()
	bell := w.bells.feed(p)
	if !bell && !a.fromScreen && a.codexHooks {
		return
	}
	if w.timer != nil {
		w.again = true
		return
	}
	delay := max(codexCheckEvery-time.Since(w.lastCheck), 0)
	if bell {
		delay = max(delay, codexBellDelay)
	}
	w.timer = time.AfterFunc(delay, func() { r.codexCheck(local, gen) })
}

func (r *Registry) codexCheck(local string, gen int) {
	r.mu.Lock()
	a := r.agents[local]
	if a == nil || a.gen != gen || a.term == nil {
		r.mu.Unlock()
		return
	}
	term := a.term
	r.mu.Unlock()
	var rows []string
	term.WithScreen(func(s *vt.Screen) { rows = screenRows(s, codexRows) })
	found, title, detail := codexPrompt(rows)
	r.mu.Lock()
	defer r.mu.Unlock()
	if a.gen != gen {
		return
	}
	w := a.watch
	w.timer, w.lastCheck = nil, time.Now()
	if w.again {
		// What arrived meanwhile gets its own look.
		w.again = false
		w.timer = time.AfterFunc(codexCheckEvery, func() { r.codexCheck(local, gen) })
	}
	switch {
	case found && a.State != wire.StateApproval && a.Exit == nil:
		r.setState(a, wire.StateApproval, &wire.Attention{Kind: "approval", Title: title, Detail: detail, Options: approvalOptions})
		a.fromScreen = true
	case !found && a.fromScreen && a.State == wire.StateApproval:
		r.setState(a, wire.StateWorking, nil)
	}
}

var rolloutName = regexp.MustCompile(`^rollout-(\d{4}-\d\d-\d\dT\d\d-\d\d-\d\d)-(.+)\.jsonl$`)

// codexSessionSince is the first Codex conversation started in dir since
// started (rollout names carry the local start time), skipping claimed ids.
func codexSessionSince(home, dir string, started time.Time, claimed map[string]bool) string {
	if home == "" || dir == "" || started.IsZero() {
		return ""
	}
	type cand struct {
		at   time.Time
		path string
	}
	var cands []cand
	days, _ := filepath.Glob(filepath.Join(home, "sessions", "[0-9]*", "[0-9]*", "[0-9]*"))
	for _, day := range days {
		parts := strings.Split(filepath.ToSlash(day), "/")
		d, err := time.ParseInLocation("2006/01/02", strings.Join(parts[len(parts)-3:], "/"), time.Local)
		if err != nil || d.Add(48*time.Hour).Before(started) {
			continue
		}
		files, _ := os.ReadDir(day)
		for _, f := range files {
			m := rolloutName.FindStringSubmatch(f.Name())
			if m == nil {
				continue
			}
			at, err := time.ParseInLocation("2006-01-02T15-04-05", m[1], time.Local)
			if err == nil && !at.Before(started.Add(-5*time.Second)) {
				cands = append(cands, cand{at, filepath.Join(day, f.Name())})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].at.Before(cands[j].at) })
	for i, c := range cands {
		if i >= 200 {
			break
		}
		id, cwd := rolloutMeta(c.path)
		if id != "" && cwd != "" && sameDir(cwd, dir) && sessionIDRe.MatchString(id) && !claimed[id] {
			return id
		}
	}
	return ""
}

// rolloutMeta is the session id and directory on a rollout's first line.
func rolloutMeta(path string) (id, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	line, _ := bufio.NewReaderSize(f, 64<<10).ReadBytes('\n')
	var rec struct {
		Payload map[string]any `json:"payload"`
	}
	if json.Unmarshal(bytes.TrimSpace(line), &rec) != nil || rec.Payload == nil {
		return "", ""
	}
	return str(rec.Payload, "id", "session_id"), str(rec.Payload, "cwd")
}

// adoptCodexSession gives a Codex agent without a session id the one its
// rollout files name (the registry's lock is held).
func (r *Registry) adoptCodexSession(a *agent) {
	if a.Kind != wire.KindCodex || a.SessionID != "" {
		return
	}
	claimed := map[string]bool{}
	for _, b := range r.agents {
		if b.SessionID != "" {
			claimed[b.SessionID] = true
		}
	}
	if id := codexSessionSince(r.opt.CodexHome, a.Dir(), a.started, claimed); id != "" {
		a.SessionID = id
	}
}
