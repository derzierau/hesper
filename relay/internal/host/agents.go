package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The agents methods (rebuild contract part R): hesperd's local methods,
// served to controllers. Ids may be full ("M/a7f3k2", this machine's own
// short name) or local ("a7f3k2"); results carry this machine's ids, the
// controller renames them to its own naming.

// stopGrace bounds agents.stop's wait when a move asks for it.
var stopGrace = 8 * time.Second

func (s *Service) agents(ctx context.Context, m protocol.Message) (json.RawMessage, error) {
	reg := s.Agents
	switch m.Method {
	case "agents.list":
		var p struct{}
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		list := []wire.Agent{}
		for _, a := range reg.List() {
			if s.visible(ctx, a) {
				list = append(list, a)
			}
		}
		return protocol.JSON(list), nil
	case "agents.spawn":
		var p wire.SpawnParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		if p.Machine != "" && p.Machine != reg.Machine() {
			return nil, protocol.Err("invalid", "machine "+p.Machine+" is not this one")
		}
		p.Machine = ""
		kind, err := reg.SpawnKind(p)
		if err != nil {
			return nil, publicError(err)
		}
		if kind == wire.KindShell {
			if err := s.shellAllowed(ctx, m, true); err != nil {
				return nil, err
			}
		}
		a, err := reg.Spawn(p)
		if err != nil {
			return nil, publicError(err)
		}
		return protocol.JSON(a), nil
	case "agents.input":
		var p wire.InputParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		a, err := s.typable(ctx, m, p.ID)
		if err != nil {
			return nil, err
		}
		p.ID = a.ID
		if len(p.Text) > 64<<10 {
			return nil, protocol.Err("invalid", "text is at most 64 KiB")
		}
		return protocol.JSON(struct{}{}), publicErr(reg.Input(p))
	case "agents.answer":
		var p wire.AnswerParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		a, err := s.typable(ctx, m, p.ID)
		if err != nil {
			return nil, err
		}
		p.ID = a.ID
		return protocol.JSON(struct{}{}), publicErr(reg.Answer(p))
	case "agents.stop", "agents.resume", "agents.remove":
		var p struct {
			ID   string `json:"id"`
			Wait bool   `json:"wait"`
		}
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		a, err := s.agent(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		switch m.Method {
		case "agents.stop":
			// wait (a move): until the process ended, so its
			// conversation is complete.
			if p.Wait {
				return protocol.JSON(struct{}{}), publicErr(reg.StopWait(a.ID, stopGrace))
			}
			return protocol.JSON(struct{}{}), publicErr(reg.Stop(a.ID))
		case "agents.resume":
			if a.Kind == wire.KindShell {
				if err := s.shellAllowed(ctx, m, false); err != nil {
					return nil, err
				}
			}
			got, err := reg.Resume(a.ID)
			if err != nil {
				return nil, publicError(err)
			}
			return protocol.JSON(got), nil
		}
		return protocol.JSON(struct{}{}), publicErr(reg.Remove(a.ID))
	case "agents.close", "agents.kill", "agents.background":
		// closing agents (internal/agents/close.go)
		var p wire.BackgroundParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		a, err := s.agent(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		switch m.Method {
		case "agents.close":
			res, err := reg.CloseAgent(a.ID)
			if err != nil {
				return nil, publicError(err)
			}
			return protocol.JSON(res), nil
		case "agents.kill":
			return protocol.JSON(struct{}{}), publicErr(reg.Kill(a.ID))
		}
		return protocol.JSON(struct{}{}), publicErr(reg.SetBackground(a.ID, p.Background))
	case "agents.rename":
		var p wire.RenameParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		a, err := s.agent(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		got, err := reg.Rename(a.ID, p.Name)
		if err != nil {
			return nil, publicError(err)
		}
		return protocol.JSON(got), nil
	case "agents.plan":
		var p wire.IDParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		a, err := s.agent(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		plan, err := reg.Plan(ctx, a.ID)
		if err != nil {
			return nil, publicError(err)
		}
		return protocol.JSON(plan), nil
	case "agents.probe":
		var p struct {
			Path    string   `json:"path"`
			Home    string   `json:"home"`
			Commits []string `json:"commits"`
		}
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		if !cleanAbs(p.Path) || p.Home != "" && !cleanAbs(p.Home) {
			return nil, protocol.Err("invalid_request", "path and home must be clean absolute paths")
		}
		if len(p.Commits) > 64 {
			return nil, protocol.Err("invalid_request", "At most 64 commits")
		}
		for _, c := range p.Commits {
			if !commitID.MatchString(c) {
				return nil, protocol.Err("invalid_request", "commits must be hexadecimal SHAs")
			}
		}
		return protocol.JSON(reg.Probe(ctx, p.Path, p.Home, p.Commits)), nil
	case "agents.attach":
		return s.attach(ctx, m)
	case "projects.clone":
		var p agents.CloneParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		if p.Machine != "" && p.Machine != reg.Machine() {
			return nil, protocol.Err("invalid", "machine "+p.Machine+" is not this one")
		}
		res, err := reg.Clone(ctx, p)
		if err != nil {
			return nil, publicError(err)
		}
		return protocol.JSON(res), nil
	case "projects.recent":
		return protocol.JSON(reg.Recent()), nil
	case "profiles.list":
		return protocol.JSON(reg.Profiles()), nil
	case "fs.stat":
		var p wire.FSStatParams
		if err := params(m.Params, &p); err != nil {
			return nil, err
		}
		if p.Machine != "" && p.Machine != reg.Machine() {
			return nil, protocol.Err("invalid", "machine "+p.Machine+" is not this one")
		}
		if !cleanAbs(p.Path) {
			return nil, protocol.Err("invalid_request", "path must be a clean absolute path")
		}
		st, err := agents.Stat(p.Path)
		if err != nil {
			return nil, publicError(err)
		}
		return protocol.JSON(st), nil
	}
	return nil, protocol.Err("unsupported", "Unsupported operation: "+m.Method)
}

func publicErr(err error) error {
	if err == nil {
		return nil
	}
	return publicError(err)
}

func cleanAbs(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && len(path) <= 4096 && !strings.ContainsRune(path, 0)
}

// typable finds an agent the caller may type into or answer: a shell needs
// the shell right (and --allow-shell).
func (s *Service) typable(ctx context.Context, m protocol.Message, id string) (wire.Agent, error) {
	a, err := s.agent(ctx, id)
	if err != nil {
		return a, err
	}
	if a.Kind == wire.KindShell {
		if err := s.shellAllowed(ctx, m, false); err != nil {
			return a, err
		}
	}
	return a, nil
}

// shellAllowed: a shell on this machine for a remote caller needs
// --allow-shell and the shell right; starting one also Touch ID (the
// strong key), announced as such in the request (kind shell), so the
// signature covered it.
func (s *Service) shellAllowed(ctx context.Context, m protocol.Message, start bool) error {
	caller, ok := CallerFrom(ctx)
	switch {
	case !s.AllowShell:
		return protocol.Err("forbidden", "This machine does not offer shells to other machines (hesperd serve --allow-shell)")
	case !ok || !caller.Verified || !caller.Has(devicekey.Shell):
		return protocol.Err("forbidden", "Shells on this machine need a device approved with the shell right")
	case start && (!caller.Strong || !devicekey.SpawnsShell(m.Params)):
		return protocol.Err("forbidden", "Starting a shell on another machine needs Touch ID: spawn it with kind \"shell\"")
	}
	return nil
}
