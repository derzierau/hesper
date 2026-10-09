package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

var testEnv = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = testEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo is a repository with files committed; it returns its folder
// and the commit.
func newRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, dir, "init", "-q", "-b", "main")
	for name, content := range files {
		write(t, dir, name, content)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "--no-gpg-sign", "--allow-empty", "-m", "base")
	return dir, gitIn(t, dir, "rev-parse", "HEAD")
}

func testGit() Git { return Git{Ctx: context.Background(), Env: testEnv} }

func diffOf(t *testing.T, dir, base string) wire.ReviewDiff {
	t.Helper()
	g := testGit()
	f := g.Open(dir)
	if f == nil {
		t.Fatal("not a repository")
	}
	tree, err := g.Snapshot(f)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := g.Diff(f, base, tree, wire.DefaultReviewContext)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func fileOf(t *testing.T, d wire.ReviewDiff, path string) wire.ReviewFile {
	t.Helper()
	for _, f := range d.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no %s in %+v", path, paths(d))
	return wire.ReviewFile{}
}

func paths(d wire.ReviewDiff) []string {
	var out []string
	for _, f := range d.Files {
		out = append(out, f.Path)
	}
	return out
}

const longFile = "package x\n\nfunc one() int {\n\treturn 1\n}\n\nfunc two() int {\n\treturn 2\n}\n\nfunc three() int {\n\treturn 3\n}\n"

func TestDiffShapeAndIndexUntouched(t *testing.T) {
	dir, base := newRepo(t, map[string]string{
		"calc.go":   "package calc\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n",
		"gone.txt":  "bye\n",
		"old.go":    longFile,
		"staged.go": "package s\n",
		"img.png":   "\x89PNG\x00\x01\x02",
	})
	write(t, dir, "calc.go", "package calc\n\nfunc Add(a, b int) int {\n\treturn a - b\n}\n")
	os.Remove(filepath.Join(dir, "gone.txt"))
	gitIn(t, dir, "mv", "old.go", "new.go")
	write(t, dir, "new.go", strings.Replace(longFile, "return 3", "return 33", 1))
	write(t, dir, "fresh.go", "package fresh\n\nvar X = 1\n")
	write(t, dir, "staged.go", "package s\n\nvar Y = 2\n")
	gitIn(t, dir, "add", "staged.go")
	write(t, dir, "img.png", "\x89PNG\x00\x03\x04")
	indexPath := filepath.Join(dir, ".git", "index")
	status := gitIn(t, dir, "status", "--porcelain")
	before, _ := os.ReadFile(indexPath)

	d := diffOf(t, dir, base)
	if d.Base != base || d.Head != "worktree" || d.Tree == "" {
		t.Fatalf("diff %+v", d)
	}
	want := map[string]string{"calc.go": "M", "gone.txt": "D", "new.go": "R", "fresh.go": "A", "staged.go": "M", "img.png": "M"}
	if len(d.Files) != len(want) {
		t.Fatalf("files %v", paths(d))
	}
	for i, f := range d.Files {
		if want[f.Path] != f.Status || f.Order != i {
			t.Errorf("%s: status %s order %d", f.Path, f.Status, f.Order)
		}
		for j, h := range f.Hunks {
			if h.ID != strconv.Itoa(i)+":"+strconv.Itoa(j) {
				t.Errorf("%s hunk id %s", f.Path, h.ID)
			}
		}
	}
	if r := fileOf(t, d, "new.go"); r.OldPath != "old.go" || len(r.Hunks) != 1 {
		t.Errorf("rename %+v", r)
	}
	if b := fileOf(t, d, "img.png"); !b.Binary || len(b.Hunks) != 0 {
		t.Errorf("binary %+v", b)
	}
	fresh := fileOf(t, d, "fresh.go")
	if fresh.Added != 3 || len(fresh.Hunks) != 1 || fresh.Hunks[0].OldLines != 0 || fresh.Hunks[0].NewStart != 1 || fresh.Hunks[0].Lines[2].New != 3 {
		t.Errorf("untracked %+v", fresh)
	}
	calc := fileOf(t, d, "calc.go")
	h := calc.Hunks[0]
	var removed, added wire.ReviewLine
	for _, l := range h.Lines {
		switch l.Kind {
		case "-":
			removed = l
		case "+":
			added = l
		}
	}
	if removed.Old != 4 || added.New != 4 || removed.Text != "\treturn a + b" {
		t.Errorf("lines %+v %+v", removed, added)
	}
	// Word ranges: only the operator changed.
	if len(removed.Words) != 1 || removed.Words[0] != [2]int{10, 11} || len(added.Words) != 1 || added.Words[0] != [2]int{10, 11} {
		t.Errorf("words %v %v", removed.Words, added.Words)
	}
	// The user's index and status are as they were: fresh.go is still
	// untracked.
	after, _ := os.ReadFile(indexPath)
	if string(before) != string(after) || gitIn(t, dir, "status", "--porcelain") != status || !strings.Contains(status, "?? fresh.go") {
		t.Errorf("index changed; status %q", gitIn(t, dir, "status", "--porcelain"))
	}
}

func TestFormattingMovedGenerated(t *testing.T) {
	block := "\tfirst := 1\n\tsecond := 2\n\tthird := 3\n\tfourth := 4\n"
	dir, base := newRepo(t, map[string]string{
		"a.go":   "package a\n\nfunc A() {\n" + block + "}\n",
		"b.go":   "package b\n\nfunc B() {\n}\n",
		"fmt.go": "package f\n\nfunc F() {\n  x := 1\n  y := 2\n}\n",
	})
	write(t, dir, "a.go", "package a\n\nfunc A() {\n}\n")
	write(t, dir, "b.go", "package b\n\nfunc B() {\n"+strings.ReplaceAll(block, "\t", "\t\t")+"}\n")
	write(t, dir, "fmt.go", "package f\n\nfunc F() {\n\tx := 1\n\ty := 2\n}\n")
	write(t, dir, "api.pb.go", "// Code generated by protoc-gen-go. DO NOT EDIT.\npackage api\n")
	write(t, dir, "package-lock.json", "{}\n")
	d := diffOf(t, dir, base)
	f := fileOf(t, d, "fmt.go")
	if !f.FormattingOnly || !f.Hunks[0].FormattingOnly || f.Risk != wire.RiskLow {
		t.Errorf("formatting %+v", f)
	}
	a, b := fileOf(t, d, "a.go"), fileOf(t, d, "b.go")
	if !a.Hunks[0].Moved || !b.Hunks[0].Moved || a.FormattingOnly {
		t.Errorf("moved: %+v %+v", a.Hunks[0], b.Hunks[0])
	}
	for _, p := range []string{"api.pb.go", "package-lock.json"} {
		if g := fileOf(t, d, p); !g.Generated {
			t.Errorf("%s not generated", p)
		}
	}
	// Formatting-only, then generated, last.
	n := len(d.Files)
	if d.Files[n-3].Path != "fmt.go" || !d.Files[n-1].Generated || !d.Files[n-2].Generated {
		t.Errorf("order %v", paths(d))
	}
	// A block rewritten in place is no move.
	dir2, base2 := newRepo(t, map[string]string{"c.go": "package c\n\nfunc C() {\n" + block + "}\n"})
	write(t, dir2, "c.go", "package c\n\nfunc C() {\n"+strings.ReplaceAll(block, " := ", " = ")+"}\n")
	if c := fileOf(t, diffOf(t, dir2, base2), "c.go"); c.Hunks[0].Moved {
		t.Error("rewrite read as a move")
	}
}

func TestReadingOrder(t *testing.T) {
	files := map[string]string{
		"internal/auth/login.go": "package auth\n", "go.mod": "module x\n", "README.md": "# x\n",
		"store/store.go": "package store\n", "store/store_test.go": "package store\n",
		"util/util.go": "package util\n", "other_test.go": "package x\n", "big/big.go": "package big\n",
	}
	dir, base := newRepo(t, files)
	for name, content := range files {
		write(t, dir, name, content+"// changed\n")
	}
	write(t, dir, "big/big.go", "package big\n"+strings.Repeat("// more\n", 20))
	got := paths(diffOf(t, dir, base))
	want := []string{"internal/auth/login.go", "go.mod", "big/big.go", "store/store.go", "store/store_test.go", "util/util.go", "other_test.go", "README.md"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("order\n got %v\nwant %v", got, want)
	}
}

func TestWordRanges(t *testing.T) {
	old, nw := wordRanges("let 👋 = name", "let 👋 = title")
	// 👋 is four UTF-8 bytes: "name" starts at byte 11.
	if len(old) != 1 || old[0] != [2]int{11, 15} || len(nw) != 1 || nw[0] != [2]int{11, 16} {
		t.Errorf("%v %v", old, nw)
	}
	if o, n := wordRanges("completely different", "nothing alike here at all"); o != nil || n != nil {
		t.Errorf("unrelated lines got words %v %v", o, n)
	}
}

func TestChangeRisk(t *testing.T) {
	level, notes := ChangeRisk([]Change{{Path: "db/migrations/001.sql", Status: "A", Added: 3}})
	if level != wire.RiskHigh || len(notes) != 1 || !strings.Contains(notes[0], "migration") {
		t.Errorf("%s %v", level, notes)
	}
	level, notes = ChangeRisk([]Change{{Path: "main.go", Status: "M", Added: 150}})
	if level != wire.RiskMedium || !strings.Contains(strings.Join(notes, ";"), "no tests") {
		t.Errorf("%s %v", level, notes)
	}
	level, _ = ChangeRisk([]Change{{Path: "main.go", Status: "M", Added: 150}, {Path: "main_test.go", Status: "M", Added: 20}})
	if level != wire.RiskLow {
		t.Errorf("with tests: %s", level)
	}
	if level, _ := ChangeRisk([]Change{{Path: "docs/author.md", Status: "M", Added: 2}}); level != wire.RiskLow {
		t.Errorf("author is not auth: %s", level)
	}
}
