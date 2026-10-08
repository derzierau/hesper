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
	// Created (scratch projects): when it was made (adopted folders: the
	// date in their name).
	Created time.Time `json:"created,omitzero"`
	// Scratch is a scratch project's lifecycle (kind "scratch" from the
	// catalog; nil for every other kind and for the per-folder
	// "scratch:<folder>" ids).
	Scratch *ScratchInfo `json:"scratch,omitempty"`
}

// Scratch states (ScratchInfo.State).
const (
	ScratchActive   = "active"   // agents run in it
	ScratchResting  = "resting"  // no agents
	ScratchArchived = "archived" // its folder is in <scratch root>/.archive
)

// ScratchInfo is a scratch project's lifecycle: State (active while
// agents run in it, as far as hesperd last looked), Keep (never archived
// or deleted), ArchivedAt, Home (the machine whose folder is the scratch:
// it archives and deletes it) and Git (its folder is a Git repository:
// false for adopted folders with content and no .git; no checkpoints).
type ScratchInfo struct {
	State      string     `json:"state"`
	Keep       bool       `json:"keep"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty"`
	Home       string     `json:"home"`
	Git        bool       `json:"git"`
}

// ProjectListParams are projects.list's (optional): Archived includes
// archived scratch projects.
type ProjectListParams struct {
	Archived bool `json:"archived,omitempty"`
}

// ScratchParams are projects.scratch's: a new scratch project on Machine
// (default this Mac), named Name, else from Task.
type ScratchParams struct {
	Name    string `json:"name,omitempty"`
	Task    string `json:"task,omitempty"`
	Machine string `json:"machine,omitempty"`
}

// ScratchResult is projects.scratch's result: the project and its new
// folder (on its machine).
type ScratchResult struct {
	Project ProjectInfo `json:"project"`
	Path    string      `json:"path"`
}

// ScratchKeepParams are projects.scratchKeep's.
type ScratchKeepParams struct {
	ID   string `json:"id"`
	Keep bool   `json:"keep"`
}

// ScratchSettings are projects.scratchSettings's result, and its params
// with the fields to change (settings.json "scratch").
type ScratchSettings struct {
	ArchiveAfterDays int `json:"archiveAfterDays,omitempty"`
	DeleteAfterDays  int `json:"deleteAfterDays,omitempty"`
}

// Settings keys of settings.get / settings.set (scratch projects).
const (
	SettingScratchArchiveDays = "scratch.archiveAfterDays"
	SettingScratchDeleteDays  = "scratch.deleteAfterDays"
)

// SettingsGetParams are settings.get's: the keys asked (none: all).
type SettingsGetParams struct {
	Keys []string `json:"keys,omitempty"`
}

// SettingsValues are settings.get's result and settings.set's params and
// result: key → value.
type SettingsValues struct {
	Values map[string]int `json:"values"`
}

// CreateRepoGitHub is ProjectPromoteParams.CreateRepo for a private
// GitHub repository (gh repo create).
const CreateRepoGitHub = "github"

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
// project (identity detected), or (ID) a scratch project a repository in
// the projects root; CreateRepo "github" also makes it a private GitHub
// repository.
type ProjectPromoteParams struct {
	Machine    string `json:"machine,omitempty"`
	Path       string `json:"path,omitempty"`
	Name       string `json:"name,omitempty"`
	Kind       string `json:"kind,omitempty"`
	ID         string `json:"id,omitempty"`
	CreateRepo string `json:"createRepo,omitempty"`
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
