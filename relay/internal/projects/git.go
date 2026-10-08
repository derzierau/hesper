package projects

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// gitInfo is what a folder's repository is: Top (the checkout the folder
// is in: a worktree's own root), Root (the repository's main worktree, via
// git's common directory, so a worktree maps back to its repository) and
// Remote (normalized; origin, else upstream, else the only/first remote).
type gitInfo struct {
	OK     bool
	Top    string
	Root   string
	Remote string
	// RawRemote is the remote as configured (the default name keeps its
	// case).
	RawRemote string
	at        time.Time
}

// detector looks at folders: git (two git calls per new folder) and
// monorepo packages (manifests; packages.go), both cached.
type detector struct {
	env []string
	ttl time.Duration
	now func() time.Time

	mu   sync.Mutex
	git  map[string]gitInfo // by folder
	pkgs map[string]pkgCache
	// calls counts git invocations (tests: detection is cached).
	calls int
}

func newDetector(env []string, now func() time.Time) *detector {
	if env == nil {
		env = gitEnv(os.Environ())
	}
	return &detector{env: env, ttl: 2 * time.Minute, now: now, git: map[string]gitInfo{}, pkgs: map[string]pkgCache{}}
}

// gitEnv drops the variables that point git at another repository.
func gitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_CEILING_DIRECTORIES", "GIT_PREFIX", "GIT_NAMESPACE":
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (d *detector) run(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = d.env
	var out bytes.Buffer
	cmd.Stdout = &out
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// cached is a folder's git info if it was looked at lately (no git call).
func (d *detector) cached(dir string) (gitInfo, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	gi, ok := d.git[dir]
	if ok && d.now().Sub(gi.at) > d.ttl {
		return gi, false
	}
	return gi, ok
}

// gitOf is a folder's git info: cached for a while, else asked of git.
func (d *detector) gitOf(dir string) gitInfo {
	if gi, ok := d.cached(dir); ok {
		return gi
	}
	gi := gitInfo{at: d.now()}
	out, err := d.run(dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	lines := strings.Split(out, "\n")
	if err == nil && len(lines) == 2 && lines[0] != "" {
		gi.OK = true
		gi.Top = canonical(lines[0])
		common := canonical(lines[1])
		gi.Root = gi.Top
		if filepath.Base(common) == ".git" {
			// A worktree's (or the main checkout's) repository: the
			// main worktree holds the common directory. Submodules and
			// bare repositories keep their own top.
			gi.Root = filepath.Dir(common)
		}
		gi.RawRemote = d.remoteOf(gi.Root)
		gi.Remote = NormalizeRemote(gi.RawRemote)
	}
	d.mu.Lock()
	d.git[dir] = gi
	d.mu.Unlock()
	return gi
}

// remoteOf is a repository's remote URL: origin, else upstream, else
// the first by name.
func (d *detector) remoteOf(root string) string {
	out, err := d.run(root, "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil || out == "" {
		return ""
	}
	urls := map[string]string{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		key, url, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		if _, dup := urls[name]; !dup {
			names = append(names, name)
		}
		urls[name] = strings.TrimSpace(url)
	}
	for _, pick := range []string{"origin", "upstream"} {
		if u, ok := urls[pick]; ok {
			return u
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return urls[names[0]]
	}
	return ""
}

// forget drops cached git info (tests, promote).
func (d *detector) forget(dir string) {
	d.mu.Lock()
	delete(d.git, dir)
	d.mu.Unlock()
}

// canonical is a clean absolute path with symlinks resolved (when it
// exists).
func canonical(p string) string {
	p = filepath.Clean(p)
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	// Not there (yet): its nearest existing folder resolved, the rest
	// as given.
	if parent := filepath.Dir(p); parent != p {
		return filepath.Join(canonical(parent), filepath.Base(p))
	}
	return p
}

// existing is p or its nearest folder that exists (git runs there).
func existing(p string) string {
	for {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p
		}
		p = parent
	}
}

// within: p is dir or below it.
func within(p, dir string) bool {
	if dir == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// rel is p relative to dir ("" for dir itself); p must be within dir.
func rel(dir, p string) string {
	if p == dir {
		return ""
	}
	if dir == "/" {
		return strings.TrimPrefix(p, "/")
	}
	return strings.TrimPrefix(p, dir+"/")
}
