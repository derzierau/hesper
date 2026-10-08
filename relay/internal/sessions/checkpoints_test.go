package sessions

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// A hesperd agent's checkpoint stays with its session after the agent is
// closed (Session.checkpoint), and checkpoints.restore makes a worktree
// from it with the uncommitted work.
func TestCheckpointInHistoryAndRestore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	e := newEnv(t)
	t.Setenv("HESPER_WORKTREE_ROOT", filepath.Join(e.dir, "wt"))
	git := func(args ...string) string {
		t.Helper()
		out, err := execGit(e.work, append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(e.work, "a.txt"), []byte("a\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "a")
	e.install("claude.jsonl", e.work)
	s := e.open(Options{})
	reg := e.registry()
	s.SetRegistry(reg)
	waitFor(t, "indexed", func() bool { return len(e.search(wire.SessionSearchParams{})) == 1 })
	id := "L:claude:" + claudeSID
	a, err := s.Resume(id, "", false)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.work, "a.txt"), []byte("a\nWORK\n"), 0o644)
	os.WriteFile(filepath.Join(e.work, "new.txt"), []byte("new\n"), 0o644)
	cp, err := reg.Checkpoint(a.ID)
	if err != nil || cp == nil || cp.Changed != 2 {
		t.Fatalf("checkpoint %+v %v", cp, err)
	}
	if _, err := reg.CloseAgent(a.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "closed", func() bool { _, err := reg.Get(a.ID); return err != nil })
	got, _ := e.session(id)
	if got.Checkpoint == nil || got.Checkpoint.Commit != cp.Commit {
		t.Fatalf("session checkpoint %+v", got.Checkpoint)
	}
	git("checkout", "-q", "--", ".")
	git("clean", "-qfd")
	params, _ := json.Marshal(RestoreParams{Session: id, Ref: cp.Ref, Commit: cp.Commit})
	res, err, _ := s.Call("checkpoints.restore", params)
	if err != nil {
		t.Fatal(err)
	}
	r := res.(RestoreResult)
	if r.Path != filepath.Join(e.dir, "wt", filepath.Base(e.work), "main-restored") || r.Branch != "main-restored" {
		t.Fatalf("restored %+v", r)
	}
	if data, _ := os.ReadFile(filepath.Join(r.Path, "a.txt")); string(data) != "a\nWORK\n" {
		t.Fatalf("a.txt %q", data)
	}
	if _, err := os.Stat(filepath.Join(r.Path, "new.txt")); err != nil {
		t.Fatal(err)
	}
	// A commit that is not that checkpoint: not found.
	params, _ = json.Marshal(RestoreParams{Session: id, Ref: cp.Ref, Commit: git("rev-parse", "HEAD")})
	if _, err, _ := s.Call("checkpoints.restore", params); err == nil || err.(*wire.Error).Code != wire.CodeNotFound {
		t.Fatalf("bad commit: %v", err)
	}
}
