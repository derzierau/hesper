package handoff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var shaRE = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// git runs git with an environment that never prompts (ctx bounds it).
type git struct {
	ctx context.Context
	env []string
}

func (g git) cmd(dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(g.ctx, "git", append([]string{"-C", dir}, args...)...)
	base := g.env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = append(append(slices.Clone(base), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"), env...)
	return cmd
}

// run returns trimmed stdout; a failure is an Error with code "git".
func (g git) run(dir string, args ...string) (string, error) {
	return g.runEnv(dir, nil, args...)
}

func (g git) runEnv(dir string, env []string, args ...string) (string, error) {
	cmd := g.cmd(dir, env, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(errb.String())
		if detail == "" {
			detail = strings.TrimSpace(out.String())
		}
		if i := strings.LastIndexByte(detail, '\n'); i >= 0 {
			detail = detail[i+1:]
		}
		if detail == "" {
			detail = err.Error()
		}
		return "", Errorf("git", "git %s failed: %s", args[0], detail)
	}
	return strings.TrimSpace(out.String()), nil
}

// try is run that reports failure as "".
func (g git) try(dir string, args ...string) string {
	out, err := g.run(dir, args...)
	if err != nil {
		return ""
	}
	return out
}

func (g git) ok(dir string, args ...string) bool {
	return g.cmd(dir, nil, args...).Run() == nil
}

// repo is where an agent's code is.
type repo struct {
	top, path, worktree      string
	branch, base, mainBranch string
	remote, remoteName       string
}

func (g git) repoInfo(dir string) *repo {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}
	top := g.try(dir, "rev-parse", "--show-toplevel")
	if top == "" {
		return nil
	}
	common := g.try(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	path := top
	if strings.HasSuffix(common, "/.git") {
		path = filepath.Dir(common)
	}
	r := &repo{top: top, path: path}
	if top != path {
		r.worktree = top
	}
	r.base = g.try(top, "rev-parse", "--verify", "-q", "HEAD")
	r.branch = g.try(top, "symbolic-ref", "--short", "-q", "HEAD")
	r.mainBranch = r.branch
	if path != top {
		r.mainBranch = g.try(path, "symbolic-ref", "--short", "-q", "HEAD")
	}
	remotes := strings.Fields(g.try(top, "remote"))
	if slices.Contains(remotes, "origin") {
		r.remoteName = "origin"
	} else if len(remotes) > 0 {
		r.remoteName = remotes[0]
	}
	if r.remoteName != "" {
		r.remote = g.try(top, "remote", "get-url", r.remoteName)
	}
	return r
}

// snapshotTrees writes (index tree, working tree tree) of a checkout
// through a copy of its index: the real index, branch and files are never
// touched.
func (g git) snapshotTrees(top string) (string, string, error) {
	index, err := g.run(top, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", "", err
	}
	scratch, err := os.MkdirTemp("", "hesper-handoff-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(scratch)
	temp := filepath.Join(scratch, "index")
	env := []string{"GIT_INDEX_FILE=" + temp}
	if st, err := os.Stat(index); err == nil {
		// The copy keeps the index's mtime: Git re-reads files changed in
		// the same second as the index only when the copy is not newer.
		if err := copyFile(index, temp, 0o600); err != nil {
			return "", "", err
		}
		os.Chtimes(temp, st.ModTime(), st.ModTime())
	} else if _, err := g.runEnv(top, env, "read-tree", "HEAD"); err != nil {
		return "", "", err
	}
	if unmerged, _ := g.runEnv(top, env, "ls-files", "--unmerged"); unmerged != "" {
		return "", "", Errorf("conflicts", "the checkout has unresolved merge conflicts; resolve them first")
	}
	indexTree, err := g.runEnv(top, env, "write-tree")
	if err != nil {
		return "", "", err
	}
	if _, err := g.runEnv(top, env, "add", "-A", "--", "."); err != nil {
		return "", "", err
	}
	workTree, err := g.runEnv(top, env, "write-tree")
	return indexTree, workTree, err
}

func (g git) commitEnv(top string) []string {
	var env []string
	if g.try(top, "config", "user.name") == "" && os.Getenv("GIT_AUTHOR_NAME") == "" {
		env = append(env, "GIT_AUTHOR_NAME=Hesper", "GIT_COMMITTER_NAME=Hesper")
	}
	if g.try(top, "config", "user.email") == "" && os.Getenv("GIT_AUTHOR_EMAIL") == "" {
		env = append(env, "GIT_AUTHOR_EMAIL=hesper@localhost", "GIT_COMMITTER_EMAIL=hesper@localhost")
	}
	return env
}

// handoffCommit: base <- index commit (what was staged) <- handoff commit
// (every file, untracked included). Neither is on a branch.
func (g git) handoffCommit(top, base, id string) (string, error) {
	indexTree, workTree, err := g.snapshotTrees(top)
	if err != nil {
		return "", err
	}
	env := g.commitEnv(top)
	indexCommit, err := g.runEnv(top, env, "commit-tree", "--no-gpg-sign", indexTree, "-p", base, "-m", "Hesper handoff "+id+": staged changes")
	if err != nil {
		return "", err
	}
	return g.runEnv(top, env, "commit-tree", "--no-gpg-sign", workTree, "-p", indexCommit, "-m", "Hesper handoff "+id)
}

// usableHaves: the given commits this repository knows that are ancestors
// of base, the one with the most history first.
func (g git) usableHaves(top, base string, haves []string) []string {
	type have struct {
		count int
		sha   string
	}
	var found []have
	seen := map[string]bool{}
	for _, sha := range haves {
		if seen[sha] || !shaRE.MatchString(sha) {
			continue
		}
		seen[sha] = true
		if !g.ok(top, "cat-file", "-e", sha+"^{commit}") || !g.ok(top, "merge-base", "--is-ancestor", sha, base) {
			continue
		}
		full := g.try(top, "rev-parse", sha)
		n, _ := strconv.Atoi(g.try(top, "rev-list", "--count", full))
		found = append(found, have{n, full})
	}
	slices.SortFunc(found, func(a, b have) int { return b.count - a.count })
	out := make([]string, len(found))
	for i, h := range found {
		out[i] = h.sha
	}
	return out
}

// writeBundle writes code.bundle with the handoff ref and the branch,
// incremental from the target's commits when it has any. It returns
// ("full"|"incremental", newest have).
func (g git) writeBundle(r *repo, handoff, id string, haves []string, out string) (string, string, error) {
	// wire name: kept as "refs/ghosty/handoff/" until the next relay deploy
	// (the bundle ref another Mac's daemon reads).
	ref := "refs/ghosty/handoff/" + id
	if _, err := g.run(r.top, "update-ref", ref, handoff); err != nil {
		return "", "", err
	}
	defer g.try(r.top, "update-ref", "-d", ref)
	refs := []string{ref}
	if r.branch != "" {
		refs = append(refs, "refs/heads/"+r.branch)
	}
	usable := g.usableHaves(r.top, r.base, haves)
	if len(usable) == 0 && r.mainBranch != "" && r.mainBranch != r.branch {
		refs = append(refs, "refs/heads/"+r.mainBranch)
	}
	args := append([]string{"bundle", "create", "-q", out}, refs...)
	for _, sha := range usable {
		args = append(args, "^"+sha)
	}
	if _, err := g.run(r.top, args...); err != nil {
		return "", "", err
	}
	if len(usable) > 0 {
		return "incremental", usable[0], nil
	}
	return "full", "", nil
}

func (g git) isRepoAt(path string) bool {
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return false
	}
	top := g.try(path, "rev-parse", "--show-toplevel")
	if top == "" {
		return false
	}
	a, _ := filepath.EvalSymlinks(top)
	b, _ := filepath.EvalSymlinks(path)
	return a == b
}

func (g git) isClean(dir string) bool {
	out, err := g.run(dir, "status", "--porcelain", "--untracked-files=all")
	return err == nil && out == ""
}

// checkedOut is the worktree that has branch checked out, if any.
func (g git) checkedOut(path, branch string) string {
	current := ""
	for _, line := range strings.Split(g.try(path, "worktree", "list", "--porcelain"), "\n") {
		if w, ok := strings.CutPrefix(line, "worktree "); ok {
			current = w
		} else if line == "branch refs/heads/"+branch {
			return current
		}
	}
	return ""
}

// fetchCode makes the handoff commit available in the repository at path
// (created from a full bundle when missing). It reports whether the
// repository is new.
func (g git) fetchCode(m *Manifest, bundle, path string) (bool, error) {
	p := m.Project
	// wire name: kept as "refs/ghosty/handoff/" until the next relay deploy
	// (the bundle ref another Mac's daemon writes).
	ref := "refs/ghosty/handoff/" + m.ID
	fresh := false
	if _, err := os.Stat(bundle); err != nil {
		return false, Errorf("bundle", "code.bundle is missing")
	}
	if !g.isRepoAt(path) {
		if entries, err := os.ReadDir(path); err == nil && len(entries) > 0 {
			return false, Errorf("path_conflict", "%s exists and is not a Git repository", path)
		}
		if p.Bundle != "full" {
			return false, Errorf("missing_project", "no repository at %s for an incremental bundle", path)
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return false, err
		}
		branch := p.MainBranch
		if branch == "" {
			branch = p.Branch
		}
		if branch == "" {
			branch = "main"
		}
		if _, err := g.run(path, "init", "-q", "-b", branch); err != nil {
			return false, err
		}
		if _, err := g.run(path, "fetch", "-q", "--update-head-ok", bundle, "refs/heads/*:refs/heads/*", ref+":"+ref); err != nil {
			return false, err
		}
		if p.Remote != "" {
			g.try(path, "remote", "add", "origin", p.Remote)
		}
		fresh = true
	} else {
		if _, err := g.run(path, "bundle", "verify", "-q", bundle); err != nil {
			return false, Errorf("bundle", "the bundle needs commits this repository lacks: %v", err)
		}
		if _, err := g.run(path, "fetch", "-q", bundle, "+"+ref+":"+ref); err != nil {
			return false, err
		}
	}
	for _, sha := range []string{p.Base, p.Handoff} {
		if !g.ok(path, "cat-file", "-e", sha+"^{commit}") {
			return false, Errorf("bundle", "commit %.12s did not arrive", sha)
		}
	}
	return fresh, nil
}

// prepareBranch points the local branch at base: created, fast-forwarded,
// or refused when this machine has commits on it the source does not.
func (g git) prepareBranch(path, workdir, branch, base string) error {
	existing := g.try(path, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	if existing == base {
		return nil
	}
	if existing != "" && !g.ok(path, "merge-base", "--is-ancestor", existing, base) {
		return Errorf("branch_diverged", "%s here has commits the source does not have", branch)
	}
	holder := ""
	if existing != "" {
		holder = g.checkedOut(path, branch)
	}
	if holder != "" && !samePath(holder, workdir) {
		return Errorf("branch_busy", "%s is checked out at %s", branch, holder)
	}
	if holder != "" {
		if !g.isClean(holder) {
			return Errorf("dirty_target", "%s has uncommitted changes", holder)
		}
		_, err := g.run(holder, "merge", "-q", "--ff-only", base)
		return err
	}
	_, err := g.run(path, "update-ref", "refs/heads/"+branch, base)
	return err
}

// checkOut puts workdir (the project, or its worktree) on branch at base.
func (g git) checkOut(path, workdir, branch, base, worktree, mainBranch string, fresh bool) error {
	if branch != "" {
		if err := g.prepareBranch(path, workdir, branch, base); err != nil {
			return err
		}
	}
	if fresh {
		own := branch
		if worktree != "" {
			own = mainBranch
		}
		if own != "" && g.ok(path, "rev-parse", "--verify", "-q", "refs/heads/"+own) {
			if _, err := g.run(path, "symbolic-ref", "HEAD", "refs/heads/"+own); err != nil {
				return err
			}
			if _, err := g.run(path, "reset", "-q", "--hard", "refs/heads/"+own); err != nil {
				return err
			}
		} else if worktree == "" {
			if _, err := g.run(path, "checkout", "-q", "--detach", base); err != nil {
				return err
			}
		}
	}
	if worktree != "" {
		if _, err := os.Stat(worktree); err == nil {
			common := g.try(worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
			if !g.isRepoAt(worktree) || !samePath(common, filepath.Join(path, ".git")) {
				return Errorf("path_conflict", "%s exists and is not a worktree of %s", worktree, path)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
				return err
			}
			args := []string{"worktree", "add", "-q", "--detach", worktree, base}
			if branch != "" {
				args = []string{"worktree", "add", "-q", worktree, branch}
			}
			_, err := g.run(path, args...)
			return err
		}
	}
	head := g.try(workdir, "rev-parse", "HEAD")
	current := g.try(workdir, "symbolic-ref", "--short", "-q", "HEAD")
	if head == base && current == branch {
		return nil
	}
	if !g.isClean(workdir) {
		return Errorf("dirty_target", "%s has uncommitted changes", workdir)
	}
	target := branch
	if target == "" {
		_, err := g.run(workdir, "checkout", "-q", "--detach", base)
		return err
	}
	_, err := g.run(workdir, "checkout", "-q", target)
	return err
}

// restoreChanges puts the handoff commit's files back as uncommitted
// changes and its staged part in the index: git status as on the source.
func (g git) restoreChanges(workdir, base, handoff string) error {
	if g.try(workdir, "rev-parse", "HEAD") != base {
		return Errorf("dirty_target", "%s is not at the base commit", workdir)
	}
	if !g.isClean(workdir) {
		if _, tree, err := g.snapshotTrees(workdir); err == nil && tree == g.try(workdir, "rev-parse", handoff+"^{tree}") {
			return nil // restored by an earlier run of this handoff
		}
		return Errorf("dirty_target", "%s has uncommitted changes", workdir)
	}
	indexCommit, err := g.run(workdir, "rev-parse", handoff+"^")
	if err != nil {
		return err
	}
	if _, err := g.run(workdir, "read-tree", "-m", "-u", base, handoff); err != nil {
		return err
	}
	if _, err := g.run(workdir, "read-tree", indexCommit); err != nil {
		return err
	}
	g.try(workdir, "update-index", "-q", "--refresh")
	return nil
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Error is a handoff failure with a code (the codes ghosty-handoff used).
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Errorf makes an Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf is err's code ("" for other errors).
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
