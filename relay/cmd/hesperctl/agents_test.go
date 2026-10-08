package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// daemon runs hesperd's registry and server on a socket in a temp
// directory; its "claude" profile is cat in a PTY (never a real agent).
func daemon(t *testing.T) (*agents.Registry, string, string) {
	t.Helper()
	return daemonWith(t, nil)
}

// daemonWith is daemon with more profiles.
func daemonWith(t *testing.T, extra map[string]wire.Profile) (*agents.Registry, string, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	project := filepath.Join(dir, "proj")
	config := filepath.Join(dir, "config")
	os.MkdirAll(project, 0o755)
	os.MkdirAll(config, 0o755)
	project, _ = filepath.EvalSymlinks(project)
	all := map[string]wire.Profile{
		"cat": {Kind: wire.KindClaude, Argv: []string{"/bin/sh", "-c", "exec cat"}},
	}
	for name, p := range extra {
		all[name] = p
	}
	profiles, _ := json.Marshal(all)
	os.WriteFile(filepath.Join(config, "profiles.json"), profiles, 0o600)
	os.WriteFile(filepath.Join(config, "settings.json"), []byte(`{"defaults":{"kinds":{"claude":"cat"}}}`), 0o600)
	trust, _ := json.Marshal(map[string]any{"projects": map[string]any{project: map[string]any{"hasTrustDialogAccepted": true}}})
	os.WriteFile(filepath.Join(dir, "claude.json"), trust, 0o600)
	sock := filepath.Join(dir, "state", "hesperd.sock")
	reg, err := agents.Open(agents.Options{StateDir: filepath.Join(dir, "state"), ConfigDir: config, Socket: sock, Machine: "L",
		Env: os.Environ(), LoginShell: "/bin/sh", ClaudeConfig: filepath.Join(dir, "claude.json"), CodexHome: filepath.Join(dir, "codex"),
		WorktreeRoot: filepath.Join(dir, "wt"), StopGrace: time.Second, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := agents.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := agents.NewServer(reg)
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); reg.Close() })
	return reg, sock, project
}

func screenOf(reg *agents.Registry, id string) string {
	term, err := reg.Term(id)
	if err != nil {
		return ""
	}
	var b strings.Builder
	term.WithScreen(func(s *vt.Screen) {
		_, h := s.Size()
		for y := range h {
			b.WriteString(s.Text(y) + "\n")
		}
	})
	return b.String()
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAgentCommands(t *testing.T) {
	reg, sock, project := daemon(t)
	ctx := context.Background()
	ctl := func(args ...string) error {
		return run(ctx, append([]string{args[0], "--daemon-socket", sock}, args[1:]...))
	}
	if err := ctl("new", "--project", project, "fix", "the", "bug"); err != nil {
		t.Fatal(err)
	}
	list := reg.List()
	if len(list) != 1 || list[0].Name != "fix-the-bug" || list[0].Kind != "claude" || list[0].Task != "fix the bug" {
		t.Fatalf("%+v", list)
	}
	a := list[0]
	_, local, _ := strings.Cut(a.ID, "/")
	if err := ctl("send", local, "hello there"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "echo", func() bool { return strings.Contains(screenOf(reg, a.ID), "hello there") })

	ask := func() {
		reg.Hook(wire.HookParams{Agent: a.ID, Source: "claude", Event: "PermissionRequest", Payload: json.RawMessage(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`)})
		if g, _ := reg.Get(a.ID); g.State != wire.StateApproval {
			t.Fatalf("state %s", g.State)
		}
	}
	ask()
	if err := ctl("approve", "fix-the-bug"); err != nil { // by name
		t.Fatal(err)
	}
	if g, _ := reg.Get(a.ID); g.State != wire.StateWorking {
		t.Fatalf("after approve %s", g.State)
	}
	ask()
	if err := ctl("deny", a.ID, "--message", "do this instead"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "feedback", func() bool { return strings.Contains(screenOf(reg, a.ID), "do this instead") })

	var buf bytes.Buffer
	printAgents(&buf, reg.List())
	if !strings.Contains(buf.String(), a.ID) || !strings.Contains(buf.String(), "working") {
		t.Fatalf("ls:\n%s", buf.String())
	}
	if err := ctl("ls"); err != nil {
		t.Fatal(err)
	}
	if err := ctl("rename", local, "better", "name"); err != nil {
		t.Fatal(err)
	}
	if err := ctl("stop", a.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "exited", func() bool { g, _ := reg.Get(a.ID); return g.State == wire.StateExited })
	if err := ctl("resume", a.ID); err != nil {
		t.Fatal(err)
	}
	if g, _ := reg.Get(a.ID); g.Exit != nil || g.Name != "better name" {
		t.Fatalf("resumed %+v", g)
	}
	err := ctl("mv", a.ID, "mini")
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.CodeUnavailable {
		t.Fatalf("mv: %v", err)
	}
	// move (mv is its alias): --to, the flags; without a machine a usage
	// error; checkpoint of a folder outside Git: none, no error.
	if err := ctl("move", a.ID, "--to", "mini", "--fork", "--interrupt", "--leave-processes"); !errors.As(err, &we) || we.Code != wire.CodeUnavailable {
		t.Fatalf("move: %v", err)
	}
	if err := ctl("move", a.ID); exitCode(err) != exitUsage {
		t.Fatalf("move without --to: %v", err)
	}
	if err := ctl("checkpoint", a.ID); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	ctl("stop", a.ID)
	eventually(t, "exited", func() bool { g, _ := reg.Get(a.ID); return g.State == wire.StateExited })
	if err := ctl("rm", a.ID); err != nil {
		t.Fatal(err)
	}
	if len(reg.List()) != 0 {
		t.Fatal("not removed")
	}
	if err := ctl("stop", "nope00"); !errors.As(err, &we) || we.Code != wire.CodeNotFound {
		t.Fatalf("unknown: %v", err)
	}
}

func TestAgentOrRelayCommand(t *testing.T) {
	cases := map[string]bool{
		"ls":                            true,
		"attach L/abc123":               true,
		"attach --ro L/abc123":          true,
		"approve L/abc123":              true,
		"approve --always abc123":       true,
		"approve --json abc123":         true,
		"self":                          true,
		"approve-device ABC123":         false,
		"approve --deny ABC123":         false,
		"approve --rights shell laptop": false,
		"login --name x":                false,
	}
	for args, want := range cases {
		if got := isAgentCommand(strings.Fields(args)); got != want {
			t.Errorf("%q: agent command %v", args, got)
		}
	}
}

func TestApproveFallsBackToDevices(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HESPER_STATE_DIR", dir) // never the user's host store
	// No daemon, no agent: a device code goes to the host's device list.
	err := run(context.Background(), []string{"approve", "--daemon-socket", filepath.Join(dir, "none.sock"), "ABC123"})
	if err == nil || errors.Is(err, errNoDaemon) || strings.Contains(err.Error(), "hesperd") {
		t.Fatalf("approve fell through to: %v", err)
	}
}
