package main

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// Projects and groups (TEST-ONLY imitation of the data agent's wire, see
// docs/rebuild-contract.md "As built — projects (views)"): projects.list,
// groups.list, projects.update, projects.promote, projects.remove,
// groups.save, groups.remove, and projects.changed/removed,
// groups.changed/removed on agents.subscribe; shaped like relay/pkg/wire/
// projects.go. Agents carry projectId: the project whose folder (on their
// machine) holds their project path, longest match; a folder of no
// project is the virtual "scratch:<folder>" (listed by projects.list,
// never notified, as in hesperd).

type Project struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Color            string            `json:"color"`
	ColorSet         bool              `json:"colorSet,omitempty"`
	Kind             string            `json:"kind"`
	Identity         map[string]string `json:"identity"`
	ParentID         string            `json:"parentId,omitempty"`
	Paths            map[string]string `json:"paths"`
	Groups           []string          `json:"groups"`
	Defaults         map[string]string `json:"defaults,omitempty"`
	DetectedPackages []string          `json:"detectedPackages,omitempty"`
	LastUsed         string            `json:"lastUsed,omitempty"`
}

type Group struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	ProjectIDs []string `json:"projectIds"`
	Order      int      `json:"order"`
	Color      string   `json:"color,omitempty"`
}

const fakeRoot = "/tmp/fake/projects"

// seedDemoProjects: two groups, a repository with a package, a scratch
// folder (--projects demo).
func (d *daemon) seedDemoProjects() {
	add := func(p *Project) { d.projects[p.ID] = p }
	add(&Project{ID: "p-acme-apps", Name: "acme-apps", Color: "#7aa2f7", Kind: "repo", Identity: map[string]string{"remote": "github.com/acme/acme-apps"},
		Paths: map[string]string{"L": fakeRoot + "/acme-apps", "M": "/Users/mini/projects/acme-apps"}, DetectedPackages: []string{"apps/ios-app", "apps/android"}})
	add(&Project{ID: "p-ios-app", Name: "ios-app", Color: "#2ac3de", Kind: "package", ParentID: "p-acme-apps",
		Paths: map[string]string{"L": fakeRoot + "/acme-apps/apps/ios-app", "M": "/Users/mini/projects/acme-apps/apps/ios-app"}})
	add(&Project{ID: "p-edition", Name: "design-system", Color: "#ff9e64", Kind: "repo", Identity: map[string]string{"remote": "github.com/acme/design-system"},
		Paths: map[string]string{"L": fakeRoot + "/design-system"}})
	add(&Project{ID: "p-hesper", Name: "hesper", Color: "#bb9af7", Kind: "repo", Identity: map[string]string{"remote": "github.com/example/hesper"},
		Paths: map[string]string{"L": fakeRoot + "/hesper", "M": "/Users/mini/projects/hesper"}})
	add(&Project{ID: "p-trial", Name: "hesper-trial", Color: "#73daca", Kind: "folder", Identity: map[string]string{"local": "l-0123456789abcdef"},
		Paths: map[string]string{"L": "/tmp/fake/scratch/hesper-trial"}})
	d.groups["g-acme"] = &Group{ID: "g-acme", Name: "acme apps", ProjectIDs: []string{"p-acme-apps", "p-edition"}, Order: 0}
	d.groups["g-tools"] = &Group{ID: "g-tools", Name: "tools", ProjectIDs: []string{"p-hesper"}, Order: 1}
	d.syncProjectGroups()
}

// demoSeat is where the n-th seeded agent of --projects demo works.
func demoSeat(i int) (machine, path, branch string) {
	seats := []struct{ m, p, b string }{
		{"L", fakeRoot + "/acme-apps", "feature/push-provider-fcm"},
		{"L", fakeRoot + "/hesper", ""},
		{"L", fakeRoot + "/design-system", ""},
		{"M", "/Users/mini/projects/acme-apps", ""},
		{"L", fakeRoot + "/hesper", "rebuild"},
		{"L", fakeRoot + "/acme-apps/apps/ios-app", ""},
		{"L", "/tmp/fake/scratch/spike-vt", ""},
		{"L", fakeRoot + "/design-system", ""},
		{"L", fakeRoot + "/acme-apps", ""},
		{"L", fakeRoot + "/hesper", ""},
		{"L", fakeRoot + "/acme-apps", "feature/push-provider-fcm"},
		{"L", fakeRoot + "/design-system", ""},
	}
	s := seats[i%len(seats)]
	return s.m, s.p, s.b
}

// syncProjectGroups sets every project's groups from the groups (the lock
// is held or not needed yet).
func (d *daemon) syncProjectGroups() {
	for _, p := range d.projects {
		p.Groups = []string{}
	}
	ids := make([]string, 0, len(d.groups))
	for id := range d.groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, gid := range ids {
		for _, pid := range d.groups[gid].ProjectIDs {
			if p := d.projects[pid]; p != nil {
				p.Groups = append(p.Groups, gid)
			}
		}
	}
}

// projectFor finds the project of a folder on a machine, else its
// scratch id. Lock held.
func (d *daemon) projectFor(machine, path string) string {
	if path == "" {
		return ""
	}
	best, bestLen := "", -1
	for _, p := range d.projects {
		root, ok := p.Paths[machine]
		if !ok {
			continue
		}
		if path == root || strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/") {
			if len(root) > bestLen {
				best, bestLen = p.ID, len(root)
			}
		}
	}
	if best != "" {
		return best
	}
	return "scratch:" + path
}

// projectList: every project, then the scratch projects of the agents
// there are (lock held).
func (d *daemon) projectList() []*Project {
	list := make([]*Project, 0, len(d.projects))
	for _, p := range d.projects {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	seen := map[string]bool{}
	for _, id := range d.order {
		a := d.agents[id].a
		if strings.HasPrefix(a.ProjectID, "scratch:") && !seen[a.ProjectID] {
			seen[a.ProjectID] = true
			dir := strings.TrimPrefix(a.ProjectID, "scratch:")
			list = append(list, &Project{ID: a.ProjectID, Name: filepath.Base(dir), Color: "#565f89", Kind: "scratch",
				Identity: map[string]string{}, Paths: map[string]string{a.Machine: dir}, Groups: []string{}})
		}
	}
	return list
}

func (d *daemon) groupList() []*Group {
	list := make([]*Group, 0, len(d.groups))
	for _, g := range d.groups {
		list = append(list, g)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Order != list[j].Order {
			return list[i].Order < list[j].Order
		}
		return list[i].ID < list[j].ID
	})
	return list
}

func (d *daemon) notify(method string, params map[string]any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	for s := range d.subs {
		select {
		case s <- b:
		default:
		}
	}
}

func clone[T any](v *T) *T { c := *v; return &c }

func (d *daemon) projectCall(method string, params json.RawMessage) (any, *rpcError) {
	var p map[string]json.RawMessage
	_ = json.Unmarshal(params, &p)
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(p[k], &s)
		return s
	}
	switch method {
	case "projects.list":
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.projectList(), nil
	case "groups.list":
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.groupList(), nil
	case "projects.update":
		d.mu.Lock()
		pr := d.projects[str("id")]
		if pr == nil {
			d.mu.Unlock()
			return nil, errCode("not_found", "no project "+str("id"))
		}
		if v := str("name"); v != "" {
			pr.Name = v
		}
		if _, ok := p["color"]; ok {
			pr.Color, pr.ColorSet = str("color"), str("color") != ""
			if pr.Color == "" {
				pr.Color = "#7aa2f7"
			}
		}
		if raw, ok := p["defaults"]; ok {
			var def map[string]string
			_ = json.Unmarshal(raw, &def)
			pr.Defaults = def
		}
		out := clone(pr)
		d.mu.Unlock()
		d.notify("projects.changed", map[string]any{"project": out})
		return out, nil
	case "projects.promote":
		machine := str("machine")
		if machine == "" {
			machine = d.self
		}
		path := str("path")
		if path == "" {
			return nil, errCode("invalid", "path is required")
		}
		d.mu.Lock()
		pr := d.projects[d.projectFor(machine, path)]
		if pr == nil {
			pr = &Project{ID: "p-" + newID() + newID(), Name: filepath.Base(path), Color: "#9ece6a", Kind: "folder",
				Identity: map[string]string{"local": "l-" + newID()}, Paths: map[string]string{machine: path}, Groups: []string{}}
			d.projects[pr.ID] = pr
			for _, ap := range d.agents {
				if ap.a.ProjectID == "scratch:"+path {
					ap.a.ProjectID = pr.ID
					defer d.changed(ap) // after the lock: agents.changed with the new projectId
				}
			}
		}
		pr.Kind = "folder"
		if k := str("kind"); k != "" {
			pr.Kind = k
		}
		if n := str("name"); n != "" {
			pr.Name = n
		}
		out := clone(pr)
		d.mu.Unlock()
		d.notify("projects.changed", map[string]any{"project": out})
		return out, nil
	case "projects.remove":
		id := str("id")
		d.mu.Lock()
		if d.projects[id] == nil {
			d.mu.Unlock()
			return nil, errCode("not_found", "no project "+id)
		}
		delete(d.projects, id)
		var changed []*Group
		for _, g := range d.groups {
			for i, x := range g.ProjectIDs {
				if x == id {
					g.ProjectIDs = append(g.ProjectIDs[:i], g.ProjectIDs[i+1:]...)
					changed = append(changed, clone(g))
					break
				}
			}
		}
		d.mu.Unlock()
		d.notify("projects.removed", map[string]any{"id": id})
		for _, g := range changed {
			d.notify("groups.changed", map[string]any{"group": g})
		}
		return map[string]any{}, nil
	case "groups.save":
		var g Group
		if raw, ok := p["group"]; ok {
			_ = json.Unmarshal(raw, &g)
		} else {
			_ = json.Unmarshal(params, &g)
		}
		if strings.TrimSpace(g.Name) == "" {
			return nil, errCode("invalid", "a group needs a name")
		}
		d.mu.Lock()
		if g.ID == "" {
			g.ID = "g-" + newID()
		}
		if g.ProjectIDs == nil {
			g.ProjectIDs = []string{}
		}
		for _, pid := range g.ProjectIDs {
			if d.projects[pid] == nil {
				d.mu.Unlock()
				return nil, errCode("invalid", "not a project: "+pid)
			}
		}
		d.groups[g.ID] = &g
		d.syncProjectGroups()
		var touched []*Project
		for _, pr := range d.projects {
			touched = append(touched, clone(pr))
		}
		out := clone(&g)
		d.mu.Unlock()
		d.notify("groups.changed", map[string]any{"group": out})
		for _, pr := range touched {
			d.notify("projects.changed", map[string]any{"project": pr})
		}
		return out, nil
	case "groups.remove":
		id := str("id")
		d.mu.Lock()
		if d.groups[id] == nil {
			d.mu.Unlock()
			return nil, errCode("not_found", "no group "+id)
		}
		delete(d.groups, id)
		d.syncProjectGroups()
		d.mu.Unlock()
		d.notify("groups.removed", map[string]any{"id": id})
		return map[string]any{}, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + method, Data: map[string]any{"code": "not_found"}}
}

// projectSnapshot: projects.changed / groups.changed for a new subscriber
// (the lock is held).
func (d *daemon) projectSnapshot() [][]byte {
	var out [][]byte
	for _, p := range d.projectList() {
		if strings.HasPrefix(p.ID, "scratch:") {
			continue // no notifications for scratch projects
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "projects.changed", "params": map[string]any{"project": p}})
		out = append(out, b)
	}
	for _, g := range d.groupList() {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "groups.changed", "params": map[string]any{"group": g}})
		out = append(out, b)
	}
	return out
}
