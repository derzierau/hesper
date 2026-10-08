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
	"strings"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/client"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Bring the folder along (agents.spawn with params.bring; docs/
// rebuild-contract.md, "As built — bring the folder"): the spawn's
// machine lacks the folder, so it is brought there from its machine
// first, any two machines (this one included). This daemon orchestrates
// and tells its subscribers the progress (agents.bringing):
//
//  1. preflight, nothing written on failure: both machines reachable
//     ("offline"), the source's plan (bring.plan: the folder, its Git
//     remote, a scratch project), the target's probe (bring.probe: where
//     the folder goes there — "exists" when something is, never
//     overwritten — and its tool, "tool-missing");
//  2. checkpoint: the source packs the folder (a checkpoint of a Git
//     folder's work as the handoff commit, incremental from the remote's
//     head when the target clones; else a tar, at most 100 MB,
//     "too-large"); a remote source seals it for this Mac
//     (agents.export "folder:…", download);
//  3. transfer and unpack: the target gets it (a remote one as a sealed
//     upload) and makes the folder (agents.import);
//  4. spawn: the agent starts there in that folder (agents.spawn).

// Bring brings p.Bring's folder to p.Machine and spawns the agent there.
func (f *Fleet) Bring(ctx context.Context, p wire.SpawnParams) (wire.Agent, error) {
	b := *p.Bring
	var raw [6]byte
	rand.Read(raw[:])
	id := "br-" + hex.EncodeToString(raw[:])
	progress := func(x wire.Bringing) {
		x.ID, x.Draft, x.To, x.From = id, b.Draft, p.Machine, b.From
		if f.opt.Local != nil {
			f.opt.Local.NoteBringing(x)
		}
	}
	a, err := f.bring(ctx, p, progress)
	if err != nil {
		var we *wire.Error
		if !errors.As(err, &we) {
			we = &wire.Error{Code: wire.CodeRemote, Message: err.Error()}
			err = we
		}
		progress(wire.Bringing{Step: wire.MoveFailed, Error: we})
		return wire.Agent{}, err
	}
	progress(wire.Bringing{Step: wire.MoveDone, Agent: a.ID, Path: a.Project})
	return a, nil
}

func (f *Fleet) bring(ctx context.Context, p wire.SpawnParams, progress func(wire.Bringing)) (wire.Agent, error) {
	b := *p.Bring
	if b.From == p.Machine {
		return wire.Agent{}, wire.Errorf(wire.CodeInvalid, "the folder is on %s already", p.Machine)
	}
	src, err := f.side(b.From)
	if err != nil {
		return wire.Agent{}, err
	}
	dst, err := f.side(p.Machine)
	if err != nil {
		return wire.Agent{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, moveTimeout)
	defer cancel()
	changes := b.Changes != wire.BringClean
	// 1. Preflight.
	plan, err := f.bringPlan(ctx, src, b.Path)
	if err != nil {
		return wire.Agent{}, err
	}
	probe, err := f.bringProbe(ctx, dst, plan, p.Kind, p.Profile)
	if err != nil {
		return wire.Agent{}, err
	}
	if probe.Exists {
		return wire.Agent{}, &wire.Error{Code: wire.CodeExists, Message: p.Machine + " already has " + probe.Path, Path: probe.Path}
	}
	if probe.Tool != nil && !*probe.Tool {
		kind := p.Kind
		if kind == "" {
			kind = "the agent's tool"
		}
		return wire.Agent{}, wire.Errorf(wire.CodeToolMissing, "%s has no %s", p.Machine, kind)
	}
	var have []string
	if plan.Git && plan.Remote != "" && !plan.Scratch && plan.RemoteHead != "" {
		have = []string{plan.RemoteHead} // the target's clone has it
	}
	// 2–3. Pack, carry, unpack.
	res, err := f.carryFolder(ctx, src, dst, plan.Path, changes, have, progress)
	if err != nil {
		return wire.Agent{}, err
	}
	// 4. The agent, in the folder there.
	progress(wire.Bringing{Step: wire.BringSpawn, Path: res.Path})
	sp := p
	sp.Bring, sp.Project, sp.Scratch = nil, res.Path, false
	if dst.m == nil {
		sp.Machine = ""
		return f.opt.Local.Spawn(sp)
	}
	params, err := json.Marshal(sp)
	if err != nil {
		return wire.Agent{}, err
	}
	out, err := f.Call(ctx, p.Machine, "agents.spawn", params)
	if err != nil {
		return wire.Agent{}, err
	}
	var a wire.Agent
	if err := json.Unmarshal(out, &a); err != nil {
		return wire.Agent{}, wire.Errorf(wire.CodeRemote, "the spawn's result is not an agent")
	}
	return a, nil
}

// carryFolder runs steps 2 and 3.
func (f *Fleet) carryFolder(ctx context.Context, src, dst side, path string, changes bool, have []string,
	progress func(wire.Bringing)) (agents.BringResult, error) {
	var res agents.BringResult
	stage, err := os.MkdirTemp(f.opt.StateDir, "bring-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(stage)
	os.Chmod(stage, 0o700)
	both := src.m != nil && dst.m != nil
	var size int64
	percent := func(done, total int64, from, span int) {
		if total > 0 {
			if size == 0 {
				size = total
			}
			progress(wire.Bringing{Step: wire.BringTransfer, Percent: from + int(int64(span)*done/total), Total: size,
				Bytes: legDone(size, done, total, from, span)})
		}
	}
	progress(wire.Bringing{Step: wire.BringCheckpoint})
	bundle := filepath.Join(stage, "bundle")
	if src.m == nil {
		if _, err := f.opt.Local.PackFolder(ctx, path, changes, have, bundle); err != nil {
			return res, err
		}
	} else {
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return res, err
		}
		exp, err := src.c.Export(client.RequireE2E(ctx), src.m.id, agents.FolderExport(path, changes), have, key)
		if err != nil {
			return res, wireError(err)
		}
		size = exp.Size()
		progress(wire.Bringing{Step: wire.BringTransfer, Total: size})
		span := 100
		if both {
			span = 50
		}
		if err := src.c.Download(client.RequireE2E(ctx), src.m.id, src.transferKey, exp, key, bundle,
			func(got, total int64) { percent(got, total, 0, span) }); err != nil {
			return res, wireError(err)
		}
	}
	if dst.m == nil {
		progress(wire.Bringing{Step: wire.BringUnpack})
		return f.opt.Local.ImportFolder(ctx, bundle)
	}
	var files []client.UploadFile
	for _, name := range []string{handoff.ManifestFile, handoff.BundleFile, handoff.FolderFile} {
		if _, err := os.Stat(filepath.Join(bundle, name)); err == nil {
			files = append(files, client.UploadFile{Name: name, Path: filepath.Join(bundle, name)})
		}
	}
	var raw [8]byte
	rand.Read(raw[:])
	upload := "br-" + hex.EncodeToString(raw[:])
	from, span := 0, 100
	if both {
		from, span = 50, 50
	}
	if size == 0 {
		size = filesSize(files)
	}
	progress(wire.Bringing{Step: wire.BringTransfer, Percent: from, Total: size, Bytes: size * int64(from) / 100})
	if err := dst.c.Upload(client.RequireE2E(ctx), dst.m.id, dst.transferKey, upload, files,
		func(sent, total int64) { percent(sent, total, from, span) }); err != nil {
		return res, wireError(err)
	}
	progress(wire.Bringing{Step: wire.BringUnpack})
	result, err := f.importUpload(ctx, dst, upload)
	if err != nil {
		return res, err
	}
	if err := json.Unmarshal(result, &res); err != nil || res.Path == "" {
		return res, wire.Errorf(wire.CodeRemote, "%s did not make the folder (update hesperd there)", dst.short)
	}
	return res, nil
}

// bringPlan asks the source about the folder.
func (f *Fleet) bringPlan(ctx context.Context, s side, path string) (handoff.BringPlan, error) {
	if s.m == nil {
		return f.opt.Local.BringPlan(ctx, path)
	}
	var plan handoff.BringPlan
	raw, err := f.request(ctx, s.c, s.m, "bring.plan", map[string]any{"path": path})
	if err != nil {
		return plan, unsupported(err, s.short)
	}
	if err := json.Unmarshal(raw, &plan); err != nil || plan.Path == "" {
		return plan, wire.Errorf(wire.CodeRemote, "%s's plan does not read", s.short)
	}
	return plan, nil
}

// bringProbe asks the target where the folder goes.
func (f *Fleet) bringProbe(ctx context.Context, s side, plan handoff.BringPlan, kind, profile string) (agents.BringProbe, error) {
	if s.m == nil {
		return f.opt.Local.BringProbe(plan, kind, profile)
	}
	var probe agents.BringProbe
	params := map[string]any{"plan": plan}
	if kind != "" {
		params["kind"] = kind
	}
	if profile != "" {
		params["profile"] = profile
	}
	raw, err := f.request(ctx, s.c, s.m, "bring.probe", params)
	if err != nil {
		return probe, unsupported(err, s.short)
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Path == "" {
		return probe, wire.Errorf(wire.CodeRemote, "%s's probe does not read", s.short)
	}
	return probe, nil
}

// unsupported: a host without bring answers an unknown method.
func unsupported(err error, short string) error {
	var we *wire.Error
	if errors.As(err, &we) && (we.Code == wire.CodeRemote || we.Code == wire.CodeForbidden && strings.Contains(we.Message, "Unknown method")) {
		return wire.Errorf(wire.CodeUnavailable, "%s cannot take a brought folder (%s; update hesperd there)", short, we.Message)
	}
	return err
}

var _ agents.Bringer = (*Fleet)(nil)
