package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestActivityFollowsTools(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "please approve this"})
	got := h.waitState(a.ID, wire.StateApproval)
	if got.Activity != "Bash: git push origin main" {
		t.Fatalf("activity %q", got.Activity)
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatal(err)
	}
	done := h.waitState(a.ID, wire.StateDone)
	if done.Activity != "" {
		t.Fatalf("activity after the turn %q", done.Activity)
	}
}

func TestProjectsClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	h := newHarness(t)
	src := filepath.Join(h.dir, "origin", "tool.git")
	for _, args := range [][]string{{"init", "-q", "--bare", src}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	var res CloneResult
	if err := h.call("projects.clone", CloneParams{URL: src}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Path != filepath.Join(h.opt.ProjectsRoot, "tool") {
		t.Fatalf("path %s", res.Path)
	}
	if _, err := os.Stat(filepath.Join(res.Path, ".git")); err != nil {
		t.Fatal(err)
	}
	// Again: the same clone.
	if err := h.call("projects.clone", CloneParams{URL: src}, &res); err != nil {
		t.Fatal(err)
	}
	var recent []wire.Project
	h.call("projects.recent", nil, &recent)
	if len(recent) == 0 || recent[0].Path != res.Path {
		t.Fatalf("recent %+v", recent)
	}
	err := h.call("projects.clone", CloneParams{URL: "--upload-pack=evil"}, nil)
	if we := asWire(err); we == nil || we.Code != wire.CodeInvalid {
		t.Fatalf("option url: %v", err)
	}
	os.MkdirAll(filepath.Join(h.opt.ProjectsRoot, "other"), 0o755)
	err = h.call("projects.clone", CloneParams{URL: filepath.Join(h.dir, "x", "other.git")}, nil)
	if we := asWire(err); we == nil || we.Code != wire.CodeExists {
		t.Fatalf("taken folder: %v", err)
	}
	err = h.call("projects.clone", CloneParams{URL: src, Machine: "M"}, nil)
	if we := asWire(err); we == nil || we.Code != wire.CodeUnavailable || !strings.Contains(err.Error(), "M") {
		t.Fatalf("remote without gateway: %v", err)
	}
	_ = time.Second
}

func asWire(err error) *wire.Error {
	if we, ok := err.(*wire.Error); ok {
		return we
	}
	return nil
}
