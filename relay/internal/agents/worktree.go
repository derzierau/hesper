package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

var slugWord = regexp.MustCompile(`[a-z0-9]+`)

// Slugify is the task's words, lowercased and joined by "-", at most limit
// characters (whole words), "task" when there are none.
func Slugify(text string, limit int) string {
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
	if slug == "" {
		return "task"
	}
	return slug
}

// NameFor is an agent's display name from its task: the slug of the
// task's first line.
func NameFor(task string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(task), "\n")
	return Slugify(first, 40)
}

// sessionName is the --remote-control name: the task's first line,
// controls blanked, at most 60 characters.
func sessionName(task, fallback string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(task), "\n")
	first = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(first))
	if first == "" {
		first = fallback
	}
	if utf8.RuneCountInString(first) > 60 {
		first = string([]rune(first)[:60])
	}
	return first
}

type git struct {
	env []string
}

func (g git) run(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	if p, err := lookPathEnv("git", g.env); err == nil {
		cmd.Path, cmd.Err = p, nil
	}
	cmd.Env = append(g.env, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
}

func (g git) ok(dir string, args ...string) bool {
	_, err := g.run(dir, args...)
	return err == nil
}

// mainRoot is the root of the main worktree of dir's repository ("" outside
// Git).
func (g git) mainRoot(dir string) string {
	top, err := g.run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	common, err := g.run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err == nil && strings.HasSuffix(common, "/.git") {
		return filepath.Dir(common)
	}
	return top
}

// projectKey names a project under the worktree root: its path below the
// projects root, else its base name.
func projectKey(project, projectsRoot string) string {
	if rel, err := filepath.Rel(projectsRoot, project); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
		return rel
	}
	return filepath.Base(project)
}

// makeWorktree creates (or takes) the worktree of a spawn. want is the
// spawn's worktree param (true or a path), branch its branch ("" for
// worktree/<slug>). It returns the worktree's path and branch.
func (g git) makeWorktree(project string, want json.RawMessage, branch, slug, worktreeRoot, projectsRoot string) (string, string, error) {
	var path string
	var flag bool
	switch {
	case len(want) == 0 || string(want) == "null" || string(want) == "false":
		if branch == "" {
			return "", "", nil
		}
		flag = true // a branch means its own worktree
	case json.Unmarshal(want, &flag) == nil:
		if !flag {
			return "", "", nil
		}
	case json.Unmarshal(want, &path) == nil:
		path = cleanPath(path)
		if !filepath.IsAbs(path) {
			return "", "", wire.Errorf(wire.CodeInvalid, "worktree must be true or an absolute path")
		}
	default:
		return "", "", wire.Errorf(wire.CodeInvalid, "worktree must be true or a path")
	}
	root := g.mainRoot(project)
	if root == "" {
		return "", "", wire.Errorf(wire.CodeInvalid, "%s is not a Git repository", project)
	}
	if !g.ok(project, "rev-parse", "--verify", "-q", "HEAD") {
		return "", "", wire.Errorf(wire.CodeInvalid, "the project has no commit to branch from yet")
	}
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			// An existing worktree of this repository.
			common, err := g.run(path, "rev-parse", "--path-format=absolute", "--git-common-dir")
			if err != nil || !sameDir(filepath.Dir(common), root) {
				return "", "", wire.Errorf(wire.CodeExists, "%s exists and is not a worktree of %s", path, root)
			}
			current, _ := g.run(path, "symbolic-ref", "--short", "-q", "HEAD")
			return path, current, nil
		}
	} else {
		key := projectKey(root, projectsRoot)
		base, baseBranch := slug, branch
		path = filepath.Join(worktreeRoot, key, base)
		if branch == "" {
			branch = "worktree/" + base
		}
		for n := 2; exists(path) || (baseBranch == "" && g.ok(root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)); n++ {
			suffix := base + "-" + strconv.Itoa(n)
			path = filepath.Join(worktreeRoot, key, suffix)
			if baseBranch == "" {
				branch = "worktree/" + suffix
			}
		}
	}
	if branch == "" {
		branch = "worktree/" + slug
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", err
	}
	var err error
	if g.ok(root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch) {
		_, err = g.run(root, "worktree", "add", "-q", path, branch)
	} else {
		_, err = g.run(root, "worktree", "add", "-q", "-b", branch, path, "HEAD")
	}
	if err != nil {
		return "", "", wire.Errorf(wire.CodeInvalid, "%v", err)
	}
	return path, branch, nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func lookPathEnv(name string, env []string) (string, error) {
	for _, dir := range filepath.SplitList(envValue(env, "PATH")) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return exec.LookPath(name)
}

// sameDir compares directories through symlinks (/var is /private/var).
func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
