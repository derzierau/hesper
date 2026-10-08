package agents

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Bring the folder along (docs/rebuild-contract.md, "As built — bring
// the folder"): the registry's side. The controller (internal/remote,
// Fleet.Bring) asks the source's plan (BringPlan), the target's probe
// (BringProbe: where the folder goes there, whether something is there
// already, the tool), has the source pack (PackFolder: a checkpoint of
// a Git folder's work, else a tar), carries the bundle over and has the
// target make the folder (ImportFolder); then it spawns the agent there.

// BringCheckpoint is the checkpoint id a brought folder's work is kept
// under (refs/hesper/checkpoints/bring in its repository).
const BringCheckpoint = "bring"

// FolderExportPrefix marks agents.export ids that name a folder to
// bring: "folder:with:<path>" (its uncommitted work along) or
// "folder:clean:<path>".
const FolderExportPrefix = "folder:"

// FolderExport is the agents.export id of the folder at path.
func FolderExport(path string, changes bool) string {
	if changes {
		return FolderExportPrefix + wire.BringWith + ":" + path
	}
	return FolderExportPrefix + wire.BringClean + ":" + path
}

// ParseFolderExport reads a FolderExport id.
func ParseFolderExport(id string) (path string, changes bool, err error) {
	rest, ok := strings.CutPrefix(id, FolderExportPrefix)
	mode, path, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || mode != wire.BringWith && mode != wire.BringClean || !filepath.IsAbs(path) || len(path) > 4096 {
		return "", false, wire.Errorf(wire.CodeInvalid, "a folder export is folder:with:<path> or folder:clean:<path>")
	}
	return path, mode == wire.BringWith, nil
}

// BringResult is what the target made: the folder and its project.
type BringResult struct {
	Path      string `json:"path"`
	ProjectID string `json:"projectId,omitempty"`
}

// BringProbe is the target's answer to a plan: where the folder goes,
// whether that is taken (never overwritten), whether the agent's tool is
// here (nil for a shell).
type BringProbe struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Tool   *bool  `json:"tool,omitempty"`
}

// bringSource checks a folder this Mac may bring elsewhere: an existing
// folder below the home, not the home, none of its folders below the
// home hidden (~/.ssh and the like never travel).
func (r *Registry) bringSource(path string) (string, error) {
	dir := cleanPath(path)
	if dir == "" || !filepath.IsAbs(dir) {
		return "", wire.Errorf(wire.CodeInvalid, "path must be an absolute path")
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", wire.Errorf(wire.CodeNotFound, "no folder %s on %s", dir, r.machine)
	}
	home := strings.TrimRight(r.opt.Home, "/")
	rel, ok := strings.CutPrefix(dir, home+"/")
	if home == "" || !ok || rel == "" {
		return "", wire.Errorf(wire.CodeInvalid, "only folders in the home folder are brought (%s is not)", dir)
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") {
			return "", wire.Errorf(wire.CodeInvalid, "hidden folders are not brought (%s)", dir)
		}
	}
	return dir, nil
}

// BringPlan plans bringing the folder at path elsewhere (its source).
func (r *Registry) BringPlan(ctx context.Context, path string) (handoff.BringPlan, error) {
	dir, err := r.bringSource(path)
	if err != nil {
		return handoff.BringPlan{}, err
	}
	plan, err := handoff.PlanFolder(ctx, dir, r.HandoffPaths())
	if err != nil {
		return plan, asMoveError(err)
	}
	if plan.Git {
		plan.ProjectID = r.projectOf(plan.Path)
		if sp := r.scratch(); sp != nil && plan.ProjectID != "" {
			_, _, _, plan.Scratch = sp.ScratchOf(plan.ProjectID)
		}
	}
	return plan, nil
}

// bringDest is where a planned folder goes here.
func (r *Registry) bringDest(plan handoff.BringPlan) string {
	scratchRoot := ""
	if sp := r.scratch(); sp != nil {
		scratchRoot = sp.ScratchRoot()
	}
	return handoff.BringDest(plan, r.opt.Home, r.opt.ProjectsRoot, scratchRoot)
}

// BringProbe answers a plan as the target, for an agent of kind or
// profile (both empty: the default).
func (r *Registry) BringProbe(plan handoff.BringPlan, kind, profile string) (BringProbe, error) {
	if !filepath.IsAbs(plan.Path) || filepath.Clean(plan.Path) != plan.Path || plan.Home != "" && !filepath.IsAbs(plan.Home) ||
		plan.Name == "" || plan.Name != filepath.Base(plan.Name) || plan.Name == "." || plan.Name == ".." {
		return BringProbe{}, wire.Errorf(wire.CodeInvalid, "bad folder plan")
	}
	probe := BringProbe{Path: r.bringDest(plan)}
	if _, err := os.Lstat(probe.Path); err == nil {
		probe.Exists = true
	}
	r.mu.Lock()
	profiles, defaults := r.profiles, r.settings.Defaults
	r.mu.Unlock()
	_, prof, err := resolveProfile(profiles, defaults, profile, kind, "")
	if err != nil {
		return probe, err
	}
	if prof.Kind != wire.KindShell {
		ok := r.hasCommand(prof)
		probe.Tool = &ok
	}
	return probe, nil
}

// PackFolder writes the bring bundle of the folder at path into dir: a
// Git folder's work (changes) checkpointed (BringCheckpoint) and carried
// incremental from have, any other folder as a tar (at most
// handoff.MaxFolderBytes). Over MaxMoveBytes it is "too-large".
func (r *Registry) PackFolder(ctx context.Context, path string, changes bool, have []string, dir string) (*handoff.Manifest, error) {
	plan, err := r.BringPlan(ctx, path)
	if err != nil {
		return nil, err
	}
	paths := r.HandoffPaths()
	checkpoint := ""
	if plan.Git && changes {
		r.cpRun.Lock()
		info, err := handoff.TakeCheckpoint(ctx, plan.Path, BringCheckpoint, paths)
		r.cpRun.Unlock()
		if err != nil {
			return nil, asMoveError(err)
		}
		if info != nil {
			checkpoint = info.Commit
			if !info.Unchanged {
				r.noteCheckpointRepo(info.Repo)
			}
		}
	}
	m, err := handoff.PackFolder(ctx, plan, r.machine, have, dir, paths, changes, checkpoint)
	if err != nil {
		return nil, asMoveError(err)
	}
	if sp := r.scratch(); sp != nil && plan.Scratch && m.Project.Bundle != "" {
		if local, name, created, ok := sp.ScratchOf(plan.ProjectID); ok {
			m.Project.Scratch = &handoff.ScratchInfo{Local: local, Name: name, Created: created}
			if err := handoff.WriteManifest(dir, m); err != nil {
				return nil, err
			}
		}
	}
	if size := dirSize(dir); size > MaxMoveBytes {
		for _, name := range []string{handoff.ManifestFile, handoff.BundleFile, handoff.FolderFile} {
			os.Remove(filepath.Join(dir, name))
		}
		return nil, wire.Errorf(wire.CodeTooLarge, "%s is %d MB, over the %d MB a bring carries", plan.Name, size>>20, MaxMoveBytes>>20)
	}
	return m, nil
}

// ImportFolder makes a bring bundle's folder (dir) here, where
// BringProbe said: built next to it and renamed into place while that
// is still free ("exists" otherwise; nothing is overwritten). The
// folder's project is recorded on this machine: a scratch project's as
// a copy (its home stays the source), any other through Resolve.
func (r *Registry) ImportFolder(ctx context.Context, dir string) (BringResult, error) {
	m, err := handoff.LoadManifest(dir)
	if err != nil {
		return BringResult{}, asMoveError(err)
	}
	if m.Bring == nil {
		return BringResult{}, wire.Errorf(wire.CodeInvalid, "not a brought folder")
	}
	name := filepath.Base(m.Project.Path)
	plan := handoff.BringPlan{Path: m.Project.Path, Home: m.Source.Home, Name: name, Git: m.Bring.Kind == handoff.BringGit,
		Remote: m.Project.Remote, Scratch: m.Project.Scratch != nil}
	dest := r.bringDest(plan)
	if _, err := os.Lstat(dest); err == nil {
		return BringResult{}, &wire.Error{Code: wire.CodeExists, Message: r.machine + " already has " + dest, Path: dest}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return BringResult{}, err
	}
	temp, err := os.MkdirTemp(filepath.Dir(dest), "."+name+".bring-")
	if err != nil {
		return BringResult{}, err
	}
	defer os.RemoveAll(temp) // gone once renamed
	os.Chmod(temp, 0o755)
	if err := handoff.UnpackFolder(ctx, m, dir, temp, r.HandoffPaths()); err != nil {
		return BringResult{}, asMoveError(err)
	}
	if _, err := os.Lstat(dest); err == nil {
		return BringResult{}, &wire.Error{Code: wire.CodeExists, Message: r.machine + " already has " + dest, Path: dest}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return BringResult{}, err
	}
	if err := os.Rename(temp, dest); err != nil {
		return BringResult{}, err
	}
	res := BringResult{Path: dest}
	if sc := m.Project.Scratch; sc != nil && r.scratch() != nil {
		res.ProjectID = r.scratch().ScratchArrived(sc.Local, sc.Name, sc.Created, dest, false)
	} else {
		res.ProjectID = r.projectOf(dest)
	}
	r.mu.Lock()
	r.touchProject(dest, time.Now().UTC())
	r.mu.Unlock()
	return res, nil
}

// Bringer is the Remote's side of a bring (internal/remote's Fleet).
type Bringer interface {
	Bring(ctx context.Context, p wire.SpawnParams) (wire.Agent, error)
}

// BringTimeout bounds a bring with its spawn.
var BringTimeout = 10 * time.Minute

// bring is agents.spawn with params.bring: from the folder's machine to
// the spawn's, through the Remote. A bring from the machine itself is a
// plain spawn of that folder.
func (s *Server) bring(p wire.SpawnParams) (any, error) {
	reg := s.reg
	b := *p.Bring
	switch b.Changes {
	case "", wire.BringWith, wire.BringClean:
	default:
		return nil, &badParams{errors.New(`bring.changes must be "with" or "clean"`)}
	}
	if b.Path == "" {
		b.Path = p.Project
	}
	if b.Draft == "" {
		b.Draft = p.Draft
	}
	p.Draft = ""
	if b.Path == "" {
		return nil, &badParams{errors.New("bring.path (or project) is required")}
	}
	from, to := b.From, p.Machine
	if from == "" {
		from = reg.machine
	}
	if to == "" {
		to = reg.machine
	}
	if from == to {
		p.Bring, p.Project = nil, b.Path
		if to != reg.machine {
			raw, err := json.Marshal(p)
			if err != nil {
				return nil, err
			}
			return s.call("agents.spawn", raw)
		}
		return reg.Spawn(p)
	}
	bringer, ok := reg.opt.Remote.(Bringer)
	if !ok {
		return nil, wire.Errorf(wire.CodeUnavailable, "bringing a folder to another machine needs the remote connection (part R)")
	}
	b.From, p.Bring, p.Machine = from, &b, to
	ctx, cancel := context.WithTimeout(context.Background(), BringTimeout)
	defer cancel()
	return bringer.Bring(ctx, p)
}
