package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Scratch projects (docs/rebuild-contract.md, "As built — scratch
// projects"): a project of kind scratch is a folder <scratch
// root>/<yyyy-mm-dd>-<slug> on the Mac it was made on (its home), always
// a local Git repository (an empty first commit, no remote), with a
// lifecycle: active while agents run in it, resting without, archived
// (moved to <scratch root>/.archive) after resting archiveAfterDays,
// deleted (folder and catalog entry) after archived deleteAfterDays;
// keep pins it. Only its home archives, restores, deletes and promotes
// it: another Mac forwards those to it. Date-prefixed folders already in
// the scratch root are adopted.

// ScratchRecord is a scratch project's replicated lifecycle: Home (the
// node whose folder it is), Keep, ArchivedAt (Unix milliseconds; 0: not
// archived), Git (its folder is a repository) and AdoptedAt (Unix
// milliseconds: an adopted folder rests from then at the earliest, so the
// first start does not archive every old folder at once).
type ScratchRecord struct {
	Home       Field[string] `json:"home"`
	Keep       Field[bool]   `json:"keep"`
	ArchivedAt Field[int64]  `json:"archivedAt"`
	Git        Field[bool]   `json:"git"`
	AdoptedAt  Field[int64]  `json:"adoptedAt"`
}

func (x *ScratchRecord) merge(o *ScratchRecord) bool {
	changed := x.Home.merge(o.Home)
	changed = x.Keep.merge(o.Keep) || changed
	changed = x.ArchivedAt.merge(o.ArchivedAt) || changed
	changed = x.Git.merge(o.Git) || changed
	changed = x.AdoptedAt.merge(o.AdoptedAt) || changed
	return changed
}

func (x *ScratchRecord) stamps(fn func(Stamp)) {
	fn(x.Home.S)
	fn(x.Keep.S)
	fn(x.ArchivedAt.S)
	fn(x.Git.S)
	fn(x.AdoptedAt.S)
}

const (
	defaultArchiveDays = 14
	defaultDeleteDays  = 30
	// archiveDir is where archived scratch folders go, in the scratch
	// root.
	archiveDir = ".archive"
	// scratchSweepEvery is how often the lifecycle runs (and on start).
	scratchSweepEvery = 24 * time.Hour
	// liveEvery is how often the scratch projects' states follow the
	// agents.
	liveEvery = 30 * time.Second
	ghTimeout = 2 * time.Minute
)

var (
	scratchFolder = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})(?:-(.+))?$`)
	slugWord      = regexp.MustCompile(`[a-z0-9]+`)
	localIdentity = regexp.MustCompile(`^l-[0-9a-f]{1,64}$`)
)

// scratchSlug is text's words (lower-case letters and digits) joined by
// "-", whole words up to limit characters; "" without any.
func scratchSlug(text string, limit int) string {
	slug := ""
	for _, w := range slugWord.FindAllString(strings.ToLower(text), -1) {
		candidate := w
		if slug != "" {
			candidate = slug + "-" + w
		}
		if len(candidate) > limit {
			if slug == "" {
				slug = w[:limit]
			}
			break
		}
		slug = candidate
	}
	return slug
}

// scratchName is a new scratch's name and slug: the name given, else
// the first words of the task's first line ("csv cleanup").
func scratchName(name, task string) (string, string, error) {
	if strings.TrimSpace(name) != "" {
		display, err := cleanName(name)
		if err != nil {
			return "", "", err
		}
		slug := scratchSlug(display, 40)
		if slug == "" {
			slug = "scratch"
		}
		return display, slug, nil
	}
	first, _, _ := strings.Cut(strings.TrimSpace(task), "\n")
	slug := scratchSlug(first, 40)
	if slug == "" {
		return "", "", wire.Errorf(wire.CodeInvalid, "a scratch project needs a name or a task")
	}
	return strings.ReplaceAll(slug, "-", " "), slug, nil
}

// --- settings ---

// loadScratchSettings reads settings.json's "scratch" (defaults 14 and
// 30 days).
func loadScratchSettings(dir string, logf func(string, ...any)) wire.ScratchSettings {
	cfg := wire.ScratchSettings{ArchiveAfterDays: defaultArchiveDays, DeleteAfterDays: defaultDeleteDays}
	if dir == "" {
		return cfg
	}
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return cfg
	}
	var f struct {
		Scratch *wire.ScratchSettings `json:"scratch"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		logf("hesperd: settings.json: %v (scratch projects keep their defaults)", err)
		return cfg
	}
	if f.Scratch != nil && f.Scratch.ArchiveAfterDays > 0 {
		cfg.ArchiveAfterDays = f.Scratch.ArchiveAfterDays
	}
	if f.Scratch != nil && f.Scratch.DeleteAfterDays > 0 {
		cfg.DeleteAfterDays = f.Scratch.DeleteAfterDays
	}
	return cfg
}

// ScratchSettings is projects.scratchSettings without changes.
func (s *Store) ScratchSettings() wire.ScratchSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scratchCfg
}

// SetScratchSettings is projects.scratchSettings: the days given (1 to
// 3650; 0 keeps) change, in settings.json's "scratch" too (its other
// keys kept).
func (s *Store) SetScratchSettings(p wire.ScratchSettings) (wire.ScratchSettings, error) {
	for _, d := range []int{p.ArchiveAfterDays, p.DeleteAfterDays} {
		if d < 0 || d > 3650 {
			return wire.ScratchSettings{}, wire.Errorf(wire.CodeInvalid, "days must be 1-3650")
		}
	}
	s.scratchMu.Lock()
	defer s.scratchMu.Unlock()
	s.mu.Lock()
	cfg := s.scratchCfg
	s.mu.Unlock()
	if p.ArchiveAfterDays > 0 {
		cfg.ArchiveAfterDays = p.ArchiveAfterDays
	}
	if p.DeleteAfterDays > 0 {
		cfg.DeleteAfterDays = p.DeleteAfterDays
	}
	if s.opt.ConfigDir != "" {
		path := filepath.Join(s.opt.ConfigDir, "settings.json")
		fields := map[string]json.RawMessage{}
		perm := os.FileMode(0o600)
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &fields); err != nil {
				return wire.ScratchSettings{}, wire.Errorf(wire.CodeInvalid, "settings.json does not read: %v", err)
			}
			if st, err := os.Stat(path); err == nil {
				perm = st.Mode().Perm()
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return wire.ScratchSettings{}, err
		}
		fields["scratch"], _ = json.Marshal(cfg)
		data, err := json.MarshalIndent(fields, "", "  ")
		if err != nil {
			return wire.ScratchSettings{}, err
		}
		if err := os.MkdirAll(s.opt.ConfigDir, 0o700); err != nil {
			return wire.ScratchSettings{}, err
		}
		if err := atomicWrite(path, append(data, '\n'), perm); err != nil {
			return wire.ScratchSettings{}, fmt.Errorf("writing settings.json: %w", err)
		}
	}
	s.mu.Lock()
	s.scratchCfg = cfg
	s.mu.Unlock()
	return cfg, nil
}

// SettingsGet is settings.get: the scratch settings asked for (no keys:
// both); unknown keys are left out.
func (s *Store) SettingsGet(keys []string) wire.SettingsValues {
	cfg := s.ScratchSettings()
	all := map[string]int{wire.SettingScratchArchiveDays: cfg.ArchiveAfterDays, wire.SettingScratchDeleteDays: cfg.DeleteAfterDays}
	if len(keys) == 0 {
		return wire.SettingsValues{Values: all}
	}
	out := wire.SettingsValues{Values: map[string]int{}}
	for _, k := range keys {
		if v, ok := all[k]; ok {
			out.Values[k] = v
		}
	}
	return out
}

// SettingsSet is settings.set: the scratch settings given (1–3650);
// another key is invalid. It answers the values after the change.
func (s *Store) SettingsSet(values map[string]int) (wire.SettingsValues, error) {
	var p wire.ScratchSettings
	for k, v := range values {
		if v < 1 || v > 3650 {
			return wire.SettingsValues{}, wire.Errorf(wire.CodeInvalid, "%s must be 1-3650", k)
		}
		switch k {
		case wire.SettingScratchArchiveDays:
			p.ArchiveAfterDays = v
		case wire.SettingScratchDeleteDays:
			p.DeleteAfterDays = v
		default:
			return wire.SettingsValues{}, wire.Errorf(wire.CodeInvalid, "unknown setting %q", k)
		}
	}
	if _, err := s.SetScratchSettings(p); err != nil {
		return wire.SettingsValues{}, err
	}
	return s.SettingsGet(nil), nil
}

// --- views ---

func (s *Store) archivedLocked(r *Record) bool {
	return r.Kind.V == wire.ProjectScratch && r.Scratch != nil && r.Scratch.ArchivedAt.V > 0
}

// archivedOrigin is where an archived scratch's folder was on this Mac
// before it went to the archive ("" for anything else).
func (s *Store) archivedOrigin(r *Record) string {
	if !s.archivedLocked(r) {
		return ""
	}
	return originOf(r.Paths[s.node].V)
}

// originOf is the folder an archived folder (<root>/.archive/<name>)
// came from: <root>/<name>; "" for others.
func originOf(p string) string {
	if p == "" || filepath.Base(filepath.Dir(p)) != archiveDir {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Dir(p)), filepath.Base(p))
}

func (s *Store) scratchViewLocked(r *Record) *wire.ScratchInfo {
	info := &wire.ScratchInfo{State: wire.ScratchResting}
	if x := r.Scratch; x != nil {
		info.Keep, info.Git = x.Keep.V, x.Git.V
		if x.Home.V != "" {
			info.Home = s.nameOf(x.Home.V)
		}
		if x.ArchivedAt.V > 0 {
			at := time.UnixMilli(x.ArchivedAt.V).UTC()
			info.State, info.ArchivedAt = wire.ScratchArchived, &at
			return info
		}
	}
	if s.live[r.ID] {
		info.State = wire.ScratchActive
	}
	return info
}

// liveAgent: an agent whose process runs (or will: starting).
func liveAgent(a wire.Agent) bool { return a.State != wire.StateExited && a.Exit == nil }

// liveInLocked: a live agent works in the scratch project (its project
// id, or this Mac's agent in its folder).
func (s *Store) liveInLocked(r *Record, agents []wire.Agent) bool {
	p := r.Paths[s.node].V
	for _, a := range agents {
		if !liveAgent(a) {
			continue
		}
		if a.ProjectID == r.ID {
			return true
		}
		if p != "" && a.Machine == s.short && (a.Project != "" && within(a.Project, p) || a.Worktree != "" && within(a.Worktree, p)) {
			return true
		}
	}
	return false
}

// setLiveLocked notes which scratch projects agents run in.
func (s *Store) setLiveLocked(agents []wire.Agent) {
	live := map[string]bool{}
	for _, r := range s.projects {
		if !r.Deleted.V && r.Kind.V == wire.ProjectScratch && s.liveInLocked(r, agents) {
			live[r.ID] = true
		}
	}
	s.live = live
}

// agentsNow is every agent, asked without the store's lock.
func (s *Store) agentsNow() []wire.Agent {
	s.mu.Lock()
	fn := s.agents
	s.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// refreshLive makes the scratch projects' states follow the agents.
func (s *Store) refreshLive() {
	agents := s.agentsNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.setLiveLocked(agents)
	s.emitLocked()
}

// --- methods ---

func (s *Store) callScratch(method string, params json.RawMessage) (any, error) {
	if len(params) == 0 || string(params) == "null" {
		params = []byte("{}")
	}
	decode := func(v any) error {
		if err := json.Unmarshal(params, v); err != nil {
			return wire.Errorf(wire.CodeInvalid, "invalid params: %v", err)
		}
		return nil
	}
	if method == "projects.scratchSettings" {
		var p wire.ScratchSettings
		if err := decode(&p); err != nil {
			return nil, err
		}
		if p == (wire.ScratchSettings{}) {
			return s.ScratchSettings(), nil
		}
		return s.SetScratchSettings(p)
	}
	if method == "projects.scratch" {
		var p wire.ScratchParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		return s.NewScratch(p)
	}
	if method == "projects.scratchKeep" {
		var p wire.ScratchKeepParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		return s.KeepScratch(p.ID, p.Keep)
	}
	var p wire.IDParams
	if err := decode(&p); err != nil {
		return nil, err
	}
	switch method {
	case "projects.scratchArchive":
		return s.ArchiveScratch(p.ID)
	case "projects.scratchRestore":
		return s.RestoreScratch(p.ID)
	}
	return struct{}{}, s.DeleteScratch(p.ID)
}

// scratchLocked is a live scratch project.
func (s *Store) scratchLocked(id string) (*Record, error) {
	r, err := s.liveLocked(id)
	if err != nil {
		return nil, err
	}
	if r.Kind.V != wire.ProjectScratch || r.Scratch == nil {
		return nil, wire.Errorf(wire.CodeInvalid, "%s is not a scratch project", id)
	}
	return r, nil
}

// scratchHomeLocked is the machine a scratch project's folder operations
// run on: "" for this Mac, else this Mac's name for its home.
func (s *Store) scratchHomeLocked(id string) (string, error) {
	r, err := s.scratchLocked(id)
	if err != nil {
		return "", err
	}
	home := r.Scratch.Home.V
	if home == "" || home == s.node {
		return "", nil
	}
	return s.nameOf(home), nil
}

// atHome runs a folder operation of a scratch project on its home: here
// (local), else as the host method of the same name there, whose state
// is merged here.
func (s *Store) atHome(id, method string, params any, local func() error) error {
	s.mu.Lock()
	home, err := s.scratchHomeLocked(id)
	forward := s.forward
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if home == "" {
		return local()
	}
	if forward == nil {
		return wire.Errorf(wire.CodeUnavailable, "machine %s (the scratch project's home) is not connected", home)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	raw, err := forward(ctx, home, method, params)
	if err != nil {
		return err
	}
	var res PromoteResult
	if json.Unmarshal(raw, &res) == nil && res.State != nil {
		s.Merge(res.State, home)
	}
	return nil
}

// ScratchForPeer runs a scratch method another machine's daemon sent
// (host method; this Mac is the scratch's home): the id, the new folder
// (projects.scratch) and this daemon's state.
func (s *Store) ScratchForPeer(method string, params json.RawMessage) (PromoteResult, error) {
	if s.opt.ScratchRoot == "" {
		return PromoteResult{}, wire.Errorf(wire.CodeUnavailable, "scratch projects are off on this Mac")
	}
	var res PromoteResult
	var err error
	switch method {
	case "projects.scratch":
		var p wire.ScratchParams
		if err := json.Unmarshal(params, &p); err != nil {
			return PromoteResult{}, wire.Errorf(wire.CodeInvalid, "invalid params: %v", err)
		}
		res.ID, res.Path, err = s.CreateScratch(p.Name, p.Task)
	case "projects.scratchArchive", "projects.scratchRestore", "projects.scratchDelete":
		var p wire.IDParams
		if err := json.Unmarshal(params, &p); err != nil {
			return PromoteResult{}, wire.Errorf(wire.CodeInvalid, "invalid params: %v", err)
		}
		res.ID = p.ID
		switch method {
		case "projects.scratchArchive":
			err = s.archiveLocal(p.ID, s.opt.Now(), true)
		case "projects.scratchRestore":
			err = s.restoreLocal(p.ID)
		default:
			err = s.deleteLocal(p.ID, true)
		}
	default:
		return PromoteResult{}, wire.Errorf(wire.CodeInvalid, "unknown method %s", method)
	}
	if err != nil {
		return PromoteResult{}, err
	}
	res.State = s.Export()
	return res, nil
}

// NewScratch is projects.scratch: a new scratch project on p.Machine
// (default this Mac).
func (s *Store) NewScratch(p wire.ScratchParams) (wire.ScratchResult, error) {
	s.mu.Lock()
	short, forward := s.short, s.forward
	s.mu.Unlock()
	if p.Machine != "" && p.Machine != short {
		if forward == nil {
			return wire.ScratchResult{}, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", p.Machine)
		}
		if _, _, err := scratchName(p.Name, p.Task); err != nil {
			return wire.ScratchResult{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		machine := p.Machine
		p.Machine = ""
		raw, err := forward(ctx, machine, "projects.scratch", p)
		if err != nil {
			return wire.ScratchResult{}, err
		}
		var res PromoteResult
		if err := json.Unmarshal(raw, &res); err != nil || res.State == nil {
			return wire.ScratchResult{}, wire.Errorf(wire.CodeRemote, "%s answered projects.scratch without a project", machine)
		}
		s.Merge(res.State, machine)
		info, err := s.Get(res.ID)
		return wire.ScratchResult{Project: info, Path: res.Path}, err
	}
	id, path, err := s.CreateScratch(p.Name, p.Task)
	if err != nil {
		return wire.ScratchResult{}, err
	}
	info, err := s.Get(id)
	return wire.ScratchResult{Project: info, Path: path}, err
}

// CreateScratch makes a scratch project on this Mac (agents.spawn with
// scratch: true too): <scratch root>/<yyyy-mm-dd>-<slug> (-2, -3, … when
// taken), git init with an empty first commit, the catalog entry.
func (s *Store) CreateScratch(name, task string) (id, path string, err error) {
	root := s.opt.ScratchRoot
	if root == "" {
		return "", "", wire.Errorf(wire.CodeUnavailable, "scratch projects are off on this Mac")
	}
	display, slug, err := scratchName(name, task)
	if err != nil {
		return "", "", err
	}
	s.scratchMu.Lock()
	defer s.scratchMu.Unlock()
	now := s.opt.Now()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", fmt.Errorf("scratch root: %w", err)
	}
	prefix := now.Local().Format("2006-01-02") + "-" + slug
	dir := ""
	for i := 1; i < 1000 && dir == ""; i++ {
		base := prefix
		if i > 1 {
			base = fmt.Sprintf("%s-%d", prefix, i)
		}
		if s.scratchTaken(root, base) {
			continue
		}
		candidate := filepath.Join(root, base)
		if err := os.Mkdir(candidate, 0o755); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", "", err
		}
		dir = candidate
	}
	if dir == "" {
		return "", "", wire.Errorf(wire.CodeExists, "no free folder for %s in %s", prefix, root)
	}
	if err := s.gitInit(dir, display); err != nil {
		os.RemoveAll(dir) // made just now, empty but for .git
		return "", "", err
	}
	s.det.forget(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.projects) >= maxRecords {
		os.RemoveAll(dir)
		return "", "", wire.Errorf(wire.CodeInvalid, "too many projects")
	}
	ident := wire.ProjectIdentity{Local: newLocalIdentity()}
	r := &Record{ID: ProjectID(ident), Identity: ident, Name: Field[string]{V: display, S: s.clock.tick()},
		Kind: Field[string]{V: wire.ProjectScratch, S: s.clock.tick()}, Paths: map[string]Field[string]{},
		Created: now.UTC(), LastUsed: now.UTC(),
		Scratch: &ScratchRecord{Home: Field[string]{V: s.node, S: s.clock.tick()}, Git: Field[bool]{V: true, S: s.clock.tick()}}}
	s.projects[r.ID] = r
	s.setPathLocked(r, dir, true)
	s.commitLocked(true, true)
	return r.ID, dir, nil
}

// scratchTaken: a folder of that name is in the scratch root or its
// archive, or the catalog has it.
func (s *Store) scratchTaken(root, base string) bool {
	for _, p := range []string{filepath.Join(root, base), filepath.Join(root, archiveDir, base)} {
		if _, err := os.Lstat(p); err == nil {
			return true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	want := filepath.Join(root, base)
	for _, r := range s.projects {
		if p := r.Paths[s.node].V; p == want || originOf(p) == want {
			return !r.Deleted.V
		}
	}
	return false
}

// gitInit makes dir a repository with an empty first commit (not signed:
// hesperd's own commit, made without a person to unlock a key).
func (s *Store) gitInit(dir, name string) error {
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = s.det.env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return strings.TrimSpace(out.String()), err
	}
	if out, err := run("init", "-q", "-b", "main"); err != nil {
		return fmt.Errorf("git init: %v: %s", err, out)
	}
	args := []string{}
	if email, _ := run("config", "user.email"); email == "" {
		args = append(args, "-c", "user.name=Hesper", "-c", "user.email=hesper@localhost")
	}
	args = append(args, "commit", "-q", "--allow-empty", "--no-gpg-sign", "--no-verify", "-m", "Start scratch: "+name)
	if out, err := run(args...); err != nil {
		return fmt.Errorf("git commit: %v: %s", err, out)
	}
	return nil
}

// KeepScratch is projects.scratchKeep: keep pins a scratch project (it
// is never archived or deleted). Any Mac may set it.
func (s *Store) KeepScratch(id string, keep bool) (wire.ProjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.scratchLocked(id)
	if err != nil {
		return wire.ProjectInfo{}, err
	}
	if r.Scratch.Keep.set(keep, s.clock.tick()) {
		s.commitLocked(true, false)
	}
	return s.viewLocked(r), nil
}

// ArchiveScratch is projects.scratchArchive: its folder goes to the
// archive now (on its home). Refused ("busy") while agents run in it.
func (s *Store) ArchiveScratch(id string) (wire.ProjectInfo, error) {
	err := s.atHome(id, "projects.scratchArchive", wire.IDParams{ID: id}, func() error { return s.archiveLocal(id, s.opt.Now(), true) })
	if err != nil {
		return wire.ProjectInfo{}, err
	}
	return s.Get(id)
}

// RestoreScratch is projects.scratchRestore: an archived scratch's folder
// comes back (resting, its lastUsed now).
func (s *Store) RestoreScratch(id string) (wire.ProjectInfo, error) {
	err := s.atHome(id, "projects.scratchRestore", wire.IDParams{ID: id}, func() error { return s.restoreLocal(id) })
	if err != nil {
		return wire.ProjectInfo{}, err
	}
	return s.Get(id)
}

// DeleteScratch is projects.scratchDelete: its folder and catalog entry
// go (a tombstone; history keeps its sessions, "folder removed").
// Refused ("busy") while agents run in it.
func (s *Store) DeleteScratch(id string) error {
	return s.atHome(id, "projects.scratchDelete", wire.IDParams{ID: id}, func() error { return s.deleteLocal(id, true) })
}

// homeFolderLocked is a scratch project's folder for an operation here:
// on its home, within the scratch root (in the archive when archived);
// busy when agents run in it (unless force).
func (s *Store) homeFolderLocked(r *Record, agents []wire.Agent, checkLive bool) (string, error) {
	if h := r.Scratch.Home.V; h != "" && h != s.node {
		return "", wire.Errorf(wire.CodeInvalid, "%s's home is %s, not this Mac", r.ID, s.nameOf(h))
	}
	if checkLive && s.liveInLocked(r, agents) {
		return "", wire.Errorf(wire.CodeBusy, "agents run in %s: close them first", r.Name.V)
	}
	p := r.Paths[s.node].V
	root := s.opt.ScratchRoot
	if p == "" || root == "" || filepath.Dir(p) != root && filepath.Dir(p) != filepath.Join(root, archiveDir) {
		return "", wire.Errorf(wire.CodeInvalid, "%s's folder %s is not in the scratch folder %s", r.Name.V, p, root)
	}
	return p, nil
}

// freePath is dir/base, else dir/base-2, -3, … whichever does not exist.
func freePath(dir, base string) string {
	p := filepath.Join(dir, base)
	for i := 2; i < 1000; i++ {
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s-%d", base, i))
	}
	return p
}

// archiveLocal moves a scratch's folder to the archive (this Mac is its
// home).
func (s *Store) archiveLocal(id string, now time.Time, manual bool) error {
	agents := s.agentsNow()
	s.scratchMu.Lock()
	defer s.scratchMu.Unlock()
	s.mu.Lock()
	r, err := s.scratchLocked(id)
	if err == nil && s.archivedLocked(r) {
		s.mu.Unlock()
		return nil
	}
	var src string
	if err == nil {
		src, err = s.homeFolderLocked(r, agents, true)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	archive := filepath.Join(s.opt.ScratchRoot, archiveDir)
	if err := os.MkdirAll(archive, 0o755); err != nil {
		return err
	}
	dst := freePath(archive, filepath.Base(src))
	if err := os.Rename(src, dst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return wire.Errorf(wire.CodeNotFound, "%s's folder %s is gone", r.Name.V, src)
		}
		return fmt.Errorf("archiving %s: %w", src, err)
	}
	s.det.forget(src)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setPathLocked(r, dst, true)
	r.Scratch.ArchivedAt.set(now.UnixMilli(), s.clock.tick())
	s.commitLocked(true, true)
	if !manual {
		s.opt.Logf("hesperd: archived the scratch project %s (%s)", r.Name.V, dst)
	}
	return nil
}

// restoreLocal moves an archived scratch's folder back.
func (s *Store) restoreLocal(id string) error {
	s.scratchMu.Lock()
	defer s.scratchMu.Unlock()
	s.mu.Lock()
	r, err := s.scratchLocked(id)
	var src string
	if err == nil {
		if !s.archivedLocked(r) {
			s.mu.Unlock()
			return nil
		}
		src, err = s.homeFolderLocked(r, nil, false)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	dst := freePath(s.opt.ScratchRoot, filepath.Base(src))
	if err := os.Rename(src, dst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return wire.Errorf(wire.CodeNotFound, "%s's folder %s is gone", r.Name.V, src)
		}
		return fmt.Errorf("restoring %s: %w", src, err)
	}
	s.det.forget(dst)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setPathLocked(r, dst, true)
	r.Scratch.ArchivedAt.set(0, s.clock.tick())
	if now := s.opt.Now().UTC(); now.After(r.LastUsed) {
		r.LastUsed = now
	}
	s.commitLocked(true, true)
	return nil
}

// deleteLocal removes a scratch's folder and its catalog entry.
func (s *Store) deleteLocal(id string, manual bool) error {
	agents := s.agentsNow()
	s.scratchMu.Lock()
	defer s.scratchMu.Unlock()
	s.mu.Lock()
	r, err := s.scratchLocked(id)
	var dir string
	if err == nil {
		dir, err = s.homeFolderLocked(r, agents, true)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("deleting %s: %w", dir, err)
	}
	s.det.forget(dir)
	s.mu.Lock()
	defer s.mu.Unlock()
	r.Deleted = Field[bool]{V: true, S: s.clock.tick()}
	delete(s.live, id)
	s.commitLocked(true, true)
	if !manual {
		s.opt.Logf("hesperd: deleted the scratch project %s (%s)", r.Name.V, dir)
	}
	return nil
}

// promoteScratch is projects.promote {id}: the scratch's folder moves to
// <projects root>/<name>, the project (same id) becomes a repository
// (a folder project when it is no Git repository), with createRepo
// "github" also gh repo create --private --source . --push. Refused
// ("busy") while agents run in it.
func (s *Store) promoteScratch(p wire.ProjectPromoteParams) (string, error) {
	if p.CreateRepo != "" && p.CreateRepo != wire.CreateRepoGitHub {
		return "", wire.Errorf(wire.CodeInvalid, "createRepo must be %q", wire.CreateRepoGitHub)
	}
	name := ""
	if p.Name != "" {
		var err error
		if name, err = cleanName(p.Name); err != nil {
			return "", err
		}
	}
	if s.opt.ProjectsRoot == "" {
		return "", wire.Errorf(wire.CodeUnavailable, "no projects folder")
	}
	gh := ""
	if p.CreateRepo != "" {
		gh = s.opt.GH
		if gh == "" {
			gh, _ = exec.LookPath("gh")
		}
		if st, err := os.Stat(gh); gh == "" || err != nil || st.IsDir() {
			return "", wire.Errorf(wire.CodeUnavailable, "gh (GitHub CLI) is not installed: promote without createRepo")
		}
	}
	agents := s.agentsNow()
	s.scratchMu.Lock()
	defer s.scratchMu.Unlock()
	s.mu.Lock()
	r, err := s.scratchLocked(p.ID)
	var src string
	if err == nil {
		src, err = s.homeFolderLocked(r, agents, true)
	}
	if err == nil && name == "" {
		name = r.Name.V
	}
	git := err == nil && r.Scratch.Git.V
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if gh != "" && !git {
		return "", wire.Errorf(wire.CodeInvalid, "%s is not a Git repository: promote without createRepo", name)
	}
	slug := scratchSlug(name, 60)
	if slug == "" {
		slug = "project"
	}
	dst := filepath.Join(s.opt.ProjectsRoot, slug)
	if _, err := os.Lstat(dst); err == nil {
		return "", wire.Errorf(wire.CodeExists, "%s exists: choose another name", dst)
	}
	if err := os.MkdirAll(s.opt.ProjectsRoot, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("moving %s to %s: %w", src, dst, err)
	}
	dst = canonical(dst)
	s.det.forget(src)
	s.det.forget(dst)
	s.mu.Lock()
	kind := wire.ProjectFolder
	if git {
		kind = wire.ProjectRepo
	}
	r.Kind.set(kind, s.clock.tick())
	r.Name.set(name, s.clock.tick())
	r.Scratch.ArchivedAt.set(0, s.clock.tick())
	s.setPathLocked(r, dst, true)
	if now := s.opt.Now().UTC(); now.After(r.LastUsed) {
		r.LastUsed = now
	}
	delete(s.live, r.ID)
	s.commitLocked(true, true)
	s.mu.Unlock()
	if gh != "" {
		ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, gh, "repo", "create", slug, "--private", "--source", ".", "--push")
		cmd.Dir, cmd.Env = dst, s.det.env
		if out, err := cmd.CombinedOutput(); err != nil {
			msg := strings.TrimSpace(string(out))
			if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
				msg = msg[i+1:]
			}
			return "", wire.Errorf(wire.CodeRemote, "promoted to %s, but gh repo create failed: %s", dst, orDefault(msg, err.Error()))
		}
		s.det.forget(dst)
	}
	return r.ID, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// --- adoption and lifecycle ---

// StartScratch runs the scratch lifecycle (no-op without a scratch
// root): adoption and the lifecycle now and daily, the states following
// the agents every 30 s. Call it once the agents are set (SetAgents).
func (s *Store) StartScratch() {
	if s.opt.ScratchRoot == "" {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		s.Sweep(s.opt.Now())
		last := time.Now()
		tick := time.NewTicker(liveEvery)
		defer tick.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-tick.C:
				if time.Since(last) >= scratchSweepEvery {
					s.Sweep(s.opt.Now())
					last = time.Now()
				} else {
					s.refreshLive()
				}
			}
		}
	}()
}

// Sweep adopts the scratch root's date-prefixed folders, then runs the
// lifecycle of this Mac's scratch projects at now: resting ≥
// archiveAfterDays → archived; archived ≥ deleteAfterDays → deleted;
// keep: neither; while agents run in one its lastUsed follows.
func (s *Store) Sweep(now time.Time) {
	if s.opt.ScratchRoot == "" {
		return
	}
	s.adoptScratch(now)
	agents := s.agentsNow()
	s.mu.Lock()
	s.setLiveLocked(agents)
	cfg := s.scratchCfg
	archiveAfter := time.Duration(cfg.ArchiveAfterDays) * 24 * time.Hour
	deleteAfter := time.Duration(cfg.DeleteAfterDays) * 24 * time.Hour
	var archive, remove []string
	bumped := false
	for _, r := range s.projects {
		if r.Deleted.V || r.Kind.V != wire.ProjectScratch || r.Scratch == nil || r.Scratch.Home.V != s.node || r.Scratch.Keep.V {
			continue
		}
		if s.live[r.ID] {
			if now.Sub(r.LastUsed) > time.Hour {
				r.LastUsed, bumped = now.UTC(), true
			}
			continue
		}
		if at := r.Scratch.ArchivedAt.V; at > 0 {
			if now.Sub(time.UnixMilli(at)) >= deleteAfter {
				remove = append(remove, r.ID)
			}
			continue
		}
		base := r.LastUsed
		if r.Created.After(base) {
			base = r.Created
		}
		if at := time.UnixMilli(r.Scratch.AdoptedAt.V); r.Scratch.AdoptedAt.V > 0 && at.After(base) {
			base = at
		}
		if base.IsZero() {
			r.LastUsed, bumped = now.UTC(), true // counted from now
			continue
		}
		if now.Sub(base) >= archiveAfter {
			archive = append(archive, r.ID)
		}
	}
	if bumped {
		s.commitLocked(true, false)
	} else {
		s.emitLocked()
	}
	s.mu.Unlock()
	for _, id := range archive {
		if err := s.archiveLocal(id, now, false); err != nil {
			s.opt.Logf("hesperd: archiving scratch project %s: %v", id, err)
		}
	}
	for _, id := range remove {
		if err := s.deleteLocal(id, false); err != nil {
			s.opt.Logf("hesperd: deleting scratch project %s: %v", id, err)
		}
	}
}

// adoptScratch makes the scratch root's date-prefixed folders scratch
// projects of this Mac (their date the created one, the rest the name):
// a new entry, or the project the folder already is (kind repo or folder
// without a remote) turned scratch. A folder with content and no .git
// stays as it is (git: false); an empty one gets its repository. Removed
// projects stay removed. Projects of kind scratch without a lifecycle
// get one.
func (s *Store) adoptScratch(now time.Time) {
	root := s.opt.ScratchRoot
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.opt.Logf("hesperd: scratch folder %s: %v", root, err)
	}
	type found struct {
		path, name string
		created    time.Time
		mtime      time.Time
		git        bool
	}
	var list []found
	for _, e := range entries {
		m := scratchFolder.FindStringSubmatch(e.Name())
		if !e.IsDir() || m == nil {
			continue
		}
		created, err := time.ParseInLocation("2006-01-02", m[1], time.Local)
		if err != nil {
			continue
		}
		f := found{path: filepath.Join(root, e.Name()), name: strings.TrimSpace(strings.ReplaceAll(m[2], "-", " ")), created: created}
		if f.name == "" {
			f.name = m[1]
		}
		if info, err := e.Info(); err == nil {
			f.mtime = info.ModTime()
		}
		if _, err := os.Lstat(filepath.Join(f.path, ".git")); err == nil {
			if gi := s.det.gitOf(canonical(f.path)); gi.OK && gi.Remote != "" {
				continue // a clone: a repository project, not a scratch
			}
			f.git = true
		} else if inside, err := os.ReadDir(f.path); err == nil && len(inside) == 0 && !s.knownPath(f.path) {
			// An empty folder: a scratch is a repository.
			f.git = s.gitInit(f.path, f.name) == nil
		}
		list = append(list, f)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, f := range list {
		r := s.byLocalPathLocked(f.path, func(r *Record) bool { return r.Identity.Package == "" })
		switch {
		case r != nil && (r.Deleted.V || r.Identity.Remote != ""):
			continue
		case r != nil:
			if r.Kind.V != wire.ProjectScratch && r.Kind.V != wire.ProjectReference {
				r.Kind.set(wire.ProjectScratch, s.clock.tick())
				if r.Name.V == filepath.Base(f.path) {
					r.Name.set(f.name, s.clock.tick())
				}
				changed = true
			}
		default:
			if len(s.projects) >= maxRecords {
				continue
			}
			ident := wire.ProjectIdentity{Local: newLocalIdentity()}
			r = &Record{ID: ProjectID(ident), Identity: ident, Name: Field[string]{V: f.name, S: s.clock.tick()},
				Kind: Field[string]{V: wire.ProjectScratch, S: s.clock.tick()}, Paths: map[string]Field[string]{}, Created: f.created.UTC()}
			if f.mtime.After(f.created) {
				r.LastUsed = f.mtime.UTC()
			} else {
				r.LastUsed = f.created.UTC()
			}
			s.projects[r.ID] = r
			s.setPathLocked(r, f.path, true)
			changed = true
		}
		if r.Kind.V == wire.ProjectScratch && r.Created.IsZero() {
			r.Created, changed = f.created.UTC(), true
		}
		if r.Kind.V == wire.ProjectScratch && r.Scratch == nil {
			r.Scratch = &ScratchRecord{Home: Field[string]{V: s.node, S: s.clock.tick()}, Git: Field[bool]{V: f.git, S: s.clock.tick()},
				AdoptedAt: Field[int64]{V: now.UnixMilli(), S: s.clock.tick()}}
			changed = true
		} else if r.Kind.V == wire.ProjectScratch && r.Scratch.Git.V != f.git && r.Scratch.Home.V == s.node {
			r.Scratch.Git.set(f.git, s.clock.tick())
			changed = true
		}
	}
	// Scratch projects (kind set elsewhere) without their lifecycle.
	for _, r := range s.projects {
		if r.Deleted.V || r.Kind.V != wire.ProjectScratch || r.Scratch != nil {
			continue
		}
		p := r.Paths[s.node].V
		if p == "" {
			continue
		}
		_, err := os.Lstat(filepath.Join(p, ".git"))
		r.Scratch = &ScratchRecord{Home: Field[string]{V: s.node, S: s.clock.tick()}, Git: Field[bool]{V: err == nil, S: s.clock.tick()},
			AdoptedAt: Field[int64]{V: now.UnixMilli(), S: s.clock.tick()}}
		if r.Created.IsZero() {
			r.Created = now.UTC()
		}
		changed = true
	}
	if changed {
		s.commitLocked(true, true)
	}
}

// knownPath: a project (removed ones too) has this folder here.
func (s *Store) knownPath(p string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byLocalPathLocked(p, func(*Record) bool { return true }) != nil
}

// --- moves and history ---

// ScratchRoot is where this Mac's scratch projects are ("": off).
func (s *Store) ScratchRoot() string { return s.opt.ScratchRoot }

// ScratchOf (agents.move) is a scratch project's identity, name and
// creation; ok false for anything else.
func (s *Store) ScratchOf(id string) (local, name string, created time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.projects[id]
	if r == nil || r.Deleted.V || r.Kind.V != wire.ProjectScratch || r.Identity.Local == "" {
		return "", "", time.Time{}, false
	}
	return r.Identity.Local, r.Name.V, r.Created, true
}

// ScratchArrived (agents.move) records a moved scratch project's folder
// on this Mac (the project made from the move's manifest when its state
// did not arrive yet); home: it is this Mac's now (a move, not a fork).
// It returns the project's id.
func (s *Store) ScratchArrived(local, name string, created time.Time, path string, home bool) string {
	if !localIdentity.MatchString(local) {
		return ""
	}
	if _, err := cleanName(name); err != nil {
		name = ""
	}
	ident := wire.ProjectIdentity{Local: local}
	id := ProjectID(ident)
	path = canonical(path)
	s.det.forget(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.projects[id]
	if r == nil {
		if len(s.projects) >= maxRecords {
			return ""
		}
		if name == "" {
			name = filepath.Base(path)
		}
		r = &Record{ID: id, Identity: ident, Name: Field[string]{V: name}, Paths: map[string]Field[string]{}, Created: created.UTC()}
		s.projects[id] = r
	}
	if r.Deleted.V {
		r.Deleted = Field[bool]{V: false, S: s.clock.tick()}
	}
	r.Kind.set(wire.ProjectScratch, s.clock.tick())
	if r.Scratch == nil {
		r.Scratch = &ScratchRecord{}
	}
	if home {
		r.Scratch.Home.set(s.node, s.clock.tick())
		r.Scratch.ArchivedAt.set(0, s.clock.tick())
	}
	r.Scratch.Git.set(true, s.clock.tick())
	s.setPathLocked(r, path, true)
	if now := s.opt.Now().UTC(); now.After(r.LastUsed) {
		r.LastUsed = now
	}
	s.live[id] = true
	s.commitLocked(true, true)
	return id
}

// FolderRemoved (shared history) reports whether a session in cwd with
// project id ran in a scratch project whose folder was deleted.
func (s *Store) FolderRemoved(id, cwd string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.projects[id]; r != nil {
		return r.Deleted.V && r.Kind.V == wire.ProjectScratch
	}
	if cwd == "" {
		return false
	}
	for _, r := range s.projects {
		if !r.Deleted.V || r.Kind.V != wire.ProjectScratch {
			continue
		}
		for _, f := range r.Paths {
			if f.V != "" && (within(cwd, f.V) || within(cwd, orDefault(originOf(f.V), f.V))) {
				return true
			}
		}
	}
	return false
}
