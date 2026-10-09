package review

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// numbered is n lines "line 1".."line n".
func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// review is the folder's diff and changes against base, now.
func reviewOf(t *testing.T, dir, base string) (*Folder, wire.ReviewDiff, []Change) {
	t.Helper()
	g := testGit()
	f := g.Open(dir)
	tree, err := g.Snapshot(f)
	if err != nil {
		t.Fatal(err)
	}
	d, changes, err := g.Diff(f, base, tree, wire.DefaultReviewContext)
	if err != nil {
		t.Fatal(err)
	}
	return f, d, changes
}

func fileIndex(t *testing.T, d wire.ReviewDiff, path string) int {
	t.Helper()
	for i, f := range d.Files {
		if f.Path == path {
			return i
		}
	}
	t.Fatalf("no %s", path)
	return -1
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "<missing>"
	}
	return string(data)
}

// Accept everything: the commit is the folder; status clean after.
func TestAcceptAll(t *testing.T) {
	dir, base := newRepo(t, map[string]string{"a.txt": "a\n", "gone.txt": "bye\n", "old.go": longFile})
	write(t, dir, "a.txt", "a\nb\n")
	os.Remove(filepath.Join(dir, "gone.txt"))
	os.Rename(filepath.Join(dir, "old.go"), filepath.Join(dir, "new.go"))
	write(t, dir, "fresh.txt", "fresh\n")
	f, d, changes := reviewOf(t, dir, base)
	commit, err := testGit().Accept(f, base, d, changes, nil, "Accept all")
	if err != nil {
		t.Fatal(err)
	}
	if gitIn(t, dir, "rev-parse", "HEAD") != commit || gitIn(t, dir, "rev-parse", "HEAD^") != base {
		t.Fatal("HEAD is not the accept commit on the base")
	}
	if gitIn(t, dir, "rev-parse", commit+"^{tree}") != d.Tree || gitIn(t, dir, "log", "-1", "--format=%s") != "Accept all" {
		t.Fatal("commit")
	}
	if st := gitIn(t, dir, "status", "--porcelain"); st != "" {
		t.Fatalf("status after accept: %q", st)
	}
	// Nothing left: accepting again is HEAD.
	f, d, changes = reviewOf(t, dir, commit)
	if again, err := testGit().Accept(f, commit, d, changes, nil, "x"); err != nil || again != commit || len(d.Files) != 0 {
		t.Fatalf("again %s %v %d", again, err, len(d.Files))
	}
}

// Accept one hunk: only it is committed; the rest stays uncommitted.
func TestAcceptHunks(t *testing.T) {
	text := numbered(30)
	dir, base := newRepo(t, map[string]string{"x.txt": text, "y.txt": "y\n"})
	changed := strings.Replace(strings.Replace(text, "line 2\n", "line two\n", 1), "line 25\n", "line twenty-five\n", 1)
	write(t, dir, "x.txt", changed)
	write(t, dir, "y.txt", "y\ny\n")
	f, d, changes := reviewOf(t, dir, base)
	x := fileIndex(t, d, "x.txt")
	if len(d.Files[x].Hunks) != 2 {
		t.Fatalf("hunks %+v", d.Files[x].Hunks)
	}
	sel, err := ParseSelection(d, []string{fmt.Sprintf("%d:0", x)})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := testGit().Accept(f, base, d, changes, sel, "first hunk")
	if err != nil {
		t.Fatal(err)
	}
	committed := gitIn(t, dir, "show", commit+":x.txt") + "\n"
	if committed != strings.Replace(text, "line 2\n", "line two\n", 1) {
		t.Fatalf("committed x.txt:\n%s", committed)
	}
	if gitIn(t, dir, "show", commit+":y.txt") != "y" || read(t, dir, "x.txt") != changed || read(t, dir, "y.txt") != "y\ny\n" {
		t.Fatal("the rest changed")
	}
	if cached := gitIn(t, dir, "diff", "--cached", "--name-only"); cached != "" {
		t.Fatalf("staged after accept: %q", cached)
	}
	// What is left to review: the second hunk and y.txt.
	_, rest, _ := reviewOf(t, dir, commit)
	if len(rest.Files) != 2 || len(rest.Files[fileIndex(t, rest, "x.txt")].Hunks) != 1 {
		t.Fatalf("left %+v", paths(rest))
	}
	// Bad ids.
	for _, bad := range []string{"9:0", "0:9", "x", ""} {
		if _, err := ParseSelection(d, []string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The agent committed since its base: an accept of part of its work
// commits the base plus that part; the rest is uncommitted again.
func TestAcceptAfterAgentCommit(t *testing.T) {
	dir, base := newRepo(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	write(t, dir, "a.txt", "a\nagent committed\n")
	gitIn(t, dir, "commit", "-q", "--no-gpg-sign", "-am", "agent")
	write(t, dir, "b.txt", "b\nuncommitted\n")
	f, d, changes := reviewOf(t, dir, base)
	sel, _ := ParseSelection(d, []string{fmt.Sprint(fileIndex(t, d, "b.txt"))})
	commit, err := testGit().Accept(f, base, d, changes, sel, "b only")
	if err != nil {
		t.Fatal(err)
	}
	if gitIn(t, dir, "show", commit+":a.txt") != "a" || gitIn(t, dir, "show", commit+":b.txt") != "b\nuncommitted" {
		t.Fatal("commit content")
	}
	if read(t, dir, "a.txt") != "a\nagent committed\n" || gitIn(t, dir, "status", "--porcelain") != "M a.txt" {
		t.Fatalf("status %q", gitIn(t, dir, "status", "--porcelain"))
	}
}

// Reject: hunks and whole files go back to the base in the working tree;
// the index is not touched.
func TestReject(t *testing.T) {
	text := numbered(30)
	dir, base := newRepo(t, map[string]string{"x.txt": text, "gone.txt": "bye\n", "old.go": longFile})
	changed := strings.Replace(strings.Replace(text, "line 2\n", "line two\n", 1), "line 25\n", "line twenty-five\n", 1)
	write(t, dir, "x.txt", changed)
	os.Remove(filepath.Join(dir, "gone.txt"))
	os.Rename(filepath.Join(dir, "old.go"), filepath.Join(dir, "new.go"))
	write(t, dir, "fresh.txt", "fresh\n")
	write(t, dir, "bin/run.sh", "#!/bin/sh\n")
	os.Chmod(filepath.Join(dir, "bin/run.sh"), 0o755)
	index, _ := os.ReadFile(filepath.Join(dir, ".git", "index"))
	f, d, changes := reviewOf(t, dir, base)
	ids := []string{fmt.Sprintf("%d:1", fileIndex(t, d, "x.txt")), fmt.Sprint(fileIndex(t, d, "gone.txt")),
		fmt.Sprint(fileIndex(t, d, "new.go")), fmt.Sprintf("%d:0", fileIndex(t, d, "fresh.txt"))}
	sel, err := ParseSelection(d, ids)
	if err != nil {
		t.Fatal(err)
	}
	if err := testGit().Reject(f, d, changes, sel); err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "x.txt") != strings.Replace(text, "line 2\n", "line two\n", 1) {
		t.Errorf("x.txt:\n%s", read(t, dir, "x.txt"))
	}
	if read(t, dir, "gone.txt") != "bye\n" || read(t, dir, "old.go") != longFile || read(t, dir, "new.go") != "<missing>" || read(t, dir, "fresh.txt") != "<missing>" {
		t.Error("whole files not back")
	}
	if st, _ := os.Stat(filepath.Join(dir, "bin/run.sh")); st == nil || st.Mode().Perm() != 0o755 {
		t.Error("an untouched file changed")
	}
	if after, _ := os.ReadFile(filepath.Join(dir, ".git", "index")); string(after) != string(index) {
		t.Error("the index changed")
	}
	_, left, _ := reviewOf(t, dir, base)
	if strings.Join(paths(left), " ") != "x.txt bin/run.sh" && strings.Join(paths(left), " ") != "bin/run.sh x.txt" {
		t.Fatalf("left %v", paths(left))
	}
}

func TestMergeNoNewlineAtEnd(t *testing.T) {
	old, nw := []byte("a\nb"), []byte("a\nb\nc\n")
	hunks := []wire.ReviewHunk{{OldStart: 2, OldLines: 1, NewStart: 2, NewLines: 2}}
	if got, _ := merge(old, nw, hunks, map[int]bool{0: true}); string(got) != "a\nb\nc\n" {
		t.Errorf("selected %q", got)
	}
	if got, _ := merge(old, nw, hunks, nil); string(got) != "a\nb" {
		t.Errorf("not selected %q", got)
	}
	// An insertion after line 1 (oldLines 0).
	ins := []wire.ReviewHunk{{OldStart: 1, OldLines: 0, NewStart: 2, NewLines: 1}}
	if got, _ := merge([]byte("a\nc\n"), []byte("a\nb\nc\n"), ins, map[int]bool{0: true}); string(got) != "a\nb\nc\n" {
		t.Errorf("insertion %q", got)
	}
}
