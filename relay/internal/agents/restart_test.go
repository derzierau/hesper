package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Fixes from the live trial with Claude Code 2.1.290 and Codex 0.160.0.

// editRecord changes one saved agent in agents.json (the daemon is closed).
func editRecord(t *testing.T, h *harness, id string, edit func(rec map[string]any)) {
	t.Helper()
	path := filepath.Join(h.state, "agents.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	for _, r := range f["agents"].([]any) {
		if rec := r.(map[string]any); rec["id"] == id {
			edit(rec)
		}
	}
	data, _ = json.Marshal(f)
	os.WriteFile(path, data, 0o600)
}

func lastArgv(t *testing.T, h *harness, id string) []string {
	t.Helper()
	argvs := h.argvs(id)
	if len(argvs) == 0 {
		t.Fatalf("%s never started", id)
	}
	return argvs[len(argvs)-1]
}

// The trial: an agent that had finished, whose session was never saved
// (the old environment bug turned Claude's transcripts off; agents.json
// from before the "engaged" flag), came back after a daemon restart fresh
// and with its task again: it redid the work. Now it starts without a
// prompt, idle, with a note.
func TestDoneAgentIsNotGivenItsTaskAgain(t *testing.T) {
	h := realClaude(t)
	a := h.spawn(wire.SpawnParams{Task: "push the branch"})
	h.waitState(a.ID, wire.StateDone)
	h.close()
	data, _ := os.ReadFile(filepath.Join(h.state, "agents.json"))
	if !strings.Contains(string(data), `"engaged": true`) {
		t.Fatalf("engaged not saved: %s", data)
	}
	// As the trial's file: no confirmed session, no flag, no transcript.
	editRecord(t, h, a.ID, func(rec map[string]any) {
		delete(rec, "sessionSeen")
		delete(rec, "engaged")
		delete(rec, "summary")
	})
	matches, _ := filepath.Glob(filepath.Join(h.opt.ClaudeHome, "projects", "*", a.SessionID+".jsonl"))
	for _, m := range matches {
		os.Remove(m)
	}
	h.open()
	g := h.waitState(a.ID, wire.StateIdle)
	if g.Summary != NotResumedNote {
		t.Fatalf("summary %q", g.Summary)
	}
	argv := lastArgv(t, h, a.ID)
	if slices.Contains(argv, "push the branch") || slices.Contains(argv, "--resume") || !slices.Contains(argv, "--session-id") {
		t.Fatalf("respawn argv %q", argv)
	}
	if len(h.argvs(a.ID)) != 2 {
		t.Fatalf("argvs %q", h.argvs(a.ID))
	}
	// Nothing ran: still idle, the note kept, and agents.resume after a
	// stop does the same (the flag is saved).
	h.call("agents.stop", wire.IDParams{ID: a.ID}, nil)
	h.waitState(a.ID, wire.StateExited)
	os.RemoveAll(filepath.Join(h.opt.ClaudeHome, "projects"))
	h.call("agents.resume", wire.IDParams{ID: a.ID}, nil)
	if g := h.waitState(a.ID, wire.StateIdle); g.Summary != NotResumedNote || slices.Contains(lastArgv(t, h, a.ID), "push the branch") {
		t.Fatalf("resume %+v %q", g, lastArgv(t, h, a.ID))
	}
	// It still works: a prompt typed in runs.
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "next", Paste: true, Submit: true}, nil)
	waitFor(t, func() bool { g, _ := h.reg.Get(a.ID); return g.Summary == "Done: next" })
}

// An agent that never got past starting (the trust question was up when
// the daemon stopped) still gets its task.
func TestStartingAgentGetsItsTaskAfterRestart(t *testing.T) {
	h := realClaude(t)
	setSettings(t, h, map[string]any{"trustProjects": false})
	os.WriteFile(h.opt.ClaudeConfig, []byte("{}\n"), 0o600)
	a := h.spawn(wire.SpawnParams{Task: "first run"})
	h.waitState(a.ID, wire.StateQuestion)
	h.close()
	h.open()
	h.waitState(a.ID, wire.StateQuestion)
	h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Trust}, nil)
	waitFor(t, func() bool { g, _ := h.reg.Get(a.ID); return g.Summary == "Done: first run" })
}

// Codex sends no hook after `codex resume` until the next prompt: the
// agent is idle once its composer shows, quiet.
func TestCodexResumeBecomesIdle(t *testing.T) {
	h := newHarness(t, "FAKE_CODEX_NO_SESSIONSTART=1")
	a := h.spawn(wire.SpawnParams{Kind: "codex", Task: "codex job"})
	h.waitState(a.ID, wire.StateDone)
	h.call("agents.stop", wire.IDParams{ID: a.ID}, nil)
	h.waitState(a.ID, wire.StateExited)
	if err := h.call("agents.resume", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitState(a.ID, wire.StateIdle)
	argv := lastArgv(t, h, a.ID)
	if argv[1] != "resume" || argv[len(argv)-1] == "codex job" {
		t.Fatalf("resume argv %q", argv)
	}
	// Daemon restart: the same.
	h.close()
	h.open()
	h.waitState(a.ID, wire.StateIdle)
	if argv := lastArgv(t, h, a.ID); argv[1] != "resume" {
		t.Fatalf("restart argv %q", argv)
	}
	// A fresh Codex without a task: idle too.
	b := h.spawn(wire.SpawnParams{Kind: "codex"})
	h.waitState(b.ID, wire.StateIdle)
}

// A Codex agent that ran its task but whose session cannot be resumed
// starts over without it.
func TestCodexDoneAgentStartsWithoutTask(t *testing.T) {
	h := newHarness(t, "FAKE_CODEX_NO_SESSIONSTART=1")
	a := h.spawn(wire.SpawnParams{Kind: "codex", Task: "release it"})
	h.waitState(a.ID, wire.StateDone)
	h.close()
	editRecord(t, h, a.ID, func(rec map[string]any) { delete(rec, "sessionSeen") }) // no rollout on disk either
	h.open()
	g := h.waitState(a.ID, wire.StateIdle)
	argv := lastArgv(t, h, a.ID)
	if g.Summary != NotResumedNote || argv[1] == "resume" || slices.Contains(argv, "release it") {
		t.Fatalf("%+v argv %q", g, argv)
	}
}

// The trial's Codex resume stopped at "Update available": a question with
// skip (Esc) and update ("1", Enter).
func TestCodexUpdateScreen(t *testing.T) {
	h := newHarness(t, "FAKE_CODEX_UPDATE=1")
	a := h.spawn(wire.SpawnParams{Kind: "codex", Task: "after the update screen"})
	q := h.waitState(a.ID, wire.StateQuestion)
	if q.Attention == nil || q.Attention.Title != "Codex update available (0.160.0 → 0.160.1)" ||
		!slices.Equal(q.Attention.Options, []string{"skip", "update"}) || !strings.Contains(q.Attention.Detail, "brew upgrade --cask codex") {
		t.Fatalf("attention %+v", q.Attention)
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Trust}, nil); err == nil {
		t.Fatal("trust answered the update screen")
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Skip}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitKeys(a.ID, "\x1b")
	waitFor(t, func() bool { g, _ := h.reg.Get(a.ID); return g.Summary == "Done: after the update screen" })

	b := h.spawn(wire.SpawnParams{Kind: "codex", Task: "update first"})
	h.waitState(b.ID, wire.StateQuestion)
	if err := h.call("agents.answer", wire.AnswerParams{ID: b.ID, Decision: wire.Update}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitKeys(b.ID, "1\r")
	if x := h.waitState(b.ID, wire.StateExited); x.Exit == nil || *x.Exit.Code != 0 {
		t.Fatalf("after update %+v", x.Exit)
	}
}

// settings.json codexUpdatePrompt "skip": no update check at start.
func TestCodexUpdatePromptSkip(t *testing.T) {
	h := newHarness(t, "FAKE_CODEX_UPDATE=1")
	setSettings(t, h, map[string]any{"codexUpdatePrompt": "skip"})
	a := h.spawn(wire.SpawnParams{Kind: "codex", Task: "no update"})
	h.waitState(a.ID, wire.StateDone)
	if j := strings.Join(lastArgv(t, h, a.ID), "\n"); !strings.Contains(j, "-c\ncheck_for_update_on_startup=false") {
		t.Fatalf("argv:\n%s", j)
	}
	if _, err := os.Stat(filepath.Join(h.logs, strings.ReplaceAll(a.ID, "/", "_")+".keys")); err == nil {
		t.Fatal("keys typed")
	}
	writeJSON(t, filepath.Join(h.config, "settings.json"), map[string]any{"codexUpdatePrompt": "never"})
	if _, err := loadSettings(h.config); err == nil {
		t.Fatal("a bad codexUpdatePrompt was taken")
	}
}

// Claude Code 2.1.29x's permission prompt (recorded text): allow is 1,
// always the "don't ask again" choice (2 here), deny Esc.
func TestClaudeApprovalKeys(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "approve with always"})
	h.waitState(a.ID, wire.StateApproval)
	h.waitScreen(a.ID, "Do you want to proceed?")
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Always}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitKeys(a.ID, "2")
	h.waitState(a.ID, wire.StateDone)

	b := h.spawn(wire.SpawnParams{Task: "approve then deny"})
	h.waitState(b.ID, wire.StateApproval)
	h.waitScreen(b.ID, "Do you want to proceed?")
	if err := h.call("agents.answer", wire.AnswerParams{ID: b.ID, Decision: wire.Deny}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitKeys(b.ID, "\x1b")
	h.waitScreen(b.ID, "declined")
	if g, _ := h.reg.Get(b.ID); g.State != wire.StateIdle {
		t.Fatalf("after deny %s", g.State)
	}
}

// A prompt without "don't ask again" (2 is "No"): always is refused, not
// typed as 2.
func TestClaudeAlwaysWithoutTheChoice(t *testing.T) {
	h := newHarness(t, "FAKE_PERMISSION_NO_ALWAYS=1")
	a := h.spawn(wire.SpawnParams{Task: "approve once"})
	h.waitState(a.ID, wire.StateApproval)
	h.waitScreen(a.ID, "No, and tell Claude")
	err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Always}, nil)
	if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeInvalid {
		t.Fatalf("always: %v", err)
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitKeys(a.ID, "1")
	h.waitState(a.ID, wire.StateDone)
}

// recorded is a screen recorded from the real programs.
func recorded(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "fakeagent", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestScreensFromTheTrial(t *testing.T) {
	update := recorded(t, "codex-update.txt")
	s := findFirstRun(wire.KindCodex, update)
	if s == nil || s.id != "update" {
		t.Fatalf("update screen: %+v", s)
	}
	att := s.attention("/x", "", update)
	if att.Title != "Codex update available (0.160.0 → 0.160.1)" || att.Detail != "Update Codex now (runs brew upgrade --cask codex) or skip it and go on." {
		t.Fatalf("%+v", att)
	}
	// What a skip leaves in the history is no question, and the composer
	// below it is idle.
	banner := "╭──────╮\n│ ✨ Update available! 0.160.0 -> 0.160.1 │\n│ Run brew upgrade --cask codex to update. │\n╰──────╯\n"
	composer := strings.NewReplacer("{dir}", "/x", "{status}", "").Replace(recorded(t, "codex-composer.txt"))
	if s := findFirstRun(wire.KindCodex, banner+composer); s != nil {
		t.Fatalf("banner taken for %s", s.id)
	}
	if !codexComposer(banner+composer) || codexComposer(update) {
		t.Fatal("composer")
	}
	if codexComposer(composer + "\n• Working (3s • esc to interrupt)") {
		t.Fatal("a working Codex taken for idle")
	}
	// Claude's "Update installed · Restart to update" is no first-run screen.
	if s := findFirstRun(wire.KindClaude, "  ? for shortcuts      Update installed · Restart to update"); s != nil {
		t.Fatalf("claude banner taken for %s", s.id)
	}
	perm := strings.NewReplacer("{command}", "git push", "{description}", "Push", "{prefix}", "git push", "{dir}", "/x").Replace(recorded(t, "claude-permission.txt"))
	// Output above the prompt with its own numbered list.
	if k, ok := claudeAlwaysKey("1. No changes\n2. Yes, and don't ask again later\n" + perm); !ok || k != "2" {
		t.Fatalf("always key %q %v", k, ok)
	}
	noAlways := strings.NewReplacer("{command}", "rm -rf build", "{description}", "Clean").Replace(recorded(t, "claude-permission-noalways.txt"))
	if _, ok := claudeAlwaysKey(noAlways); ok {
		t.Fatal("always on a prompt without it")
	}
	if k, ok := claudeAlwaysKey("not drawn yet"); !ok || k != "2" {
		t.Fatalf("blank: %q %v", k, ok)
	}
}

// A wrapper notify such as Codex Computer Use's, which chains a previous
// notify of its own, is chained after hesperd's whole.
func TestComputerUseNotifyIsChained(t *testing.T) {
	sky := []string{"/Users/me/.codex/computer-use/Codex Computer Use.app/Contents/MacOS/SkyComputerUseClient", "turn-ended",
		"--previous-notify", `["/usr/bin/true"]`}
	config := "notify = " + tomlArray(sky) + "\nmodel = \"o3\"\n"
	merged, warn := MergeCodexConfig(config, "/opt/hesperd")
	want := "notify = " + tomlArray(append([]string{"/opt/hesperd", "hook", "codex", "notify", "--then"}, sky...))
	if warn != "" || !strings.HasPrefix(merged, want+"\n") {
		t.Fatalf("merged %q", merged)
	}
	// Per agent (codexargs.go).
	h := newHarness(t)
	os.MkdirAll(h.opt.CodexHome, 0o700)
	os.WriteFile(filepath.Join(h.opt.CodexHome, "config.toml"), []byte(config), 0o600)
	h.reg.opt.HookBin = "/opt/hesperd"
	h.reg.mu.Lock()
	args := h.reg.codexSessionArgs()
	h.reg.mu.Unlock()
	if j := strings.Join(args, "\n"); !strings.Contains(j, "notify="+tomlArray(append([]string{"/opt/hesperd", "hook", "codex", "notify", "--then"}, sky...))) {
		t.Fatalf("args:\n%s", j)
	}
}
