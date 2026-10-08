package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// realClaude: the fake Claude decides trust from the harness's
// ~/.claude.json and keeps transcripts like Claude Code 2.1 (resume of an
// unsaved session exits 1).
func realClaude(t *testing.T, extra ...string) *harness {
	return newHarness(t, append([]string{"FAKE_CLAUDE_JSON={dir}/claude.json", "FAKE_CLAUDE_HOME={dir}/claudehome"}, extra...)...)
}

// setSettings changes settings.json and reloads it.
func setSettings(t *testing.T, h *harness, extra map[string]any) {
	s := map[string]any{"defaults": map[string]any{"kind": "claude", "kinds": map[string]string{"claude": "fake-claude", "codex": "fake-codex", "shell": "fake-shell"}}}
	for k, v := range extra {
		s[k] = v
	}
	writeJSON(t, filepath.Join(h.config, "settings.json"), s)
	loaded, err := loadSettings(h.config)
	if err != nil {
		t.Fatal(err)
	}
	h.reg.mu.Lock()
	h.reg.settings = loaded
	h.reg.mu.Unlock()
}

func TestPretrustClaudeProject(t *testing.T) {
	h := realClaude(t)
	// Claude Code has run here, but never in this project.
	os.WriteFile(h.opt.ClaudeConfig, []byte(`{
  "numStartups": 12345678901234567890,
  "theme": "dark",
  "tipsHistory": {"a<b>&c": 1.50},
  "projects": {
    "/elsewhere": {
      "allowedTools": [],
      "hasTrustDialogAccepted": true
    }
  },
  "oauthAccount": {"emailAddress": "x@y"}
}
`), 0o600)
	a := h.spawn(wire.SpawnParams{Task: "trusted at once"})
	if d := h.waitState(a.ID, wire.StateDone); d.Summary != "Done: trusted at once" {
		t.Fatalf("%+v", d)
	}
	if argv := h.argvs(a.ID)[0]; argv[len(argv)-1] != "trusted at once" {
		t.Fatalf("the task was not on the command line: %q", argv)
	}
	data, _ := os.ReadFile(h.opt.ClaudeConfig)
	text := string(data)
	for _, keep := range []string{`"numStartups": 12345678901234567890`, `"a<b>&c": 1.50`, `"/elsewhere"`, `"emailAddress": "x@y"`} {
		if !strings.Contains(text, keep) {
			t.Fatalf("lost %s:\n%s", keep, text)
		}
	}
	if strings.Index(text, `"theme"`) > strings.Index(text, `"projects"`) || !strings.HasSuffix(text, "}\n") {
		t.Fatalf("order or trailing newline changed:\n%s", text)
	}
	var c struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	json.Unmarshal(data, &c)
	entry := c.Projects[h.project]
	if entry["hasTrustDialogAccepted"] != true || entry["projectOnboardingSeenCount"] == nil {
		t.Fatalf("entry %v", entry)
	}
	if _, err := os.Stat(h.opt.ClaudeConfig + ".lock"); err == nil {
		t.Fatal("lock left behind")
	}
}

// The trial: trust off (or not recordable), Claude asks with its cursor on
// "No, exit". The daemon shows the question; answering trust goes on with
// the task.
func TestTrustQuestionAsAttention(t *testing.T) {
	h := realClaude(t)
	setSettings(t, h, map[string]any{"trustProjects": false})
	os.WriteFile(h.opt.ClaudeConfig, []byte("{}\n"), 0o600)
	a := h.spawn(wire.SpawnParams{Task: "after the question"})
	q := h.waitState(a.ID, wire.StateQuestion)
	if q.Attention == nil || q.Attention.Title != "Trust folder" || q.Attention.Detail != "Trust "+h.project+"?" ||
		!slices.Equal(q.Attention.Options, []string{"trust", "exit"}) {
		t.Fatalf("attention %+v", q.Attention)
	}
	if !strings.Contains(h.screen(a.ID), "Quick safety check: Is this a project you created or one you trust?") {
		t.Fatalf("screen %s", h.screen(a.ID))
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: "allow"}, nil); err == nil {
		t.Fatal("allow answered a trust question")
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Trust}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { g, _ := h.reg.Get(a.ID); return g.Summary == "Done: after the question" })
	if data, _ := os.ReadFile(h.opt.ClaudeConfig); string(data) != "{}\n" {
		t.Fatalf("trustProjects false wrote %s", data)
	}
}

// The trial's second half: the trust question exited Claude; resuming a
// session that was never saved must not --resume it (claude exits 1) but
// start fresh with the same session id and the task again.
func TestResumeOfUnsavedSessionStartsFresh(t *testing.T) {
	h := realClaude(t)
	setSettings(t, h, map[string]any{"trustProjects": false})
	os.WriteFile(h.opt.ClaudeConfig, []byte("{}\n"), 0o600)
	a := h.spawn(wire.SpawnParams{Task: "do it again"})
	h.waitState(a.ID, wire.StateQuestion)
	// An arrow key and Enter: "No, exit" stays chosen; Claude exits 1.
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "\x1b[B\r"}, nil)
	e := h.waitState(a.ID, wire.StateError)
	if e.Attention == nil || !strings.HasPrefix(e.Attention.Detail, "exit status 1") {
		t.Fatalf("%+v", e.Attention)
	}
	var r wire.Agent
	if err := h.call("agents.resume", wire.IDParams{ID: a.ID}, &r); err != nil {
		t.Fatal(err)
	}
	argvs := h.argvs(a.ID)
	last := argvs[len(argvs)-1]
	// (Untrusted: the task is typed in after the question, not on the command line.)
	if slices.Contains(last, "--resume") || !slices.Contains(last, "--session-id") ||
		last[slices.Index(last, "--session-id")+1] != a.SessionID {
		t.Fatalf("respawn argv %q", last)
	}
	h.waitState(a.ID, wire.StateQuestion)
	h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Trust}, nil)
	waitFor(t, func() bool { g, _ := h.reg.Get(a.ID); return g.Summary == "Done: do it again" })
	// Now the session is saved: the next resume resumes it.
	h.call("agents.stop", wire.IDParams{ID: a.ID}, nil)
	h.waitState(a.ID, wire.StateExited)
	h.call("agents.resume", wire.IDParams{ID: a.ID}, &r)
	h.waitState(a.ID, wire.StateIdle)
	argvs = h.argvs(a.ID)
	if last := argvs[len(argvs)-1]; strings.Join(last[len(last)-2:], " ") != "--resume "+a.SessionID {
		t.Fatalf("resume argv %q", last)
	}
}

func TestTrustQuestionExit(t *testing.T) {
	h := realClaude(t)
	setSettings(t, h, map[string]any{"trustProjects": false})
	os.WriteFile(h.opt.ClaudeConfig, []byte("{}\n"), 0o600)
	a := h.spawn(wire.SpawnParams{Task: "never mind"})
	h.waitState(a.ID, wire.StateQuestion)
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Leave}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitState(a.ID, wire.StateExited)
}

// A resume that dies at once tells why: the agent's last lines.
func TestQuickExitShowsTheScreen(t *testing.T) {
	h := realClaude(t)
	a := h.spawn(wire.SpawnParams{Task: "first"})
	h.waitState(a.ID, wire.StateDone)
	h.call("agents.stop", wire.IDParams{ID: a.ID}, nil)
	h.waitState(a.ID, wire.StateExited)
	// The transcript is gone (another Mac, cleaned up), the hooks saw it.
	matches, _ := filepath.Glob(filepath.Join(h.reg.opt.ClaudeHome, "projects", "*", a.SessionID+".jsonl"))
	for _, m := range matches {
		os.Remove(m)
	}
	h.call("agents.resume", wire.IDParams{ID: a.ID}, nil)
	e := h.waitState(a.ID, wire.StateError)
	if e.Attention == nil || e.Attention.Detail != "exit status 1: No conversation found with session ID: "+a.SessionID {
		t.Fatalf("detail %+v", e.Attention)
	}
}

func TestFirstRunSetupScreen(t *testing.T) {
	h := newHarness(t, "FAKE_FIRST_RUN=theme")
	a := h.spawn(wire.SpawnParams{Task: "after setup"})
	q := h.waitState(a.ID, wire.StateQuestion)
	if q.Attention.Title != "Claude setup" || len(q.Attention.Options) != 0 || !strings.Contains(q.Attention.Detail, "open the agent") {
		t.Fatalf("attention %+v", q.Attention)
	}
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "\r"}, nil)
	waitFor(t, func() bool { g, _ := h.reg.Get(a.ID); return g.Summary == "Done: after setup" })
}

func TestAgentEnvironmentIsClean(t *testing.T) {
	h := newHarness(t, "FAKE_PRINT_ENV=1", "CLAUDECODE=1", "CLAUDE_CODE_CHILD_SESSION=1", "CLAUDE_CODE_ENTRYPOINT=cli",
		"CLAUDE_CODE_SESSION_ID=abc", "CLAUDE_PROJECT_DIR=/x", "CODEX_SANDBOX=seatbelt", "CODEX_THREAD_ID=t1",
		"CODEX_SANDBOX_NETWORK_DISABLED=1", "TMUX=/tmp/tmux,1,0", "TMUX_PANE=%3", "GHOSTTY_SURFACE_ID=7",
		"TERM_PROGRAM=ghostty", "TERM=xterm-ghostty", "GIT_EDITOR=true", "CODEX_HOME=/keep/codex",
		"CLAUDE_CODE_USE_BEDROCK=1", "SSH_AUTH_SOCK=/keep/agent", "LANG=de_DE.UTF-8")
	a := h.spawn(wire.SpawnParams{Task: "env"})
	h.waitState(a.ID, wire.StateDone)
	data, err := os.ReadFile(filepath.Join(h.logs, strings.ReplaceAll(a.ID, "/", "_")+".env"))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, kv := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for _, gone := range []string{"CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID",
		"CLAUDE_PROJECT_DIR", "CODEX_SANDBOX", "CODEX_THREAD_ID", "CODEX_SANDBOX_NETWORK_DISABLED", "TMUX", "TMUX_PANE",
		"GHOSTTY_SURFACE_ID", "GIT_EDITOR"} {
		if v, ok := env[gone]; ok {
			t.Errorf("%s=%s reached the agent", gone, v)
		}
	}
	want := map[string]string{"CODEX_HOME": "/keep/codex", "CLAUDE_CODE_USE_BEDROCK": "1", "SSH_AUTH_SOCK": "/keep/agent",
		"LANG": "de_DE.UTF-8", "TERM": "xterm-256color", "COLORTERM": "truecolor", "TERM_PROGRAM": "hesperd", "HESPER_AGENT_ID": a.ID}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s=%q, want %q", k, env[k], v)
		}
	}
}

func TestLoginEnvStartsClean(t *testing.T) {
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("TMUX", "/tmp/x")
	t.Setenv("HESPER_TEST_KEEP_PATHISH", "no")
	t.Setenv("LC_ALL", "C")
	env := LoginEnv("/bin/sh", 10*time.Second)
	has := func(k string) bool { return envValue(env, k) != "" }
	if has("CLAUDE_CODE_CHILD_SESSION") || has("TMUX") || has("HESPER_TEST_KEEP_PATHISH") {
		t.Fatalf("inherited markers: %q", env)
	}
	if !has("HOME") || !has("PATH") || envValue(env, "LC_ALL") != "C" {
		t.Fatalf("lost the basics: %q", env)
	}
}

func TestCodexAgentGetsSessionHooks(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(h.opt.CodexHome, 0o700)
	os.WriteFile(filepath.Join(h.opt.CodexHome, "config.toml"), []byte("notify = [\"/usr/bin/say\", \"done\"]\nmodel = \"o3\"\n"), 0o600)
	h.reg.opt.HookBin = "/opt/hesperd"
	a := h.spawn(wire.SpawnParams{Kind: "codex", Task: "hooked"})
	h.waitState(a.ID, wire.StateDone)
	argv := h.argvs(a.ID)[0]
	joined := strings.Join(argv, "\n")
	for _, want := range []string{"--no-daemon", "features.hooks=true", `hooks.Stop=[{hooks=[{async=true,command="\"/opt/hesperd\" hook codex Stop",timeout=5,type="command"}]}]`,
		`notify=["/opt/hesperd", "hook", "codex", "notify", "--then", "/usr/bin/say", "done"]`, `"/<session-flags>/config.toml:stop:0:0"={trusted_hash="sha256:`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv lacks %s:\n%s", want, joined)
		}
	}
	if argv[len(argv)-1] != "hooked" {
		t.Fatalf("the task is not last: %q", argv)
	}
	// The project is trusted in Codex's config, the rest untouched.
	data, _ := os.ReadFile(filepath.Join(h.opt.CodexHome, "config.toml"))
	want := "notify = [\"/usr/bin/say\", \"done\"]\nmodel = \"o3\"\n\n[projects." + tomlString(h.project) + "]\ntrust_level = \"trusted\"\n"
	if string(data) != want {
		t.Fatalf("config.toml:\n%s\nwant:\n%s", data, want)
	}
	// Hooks installed in the Codex home: no -c hooks (they would run twice).
	os.WriteFile(filepath.Join(h.opt.CodexHome, "hooks.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"\"/x/hesperd\" hook codex Stop"}]}]}}`), 0o600)
	b := h.spawn(wire.SpawnParams{Kind: "codex", Task: "installed"})
	h.waitState(b.ID, wire.StateDone)
	if j := strings.Join(h.argvs(b.ID)[0], "\n"); strings.Contains(j, "features.hooks") || !strings.Contains(j, "notify=") {
		t.Fatalf("argv with installed hooks:\n%s", j)
	}
}
