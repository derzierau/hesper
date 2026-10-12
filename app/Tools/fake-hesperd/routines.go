package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Routines (TEST-ONLY imitation of the routines.* wire the app is built
// against, docs/rebuild-contract.md "As built — routines"):
// routines.list/save/remove/pause/runs/run and routines.changed /
// routines.removed on agents.subscribe, shaped like relay/pkg/wire/
// routines.go. In memory only. Seeded with --routines N; --no-routines
// answers -32601 like an older hesperd. Every routines.* call is logged
// for the UI test (fake.routineCalls); fake.routineRun ends a run with
// any outcome on demand. A run's agent is an ordinary fake agent that
// stays on the wall (the real daemon closes a finished background run).

type Routine struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Machine   string      `json:"machine,omitempty"`
	Spawn     spawnParams `json:"spawn"`
	Every     string      `json:"every,omitempty"`
	Offset    string      `json:"offset,omitempty"`
	At        []string    `json:"at,omitempty"`
	Policy    RunPolicy   `json:"policy"`
	Paused    bool        `json:"paused,omitempty"`
	CreatedBy string      `json:"createdBy,omitempty"`
	Created   time.Time   `json:"created"`
	Updated   time.Time   `json:"updated"`
	Runs      int         `json:"runs,omitempty"`
	LastRun   *RoutineRun `json:"lastRun,omitempty"`
	NextRun   time.Time   `json:"nextRun,omitzero"`
}

type RunPolicy struct {
	Timeout    string `json:"timeout,omitempty"`
	Overlap    string `json:"overlap,omitempty"`
	CatchUp    string `json:"catchUp,omitempty"`
	Foreground bool   `json:"foreground,omitempty"`
	KeepRuns   int    `json:"keepRuns,omitempty"`
}

type RoutineRun struct {
	Seq     int       `json:"seq"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	Agent   string    `json:"agent,omitempty"`
	Session string    `json:"session,omitempty"`
	Outcome string    `json:"outcome"`
	State   string    `json:"state,omitempty"`
	Message string    `json:"message,omitempty"`
	Error   string    `json:"error,omitempty"`
	Manual  bool      `json:"manual,omitempty"`
}

type routineStore struct {
	byID     map[string]*Routine
	runs     map[string][]RoutineRun // by routine id, seq order
	calls    []map[string]any
	disabled bool
}

var (
	routineID = regexp.MustCompile(`^r-[a-z0-9-]{1,40}$`)
	atTime    = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

func (d *daemon) routinesEnabled() bool { return d.routines != nil && !d.routines.disabled }

// routineList is every routine, oldest first (the lock is held).
func (d *daemon) routineList() []*Routine {
	list := make([]*Routine, 0, len(d.routines.byID))
	for _, r := range d.routines.byID {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].Created.Equal(list[j].Created) {
			return list[i].Created.Before(list[j].Created)
		}
		return list[i].ID < list[j].ID
	})
	return list
}

func (d *daemon) routineChangedLocked(r *Routine) {
	if d.routinesEnabled() {
		d.broadcastLocked("routines.changed", map[string]any{"routine": r})
	}
}

// validateRoutine mirrors internal/routines.Validate: defaults filled in
// place, an invalid error naming the field.
func validateRoutine(r *Routine) *rpcError {
	if r.ID != "" && !routineID.MatchString(r.ID) {
		return errCode("invalid", fmt.Sprintf("routine id %q: want r- and 1-40 of [a-z0-9-]", r.ID))
	}
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		r.Name = strings.TrimPrefix(r.ID, "r-")
	}
	if r.Name == "" {
		return errCode("invalid", "a routine needs a name")
	}
	r.Every, r.Offset = strings.TrimSpace(r.Every), strings.TrimSpace(r.Offset)
	switch {
	case r.Every == "" && len(r.At) == 0:
		return errCode("invalid", "a routine needs every or at")
	case r.Every != "" && len(r.At) > 0:
		return errCode("invalid", "every and at exclude each other")
	case r.Every != "":
		every, err := time.ParseDuration(r.Every)
		if err != nil {
			return errCode("invalid", fmt.Sprintf("every %q: want a duration like 15m or 1h", r.Every))
		}
		if every < time.Minute || every > 168*time.Hour {
			return errCode("invalid", fmt.Sprintf("every %s: want 1m0s to 168h0m0s", r.Every))
		}
		if r.Offset == "" {
			r.Offset = "0s"
		}
		offset, err := time.ParseDuration(r.Offset)
		if err != nil {
			return errCode("invalid", fmt.Sprintf("offset %q: want a duration like 5m", r.Offset))
		}
		if offset < 0 || offset >= every {
			return errCode("invalid", fmt.Sprintf("offset %s: want 0 up to every (%s)", r.Offset, r.Every))
		}
	default:
		if r.Offset != "" {
			return errCode("invalid", "offset goes with every")
		}
		seen := map[string]bool{}
		var at []string
		for _, t := range r.At {
			t = strings.TrimSpace(t)
			if !atTime.MatchString(t) {
				return errCode("invalid", fmt.Sprintf("at %q: want HH:MM (24h)", t))
			}
			if !seen[t] {
				seen[t] = true
				at = append(at, t)
			}
		}
		sort.Strings(at)
		r.At = at
	}
	sp := &r.Spawn
	if strings.TrimSpace(sp.Task) == "" {
		return errCode("invalid", "a routine needs a task")
	}
	if sp.Project == "" || !filepath.IsAbs(sp.Project) {
		return errCode("invalid", fmt.Sprintf("project %q: want an absolute path", sp.Project))
	}
	if sp.Machine != "" && sp.Machine != r.Machine {
		return errCode("invalid", fmt.Sprintf("the spawn's machine %q is not the routine's (%q)", sp.Machine, r.Machine))
	}
	switch sp.Kind {
	case "", "claude", "codex", "shell":
	default:
		return errCode("invalid", fmt.Sprintf("kind %q: want claude, codex or shell", sp.Kind))
	}
	sp.Name = "" // the daemon's: it names the run
	p := &r.Policy
	p.Timeout = strings.TrimSpace(p.Timeout)
	if p.Timeout == "" {
		p.Timeout = "1h"
	}
	timeout, err := time.ParseDuration(p.Timeout)
	if err != nil {
		return errCode("invalid", fmt.Sprintf("timeout %q: want a duration like 30m", p.Timeout))
	}
	if timeout < time.Minute || timeout > 24*time.Hour {
		return errCode("invalid", fmt.Sprintf("timeout %s: want 1m0s to 24h0m0s", p.Timeout))
	}
	switch p.Overlap {
	case "":
		p.Overlap = "skip"
	case "skip", "replace":
	default:
		return errCode("invalid", fmt.Sprintf("overlap %q: want skip or replace", p.Overlap))
	}
	switch p.CatchUp {
	case "":
		p.CatchUp = "once"
	case "once", "none":
	default:
		return errCode("invalid", fmt.Sprintf("catchUp %q: want once or none", p.CatchUp))
	}
	switch {
	case p.KeepRuns == 0:
		p.KeepRuns = 200
	case p.KeepRuns < 0 || p.KeepRuns > 1000:
		return errCode("invalid", fmt.Sprintf("keepRuns %d: want 1 to 1000", p.KeepRuns))
	}
	return nil
}

// routineNext mirrors internal/routines.Next for a validated routine: the
// first instant after `after` (local midnight + offset + k·every within
// the day, or the next listed time), UTC; zero while paused.
func routineNext(r *Routine, after time.Time) time.Time {
	if r.Paused {
		return time.Time{}
	}
	local := after.In(time.Local)
	y, m, dd := local.Date()
	midnight := time.Date(y, m, dd, 0, 0, 0, 0, time.Local)
	tomorrow := time.Date(y, m, dd+1, 0, 0, 0, 0, time.Local)
	if r.Every != "" {
		every, _ := time.ParseDuration(r.Every)
		offset, _ := time.ParseDuration(r.Offset)
		first := midnight.Add(offset)
		next := first
		if !after.Before(first) {
			next = first.Add((after.Sub(first)/every + 1) * every)
		}
		if !next.Before(tomorrow) {
			next = tomorrow.Add(offset)
		}
		return next.UTC()
	}
	var today, nextDay time.Time
	for _, at := range r.At {
		hh := int(at[0]-'0')*10 + int(at[1]-'0')
		mm := int(at[3]-'0')*10 + int(at[4]-'0')
		if t := time.Date(y, m, dd, hh, mm, 0, 0, time.Local); t.After(after) && (today.IsZero() || t.Before(today)) {
			today = t
		}
		if t := time.Date(y, m, dd+1, hh, mm, 0, 0, time.Local); nextDay.IsZero() || t.Before(nextDay) {
			nextDay = t
		}
	}
	if !today.IsZero() {
		return today.UTC()
	}
	return nextDay.UTC()
}

// seedRoutines: n demo routines over the demo project folders, with the
// outcomes a panel shows: needs-you (its agent waits at an approval),
// paused, timed-out, several done runs, failed (--routines N).
func (d *daemon) seedRoutines(n int) {
	type seed struct {
		id, name, project, task string
		every, offset           string
		at                      []string
		policy                  RunPolicy
		paused                  bool
		runs                    []RoutineRun // ended runs, oldest first (started/ended set below)
	}
	seeds := []seed{
		{id: "r-floor-prices", name: "floor-prices", project: fakeRoot + "/acme-apps", every: "1h", offset: "5m",
			task: "Sweep the floor prices of every ad unit against yesterday's. Refuse any change over 15 % and report what moved.",
			runs: []RoutineRun{
				{Outcome: "done", State: "done", Message: "Swept 48 units; raised 3, nothing refused."},
				{Outcome: "done", State: "done", Message: "Swept 48 units; nothing moved."},
				{Outcome: "skipped", Error: "run 2 is still live"},
				{Outcome: "needs-you", State: "approval"},
			}},
		{id: "r-nightly-digest", name: "nightly-digest", project: fakeRoot + "/hesper", at: []string{"06:30"},
			task: "Read yesterday's merged PRs and open issues; write a digest to docs/digest/<date>.md and open a draft PR.",
			runs: []RoutineRun{
				{Outcome: "done", State: "done", Message: "Digest for 7 PRs and 3 issues; opened PR #471 (draft)."},
				{Outcome: "done", State: "done", Message: "Digest for 4 PRs; opened PR #476 (draft)."},
				{Outcome: "exited", State: "exited", Error: "hesperd restarted"},
				{Outcome: "done", State: "idle", Message: "Digest for 9 PRs and 1 issue; opened PR #482 (draft)."},
			}},
		{id: "r-dependency-audit", name: "dependency-audit", project: fakeRoot + "/acme-apps/apps/ios-app", at: []string{"09:00", "15:00"},
			task: "Run the dependency audit; file an issue per advisory with the affected targets.", paused: true,
			policy: RunPolicy{CatchUp: "none"},
			runs: []RoutineRun{
				{Outcome: "done", State: "done", Message: "No advisories for 41 packages."},
			}},
		{id: "r-review-sweep", name: "review-sweep", project: fakeRoot + "/design-system", every: "15m",
			task:   "Review every open PR without a review; leave comments, approve nothing.",
			policy: RunPolicy{Timeout: "10m", Overlap: "replace"},
			runs: []RoutineRun{
				{Outcome: "done", State: "done", Message: "Reviewed 2 PRs."},
				{Outcome: "timed-out", State: "working", Error: "timed out after 10m0s"},
			}},
		{id: "r-release-notes", name: "release-notes", project: fakeRoot + "/hesper", at: []string{"18:00"},
			task:   "Draft the release notes for the commits since the last tag into docs/releases/next.md.",
			policy: RunPolicy{Foreground: true},
			runs: []RoutineRun{
				{Outcome: "failed", Error: "agents.spawn: profile \"claude-unattended\": claude not found on PATH"},
			}},
	}
	base := time.Now().Add(-10 * time.Minute)
	d.mu.Lock()
	defer d.mu.Unlock()
	var needsYou *Routine
	for i := 0; i < n; i++ {
		s := seeds[i%len(seeds)]
		if i >= len(seeds) {
			s.id, s.name = fmt.Sprintf("%s-%d", s.id, i/len(seeds)+1), fmt.Sprintf("%s-%d", s.name, i/len(seeds)+1)
			s.runs, s.paused = s.runs[:1], false
		}
		created := base.Add(-time.Duration(30-i) * 24 * time.Hour)
		r := &Routine{ID: s.id, Name: s.name, Spawn: spawnParams{Kind: "claude", Project: s.project, Task: s.task},
			Every: s.every, Offset: s.offset, At: s.at, Policy: s.policy, Paused: s.paused, CreatedBy: "local",
			Created: created.UTC(), Updated: created.UTC()}
		if e := validateRoutine(r); e != nil {
			continue
		}
		var runs []RoutineRun
		for k, run := range s.runs {
			run.Seq = k + 1
			run.Started = base.Add(-time.Duration(len(s.runs)-k) * 90 * time.Minute).UTC()
			run.Ended = run.Started.Add(time.Duration(3+k) * time.Minute).UTC()
			if run.Outcome != "skipped" && run.Outcome != "failed" {
				run.Agent, run.Session = "L/"+newID(), "fake-"+newID()
			}
			runs = append(runs, run)
		}
		r.Runs = len(runs)
		if len(runs) > 0 {
			last := runs[len(runs)-1]
			r.LastRun = &last
		}
		r.NextRun = routineNext(r, time.Now())
		d.routines.byID[r.ID] = r
		d.routines.runs[r.ID] = runs
		if r.LastRun != nil && r.LastRun.Outcome == "needs-you" && needsYou == nil {
			needsYou = r
		}
	}
	if needsYou != nil {
		// The needs-you run's agent waits at an approval on the wall.
		d.mu.Unlock()
		a, err := d.routineSpawn(needsYou, needsYou.LastRun.Seq)
		d.mu.Lock()
		if err == nil {
			needsYou.LastRun.Agent, needsYou.LastRun.Session = a.ID, a.SessionID
			runs := d.routines.runs[needsYou.ID]
			runs[len(runs)-1] = *needsYou.LastRun
			go func() {
				time.Sleep(time.Second) // after spawn's "working"
				d.setState(a.ID, "approval", &Attention{Kind: "approval", Title: "Bash", Detail: "./scripts/set-floor.sh --unit rail-top --price 0.42", Options: []string{"allow", "always", "deny"}}, "")
			}()
		}
	}
}

// routineSpawn starts a run's agent as hesperd does: the routine's spawn,
// named "<routine> · HH:MM", carrying routine and routineRun.
func (d *daemon) routineSpawn(r *Routine, seq int) (*Agent, error) {
	sp := r.Spawn
	sp.Name = r.Name + " · " + time.Now().Format("15:04")
	sp.routine, sp.routineRun = r.ID, seq
	return d.spawn(sp)
}

// routineLiveLocked: the routine's run that is still running, if any.
func (d *daemon) routineLiveLocked(id string) (int, bool) {
	for _, run := range d.routines.runs[id] {
		if run.Outcome == "running" {
			return run.Seq, true
		}
	}
	return 0, false
}

// beginRun begins a manual run: Seq runs+1, running, its agent spawned;
// `exists` while a run is live. It ends by itself after `after` with
// outcome, message and error (routineEnd).
func (d *daemon) beginRun(id, outcome, message, errText string, after time.Duration) (any, *rpcError) {
	d.mu.Lock()
	r, ok := d.routines.byID[id]
	if !ok {
		d.mu.Unlock()
		return nil, errCode("not_found", "no routine "+id)
	}
	if seq, live := d.routineLiveLocked(id); live {
		d.mu.Unlock()
		return nil, errCode("exists", fmt.Sprintf("routine %s: run %d is still live", id, seq))
	}
	r.Runs++
	run := RoutineRun{Seq: r.Runs, Started: time.Now().UTC(), Outcome: "running", Manual: true}
	d.mu.Unlock()
	a, err := d.routineSpawn(r, run.Seq)
	d.mu.Lock()
	if err != nil {
		run.Outcome, run.Ended = "failed", time.Now().UTC()
		run.Error = "agents.spawn: " + err.Error()
	} else {
		run.Agent, run.Session = a.ID, a.SessionID
	}
	d.routines.runs[id] = append(d.routines.runs[id], run)
	r.LastRun = &run
	d.routineChangedLocked(r)
	d.mu.Unlock()
	if err == nil {
		go func() {
			time.Sleep(after)
			d.routineEnd(id, run.Seq, outcome, message, errText)
		}()
	}
	return run, nil
}

// routineEnd ends a running run and puts its agent in the matching state.
func (d *daemon) routineEnd(id string, seq int, outcome, message, errText string) {
	d.mu.Lock()
	r, ok := d.routines.byID[id]
	if !ok {
		d.mu.Unlock()
		return
	}
	runs := d.routines.runs[id]
	i := -1
	for k, run := range runs {
		if run.Seq == seq && run.Outcome == "running" {
			i = k
		}
	}
	if i < 0 {
		d.mu.Unlock()
		return
	}
	run := &runs[i]
	run.Ended, run.Outcome, run.Message, run.Error = time.Now().UTC(), outcome, message, errText
	state, summary := "", ""
	var att *Attention
	switch outcome {
	case "done":
		state, summary = "done", message
	case "needs-you":
		state = "approval"
		att = &Attention{Kind: "approval", Title: "Bash", Detail: "git push origin routine/" + r.Name, Options: []string{"allow", "always", "deny"}}
	case "error":
		state = "error"
		if errText == "" {
			run.Error = "API rate limit reached"
		}
		att = &Attention{Kind: "error", Title: "Agent error", Detail: run.Error}
	case "timed-out":
		state = "exited"
		if errText == "" {
			timeout, _ := time.ParseDuration(r.Policy.Timeout)
			run.Error = "timed out after " + timeout.String()
		}
	case "exited":
		state = "exited"
	}
	run.State = state
	if r.LastRun != nil && r.LastRun.Seq == seq {
		copied := *run
		r.LastRun = &copied
	}
	d.routineChangedLocked(r)
	agent := run.Agent
	d.mu.Unlock()
	if agent == "" || state == "" {
		return
	}
	if state == "exited" {
		d.mu.Lock()
		ap := d.agents[agent]
		if ap != nil && ap.cmd != nil && ap.cmd.Process != nil {
			ap.stopping = true
			_ = ap.cmd.Process.Signal(syscall.SIGHUP)
		}
		d.mu.Unlock()
		return
	}
	_ = d.setState(agent, state, att, summary)
}

func (d *daemon) routineCall(method string, raw json.RawMessage) (any, *rpcError) {
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	str := func(k string) string { s, _ := p[k].(string); return s }
	d.mu.Lock()
	if !d.routinesEnabled() {
		d.mu.Unlock()
		return nil, &rpcError{Code: -32601, Message: "method not found: " + method, Data: map[string]any{"code": "not_found"}}
	}
	d.routines.calls = append(d.routines.calls, map[string]any{"method": method, "params": p})
	get := func() (*Routine, *rpcError) {
		r, ok := d.routines.byID[str("id")]
		if !ok {
			return nil, errCode("not_found", "no routine "+str("id"))
		}
		return r, nil
	}
	switch method {
	case "routines.list":
		defer d.mu.Unlock()
		return d.routineList(), nil
	case "routines.save":
		defer d.mu.Unlock()
		var sp struct {
			Routine *Routine `json:"routine"`
		}
		if json.Unmarshal(raw, &sp) != nil || sp.Routine == nil {
			return nil, errCode("invalid", "routine is required")
		}
		r := sp.Routine
		if r.Machine != "" && r.Machine != d.self {
			return nil, errCode("unavailable", "routine on machine "+r.Machine+": not this Mac")
		}
		if e := validateRoutine(r); e != nil {
			return nil, e
		}
		if r.ID == "" {
			r.ID = "r-" + newID()
		}
		ts := time.Now().UTC()
		if old, ok := d.routines.byID[r.ID]; ok {
			r.Created, r.CreatedBy, r.Runs, r.LastRun = old.Created, old.CreatedBy, old.Runs, old.LastRun
		} else {
			r.Created, r.CreatedBy, r.Runs, r.LastRun = ts, "local", 0, nil
			if len(d.routines.byID) >= 100 {
				return nil, errCode("invalid", "100 routines already")
			}
		}
		r.Updated = ts
		r.NextRun = routineNext(r, ts)
		d.routines.byID[r.ID] = r
		d.routineChangedLocked(r)
		return r, nil
	case "routines.remove":
		defer d.mu.Unlock()
		r, e := get()
		if e != nil {
			return nil, e
		}
		delete(d.routines.byID, r.ID)
		delete(d.routines.runs, r.ID)
		d.broadcastLocked("routines.removed", map[string]any{"id": r.ID})
		return map[string]any{}, nil
	case "routines.pause":
		defer d.mu.Unlock()
		r, e := get()
		if e != nil {
			return nil, e
		}
		r.Paused, _ = p["paused"].(bool)
		r.Updated = time.Now().UTC()
		r.NextRun = routineNext(r, r.Updated)
		d.routineChangedLocked(r)
		return r, nil
	case "routines.runs":
		defer d.mu.Unlock()
		r, e := get()
		if e != nil {
			return nil, e
		}
		limit := 50
		if l, ok := p["limit"].(float64); ok && l > 0 {
			limit = int(l)
		}
		runs := d.routines.runs[r.ID]
		out := []RoutineRun{}
		for i := len(runs) - 1; i >= 0 && len(out) < limit; i-- {
			out = append(out, runs[i])
		}
		return out, nil
	case "routines.run":
		d.mu.Unlock()
		return d.beginRun(str("id"), "done", "Swept 3 units; nothing refused.", "", 1500*time.Millisecond)
	}
	d.mu.Unlock()
	return nil, &rpcError{Code: -32601, Message: "method not found: " + method, Data: map[string]any{"code": "not_found"}}
}

// routineFakeCall: the test-only hooks (serve.go's fake.* block).
//
//	fake.routineCalls {clear?}: the routines.* calls so far (method, params)
//	fake.routineRun {id, outcome, message?, error?, delayMs?}: begin a run
//	  and end it with outcome after delayMs (0: at once)
func (d *daemon) routineFakeCall(method string, raw json.RawMessage) (any, *rpcError) {
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	str := func(k string) string { s, _ := p[k].(string); return s }
	switch method {
	case "fake.routineCalls":
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.routines == nil {
			return []any{}, nil
		}
		calls := append([]map[string]any{}, d.routines.calls...)
		if c, _ := p["clear"].(bool); c {
			d.routines.calls = nil
		}
		return calls, nil
	case "fake.routineRun":
		if d.routines == nil {
			return nil, errCode("invalid", "the fake runs without routines")
		}
		outcome := str("outcome")
		switch outcome {
		case "done", "needs-you", "exited", "error", "timed-out":
		default:
			return nil, errCode("invalid", "outcome must be done, needs-you, exited, error or timed-out")
		}
		ms, _ := p["delayMs"].(float64)
		return d.beginRun(str("id"), outcome, str("message"), str("error"), time.Duration(ms)*time.Millisecond)
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + method, Data: map[string]any{"code": "not_found"}}
}
