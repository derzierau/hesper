package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/fakeagent"

	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGENTS_FAKE") == "1" && len(os.Args) > 1 {
		fakeagent.Run(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type harness struct {
	t       *testing.T
	dir     string
	state   string
	config  string
	project string
	logs    string
	sock    string
	env     []string
	opt     Options
	reg     *Registry
	srv     *Server
}

// newHarness: a daemon on a socket in a temp directory (short: socket
// paths are limited), fake agent profiles, a trusted project.
func newHarness(t *testing.T, env ...string) *harness {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h := &harness{t: t, dir: dir, state: filepath.Join(dir, "s"), config: filepath.Join(dir, "c"), project: filepath.Join(dir, "proj"), logs: filepath.Join(dir, "logs")}
	for _, d := range []string{h.config, h.project, h.logs} {
		os.MkdirAll(d, 0o755)
	}
	h.project, _ = filepath.EvalSymlinks(h.project)
	exe, _ := os.Executable()
	profiles := map[string]wire.Profile{
		"fake-claude": {Kind: wire.KindClaude, Argv: []string{exe, "claude", "--permission-mode", "auto", "--remote-control", "{name}"}},
		"fake-codex":  {Kind: wire.KindCodex, Argv: []string{exe, "codex", "--dangerously-bypass-approvals-and-sandbox"}},
		"fake-shell":  {Kind: wire.KindShell, Argv: []string{exe, "shell"}},
		// shell.go: a real shell (job control), and one whose prompt comes late
		"real-shell": {Kind: wire.KindShell, Argv: []string{"/bin/sh", "-i"}},
		"slow-shell": {Kind: wire.KindShell, Argv: []string{"/bin/sh", "-c", "sleep 1; exec /bin/sh -i"}},
	}
	writeJSON(t, filepath.Join(h.config, "profiles.json"), profiles)
	writeJSON(t, filepath.Join(h.config, "settings.json"), map[string]any{"defaults": map[string]any{"kind": "claude", "kinds": map[string]string{"claude": "fake-claude", "codex": "fake-codex", "shell": "fake-shell"}}})
	writeJSON(t, filepath.Join(dir, "claude.json"), map[string]any{"projects": map[string]any{h.project: map[string]any{"hasTrustDialogAccepted": true}}})
	h.env = append(os.Environ(), "AGENTS_FAKE=1", "FAKE_LOG="+h.logs)
	for _, kv := range env {
		h.env = append(h.env, strings.ReplaceAll(kv, "{dir}", dir)) // {dir}: the harness's directory
	}
	h.sock = filepath.Join(h.state, "hesperd.sock")
	h.opt = Options{StateDir: h.state, ConfigDir: h.config, Socket: h.sock, Machine: "L", Env: h.env, LoginShell: "/bin/sh",
		WorktreeRoot: filepath.Join(dir, "wt"), ProjectsRoot: dir, ClaudeConfig: filepath.Join(dir, "claude.json"),
		CodexHome: filepath.Join(dir, "codex"), ClaudeHome: filepath.Join(dir, "claudehome"), StopGrace: 2 * time.Second, DeliverPoll: 100 * time.Millisecond, Logf: t.Logf}
	h.open()
	t.Cleanup(h.close)
	return h
}

func (h *harness) open() {
	h.t.Helper()
	reg, err := Open(h.opt)
	if err != nil {
		h.t.Fatal(err)
	}
	ln, err := Listen(h.sock)
	if err != nil {
		h.t.Fatal(err)
	}
	h.reg, h.srv = reg, NewServer(reg)
	go h.srv.Serve(ln)
}

func (h *harness) close() {
	if h.srv != nil {
		h.srv.Close()
		h.reg.Close()
		h.srv = nil
	}
}

func writeJSON(t *testing.T, path string, v any) {
	data, _ := json.Marshal(v)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) client() *wire.Client {
	h.t.Helper()
	c, err := wire.Dial(context.Background(), h.sock)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { c.Close() })
	return c
}

func (h *harness) call(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return h.client().Call(ctx, method, params, result)
}

func (h *harness) spawn(p wire.SpawnParams) wire.Agent {
	h.t.Helper()
	if p.Project == "" {
		p.Project = h.project
	}
	var a wire.Agent
	if err := h.call("agents.spawn", p, &a); err != nil {
		h.t.Fatal(err)
	}
	return a
}

func (h *harness) waitState(id, state string) wire.Agent {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		a, err := h.reg.Get(id)
		if err == nil && a.State == state {
			return a
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: state %q, want %q (screen: %s)", id, a.State, state, h.screen(id))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) screen(id string) string {
	term, err := h.reg.Term(id)
	if err != nil {
		return err.Error()
	}
	var rows []string
	term.WithScreen(func(s *vt.Screen) { rows = screenRows(s, 1000) })
	var out []string
	for _, r := range rows {
		if strings.TrimSpace(r) != "" {
			out = append(out, strings.TrimSpace(r))
		}
	}
	return strings.Join(out, " | ")
}

func (h *harness) waitScreen(id, text string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(h.screen(id), text) {
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: no %q on screen: %s", id, text, h.screen(id))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) waitKeys(id, want string) {
	h.t.Helper()
	path := filepath.Join(h.logs, strings.ReplaceAll(id, "/", "_")+".keys")
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(path)
		if string(data) == want {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("%s: keys %q, want %q", id, data, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) argvs(id string) [][]string {
	data, _ := os.ReadFile(filepath.Join(h.logs, strings.ReplaceAll(id, "/", "_")+".argv"))
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var a []string
		if json.Unmarshal([]byte(line), &a) == nil {
			out = append(out, a)
		}
	}
	return out
}

func TestSpawnStatesAnswerDone(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Call(ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	a := h.spawn(wire.SpawnParams{Task: "Push provider FCM, approve the push\nmore detail"})
	if a.ID[:2] != "L/" || len(a.ID) != 8 || a.Name != "push-provider-fcm-approve-the-push" || a.Kind != "claude" || a.Profile != "fake-claude" {
		t.Fatalf("agent %+v", a)
	}
	if a.State != wire.StateStarting || a.SessionID == "" {
		t.Fatalf("spawned %+v", a)
	}
	got := h.waitState(a.ID, wire.StateApproval)
	if got.Attention == nil || got.Attention.Title != "Bash" || got.Attention.Detail != "git push origin main" || strings.Join(got.Attention.Options, ",") != "allow,always,deny" {
		t.Fatalf("attention %+v", got.Attention)
	}
	argv := h.argvs(a.ID)[0]
	want := []string{"claude", "--permission-mode", "auto", "--remote-control", "Push provider FCM, approve the push", "--session-id", a.SessionID, a.Task}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv %q\nwant %q", argv, want)
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitKeys(a.ID, "1")
	done := h.waitState(a.ID, wire.StateDone)
	if done.Summary != "Opened PR #482 (draft)" || done.Attention != nil {
		t.Fatalf("done %+v", done)
	}
	// Notifications: every state on the way, in order.
	var states []string
	for len(states) == 0 || states[len(states)-1] != wire.StateDone {
		select {
		case n := <-c.Notifications():
			if n.Method != wire.NoteChanged {
				continue
			}
			var ch wire.Changed
			json.Unmarshal(n.Params, &ch)
			if len(states) == 0 || states[len(states)-1] != ch.Agent.State {
				states = append(states, ch.Agent.State)
			}
		case <-ctx.Done():
			t.Fatalf("notifications %v", states)
		}
	}
	if !strings.Contains(strings.Join(states, ","), "approval,working,done") {
		t.Fatalf("states %v", states)
	}
	// Answering when nothing waits is refused.
	err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil)
	if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeInvalid {
		t.Fatalf("answer when done: %v", err)
	}
}

func TestDenyWithMessageAndInput(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "approve this"})
	h.waitState(a.ID, wire.StateApproval)
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Deny, Message: "use a PR instead"}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitKeys(a.ID, "")
	if d := h.waitState(a.ID, wire.StateDone); d.Summary != "Done: use a PR instead" {
		t.Fatalf("after deny %+v", d)
	}
	// agents.input with paste and submit.
	if err := h.call("agents.input", wire.InputParams{ID: a.ID, Text: "next thing", Paste: true, Submit: true}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { g, _ := h.reg.Get(a.ID); return g.Summary == "Done: next thing" })
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCodexHooksFromItsServer(t *testing.T) {
	h := newHarness(t, "FAKE_CODEX_SERVER=1")
	a := h.spawn(wire.SpawnParams{Kind: "codex", Task: "approve the release"})
	got := h.waitState(a.ID, wire.StateApproval)
	if got.Attention.Detail != "git push origin main" || got.SessionID == "" {
		t.Fatalf("approval %+v", got)
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Always}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitKeys(a.ID, "a")
	h.waitState(a.ID, wire.StateDone)
	// A late tool event of the ended turn does not undo done.
	h.reg.Hook(wire.HookParams{Source: "codex", Event: "PreToolUse", Payload: mustJSON(map[string]any{"session_id": got.SessionID, "turn_id": "turn-1", "cwd": h.project})})
	if g, _ := h.reg.Get(a.ID); g.State != wire.StateDone {
		t.Fatalf("late event: %s", g.State)
	}
	// Codex's notify ends a turn too.
	h.reg.Hook(wire.HookParams{Source: "codex", Event: "PreToolUse", Payload: mustJSON(map[string]any{"session_id": got.SessionID, "turn_id": "turn-2", "cwd": h.project})})
	h.waitState(a.ID, wire.StateWorking)
	h.reg.Hook(wire.HookParams{Source: "codex", Event: "notify", Payload: mustJSON(map[string]any{"type": "agent-turn-complete", "thread-id": got.SessionID, "turn-id": "turn-2", "cwd": h.project, "last-assistant-message": "All green."})})
	if g := h.waitState(a.ID, wire.StateDone); g.Summary != "All green." {
		t.Fatalf("summary %q", g.Summary)
	}
}

func TestCodexApprovalFallbackFromScreen(t *testing.T) {
	h := newHarness(t, "FAKE_CODEX_NOHOOKS=1")
	a := h.spawn(wire.SpawnParams{Kind: "codex", Task: "approve it"})
	got := h.waitState(a.ID, wire.StateApproval)
	if got.Attention.Title != "Command" || got.Attention.Detail != "git push origin main" {
		t.Fatalf("attention %+v", got.Attention)
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitKeys(a.ID, "y")
	h.waitScreen(a.ID, "turn done") // the overlay is gone: the fallback's approval ends
	h.waitState(a.ID, wire.StateWorking)
}

func TestStopResumeWithSession(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(a.ID, wire.StateDone)
	if err := h.call("agents.stop", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	ex := h.waitState(a.ID, wire.StateExited)
	if ex.Exit == nil || ex.Exit.Signal != "SIGHUP" {
		t.Fatalf("exit %+v", ex.Exit)
	}
	// Attaching to it: the last screen, then EXIT.
	conn, err := wire.Attach(context.Background(), h.sock, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRO})
	if err != nil {
		t.Fatal(err)
	}
	if typ, _, _ := conn.ReadFrame(); typ != wire.FrameData {
		t.Fatal("no redraw")
	}
	if typ, _, _ := conn.ReadFrame(); typ != wire.FrameExit {
		t.Fatal("no EXIT")
	}
	conn.Close()
	// Removing a running agent is refused; resume brings it back.
	var r wire.Agent
	if err := h.call("agents.resume", wire.IDParams{ID: a.ID}, &r); err != nil {
		t.Fatal(err)
	}
	if r.State != wire.StateStarting || r.Exit != nil {
		t.Fatalf("resumed %+v", r)
	}
	h.waitState(a.ID, wire.StateIdle)
	argvs := h.argvs(a.ID)
	last := argvs[len(argvs)-1]
	if strings.Join(last[len(last)-2:], " ") != "--resume "+a.SessionID {
		t.Fatalf("resume argv %q", last)
	}
	if err := h.call("agents.remove", wire.IDParams{ID: a.ID}, nil); err == nil {
		t.Fatal("removed a running agent")
	}
	h.call("agents.stop", wire.IDParams{ID: a.ID}, nil)
	h.waitState(a.ID, wire.StateExited)
	if err := h.call("agents.remove", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.reg.Get(a.ID); err == nil {
		t.Fatal("still listed")
	}
}

func TestExitStates(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "job"})
	h.waitState(a.ID, wire.StateDone)
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "crash\r"}, nil)
	e := h.waitState(a.ID, wire.StateError)
	if e.Attention == nil || !strings.HasPrefix(e.Attention.Detail, "exit status 2") {
		t.Fatalf("error %+v", e.Attention)
	}
	b := h.spawn(wire.SpawnParams{Task: "job"})
	h.waitState(b.ID, wire.StateDone)
	h.call("agents.input", wire.InputParams{ID: b.ID, Text: "exit", Submit: true}, nil)
	if x := h.waitState(b.ID, wire.StateExited); *x.Exit.Code != 0 {
		t.Fatalf("exit %+v", x.Exit)
	}
}

func TestDaemonRestartRespawnsWithResume(t *testing.T) {
	h := newHarness(t)
	claude := h.spawn(wire.SpawnParams{Task: "long job"})
	shell := h.spawn(wire.SpawnParams{Kind: "shell"})
	stopped := h.spawn(wire.SpawnParams{Task: "stopped job"})
	h.waitState(claude.ID, wire.StateDone)
	h.waitState(shell.ID, wire.StateIdle)
	h.waitState(stopped.ID, wire.StateDone)
	h.call("agents.stop", wire.IDParams{ID: stopped.ID}, nil)
	h.waitState(stopped.ID, wire.StateExited)
	h.call("agents.rename", wire.RenameParams{ID: claude.ID, Name: "renamed"}, nil)
	h.close()
	data, err := os.ReadFile(filepath.Join(h.state, "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(h.state, "agents.json")); st.Mode().Perm() != 0o600 {
		t.Fatalf("agents.json mode %v", st.Mode())
	}
	if !strings.Contains(string(data), `"running": true`) {
		t.Fatalf("agents.json %s", data)
	}
	h.open()
	if a, _ := h.reg.Get(claude.ID); a.State != wire.StateStarting || a.Name != "renamed" {
		t.Fatalf("respawned %+v", a)
	}
	h.waitState(claude.ID, wire.StateIdle)
	argvs := h.argvs(claude.ID)
	if last := argvs[len(argvs)-1]; strings.Join(last[len(last)-2:], " ") != "--resume "+claude.SessionID {
		t.Fatalf("respawn argv %q", last)
	}
	h.waitState(shell.ID, wire.StateIdle)
	if a, _ := h.reg.Get(stopped.ID); a.State != wire.StateExited || len(h.argvs(stopped.ID)) != 1 {
		t.Fatalf("a stopped agent came back: %+v", a)
	}
}

func TestDeliverTaskAfterTrustQuestion(t *testing.T) {
	h := newHarness(t, "FAKE_TRUST_ASK=1")
	os.Remove(h.opt.ClaudeConfig) // not trusted
	a := h.spawn(wire.SpawnParams{Task: "the first prompt"})
	h.waitScreen(a.ID, "trust the files")
	if argv := h.argvs(a.ID)[0]; argv[len(argv)-1] == a.Task {
		t.Fatalf("task on the command line of an untrusted folder: %q", argv)
	}
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(h.screen(a.ID), "the first prompt") {
		t.Fatal("typed into the trust question")
	}
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "1"}, nil)
	waitFor(t, func() bool { g, _ := h.reg.Get(a.ID); return g.Summary == "Done: the first prompt" })
}

func TestHookResolution(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "job"})
	h.waitState(a.ID, wire.StateDone)
	// A nested claude in the agent's environment (another session) is not
	// the agent.
	h.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: "UserPromptSubmit", Payload: mustJSON(map[string]any{"session_id": "other"})})
	if g, _ := h.reg.Get(a.ID); g.State != wire.StateDone {
		t.Fatalf("nested session moved the agent to %s", g.State)
	}
	// /clear: a new session of the agent itself.
	h.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: "SessionStart", Payload: mustJSON(map[string]any{"session_id": "cleared", "source": "clear"})})
	if g, _ := h.reg.Get(a.ID); g.SessionID != "cleared" || g.State != wire.StateIdle {
		t.Fatalf("after clear %+v", g)
	}
	// Unknown agents and other kinds are ignored.
	if err := h.reg.Hook(wire.HookParams{Agent: "L/zzzzzz", Source: "claude", Event: "Stop"}); err != nil {
		t.Fatal(err)
	}
	h.reg.Hook(wire.HookParams{Agent: a.ID, Source: "codex", Event: "UserPromptSubmit", Payload: mustJSON(map[string]any{})})
	if g, _ := h.reg.Get(a.ID); g.State != wire.StateIdle {
		t.Fatalf("a codex hook moved a claude agent to %s", g.State)
	}
	// AskUserQuestion is a question; elicitation too; StopFailure an error.
	h.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: "PreToolUse", Payload: mustJSON(map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{map[string]any{"question": "Which DB?"}}}})})
	if g, _ := h.reg.Get(a.ID); g.State != wire.StateQuestion || g.Attention.Detail != "Which DB?" {
		t.Fatalf("question %+v", g)
	}
	h.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: "PreToolUse", Payload: mustJSON(map[string]any{"tool_name": "Bash"})})
	if g, _ := h.reg.Get(a.ID); g.State != wire.StateQuestion {
		t.Fatal("a concurrent tool event cleared the question")
	}
	h.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: "PostToolUse", Payload: mustJSON(map[string]any{"tool_name": "AskUserQuestion"})})
	h.waitState(a.ID, wire.StateWorking)
	h.reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: "StopFailure", Payload: mustJSON(map[string]any{"error": "rate_limit"})})
	if g, _ := h.reg.Get(a.ID); g.State != wire.StateError || g.Attention.Kind != "error" {
		t.Fatalf("error %+v", g)
	}
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func TestControlMethods(t *testing.T) {
	h := newHarness(t)
	var hello wire.HelloResult
	if err := h.call("hello", wire.HelloParams{Client: "test", Version: "1"}, &hello); err != nil || hello.Machine != "L" || hello.Daemon != "hesperd" || len(hello.Machines) != 1 || hello.Machines[0].Route != "local" {
		t.Fatalf("hello %+v %v", hello, err)
	}
	var profiles wire.ProfilesResult
	if err := h.call("profiles.list", nil, &profiles); err != nil || profiles.Profiles["claude-auto-rc"].Kind != "claude" || profiles.Defaults.Kinds["codex"] != "fake-codex" {
		t.Fatalf("profiles %+v %v", profiles, err)
	}
	a := h.spawn(wire.SpawnParams{Kind: "shell", Name: "my shell"})
	if a.Name != "my shell" || a.State != wire.StateIdle {
		t.Fatalf("shell %+v", a)
	}
	var list []wire.Agent
	if err := h.call("agents.list", nil, &list); err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	var recent []wire.Project
	if err := h.call("projects.recent", nil, &recent); err != nil || len(recent) != 1 || recent[0].Path != h.project {
		t.Fatalf("recent %+v %v", recent, err)
	}
	var renamed wire.Agent
	if err := h.call("agents.rename", wire.RenameParams{ID: a.ID, Name: "build"}, &renamed); err != nil || renamed.Name != "build" {
		t.Fatalf("rename %+v %v", renamed, err)
	}
	check := func(method string, params any, code string) {
		t.Helper()
		err := h.call(method, params, nil)
		if we, ok := err.(*wire.Error); !ok || we.Code != code {
			t.Fatalf("%s: %v, want %s", method, err, code)
		}
	}
	check("agents.stop", wire.IDParams{ID: "L/nope00"}, wire.CodeNotFound)
	check("agents.stop", wire.IDParams{ID: "M/abcdef"}, wire.CodeUnavailable)
	check("agents.move", wire.MoveParams{ID: a.ID, To: "M"}, wire.CodeUnavailable)
	check("agents.spawn", wire.SpawnParams{Machine: "M", Project: h.project}, wire.CodeUnavailable)
	check("agents.spawn", wire.SpawnParams{Project: "/does/not/exist"}, wire.CodeNotFound)
	check("agents.spawn", wire.SpawnParams{Project: h.project, Profile: "nope"}, wire.CodeNotFound)
	check("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, wire.CodeInvalid)
	check("agents.resume", wire.IDParams{ID: a.ID}, wire.CodeExists)
	check("nope", nil, wire.CodeNotFound)
	// Attach errors.
	if _, err := wire.Attach(context.Background(), h.sock, wire.AttachRequest{Attach: "L/nope00", Mode: "ro"}); err == nil || err.(*wire.Error).Code != wire.CodeNotFound {
		t.Fatalf("attach unknown: %v", err)
	}
	if _, err := wire.Attach(context.Background(), h.sock, wire.AttachRequest{Attach: "M/abcdef", Mode: "ro"}); err == nil || err.(*wire.Error).Code != wire.CodeUnavailable {
		t.Fatalf("attach remote: %v", err)
	}
}

func TestSocketPermissionsAndPeer(t *testing.T) {
	h := newHarness(t)
	st, _ := os.Stat(h.sock)
	if st.Mode().Perm() != 0o600 || st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode %v", st.Mode())
	}
	dst, _ := os.Stat(h.state)
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dst.Mode())
	}
	// Our own uid passes the peer check.
	if err := h.call("hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Another uid is cut off before anything is read.
	old := peerCheck.Load()
	other := func(net.Conn) (int, error) { return os.Getuid() + 1, nil }
	peerCheck.Store(&other)
	t.Cleanup(func() { peerCheck.Store(old) })
	conn, err := net.Dial("unix", h.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"hello"}` + "\n"))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if line, err := bufio.NewReader(conn).ReadString('\n'); err == nil {
		t.Fatalf("answered another user: %s", line)
	}
	// A second daemon on a live socket is refused; a stale socket replaced.
	if _, err := Listen(h.sock); err == nil {
		t.Fatal("listened twice")
	}
}

func TestWorktreeSpawn(t *testing.T) {
	h := newHarness(t)
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", h.project}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "--no-gpg-sign", "-m", "init")
	a := h.spawn(wire.SpawnParams{Kind: "shell", Task: "Add rate limiting!", Worktree: json.RawMessage("true")})
	wantPath := filepath.Join(h.opt.WorktreeRoot, "proj", "add-rate-limiting")
	if a.Worktree != wantPath || a.Branch != "worktree/add-rate-limiting" {
		t.Fatalf("worktree %q branch %q", a.Worktree, a.Branch)
	}
	if _, err := os.Stat(filepath.Join(wantPath, ".git")); err != nil {
		t.Fatal(err)
	}
	b := h.spawn(wire.SpawnParams{Kind: "shell", Task: "Add rate limiting!", Branch: "feature/rl"})
	if b.Worktree != wantPath+"-2" || b.Branch != "feature/rl" {
		t.Fatalf("second worktree %q branch %q", b.Worktree, b.Branch)
	}
	// The agent runs in its worktree.
	h.waitScreen(b.ID, "$")
	h.call("agents.input", wire.InputParams{ID: b.ID, Text: "pwd", Submit: true}, nil)
	h.waitScreen(b.ID, "ran pwd")
}
