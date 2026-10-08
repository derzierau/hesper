package handoff

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Bring the folder along (docs/rebuild-contract.md, "As built — bring
// the folder"): a folder, without an agent, made on another machine
// before an agent starts there. The bundle directory is a move's
// (manifest.json with "bring", code.bundle), or for a folder that is not
// a Git repository with a commit, folder.tar:
//
//   - a Git repository: its branch and, with changes, a checkpoint of
//     its uncommitted work (the handoff commit); with a remote, the
//     target clones it and the bundle carries only what the remote lacks
//     (incremental from the remote's head the source last saw);
//     without one (a scratch project too), the bundle is full and the
//     target makes the repository from it;
//   - another folder: a tar of it without build and dependency folders
//     (FolderExcludes), symlinks kept only when they point inside it,
//     at most MaxFolderBytes.
//
// The target never overwrites: its folder is made next to where it goes
// and renamed into place only while that is still free.

// FolderFile is a bring's tar of a folder that is not a Git repository.
const FolderFile = "folder.tar"

// MaxFolderBytes caps the files of a brought folder that is not a Git
// repository (error "too-large").
var MaxFolderBytes int64 = DefaultMaxTransfer

// DefaultMaxTransfer is the default cap of one transfer (5 GB):
// settings.json maxTransferMB changes it (Paths.MaxBytes).
const DefaultMaxTransfer int64 = 5 << 30

// maxFolder is the cap of a folder's tar: the configured one, else
// MaxFolderBytes.
func (p Paths) maxFolder() int64 {
	if p.MaxBytes > 0 {
		return p.MaxBytes
	}
	return MaxFolderBytes
}

// FolderExcludes are the folders a tar leaves out (at any depth).
var FolderExcludes = map[string]bool{
	"node_modules": true, ".build": true, "DerivedData": true, "target": true, "dist": true, ".venv": true, "__pycache__": true,
}

// Bring kinds (BringInfo.Kind).
const (
	BringGit    = "git"
	BringFolder = "folder"
)

// BringInfo is a bring's part of a manifest (a manifest with it has no
// agent): what travels and whether the uncommitted work came along.
type BringInfo struct {
	Kind    string `json:"kind"`
	Changes bool   `json:"changes,omitempty"`
}

// BringPlan is what the source tells the controller before a bring:
// the folder (a repository's checkout top), its name, and for a Git
// repository its remote and the commit the source last saw on the
// remote's default branch (a clone has it). ProjectID and Scratch are
// the registry's.
type BringPlan struct {
	Path       string `json:"path"`
	Home       string `json:"home"`
	Name       string `json:"name"`
	Git        bool   `json:"git,omitempty"`
	Remote     string `json:"remote,omitempty"`
	RemoteHead string `json:"remoteHead,omitempty"`
	Branch     string `json:"branch,omitempty"`
	ProjectID  string `json:"projectId,omitempty"`
	Scratch    bool   `json:"scratch,omitempty"`
}

// PlanFolder plans the bring of dir (an existing folder).
func PlanFolder(ctx context.Context, dir string, p Paths) (BringPlan, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return BringPlan{}, Errorf("missing_project", "no folder %s", dir)
	}
	g := p.git(ctx)
	plan := BringPlan{Path: dir, Home: p.Home}
	if r := g.repoInfo(dir); r != nil && r.base != "" {
		plan.Path, plan.Git, plan.Remote, plan.Branch = r.top, true, r.remote, r.branch
		if r.remoteName != "" {
			for _, ref := range []string{"refs/remotes/" + r.remoteName + "/HEAD", "refs/remotes/" + r.remoteName + "/" + r.mainBranch} {
				if head := g.try(r.path, "rev-parse", "--verify", "-q", ref+"^{commit}"); head != "" {
					plan.RemoteHead = head
					break
				}
			}
		}
	}
	plan.Name = filepath.Base(plan.Path)
	return plan, nil
}

// BringDest is where a brought folder goes on this machine (home): a
// scratch project in the scratch folder under its name; a repository
// with a remote at the same place under the home when it is in the
// source's projects or scratch folder, else in the projects root under
// its name; anything else at the same place under the home.
func BringDest(plan BringPlan, home, projectsRoot, scratchRoot string) string {
	mapped := MapPath(plan.Path, plan.Home, home)
	under := func(dir string) bool {
		return dir != "" && strings.HasPrefix(plan.Path, strings.TrimRight(dir, "/")+"/")
	}
	src := strings.TrimRight(plan.Home, "/")
	switch {
	case plan.Scratch && scratchRoot != "":
		return filepath.Join(scratchRoot, plan.Name)
	case plan.Git && plan.Remote != "" && !plan.Scratch:
		if src != "" && (under(src+"/projects") || under(src+"/scratch")) {
			return mapped
		}
		return filepath.Join(projectsRoot, plan.Name)
	case src != "" && under(src):
		return mapped
	}
	return filepath.Join(projectsRoot, plan.Name)
}

// PackFolder writes the bring bundle of the folder plan describes into
// dir (created, 0700): with changes, checkpoint (when it is a checkpoint
// of the folder's HEAD) or a fresh handoff commit carries the
// uncommitted work. have are commits the target will have (a clone's).
func PackFolder(ctx context.Context, plan BringPlan, machine string, have []string, dir string, p Paths, changes bool, checkpoint string) (*Manifest, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	g := p.git(ctx)
	m := &Manifest{Version: Version, ID: newID(), Created: time.Now().Unix(), Bring: &BringInfo{Kind: BringFolder, Changes: true}}
	m.Source.Machine, m.Source.Home = machine, p.Home
	m.Project = ProjectInfo{Path: plan.Path, ProjectID: plan.ProjectID}
	if r := g.repoInfo(plan.Path); plan.Git && r != nil && r.base != "" {
		r.path, r.worktree = r.top, "" // the checkout comes as a repository of its own
		m.Bring = &BringInfo{Kind: BringGit, Changes: changes}
		m.Project = ProjectInfo{Path: r.top, Branch: r.branch, MainBranch: r.mainBranch, Base: r.base, Remote: r.remote, ProjectID: plan.ProjectID}
		handoff := r.base
		if changes {
			handoff = checkpoint
			if !g.usableCheckpoint(r.top, r.base, handoff) {
				var err error
				if handoff, err = g.handoffCommit(r.top, r.base, m.ID); err != nil {
					return nil, err
				}
			}
		}
		m.Project.Handoff = handoff
		kind, newest, err := g.writeBundle(r, handoff, m.ID, have, filepath.Join(dir, BundleFile))
		if err != nil {
			return nil, err
		}
		m.Project.Bundle, m.Project.Have = kind, newest
	} else if err := TarFolder(plan.Path, filepath.Join(dir, FolderFile), p.maxFolder()); err != nil {
		os.Remove(filepath.Join(dir, FolderFile))
		return nil, err
	}
	if err := WriteManifest(dir, m); err != nil {
		return nil, err
	}
	return m, nil
}

// UnpackFolder makes a bring bundle's folder at dest, which must be
// missing or an empty folder (the caller makes it next to where it goes
// and renames it). A repository with a remote and an incremental bundle
// is cloned first; a full bundle makes it without the remote (whose
// URL it keeps as origin).
func UnpackFolder(ctx context.Context, m *Manifest, dir, dest string, p Paths) error {
	if m.Bring == nil {
		return Errorf("manifest", "not a bring")
	}
	if entries, err := os.ReadDir(dest); err == nil && len(entries) > 0 {
		return Errorf("exists", "%s exists", dest)
	}
	if m.Bring.Kind == BringFolder {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		return UntarFolder(filepath.Join(dir, FolderFile), dest, p.maxFolder())
	}
	g := p.git(ctx)
	pr := m.Project
	if pr.Bundle == "incremental" {
		if pr.Remote == "" || strings.HasPrefix(pr.Remote, "-") {
			return Errorf("missing_project", "an incremental bundle needs a remote to clone")
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if _, err := g.run(filepath.Dir(dest), "clone", "-q", "--", pr.Remote, dest); err != nil {
			return err
		}
	}
	fresh, err := g.fetchCode(m, filepath.Join(dir, BundleFile), dest)
	if err != nil {
		return err
	}
	if fresh {
		if err := g.checkOut(dest, dest, pr.Branch, pr.Base, "", pr.MainBranch, true); err != nil {
			return err
		}
	} else {
		// A clone made for this: the branch where the source has it,
		// whatever the remote's says.
		args := []string{"checkout", "-q", "--detach", pr.Base}
		if pr.Branch != "" {
			args = []string{"checkout", "-q", "-B", pr.Branch, pr.Base}
		}
		if _, err := g.run(dest, args...); err != nil {
			return err
		}
		if pr.Branch != "" && g.ok(dest, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+pr.Branch) {
			g.try(dest, "branch", "-q", "--set-upstream-to=origin/"+pr.Branch, pr.Branch)
		}
	}
	defer g.try(dest, "update-ref", "-d", "refs/ghosty/handoff/"+m.ID)
	if m.Bring.Changes && pr.Handoff != pr.Base {
		return g.restoreChanges(dest, pr.Base, pr.Handoff)
	}
	return nil
}

// TarFolder writes root's files to out as a tar: the FolderExcludes
// folders left out, symlinks kept only when they point inside root
// (relative), sockets and devices skipped. Over max bytes of files it
// fails with "too-large".
func TarFolder(root, out string, max int64) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	var total int64
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) && path != root {
				return nil // what the user cannot read stays
			}
			return err
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() && FolderExcludes[d.Name()] {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hdr := &tar.Header{Name: filepath.ToSlash(rel), Mode: int64(info.Mode().Perm()), ModTime: info.ModTime(), Format: tar.FormatPAX}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return nil
			}
			link := insideLink(root, filepath.Dir(path), target)
			if link == "" {
				return nil // never what is outside the folder
			}
			hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, filepath.ToSlash(link)
			return tw.WriteHeader(hdr)
		case info.IsDir():
			hdr.Typeflag, hdr.Name = tar.TypeDir, hdr.Name+"/"
			return tw.WriteHeader(hdr)
		case info.Mode().IsRegular():
			total += info.Size()
			if total > max {
				return Errorf("too-large", "%s is over the %d MB a folder brings (build and dependency folders left out; settings.json maxTransferMB)", filepath.Base(root), max>>20)
			}
			in, err := os.Open(path)
			if err != nil {
				if errors.Is(err, fs.ErrPermission) {
					return nil
				}
				return err
			}
			defer in.Close()
			hdr.Typeflag, hdr.Size = tar.TypeReg, info.Size()
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			_, err = io.CopyN(tw, in, info.Size())
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return f.Close()
}

// insideLink is a symlink's target relative to its folder dir when it
// stays inside root (lexically), else "".
func insideLink(root, dir, target string) string {
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(dir, target)
	}
	abs = filepath.Clean(abs)
	if abs != root && !strings.HasPrefix(abs, root+"/") {
		return ""
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil {
		return ""
	}
	return rel
}

// UntarFolder unpacks a TarFolder tar into dest (an existing folder):
// only folders, files and symlinks that stay inside dest, nothing written
// through a symlink, at most max bytes of files.
func UntarFolder(file, dest string, max int64) error {
	f, err := os.Open(file)
	if err != nil {
		return Errorf("bundle", "%s is missing", FolderFile)
	}
	defer f.Close()
	dest = filepath.Clean(dest)
	tr := tar.NewReader(f)
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return Errorf("bundle", "%s does not read: %v", FolderFile, err)
		}
		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return Errorf("bundle", "%s names %q outside the folder", FolderFile, hdr.Name)
		}
		target := filepath.Join(dest, name)
		if err := realParents(dest, name); err != nil {
			return err
		}
		mode := os.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if st, err := os.Lstat(target); err == nil {
				if !st.IsDir() {
					return Errorf("bundle", "%s twice in %s", name, FolderFile)
				}
				continue
			}
			if err := os.Mkdir(target, mode|0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			total += hdr.Size
			if hdr.Size < 0 || total > max {
				return Errorf("too-large", "%s is over the %d MB a folder brings", FolderFile, max>>20)
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode|0o600)
			if err != nil {
				return err
			}
			if _, err := io.CopyN(out, tr, hdr.Size); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			os.Chtimes(target, hdr.ModTime, hdr.ModTime)
		case tar.TypeSymlink:
			link := filepath.FromSlash(hdr.Linkname)
			if filepath.IsAbs(link) || insideLink(dest, filepath.Dir(target), link) == "" {
				continue // never pointing outside
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
		}
	}
}

// realParents makes sure name's parent folders below dest are folders
// (made when missing), none a symlink.
func realParents(dest, name string) error {
	dir := dest
	parts := strings.Split(filepath.Dir(name), string(filepath.Separator))
	for _, part := range parts {
		if part == "." || part == "" {
			continue
		}
		dir = filepath.Join(dir, part)
		st, err := os.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(dir, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case !st.IsDir():
			return Errorf("bundle", "%s in %s goes through a symlink or file", name, FolderFile)
		}
	}
	return nil
}
