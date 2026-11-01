package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestLoopsCommands(t *testing.T) {
	e := fullDaemon(t)
	e.ok(t, "projects", "promote", e.project, "--name", "app")

	// An agent gets a loop of its own; its child joins it.
	var parent, child wire.Agent
	e.okJSON(t, &parent, "new", "--project", e.project, "--name", "lead", "fix the login")
	t.Setenv("HESPER_AGENT_ID", parent.ID)
	t.Setenv("HESPER_SOCKET", e.sock)
	e.okJSON(t, &child, "new", "--project", e.project, "--name", "helper", "write the test")
	t.Setenv("HESPER_AGENT_ID", "")
	var list []wire.Loop
	eventually(t, "one loop with both agents", func() bool {
		e.okJSON(t, &list, "loops", "ls")
		return len(list) == 1 && list[0].Counts.Agents == 2
	})
	id := list[0].ID
	if out := e.ok(t, "loops", "ls"); !strings.Contains(out, id) || !strings.Contains(out, "NEXT") {
		t.Errorf("ls:\n%s", out)
	}

	// Names: id, title, a beginning of it; ambiguous; not found.
	e.ok(t, "loops", "rename", id, "Login", "fix")
	var l wire.Loop
	e.okJSON(t, &l, "loops", "show", "login f")
	if l.ID != id || l.Title != "Login fix" {
		t.Fatalf("show by prefix: %+v", l)
	}
	other := strings.TrimSpace(e.ok(t, "loops", "new", "Login", "page", "--done-when", "page ships"))
	if !strings.HasPrefix(other, "lp-") {
		t.Fatalf("new: %q", other)
	}
	e.fails(t, exitUsage, codeAmbiguous, "loops", "show", "login")
	e.fails(t, exitNotFound, wire.CodeNotFound, "loops", "show", "nope")
	e.fails(t, exitUsage, codeUsage, "loops", "new")

	// Items: add, after, check by number, rm by id.
	e.okJSON(t, &l, "loops", "item", "add", id, "merge", "the", "PR")
	e.okJSON(t, &l, "loops", "item", "add", id, "--owner", "agent", "--kind", "promise", "--after", "1", "update the changelog")
	items := sortedItems(l.Items)
	if len(items) != 2 || items[0].Owner != wire.OwnerYou || items[1].Kind != wire.ItemPromise || len(items[1].After) != 1 || items[1].After[0] != items[0].ID {
		t.Fatalf("items: %+v", items)
	}
	if l.Next != wire.NextYou || l.NextText == "" {
		t.Errorf("next: %q %q", l.Next, l.NextText)
	}
	e.okJSON(t, &l, "loops", "item", "check", id, "1")
	if it, _ := resolveItem(l.Items, "1"); it.State != wire.ItemDone {
		t.Fatalf("check: %+v", it)
	}
	if out := e.ok(t, "loops", "show", id); !strings.Contains(out, "[x]") || !strings.Contains(out, "update the changelog") || !strings.Contains(out, "app") {
		t.Errorf("show:\n%s", out)
	}
	e.okJSON(t, &l, "loops", "item", "rm", id, items[1].ID)
	if len(l.Items) != 1 {
		t.Fatalf("rm: %+v", l.Items)
	}
	e.fails(t, exitNotFound, wire.CodeNotFound, "loops", "item", "check", id, "9")
	e.fails(t, exitUsage, codeUsage, "loops", "item", "add", id, "--owner", "boss", "x")
	if out := e.ok(t, "loops", "item"); !strings.Contains(out, "loops item add") {
		t.Errorf("loops item:\n%s", out)
	}

	// Links: a URL, a file, a loop (blocks); unlink.
	e.okJSON(t, &l, "loops", "link", id, "--url", "https://example.com/pr/1", "--label", "PR 1")
	e.okJSON(t, &l, "loops", "link", id, "--file", "L:/tmp/brief.md", "--rel", wire.RelHandoff)
	e.okJSON(t, &l, "loops", "link", id, "--loop", other, "--blocks")
	var blocks bool
	for _, k := range l.Links {
		blocks = blocks || k.Kind == wire.LinkLoop && k.Rel == wire.RelBlocks && k.Ref == other
	}
	if !blocks {
		t.Fatalf("blocks: %+v", l.Links)
	}
	e.okJSON(t, &l, "loops", "unlink", id, "--url", "https://example.com/pr/1")
	for _, k := range l.Links {
		if k.Kind == wire.LinkURL {
			t.Fatalf("unlink: %+v", k)
		}
	}
	e.fails(t, exitUsage, codeUsage, "loops", "link", id)
	e.fails(t, exitUsage, codeUsage, "loops", "link", id, "--url", "u", "--loop", other)

	// Graph: the child under its parent.
	var g loopGraph
	e.okJSON(t, &g, "loops", "graph", id)
	if len(g.Blocks) != 1 || g.Blocks[0].Title != "Login page" {
		t.Fatalf("graph blocks: %+v", g.Blocks)
	}
	var lead *graphNode
	for _, n := range g.Nodes {
		if n.Agent == parent.ID {
			lead = n
		}
	}
	if lead == nil || len(lead.Children) != 1 || lead.Children[0].Agent != child.ID || lead.Children[0].Rel != wire.RelStarted {
		t.Fatalf("graph: %+v", g.Nodes)
	}
	if out := e.ok(t, "loops", "graph", id); !strings.Contains(out, "    started agent helper") || !strings.Contains(out, "blocks "+other) {
		t.Errorf("graph:\n%s", out)
	}

	// Park, unpark, close; filters.
	e.okJSON(t, &l, "loops", "park", other, "--until", "tomorrow")
	if l.Status != wire.LoopParked || l.ParkedUntil.IsZero() {
		t.Fatalf("park: %+v", l)
	}
	e.okJSON(t, &l, "loops", "park", other, "--unpark")
	if l.Status != wire.LoopOpen {
		t.Fatalf("unpark: %+v", l)
	}
	e.fails(t, exitUsage, codeUsage, "loops", "park", other, "--until", "someday")
	e.fails(t, exitUsage, codeUsage, "loops", "close", other)
	e.okJSON(t, &l, "loops", "close", other, "--drop")
	if l.Status != wire.LoopDropped {
		t.Fatalf("close: %+v", l)
	}
	e.okJSON(t, &list, "loops", "ls")
	if len(list) != 1 {
		t.Fatalf("ls after close: %d", len(list))
	}
	e.okJSON(t, &list, "loops", "ls", "--status", "dropped")
	if len(list) != 1 || list[0].ID != other {
		t.Fatalf("--status dropped: %+v", list)
	}
	e.okJSON(t, &list, "loops", "ls", "--project", "app")
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("--project: %+v", list)
	}

	// self: the agent's own loop; an agent may not close it.
	t.Setenv("HESPER_AGENT_ID", child.ID)
	e.okJSON(t, &l, "loops", "item", "add", "self", "--owner", "agent", "add the docs")
	if l.ID != id {
		t.Fatalf("self: %s", l.ID)
	}
	e.fails(t, exitForbidden, wire.CodeForbidden, "loops", "close", "self", "--done")
	t.Setenv("HESPER_AGENT_ID", "")

	// Split the child off, merge it back.
	var split wire.Loop
	e.okJSON(t, &split, "loops", "split", id, "helper", "--title", "Login test")
	if split.ID == id || split.Title != "Login test" {
		t.Fatalf("split: %+v", split)
	}
	e.okJSON(t, &l, "loops", "merge", id, split.ID)
	if l.ID != id {
		t.Fatalf("merge: %+v", l)
	}
	e.okJSON(t, &l, "loops", "show", split.ID)
	if l.MergedInto != id {
		t.Fatalf("merged: %+v", l)
	}

	// Settings.
	var s wire.LoopSettings
	e.okJSON(t, &s, "loops", "settings")
	if s.LoopLimit != 5 || s.EveningAt != "18:00" {
		t.Fatalf("settings: %+v", s)
	}
	e.okJSON(t, &s, "loops", "settings", "--loop-limit", "3", "--evening", "19:30")
	if s.LoopLimit != 3 || s.EveningAt != "19:30" || s.StallDays != 7 {
		t.Fatalf("settings set: %+v", s)
	}
	e.fails(t, exitUsage, codeUsage, "loops", "settings", "--evening", "late")
}

// new --loop, --milestone and --report-to-parent reach the spawn.
func TestNewInLoop(t *testing.T) {
	e := fullDaemon(t)
	lid := strings.TrimSpace(e.ok(t, "loops", "new", "Release", "notes"))
	e.ok(t, "projects", "promote", e.project, "--name", "app")
	var ch wire.GoalChanged
	e.okJSON(t, &ch, "goals", "new", "app", "1.4", "--milestone")

	var a wire.Agent
	e.okJSON(t, &a, "new", "--project", e.project, "--loop", "release", "write the notes")
	eventually(t, "joined the loop", func() bool {
		var l wire.Loop
		e.okJSON(t, &l, "loops", "show", lid)
		return l.Counts.Agents == 1
	})
	e.okJSON(t, &a, "new", "--project", e.project, "--milestone", "1.4", "fix the login")
	var list []wire.Loop
	eventually(t, "a loop under the milestone", func() bool {
		e.okJSON(t, &list, "loops", "ls", "--milestone", "1.4")
		return len(list) == 1 && list[0].ID != lid && list[0].Counts.Agents == 1
	})
	e.fails(t, exitUsage, codeUsage, "new", "--project", e.project, "--loop", lid, "--milestone", "1.4", "x")
	e.fails(t, exitNotFound, wire.CodeNotFound, "new", "--project", e.project, "--loop", "nope", "x")
	// --report-to-parent: only inside an agent.
	e.fails(t, exitUsage, codeUsage, "new", "--project", e.project, "--report-to-parent", "x")
	t.Setenv("HESPER_AGENT_ID", a.ID)
	t.Setenv("HESPER_SOCKET", e.sock)
	var child wire.Agent
	e.okJSON(t, &child, "new", "--project", e.project, "--report-to-parent", "write the test")
	if !child.ReportToParent || child.Parent != a.ID {
		t.Fatalf("child: parent %q reportToParent %v", child.Parent, child.ReportToParent)
	}
}

// An older hesperd knows no loops.* method and would drop the loop
// flags: new refuses them (exit 2) and starts nothing.
func TestNewInLoopOnOlderDaemon(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "gctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "old.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var methods []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					line, err := wire.ReadLine(r)
					if err != nil {
						return
					}
					var req wire.Request
					json.Unmarshal(line, &req)
					mu.Lock()
					methods = append(methods, req.Method)
					mu.Unlock()
					out, _ := json.Marshal(wire.Response{JSONRPC: "2.0", ID: req.ID,
						Error: &wire.RPCError{Code: wire.RPCNoMethod, Message: "no method " + req.Method, Data: &wire.ErrorData{Code: wire.CodeNotFound}}})
					conn.Write(append(out, '\n'))
				}
			}()
		}
	}()
	for _, flags := range [][]string{{"--loop", "lp-000000000001"}, {"--milestone", "1.4"}} {
		args := append([]string{"new", "--project", t.TempDir(), "--daemon-socket", sock, "--json"}, flags...)
		code, errCode, msg := jsonError(t, append(args, "fix it")...)
		if code != exitUsage || errCode != codeUsage || !strings.Contains(msg, "does not know loops") {
			t.Fatalf("%v: exit %d %s %s", flags, code, errCode, msg)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if contains(methods, "agents.spawn") {
		t.Fatalf("spawned anyway: %v", methods)
	}
}
