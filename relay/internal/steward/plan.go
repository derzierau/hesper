package steward

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Planning sessions (steward.plan): a visible planner agent (service
// planner) that proposes milestones for a product and talks them over
// with the person, who accepts them (goals.accept). A child of the
// Steward when it runs here, else of no one.

// safeScope is what a scope id may be as a folder name.
var safeScope = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Plan is steward.plan: it starts the planner and returns it. One
// planner per scope at a time: while one runs (in the scope's folder),
// Plan returns it (the app opens the planner it gets).
func (s *Steward) Plan(p wire.StewardPlanParams) (wire.Agent, error) {
	scope := strings.TrimSpace(p.Scope)
	if !safeScope.MatchString(scope) || strings.Trim(scope, ".") == "" {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "scope must be a project or group id")
	}
	dir := ""
	if s.opt.ProjectPath != nil {
		dir = s.opt.ProjectPath(scope)
	}
	if st, err := os.Stat(dir); dir == "" || err != nil || !st.IsDir() {
		dir = filepath.Join(s.Dir(), "plans", scope)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return wire.Agent{}, fmt.Errorf("planning folder: %w", err)
		}
	}
	s.planMu.Lock() // one look-and-start at a time: no second planner for a scope
	defer s.planMu.Unlock()
	s.mu.Lock()
	reg, parent := s.reg, s.agent
	s.mu.Unlock()
	if reg == nil {
		return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "the Steward is starting")
	}
	for _, a := range reg.List() {
		if a.Service == wire.ServicePlanner && a.Project == dir && a.Exit == nil && a.State != wire.StateExited {
			return a, nil
		}
	}
	sp := wire.SpawnParams{Kind: wire.KindClaude, Project: dir, Name: "Plan " + scope, Task: planTask(scope, p.Prompt)}
	if parent != "" {
		if a, err := reg.Get(parent); err == nil {
			sp.Parent, sp.Depth = a.ID, a.Depth+1
		}
	}
	return reg.SpawnService(sp, wire.ServicePlanner)
}

// planTask is a planner's first message.
func planTask(scope, prompt string) string {
	task := "Plan the product " + scope + " (scope " + scope + "): read it, propose milestones with status proposed, then talk them over with me."
	if prompt = strings.TrimSpace(prompt); prompt != "" {
		task += "\n\n" + prompt
	}
	return task
}
