// Package steward runs the Steward: one background Claude agent, on the one
// Mac its shared configuration names, that keeps the loops records
// (internal/loops) straight. hesperd wakes it with one short line when
// events wait for it (wake.go); it reads them with steward.inbox. It also
// starts planning sessions (steward.plan). It implements agents.Steward.
//
// docs/rebuild-contract.md, "As built — the Steward".
package steward

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The Steward's instructions (its system prompt) and a planning session's.
// skills/hesper/references/steward.md mirrors prompt.md for people.
var (
	//go:embed prompt.md
	Prompt string
	//go:embed plan.md
	PlanPrompt string
)

// Tools are the Steward's tool allowlist (Claude Code's --allowedTools);
// PlanTools a planner's. Only read subcommands where a command also
// deletes (history, drafts: never history delete or archive, drafts rm).
// Anything else asks a person: permissions are never skipped.
// skills/hesper/references/steward.md names the Steward's.
var (
	Tools = []string{
		"Bash(hesperctl loops:*)", "Bash(hesperctl goals:*)", "Bash(hesperctl steward:*)", "Bash(hesperctl ls:*)",
		"Bash(hesperctl show:*)", "Bash(hesperctl result:*)", "Bash(hesperctl history search:*)",
		"Bash(hesperctl history show:*)", "Bash(hesperctl history brief:*)", "Bash(hesperctl history stats:*)",
		"Bash(hesperctl review ls:*)", "Bash(hesperctl drafts ls:*)", "Bash(hesperctl drafts save:*)",
		"Bash(gh pr view:*)", "Bash(gh release view:*)", "Read", "Grep", "Glob",
	}
	PlanTools = []string{
		"Bash(hesperctl goals ls:*)", "Bash(hesperctl goals new:*)", "Bash(hesperctl goals save:*)",
		"Bash(hesperctl loops ls:*)", "Bash(hesperctl loops show:*)", "Bash(hesperctl ls:*)", "Bash(hesperctl show:*)",
		"Bash(hesperctl history search:*)", "Bash(hesperctl history show:*)", "Bash(hesperctl history brief:*)",
		"Bash(hesperctl steward status:*)", "Bash(git branch:*)", "Bash(git log:*)", "Bash(gh pr list:*)",
		"Bash(gh pr view:*)", "Bash(gh release view:*)", "Read", "Grep", "Glob",
	}
)

// Argv is a service agent's Claude options (agents.Options.ServiceArgv):
// they replace its profile's, so a profile that skips permissions never
// applies.
func Argv(service string) []string {
	tools, prompt := Tools, Prompt
	if service == wire.ServicePlanner {
		tools, prompt = PlanTools, PlanPrompt
	}
	return []string{"--permission-mode", "default", "--allowedTools", strings.Join(tools, ","), "--append-system-prompt", prompt}
}

// Store is the loops store's side the Steward uses (internal/loops.Store).
type Store interface {
	Node() string
	StewardConfig() wire.StewardConfig
	SetStewardConfig(p wire.StewardSetParams) (wire.StewardConfig, error)
	StewardChanges() <-chan struct{}
	Settings() wire.LoopSettings
	List(p wire.LoopsListParams) []wire.Loop
	Goals(scope string) wire.GoalsList
}

// Registry is the agents registry's side the Steward uses
// (agents.Registry). It is called without the Steward's lock held.
type Registry interface {
	SpawnService(p wire.SpawnParams, service string) (wire.Agent, error)
	Get(id string) (wire.Agent, error)
	List() []wire.Agent
	Resume(id string) (wire.Agent, error)
	CloseAgent(id string) (wire.CloseResult, error)
	Input(p wire.InputParams) error
}

// Options configure a Steward.
type Options struct {
	StateDir string // $STATE: the Steward works in $STATE/steward
	Store    Store
	// ProjectPath is a project's folder on this Mac ("" none; a group, a
	// project elsewhere): where a planning session runs.
	ProjectPath func(id string) string
	Logf        func(format string, args ...any)
	// Now and Location (tests): the clock and the owner's time zone
	// (time.Now, time.Local).
	Now      func() time.Time
	Location *time.Location
}

// Limits.
const (
	// maxRestarts in restartWindow, then it stays stopped (Status.Stopped).
	maxRestarts   = 3
	restartWindow = time.Hour
	// wakeGap: at most one wake a minute.
	wakeGap = time.Minute
	// maxEvents: the queue keeps the newest; the oldest is dropped.
	maxEvents = 256
)

// restartDelays is the backoff before the nth restart in the window.
var restartDelays = [maxRestarts]time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}

// Steward is the Steward of this Mac (running only when the shared
// configuration names this Mac's node).
type Steward struct {
	opt Options
	reg Registry

	planMu sync.Mutex // Plan: one at a time
	mu     sync.Mutex
	// the running agent ("" none) and its last known state; day: the
	// local date its session started (rotated at the first wake of a new
	// day).
	agent, state, session, day string
	// here: the configuration names this Mac (events queue only then).
	here bool
	// started: it ran since it was turned on here (what follows is a
	// restart, within the budget).
	started     bool
	restarts    []time.Time
	nextRestart time.Time
	stopped     string
	// the wake queue (wake.go)
	queue      []wire.StewardEvent
	keys       []string // queue[i]'s coalescing key
	dropped    int
	wakes      int
	wakeDay    string
	lastWake   time.Time
	eveningDay string
	seenLoops  map[string]bool // LoopCreated

	kick    chan struct{}
	changed chan struct{} // closed at the next status change (Watch)
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New is a Steward that does nothing until Start.
func New(opt Options) *Steward {
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Location == nil {
		opt.Location = time.Local
	}
	return &Steward{opt: opt, kick: make(chan struct{}, 1), changed: make(chan struct{})}
}

// Start runs the Steward with the registry: it starts (or adopts) its
// agent when the configuration names this Mac and follows the changes,
// until ctx ends or Close.
func (s *Steward) Start(ctx context.Context, reg Registry) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.reg, s.cancel = reg, cancel
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx)
	}()
}

// Close stops following (the agent keeps running: hesperd's stop respawns
// it next time).
func (s *Steward) Close() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *Steward) poke() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// notifyLocked tells Watch the status changed (the lock is held).
func (s *Steward) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Steward) run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		changes := s.opt.Store.StewardChanges()
		next := s.step()
		wait := time.Hour
		if !next.IsZero() {
			wait = max(next.Sub(s.opt.Now()), 10*time.Millisecond)
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-changes:
		case <-s.kick:
		case <-timer.C:
		}
	}
}

// step brings the agent in line with the configuration and wakes it when
// it should; it returns when to look again (zero: on the next change).
func (s *Steward) step() time.Time {
	cfg := s.opt.Store.StewardConfig()
	here := cfg.Enabled && cfg.Node != "" && cfg.Node == s.opt.Store.Node()
	s.mu.Lock()
	s.here = here
	s.mu.Unlock()
	if !here {
		s.stop()
		return time.Time{}
	}
	if next := s.ensure(); !next.IsZero() {
		return next
	}
	return s.wake() // wake.go
}

// stop closes the Steward's agent here, if any (the configuration names
// another Mac or turned it off).
func (s *Steward) stop() {
	s.mu.Lock()
	id := s.agent
	s.agent, s.state, s.session, s.day = "", "", "", ""
	s.restarts, s.stopped, s.nextRestart, s.started, s.eveningDay = nil, "", time.Time{}, false, ""
	if id == "" {
		id = s.adoptableLocked()
	} else {
		s.notifyLocked()
	}
	reg := s.reg
	s.mu.Unlock()
	if id != "" {
		if _, err := reg.CloseAgent(id); err != nil {
			s.opt.Logf("steward: closing %s: %v", id, err)
		}
	}
}

// adoptableLocked is a Steward agent the registry already has (one
// restored after a daemon restart), "" none (the lock is held; the
// registry's List takes only its own).
func (s *Steward) adoptableLocked() string {
	if s.reg == nil {
		return ""
	}
	for _, a := range s.reg.List() {
		if a.Service == wire.ServiceSteward {
			return a.ID
		}
	}
	return ""
}

// ensure keeps the agent running: adopts a restored one, starts one,
// restarts one whose process ended or that a person closed (with backoff,
// at most maxRestarts an hour, then it stays stopped). Non-zero: when to
// try again.
func (s *Steward) ensure() time.Time {
	now := s.opt.Now()
	s.mu.Lock()
	reg := s.reg
	if s.agent == "" {
		if id := s.adoptableLocked(); id != "" {
			s.agent, s.started = id, true
			s.notifyLocked()
		}
	}
	id, stopped := s.agent, s.stopped
	s.mu.Unlock()
	if stopped != "" {
		return time.Time{}
	}
	if id != "" {
		a, err := reg.Get(id)
		if err == nil {
			s.seen(a)
			if a.Exit == nil {
				return time.Time{} // runs
			}
		} else {
			s.mu.Lock()
			s.agent = "" // removed: a new one
			s.mu.Unlock()
			id = ""
		}
	}
	s.mu.Lock()
	if s.started {
		cut := 0
		for cut < len(s.restarts) && now.Sub(s.restarts[cut]) >= restartWindow {
			cut++
		}
		s.restarts = s.restarts[cut:]
		if len(s.restarts) >= maxRestarts {
			s.stopped = fmt.Sprintf("it ended %d times within an hour; turn it off and on again to retry (hesperctl steward off, on)", maxRestarts)
			s.notifyLocked()
			s.mu.Unlock()
			s.opt.Logf("steward: %s", s.stopped)
			return time.Time{}
		}
		if s.nextRestart.IsZero() {
			s.nextRestart = now.Add(restartDelays[len(s.restarts)])
		}
		if now.Before(s.nextRestart) {
			next := s.nextRestart
			s.mu.Unlock()
			return next
		}
		s.restarts = append(s.restarts, now)
		s.nextRestart = time.Time{}
	}
	s.started = true
	s.mu.Unlock()
	if id != "" {
		if a, err := reg.Resume(id); err == nil {
			s.seen(a)
			return time.Time{}
		}
		reg.CloseAgent(id) // it did not come back: a new one
		s.mu.Lock()
		s.agent = ""
		s.mu.Unlock()
	}
	a, err := s.spawn()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.opt.Logf("steward: starting: %v", err)
		s.nextRestart = now.Add(restartDelays[min(len(s.restarts), maxRestarts-1)])
		return s.nextRestart
	}
	s.agent, s.state, s.session, s.day = a.ID, a.State, a.SessionID, s.dayOf(now)
	s.notifyLocked()
	return time.Time{}
}

// Dir is where the Steward works ($STATE/steward).
func (s *Steward) Dir() string { return filepath.Join(s.opt.StateDir, "steward") }

// spawn starts a Steward agent: Claude, in the background, in Dir, its
// first message a brief of the records (brief.go).
func (s *Steward) spawn() (wire.Agent, error) {
	dir := s.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return wire.Agent{}, fmt.Errorf("steward folder: %w", err)
	}
	os.Chmod(dir, 0o700)
	s.mu.Lock()
	reg := s.reg
	s.mu.Unlock()
	return reg.SpawnService(wire.SpawnParams{Kind: wire.KindClaude, Project: dir, Name: "Steward", Task: s.Brief()}, wire.ServiceSteward)
}

// seen records the Steward agent's state (the lock is not held).
func (s *Steward) seen(a wire.Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ID != s.agent {
		return
	}
	if s.state != a.State || s.session != a.SessionID {
		s.state, s.session = a.State, a.SessionID
		if s.day == "" {
			s.day = s.dayOf(a.Created)
		}
	}
}

// AgentChanged is an agent's change (the gateway's subscription): the
// Steward's own state decides when it can be woken or must restart.
func (s *Steward) AgentChanged(a wire.Agent) {
	if a.Service != wire.ServiceSteward {
		return
	}
	s.mu.Lock()
	mine := a.ID == s.agent
	s.mu.Unlock()
	if mine {
		s.seen(a)
	}
	s.poke()
}

func (s *Steward) dayOf(t time.Time) string { return t.In(s.opt.Location).Format(time.DateOnly) }

// Status is steward.status.
func (s *Steward) Status() wire.StewardStatus {
	cfg := s.opt.Store.StewardConfig()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := wire.StewardStatus{StewardConfig: cfg, Agent: s.agent, Queued: len(s.queue), LastWake: s.lastWake,
		Session: s.session, Dropped: s.dropped, Stopped: s.stopped}
	if s.wakeDay == s.dayOf(s.opt.Now()) {
		st.Wakes = s.wakes
	}
	return st
}

// Set is steward.set: the shared configuration; turning it on again
// clears a stop after too many restarts.
func (s *Steward) Set(p wire.StewardSetParams) (wire.StewardStatus, error) {
	if _, err := s.opt.Store.SetStewardConfig(p); err != nil {
		return wire.StewardStatus{}, err
	}
	if p.Enabled != nil {
		s.mu.Lock()
		if s.stopped != "" {
			s.started = false // a new start, at once
		}
		s.restarts, s.stopped, s.nextRestart = nil, "", time.Time{}
		s.notifyLocked()
		s.mu.Unlock()
	}
	s.poke()
	return s.Status(), nil
}

// Call runs a steward.* method (agents.Steward). Who may call what is the
// dispatcher's (internal/agents/loopscall.go).
func (s *Steward) Call(method string, params json.RawMessage, caller agents.Caller) (any, error, bool) {
	switch method {
	case wire.MethodStewardStatus:
		return s.Status(), nil, true
	case wire.MethodStewardSet:
		var p wire.StewardSetParams
		if err := decode(params, &p); err != nil {
			return nil, err, true
		}
		res, err := s.Set(p)
		return res, err, true
	case wire.MethodStewardInbox:
		return s.Inbox(), nil, true // wake.go
	case wire.MethodStewardPlan:
		var p wire.StewardPlanParams
		if err := decode(params, &p); err != nil {
			return nil, err, true
		}
		res, err := s.Plan(p) // plan.go
		return res, err, true
	}
	return nil, nil, false
}

func decode(params json.RawMessage, v any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	d := json.NewDecoder(strings.NewReader(string(params)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return wire.Errorf(wire.CodeInvalid, "invalid params: %v", err)
	}
	return nil
}

// Watch sends steward.changed now and after each change of the status
// (configuration, agent, stop, wakes), at most every 250 ms, until done
// closes or send fails (agents.Steward).
func (s *Steward) Watch(done <-chan struct{}, send func(method string, params any) error) {
	for {
		s.mu.Lock()
		changed := s.changed
		s.mu.Unlock()
		if send(wire.NoteStewardChanged, wire.StewardChanged{Steward: s.Status()}) != nil {
			return
		}
		select {
		case <-done:
			return
		case <-changed:
		case <-s.opt.Store.StewardChanges():
		}
		select {
		case <-done:
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}
