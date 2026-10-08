package agents

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/ptyhost"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Fixes from the Mac mini: after a daemon restart for an update a done
// Claude agent ended "exited" (status 0) instead of idle, and
// `hesperctl resume` failed with `exec: "claude": executable file not
// found in $PATH` while Claude Code's npm auto-updater was reinstalling it.

// fakeLoginShell writes a login shell whose profile sets PATH (first
// entry with spaces, as Android Studio's on the mini) after sleeping
// sleep seconds (only while the file slowOnce exists, if given).
func fakeLoginShell(t *testing.T, path, slowOnce string, sleep int) string {
	t.Helper()
	dir := t.TempDir()
	sh := filepath.Join(dir, "zsh")
	slow := ""
	if sleep > 0 {
		slow = "sleep " + itoa(sleep) + "\n"
		if slowOnce != "" {
			slow = "if [ -e '" + slowOnce + "' ]; then rm -f '" + slowOnce + "'; sleep " + itoa(sleep) + "; fi\n"
		}
	}
	script := "#!/bin/sh\n# a login shell: -l -c CMD\n" + slow +
		"PATH='" + path + "'; export PATH\nFROM_PROFILE='a b'; export FROM_PROFILE\nshift; shift\nexec /bin/sh -c \"$1\"\n"
	if err := os.WriteFile(sh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return sh
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func pathList(env []string) []string { return strings.Split(envValue(env, "PATH"), ":") }

func TestLoginEnvPathWithSpaces(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "My Tools (x86)", "bin")
	os.MkdirAll(bin, 0o755)
	exe, _ := os.Executable()
	os.Symlink(exe, filepath.Join(bin, "fakeclaude"))
	first := "/Applications/Android Studio.app/Contents/jbr/Contents/Home/bin"
	sh := fakeLoginShell(t, first+":"+bin+":relative/dir::/usr/bin:/bin:/usr/bin", "", 0)
	t.Setenv("PATH", "/daemon/only/bin:/usr/bin:/bin")
	t.Setenv("HOME", "/Users/someone")
	env, source := ResolveEnv(sh, 5*time.Second, 2)
	if !strings.HasPrefix(source, "login shell") {
		t.Fatalf("source %q", source)
	}
	got := pathList(env)
	want := []string{first, bin, "/usr/bin", "/bin", "/daemon/only/bin", "/Users/someone/.local/bin", "/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/sbin", "/sbin"}
	if !slices.Equal(got, want) {
		t.Fatalf("PATH\n got %q\nwant %q", got, want)
	}
	if envValue(env, "FROM_PROFILE") != "a b" {
		t.Fatalf("profile's variable lost: %q", env)
	}
	if n := strings.Count(strings.Join(env, "\n"), "\nPATH="); n > 1 {
		t.Fatalf("%d PATHs", n)
	}
	// The agent's command is found there, not on the daemon's PATH.
	if p, err := ptyhost.LookPath("fakeclaude", env); err != nil || p != filepath.Join(bin, "fakeclaude") {
		t.Fatalf("LookPath %q %v", p, err)
	}
}

// A login shell slower than the timeout: tried again, then the daemon's
// own environment with a completed PATH, never an empty one.
func TestSlowLoginShell(t *testing.T) {
	t.Setenv("PATH", "/daemon/only/bin:/usr/bin:/bin")
	t.Setenv("HOME", "/Users/someone")
	sh := fakeLoginShell(t, "/from/profile:/usr/bin:/bin", "", 5)
	start := time.Now()
	env, source := ResolveEnv(sh, 300*time.Millisecond, 2)
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("took %v: the slow shell was waited for", took)
	}
	if !strings.Contains(source, "daemon's own environment") || !strings.Contains(source, "timed out") {
		t.Fatalf("source %q", source)
	}
	got := pathList(env)
	for _, d := range []string{"/daemon/only/bin", "/Users/someone/.local/bin", "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"} {
		if !slices.Contains(got, d) {
			t.Fatalf("PATH %q lacks %s", got, d)
		}
	}
	if got[0] != "/daemon/only/bin" {
		t.Fatalf("PATH %q", got)
	}
	// Slow only the first time (a busy Mac at login): the retry has it.
	once := filepath.Join(t.TempDir(), "slow")
	os.WriteFile(once, nil, 0o600)
	sh = fakeLoginShell(t, "/from/profile:/usr/bin:/bin", once, 5)
	env, source = ResolveEnv(sh, 500*time.Millisecond, 2)
	if !strings.HasPrefix(source, "login shell") || pathList(env)[0] != "/from/profile" {
		t.Fatalf("retry: %q %q", source, envValue(env, "PATH"))
	}
}

// A profile that prints, fails at the end, or leaves no PATH.
func TestLoginShellOutputs(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "sh"+itoa(len(body)))
		os.WriteFile(p, []byte("#!/bin/sh\nshift; shift\n"+body), 0o755)
		return p
	}
	noisy := write("echo 'Welcome!  PATH=/bogus'\n/bin/sh -c \"$1\"\nexit 3\n")
	if env, err := probeLogin(noisy, 5*time.Second); err != nil || envValue(env, "PATH") == "/bogus" {
		t.Fatalf("noisy: %v %q", err, envValue(env, "PATH"))
	}
	if _, err := probeLogin(write("echo nothing\n"), 5*time.Second); err == nil || !strings.Contains(err.Error(), "printed no environment") {
		t.Fatalf("no marker: %v", err)
	}
	if _, err := probeLogin(filepath.Join(dir, "missing"), 5*time.Second); err == nil {
		t.Fatal("a missing shell worked")
	}
}

// pathHarness: a harness whose "path-claude" profile runs the fake by a
// bare name found only on the agents' PATH (a directory with spaces), not
// the daemon's.
func pathHarness(t *testing.T, env ...string) (*harness, string) {
	t.Helper()
	dir, _ := os.MkdirTemp("/tmp", "gdbin")
	t.Cleanup(func() { os.RemoveAll(dir) })
	bin := filepath.Join(dir, "Android Studio.app", "bin")
	os.MkdirAll(bin, 0o755)
	exe, _ := os.Executable()
	cmd := filepath.Join(bin, "fakeclaude")
	if err := os.Symlink(exe, cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("fakeclaude"); err == nil {
		t.Fatal("fakeclaude is on the daemon's PATH")
	}
	h := newHarness(t, append(env, "PATH="+bin+":"+os.Getenv("PATH"))...)
	h.call("agents.list", nil, nil) // serving: close stops it
	h.close()
	profiles := map[string]wire.Profile{
		"fake-claude": {Kind: wire.KindClaude, Argv: []string{"fakeclaude", "claude", "--permission-mode", "auto", "--remote-control", "{name}"}},
		"fake-codex":  {Kind: wire.KindCodex, Argv: []string{exe, "codex"}},
		"fake-shell":  {Kind: wire.KindShell, Argv: []string{exe, "shell"}},
		"gone":        {Kind: wire.KindClaude, Argv: []string{"no-such-agent"}},
	}
	writeJSON(t, filepath.Join(h.config, "profiles.json"), profiles)
	h.opt.CommandWait = 600 * time.Millisecond
	h.open()
	return h, cmd
}

func TestSpawnFindsCommandOnAgentPath(t *testing.T) {
	h, _ := pathHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "job"})
	h.waitState(a.ID, wire.StateDone)
	if argv := lastArgv(t, h, a.ID); argv[0] != "claude" {
		t.Fatalf("argv %q", argv)
	}
	// Not there: a plain error, not exec's "in $PATH".
	var got wire.Agent
	err := h.call("agents.spawn", wire.SpawnParams{Project: h.project, Profile: "gone"}, &got)
	if err == nil || !strings.Contains(err.Error(), `"no-such-agent" not found on the agent's PATH (`) {
		t.Fatalf("spawn of a missing command: %v", err)
	}
}

// The mini: a done agent, the daemon stopped (Claude ends with status 0
// on the hangup) and started again: it comes back with --resume and is
// idle, never "exited".
func TestRestartDoneAgentWithCleanHangupExit(t *testing.T) {
	h := newHarness(t, "FAKE_HUP_EXIT0=1")
	a := h.spawn(wire.SpawnParams{Task: "Reply OK"})
	h.waitState(a.ID, wire.StateDone)
	h.close()
	var f stateFile
	data, _ := os.ReadFile(filepath.Join(h.state, "agents.json"))
	json.Unmarshal(data, &f)
	if len(f.Agents) != 1 || !f.Agents[0].Running || f.Agents[0].State == wire.StateExited {
		t.Fatalf("saved %s", data)
	}
	if e := f.Agents[0].Exit; e == nil || e.Code == nil || *e.Code != 0 {
		t.Fatalf("the hangup did not end it with 0 (the test proves nothing): %s", data)
	}
	h.open()
	g := h.waitState(a.ID, wire.StateIdle)
	if g.Exit != nil || !slices.Contains(lastArgv(t, h, a.ID), "--resume") {
		t.Fatalf("respawned %+v %q", g, lastArgv(t, h, a.ID))
	}
}

// A respawn that ends at once with status 0 (what Claude did on the
// mini) is an error with what it said, and a later resume works.
func TestRespawnEndingAtOnceIsAnError(t *testing.T) {
	once := filepath.Join(t.TempDir(), "exit0")
	h := newHarness(t, "FAKE_HUP_EXIT0=1", "FAKE_RESUME_EXIT0_ONCE="+once)
	a := h.spawn(wire.SpawnParams{Task: "Reply OK"})
	h.waitState(a.ID, wire.StateDone)
	h.close()
	os.WriteFile(once, nil, 0o600)
	h.open()
	e := h.waitState(a.ID, wire.StateError)
	if e.Attention == nil || e.Attention.Title != "Not resumed" || !strings.Contains(e.Attention.Detail, "exit status 0") ||
		!strings.Contains(e.Attention.Detail, "Remote control session ended") {
		t.Fatalf("attention %+v", e.Attention)
	}
	if err := h.call("agents.resume", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitState(a.ID, wire.StateIdle)
	// A plain stop and resume of a fresh agent, and an agent the user
	// ends quickly, stay as they were: exited.
	h.call("agents.stop", wire.IDParams{ID: a.ID}, nil)
	h.waitState(a.ID, wire.StateExited)
}

// The command is gone at the daemon's start (npm reinstalling Claude
// Code): an error with the reason, saved to be respawned; it comes back
// by itself once the command is back.
func TestRespawnWaitsForMissingCommand(t *testing.T) {
	h, cmd := pathHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "job"})
	h.waitState(a.ID, wire.StateDone)
	h.close()
	os.Remove(cmd)
	h.opt.CommandWait = 5 * time.Second
	h.open()
	e := h.waitState(a.ID, wire.StateError)
	if e.Attention == nil || e.Attention.Title != "Not resumed" || !strings.Contains(e.Attention.Detail, `"fakeclaude" not found on the agent's PATH (`) ||
		!strings.Contains(e.Attention.Detail, "Android Studio.app/bin") {
		t.Fatalf("attention %+v", e.Attention)
	}
	waitFor(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(h.state, "agents.json"))
		return strings.Contains(string(data), `"respawn": true`)
	})
	exe, _ := os.Executable()
	os.Symlink(exe, cmd)
	h.waitState(a.ID, wire.StateIdle)
	if !slices.Contains(lastArgv(t, h, a.ID), "--resume") {
		t.Fatalf("argv %q", lastArgv(t, h, a.ID))
	}
}

// Still gone after the wait: the error stays (and is saved to be
// respawned at the next start); agents.resume brings it back once the
// command is there, and a resume while it is missing is an error with the
// reason, not "exited".
func TestResumeAfterRespawnFailure(t *testing.T) {
	h, cmd := pathHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "job"})
	h.waitState(a.ID, wire.StateDone)
	h.close()
	os.Remove(cmd)
	h.open()
	h.waitState(a.ID, wire.StateError)
	time.Sleep(h.opt.CommandWait + 300*time.Millisecond) // respawnLater gave up
	if g, _ := h.reg.Get(a.ID); g.State != wire.StateError {
		t.Fatalf("state %q", g.State)
	}
	// agents.resume while it is missing: an error, shown on the agent.
	err := h.call("agents.resume", wire.IDParams{ID: a.ID}, nil)
	if err == nil || !strings.Contains(err.Error(), "not found on the agent's PATH") {
		t.Fatalf("resume err %v", err)
	}
	if g, _ := h.reg.Get(a.ID); g.State != wire.StateError || g.Attention == nil || !strings.Contains(g.Attention.Detail, "fakeclaude") {
		t.Fatalf("after resume %+v", g)
	}
	// The next daemon start tries again (saved "respawn").
	h.close()
	exe, _ := os.Executable()
	os.Symlink(exe, cmd)
	h.open()
	h.waitState(a.ID, wire.StateIdle)

	// Gone at a resume, back within the wait: the resume waits for it.
	h.call("agents.stop", wire.IDParams{ID: a.ID}, nil)
	h.waitState(a.ID, wire.StateExited)
	os.Remove(cmd)
	go func() {
		time.Sleep(200 * time.Millisecond)
		os.Symlink(exe, cmd)
	}()
	if err := h.call("agents.resume", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitState(a.ID, wire.StateIdle)
}

// A stopped agent whose respawn had failed stays down.
func TestStopClearsRespawn(t *testing.T) {
	h, cmd := pathHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "job"})
	h.waitState(a.ID, wire.StateDone)
	h.close()
	os.Remove(cmd)
	h.open()
	h.waitState(a.ID, wire.StateError)
	h.call("agents.stop", wire.IDParams{ID: a.ID}, nil)
	h.close()
	exe, _ := os.Executable()
	os.Symlink(exe, cmd)
	h.open()
	time.Sleep(300 * time.Millisecond)
	if g, _ := h.reg.Get(a.ID); g.State != wire.StateError || len(h.argvs(a.ID)) != 1 {
		t.Fatalf("came back: %+v %d", g, len(h.argvs(a.ID)))
	}
}
