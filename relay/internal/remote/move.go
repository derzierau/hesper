package remote

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Moves (agents.move; move work in docs/rebuild-contract.md): an agent
// goes from its machine to another with its conversation and its code,
// any two machines (this one included). This daemon orchestrates and
// tells its subscribers the progress (agents.moving):
//
//  1. preflight, the agent untouched on failure: both machines reachable
//     ("offline"), the agent settled ("busy"; params.interrupt interrupts
//     it first), no processes it started that would stay behind
//     ("processes"; params.leaveProcesses), the tool on the target
//     ("tool-missing"), the project there or clonable ("no-remote");
//  2. the source checkpoints the folder and packs the bundle
//     (internal/handoff; at most 200 MB, "too-large"), incremental from
//     what the target has; a remote source seals it for this Mac
//     (agents.export, download);
//  3. the target gets it (a remote target as a sealed upload: transfer)
//     and imports it (agents.import): the project found or cloned, a
//     worktree on the branch with the uncommitted work, the conversation
//     placed, the agent resumed with the handover note;
//  4. the source's agent is closed with reason "moved" (agents.close),
//     unless params.fork.

// moveTimeout bounds a move.
var moveTimeout = 10 * time.Minute

// settleWait bounds the wait for an interrupted agent to settle.
var settleWait = 15 * time.Second

// side is one end of a move: this Mac (m nil) or another machine.
type side struct {
	m           *machine
	c           *client.Controller
	transferKey string // the host's, as its snapshot published it
	short       string
}

func (f *Fleet) side(short string) (side, error) {
	if short == f.localShort() {
		if f.opt.Local == nil {
			return side{}, wire.Errorf(wire.CodeUnavailable, "no local registry")
		}
		return side{short: short}, nil
	}
	m, c, err := f.machineByShort(short)
	if err != nil {
		var we *wire.Error
		if errors.As(err, &we) && we.Code == wire.CodeUnavailable {
			return side{}, &wire.Error{Code: wire.CodeOffline, Message: "machine offline: " + we.Message}
		}
		return side{}, err
	}
	f.mu.Lock()
	caps, key := m.state.Capabilities, m.state.TransferKey
	f.mu.Unlock()
	if !caps.Transfer || !caps.Agents || key == "" {
		return side{}, wire.Errorf(wire.CodeUnavailable, "%s does not take moves", short)
	}
	return side{m: m, c: c, transferKey: key, short: short}, nil
}

// settled: states a move takes an agent in (its turn is over).
func settled(state string) bool {
	switch state {
	case wire.StateDone, wire.StateIdle, wire.StateQuestion, wire.StateApproval, wire.StateError, wire.StateExited:
		return true
	}
	return false
}

// Move hands an agent over to another machine.
func (f *Fleet) Move(ctx context.Context, p wire.MoveParams) (wire.MoveResult, error) {
	fromShort, local := splitID(p.ID)
	if fromShort == "" {
		fromShort = f.localShort()
	}
	id := fromShort + "/" + local
	if fromShort == p.To {
		return wire.MoveResult{}, wire.Errorf(wire.CodeInvalid, "%s is already on %s", id, p.To)
	}
	progress := func(m wire.Moving) {
		m.ID, m.To, m.Fork = id, p.To, p.Fork
		if f.opt.Local != nil {
			f.opt.Local.NoteMoving(m)
		}
	}
	res, err := f.move(ctx, p, fromShort, local, progress)
	if err != nil {
		var we *wire.Error
		if !errors.As(err, &we) {
			we = &wire.Error{Code: wire.CodeRemote, Message: err.Error()}
		}
		progress(wire.Moving{Step: wire.MoveFailed, Error: we})
		return wire.MoveResult{}, err
	}
	progress(wire.Moving{Step: wire.MoveDone, Agent: res.Agent})
	return res, nil
}

func (f *Fleet) move(ctx context.Context, p wire.MoveParams, fromShort, local string, progress func(wire.Moving)) (wire.MoveResult, error) {
	src, err := f.side(fromShort)
	if err != nil {
		return wire.MoveResult{}, err
	}
	dst, err := f.side(p.To)
	if err != nil {
		return wire.MoveResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, moveTimeout)
	defer cancel()
	agent, err := f.get(ctx, src, local)
	if err != nil {
		return wire.MoveResult{}, err
	}
	if agent.Kind == wire.KindShell {
		return wire.MoveResult{}, wire.Errorf(wire.CodeInvalid, "a shell does not move")
	}
	// 1. Preflight.
	if !settled(agent.State) {
		if !p.Interrupt {
			return wire.MoveResult{}, wire.Errorf(wire.CodeBusy, "%s is %s: interrupt it to move it now", agent.ID, agent.State)
		}
		if agent, err = f.interrupt(ctx, src, local); err != nil {
			return wire.MoveResult{}, err
		}
	}
	plan, err := f.plan(ctx, src, local)
	if err != nil {
		return wire.MoveResult{}, err
	}
	if len(plan.Processes) > 0 && !p.LeaveProcesses {
		return wire.MoveResult{}, &wire.Error{Code: wire.CodeProcesses, Processes: plan.Processes,
			Message: agent.ID + " started processes that would stay behind on " + fromShort + " (move anyway with leaveProcesses)"}
	}
	probe, err := f.probeMove(ctx, dst, plan, agent.Kind)
	if err != nil {
		return wire.MoveResult{}, err
	}
	if probe.Tool != nil && !*probe.Tool {
		return wire.MoveResult{}, wire.Errorf(wire.CodeToolMissing, "%s has no %s", p.To, agent.Kind)
	}
	if plan.Git && !probe.Exists && plan.Remote == "" && !plan.Scratch { // a scratch project travels whole
		return wire.MoveResult{}, wire.Errorf(wire.CodeNoRemote, "%s is not on %s and has no Git remote to clone it from", filepath.Base(plan.Project), p.To)
	}
	var have []string
	for c, ok := range probe.Has {
		if ok {
			have = append(have, c)
		}
	}
	if !probe.Exists && plan.RemoteHead != "" {
		have = []string{plan.RemoteHead} // the clone will have it
	}
	sort.Strings(have)
	// 2–3. Checkpoint, pack, carry, import.
	moved, err := f.carry(ctx, src, dst, local, have, p, progress)
	if err != nil {
		return wire.MoveResult{}, err
	}
	// 4. The source's agent is closed (moved), unless it forks.
	if !p.Fork {
		if err := f.closeMoved(ctx, src, local, moved.ID); err != nil {
			f.opt.Logf("remote: %s/%s moved to %s, but the source could not close it: %v", fromShort, local, p.To, err)
		}
	}
	return wire.MoveResult{Agent: moved.ID, Moved: moved}, nil
}

// interrupt interrupts a working agent (Esc) and waits until it settled.
func (f *Fleet) interrupt(ctx context.Context, s side, local string) (wire.Agent, error) {
	in := wire.InputParams{ID: local, Text: "\x1b"}
	var err error
	if s.m == nil {
		in.ID = f.localShort() + "/" + local
		err = f.opt.Local.Input(in)
	} else {
		_, err = f.request(ctx, s.c, s.m, "agents.input", in)
	}
	if err != nil {
		return wire.Agent{}, err
	}
	deadline := time.Now().Add(settleWait)
	for {
		a, err := f.get(ctx, s, local)
		if err != nil {
			return a, err
		}
		if settled(a.State) {
			return a, nil
		}
		if time.Now().After(deadline) {
			return a, wire.Errorf(wire.CodeBusy, "%s is still %s after the interrupt", a.ID, a.State)
		}
		select {
		case <-ctx.Done():
			return a, wire.Errorf(wire.CodeBusy, "%s did not settle in time", a.ID)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// carry runs steps 2 and 3.
func (f *Fleet) carry(ctx context.Context, src, dst side, local string, have []string, p wire.MoveParams, progress func(wire.Moving)) (wire.Agent, error) {
	stage, err := os.MkdirTemp(f.opt.StateDir, "move-")
	if err != nil {
		return wire.Agent{}, err
	}
	defer os.RemoveAll(stage)
	os.Chmod(stage, 0o700)
	both := src.m != nil && dst.m != nil
	percent := func(done, total int64, from, span int) {
		if total > 0 {
			progress(wire.Moving{Step: wire.MoveTransfer, Percent: from + int(int64(span)*done/total)})
		}
	}
	// 2. The checkpoint and the bundle, here.
	progress(wire.Moving{Step: wire.MoveCheckpoint})
	bundle := filepath.Join(stage, "bundle")
	if src.m == nil {
		if _, err := f.opt.Local.Pack(ctx, f.localShort()+"/"+local, have, bundle); err != nil {
			return wire.Agent{}, err
		}
	} else {
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return wire.Agent{}, err
		}
		exp, err := src.c.Export(client.RequireE2E(ctx), src.m.id, local, have, key)
		if err != nil {
			return wire.Agent{}, wireError(err)
		}
		progress(wire.Moving{Step: wire.MoveTransfer})
		span := 100
		if both {
			span = 50
		}
		if err := src.c.Download(client.RequireE2E(ctx), src.m.id, src.transferKey, exp, key, bundle,
			func(got, total int64) { percent(got, total, 0, span) }); err != nil {
			return wire.Agent{}, wireError(err)
		}
	}
	if err := markMove(bundle, handoff.MoveInfo{From: src.short, To: dst.short, Fork: p.Fork, Note: true}); err != nil {
		return wire.Agent{}, err
	}
	// 3. The target imports it.
	if dst.m == nil {
		progress(wire.Moving{Step: wire.MoveWorktree})
		a, err := f.opt.Local.Import(ctx, bundle)
		if err != nil {
			return wire.Agent{}, err
		}
		progress(wire.Moving{Step: wire.MoveResume, Agent: a.ID})
		return a, nil
	}
	var files []client.UploadFile
	for _, name := range []string{handoff.ManifestFile, handoff.TranscriptFile, handoff.BundleFile} {
		if _, err := os.Stat(filepath.Join(bundle, name)); err == nil {
			files = append(files, client.UploadFile{Name: name, Path: filepath.Join(bundle, name)})
		}
	}
	var raw [8]byte
	rand.Read(raw[:])
	upload := "mv-" + hex.EncodeToString(raw[:])
	from, span := 0, 100
	if both {
		from, span = 50, 50
	}
	progress(wire.Moving{Step: wire.MoveTransfer, Percent: from})
	if err := dst.c.Upload(client.RequireE2E(ctx), dst.m.id, dst.transferKey, upload, files,
		func(sent, total int64) { percent(sent, total, from, span) }); err != nil {
		return wire.Agent{}, wireError(err)
	}
	progress(wire.Moving{Step: wire.MoveWorktree})
	job, err := dst.c.Spawn(client.RequireE2E(ctx), dst.m.id, upload)
	if err != nil {
		return wire.Agent{}, wireError(err)
	}
	for {
		j, err := dst.c.Job(client.RequireE2E(ctx), dst.m.id, job, false)
		if err != nil {
			return wire.Agent{}, wireError(err)
		}
		if j.Finished() {
			if j.State != "done" || j.Error != nil {
				code, msg := wire.CodeRemote, "the import failed"
				if j.Error != nil {
					code, msg = j.Error.Code, j.Error.Message
				}
				return wire.Agent{}, wireError(&wire.Error{Code: code, Message: msg})
			}
			var a wire.Agent
			if err := json.Unmarshal(j.Result, &a); err != nil {
				return wire.Agent{}, wire.Errorf(wire.CodeRemote, "the import's result is not an agent")
			}
			f.mu.Lock()
			a = dst.m.named(a)
			dst.m.agents[localID(a.ID)] = a
			f.emitChangedLocked(a)
			f.mu.Unlock()
			progress(wire.Moving{Step: wire.MoveResume, Agent: a.ID})
			return a, nil
		}
		select {
		case <-ctx.Done():
			return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "the import did not finish in time")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// markMove adds the move's part to a packed bundle's manifest (older
// targets ignore it).
func markMove(bundle string, info handoff.MoveInfo) error {
	path := filepath.Join(bundle, handoff.ManifestFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return wire.Errorf(wire.CodeRemote, "the bundle's manifest does not read: %v", err)
	}
	fields["move"], _ = json.Marshal(info)
	out, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

func (f *Fleet) get(ctx context.Context, s side, local string) (wire.Agent, error) {
	if s.m == nil {
		return f.opt.Local.Get(f.localShort() + "/" + local)
	}
	f.mu.Lock()
	a, ok := s.m.agents[local]
	f.mu.Unlock()
	if !ok {
		return wire.Agent{}, wire.Errorf(wire.CodeNotFound, "no agent %s/%s", s.short, local)
	}
	return a, nil
}

// closeMoved closes the source's agent with reason "moved" (a host
// without it: a plain close).
func (f *Fleet) closeMoved(ctx context.Context, s side, local, to string) error {
	if s.m == nil {
		_, err := f.opt.Local.CloseAs(f.localShort()+"/"+local, wire.ReasonMoved, to)
		return err
	}
	_, err := f.request(ctx, s.c, s.m, "agents.close", map[string]any{"id": local, "reason": wire.ReasonMoved, "to": to})
	var we *wire.Error
	if errors.As(err, &we) && we.Code == wire.CodeInvalid {
		_, err = f.request(ctx, s.c, s.m, "agents.close", map[string]any{"id": local})
	}
	return err
}

func (f *Fleet) plan(ctx context.Context, s side, local string) (handoff.Plan, error) {
	if s.m == nil {
		return f.opt.Local.Plan(ctx, f.localShort()+"/"+local)
	}
	var plan handoff.Plan
	raw, err := f.request(ctx, s.c, s.m, "agents.plan", map[string]any{"id": local})
	if err == nil {
		err = json.Unmarshal(raw, &plan)
	}
	return plan, err
}

// probeMove asks the target where the project is, which commits it has
// and whether it has the tool (a host without that: the plain probe).
func (f *Fleet) probeMove(ctx context.Context, s side, plan handoff.Plan, kind string) (handoff.Probe, error) {
	commits := slices.Clone(plan.Commits)
	if s.m == nil {
		return f.opt.Local.ProbeMove(ctx, plan.Project, plan.Home, commits, plan.ProjectID, kind), nil
	}
	params := map[string]any{"path": plan.Project, "home": plan.Home, "commits": commits, "kind": kind}
	if plan.ProjectID != "" {
		params["projectId"] = plan.ProjectID
	}
	raw, err := f.request(ctx, s.c, s.m, "agents.probe", params)
	var we *wire.Error
	if errors.As(err, &we) && we.Code == wire.CodeInvalid {
		raw, err = f.request(ctx, s.c, s.m, "agents.probe", map[string]any{"path": plan.Project, "home": plan.Home, "commits": commits})
	}
	var probe handoff.Probe
	if err == nil {
		err = json.Unmarshal(raw, &probe)
	}
	return probe, err
}
