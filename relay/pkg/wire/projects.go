package wire

import "time"

// Projects and groups (workspace model, step 1: data; internal/projects).
// A project is a folder with an identity: a repository (its normalized
// git remote), a monorepo package (repository identity + relative path),
// a plain folder or a reference folder (an explicit generated id). Agents
// join the deepest project containing their folder (Agent.ProjectID);
// agents outside every project belong to a virtual scratch project per
// folder ("scratch:<folder>"). Projects and groups are shared between the
// owner's Macs; their paths are per machine.

// Project kinds.
const (
	ProjectRepo      = "repo"
	ProjectPackage   = "package"
	ProjectFolder    = "folder"
	ProjectReference = "reference"
	ProjectScratch   = "scratch"
)

// ScratchPrefix starts a scratch project's id: "scratch:<folder>".
const ScratchPrefix = "scratch:"

// ProjectInfo is one project of projects.list (and of projects.changed).
type ProjectInfo struct {
	ID string `json:"id"`
	// Name defaults to the repository's name, the package's manifest name
	// or the folder's base name.
	Name string `json:"name"`
	// Color is "#rrggbb": set by projects.update, else picked stably from
	// the Tokyo Night palette by the id (ColorSet false).
	Color    string          `json:"color"`
	ColorSet bool            `json:"colorSet,omitempty"`
	Kind     string          `json:"kind"`
	Identity ProjectIdentity `json:"identity"`
	// ParentID is a package's repository.
	ParentID string `json:"parentId,omitempty"`
	// Paths: the project's folder on each machine (machine short name →
	// absolute path), as far as known.
	Paths map[string]string `json:"paths"`
	// Groups are the groups the project is in (ids).
	Groups   []string        `json:"groups"`
	Defaults ProjectDefaults `json:"defaults"`
	// DetectedPackages are a repository's monorepo packages (pnpm/npm/yarn
	// workspaces, Nx, Turborepo, Lerna, go.work, Cargo workspaces), found
	// in its folder on this machine; only repositories, only once looked
	// at (projects.list).
	DetectedPackages []DetectedPackage `json:"detectedPackages,omitempty"`
	LastUsed         time.Time         `json:"lastUsed"`
}

// ProjectIdentity is what makes two folders the same project: Remote (the
// normalized git remote, "github.com/owner/repo"), Package (a package's
// path relative to its repository) and Local (a generated id for folders
// without a remote).
type ProjectIdentity struct {
	Remote  string `json:"remote,omitempty"`
	Package string `json:"package,omitempty"`
	Local   string `json:"local,omitempty"`
}

// ProjectDefaults are what new agents in the project start with.
type ProjectDefaults struct {
	Profile string `json:"profile,omitempty"`
	Machine string `json:"machine,omitempty"`
}

// DetectedPackage is one monorepo package: its path relative to the
// repository, its name (manifest) and the tool that declares it (pnpm,
// npm, turbo, lerna, nx, go, cargo).
type DetectedPackage struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Tool string `json:"tool,omitempty"`
}

// Group is a named, ordered set of projects (one level; a project may be
// in several groups).
type Group struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	ProjectIDs []string `json:"projectIds"`
	Order      int      `json:"order"`
	Color      string   `json:"color,omitempty"`
}

// ProjectUpdateParams are projects.update's: only the fields given change
// (color "" goes back to the automatic color).
type ProjectUpdateParams struct {
	ID       string           `json:"id"`
	Name     *string          `json:"name,omitempty"`
	Color    *string          `json:"color,omitempty"`
	Kind     *string          `json:"kind,omitempty"`
	Defaults *ProjectDefaults `json:"defaults,omitempty"`
}

// ProjectPromoteParams are projects.promote's: make a folder on machine a
// project (identity detected).
type ProjectPromoteParams struct {
	Machine string `json:"machine,omitempty"`
	Path    string `json:"path"`
	Name    string `json:"name,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

// GroupSaveParams are groups.save's.
type GroupSaveParams struct {
	Group Group `json:"group"`
}

// Notifications about projects and groups on agents.subscribe
// connections (first a projects.changed for every project and a
// groups.changed for every group).
const (
	NoteProjectChanged = "projects.changed"
	NoteProjectRemoved = "projects.removed"
	NoteGroupChanged   = "groups.changed"
	NoteGroupRemoved   = "groups.removed"
)

// ProjectChanged is projects.changed's params; projects.removed's is
// Removed.
type ProjectChanged struct {
	Project ProjectInfo `json:"project"`
}

// GroupChanged is groups.changed's params; groups.removed's is Removed.
type GroupChanged struct {
	Group Group `json:"group"`
}
