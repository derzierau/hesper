package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// reviewEnv keeps the daemon's Git away from the user's configuration.
var reviewEnv = []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}

// reviewHarness: a daemon whose project is a repository with a.txt
// committed on main.
func reviewHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, reviewEnv...)
	gitIn(t, h.project, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(h.project, "a.txt"), []byte("one\n"), 0o644)
	gitIn(t, h.project, "add", ".")
	gitIn(t, h.project, "commit", "-q", "--no-gpg-sign", "-m", "first")
	return h
}

// waitSettled waits until id is done and its turn's checkpoint was taken
// (that runs Git in the folder: a test must not end before it).
func (h *harness) waitSettled(id string) wire.Agent {
	h.t.Helper()
	h.waitState(id, wire.StateDone)
	var a wire.Agent
	waitFor(h.t, func() bool { a, _ = h.reg.Get(id); return a.Checkpoint != nil })
	return a
}

func (h *harness) reviewList() []wire.ReviewItem {
	h.t.Helper()
	var list []wire.ReviewItem
	if err := h.call("review.list", nil, &list); err != nil {
		h.t.Fatal(err)
	}
	return list
}

func findItem(list []wire.ReviewItem, id string) *wire.ReviewItem {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// review.list: a settled agent with changes against the commit it started
// at; a working one, one without changes and a shell are not listed.
func TestReviewListReadiness(t *testing.T) {
	h := reviewHarness(t)
	head := gitIn(t, h.project, "rev-parse", "HEAD")
	a := h.spawn(wire.SpawnParams{Task: "do it"})
	if a.ReviewBase != head {
		t.Fatalf("review base %q, want %s", a.ReviewBase, head)
	}
	h.waitSettled(a.ID)
	if list := h.reviewList(); len(list) != 0 {
		t.Fatalf("no changes yet: %+v", list)
	}
	// Changes: a commit after the start, an edit, an untracked file.
	os.WriteFile(filepath.Join(h.project, "b.txt"), []byte("b\n"), 0o644)
	gitIn(t, h.project, "add", "b.txt")
	gitIn(t, h.project, "commit", "-q", "--no-gpg-sign", "-m", "agent's commit")
	os.WriteFile(filepath.Join(h.project, "a.txt"), []byte("one\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(h.project, "c.txt"), []byte("c\nc\n"), 0o644)
	busy := h.spawn(wire.SpawnParams{Task: "keep working", Worktree: json.RawMessage("true")})
	h.waitState(busy.ID, wire.StateWorking)
	os.WriteFile(filepath.Join(busy.Worktree, "w.txt"), []byte("w\n"), 0o644)
	shell := h.spawn(wire.SpawnParams{Kind: wire.KindShell})
	list := h.reviewList()
	if len(list) != 1 {
		t.Fatalf("list %+v", list)
	}
	item := list[0]
	if item.ID != a.ID || item.Machine != "L" || item.Name != a.Name || item.Kind != wire.KindClaude || item.State != wire.StateDone ||
		item.Files != 3 || item.Added != 4 || item.Removed != 0 || item.Base != head || item.Risk != wire.RiskLow ||
		item.RiskNotes == nil || item.ReadyAt.IsZero() || item.Project != h.project {
		t.Fatalf("item %+v", item)
	}
	if findItem(list, busy.ID) != nil || findItem(list, shell.ID) != nil {
		t.Fatalf("working agent or shell listed: %+v", list)
	}
	// review.diff of a shell is refused; of an unknown agent not found.
	if err := h.call("review.diff", wire.ReviewDiffParams{ID: shell.ID}, nil); err == nil {
		t.Fatal("a shell's diff")
	}
	err := h.call("review.diff", wire.ReviewDiffParams{ID: "L/nope00"}, nil)
	if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeNotFound {
		t.Fatalf("unknown agent: %v", err)
	}
	// The working agent's folder settles: listed.
	h.call("agents.input", wire.InputParams{ID: busy.ID, Text: "x"}, nil)
	h.waitSettled(busy.ID)
	if item := findItem(h.reviewList(), busy.ID); item == nil || item.Files != 1 || item.Worktree != busy.Worktree || item.Branch == "" {
		t.Fatalf("settled worktree agent: %+v", item)
	}
}

// review.diff: the folder against the review base, through a copy of the
// index (the untracked file stays untracked).
func TestReviewDiff(t *testing.T) {
	h := reviewHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "do it"})
	h.waitSettled(a.ID)
	os.WriteFile(filepath.Join(h.project, "a.txt"), []byte("one\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(h.project, "new.txt"), []byte("new\n"), 0o644)
	status := gitIn(t, h.project, "status", "--porcelain")
	var d wire.ReviewDiff
	zero := 0
	if err := h.call("review.diff", wire.ReviewDiffParams{ID: a.ID, Context: &zero}, &d); err != nil {
		t.Fatal(err)
	}
	if d.Base != a.ReviewBase || d.Head != "worktree" || len(d.Files) != 2 {
		t.Fatalf("diff %+v", d)
	}
	for _, f := range d.Files {
		switch f.Path {
		case "a.txt":
			if f.Status != "M" || len(f.Hunks) != 1 || len(f.Hunks[0].Lines) != 1 || f.Hunks[0].Lines[0].Text != "two" || f.Hunks[0].Lines[0].New != 2 {
				t.Errorf("a.txt %+v", f)
			}
		case "new.txt":
			if f.Status != "A" || f.Added != 1 {
				t.Errorf("new.txt %+v", f)
			}
		default:
			t.Errorf("unexpected %s", f.Path)
		}
	}
	if got := gitIn(t, h.project, "status", "--porcelain"); got != status {
		t.Fatalf("status changed: %q, was %q", got, status)
	}
}

// review.changed: an agent that settles with changes in its folder.
func TestReviewChangedNote(t *testing.T) {
	h := reviewHarness(t)
	c := h.client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Call(ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(h.project, "a.txt"), []byte("changed\n"), 0o644)
	a := h.spawn(wire.SpawnParams{Task: "do it"})
	for {
		select {
		case n := <-c.Notifications():
			if n.Method != wire.NoteReviewChanged {
				continue
			}
			var rc wire.ReviewChanged
			json.Unmarshal(n.Params, &rc)
			if rc.ID != a.ID {
				t.Fatalf("review.changed %+v", rc)
			}
			h.waitSettled(a.ID)
			return
		case <-ctx.Done():
			t.Fatal("no review.changed")
		}
	}
}

func (h *harness) reviewDiff(id string) wire.ReviewDiff {
	h.t.Helper()
	var d wire.ReviewDiff
	if err := h.call("review.diff", wire.ReviewDiffParams{ID: id}, &d); err != nil {
		h.t.Fatal(err)
	}
	return d
}

func fileID(t *testing.T, d wire.ReviewDiff, path string) string {
	t.Helper()
	for i, f := range d.Files {
		if f.Path == path {
			return strconv.Itoa(i)
		}
	}
	t.Fatalf("no %s in the diff", path)
	return ""
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if we, ok := err.(*wire.Error); !ok || we.Code != code {
		t.Fatalf("error %v, want code %s", err, code)
	}
}

// review.accept and review.reject: refused while the agent works and for
// a folder that changed since the diff; reject reverts in the working
// tree, accept commits on HEAD and moves the review base.
func TestReviewAcceptReject(t *testing.T) {
	h := reviewHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "do it"})
	h.waitSettled(a.ID)
	os.WriteFile(filepath.Join(h.project, "a.txt"), []byte("one\nrejected\n"), 0o644)
	os.WriteFile(filepath.Join(h.project, "b.txt"), []byte("accepted\n"), 0o644)
	d := h.reviewDiff(a.ID)

	err := h.call("review.accept", wire.ReviewAcceptParams{ID: a.ID, Tree: "0123456789abcdef0123456789abcdef01234567"}, nil)
	wantCode(t, err, wire.CodeInvalid)
	wantCode(t, h.call("review.reject", wire.ReviewRejectParams{ID: a.ID}, nil), wire.CodeInvalid)
	wantCode(t, h.call("review.accept", wire.ReviewAcceptParams{ID: a.ID, Hunks: []string{"7:0"}}, nil), wire.CodeInvalid)

	if err := h.call("review.reject", wire.ReviewRejectParams{ID: a.ID, Hunks: []string{fileID(t, d, "a.txt") + ":0"}, Tree: d.Tree}, nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(h.project, "a.txt")); string(data) != "one\n" {
		t.Fatalf("a.txt after reject: %q", data)
	}
	d = h.reviewDiff(a.ID)
	var res wire.ReviewAcceptResult
	if err := h.call("review.accept", wire.ReviewAcceptParams{ID: a.ID, Hunks: []string{fileID(t, d, "b.txt")}, Message: "Add b", Tree: d.Tree}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Commit == "" || gitIn(t, h.project, "rev-parse", "HEAD") != res.Commit || gitIn(t, h.project, "log", "-1", "--format=%s") != "Add b" {
		t.Fatalf("accept %+v", res)
	}
	if st := gitIn(t, h.project, "status", "--porcelain"); st != "" {
		t.Fatalf("status %q", st)
	}
	if got, _ := h.reg.Get(a.ID); got.ReviewBase != res.Commit {
		t.Fatalf("review base %s, want %s", got.ReviewBase, res.Commit)
	}
	if list := h.reviewList(); len(list) != 0 {
		t.Fatalf("accepted work still listed: %+v", list)
	}

	// The default message is the agent's summary.
	os.WriteFile(filepath.Join(h.project, "c.txt"), []byte("c\n"), 0o644)
	if err := h.call("review.accept", wire.ReviewAcceptParams{ID: a.ID}, &res); err != nil {
		t.Fatal(err)
	}
	if msg := gitIn(t, h.project, "log", "-1", "--format=%s"); msg != "Done: do it" {
		t.Fatalf("default message %q", msg)
	}

	// A working agent's folder is left alone.
	busy := h.spawn(wire.SpawnParams{Task: "keep working", Worktree: json.RawMessage("true")})
	h.waitState(busy.ID, wire.StateWorking)
	os.WriteFile(filepath.Join(busy.Worktree, "w.txt"), []byte("w\n"), 0o644)
	wantCode(t, h.call("review.accept", wire.ReviewAcceptParams{ID: busy.ID}, nil), wire.CodeBusy)
	wantCode(t, h.call("review.reject", wire.ReviewRejectParams{ID: busy.ID, Hunks: []string{"0"}}, nil), wire.CodeBusy)
	wantCode(t, h.call("review.sendBack", wire.ReviewSendBackParams{ID: busy.ID, Message: "x"}, nil), wire.CodeBusy)
	// An agent may not accept another agent's work it did not start.
	c := h.client()
	c.Caller = busy.ID
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wantCode(t, c.Call(ctx, "review.accept", wire.ReviewAcceptParams{ID: a.ID}, nil), wire.CodeForbidden)
	h.call("agents.input", wire.InputParams{ID: busy.ID, Text: "x"}, nil)
	h.waitSettled(busy.ID)
}

// review.sendBack: one instruction typed into the agent, the reviewed
// point kept (a ref, the review log; across a daemon restart).
func TestReviewSendBack(t *testing.T) {
	h := reviewHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "do it"})
	h.waitSettled(a.ID)
	os.WriteFile(filepath.Join(h.project, "a.txt"), []byte("one\ntwo\n"), 0o644)
	wantCode(t, h.call("review.sendBack", wire.ReviewSendBackParams{ID: a.ID}, nil), wire.CodeInvalid)
	notes := []wire.ReviewNote{{Path: "a.txt", Line: 2, Side: "new", Text: "use a constant"}, {Path: "a.txt", Line: 1, Side: "old", Text: "keep this\nline"}}
	if err := h.call("review.sendBack", wire.ReviewSendBackParams{ID: a.ID, Notes: notes, Message: "Then run the tests."}, nil); err != nil {
		t.Fatal(err)
	}
	want := "Done: Review notes on your changes; please address them:\n- a.txt:2: use a constant\n- a.txt (old line 1): keep this line\n\nThen run the tests."
	waitFor(t, func() bool {
		var res wire.AgentResult
		h.call("agents.result", wire.IDParams{ID: a.ID}, &res)
		return res.Message == want
	})
	_, local, _ := strings.Cut(a.ID, "/")
	ref := "refs/hesper/checkpoints/" + local + "-reviewed"
	if tree := gitIn(t, h.project, "rev-parse", ref+"^{tree}"); tree == "" {
		t.Fatal("no reviewed point")
	}
	h.waitSettled(a.ID)
	item := findItem(h.reviewList(), a.ID)
	if item == nil || item.ReviewedAt.IsZero() {
		t.Fatalf("reviewedAt %+v", item)
	}
	h.close()
	h.open()
	h.waitState(a.ID, wire.StateIdle) // resumed
	if again := findItem(h.reviewList(), a.ID); again == nil || !again.ReviewedAt.Equal(item.ReviewedAt) {
		t.Fatalf("after a restart %+v", again)
	}
}
