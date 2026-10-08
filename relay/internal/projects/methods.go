package projects

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Call runs one of the projects/groups methods of the local socket;
// handled is false for any other method.
func (s *Store) Call(method string, params json.RawMessage) (result any, err error, handled bool) {
	decode := func(v any) error {
		if len(params) == 0 || string(params) == "null" {
			params = []byte("{}")
		}
		if err := json.Unmarshal(params, v); err != nil {
			return wire.Errorf(wire.CodeInvalid, "invalid params: %v", err)
		}
		return nil
	}
	switch method {
	case "projects.list":
		return s.List(), nil, true
	case "projects.update":
		var p wire.ProjectUpdateParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.Update(p)
		return res, err, true
	case "projects.promote":
		var p wire.ProjectPromoteParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.Promote(p)
		return res, err, true
	case "projects.remove":
		var p wire.IDParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		return struct{}{}, s.Remove(p.ID), true
	case "groups.list":
		return s.Groups(), nil, true
	case "groups.save":
		var p wire.GroupSaveParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		res, err := s.SaveGroup(p.Group)
		return res, err, true
	case "groups.remove":
		var p wire.IDParams
		if err := decode(&p); err != nil {
			return nil, err, true
		}
		return struct{}{}, s.RemoveGroup(p.ID), true
	}
	return nil, nil, false
}

// List is projects.list: every project (repositories with their detected
// packages, looked at again when their manifests changed), most recently
// used first, then the scratch projects of the agents there are.
func (s *Store) List() []wire.ProjectInfo {
	s.mu.Lock()
	agentsFn := s.agents
	var roots []string
	for _, r := range s.projects {
		if p := r.Paths[s.node].V; p != "" && !r.Deleted.V && r.Identity.Package == "" {
			roots = append(roots, p)
		}
	}
	s.mu.Unlock()
	for _, root := range roots {
		s.det.packagesOf(root)
	}
	var agents []wire.Agent
	if agentsFn != nil {
		agents = agentsFn()
	}
	s.mu.Lock()
	s.emitLocked() // detected packages may have changed
	out := []wire.ProjectInfo{}
	for _, r := range s.projects {
		if !r.Deleted.V {
			out = append(out, s.viewLocked(r))
		}
	}
	s.mu.Unlock()
	sortProjects(out)
	return append(out, scratchViews(agents)...)
}

func sortProjects(list []wire.ProjectInfo) {
	sort.Slice(list, func(i, j int) bool {
		if !list[i].LastUsed.Equal(list[j].LastUsed) {
			return list[i].LastUsed.After(list[j].LastUsed)
		}
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].ID < list[j].ID
	})
}

// Get is one project (not scratch).
func (s *Store) Get(id string) (wire.ProjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.liveLocked(id)
	if err != nil {
		return wire.ProjectInfo{}, err
	}
	return s.viewLocked(r), nil
}

func (s *Store) liveLocked(id string) (*Record, error) {
	if strings.HasPrefix(id, wire.ScratchPrefix) {
		return nil, wire.Errorf(wire.CodeInvalid, "%s is a scratch folder, not a project: promote it first (projects.promote)", id)
	}
	r := s.projects[id]
	if r == nil || r.Deleted.V {
		return nil, wire.Errorf(wire.CodeNotFound, "no project %s", id)
	}
	return r, nil
}

func validKind(k string) bool {
	switch k {
	case wire.ProjectRepo, wire.ProjectPackage, wire.ProjectFolder, wire.ProjectReference:
		return true
	}
	return false
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 || strings.ContainsAny(name, "\x00\n\r") {
		return "", wire.Errorf(wire.CodeInvalid, "a name is 1-200 characters on one line")
	}
	return name, nil
}

// Update is projects.update: the fields given change.
func (s *Store) Update(p wire.ProjectUpdateParams) (wire.ProjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.liveLocked(p.ID)
	if err != nil {
		return wire.ProjectInfo{}, err
	}
	if p.Name != nil {
		if _, err := cleanName(*p.Name); err != nil {
			return wire.ProjectInfo{}, err
		}
	}
	if p.Color != nil && *p.Color != "" && !validColor(*p.Color) {
		return wire.ProjectInfo{}, wire.Errorf(wire.CodeInvalid, "color must be #rrggbb or empty")
	}
	if p.Kind != nil && !validKind(*p.Kind) {
		return wire.ProjectInfo{}, wire.Errorf(wire.CodeInvalid, "kind must be repo, package, folder or reference")
	}
	changed := false
	if p.Name != nil {
		name, _ := cleanName(*p.Name)
		changed = r.Name.set(name, s.clock.tick()) || changed
	}
	if p.Color != nil {
		changed = r.Color.set(strings.ToLower(*p.Color), s.clock.tick()) || changed
	}
	if p.Kind != nil {
		changed = r.Kind.set(*p.Kind, s.clock.tick()) || changed
	}
	if p.Defaults != nil {
		changed = r.Defaults.set(*p.Defaults, s.clock.tick()) || changed
	}
	if changed {
		s.commitLocked(true, false)
	}
	return s.viewLocked(r), nil
}

// PromoteResult is what a host answers to a forwarded projects.promote:
// the project's id and the host's state (merged by the caller, so the
// project is there when it answers its app).
type PromoteResult struct {
	ID    string `json:"id"`
	State *State `json:"state"`
}

// Promote is projects.promote: the folder becomes a project (or the
// project it already is comes back): a repository's root its repository,
// a folder inside a repository a package-like project (repository
// identity + relative path, kind package when it is a detected package,
// else folder), any other folder a folder with a generated identity.
// Another machine's folder: done there (SetForward) and merged.
func (s *Store) Promote(p wire.ProjectPromoteParams) (wire.ProjectInfo, error) {
	s.mu.Lock()
	short, forward := s.short, s.forward
	s.mu.Unlock()
	if p.Machine != "" && p.Machine != short {
		if forward == nil {
			return wire.ProjectInfo{}, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", p.Machine)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		machine := p.Machine
		p.Machine = ""
		raw, err := forward(ctx, machine, "projects.promote", p)
		if err != nil {
			return wire.ProjectInfo{}, err
		}
		var res PromoteResult
		if err := json.Unmarshal(raw, &res); err != nil || res.State == nil {
			return wire.ProjectInfo{}, wire.Errorf(wire.CodeRemote, "%s answered projects.promote without a project", machine)
		}
		s.Merge(res.State, machine)
		return s.Get(res.ID)
	}
	id, err := s.promoteLocal(p)
	if err != nil {
		return wire.ProjectInfo{}, err
	}
	return s.Get(id)
}

// PromoteForPeer is projects.promote for another machine's daemon (host
// method): the id and this daemon's state.
func (s *Store) PromoteForPeer(p wire.ProjectPromoteParams) (PromoteResult, error) {
	id, err := s.promoteLocal(p)
	if err != nil {
		return PromoteResult{}, err
	}
	return PromoteResult{ID: id, State: s.Export()}, nil
}

func (s *Store) promoteLocal(p wire.ProjectPromoteParams) (string, error) {
	dir := filepath.Clean(p.Path)
	if p.Path == "" || !filepath.IsAbs(dir) {
		return "", wire.Errorf(wire.CodeInvalid, "path must be an absolute path")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", wire.Errorf(wire.CodeNotFound, "no directory %s", dir)
	}
	var name string
	if p.Name != "" {
		var err error
		if name, err = cleanName(p.Name); err != nil {
			return "", err
		}
	}
	if p.Kind != "" && !validKind(p.Kind) {
		return "", wire.Errorf(wire.CodeInvalid, "kind must be repo, package, folder or reference")
	}
	real := canonical(dir)
	s.det.forget(real)
	gi := s.det.gitOf(real)
	var pkgs []wire.DetectedPackage
	if gi.OK {
		pkgs = s.det.packagesOf(gi.Top)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var r *Record
	if gi.OK {
		repo, _ := s.ensureRepoLocked(gi, gi.Top == real)
		if repo == nil {
			// The repository was removed and a folder in it is promoted:
			// the repository stays removed, the folder hangs off it.
			repo = s.repoRecordLocked(gi)
		}
		relp := rel(gi.Top, real)
		if repo == nil {
			return "", wire.Errorf(wire.CodeInvalid, "too many projects")
		}
		if relp == "" {
			r = repo
		} else {
			kind, pkgName := wire.ProjectFolder, ""
			for _, pk := range pkgs {
				if pk.Path == relp {
					kind, pkgName = wire.ProjectPackage, pk.Name
				}
			}
			r, _ = s.ensurePackageLocked(repo, gi.Root, relp, pkgName, kind, true)
		}
	} else {
		r = s.byLocalPathLocked(real, func(r *Record) bool { return r.Identity.Remote == "" && r.Identity.Package == "" })
		if r == nil {
			ident := wire.ProjectIdentity{Local: newLocalIdentity()}
			r = &Record{ID: ProjectID(ident), Identity: ident, Name: Field[string]{V: filepath.Base(real)},
				Kind: Field[string]{V: wire.ProjectFolder}, Paths: map[string]Field[string]{}}
			s.projects[r.ID] = r
		}
	}
	if r == nil {
		return "", wire.Errorf(wire.CodeInvalid, "too many projects")
	}
	// Promoting always revives: a newer stamp than any tombstone seen.
	r.Deleted = Field[bool]{V: false, S: s.clock.tick()}
	if name != "" {
		r.Name.set(name, s.clock.tick())
	}
	if p.Kind != "" {
		r.Kind.set(p.Kind, s.clock.tick())
	}
	if gi.OK && real != gi.Top {
		s.setPathLocked(r, filepath.Join(gi.Root, rel(gi.Top, real)), true)
	} else if gi.OK {
		s.setPathLocked(r, gi.Root, true)
	} else {
		s.setPathLocked(r, real, true)
	}
	s.commitLocked(true, true)
	return r.ID, nil
}

// Remove is projects.remove: the project is forgotten (a tombstone that
// travels to the other Macs); agents in it fall back to the next project
// containing their folder, else scratch. A removed repository is not made
// a project again by agents starting in it; projects.promote brings it
// back.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.liveLocked(id)
	if err != nil {
		return err
	}
	r.Deleted = Field[bool]{V: true, S: s.clock.tick()}
	s.commitLocked(true, true)
	return nil
}

// Groups is groups.list: by order, then name.
func (s *Store) Groups() []wire.Group {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []wire.Group{}
	for _, g := range s.groups {
		if !g.Deleted.V {
			out = append(out, s.groupViewLocked(g))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// SaveGroup is groups.save: creates (empty id: one is made; "g-" and 1–40
// of [a-z0-9-]) or replaces a group. Its projects must exist (scratch
// folders cannot be grouped); duplicates are dropped.
func (s *Store) SaveGroup(g wire.Group) (wire.Group, error) {
	name, err := cleanName(g.Name)
	if err != nil {
		return wire.Group{}, err
	}
	if g.Color != "" && !validColor(g.Color) {
		return wire.Group{}, wire.Errorf(wire.CodeInvalid, "color must be #rrggbb or empty")
	}
	if g.ID == "" {
		g.ID = "g-" + randomHex(6)
	}
	if !groupID.MatchString(g.ID) {
		return wire.Group{}, wire.Errorf(wire.CodeInvalid, "group id %q: want g- and 1-40 of [a-z0-9-]", g.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []string{}
	seen := map[string]bool{}
	for _, id := range g.ProjectIDs {
		if seen[id] {
			continue
		}
		if _, err := s.liveLocked(id); err != nil {
			return wire.Group{}, err
		}
		seen[id] = true
		ids = append(ids, id)
	}
	rec := s.groups[g.ID]
	if rec == nil {
		if len(s.groups) >= maxGroups {
			return wire.Group{}, wire.Errorf(wire.CodeInvalid, "too many groups")
		}
		rec = &GroupRecord{ID: g.ID}
		s.groups[g.ID] = rec
	}
	changed := rec.Deleted.V || rec.Deleted.S == (Stamp{})
	if changed {
		rec.Deleted = Field[bool]{V: false, S: s.clock.tick()}
	}
	changed = rec.Name.set(name, s.clock.tick()) || changed
	changed = rec.ProjectIDs.set(ids, s.clock.tick()) || changed
	changed = rec.Order.set(g.Order, s.clock.tick()) || changed
	changed = rec.Color.set(strings.ToLower(g.Color), s.clock.tick()) || changed
	if changed {
		s.commitLocked(true, false)
	}
	return s.groupViewLocked(rec), nil
}

// RemoveGroup is groups.remove.
func (s *Store) RemoveGroup(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[id]
	if g == nil || g.Deleted.V {
		return wire.Errorf(wire.CodeNotFound, "no group %s", id)
	}
	g.Deleted = Field[bool]{V: true, S: s.clock.tick()}
	s.commitLocked(true, false)
	return nil
}
