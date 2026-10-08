package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestTreeOrderAndPrint(t *testing.T) {
	list := []wire.Agent{
		{ID: "L/root", State: "working", Kind: "claude", Name: "root"},
		{ID: "L/other", State: "idle", Kind: "codex", Name: "other"},
		{ID: "L/kid", State: "done", Kind: "claude", Name: "kid", Parent: "L/root", Depth: 1},
		{ID: "L/gkid", State: "approval", Kind: "claude", Name: "gkid", Parent: "L/kid", Depth: 2},
		{ID: "L/orphan", State: "idle", Kind: "claude", Name: "orphan", Parent: "L/gone", Depth: 1},
		{ID: "M/far", State: "working", Kind: "claude", Name: "far", Parent: "L/root", Depth: 1},
	}
	ordered, levels := treeOrder(list)
	var ids []string
	for _, a := range ordered {
		ids = append(ids, a.ID)
	}
	if got := strings.Join(ids, " "); got != "L/root L/kid L/gkid M/far L/other L/orphan" {
		t.Fatalf("order %s", got)
	}
	if levels["L/gkid"] != 2 || levels["M/far"] != 1 || levels["L/orphan"] != 0 {
		t.Fatalf("levels %v", levels)
	}
	var buf bytes.Buffer
	printAgentTree(&buf, ordered, levels)
	out := buf.String()
	for _, want := range []string{"\nL/root ", "\n└ L/kid ", "\n  └ L/gkid ", "\n└ M/far "} {
		if !strings.Contains(out, want) {
			t.Errorf("tree lacks %q:\n%s", want, out)
		}
	}
	if kids := childrenOf(list, "L/root"); len(kids) != 2 || kids[0].ID != "L/kid" || kids[1].ID != "M/far" {
		t.Fatalf("children %+v", kids)
	}
	// A loop (never made by hesperd) is listed, not lost.
	loop, _ := treeOrder([]wire.Agent{{ID: "L/a", Parent: "L/b"}, {ID: "L/b", Parent: "L/a"}})
	if len(loop) != 2 {
		t.Fatalf("loop %+v", loop)
	}
}

func TestEnvAgentID(t *testing.T) {
	t.Setenv("HESPER_AGENT_ID", "a7f3k2")
	t.Setenv("HESPER_MACHINE", "mini")
	if got := envAgentID(); got != "mini/a7f3k2" {
		t.Fatal(got)
	}
	t.Setenv("HESPER_AGENT_ID", "L/a7f3k2")
	if got := envAgentID(); got != "L/a7f3k2" {
		t.Fatal(got)
	}
	t.Setenv("HESPER_AGENT_ID", "")
	if got := envAgentID(); got != "" {
		t.Fatal(got)
	}
}

// Inside an agent: new makes a child, --wait prints its result, ls
// --children and --tree show it, result prints it, approve follows the
// policy.
func TestTreeCommands(t *testing.T) {
	reg, sock, project := daemon(t)
	ctx := context.Background()
	ctl := func(args ...string) error {
		return run(ctx, append([]string{args[0], "--daemon-socket", sock}, args[1:]...))
	}
	// A person starts the parent.
	out := capture(t, func() {
		if err := ctl("new", "--project", project, "--json", "lead"); err != nil {
			t.Error(err)
		}
	})
	var parent wire.Agent
	if err := json.Unmarshal([]byte(out), &parent); err != nil || parent.Parent != "" {
		t.Fatalf("parent %v %s", err, out)
	}
	// hesperctl now runs inside the parent.
	t.Setenv("HESPER_AGENT_ID", parent.ID)
	type waited struct {
		out string
		err error
	}
	done := make(chan waited, 1)
	go func() {
		var err error
		out := capture(t, func() {
			err = ctl("new", "--project", project, "--let-parent-answer", "--wait", "--timeout", "20s", "--json", "child", "task")
		})
		done <- waited{out, err}
	}()
	var child wire.Agent
	eventually(t, "child", func() bool {
		for _, a := range reg.List() {
			if a.Parent == parent.ID {
				child = a
				return true
			}
		}
		return false
	})
	if child.Depth != 1 || !child.LetParentAnswer {
		t.Fatalf("child %+v", child)
	}
	time.Sleep(100 * time.Millisecond) // --wait subscribed (it would see done anyway)
	reg.Hook(wire.HookParams{Agent: child.ID, Source: "claude", Event: "Stop", Payload: json.RawMessage(`{"last_assistant_message":"All tests pass.\n\nFixed 2 flaky tests"}`)})
	var w waited
	select {
	case w = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("new --wait did not return")
	}
	var res struct {
		Agent  wire.Agent       `json:"agent"`
		Result wire.AgentResult `json:"result"`
	}
	if w.err != nil || json.Unmarshal([]byte(w.out), &res) != nil || res.Agent.ID != child.ID || res.Agent.State != wire.StateDone ||
		res.Result.Message != "All tests pass.\n\nFixed 2 flaky tests" || res.Result.Summary != "Fixed 2 flaky tests" {
		t.Fatalf("new --wait: %v %s", w.err, w.out)
	}
	if out := capture(t, func() {
		if err := ctl("result", child.ID); err != nil {
			t.Error(err)
		}
	}); out != "All tests pass.\n\nFixed 2 flaky tests\n" {
		t.Fatalf("result %q", out)
	}
	if out := capture(t, func() {
		if err := ctl("ls", "--children", "--json"); err != nil {
			t.Error(err)
		}
	}); !strings.Contains(out, child.ID) || strings.Contains(out, `"id": "`+parent.ID) {
		t.Fatalf("ls --children:\n%s", out)
	}
	if out := capture(t, func() {
		if err := ctl("ls", "--tree"); err != nil {
			t.Error(err)
		}
	}); !strings.Contains(out, "\n"+parent.ID+" ") || !strings.Contains(out, "\n└ "+child.ID+" ") {
		t.Fatalf("ls --tree:\n%s", out)
	}
	// The policy through the CLI: the parent answers its child (it was
	// started with --let-parent-answer), not itself (forbidden: exit 5).
	reg.Hook(wire.HookParams{Agent: child.ID, Source: "claude", Event: "PermissionRequest", Payload: json.RawMessage(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`)})
	if err := ctl("approve", child.ID); err != nil {
		t.Fatalf("parent approves its child: %v", err)
	}
	reg.Hook(wire.HookParams{Agent: parent.ID, Source: "claude", Event: "PermissionRequest", Payload: json.RawMessage(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`)})
	if code := execute(ctx, []string{"approve", "--daemon-socket", sock, parent.ID}, io.Discard); code != exitForbidden {
		t.Fatalf("an agent approving itself: exit %d", code)
	}
	if code := execute(ctx, []string{"stop", "--daemon-socket", sock, parent.ID}, io.Discard); code != exitForbidden {
		t.Fatalf("an agent stopping itself: exit %d", code)
	}
	// --wait running out of time exits 6.
	if code := execute(ctx, []string{"new", "--daemon-socket", sock, "--project", project, "--wait", "--timeout", "300ms", "slow"}, io.Discard); code != exitTimeout {
		t.Fatalf("timeout: exit %d", code)
	}
	// A person again.
	t.Setenv("HESPER_AGENT_ID", "")
	if err := ctl("approve", parent.ID); err != nil {
		t.Fatalf("a person approves: %v", err)
	}
	if code := execute(ctx, []string{"ls", "--daemon-socket", sock, "--children"}, io.Discard); code != exitUsage {
		t.Fatalf("ls --children outside an agent: exit %d", code)
	}
}
