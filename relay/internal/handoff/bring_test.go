package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A folder's tar leaves build and dependency folders out, keeps
// symlinks inside the folder (absolute ones made relative) and drops
// the ones pointing outside; unpacked, nothing lands outside dest.
func TestTarFolderExcludesAndSymlinks(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	src := filepath.Join(root, "notes")
	outside := filepath.Join(root, "secret.txt")
	write(t, outside, "SECRET\n")
	write(t, filepath.Join(src, "a.txt"), "a\n")
	write(t, filepath.Join(src, "sub", "b.txt"), "b\n")
	for _, d := range []string{"node_modules", ".build", "DerivedData", "target", "dist", ".venv", "__pycache__"} {
		write(t, filepath.Join(src, d, "junk"), "junk\n")
		write(t, filepath.Join(src, "sub", d, "junk"), "junk\n")
	}
	os.Symlink("sub/b.txt", filepath.Join(src, "rel-link"))
	os.Symlink(filepath.Join(src, "a.txt"), filepath.Join(src, "abs-link"))
	os.Symlink(outside, filepath.Join(src, "out-link"))
	os.Symlink("../../secret.txt", filepath.Join(src, "sub", "up-link"))
	os.Chmod(filepath.Join(src, "a.txt"), 0o755)

	tarFile := filepath.Join(root, "folder.tar")
	if err := TarFolder(src, tarFile, 1<<20); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "dest")
	os.Mkdir(dest, 0o755)
	if err := UntarFolder(tarFile, dest, 1<<20); err != nil {
		t.Fatal(err)
	}
	var got []string
	filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
		if p != dest {
			rel, _ := filepath.Rel(dest, p)
			got = append(got, rel)
		}
		return nil
	})
	if strings.Join(got, ",") != "a.txt,abs-link,rel-link,sub,sub/b.txt" {
		t.Fatalf("unpacked %v", got)
	}
	if link, _ := os.Readlink(filepath.Join(dest, "abs-link")); link != "a.txt" {
		t.Fatalf("abs-link -> %q", link)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "rel-link")); string(data) != "b\n" {
		t.Fatalf("rel-link reads %q", data)
	}
	if st, _ := os.Stat(filepath.Join(dest, "a.txt")); st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("a.txt lost its mode: %v", st.Mode())
	}
}

// Over the cap a tar is "too-large".
func TestTarFolderCap(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "src", "big"), strings.Repeat("x", 2048))
	write(t, filepath.Join(root, "src", "node_modules", "bigger"), strings.Repeat("x", 1<<20)) // left out: not counted
	if err := TarFolder(filepath.Join(root, "src"), filepath.Join(root, "ok.tar"), 4096); err != nil {
		t.Fatalf("under the cap: %v", err)
	}
	err := TarFolder(filepath.Join(root, "src"), filepath.Join(root, "t.tar"), 1024)
	if CodeOf(err) != "too-large" {
		t.Fatalf("over the cap: %v", err)
	}
}

// A tar naming paths outside dest, or writing through a symlink, is
// refused.
func TestUntarFolderRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	write(t, filepath.Join(src, "x"), "x\n")
	tarFile := filepath.Join(root, "f.tar")
	if err := TarFolder(src, tarFile, 1<<20); err != nil {
		t.Fatal(err)
	}
	// dest has a symlink where the tar puts a folder's file.
	dest := filepath.Join(root, "dest")
	os.MkdirAll(dest, 0o755)
	if err := realParents(dest, "link/file"); err != nil {
		t.Fatal(err)
	}
	os.Symlink(root, filepath.Join(dest, "evil"))
	if err := realParents(dest, "evil/file"); CodeOf(err) != "bundle" {
		t.Fatalf("through a symlink: %v", err)
	}
}

// Where a brought folder goes.
func TestBringDest(t *testing.T) {
	src, here := "/Users/a", "/Users/b"
	cases := []struct {
		plan BringPlan
		want string
	}{
		{BringPlan{Path: "/Users/a/projects/app", Name: "app", Git: true, Remote: "git@x:app.git"}, "/Users/b/projects/app"},
		{BringPlan{Path: "/Users/a/projects/team/app", Name: "app", Git: true, Remote: "git@x:app.git"}, "/Users/b/projects/team/app"},
		{BringPlan{Path: "/Users/a/work/app", Name: "app", Git: true, Remote: "git@x:app.git"}, "/Users/b/code/app"},
		{BringPlan{Path: "/Users/a/work/app", Name: "app", Git: true}, "/Users/b/work/app"},
		{BringPlan{Path: "/Users/a/Documents/notes", Name: "notes"}, "/Users/b/Documents/notes"},
		{BringPlan{Path: "/Users/a/scratch/2026-10-08-x", Name: "2026-10-08-x", Git: true, Scratch: true}, "/Users/b/scr/2026-10-08-x"},
	}
	for _, c := range cases {
		c.plan.Home = src
		if got := BringDest(c.plan, here, "/Users/b/code", "/Users/b/scr"); got != c.want {
			t.Errorf("%s: %s, want %s", c.plan.Path, got, c.want)
		}
	}
}

// A Git folder without a remote travels as a full bundle: the target
// gets the repository at dest on the same branch, with (or without)
// its uncommitted and untracked files; the source stays as it was.
func TestBringGitFolderWithoutRemote(t *testing.T) {
	dir, p := checkpointRepo(t)
	before := userState(t, dir)
	plan, err := PlanFolder(t.Context(), dir, p)
	if err != nil || !plan.Git || plan.Remote != "" {
		t.Fatalf("plan %+v %v", plan, err)
	}
	for _, changes := range []bool{true, false} {
		bundle := filepath.Join(t.TempDir(), "bundle")
		m, err := PackFolder(t.Context(), plan, "L", nil, bundle, p, changes, "")
		if err != nil {
			t.Fatal(err)
		}
		if m.Project.Bundle != "full" || m.Bring.Kind != BringGit {
			t.Fatalf("manifest %+v", m)
		}
		loaded, err := LoadManifest(bundle)
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(t.TempDir(), "copy")
		if err := UnpackFolder(t.Context(), loaded, bundle, dest, p); err != nil {
			t.Fatal(err)
		}
		if got, want := run(t, dest, "git", "rev-parse", "HEAD"), run(t, dir, "git", "rev-parse", "HEAD"); got != want {
			t.Fatalf("HEAD %s, want %s", got, want)
		}
		status := run(t, dest, "git", "status", "--porcelain", "--untracked-files=all")
		if changes && status != strings.TrimSpace(run(t, dir, "git", "status", "--porcelain", "--untracked-files=all")) {
			t.Fatalf("changes: status %q", status)
		}
		if !changes && status != "" {
			t.Fatalf("clean: status %q", status)
		}
		if remotes := run(t, dest, "git", "remote"); remotes != "" {
			t.Fatalf("remotes %q", remotes)
		}
	}
	if after := userState(t, dir); after != before {
		t.Fatalf("source changed:\n%s\n---\n%s", before, after)
	}
}
