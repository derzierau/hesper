package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// mcpClient drives a hesperctl mcp server over pipes. The server runs in
// the test; its tool calls run the test binary as hesperctl (TestMain,
// HESPERCTL_TEST_MAIN) on the test daemon's socket.
type mcpClient struct {
	t      *testing.T
	server *mcpServer
	in     *io.PipeWriter
	mu     sync.Mutex
	nextID int
	resp   map[string]chan map[string]any
	notes  chan map[string]any
	done   chan struct{}
}

func newMCPClient(t *testing.T, sock string) *mcpClient {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "HESPERCTL_TEST_MAIN=1", "HESPER_SOCKET="+sock)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := newMCPServer(outW, testLog{t}, exe, env)
	c := &mcpClient{t: t, server: s, in: inW, resp: map[string]chan map[string]any{}, notes: make(chan map[string]any, 64), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		s.serve(ctx, inR)
		outW.Close()
		close(c.done)
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(nil, 16<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("server wrote a non-JSON line: %q", sc.Text())
				continue
			}
			if m["jsonrpc"] != "2.0" {
				t.Errorf("no jsonrpc 2.0: %v", m)
			}
			if _, isResponse := m["id"]; !isResponse {
				c.notes <- m
				continue
			}
			id, _ := json.Marshal(m["id"])
			c.mu.Lock()
			ch := c.resp[string(id)]
			c.mu.Unlock()
			if ch == nil {
				t.Errorf("unexpected response %v", m)
				continue
			}
			ch <- m
		}
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-c.done:
		case <-time.After(20 * time.Second):
			t.Error("server did not stop after stdin closed")
		}
		cancel()
	})
	return c
}

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func (c *mcpClient) write(msg any) {
	c.t.Helper()
	data, _ := json.Marshal(msg)
	if _, err := c.in.Write(append(data, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

// start sends a request and returns its id and the channel its response
// arrives on.
func (c *mcpClient) start(method string, params any) (int, chan map[string]any) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan map[string]any, 1)
	c.resp[fmt.Sprint(id)] = ch
	c.mu.Unlock()
	c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return id, ch
}

// call sends a request and returns its result (an error response fails).
func (c *mcpClient) call(method string, params any) map[string]any {
	c.t.Helper()
	m := c.rawCall(method, params)
	if m["error"] != nil {
		c.t.Fatalf("%s: %v", method, m["error"])
	}
	result, _ := m["result"].(map[string]any)
	return result
}

func (c *mcpClient) rawCall(method string, params any) map[string]any {
	c.t.Helper()
	_, ch := c.start(method, params)
	select {
	case m := <-ch:
		return m
	case <-time.After(30 * time.Second):
		c.t.Fatalf("%s: no response", method)
	}
	return nil
}

// tool calls a tool and returns its text and isError.
func (c *mcpClient) tool(name string, args map[string]any) (string, bool) {
	c.t.Helper()
	r := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	content, _ := r["content"].([]any)
	if len(content) != 1 {
		c.t.Fatalf("%s: content %v", name, r)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	isError, _ := r["isError"].(bool)
	return text, isError
}

// ok calls a tool that must succeed and decodes its JSON into v.
func (c *mcpClient) ok(name string, args map[string]any, v any) {
	c.t.Helper()
	text, isError := c.tool(name, args)
	if isError {
		c.t.Fatalf("%s %v: %s", name, args, text)
	}
	if v != nil {
		if err := json.Unmarshal([]byte(text), v); err != nil {
			c.t.Fatalf("%s: %v in %s", name, err, text)
		}
	}
}

// failure calls a tool that must fail and returns its error JSON.
func (c *mcpClient) failure(name string, args map[string]any) (code string, exit int) {
	c.t.Helper()
	text, isError := c.tool(name, args)
	if !isError {
		c.t.Fatalf("%s %v: succeeded: %s", name, args, text)
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		ExitCode int `json:"exitCode"`
	}
	if err := json.Unmarshal([]byte(text), &e); err != nil {
		c.t.Fatalf("%s: error is not JSON: %s", name, text)
	}
	return e.Error.Code, e.ExitCode
}

func (c *mcpClient) initialize(version string) map[string]any {
	c.t.Helper()
	r := c.call("initialize", map[string]any{"protocolVersion": version, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"}})
	c.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return r
}

func TestMCPHandshakeAndTools(t *testing.T) {
	c := newMCPClient(t, filepath.Join(t.TempDir(), "none.sock"))
	r := c.initialize("2025-06-18")
	if r["protocolVersion"] != "2025-06-18" {
		t.Fatalf("version %v", r["protocolVersion"])
	}
	caps, _ := r["capabilities"].(map[string]any)
	if caps["tools"] == nil || caps["resources"] == nil {
		t.Fatalf("capabilities %v", caps)
	}
	if info, _ := r["serverInfo"].(map[string]any); info["name"] != "hesper" {
		t.Fatalf("serverInfo %v", r["serverInfo"])
	}
	// An unknown version gets the newest; a dual-era probe gets an error
	// (so the client falls back to initialize).
	if r := c.call("initialize", map[string]any{"protocolVersion": "2099-01-01"}); r["protocolVersion"] != mcpVersions[0] {
		t.Fatalf("unknown version answered %v", r["protocolVersion"])
	}
	if m := c.rawCall("server/discover", map[string]any{}); m["error"] == nil {
		t.Fatalf("server/discover: %v", m)
	}
	if len(c.call("ping", nil)) != 0 {
		t.Fatal("ping")
	}

	tools := map[string]map[string]any{}
	for _, v := range c.call("tools/list", nil)["tools"].([]any) {
		tool := v.(map[string]any)
		tools[tool["name"].(string)] = tool
	}
	// Every command but the NoMCP ones is a tool.
	for _, cmd := range commands {
		_, listed := tools[mcpToolName(cmd)]
		if listed == cmd.NoMCP {
			t.Errorf("%s: listed %v, NoMCP %v", cmd.Name, listed, cmd.NoMCP)
		}
	}
	for _, name := range []string{"hesper_agents_new", "hesper_agents_ls", "hesper_agents_wait", "hesper_history_search", "hesper_projects_ls", "hesper_reference"} {
		if tools[name] == nil {
			t.Errorf("no tool %s", name)
		}
	}
	for _, name := range []string{"hesper_agents_attach", "hesper_agents_events", "hesper_help", "hesper_mcp", "hesper_login", "hesper_pair", "hesper_watch"} {
		if tools[name] != nil {
			t.Errorf("tool %s should be left out", name)
		}
	}
	for name, tool := range tools {
		checkSchema(t, name, tool["inputSchema"])
		if d, _ := tool["description"].(string); !strings.Contains(d, "CLI: hesperctl ") {
			t.Errorf("%s: description %q", name, d)
		}
	}
	ann := func(name, hint string) any { return tools[name]["annotations"].(map[string]any)[hint] }
	if ann("hesper_agents_screen", "readOnlyHint") != true || ann("hesper_history_search", "readOnlyHint") != true {
		t.Error("screen and history search are read-only")
	}
	if ann("hesper_agents_kill", "destructiveHint") != true || ann("hesper_agents_close", "destructiveHint") != true || ann("hesper_agents_send", "destructiveHint") != false {
		t.Error("kill and close are destructive, send is not")
	}
	props := func(name string) map[string]any {
		return tools[name]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	}
	if p := props("hesper_agents_send"); p["id"] == nil || p["text"].(map[string]any)["type"] != "array" || p["key"].(map[string]any)["type"] != "array" || p["daemon-socket"] != nil || p["json"] != nil {
		t.Errorf("send's schema: %v", p)
	}
	if p := props("hesper_agents_wait"); p["timeout"].(map[string]any)["type"] != "string" || p["until"] == nil || p["next"].(map[string]any)["type"] != "boolean" {
		t.Errorf("wait's schema: %v", p)
	}
	if req := tools["hesper_agents_screen"]["inputSchema"].(map[string]any)["required"]; fmt.Sprint(req) != "[id]" {
		t.Errorf("screen requires %v", req)
	}
	if p := props("hesper_agents_screen"); p["rows"].(map[string]any)["type"] != "integer" {
		t.Errorf("screen's schema: %v", p)
	}

	// Without hesperd: exit 4 as isError with the JSON error.
	if code, exit := c.failure("hesper_agents_ls", nil); code != wire.CodeUnavailable || exit != exitUnavailable {
		t.Errorf("ls without hesperd: %s %d", code, exit)
	}
	// Bad parameters are tool errors (exit 2), an unknown tool a protocol error.
	if code, exit := c.failure("hesper_agents_screen", map[string]any{"id": "x", "rows": "many"}); code != codeUsage || exit != exitUsage {
		t.Errorf("bad rows: %s %d", code, exit)
	}
	if code, _ := c.failure("hesper_agents_screen", map[string]any{"id": "x", "bogus": 1}); code != codeUsage {
		t.Errorf("unknown parameter: %s", code)
	}
	if code, _ := c.failure("hesper_agents_screen", map[string]any{}); code != codeUsage {
		t.Errorf("missing id: %s", code)
	}
	if m := c.rawCall("tools/call", map[string]any{"name": "hesper_nope"}); m["error"] == nil {
		t.Errorf("unknown tool: %v", m)
	}
	// The reference needs no daemon.
	r = c.call("resources/read", map[string]any{"uri": "hesper://reference"})
	if text := r["contents"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "# hesperctl reference") {
		t.Errorf("reference: %.200s", text)
	}
	if m := c.rawCall("resources/read", map[string]any{"uri": "hesper://nope"}); m["error"] == nil {
		t.Errorf("unknown resource: %v", m)
	}
	if list := c.call("resources/list", nil)["resources"].([]any); len(list) != 3 {
		t.Errorf("resources %v", list)
	}
}

// checkSchema checks the subset of JSON Schema the tools use.
func checkSchema(t *testing.T, name string, v any) {
	t.Helper()
	s, ok := v.(map[string]any)
	if !ok || s["type"] != "object" {
		t.Errorf("%s: schema %v", name, v)
		return
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Errorf("%s: properties %v", name, s["properties"])
		return
	}
	for pname, pv := range props {
		p, _ := pv.(map[string]any)
		switch p["type"] {
		case "string", "integer", "number", "boolean":
		case "array":
			if items, _ := p["items"].(map[string]any); items["type"] != "string" {
				t.Errorf("%s.%s: items %v", name, pname, p["items"])
			}
		default:
			t.Errorf("%s.%s: type %v", name, pname, p["type"])
		}
		if d, _ := p["description"].(string); d == "" {
			t.Errorf("%s.%s: no description", name, pname)
		}
	}
	if req, ok := s["required"]; ok {
		for _, r := range req.([]any) {
			if props[r.(string)] == nil {
				t.Errorf("%s: required %v is no property", name, r)
			}
		}
	}
}

// A command registered later (desk open, say) becomes a tool without
// more code: its flags and arguments are the schema.
func TestMCPToolFromCommand(t *testing.T) {
	c := &Command{Name: "desk open", Group: "Desks", Summary: "Open a desk", Usage: "desk open NAME [--window N] [--focus] [--tag T]… [--for D] [ID…] [--json]",
		Run: func(ctx context.Context, f *flag.FlagSet, args []string) error {
			f.Int("window", 0, "Window `N`")
			f.Bool("focus", false, "Focus it")
			var tags listFlag
			f.Var(&tags, "tag", "Tag (repeatable)")
			f.Duration("for", 0, "How long")
			f.String("name", "", "clashes with NAME")
			return f.Parse(args)
		}}
	tool := newMCPTool(c)
	if tool.Name != "hesper_desk_open" || tool.Args {
		t.Fatalf("%+v", tool)
	}
	kinds := map[string]string{}
	for _, p := range tool.Params {
		kinds[p.Name] = p.Kind
	}
	want := map[string]string{"name_arg": "string", "id": "list", "window": "integer", "focus": "boolean", "tag": "list", "for": "duration", "name": "string"}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("params %v", kinds)
	}
	argv, err := tool.argv(map[string]json.RawMessage{"name_arg": []byte(`"-odd"`), "id": []byte(`["a","b"]`), "window": []byte(`2`),
		"focus": []byte(`true`), "tag": []byte(`["x","y"]`), "for": []byte(`90`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(argv, " "); got != "desk open --json --focus=true --for=1m30s --tag=x --tag=y --window=2 -- -odd a b" {
		t.Fatalf("argv %s", got)
	}
	// Usage it cannot read: an args array.
	odd := &Command{Name: "odd", Usage: "odd <thing>", Run: func(ctx context.Context, f *flag.FlagSet, args []string) error { return f.Parse(args) }}
	if tool := newMCPTool(odd); !tool.Args || tool.schema()["properties"].(map[string]any)["args"] == nil {
		t.Fatalf("odd: %+v", tool)
	}
	// wait's timeout: default and cap.
	wait := newMCPTool(findCommand("wait"))
	argv, _ = wait.argv(map[string]json.RawMessage{"id": []byte(`["a"]`), "until": []byte(`"done"`)})
	if !strings.Contains(strings.Join(argv, " "), "--timeout=10m") {
		t.Errorf("wait without timeout: %v", argv)
	}
	argv, _ = wait.argv(map[string]json.RawMessage{"id": []byte(`["a"]`), "until": []byte(`"done"`), "timeout": []byte(`"5h"`)})
	if !strings.Contains(strings.Join(argv, " "), "--timeout=10m0s") {
		t.Errorf("wait capped: %v", argv)
	}
	newTool := newMCPTool(findCommand("new"))
	if argv, _ := newTool.argv(map[string]json.RawMessage{"task": []byte(`"x"`)}); strings.Contains(strings.Join(argv, " "), "timeout") {
		t.Errorf("new without wait: %v", argv)
	}
	if argv, _ := newTool.argv(map[string]json.RawMessage{"task": []byte(`"x"`), "wait": []byte(`true`)}); !strings.Contains(strings.Join(argv, " "), "--timeout=10m") {
		t.Errorf("new --wait: %v", argv)
	}
}

func TestMCPAgents(t *testing.T) {
	reg, sock, project := daemon(t)
	c := newMCPClient(t, sock)
	c.initialize(mcpVersions[0])

	var list []wire.Agent
	c.ok("hesper_agents_ls", nil, &list)
	if len(list) != 0 {
		t.Fatalf("ls %v", list)
	}
	var shell wire.Agent
	c.ok("hesper_agents_new", map[string]any{"project": project, "kind": "shell", "name": "sh1", "task": []string{"echo", "started"}}, &shell)
	if shell.ID == "" || shell.Kind != wire.KindShell {
		t.Fatalf("new %+v", shell)
	}
	eventually(t, "running", func() bool { g, _ := reg.Get(shell.ID); return g.PID != 0 })
	c.ok("hesper_agents_send", map[string]any{"id": "sh1", "text": "echo mcp-$((6*7))"}, nil)
	eventually(t, "screen", func() bool {
		var s struct{ Text string }
		c.ok("hesper_agents_screen", map[string]any{"id": shell.ID}, &s)
		return strings.Contains(s.Text, "mcp-42")
	})
	c.ok("hesper_agents_ls", nil, &list)
	if len(list) != 1 || list[0].ID != shell.ID {
		t.Fatalf("ls %+v", list)
	}

	// Error mapping: not found exits 3, a wait that times out 6.
	if code, exit := c.failure("hesper_agents_show", map[string]any{"id": "nope"}); code != wire.CodeNotFound || exit != exitNotFound {
		t.Errorf("show nope: %s %d", code, exit)
	}
	if code, exit := c.failure("hesper_agents_wait", map[string]any{"id": []string{shell.ID}, "until": "exited", "timeout": "300ms"}); code != codeTimeout || exit != exitTimeout {
		t.Errorf("wait: %s %d", code, exit)
	}

	// Resources: agents, and needs-you once a claude agent asks.
	read := func(uri string) []wire.Agent {
		r := c.call("resources/read", map[string]any{"uri": uri})
		var l []wire.Agent
		if err := json.Unmarshal([]byte(r["contents"].([]any)[0].(map[string]any)["text"].(string)), &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := read("hesper://agents"); len(l) != 1 {
		t.Fatalf("agents %v", l)
	}
	if l := read("hesper://needs-you"); len(l) != 0 {
		t.Fatalf("needs-you %v", l)
	}
	var asker wire.Agent
	c.ok("hesper_agents_new", map[string]any{"project": project, "task": "ask me"}, &asker)
	eventually(t, "asker running", func() bool { g, _ := reg.Get(asker.ID); return g.PID != 0 })
	reg.Hook(wire.HookParams{Agent: asker.ID, Source: "claude", Event: "PermissionRequest", Payload: json.RawMessage(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`)})
	if l := read("hesper://needs-you"); len(l) != 1 || l[0].ID != asker.ID || l[0].Attention == nil {
		t.Fatalf("needs-you %+v", l)
	}
	c.ok("hesper_agents_approve", map[string]any{"id": asker.ID}, nil)
	if l := read("hesper://needs-you"); len(l) != 0 {
		t.Fatalf("needs-you after approve %+v", l)
	}

	// close: an action whose JSON is the closed agents.
	var closed []struct{ ID string }
	c.ok("hesper_agents_close", map[string]any{"id": []string{shell.ID, asker.ID}}, &closed)
	if len(closed) != 2 {
		t.Fatalf("close %+v", closed)
	}
	eventually(t, "closed", func() bool { return len(reg.List()) == 0 })
}

func TestMCPCancelWait(t *testing.T) {
	reg, sock, project := daemon(t)
	c := newMCPClient(t, sock)
	c.initialize(mcpVersions[0])
	var a wire.Agent
	c.ok("hesper_agents_new", map[string]any{"project": project, "task": "work"}, &a)
	eventually(t, "running", func() bool { g, _ := reg.Get(a.ID); return g.PID != 0 })

	id, ch := c.start("tools/call", map[string]any{"name": "hesper_agents_wait", "arguments": map[string]any{"id": []string{a.ID}, "until": "exited"}})
	eventually(t, "wait under way", func() bool {
		c.server.mu.Lock()
		defer c.server.mu.Unlock()
		return len(c.server.inflight) == 1
	})
	time.Sleep(200 * time.Millisecond) // the child is waiting
	began := time.Now()
	c.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": id, "reason": "test"}})
	eventually(t, "wait cancelled", func() bool {
		c.server.mu.Lock()
		defer c.server.mu.Unlock()
		return len(c.server.inflight) == 0
	})
	if d := time.Since(began); d > 3*time.Second {
		t.Errorf("cancel took %s (the child did not stop on SIGINT)", d)
	}
	select {
	case m := <-ch:
		t.Fatalf("a cancelled request got a response: %v", m)
	case <-time.After(300 * time.Millisecond):
	}
	// The server goes on.
	var list []wire.Agent
	c.ok("hesper_agents_ls", nil, &list)
	if len(list) != 1 {
		t.Fatalf("ls %v", list)
	}
}

func TestMCPSubscribe(t *testing.T) {
	reg, sock, project := daemon(t)
	c := newMCPClient(t, sock)
	c.initialize(mcpVersions[0])
	c.call("resources/subscribe", map[string]any{"uri": "hesper://needs-you"})
	var a wire.Agent
	c.ok("hesper_agents_new", map[string]any{"project": project, "task": "work"}, &a)
	select {
	case n := <-c.notes:
		if n["method"] != "notifications/resources/updated" || n["params"].(map[string]any)["uri"] != "hesper://needs-you" {
			t.Fatalf("note %v", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no notifications/resources/updated")
	}
	c.call("resources/unsubscribe", map[string]any{"uri": "hesper://needs-you"})
	_ = reg
}

// Inside an agent: hesperctl mcp runs as the agent's child (as Claude
// Code runs it), and hesperd sees its tool calls as the agent's, from the
// process tree alone (HESPER_AGENT_ID unset here): a new agent is the
// agent's child.
func TestMCPCallerIsTheAgent(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	requests := filepath.Join(dir, "requests")
	out := filepath.Join(dir, "out")
	script := fmt.Sprintf("unset HESPER_AGENT_ID HESPER_MACHINE; HESPERCTL_TEST_MAIN=1 %q mcp < %q > %q 2>&1; exec cat", exe, requests, out)
	reg, sock, project := daemonWith(t, map[string]wire.Profile{
		"probe": {Kind: wire.KindClaude, Argv: []string{"/bin/sh", "-c", script}},
	})
	var lines []string
	for _, m := range []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": mcpVersions[0]}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "hesper_agents_new",
			"arguments": map[string]any{"project": project, "name": "kid", "task": "child work"}}},
	} {
		data, _ := json.Marshal(m)
		lines = append(lines, string(data))
	}
	os.WriteFile(requests, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	if err := run(context.Background(), []string{"new", "--daemon-socket", sock, "--project", project, "--profile", "probe", "--name", "probe", "lead"}); err != nil {
		t.Fatal(err)
	}
	var probe, kid wire.Agent
	eventually(t, "kid started", func() bool {
		for _, a := range reg.List() {
			switch a.Name {
			case "probe":
				probe = a
			case "kid":
				kid = a
			}
		}
		return kid.ID != ""
	})
	if kid.Parent != probe.ID || kid.Depth != 1 {
		data, _ := os.ReadFile(out)
		t.Fatalf("kid's parent %q depth %d, want %q (verified caller)\n%s", kid.Parent, kid.Depth, probe.ID, data)
	}
}
