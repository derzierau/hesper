// Package review computes what hesperd's review.* methods show
// (docs/rebuild-contract.md "As built — review"): an agent's folder
// against its review base as files and hunks in reading order, with word
// ranges, formatting-only, moved and generated flags and simple risk
// rules; accepting and rejecting hunks; evidence (commands, edits) and
// provenance from the agents' hook events. Everything runs Git through a
// copy of the index: the user's index is only touched by an accept,
// which commits.
package review

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Git runs git in a folder with an environment that never prompts and
// takes paths literally.
type Git struct {
	Ctx context.Context
	// Env is the base environment (nil: the process's).
	Env []string
}

func (g Git) cmd(dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(g.Ctx, "git", append([]string{"-C", dir}, args...)...)
	base := g.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = append(append(slices.Clone(base), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_LITERAL_PATHSPECS=1"), env...)
	return cmd
}

// run returns stdout, trailing newlines trimmed.
func (g Git) run(dir string, args ...string) (string, error) {
	out, err := g.raw(dir, nil, nil, args...)
	return strings.TrimRight(string(out), "\n"), err
}

func (g Git) runEnv(dir string, env []string, args ...string) (string, error) {
	out, err := g.raw(dir, env, nil, args...)
	return strings.TrimRight(string(out), "\n"), err
}

// raw runs git with stdin and returns its stdout as is.
func (g Git) raw(dir string, env []string, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := g.cmd(dir, env, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &errb, stdin
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(errb.String())
		if i := strings.LastIndexByte(detail, '\n'); i >= 0 {
			detail = detail[i+1:]
		}
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], detail)
	}
	return out.Bytes(), nil
}

func (g Git) try(dir string, args ...string) string {
	out, err := g.run(dir, args...)
	if err != nil {
		return ""
	}
	return out
}

// Folder is where an agent's code is: the repository's top, the agent's
// folder below it (Prefix, "" or "sub/dir/"), HEAD.
type Folder struct {
	Top, Prefix, Head string
	// Branch is the checked-out branch ("" when detached).
	Branch string
}

// Open finds dir's repository; nil (and no error) outside Git or before
// the first commit.
func (g Git) Open(dir string) *Folder {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}
	top := g.try(dir, "rev-parse", "--show-toplevel")
	if top == "" {
		return nil
	}
	head := g.try(top, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	if head == "" {
		return nil
	}
	f := &Folder{Top: top, Head: head, Prefix: g.try(dir, "rev-parse", "--show-prefix")}
	f.Branch = g.try(top, "symbolic-ref", "--short", "-q", "HEAD")
	return f
}

// pathspec limits a command to the folder.
func (f *Folder) pathspec() []string {
	if f.Prefix == "" {
		return nil
	}
	return []string{"--", strings.TrimSuffix(f.Prefix, "/")}
}

// HasCommit reports whether sha is a commit of the folder's repository.
func (g Git) HasCommit(f *Folder, sha string) bool {
	return sha != "" && g.try(f.Top, "rev-parse", "--verify", "-q", sha+"^{commit}") != ""
}

// BranchPoint is where the folder's branch left the main worktree's
// branch (the review base of an agent that recorded none): the merge base
// of HEAD and that branch, else HEAD.
func (g Git) BranchPoint(f *Folder) string {
	common := g.try(f.Top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if strings.HasSuffix(common, "/.git") {
		main := filepath.Dir(common)
		if branch := g.try(main, "symbolic-ref", "--short", "-q", "HEAD"); branch != "" && branch != f.Branch {
			if mb := g.try(f.Top, "merge-base", "HEAD", "refs/heads/"+branch); mb != "" {
				return mb
			}
		}
	}
	return f.Head
}

// HasChanges reports, without writing anything, whether the folder
// differs from base: commits since with changes in it, or uncommitted
// (untracked included) changes.
func (g Git) HasChanges(f *Folder, base string) bool {
	if base != f.Head {
		if _, err := g.run(f.Top, append([]string{"diff", "--quiet", base, f.Head}, f.pathspec()...)...); err != nil {
			return true
		}
	}
	out, err := g.run(f.Top, append([]string{"status", "--porcelain", "--untracked-files=all"}, f.pathspec()...)...)
	return err == nil && out != ""
}

// Snapshot writes the folder's files (untracked included, ignored ones
// left out) as a tree, through a copy of the index: the real index is
// never touched.
func (g Git) Snapshot(f *Folder) (string, error) {
	index, err := g.run(f.Top, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", err
	}
	scratch, err := os.MkdirTemp("", "hesper-review-")
	if err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	defer os.RemoveAll(scratch)
	temp := filepath.Join(scratch, "index")
	env := []string{"GIT_INDEX_FILE=" + temp}
	if st, err := os.Stat(index); err == nil {
		// The copy keeps the index's mtime: Git re-reads files changed in
		// the same second as the index only when the copy is not newer.
		if err := copyFile(index, temp); err != nil {
			return "", fmt.Errorf("snapshot: %w", err)
		}
		os.Chtimes(temp, st.ModTime(), st.ModTime())
	} else if _, err := g.runEnv(f.Top, env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if unmerged, _ := g.runEnv(f.Top, env, "ls-files", "--unmerged"); unmerged != "" {
		return "", fmt.Errorf("the folder has unresolved merge conflicts")
	}
	if _, err := g.runEnv(f.Top, env, append([]string{"add", "-A"}, f.pathspec()...)...); err != nil {
		return "", err
	}
	return g.runEnv(f.Top, env, "write-tree")
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
