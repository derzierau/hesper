package sessions

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Git state for sessions.show ("changed files" on the card), computed on
// the session's home only when shown, and cached by (folder, HEAD, index
// mtime): a cache check reads three small files and runs no git, so a
// repeated show costs microseconds. A cached answer older than 15 s is
// returned at once and refreshed in the background (edits to tracked
// files do not touch the index).

const maxChangedFiles = 200

type gitEntry struct {
	sig     string
	at      time.Time
	changes *wire.SessionChanges
	busy    bool
}

type gitCache struct {
	mu sync.Mutex
	m  map[string]*gitEntry
}

func newGitCache() *gitCache { return &gitCache{m: map[string]*gitEntry{}} }

// gitDirOf finds the git directory of dir (and the common one of a
// worktree) without running git.
func gitDirOf(dir string) (gitDir, common string) {
	for d := dir; ; {
		p := filepath.Join(d, ".git")
		if st, err := os.Stat(p); err == nil {
			if st.IsDir() {
				return p, p
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return "", ""
			}
			g := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
			if !filepath.IsAbs(g) {
				g = filepath.Join(d, g)
			}
			common = g
			if c, err := os.ReadFile(filepath.Join(g, "commondir")); err == nil {
				cd := strings.TrimSpace(string(c))
				if !filepath.IsAbs(cd) {
					cd = filepath.Join(g, cd)
				}
				common = filepath.Clean(cd)
			}
			return g, common
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ""
		}
		d = parent
	}
}

// signature is (HEAD, the commit it names, the index's mtime).
func signature(dir string) string {
	gd, common := gitDirOf(dir)
	if gd == "" {
		return ""
	}
	head, err := os.ReadFile(filepath.Join(gd, "HEAD"))
	if err != nil {
		return ""
	}
	h := strings.TrimSpace(string(head))
	commit := ""
	if ref, ok := strings.CutPrefix(h, "ref: "); ok {
		if data, err := os.ReadFile(filepath.Join(common, ref)); err == nil {
			commit = strings.TrimSpace(string(data))
		} else if data, err := os.ReadFile(filepath.Join(common, "packed-refs")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasSuffix(line, " "+ref) {
					commit = strings.Fields(line)[0]
				}
			}
		}
	}
	var mtime int64
	if st, err := os.Stat(filepath.Join(gd, "index")); err == nil {
		mtime = st.ModTime().UnixNano()
	}
	return h + "|" + commit + "|" + strconv.FormatInt(mtime, 10)
}

// get is the git state of dir (env: the agents' environment for git).
func (c *gitCache) get(dir string, env []string) *wire.SessionChanges {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return &wire.SessionChanges{Files: []wire.SessionFile{}, WorktreeExists: false}
	}
	sig := signature(dir)
	c.mu.Lock()
	e := c.m[dir]
	if e != nil && e.sig == sig && e.changes != nil {
		if time.Since(e.at) > 15*time.Second && !e.busy {
			e.busy = true
			go func() {
				ch := computeChanges(dir, env)
				c.mu.Lock()
				e.changes, e.at, e.busy = ch, time.Now(), false
				c.mu.Unlock()
			}()
		}
		out := e.changes
		c.mu.Unlock()
		return out
	}
	c.mu.Unlock()
	ch := computeChanges(dir, env)
	c.mu.Lock()
	c.m[dir] = &gitEntry{sig: sig, at: time.Now(), changes: ch}
	c.mu.Unlock()
	return ch
}

func gitRun(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	if env != nil {
		cmd.Env = env
	}
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err
}

// computeChanges: files changed against the branch's upstream (else the
// main branch), uncommitted changes, ahead/behind the upstream.
func computeChanges(dir string, env []string) *wire.SessionChanges {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch := &wire.SessionChanges{Files: []wire.SessionFile{}, WorktreeExists: true}
	status, err := gitRun(ctx, dir, env, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return ch
	}
	var untracked []string
	for _, e := range strings.Split(status, "\x00") {
		if len(e) < 4 {
			continue
		}
		ch.Uncommitted = true
		if strings.HasPrefix(e, "?? ") {
			untracked = append(untracked, e[3:])
		}
	}
	base := ""
	if up, err := gitRun(ctx, dir, env, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil && strings.TrimSpace(up) != "" {
		if counts, err := gitRun(ctx, dir, env, "rev-list", "--left-right", "--count", "@{upstream}...HEAD"); err == nil {
			f := strings.Fields(counts)
			if len(f) == 2 {
				behind, _ := strconv.Atoi(f[0])
				ahead, _ := strconv.Atoi(f[1])
				ch.Ahead, ch.Behind = &ahead, &behind
			}
		}
		base = "@{upstream}"
	} else {
		for _, ref := range []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"} {
			if _, err := gitRun(ctx, dir, env, "rev-parse", "--verify", "-q", ref); err == nil {
				base = ref
				break
			}
		}
	}
	from := "HEAD"
	if base != "" {
		if mb, err := gitRun(ctx, dir, env, "merge-base", base, "HEAD"); err == nil && strings.TrimSpace(mb) != "" {
			from = strings.TrimSpace(mb)
		}
	}
	if numstat, err := gitRun(ctx, dir, env, "diff", "--numstat", from); err == nil {
		for _, line := range strings.Split(numstat, "\n") {
			f := strings.SplitN(line, "\t", 3)
			if len(f) != 3 || len(ch.Files) >= maxChangedFiles {
				continue
			}
			added, _ := strconv.Atoi(f[0])
			removed, _ := strconv.Atoi(f[1])
			ch.Files = append(ch.Files, wire.SessionFile{Path: f[2], Added: added, Removed: removed})
		}
	}
	for _, p := range untracked {
		if len(ch.Files) >= maxChangedFiles {
			break
		}
		ch.Files = append(ch.Files, wire.SessionFile{Path: p})
	}
	return ch
}
