package review

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Change is one changed file between two trees (git diff --raw
// --numstat): its paths, status (A, M, D, R; a type change is M), modes
// and blobs on both sides, line counts.
type Change struct {
	Path, OldPath    string
	Status           string
	OldMode, NewMode string
	OldBlob, NewBlob string
	Added, Removed   int
	Binary           bool
}

// diffArgs are the options every diff of a review runs with, whatever
// the user's configuration says.
var diffArgs = []string{"--no-color", "--no-ext-diff", "--no-textconv", "--no-relative", "--histogram", "--find-renames"}

// Changes lists the files that differ between base and tree in the
// folder.
func (g Git) Changes(f *Folder, base, tree string) ([]Change, error) {
	args := append([]string{"diff", "-z", "--raw", "--numstat", "--no-abbrev"}, diffArgs...)
	args = append(append(args, base, tree), f.pathspec()...)
	out, err := g.raw(f.Top, nil, nil, args...)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(string(out), "\x00")
	var list []Change
	i := 0
	for ; i < len(fields) && strings.HasPrefix(fields[i], ":"); i++ {
		meta := strings.Fields(fields[i][1:])
		if len(meta) < 5 || i+1 >= len(fields) {
			return nil, fmt.Errorf("git diff --raw: unexpected %q", fields[i])
		}
		c := Change{OldMode: meta[0], NewMode: meta[1], OldBlob: meta[2], NewBlob: meta[3], Status: meta[4][:1]}
		i++
		c.Path = fields[i]
		if c.Status == "R" || c.Status == "C" {
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("git diff --raw: a rename without its new path")
			}
			c.OldPath, c.Path = c.Path, fields[i+1]
			i++
			c.Status = wire.ReviewRenamed
		}
		if c.Status == "T" {
			c.Status = wire.ReviewModified
		}
		list = append(list, c)
	}
	for n := 0; n < len(list) && i < len(fields); n++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			break
		}
		c := &list[n]
		if parts[0] == "-" {
			c.Binary = true
		} else {
			c.Added, _ = strconv.Atoi(parts[0])
			c.Removed, _ = strconv.Atoi(parts[1])
		}
		i++
		if parts[2] == "" {
			i += 2 // a rename: its old and new path follow
		}
	}
	return list, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// patches returns the unified diff of every change, in the order of
// Changes.
func (g Git) patches(f *Folder, base, tree string, context int) ([][]string, error) {
	args := append([]string{"diff", "--src-prefix=a/", "--dst-prefix=b/", "-U" + strconv.Itoa(context)}, diffArgs...)
	args = append(append(args, base, tree), f.pathspec()...)
	out, err := g.raw(f.Top, nil, nil, args...)
	if err != nil {
		return nil, err
	}
	var blocks [][]string
	for _, line := range strings.Split(string(bytes.TrimSuffix(out, []byte("\n"))), "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			blocks = append(blocks, nil)
			continue
		}
		if len(blocks) > 0 {
			blocks[len(blocks)-1] = append(blocks[len(blocks)-1], line)
		}
	}
	return blocks, nil
}

// parseHunks reads one file's patch block into hunks; tooLarge when it
// has more than MaxReviewFileLines lines.
func parseHunks(block []string) (hunks []wire.ReviewHunk, tooLarge bool) {
	if len(block) > wire.MaxReviewFileLines {
		return nil, true
	}
	var h *wire.ReviewHunk
	old, nw := 0, 0
	for _, line := range block {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			hunks = append(hunks, wire.ReviewHunk{OldStart: atoi(m[1]), OldLines: count(m[2]), NewStart: atoi(m[3]), NewLines: count(m[4])})
			h = &hunks[len(hunks)-1]
			old, nw = h.OldStart, h.NewStart
			continue
		}
		if h == nil || line == "" {
			continue // the header before the first hunk
		}
		switch line[0] {
		case ' ':
			h.Lines = append(h.Lines, wire.ReviewLine{Kind: " ", Text: line[1:], Old: old, New: nw})
			old++
			nw++
		case '-':
			h.Lines = append(h.Lines, wire.ReviewLine{Kind: "-", Text: line[1:], Old: old})
			old++
		case '+':
			h.Lines = append(h.Lines, wire.ReviewLine{Kind: "+", Text: line[1:], New: nw})
			nw++
		case '\\':
			if n := len(h.Lines); n > 0 {
				h.Lines[n-1].NoNewline = true
			}
		}
	}
	return hunks, false
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// count is a hunk header's line count: 1 when omitted.
func count(s string) int {
	if s == "" {
		return 1
	}
	return atoi(s)
}

// Diff is review.diff's result for the folder: base against tree (a
// Snapshot), files in reading order with their hunks.
func (g Git) Diff(f *Folder, base, tree string, context int) (wire.ReviewDiff, []Change, error) {
	changes, err := g.Changes(f, base, tree)
	if err != nil {
		return wire.ReviewDiff{}, nil, err
	}
	blocks, err := g.patches(f, base, tree, context)
	if err != nil {
		return wire.ReviewDiff{}, nil, err
	}
	if len(blocks) != len(changes) {
		return wire.ReviewDiff{}, nil, fmt.Errorf("git diff: %d patches for %d changed files", len(blocks), len(changes))
	}
	files := make([]wire.ReviewFile, len(changes))
	for i, c := range changes {
		file := wire.ReviewFile{Path: c.Path, OldPath: c.OldPath, Status: c.Status, Binary: c.Binary, Added: c.Added, Removed: c.Removed}
		if !c.Binary {
			file.Hunks, file.TooLarge = parseHunks(blocks[i])
		}
		files[i] = file
	}
	generated := g.generatedAttr(f, changes)
	for i := range files {
		files[i].Generated = generated[changes[i].Path] || isGenerated(&files[i])
		markFormatting(&files[i])
	}
	markMoved(files)
	words := newWordBudget(time.Now().Add(wordTime))
	for i := range files {
		for j := range files[i].Hunks {
			addWords(&files[i].Hunks[j], words)
		}
	}
	order := readingOrder(files)
	out := wire.ReviewDiff{Base: base, Head: "worktree", Tree: tree, Files: make([]wire.ReviewFile, len(files))}
	ordered := make([]Change, len(files))
	for n, i := range order {
		file := files[i]
		file.Order = n
		for j := range file.Hunks {
			file.Hunks[j].ID = strconv.Itoa(n) + ":" + strconv.Itoa(j)
			if file.Hunks[j].Lines == nil {
				file.Hunks[j].Lines = []wire.ReviewLine{}
			}
		}
		if file.Hunks == nil {
			file.Hunks = []wire.ReviewHunk{}
		}
		out.Files[n], ordered[n] = file, changes[i]
	}
	return out, ordered, nil
}

// generatedAttr: the changed files .gitattributes marks
// linguist-generated.
func (g Git) generatedAttr(f *Folder, changes []Change) map[string]bool {
	if len(changes) == 0 {
		return nil
	}
	var in bytes.Buffer
	for _, c := range changes {
		in.WriteString(c.Path)
		in.WriteByte(0)
	}
	out, err := g.raw(f.Top, nil, &in, "check-attr", "-z", "--stdin", "linguist-generated")
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+2 < len(fields); i += 3 {
		if v := fields[i+2]; v == "set" || v == "true" {
			set[fields[i]] = true
		}
	}
	return set
}
