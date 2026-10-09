package review

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Accepting and rejecting: a selection of a diff's changes (whole files,
// or hunks of text files) applied to the base, file by file. An accept
// commits the result on HEAD; a reject writes the changes it keeps to
// the working tree.

// FileSelection is what is selected of one file: all of it, or hunks by
// index.
type FileSelection struct {
	Whole bool
	Hunks map[int]bool
}

// Selection is by file index of the diff.
type Selection map[int]*FileSelection

// ParseSelection reads ids against d: "<file>:<hunk>" selects a hunk,
// "<file>" a whole file (binary, too large or renamed files are only
// selected whole).
func ParseSelection(d wire.ReviewDiff, ids []string) (Selection, error) {
	sel := Selection{}
	for _, id := range ids {
		fs, hs, hunk := strings.Cut(strings.TrimSpace(id), ":")
		fi, err := strconv.Atoi(fs)
		if err != nil || fi < 0 || fi >= len(d.Files) {
			return nil, fmt.Errorf("no hunk %q in this diff", id)
		}
		s := sel[fi]
		if s == nil {
			s = &FileSelection{Hunks: map[int]bool{}}
			sel[fi] = s
		}
		if !hunk {
			s.Whole = true
			continue
		}
		hi, err := strconv.Atoi(hs)
		if err != nil || hi < 0 || hi >= len(d.Files[fi].Hunks) {
			return nil, fmt.Errorf("no hunk %q in this diff", id)
		}
		s.Hunks[hi] = true
	}
	return sel, nil
}

// All selects every file of d whole.
func All(d wire.ReviewDiff) Selection {
	sel := Selection{}
	for i := range d.Files {
		sel[i] = &FileSelection{Whole: true}
	}
	return sel
}

// entry is a path's content: absent (mode ""), a blob, or content to
// write.
type entry struct {
	mode, blob string
	content    []byte
	computed   bool
}

const zeroSHA = "0000000000000000000000000000000000000000"

func side(mode, blob string) entry {
	if mode == "" || strings.Trim(mode, "0") == "" || blob == zeroSHA {
		return entry{}
	}
	return entry{mode: mode, blob: blob}
}

// apply is the content of every path file i of d touches with sel's part
// of its changes applied to the base (nil sel: none).
func (g Git) apply(f *Folder, d wire.ReviewDiff, changes []Change, i int, sel *FileSelection, out map[string]entry) error {
	c, file := changes[i], d.Files[i]
	oldSide, newSide := side(c.OldMode, c.OldBlob), side(c.NewMode, c.NewBlob)
	oldPath := c.Path
	if c.OldPath != "" {
		oldPath = c.OldPath
	}
	all := sel != nil && (sel.Whole || (len(file.Hunks) > 0 && len(sel.Hunks) == len(file.Hunks)))
	none := sel == nil || (!sel.Whole && len(sel.Hunks) == 0)
	// A path another file of the diff fills (a renamed file's old
	// path, added anew) is never emptied.
	set := func(p string, e entry) {
		if prev, ok := out[p]; !ok || e.mode != "" || prev.mode == "" {
			out[p] = e
		}
	}
	switch {
	case all:
		set(oldPath, entry{})
		set(c.Path, newSide)
	case none:
		set(c.Path, entry{})
		set(oldPath, oldSide)
	default:
		if file.Binary || file.TooLarge {
			return fmt.Errorf("%s can only be taken whole", c.Path)
		}
		oldText, err := g.blob(f, oldSide.blob)
		if err != nil {
			return err
		}
		newText, err := g.blob(f, newSide.blob)
		if err != nil {
			return err
		}
		merged, err := merge(oldText, newText, file.Hunks, sel.Hunks)
		if err != nil {
			return fmt.Errorf("%s: %w", c.Path, err)
		}
		mode := newSide.mode
		if mode == "" {
			mode = oldSide.mode
		}
		set(oldPath, entry{})
		set(c.Path, entry{mode: mode, content: merged, computed: true})
	}
	return nil
}

func (g Git) blob(f *Folder, sha string) ([]byte, error) {
	if sha == "" {
		return nil, nil
	}
	return g.raw(f.Top, nil, nil, "cat-file", "blob", sha)
}

// lines splits text after each newline (the last line may have none).
func lines(text []byte) [][]byte {
	var out [][]byte
	for len(text) > 0 {
		i := bytes.IndexByte(text, '\n')
		if i < 0 {
			out = append(out, text)
			break
		}
		out = append(out, text[:i+1])
		text = text[i+1:]
	}
	return out
}

// merge is old with the selected hunks' new side in place of their old
// side.
func merge(oldText, newText []byte, hunks []wire.ReviewHunk, selected map[int]bool) ([]byte, error) {
	old, nw := lines(oldText), lines(newText)
	var out bytes.Buffer
	cursor := 0
	for j, h := range hunks {
		start := h.OldStart - 1
		if h.OldLines == 0 {
			start = h.OldStart // an insertion after line OldStart
		}
		if start < cursor || start+h.OldLines > len(old) {
			return nil, fmt.Errorf("hunk %d does not fit the file", j)
		}
		for _, l := range old[cursor:start] {
			out.Write(l)
		}
		if selected[j] {
			from := h.NewStart - 1
			if h.NewLines == 0 {
				from = h.NewStart
			}
			if from < 0 || from+h.NewLines > len(nw) {
				return nil, fmt.Errorf("hunk %d does not fit the new file", j)
			}
			for _, l := range nw[from : from+h.NewLines] {
				out.Write(l)
			}
		} else {
			for _, l := range old[start : start+h.OldLines] {
				out.Write(l)
			}
		}
		cursor = start + h.OldLines
	}
	for _, l := range old[cursor:] {
		out.Write(l)
	}
	return out.Bytes(), nil
}

// commitEnv names Hesper as the author when Git has no name or email.
func (g Git) commitEnv(f *Folder) []string {
	var env []string
	if g.try(f.Top, "config", "user.name") == "" && os.Getenv("GIT_AUTHOR_NAME") == "" && !hasEnv(g.Env, "GIT_AUTHOR_NAME") {
		env = append(env, "GIT_AUTHOR_NAME=Hesper", "GIT_COMMITTER_NAME=Hesper")
	}
	if g.try(f.Top, "config", "user.email") == "" && os.Getenv("GIT_AUTHOR_EMAIL") == "" && !hasEnv(g.Env, "GIT_AUTHOR_EMAIL") {
		env = append(env, "GIT_AUTHOR_EMAIL=hesper@localhost", "GIT_COMMITTER_EMAIL=hesper@localhost")
	}
	return env
}

func hasEnv(env []string, name string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}

// nulPaths is paths, each ended by NUL (for --stdin and pathspec files).
func nulPaths(paths []string) *bytes.Buffer {
	var b bytes.Buffer
	for _, p := range paths {
		b.WriteString(p)
		b.WriteByte(0)
	}
	return &b
}

// Accept commits sel's changes of d (whole files or hunks; nil: all of
// them) on HEAD: the commit's tree is HEAD's with the folder's paths as
// the base plus the selection, so what the reviewer did not take stays
// in the working tree as uncommitted changes, also of commits the agent
// made since the base. The commit is made with the user's Git
// configuration (signing included); the index entries of the paths it
// changes are set to it. It returns the commit (HEAD when nothing
// changes).
func (g Git) Accept(f *Folder, base string, d wire.ReviewDiff, changes []Change, sel Selection, message string) (string, error) {
	if sel == nil {
		sel = All(d)
	}
	entries := map[string]entry{}
	for i := range d.Files {
		if err := g.apply(f, d, changes, i, sel[i], entries); err != nil {
			return "", err
		}
	}
	// Paths the agent's commits changed but its folder has as the base.
	out, err := g.raw(f.Top, nil, nil, append([]string{"diff", "--name-only", "-z", "--no-renames", "--no-relative", base, f.Head}, f.pathspec()...)...)
	if err != nil {
		return "", err
	}
	var committed []string
	for _, p := range strings.Split(string(out), "\x00") {
		if _, ok := entries[p]; p != "" && !ok {
			committed = append(committed, p)
			entries[p] = entry{}
		}
	}
	if len(committed) > 0 {
		listed, err := g.raw(f.Top, nil, nil, append([]string{"ls-tree", "-z", "--full-tree", base, "--"}, committed...)...)
		if err != nil {
			return "", err
		}
		for _, line := range strings.Split(string(listed), "\x00") {
			meta, p, ok := strings.Cut(line, "\t")
			if fields := strings.Fields(meta); ok && len(fields) == 3 {
				entries[p] = entry{mode: fields[0], blob: fields[2]}
			}
		}
	}
	scratch, err := os.MkdirTemp("", "hesper-accept-")
	if err != nil {
		return "", fmt.Errorf("accept: %w", err)
	}
	defer os.RemoveAll(scratch)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index")}
	if _, err := g.runEnv(f.Top, env, "read-tree", f.Head); err != nil {
		return "", err
	}
	var info bytes.Buffer
	for p, e := range entries {
		if e.computed {
			sha, err := g.raw(f.Top, nil, bytes.NewReader(e.content), "hash-object", "-w", "--stdin")
			if err != nil {
				return "", err
			}
			e.blob = strings.TrimSpace(string(sha))
		}
		if e.mode == "" {
			fmt.Fprintf(&info, "0 %s\t%s\x00", zeroSHA, p)
		} else {
			fmt.Fprintf(&info, "%s %s\t%s\x00", e.mode, e.blob, p)
		}
	}
	if _, err := g.raw(f.Top, env, &info, "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	tree, err := g.runEnv(f.Top, env, "write-tree")
	if err != nil {
		return "", err
	}
	if tree == g.try(f.Top, "rev-parse", f.Head+"^{tree}") {
		return f.Head, nil
	}
	commit, err := g.runEnv(f.Top, g.commitEnv(f), "commit-tree", tree, "-p", f.Head, "-m", message)
	if err != nil {
		return "", err
	}
	if _, err := g.run(f.Top, "update-ref", "-m", "hesper review: accept", "HEAD", commit, f.Head); err != nil {
		return "", err
	}
	// The index follows the commit for the paths it changed.
	changed, err := g.raw(f.Top, nil, nil, "diff", "--name-only", "-z", "--no-renames", "--no-relative", f.Head, commit)
	if err != nil {
		return "", err
	}
	var paths []string
	for _, p := range strings.Split(string(changed), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) > 0 {
		if _, err := g.raw(f.Top, nil, nulPaths(paths), "reset", "-q", commit, "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return commit, err
		}
		g.raw(f.Top, nil, nil, "update-index", "-q", "--refresh")
	}
	return commit, nil
}

// Reject reverts sel's changes in the working tree: every file it names
// gets the base plus the changes not rejected. The index is left as it
// is.
func (g Git) Reject(f *Folder, d wire.ReviewDiff, changes []Change, sel Selection) error {
	entries := map[string]entry{}
	for i, s := range sel {
		keep := &FileSelection{Hunks: map[int]bool{}}
		if !s.Whole {
			for j := range d.Files[i].Hunks {
				if !s.Hunks[j] {
					keep.Hunks[j] = true
				}
			}
		}
		if err := g.apply(f, d, changes, i, keep, entries); err != nil {
			return err
		}
	}
	for p, e := range entries {
		if err := g.writeFile(f, p, e); err != nil {
			return err
		}
	}
	return nil
}

// writeFile puts one path of the working tree in e's state.
func (g Git) writeFile(f *Folder, p string, e entry) error {
	target := filepath.Join(f.Top, filepath.FromSlash(p))
	if rel, err := filepath.Rel(f.Top, target); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%s is outside the folder", p)
	}
	if e.mode == "" {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("reject: %w", err)
		}
		return nil
	}
	content := e.content
	if !e.computed {
		var err error
		if content, err = g.blob(f, e.blob); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("reject: %w", err)
	}
	if st, err := os.Lstat(target); err == nil && st.IsDir() {
		return fmt.Errorf("%s is a folder now", p)
	}
	switch e.mode {
	case "120000":
		os.Remove(target)
		if err := os.Symlink(string(content), target); err != nil {
			return fmt.Errorf("reject: %w", err)
		}
		return nil
	case "160000":
		return fmt.Errorf("%s is a submodule: change it there", p)
	}
	perm := os.FileMode(0o644)
	if e.mode == "100755" {
		perm = 0o755
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".hesper-*")
	if err != nil {
		return fmt.Errorf("reject: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("reject: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("reject: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("reject: %w", err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("reject: %w", err)
	}
	return nil
}

// MarkReviewed keeps tree (a Snapshot) as the folder's reviewed point:
// a commit on HEAD at ref, so it stays until pruned.
func (g Git) MarkReviewed(f *Folder, tree, ref string) (string, error) {
	commit, err := g.runEnv(f.Top, g.commitEnv(f), "commit-tree", "--no-gpg-sign", tree, "-p", f.Head, "-m", "Hesper reviewed point")
	if err != nil {
		return "", err
	}
	if _, err := g.run(f.Top, "update-ref", ref, commit); err != nil {
		return "", err
	}
	return commit, nil
}
