package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/vt"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

var bin string

// The tests run the built hesperd binary.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hesperd-bin-")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "hesperd")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type daemonT struct {
	reg     *agents.Registry
	sock    string
	project string
	dir     string
}

// startDaemon runs the registry and server in-process; the "claude"
// profile is cat in a PTY (never a real agent).
func startDaemon(t *testing.T) *daemonT {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gdd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	d := &daemonT{dir: dir, sock: filepath.Join(dir, "state", "hesperd.sock"), project: filepath.Join(dir, "proj")}
	config := filepath.Join(dir, "config")
	os.MkdirAll(d.project, 0o755)
	os.MkdirAll(config, 0o755)
	d.project, _ = filepath.EvalSymlinks(d.project)
	profiles, _ := json.Marshal(map[string]wire.Profile{"cat": {Kind: wire.KindClaude, Argv: []string{"/bin/sh", "-c", "exec cat"}}})
	os.WriteFile(filepath.Join(config, "profiles.json"), profiles, 0o600)
	os.WriteFile(filepath.Join(config, "settings.json"), []byte(`{"defaults":{"kinds":{"claude":"cat"}}}`), 0o600)
	trust, _ := json.Marshal(map[string]any{"projects": map[string]any{d.project: map[string]any{"hasTrustDialogAccepted": true}}})
	os.WriteFile(filepath.Join(dir, "claude.json"), trust, 0o600)
	d.reg, err = agents.Open(agents.Options{StateDir: filepath.Join(dir, "state"), ConfigDir: config, Socket: d.sock, Machine: "L",
		Env: os.Environ(), LoginShell: "/bin/sh", ClaudeConfig: filepath.Join(dir, "claude.json"), CodexHome: filepath.Join(dir, "codex"),
		StopGrace: time.Second, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := agents.Listen(d.sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := agents.NewServer(d.reg)
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); d.reg.Close() })
	return d
}

func (d *daemonT) spawn(t *testing.T) wire.Agent {
	t.Helper()
	a, err := d.reg.Spawn(wire.SpawnParams{Project: d.project, Task: "a task"})
	if err != nil {
		t.Fatal(err)
	}
	return a
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

func (d *daemonT) screen(id string) string {
	term, _ := d.reg.Term(id)
	var b strings.Builder
	term.WithScreen(func(s *vt.Screen) {
		_, h := s.Size()
		for y := range h {
			b.WriteString(s.Text(y) + "\n")
		}
	})
	return b.String()
}

func TestHookWhenDaemonDownExitsFast(t *testing.T) {
	run := func() (time.Duration, []byte, error) {
		cmd := exec.Command(bin, "hook", "claude", "Stop")
		cmd.Env = append(os.Environ(), "HESPER_SOCKET=/tmp/hesperd-test-nonexistent.sock", "HESPER_AGENT_ID=L/abcdef")
		cmd.Stdin = strings.NewReader(`{"session_id":"x","hook_event_name":"Stop"}`)
		start := time.Now()
		out, err := cmd.CombinedOutput()
		return time.Since(start), out, err
	}
	run() // the first run of a new binary pays macOS's first-launch check
	took, out, err := run()
	t.Logf("hook with the daemon down: %v", took)
	if err != nil || len(out) != 0 {
		t.Fatalf("hook: %v %q", err, out)
	}
	// Wall-clock of a process on a loaded machine says little: the bound
	// only catches a hook that waits out its stdin or delivery timeout.
	if took >= stdinTimeout {
		t.Fatalf("hook took %v", took)
	}
	// The point itself, without process start-up in the measure: a down
	// daemon fails the delivery at once, long before its timeout.
	start := time.Now()
	if err := wire.SendHook("/tmp/hesperd-test-nonexistent.sock", wire.HookParams{Agent: "L/abcdef", Source: "claude", Event: "Stop", Payload: []byte("{}")}, time.Minute); err == nil {
		t.Fatal("delivered to a daemon that is down")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("delivery to a down daemon took %v: it waits for the timeout", d)
	}
}

func TestHookDelivers(t *testing.T) {
	d := startDaemon(t)
	a := d.spawn(t)
	hook := func(stdin string, args ...string) {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"hook"}, args...)...)
		cmd.Env = append(os.Environ(), "HESPER_SOCKET="+d.sock, "HESPER_AGENT_ID="+a.ID)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
			t.Fatalf("hook %v: %v %q", args, err, out)
		}
	}
	hook(`{"session_id":"`+a.SessionID+`","prompt":"hi"}`, "claude", "UserPromptSubmit")
	if g, _ := d.reg.Get(a.ID); g.State != wire.StateWorking {
		t.Fatalf("state %s", g.State)
	}
	// The event from the payload when not given; a huge tool output is
	// dropped, not the event.
	big := strings.Repeat("x", 3<<20)
	hook(`{"hook_event_name":"PostToolUse","session_id":"`+a.SessionID+`","tool_name":"Bash","tool_response":"`+big+`"}`, "claude")
	hook(`{"session_id":"`+a.SessionID+`","last_assistant_message":"Done.\nAll tests pass."}`, "claude", "Stop")
	if g, _ := d.reg.Get(a.ID); g.State != wire.StateDone || g.Summary != "All tests pass." {
		t.Fatalf("after Stop %+v", g)
	}
}

func TestAttachBridge(t *testing.T) {
	d := startDaemon(t)
	a := d.spawn(t)
	eventually(t, "cat runs", func() bool { g, _ := d.reg.Get(a.ID); return g.PID > 0 })

	// The owner view in a 90x25 terminal sets the PTY's size.
	owner, _ := bridge(t, d, 90, 25, a.ID, "--owner")
	eventually(t, "owner size", func() bool { g, _ := d.reg.Get(a.ID); return g.Size == wire.Size{Cols: 90, Rows: 25} })
	owner.write("typed by owner\r")
	eventually(t, "input", func() bool { return strings.Contains(d.screen(a.ID), "typed by owner") })
	// SIGWINCH: the owner's new size.
	pty.Setsize(owner.master, &pty.Winsize{Cols: 100, Rows: 30})
	eventually(t, "resize", func() bool { g, _ := d.reg.Get(a.ID); return g.Size == wire.Size{Cols: 100, Rows: 30} })
	owner.waitOutput(t, "typed by owner")

	// A read-only view: redraw, follows SIZE, input refused.
	ro, roTTY := bridge(t, d, 40, 10, a.ID, "--ro")
	ro.waitOutput(t, "typed by owner")
	ro.write("from the tile\r")
	eventually(t, "ro follows size", func() bool {
		ws, err := unix.IoctlGetWinsize(int(roTTY.Fd()), unix.TIOCGWINSZ)
		return err == nil && ws.Col == 100 && ws.Row == 30
	})
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(d.screen(a.ID), "from the tile") {
		t.Fatal("read-only input reached the agent")
	}

	// The agent ends: both bridges get EXIT, exit 0 and restore the tty.
	d.reg.Stop(a.ID)
	for _, b := range []*bridged{owner, ro} {
		select {
		case err := <-b.done:
			if err != nil {
				t.Fatalf("bridge: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("bridge did not end")
		}
	}
	for _, b := range []*bridged{owner, ro} {
		// The master side reads the terminal's modes (the slave is
		// revoked with its session leader gone).
		tio, err := unix.IoctlGetTermios(int(b.master.Fd()), ioctlGetTermios)
		if err != nil {
			t.Fatal(err)
		}
		if tio.Lflag&unix.ICANON == 0 || tio.Lflag&unix.ECHO == 0 {
			t.Fatal("the terminal was left in raw mode")
		}
	}
}

func TestAttachViewBridge(t *testing.T) {
	d := startDaemon(t)
	a := d.spawn(t)
	eventually(t, "cat runs", func() bool { g, _ := d.reg.Get(a.ID); return g.PID > 0 })
	owner, _ := bridge(t, d, 80, 24, a.ID, "--owner")
	for i := range 30 {
		owner.write(fmt.Sprintf("row %02d\r", i))
	}
	eventually(t, "rows", func() bool { return strings.Contains(d.screen(a.ID), "row 29") })

	// A view in a 20x4 terminal: the last rows, its size kept, input refused.
	view, viewTTY := bridge(t, d, 20, 4, a.ID, "--view")
	view.waitOutput(t, "row 29")
	view.write("from the view\r")
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(d.screen(a.ID), "from the view") {
		t.Fatal("view input reached the agent")
	}
	if ws, _ := unix.IoctlGetWinsize(int(viewTTY.Fd()), unix.TIOCGWINSZ); ws.Col != 20 || ws.Row != 4 {
		t.Fatalf("view terminal resized to %dx%d", ws.Col, ws.Row)
	}
	if g, _ := d.reg.Get(a.ID); g.Size != (wire.Size{Cols: 80, Rows: 24}) {
		t.Fatalf("view changed the PTY size: %+v", g.Size)
	}
	view.mu.Lock()
	early := view.out.String()
	view.mu.Unlock()
	if strings.Contains(early, "row 10") {
		t.Fatal("view sent rows above its window")
	}
	d.reg.Stop(a.ID)
	select {
	case err := <-view.done:
		if err != nil {
			t.Fatalf("view bridge: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("view bridge did not end")
	}
}

type bridged struct {
	master *os.File
	mu     sync.Mutex
	out    bytes.Buffer
	done   chan error
}

func (b *bridged) write(s string) { b.master.Write([]byte(s)) }

func (b *bridged) waitOutput(t *testing.T, s string) {
	t.Helper()
	eventually(t, "bridge output "+s, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return bytes.Contains(b.out.Bytes(), []byte(s))
	})
}

// bridge runs `hesperd attach` in a terminal of cols x rows.
func bridge(t *testing.T, d *daemonT, cols, rows int, args ...string) (*bridged, *os.File) {
	t.Helper()
	master, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	pty.Setsize(master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	cmd := exec.Command(bin, append([]string{"attach"}, args...)...)
	cmd.Env = append(os.Environ(), "HESPER_SOCKET="+d.sock)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	b := &bridged{master: master, done: make(chan error, 1)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			b.mu.Lock()
			b.out.Write(buf[:n])
			b.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() { b.done <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		master.Close()
		tty.Close()
	})
	return b, tty
}

func TestServeAndStop(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "gds")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "state", "hesperd.sock")
	cmd := exec.Command(bin, "serve", "--state-dir", filepath.Join(dir, "state"), "--config-dir", filepath.Join(dir, "config"))
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh", "HESPER_MACHINE=T", "HOME="+dir)
	var logs bytes.Buffer
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	var c *wire.Client
	eventually(t, "socket", func() bool {
		c, err = wire.Dial(context.Background(), sock)
		return err == nil
	})
	var hello wire.HelloResult
	if err := c.Call(context.Background(), "hello", wire.HelloParams{Client: "test"}, &hello); err != nil || hello.Machine != "T" {
		t.Fatalf("hello %+v %v", hello, err)
	}
	c.Close()
	// A second daemon refuses to start on the live socket.
	second := exec.Command(bin, "serve", "--state-dir", filepath.Join(dir, "state"), "--config-dir", filepath.Join(dir, "config"))
	second.Env = cmd.Env
	if err := second.Run(); err == nil {
		t.Fatal("a second daemon started")
	}
	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("serve: %v\n%s", err, logs.String())
	}
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("socket left behind")
	}
}

func TestHooksPrintAndDryRun(t *testing.T) {
	out, err := exec.Command(bin, "hooks", "print", "--bin", "/opt/g/hesperd").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"\"/opt/g/hesperd\" hook claude PermissionRequest"`)) || !bytes.Contains(out, []byte(`"notify"`)) {
		t.Fatalf("%s", out)
	}
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	os.WriteFile(settings, []byte(`{"theme":"dark"}`), 0o600)
	out, err = exec.Command(bin, "hooks", "install", "--dry-run", "--bin", "/opt/g/hesperd", "--claude-settings", settings, "--codex-home", filepath.Join(dir, "codex")).Output()
	if err != nil {
		t.Fatal(err)
	}
	var files []agents.HookFile
	if err := json.Unmarshal(out, &files); err != nil || len(files) != 3 {
		t.Fatalf("%s %v", out, err)
	}
	if data, _ := os.ReadFile(settings); string(data) != `{"theme":"dark"}` {
		t.Fatal("a dry run wrote")
	}
	if _, err := os.Stat(filepath.Join(dir, "codex")); err == nil {
		t.Fatal("a dry run created files")
	}
}

// A tall tile: `attach --fit` sizes the PTY to the tile while nobody owns
// it; resizing the tile follows (debounced); an owner wins.
func TestAttachFitBridge(t *testing.T) {
	d := startDaemon(t)
	a := d.spawn(t)
	eventually(t, "cat runs", func() bool { g, _ := d.reg.Get(a.ID); return g.PID > 0 })
	_, tileTTY := bridge(t, d, 120, 70, a.ID, "--fit")
	sized := func(c, r int) func() bool {
		return func() bool { g, _ := d.reg.Get(a.ID); return g.Size == wire.Size{Cols: c, Rows: r} }
	}
	eventually(t, "PTY 120x70", sized(120, 70))
	pty.Setsize(tileTTY, &pty.Winsize{Cols: 130, Rows: 90})
	eventually(t, "PTY 130x90", sized(130, 90))
	bridge(t, d, 100, 30, a.ID, "--owner")
	eventually(t, "owner 100x30", sized(100, 30))
}

// Notify chaining: hesperd's notify calls the program that was there,
// with the payload, even with the daemon down.
func TestNotifyThenChains(t *testing.T) {
	out := filepath.Join(t.TempDir(), "chained")
	script := filepath.Join(t.TempDir(), "prev.sh")
	os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s|%s' \"$1\" \"$2\" > "+out+"\n"), 0o755)
	cmd := exec.Command(bin, "hook", "codex", "notify", "--then", script, "first-arg", `{"type":"agent-turn-complete"}`)
	cmd.Env = append(os.Environ(), "HESPER_SOCKET=/tmp/hesperd-test-nonexistent.sock")
	if o, err := cmd.CombinedOutput(); err != nil || len(o) != 0 {
		t.Fatalf("%v %q", err, o)
	}
	eventually(t, "the previous notify ran", func() bool {
		data, _ := os.ReadFile(out)
		return string(data) == `first-arg|{"type":"agent-turn-complete"}`
	})
}
