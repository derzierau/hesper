package transport_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Review across Macs (docs/rebuild-contract.md "As built — review").

// reviewRepo makes n's project a repository with a.txt committed.
func reviewRepo(t *testing.T, n *node) {
	t.Helper()
	git(t, n.project, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(n.project, "a.txt"), []byte("one\n"), 0o644)
	git(t, n.project, "add", ".")
	git(t, n.project, "commit", "-q", "--no-gpg-sign", "-m", "first")
}

// review.list on L merges M's agents (named as L names them); review.diff
// of M's agent through L is what M computes.
func TestRemoteReviewListAndDiff(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	reviewRepo(t, L)
	reviewRepo(t, M)
	L.waitLinked(t, "M")
	var remote, local wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Project: M.project, Task: "do it"}, &remote); err != nil {
		t.Fatal(err)
	}
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Project: L.project, Task: "do it"}, &local); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, remote.ID, inState(wire.StateDone))
	L.waitAgent(t, local.ID, inState(wire.StateDone))
	os.WriteFile(filepath.Join(M.project, "a.txt"), []byte("one\ntwo on M\n"), 0o644)
	os.WriteFile(filepath.Join(L.project, "l.txt"), []byte("on L\n"), 0o644)

	var list []wire.ReviewItem
	if err := L.call(t, "review.list", nil, &list); err != nil {
		t.Fatal(err)
	}
	byID := map[string]wire.ReviewItem{}
	for _, item := range list {
		byID[item.ID] = item
	}
	if r, ok := byID[remote.ID]; !ok || r.Machine != "M" || r.Files != 1 || r.Added != 1 || r.Project != M.project {
		t.Fatalf("M's item in %+v", list)
	}
	if l, ok := byID[local.ID]; !ok || l.Machine != "L" || l.Files != 1 || len(list) != 2 {
		t.Fatalf("L's item in %+v", list)
	}

	var through, there wire.ReviewDiff
	if err := L.call(t, "review.diff", wire.ReviewDiffParams{ID: remote.ID}, &through); err != nil {
		t.Fatal(err)
	}
	_, id, _ := strings.Cut(remote.ID, "/")
	if err := M.call(t, "review.diff", wire.ReviewDiffParams{ID: "M/" + id}, &there); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(through)
	b, _ := json.Marshal(there)
	if string(a) != string(b) || len(through.Files) != 1 || through.Files[0].Path != "a.txt" {
		t.Fatalf("through L %s\non M %s", a, b)
	}
	if err := L.call(t, "review.diff", wire.ReviewDiffParams{ID: "M/nope00"}, nil); err == nil || !strings.Contains(err.Error(), "nope00") {
		t.Fatalf("unknown remote agent: %v", err)
	}
}

// A diff larger than one relay message comes through L in parts.
func TestRemoteReviewLargeDiff(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	reviewRepo(t, M)
	L.waitLinked(t, "M")
	var a wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Project: M.project, Task: "do it"}, &a); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, a.ID, inState(wire.StateDone))
	seed := sha256.Sum256([]byte("review"))
	for f := range 6 {
		var b strings.Builder
		for i := range 15000 {
			seed = sha256.Sum256(seed[:])
			fmt.Fprintf(&b, "line %d %x\n", i, seed[:16])
		}
		os.WriteFile(filepath.Join(M.project, fmt.Sprintf("big%d.txt", f)), []byte(b.String()), 0o644)
	}
	var through, there wire.ReviewDiff
	if err := L.call(t, "review.diff", wire.ReviewDiffParams{ID: a.ID}, &through); err != nil {
		t.Fatal(err)
	}
	_, id, _ := strings.Cut(a.ID, "/")
	if err := M.call(t, "review.diff", wire.ReviewDiffParams{ID: "M/" + id}, &there); err != nil {
		t.Fatal(err)
	}
	x, _ := json.Marshal(through)
	y, _ := json.Marshal(there)
	if len(x) < 4<<20 || string(x) != string(y) || len(through.Files) != 6 {
		t.Fatalf("through L %d bytes, on M %d bytes, %d files", len(x), len(y), len(through.Files))
	}
}
