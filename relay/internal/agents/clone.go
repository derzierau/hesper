package agents

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// CloneTimeout bounds projects.clone.
var CloneTimeout = 10 * time.Minute

// CloneParams are projects.clone's: the repository's URL and, through the
// gateway, the machine to clone on.
type CloneParams struct {
	URL     string `json:"url"`
	Machine string `json:"machine,omitempty"`
}

// CloneResult is projects.clone's result.
type CloneResult struct {
	Path string `json:"path"`
}

var (
	scpURL  = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)
	repoDir = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// cloneName is the folder a URL clones into: its last path element
// without ".git".
func cloneName(url string) string {
	u := strings.TrimRight(url, "/")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.TrimSuffix(u, ".git")
	if !repoDir.MatchString(u) || u == "." || u == ".." {
		return ""
	}
	return u
}

// Clone clones a Git repository into the projects root (ProjectsRoot,
// $HESPER_PROJECT_ROOT, ~/projects)/<repo>. A clone of the same remote
// already there is returned as it is; anything else there is "exists".
func (r *Registry) Clone(ctx context.Context, p CloneParams) (CloneResult, error) {
	url := strings.TrimSpace(p.URL)
	switch {
	case url == "" || strings.HasPrefix(url, "-") || strings.ContainsAny(url, "\n\r\x00"):
		return CloneResult{}, wire.Errorf(wire.CodeInvalid, "url must be a Git URL")
	case strings.HasPrefix(url, "https://"), strings.HasPrefix(url, "ssh://"), strings.HasPrefix(url, "git://"),
		strings.HasPrefix(url, "file://"), filepath.IsAbs(url), scpURL.MatchString(url):
	default:
		return CloneResult{}, wire.Errorf(wire.CodeInvalid, "url must be https://, ssh://, git@host:path or a local path")
	}
	name := cloneName(url)
	if name == "" {
		return CloneResult{}, wire.Errorf(wire.CodeInvalid, "cannot tell the repository's name from %s", url)
	}
	root := r.opt.ProjectsRoot
	path := filepath.Join(root, name)
	g := func(dir string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(append([]string(nil), r.env...), "GIT_TERMINAL_PROMPT=0")
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		if err != nil {
			msg := strings.TrimSpace(errb.String())
			if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
				msg = msg[i+1:]
			}
			return "", wire.Errorf(wire.CodeInvalid, "git %s: %s", args[0], orDefault(msg, err.Error()))
		}
		return strings.TrimSpace(out.String()), nil
	}
	if _, err := os.Stat(path); err == nil {
		if remote, err := g(path, "remote", "get-url", "origin"); err == nil && remote == url {
			r.mu.Lock()
			r.touchProject(path, time.Now().UTC())
			r.mu.Unlock()
			return CloneResult{Path: path}, nil
		}
		return CloneResult{}, wire.Errorf(wire.CodeExists, "%s exists and is not a clone of %s", path, url)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return CloneResult{}, wire.Errorf(wire.CodeInvalid, "%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, CloneTimeout)
	defer cancel()
	if _, err := g(root, "clone", "-q", "--", url, name); err != nil {
		os.RemoveAll(path)
		return CloneResult{}, err
	}
	r.mu.Lock()
	r.touchProject(path, time.Now().UTC())
	r.mu.Unlock()
	return CloneResult{Path: path}, nil
}
