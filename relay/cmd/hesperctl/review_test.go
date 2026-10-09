package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The review commands on a finished agent (a cat whose turn ends by a
// hook) in a repository: ls, show, diff, evidence, provenance, reject,
// accept, send-back.
func TestReviewCommands(t *testing.T) {
	for _, kv := range []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"} {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	w := newMoreWorld(t)
	gitRun(t, w.project, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(w.project, "a.txt"), []byte("one\n"), 0o644)
	gitRun(t, w.project, "add", ".")
	gitRun(t, w.project, "commit", "-q", "--no-gpg-sign", "-m", "first")
	a := w.spawn("do it")
	hook := func(event, payload string) {
		t.Helper()
		if err := w.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: event, Payload: json.RawMessage(payload)}); err != nil {
			t.Fatal(err)
		}
	}
	// review SUB … --daemon-socket S
	rout := func(args ...string) string {
		t.Helper()
		var err error
		out := capture(t, func() { err = run(context.Background(), append(args, "--daemon-socket", w.sock)) })
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	rcode := func(args ...string) int {
		return execute(context.Background(), append(args, "--daemon-socket", w.sock), io.Discard)
	}
	file := filepath.Join(w.project, "a.txt")
	hook("UserPromptSubmit", `{"prompt":"add lines"}`)
	os.WriteFile(file, []byte("one\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(w.project, "b.txt"), []byte("bee\n"), 0o644)
	hook("PostToolUse", `{"tool_name":"Edit","tool_input":{"file_path":"`+file+`"}}`)
	hook("PreToolUse", `{"tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"make test"}}`)
	hook("PostToolUse", `{"tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"make test"}}`)
	hook("Stop", `{"last_assistant_message":"Added lines"}`)
	eventually(t, "checkpoint", func() bool { g, _ := w.reg.Get(a.ID); return g.Checkpoint != nil })

	var list []wire.ReviewItem
	if err := json.Unmarshal([]byte(rout("review", "ls", "--json")), &list); err != nil || len(list) != 1 || list[0].ID != a.ID || list[0].Files != 2 || list[0].Evidence != wire.EvidenceFresh {
		t.Fatalf("ls %+v %v", list, err)
	}
	if out := rout("review", "ls"); !strings.Contains(out, a.ID) || !strings.Contains(out, "fresh") || !strings.Contains(out, "+2 -0") {
		t.Errorf("ls:\n%s", out)
	}
	if out := rout("review", "show", a.ID); !strings.Contains(out, "a.txt") || !strings.Contains(out, "b.txt") || !strings.Contains(out, "Evidence: fresh") || !strings.Contains(out, "make test") {
		t.Errorf("show:\n%s", out)
	}
	var d wire.ReviewDiff
	if err := json.Unmarshal([]byte(rout("review", "diff", a.ID, "--json")), &d); err != nil || len(d.Files) != 2 {
		t.Fatalf("diff %+v %v", d, err)
	}
	if out := rout("review", "diff", a.ID); !strings.Contains(out, "@@ 0:0") || !strings.Contains(out, "+two") {
		t.Errorf("diff:\n%s", out)
	}
	var ev wire.ReviewEvidence
	if err := json.Unmarshal([]byte(rout("review", "evidence", a.ID, "--json")), &ev); err != nil || ev.Freshness != wire.EvidenceFresh || len(ev.Commands) != 1 {
		t.Fatalf("evidence %+v %v", ev, err)
	}
	if out := rout("review", "provenance", a.ID, "a.txt:2"); !strings.Contains(out, "turn 1") || !strings.Contains(out, "add lines") {
		t.Errorf("provenance:\n%s", out)
	}

	index := map[string]string{}
	for i, f := range d.Files {
		index[f.Path] = strconv.Itoa(i)
	}
	if code := rcode("review", "reject", a.ID); code != exitUsage {
		t.Errorf("reject without --hunk: exit %d", code)
	}
	rout("review", "reject", a.ID, "--hunk", index["b.txt"], "--tree", d.Tree)
	if _, err := os.Stat(filepath.Join(w.project, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("b.txt not rejected")
	}
	commit := strings.TrimSpace(rout("review", "accept", a.ID, "-m", "Add a line"))
	if gitRun(t, w.project, "rev-parse", "HEAD") != commit || gitRun(t, w.project, "log", "-1", "--format=%s") != "Add a line" {
		t.Fatalf("accept: %s", commit)
	}

	os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644)
	rout("review", "send-back", a.ID, "--note", "a.txt:3 why three, not 3?", "--message", "Fix it.")
	eventually(t, "the notes on the agent's screen", func() bool { return strings.Contains(screenOf(w.reg, a.ID), "why three, not 3?") })
	if n, err := parseNote("src/x.go:-7 old one"); err != nil || n.Path != "src/x.go" || n.Line != 7 || n.Side != "old" || n.Text != "old one" {
		t.Errorf("old-side note %+v %v", n, err)
	}
	if code := rcode("review", "send-back", a.ID); code != exitUsage {
		t.Errorf("send-back without notes: exit %d", code)
	}
}

// Every review command is an MCP tool with its arguments.
func TestReviewMCPTools(t *testing.T) {
	want := map[string][]string{"hesper_review_ls": nil, "hesper_review_show": {"id"}, "hesper_review_diff": {"id", "context"},
		"hesper_review_accept": {"id", "hunk", "message"}, "hesper_review_reject": {"id", "hunk"}, "hesper_review_send_back": {"id", "note", "message"},
		"hesper_review_evidence": {"id"}, "hesper_review_provenance": {"id", "location"}}
	for _, tool := range mcpTools() {
		params, ok := want[tool.Name]
		if !ok {
			continue
		}
		delete(want, tool.Name)
		have := map[string]bool{}
		for _, p := range tool.Params {
			have[p.Name] = true
		}
		for _, p := range params {
			if !have[p] || tool.Args {
				t.Errorf("%s has no %s (%+v)", tool.Name, p, tool.Params)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("missing tools %v", want)
	}
	if out := capture(t, func() { writeReference(os.Stdout) }); !strings.Contains(out, "review send-back") {
		t.Error("the reference has no review commands")
	}
}
