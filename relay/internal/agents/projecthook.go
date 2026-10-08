package agents

import (
	"encoding/json"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Projects (workspace model step 1) is the project registry
// (internal/projects.Store), hooked in here: it names each agent's
// project (Agent.ProjectID), keeps projects.recent, and serves the
// projects.* / groups.* methods and notifications on the local socket.
// Nil (tests): agents get no projectId and projects.json is the old list
// of recent folders.
type Projects interface {
	// Resolve is the project of an agent in dir ("scratch:<dir>" outside
	// every project); it may register the folder's repository. Cached:
	// cheap after the first call for a folder.
	Resolve(dir string) string
	// Touch records that an agent started in path; Recent is
	// projects.recent.
	Touch(path string, at time.Time)
	Recent() []wire.Project
	// Call runs a projects/groups method (handled false: not one).
	Call(method string, params json.RawMessage) (result any, err error, handled bool)
	// Watch sends the projects/groups notifications of an agents.subscribe
	// connection until done closes or send fails.
	Watch(done <-chan struct{}, send func(method string, params any) error)
}

// projectOf is the project of an agent in dir ("" without Projects).
// Called without the registry's lock held where the folder may be new
// (Resolve may run git once).
func (r *Registry) projectOf(dir string) string {
	if r.opt.Projects == nil || dir == "" {
		return ""
	}
	return r.opt.Projects.Resolve(dir)
}

// dirOf is where an agent runs: its worktree, else its project.
func dirOf(project, worktree string) string {
	if worktree != "" {
		return worktree
	}
	return project
}

// Reproject resolves every local agent's project again (the projects
// changed: promoted, removed, merged from another Mac) and tells
// subscribers about the agents whose project changed.
func (r *Registry) Reproject() {
	if r.opt.Projects == nil {
		return
	}
	r.mu.Lock()
	dirs := make(map[string]string, len(r.agents))
	for local, a := range r.agents {
		dirs[local] = a.Dir()
	}
	r.mu.Unlock()
	ids := make(map[string]string, len(dirs))
	for local, dir := range dirs {
		ids[local] = r.projectOf(dir)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for local, id := range ids {
		a := r.agents[local]
		if a != nil && a.Dir() == dirs[local] && id != "" && a.ProjectID != id {
			a.ProjectID = id
			r.changed(a)
		}
	}
}

// watchProjects sends the project notifications on a subscribed control
// connection (server.go's subscribe) until it closes.
func (c *ctrl) watchProjects() {
	p := c.s.reg.opt.Projects
	if p == nil {
		return
	}
	p.Watch(c.done, func(method string, params any) error {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		return c.write(wire.Notification{JSONRPC: "2.0", Method: method, Params: data})
	})
}
