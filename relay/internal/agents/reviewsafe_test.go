package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// folderState is what the user sees of a folder: the index file's bytes
// and mtime, git status (read without refreshing the index) and every
// file's bytes outside .git.
func folderState(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	index := filepath.Join(dir, ".git", "index")
	data, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(index)
	sum := sha256.Sum256(data)
	b.WriteString("index " + hex.EncodeToString(sum[:]) + " " + st.ModTime().String() + "\n")
	b.WriteString("status\n" + gitIn(t, dir, "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=all") + "\n")
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			data, _ := os.ReadFile(p)
			sum := sha256.Sum256(data)
			b.WriteString(p + " " + hex.EncodeToString(sum[:]) + "\n")
		}
		return nil
	})
	return b.String()
}

// review.list, review.diff, review.evidence and review.provenance only
// read: the user's index (staged, unstaged and untracked changes alike)
// and working tree are exactly as they were. Git runs through a copy of
// the index (GIT_INDEX_FILE), never the user's.
func TestReviewLeavesIndexAndWorktree(t *testing.T) {
	h := reviewHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "do it"})
	h.waitSettled(a.ID)
	// Staged, unstaged, staged-then-edited, untracked (also in a folder).
	os.WriteFile(filepath.Join(h.project, "staged.txt"), []byte("staged\n"), 0o644)
	gitIn(t, h.project, "add", "staged.txt")
	os.WriteFile(filepath.Join(h.project, "a.txt"), []byte("one\nunstaged\n"), 0o644)
	os.WriteFile(filepath.Join(h.project, "both.txt"), []byte("first\n"), 0o644)
	gitIn(t, h.project, "add", "both.txt")
	os.WriteFile(filepath.Join(h.project, "both.txt"), []byte("first\nthen edited\n"), 0o644)
	os.MkdirAll(filepath.Join(h.project, "new"), 0o755)
	os.WriteFile(filepath.Join(h.project, "new", "untracked.txt"), []byte("u\n"), 0o644)
	before := folderState(t, h.project)

	for i := 0; i < 2; i++ { // the second list from the cache
		if item := findItem(h.reviewList(), a.ID); item == nil || item.Files != 4 {
			t.Fatalf("item %+v", item)
		}
	}
	if d := h.reviewDiff(a.ID); len(d.Files) != 4 {
		t.Fatalf("diff %+v", d)
	}
	h.evidence(a.ID)
	var prov wire.ReviewProvenance
	if err := h.call("review.provenance", wire.ReviewProvenanceParams{ID: a.ID, Path: "a.txt", Line: 2}, &prov); err != nil {
		t.Fatal(err)
	}
	if after := folderState(t, h.project); after != before {
		t.Fatalf("review changed the folder:\n%s\nwas\n%s", after, before)
	}
}
