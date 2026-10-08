package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Shared history (TEST-ONLY imitation of the sessions.* wire the app is
// built against, docs/rebuild-contract.md "As built — shared history
// (app)"): sessions.search/show/stats/resume/fork/brief/continueAs/
// archive/delete/restore and sessions.changed / sessions.indexing on
// agents.subscribe. Seeded with --sessions N; --no-sessions answers
// -32601 like an older hesperd. Every sessions.* call is logged for the
// UI test (fake.sessionCalls). Nothing here reads real transcripts.

type sessTodo struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

type sessLive struct {
	AgentID  string `json:"agentId,omitempty"`
	External bool   `json:"external"`
}

type Session struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	SessionID     string     `json:"sessionId"`
	Machine       string     `json:"machine"`
	Cwd           string     `json:"cwd"`
	ProjectID     string     `json:"projectId"`
	Branch        string     `json:"branch"`
	Title         string     `json:"title"`
	FirstPrompt   string     `json:"firstPrompt"`
	LastUser      string     `json:"lastUser"`
	LastAssistant string     `json:"lastAssistant"`
	Todos         []sessTodo `json:"todos"`
	Turns         int        `json:"turns"`
	Tokens        int        `json:"tokens,omitempty"`
	StartedAt     string     `json:"startedAt"`
	LastActivity  string     `json:"lastActivity"`
	Live          *sessLive  `json:"live,omitempty"`
	External      bool       `json:"external"`
	Archived      bool       `json:"archived"`
	Mirrored      []string   `json:"mirrored"`
	Snippet       string     `json:"snippet,omitempty"`
	RemovedAt     string     `json:"removedAt,omitempty"`
	MovedTo       string     `json:"movedTo,omitempty"`
	Origin        string     `json:"origin,omitempty"`
	Bytes         int64      `json:"bytes,omitempty"`

	last    time.Time // lastActivity, for sorting
	deleted time.Time // in the 30 s trash
}

type sessionStore struct {
	byID     map[string]*Session
	order    []string // insertion
	calls    []map[string]any
	indexing [2]int // done, total (total 0: not indexing)
	disabled bool
}

var sessionTitles = []string{
	"Fix the flaky badge test on the rail", "Badge counts double after a move", "Rail badges and switcher", "Push provider FCM migration",
	"Edition picker on the home screen", "Relay latency spikes on the mini", "Draft PR copy for the release", "iOS widgets refresh budget",
	"Auth token renewal race", "VT screen copy performance", "Debounce the ticker", "Scrollback for view attaches",
	"Worktree cleanup after merge", "Codex rollout indexing", "Project sidebar drag and drop", "Treemap weights for attention",
}

var sessionAnswers = []string{
	"20 runs, all green. The race was in the ticker's debounce; fixed in rail.py.",
	"I'd need the fleet.json from the mini to confirm.",
	"Committed as 911d3c7.",
	"Tests pass locally; CI is still running on the PR.",
	"The picker now remembers the last edition per account.",
	"p99 dropped from 180 ms to 22 ms after batching the writes.",
}

func (d *daemon) sessionsEnabled() bool { return d.sess != nil && !d.sess.disabled }

func hash32(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// seedSessions: n sessions over the demo projects (or one folder), both
// kinds, both Macs, two months; plus one live session per seeded agent.
func (d *daemon) seedSessions(n int, demo bool) {
	r := rand.New(rand.NewSource(42))
	type seat struct{ machine, cwd, project, branch string }
	seats := []seat{{"L", "/tmp/fake/project", "", ""}}
	if demo {
		seats = []seat{
			{"M", "/Users/mini/projects/hesper", "p-hesper", "fix/flaky-badge"},
			{"L", fakeRoot + "/hesper", "p-hesper", "fleet-model"},
			{"L", fakeRoot + "/acme-apps", "p-acme-apps", "feature/push-provider-fcm"},
			{"M", "/Users/mini/projects/acme-apps", "p-acme-apps", "main"},
			{"L", fakeRoot + "/acme-apps/apps/ios-app", "p-ios-app", "widgets"},
			{"L", fakeRoot + "/design-system", "p-edition", "picker"},
			{"L", "/tmp/fake/scratch/hesper-trial", "p-trial", ""},
			{"L", "/tmp/fake/scratch/spike-vt", "scratch:/tmp/fake/scratch/spike-vt", ""},
			{"L", "/tmp/fake/gone/old-experiment", "", "old"},
		}
	}
	base := time.Now().Add(-5 * time.Minute)
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := 0; i < n; i++ {
		st := seats[i%len(seats)]
		kind := "claude"
		if i%2 == 0 {
			kind = "codex"
		}
		title := sessionTitles[i%len(sessionTitles)]
		if i >= len(sessionTitles) {
			title = fmt.Sprintf("%s (%d)", title, i/len(sessionTitles)+1)
		}
		last := base.Add(-time.Duration(i) * time.Duration(20+r.Intn(40)) * time.Minute)
		s := &Session{
			ID: fmt.Sprintf("s-%05d", i), Kind: kind, SessionID: fmt.Sprintf("%08x-fake-%04d", hash32(title), i), Machine: st.machine, Cwd: st.cwd,
			ProjectID: st.project, Branch: st.branch, Title: title,
			FirstPrompt:   title + ": " + "look at the failing case first, then make it pass 20 times in a row",
			LastUser:      "make it pass 20 times in a row, then stop",
			LastAssistant: sessionAnswers[i%len(sessionAnswers)],
			Todos:         []sessTodo{{"open a PR", false}, {"remove the debug print", i%3 == 0}, {"run the full suite", true}},
			Turns:         5 + r.Intn(90), Tokens: 10_000 + r.Intn(400_000),
			StartedAt: last.Add(-3 * time.Hour).UTC().Format(time.RFC3339), LastActivity: last.UTC().Format(time.RFC3339), last: last,
			External: i%13 == 5, Archived: i%17 == 9, Mirrored: []string{},
		}
		if st.project == "" && !demo {
			s.ProjectID = "scratch:" + st.cwd
		}
		if i%7 == 3 {
			if st.machine == "L" {
				s.Mirrored = []string{"M"}
			} else {
				s.Mirrored = []string{"L"}
			}
		}
		if i%29 == 11 {
			s.Live = &sessLive{External: true}
		}
		if i%31 == 7 {
			s.MovedTo = "L" // continued on the laptop: read-only history here
		}
		s.Origin = "cli"
		if kind == "codex" {
			s.Origin = "codex-tui"
		}
		s.Bytes = int64(40_000 + (i%97)*9_000)
		d.addSessionLocked(s)
	}
	for _, id := range d.order {
		d.sessionForAgentLocked(d.agents[id].a)
	}
}

func (d *daemon) addSessionLocked(s *Session) {
	if _, ok := d.sess.byID[s.ID]; !ok {
		d.sess.order = append(d.sess.order, s.ID)
	}
	d.sess.byID[s.ID] = s
}

// sessionForAgentLocked: every agent has a session (live while it is on
// the wall).
func (d *daemon) sessionForAgentLocked(a *Agent) *Session {
	if d.sess == nil || a.SessionID == "" {
		return nil
	}
	for _, s := range d.sess.byID {
		if s.SessionID == a.SessionID {
			s.Live = &sessLive{AgentID: a.ID}
			s.RemovedAt = ""
			s.Machine = a.Machine
			if a.State == "starting" { // resumed now
				s.last = time.Now()
				s.LastActivity = s.last.UTC().Format(time.RFC3339)
			}
			return s
		}
	}
	t := time.Now()
	s := &Session{
		ID: "s-" + a.SessionID, Kind: a.Kind, SessionID: a.SessionID, Machine: a.Machine, Cwd: a.Project, ProjectID: a.ProjectID, Branch: a.Branch,
		Title: a.Name, FirstPrompt: a.Task, LastUser: a.Task, LastAssistant: "Working on it.", Todos: []sessTodo{}, Turns: 3, Tokens: 12_000,
		StartedAt: t.UTC().Format(time.RFC3339), LastActivity: t.UTC().Format(time.RFC3339), last: t, Mirrored: []string{},
		Live: &sessLive{AgentID: a.ID},
	}
	if a.Kind == "shell" {
		return nil
	}
	d.addSessionLocked(s)
	return s
}

// liveLocked: a session's live state as of now (its agent still there).
func (d *daemon) liveLocked(s *Session) *sessLive {
	if s.Live == nil {
		return nil
	}
	if s.Live.AgentID != "" {
		if _, ok := d.agents[s.Live.AgentID]; !ok {
			return nil
		}
	}
	return s.Live
}

func (d *daemon) sessionJSONLocked(s *Session, snippet string) map[string]any {
	c := *s
	c.Live = d.liveLocked(s)
	c.Snippet = snippet
	b, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (d *daemon) sessionChangedLocked(s *Session) {
	d.broadcastLocked("sessions.changed", map[string]any{"session": d.sessionJSONLocked(s, "")})
}

// agentRemovedSession: a removed agent's session stays in History with
// removedAt (ghost cards).
func (d *daemon) agentRemovedSession(a *Agent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.sessionsEnabled() {
		return
	}
	s := d.sessionForAgentLocked(a)
	if s == nil {
		return
	}
	s.Live = nil
	s.RemovedAt = time.Now().UTC().Format(time.RFC3339Nano)
	d.sessionChangedLocked(s)
}

func (d *daemon) agentAddedSession(a *Agent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.sessionsEnabled() {
		return
	}
	if s := d.sessionForAgentLocked(a); s != nil {
		d.sessionChangedLocked(s)
	}
}

func snippetFor(s *Session, words []string) (string, bool) {
	fields := []string{s.Title, s.FirstPrompt, s.LastUser, s.LastAssistant, s.Branch, s.Cwd}
	all := strings.ToLower(strings.Join(fields, "\n"))
	for _, w := range words {
		if !strings.Contains(all, w) {
			return "", false
		}
	}
	// The snippet: the first answer/prompt field holding the first word.
	for _, f := range []string{s.LastAssistant, s.LastUser, s.FirstPrompt, s.Title} {
		lf := strings.ToLower(f)
		i := strings.Index(lf, words[0])
		if i < 0 {
			continue
		}
		start := max(0, i-40)
		end := min(len(f), i+len(words[0])+60)
		out := f[start:i] + "[" + f[i:i+len(words[0])] + "]" + f[i+len(words[0]):end]
		if start > 0 {
			out = "…" + out
		}
		if end < len(f) {
			out += "…"
		}
		return out, true
	}
	return "", true
}

func (d *daemon) sessionsCall(method string, raw json.RawMessage) (any, *rpcError) {
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	str := func(k string) string { s, _ := p[k].(string); return s }
	d.mu.Lock()
	if !d.sessionsEnabled() {
		d.mu.Unlock()
		return nil, &rpcError{Code: -32601, Message: "method not found: " + method, Data: map[string]any{"code": "not_found"}}
	}
	d.sess.calls = append(d.sess.calls, map[string]any{"method": method, "params": p})
	get := func() (*Session, *rpcError) {
		s, ok := d.sess.byID[str("id")]
		undo, _ := p["undo"].(bool)
		if !ok || !s.deleted.IsZero() && !(method == "sessions.delete" && undo) {
			return nil, errCode("not_found", "no session "+str("id"))
		}
		return s, nil
	}
	switch method {
	case "sessions.search":
		defer d.mu.Unlock()
		words := strings.Fields(strings.ToLower(str("query")))
		strs := func(k string) map[string]bool {
			out := map[string]bool{}
			if a, ok := p[k].([]any); ok {
				for _, x := range a {
					if s, ok := x.(string); ok {
						out[s] = true
					}
				}
			}
			return out
		}
		kinds, machines := strs("kinds"), strs("machines")
		var since time.Time
		if s := str("since"); s != "" {
			since, _ = time.Parse(time.RFC3339Nano, s)
		}
		live, _ := p["live"].(bool)
		external, _ := p["external"].(bool)
		archived, _ := p["archived"].(bool)
		moved, _ := p["moved"].(bool)
		pid := str("projectId")
		type hit struct {
			s   *Session
			snp string
		}
		var hits []hit
		for _, id := range d.sess.order {
			s := d.sess.byID[id]
			if !s.deleted.IsZero() || s.Archived != archived || s.MovedTo != "" && !moved {
				continue
			}
			switch {
			case pid == "~scratch":
				if !strings.HasPrefix(s.ProjectID, "scratch:") {
					continue
				}
			case pid == "~elsewhere":
				if s.ProjectID != "" {
					continue
				}
			case pid != "":
				if s.ProjectID != pid && !(d.projects[s.ProjectID] != nil && d.projects[s.ProjectID].ParentID == pid) {
					continue
				}
			}
			if len(kinds) > 0 && !kinds[s.Kind] || len(machines) > 0 && !machines[s.Machine] {
				continue
			}
			if !since.IsZero() && s.last.Before(since) {
				continue
			}
			l := d.liveLocked(s)
			if live && (l == nil || l.AgentID == "") || external && !s.External {
				continue
			}
			snp := ""
			if len(words) > 0 {
				var ok bool
				if snp, ok = snippetFor(s, words); !ok {
					continue
				}
			}
			hits = append(hits, hit{s, snp})
		}
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].s.last.After(hits[j].s.last) })
		off, _ := strconv.Atoi(str("cursor"))
		limit := 50 // hesperd: default and maximum
		if l, ok := p["limit"].(float64); ok && l > 0 && l < 50 {
			limit = int(l)
		}
		items := []map[string]any{}
		for i := off; i < len(hits) && i < off+limit; i++ {
			items = append(items, d.sessionJSONLocked(hits[i].s, hits[i].snp))
		}
		res := map[string]any{"items": items}
		if off+limit < len(hits) {
			res["cursor"] = strconv.Itoa(off + limit)
		}
		return res, nil
	case "sessions.show":
		defer d.mu.Unlock()
		s, e := get()
		if e != nil {
			return nil, e
		}
		m := d.sessionJSONLocked(s, "")
		h := hash32(s.ID)
		files := []map[string]any{{"path": "rail.py", "added": 14, "removed": 6}, {"path": "tests/test_rail.py", "added": 31, "removed": 0}}
		if h%3 == 1 {
			files = append(files, map[string]any{"path": "relay/internal/vt/screen.go", "added": 120, "removed": 44})
		}
		m["changes"] = map[string]any{"files": files, "uncommitted": h%2 == 0, "ahead": 2, "behind": int(h % 2), "base": "main",
			"worktreeExists": s.ProjectID != ""}
		return m, nil
	case "sessions.stats":
		defer d.mu.Unlock()
		byProject, byMachine, byKind := map[string]int{}, map[string]int{}, map[string]int{}
		total, scratch, elsewhere := 0, 0, 0
		for _, s := range d.sess.byID {
			if !s.deleted.IsZero() || s.Archived {
				continue
			}
			total++
			byMachine[s.Machine]++
			byKind[s.Kind]++
			switch {
			case s.ProjectID == "":
				elsewhere++
			case strings.HasPrefix(s.ProjectID, "scratch:"):
				scratch++
			default:
				byProject[s.ProjectID]++
			}
		}
		// The real shape (wire.SessionStats: count, byKind, byMachine,
		// indexBytes, mirrorBytes, indexing always present) plus byProject,
		// scratch and elsewhere, which part D does not send yet (the
		// contract's "Needs"): the app uses them only when present.
		var idx [2]int
		if d.sess.indexing[1] > 0 && d.sess.indexing[0] < d.sess.indexing[1] {
			idx = d.sess.indexing
		}
		res := map[string]any{"count": total, "byProject": byProject, "byMachine": byMachine, "byKind": byKind, "scratch": scratch, "elsewhere": elsewhere,
			"indexBytes": int64(total) * 18800, "mirrorBytes": int64(0), "indexing": map[string]any{"done": idx[0], "total": idx[1]}}
		return res, nil
	case "sessions.brief":
		defer d.mu.Unlock()
		s, e := get()
		if e != nil {
			return nil, e
		}
		var todos []string
		for _, t := range s.Todos {
			if !t.Done {
				todos = append(todos, "- "+t.Text)
			}
		}
		text := fmt.Sprintf("Continue this task (from a %s session on %s).\n\nTask: %s\nLast ask: %s\nLast state: %s\nChanged files: rail.py, tests/test_rail.py\nOpen todos:\n%s\n",
			s.Kind, s.Machine, s.FirstPrompt, s.LastUser, s.LastAssistant, strings.Join(todos, "\n"))
		return map[string]any{"text": text}, nil
	case "sessions.archive":
		defer d.mu.Unlock()
		s, e := get()
		if e != nil {
			return nil, e
		}
		s.Archived, _ = p["archived"].(bool)
		d.sessionChangedLocked(s)
		return map[string]any{}, nil
	case "sessions.delete":
		defer d.mu.Unlock()
		s, e := get()
		if e != nil {
			return nil, e
		}
		if undo, _ := p["undo"].(bool); undo {
			if s.deleted.IsZero() {
				return nil, errCode("invalid", "nothing to undo")
			}
			s.deleted = time.Time{}
			d.sessionChangedLocked(s)
			return map[string]any{}, nil
		}
		if l := d.liveLocked(s); l != nil {
			return nil, &rpcError{Code: -32000, Message: "the session is live", Data: map[string]any{"code": "live", "agentId": l.AgentID}}
		}
		s.deleted = time.Now()
		d.broadcastLocked("sessions.removed", map[string]any{"id": s.ID})
		return map[string]any{}, nil
	case "sessions.resume", "sessions.fork", "sessions.continueAs":
		s, e := get()
		if e != nil {
			d.mu.Unlock()
			return nil, e
		}
		l := d.liveLocked(s)
		sc := *s
		d.mu.Unlock()
		if method == "sessions.resume" && l != nil {
			if l.AgentID != "" {
				return nil, &rpcError{Code: -32000, Message: "the session is live", Data: map[string]any{"code": "live", "agentId": l.AgentID}}
			}
			return nil, &rpcError{Code: -32000, Message: "the session is running outside Hesper", Data: map[string]any{"code": "live"}}
		}
		machine := str("machine")
		note := ""
		if machine == "" {
			machine = sc.Machine
		} else if machine != sc.Machine && method == "sessions.resume" {
			note = "resumed here; uncommitted work came along from " + sc.Machine
		}
		if sc.MovedTo != "" {
			machine = sc.MovedTo
		}
		kind, name, task := sc.Kind, sc.Title, sc.FirstPrompt
		switch method {
		case "sessions.fork":
			name = "fork: " + sc.Title
		case "sessions.continueAs":
			kind = str("kind")
			task = str("brief")
			if task == "" {
				task = "Continue: " + sc.Title
			}
			name = sc.Title
		}
		if len(name) > 40 {
			name = name[:40]
		}
		cwd := sc.Cwd
		if strings.HasPrefix(cwd, "/tmp/fake/gone/") {
			cwd = "/tmp/fake/project"
		}
		sp := spawnParams{Machine: machine, Kind: kind, Project: cwd, Task: task, Name: name, Branch: sc.Branch}
		if method == "sessions.resume" {
			sp.sessionID = sc.SessionID // the agent continues this session (one owner: its Mac now)
		}
		a, err := d.spawn(sp)
		if err != nil {
			if re, ok := err.(*rpcError); ok {
				return nil, re
			}
			return nil, errCode("invalid", err.Error())
		}
		if note != "" {
			b, _ := json.Marshal(a)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			m["note"] = note
			return m, nil
		}
		return a, nil
	}
	d.mu.Unlock()
	return nil, &rpcError{Code: -32601, Message: "method not found: " + method, Data: map[string]any{"code": "not_found"}}
}

// runIndexing imitates the first index: sessions.indexing {done,total}
// every 50 ms over `dur`.
func (d *daemon) runIndexing(total int, dur time.Duration) {
	steps := max(1, int(dur/(50*time.Millisecond)))
	for i := 0; i <= steps; i++ {
		d.mu.Lock()
		d.sess.indexing = [2]int{total * i / steps, total}
		d.broadcastLocked("sessions.indexing", map[string]any{"done": d.sess.indexing[0], "total": total})
		d.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
}

// sessionsFlood (test-only: fake.sessionsFlood {seconds, rate}): bursts of
// sessions.changed and sessions.indexing, for "History never affects the
// wall" (perf).
func (d *daemon) sessionsFlood(seconds float64, rate int) {
	end := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	tick := time.Second / time.Duration(max(1, rate))
	i := 0
	for time.Now().Before(end) {
		d.mu.Lock()
		if n := len(d.sess.order); n > 0 {
			s := d.sess.byID[d.sess.order[i%n]]
			if s.deleted.IsZero() {
				s.Turns++
				d.sessionChangedLocked(s)
			}
		}
		d.broadcastLocked("sessions.indexing", map[string]any{"done": i % 1000, "total": 1000})
		d.mu.Unlock()
		i++
		time.Sleep(tick)
	}
	d.mu.Lock()
	d.broadcastLocked("sessions.indexing", map[string]any{"done": 1000, "total": 1000})
	d.mu.Unlock()
}
