package remote

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/session"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Moves (agents.move): an agent goes from its machine to another with its
// conversation and its code, any two machines (this one included):
//
//  1. the source stops the agent and waits for its process to end, so
//     the conversation file is complete;
//  2. the source reports its project and base commits (agents.plan), the
//     target which of them it has (agents.probe);
//  3. the source packs the bundle (internal/handoff), incremental from
//     those; a remote source seals it for this Mac (agents.export,
//     download: the transfer crypto inside the channel);
//  4. the target gets the bundle (a remote target as a sealed upload:
//     transfer) and imports it (agents.import): code checked out, the
//     uncommitted work restored, the conversation placed, the agent
//     resumed with its session;
//  5. the source's agent is removed. Should anything fail after the stop,
//     the source's agent is resumed instead.

// moveTimeout bounds a move.
var moveTimeout = 10 * time.Minute

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
		return side{}, nil
	}
	m, c, err := f.machineByShort(short)
	if err != nil {
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

// Move hands an agent over to another machine.
func (f *Fleet) Move(ctx context.Context, id, to string) (wire.Agent, error) {
	fromShort, local := splitID(id)
	if fromShort == "" {
		fromShort = f.localShort()
	}
	if fromShort == to {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "%s is already on %s", id, to)
	}
	src, err := f.side(fromShort)
	if err != nil {
		return wire.Agent{}, err
	}
	dst, err := f.side(to)
	if err != nil {
		return wire.Agent{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, moveTimeout)
	defer cancel()
	agent, err := f.get(ctx, src, local)
	if err != nil {
		return wire.Agent{}, err
	}
	if agent.Kind == wire.KindShell {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "a shell does not move")
	}
	wasRunning := agent.Exit == nil
	// 1. Stop it, so its conversation is complete.
	if err := f.stop(ctx, src, local); err != nil {
		return wire.Agent{}, err
	}
	moved, err := f.carry(ctx, src, dst, local)
	if err != nil {
		if wasRunning {
			if _, rerr := f.resume(context.WithoutCancel(ctx), src, local); rerr != nil {
				f.opt.Logf("remote: move of %s failed and it could not be resumed: %v", id, rerr)
			}
		}
		return wire.Agent{}, err
	}
	// 5. The source's agent is gone (moved).
	if err := f.remove(ctx, src, local); err != nil {
		f.opt.Logf("remote: %s moved to %s, but the source could not forget it: %v", id, to, err)
	}
	return moved, nil
}

// carry runs steps 2 to 4.
func (f *Fleet) carry(ctx context.Context, src, dst side, local string) (wire.Agent, error) {
	stage, err := os.MkdirTemp(f.opt.StateDir, "move-")
	if err != nil {
		return wire.Agent{}, err
	}
	defer os.RemoveAll(stage)
	os.Chmod(stage, 0o700)
	// 2. Which commits the target has.
	plan, err := f.plan(ctx, src, local)
	if err != nil {
		return wire.Agent{}, err
	}
	var have []string
	if len(plan.Commits) > 0 {
		probe, err := f.probe(ctx, dst, plan)
		if err != nil {
			return wire.Agent{}, err
		}
		for c, ok := range probe.Has {
			if ok {
				have = append(have, c)
			}
		}
		sort.Strings(have)
	}
	// 3. The bundle, here.
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
		if err := src.c.Download(client.RequireE2E(ctx), src.m.id, src.transferKey, exp, key, bundle, nil); err != nil {
			return wire.Agent{}, wireError(err)
		}
	}
	// 4. The target imports it.
	if dst.m == nil {
		a, err := f.opt.Local.Import(ctx, bundle)
		if err != nil {
			return wire.Agent{}, err
		}
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
	if err := dst.c.Upload(client.RequireE2E(ctx), dst.m.id, dst.transferKey, upload, files, nil); err != nil {
		return wire.Agent{}, wireError(err)
	}
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
			return a, nil
		}
		select {
		case <-ctx.Done():
			return wire.Agent{}, wire.Errorf(wire.CodeUnavailable, "the import did not finish in time")
		case <-time.After(100 * time.Millisecond):
		}
	}
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

func (f *Fleet) stop(ctx context.Context, s side, local string) error {
	if s.m == nil {
		return f.opt.Local.StopWait(f.localShort()+"/"+local, 8*time.Second)
	}
	_, err := f.request(ctx, s.c, s.m, "agents.stop", map[string]any{"id": local, "wait": true})
	return err
}

func (f *Fleet) resume(ctx context.Context, s side, local string) (wire.Agent, error) {
	if s.m == nil {
		return f.opt.Local.Resume(f.localShort() + "/" + local)
	}
	raw, err := f.request(ctx, s.c, s.m, "agents.resume", map[string]any{"id": local})
	var a wire.Agent
	if err == nil {
		err = json.Unmarshal(raw, &a)
	}
	return a, err
}

func (f *Fleet) remove(ctx context.Context, s side, local string) error {
	if s.m == nil {
		return f.opt.Local.Remove(f.localShort() + "/" + local)
	}
	_, err := f.Call(ctx, s.short, "agents.remove", json.RawMessage(`{"id":"`+local+`"}`))
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

func (f *Fleet) probe(ctx context.Context, s side, plan handoff.Plan) (handoff.Probe, error) {
	commits := slices.Clone(plan.Commits)
	if s.m == nil {
		return f.opt.Local.Probe(ctx, plan.Project, plan.Home, commits), nil
	}
	var probe session.Probe
	raw, err := f.request(ctx, s.c, s.m, "agents.probe", map[string]any{"path": plan.Project, "home": plan.Home, "commits": commits})
	if err == nil {
		err = json.Unmarshal(raw, &probe)
	}
	return handoff.Probe{Exists: probe.Exists, Has: probe.Has}, err
}
