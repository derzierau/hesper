package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestBellScanner(t *testing.T) {
	cases := []struct {
		parts []string
		bell  bool
	}{
		{[]string{"plain"}, false},
		{[]string{"ask\x07"}, true},
		{[]string{"\x1b]0;title\x07"}, false},
		{[]string{"\x1b]0;ti", "tle\x07 then \x07"}, true},
		{[]string{"\x1b", "]8;;url\x1b\\link"}, false},
		{[]string{"\x1b]11;?\x1b", "\\"}, false},
	}
	for _, c := range cases {
		var b bellScanner
		got := false
		for _, p := range c.parts {
			got = b.feed([]byte(p)) || got
		}
		if got != c.bell {
			t.Errorf("%q: bell %v", c.parts, got)
		}
	}
}

func TestCodexPrompt(t *testing.T) {
	rows := []string{
		"  some output",
		"╭──────────────────────────────╮",
		"│ Would you like to run the following command? │",
		"│                              │",
		"│ $ cargo test --all           │",
		"│ › 1. Yes, proceed (y)        │",
		"│   2. Yes, and don't ask again for this command (a) │",
		"│   3. No, and tell Codex what to do differently (esc) │",
	}
	found, title, detail := codexPrompt(rows)
	if !found || title != "Command" || detail != "cargo test --all" {
		t.Fatalf("%v %q %q", found, title, detail)
	}
	if found, _, _ := codexPrompt([]string{"Would you like to run the following command? is what it asks"}); found {
		t.Fatal("a question without choices")
	}
	if found, _, _ := codexPrompt([]string{"› Yes, proceed"}); found {
		t.Fatal("choices without a question")
	}
	found, title, _ = codexPrompt([]string{"Would you like to make the following edits?", "src/main.rs", "Yes, proceed (y)"})
	if !found || title != "Edit" {
		t.Fatalf("edits: %v %q", found, title)
	}
}

func TestDescribeTool(t *testing.T) {
	cases := map[string]string{
		`{"command":"git push origin main\nmore"}`:           "git push origin main",
		`{"command":["bash","-lc","npm test"]}`:              "npm test",
		`{"file_path":"/a/b.go","content":"x"}`:              "/a/b.go",
		`{"pattern":"TODO"}`:                                 "TODO",
		`{"input":"*** Begin Patch\n*** Update File: x.go"}`: "x.go",
	}
	for in, want := range cases {
		var v any
		json.Unmarshal([]byte(in), &v)
		if got := describeTool("T", v); got != want {
			t.Errorf("%s: %q want %q", in, got, want)
		}
	}
}

func TestSummarize(t *testing.T) {
	for in, want := range map[string]string{
		"Did things.\n\n**Opened PR #482 (draft)**\n": "Opened PR #482 (draft)",
		"- one\n- two": "two",
		"":             "",
		"x\n---\n":     "x",
	} {
		if got := summarize(in); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
}

func TestLastMessageFromTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	lines := []string{
		`{"type":"user","message":{"content":"hi"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use"},{"type":"text","text":"All done.\nPR #9"}]}}`,
		`{"type":"system"}`,
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	if got := lastMessage(map[string]any{"transcript_path": path}); got != "All done.\nPR #9" {
		t.Fatalf("%q", got)
	}
}

func TestNamesAndSlugs(t *testing.T) {
	if got := NameFor("Push provider: FCM!\nDetails follow"); got != "push-provider-fcm" {
		t.Fatal(got)
	}
	if got := Slugify(strings.Repeat("word ", 20), 40); len(got) > 40 || !strings.HasPrefix(got, "word-word") {
		t.Fatal(got)
	}
	if got := Slugify("!!!", 40); got != "task" {
		t.Fatal(got)
	}
	if got := sessionName("  fix\tthe bug\nmore", "x"); got != "fix the bug" {
		t.Fatalf("%q", got)
	}
}

func TestResolveProfile(t *testing.T) {
	profiles := BuiltinProfiles()
	d := defaultSettings().Defaults
	d.Projects["/p/web"] = "claude-bypass" // a legacy name
	d.Projects["/p/api"] = "codex-unattended"
	cases := []struct{ name, kind, project, want string }{
		{"", "", "/x", "claude"},
		{"", "codex", "/x", "codex"},
		{"", "", "/p/web", "claude-unattended"},
		{"", "codex", "/p/web", "codex"},
		{"", "", "/p/api", "codex-unattended"},
		{"shell", "", "/x", "shell"},
		{"codex-full", "", "/x", "codex-unattended"},
		{"claude-auto-rc", "claude", "/x", "claude-auto-rc"},
	}
	for _, c := range cases {
		got, _, err := resolveProfile(profiles, d, c.name, c.kind, c.project)
		if err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
	if _, _, err := resolveProfile(profiles, d, "shell", "claude", "/x"); err == nil {
		t.Error("a shell profile for claude")
	}
	// The defaults keep permission prompts on.
	for kind, want := range map[string]string{"claude": "claude", "codex": "codex"} {
		_, p, _ := resolveProfile(profiles, d, "", kind, "/x")
		if strings.Join(p.Argv, " ") != want {
			t.Errorf("default %s profile runs %q", kind, p.Argv)
		}
	}
	// A kind default that is gone falls back to the built-in named after
	// the kind, never to an unattended profile by map order.
	gone := defaultSettings().Defaults
	gone.Kinds["codex"] = "deleted"
	for range 20 {
		if got, _, _ := resolveProfile(profiles, gone, "", "codex", "/x"); got != "codex" {
			t.Fatalf("fallback %q", got)
		}
	}
	// profiles.json may define a legacy name itself.
	own := BuiltinProfiles()
	own["codex-full"] = wire.Profile{Kind: wire.KindCodex, Argv: []string{"codex", "--mine"}}
	if got, p, _ := resolveProfile(own, d, "codex-full", "", "/x"); got != "codex-full" || p.Argv[1] != "--mine" {
		t.Errorf("own legacy name: %q %v", got, p.Argv)
	}
}

func TestProfilesAndSettingsFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(`{"claude-unattended":{"kind":"claude","argv":[]},"mine":{"kind":"codex","argv":["codex","--x"]}}`), 0o600)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"machine":"mbp","defaults":{"kinds":{"codex":"mine"},"projects":{"~/p":"mine"}}}`), 0o600)
	p, err := loadProfiles(dir)
	if err != nil || p["mine"].Argv[1] != "--x" {
		t.Fatalf("%v %v", p, err)
	}
	if _, ok := p["claude-unattended"]; ok {
		t.Fatal("an empty profile did not remove the built-in one")
	}
	s, err := loadSettings(dir)
	home, _ := os.UserHomeDir()
	if err != nil || s.Machine != "mbp" || s.Defaults.Kinds["codex"] != "mine" || s.Defaults.Kinds["claude"] != "claude" || s.Defaults.Projects[filepath.Join(home, "p")] != "mine" {
		t.Fatalf("%+v %v", s, err)
	}
	if machineShort(s, dir, dir) != "mbp" {
		t.Fatal("machine")
	}
}

func TestArgvPerKind(t *testing.T) {
	r := &Registry{opt: Options{LoginShell: "/bin/zsh"}}
	claude := &agent{Agent: wire.Agent{ID: "L/abc123", Kind: "claude", Name: "n", Task: "-v looks like a flag", SessionID: "S"}}
	got := r.argv(claude, BuiltinProfiles()["claude-auto-rc"], false, false)
	want := "claude --permission-mode auto --remote-control -v looks like a flag --session-id S -- -v looks like a flag"
	if strings.Join(got, " ") != want {
		t.Fatalf("%q", got)
	}
	if got := r.argv(claude, BuiltinProfiles()["claude-auto-rc"], true, false); strings.Join(got[len(got)-2:], " ") != "--resume S" {
		t.Fatalf("%q", got)
	}
	codex := &agent{Agent: wire.Agent{Kind: "codex", Task: "do it", SessionID: "C"}}
	if got := r.argv(codex, BuiltinProfiles()["codex-unattended"], false, false); strings.Join(got, " ") != "codex --dangerously-bypass-approvals-and-sandbox do it" {
		t.Fatalf("%q", got)
	}
	if got := r.argv(codex, BuiltinProfiles()["codex-unattended"], true, false); strings.Join(got, " ") != "codex resume --dangerously-bypass-approvals-and-sandbox C" {
		t.Fatalf("%q", got)
	}
	shell := &agent{Agent: wire.Agent{Kind: "shell", Task: "ignored"}}
	if got := r.argv(shell, BuiltinProfiles()["shell"], false, false); strings.Join(got, " ") != "/bin/zsh -l" {
		t.Fatalf("%q", got)
	}
}

func TestHookConfig(t *testing.T) {
	bin := "/Users/me/.local/bin/hesperd"
	existing := `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"\"/old/bin/hesperd\" hook claude Stop"}]},{"hooks":[{"type":"command","command":"say done"}]}]}}`
	out, err := MergeClaudeSettings([]byte(existing), bin)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Model string                      `json:"model"`
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	json.Unmarshal(out, &s)
	if s.Model != "opus" || len(s.Hooks["Stop"]) != 2 || strings.Count(string(out), `hook claude Stop"`) != 1 || !strings.Contains(string(out), "say done") {
		t.Fatalf("%s", out)
	}
	if !strings.Contains(string(out), `\"/Users/me/.local/bin/hesperd\" hook claude PermissionRequest`) {
		t.Fatalf("%s", out)
	}
	pre := s.Hooks["PreToolUse"][0]["hooks"].([]any)[0].(map[string]any)
	if pre["async"] != true {
		t.Fatal("PreToolUse must be async")
	}
	// Idempotent.
	again, _ := MergeClaudeSettings(out, bin)
	if !sameJSON(again, out) {
		t.Fatal("not idempotent")
	}
	toml, warn := MergeCodexConfig("model = \"o3\"\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n", bin)
	if warn != "" || !strings.HasPrefix(toml, `notify = ["/Users/me/.local/bin/hesperd", "hook", "codex", "notify"]`) {
		t.Fatalf("%q %q", toml, warn)
	}
	// Another notify program is chained after hesperd's, once.
	chained, warn := MergeCodexConfig("notify = [\"/bin/other\", 'arg x']\nmodel = \"o3\"\n", bin)
	want := `notify = ["/Users/me/.local/bin/hesperd", "hook", "codex", "notify", "--then", "/bin/other", "arg x"]` + "\nmodel = \"o3\"\n"
	if warn != "" || chained != want {
		t.Fatalf("chained %q %q", chained, warn)
	}
	if again, _ := MergeCodexConfig(chained, bin); again != chained {
		t.Fatalf("chaining again: %q", again)
	}
	if moved, _ := MergeCodexConfig(chained, "/new/hesperd"); !strings.Contains(moved, `["/new/hesperd", "hook", "codex", "notify", "--then", "/bin/other", "arg x"]`) {
		t.Fatalf("a new bin keeps the chain: %q", moved)
	}
	if _, warn := MergeCodexConfig("notify = [\n  \"other\",\n]\n", bin); warn == "" {
		t.Fatal("a multi-line notify was rewritten")
	}
	// A dry run plans; an install writes (temp paths only).
	dir := t.TempDir()
	files, err := PlanHookInstall(bin, filepath.Join(dir, "claude", "settings.json"), filepath.Join(dir, "codex"))
	if err != nil || len(files) != 3 || !files[0].Changed {
		t.Fatalf("%+v %v", files, err)
	}
	if err := InstallHooks(files); err != nil {
		t.Fatal(err)
	}
	files, _ = PlanHookInstall(bin, filepath.Join(dir, "claude", "settings.json"), filepath.Join(dir, "codex"))
	for _, f := range files {
		if f.Changed {
			t.Fatalf("%s changed again", f.Path)
		}
	}
}

// TestHookConfigReplacesGhostyd: an install after the rename to Hesper
// replaces ghostyd's hook entries, description and notify instead of adding
// hesperd's next to them.
func TestHookConfigReplacesGhostyd(t *testing.T) {
	bin := "/Users/me/.local/bin/hesperd"
	old := "/Users/me/.local/bin/ghostyd"
	claude, err := MergeClaudeSettings(nil, old)
	if err != nil {
		t.Fatal(err)
	}
	claude = []byte(strings.Replace(string(claude), `"hooks": {`, `"hooks": {"Stop2": [{"hooks": [{"type": "command", "command": "say done"}]}],`, 1))
	out, err := MergeClaudeSettings(claude, bin)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "ghostyd") || !strings.Contains(string(out), "say done") || !sameJSON(out, must(MergeClaudeSettings(claude, bin))) {
		t.Fatalf("%s", out)
	}
	var s struct {
		Hooks map[string][]any `json:"hooks"`
	}
	json.Unmarshal(out, &s)
	if len(s.Hooks["Stop"]) != 1 {
		t.Fatalf("Stop has %d groups, want hesperd's only: %s", len(s.Hooks["Stop"]), out)
	}
	codex := []byte(`{"description": "Ghosty agent state hooks (ghostyd)", "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "\"/Users/me/.local/bin/ghostyd\" hook codex Stop"}]}]}}`)
	out, err = MergeCodexHooks(codex, bin)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "ghostyd") || strings.Contains(string(out), "Ghosty") || !strings.Contains(string(out), "Hesper agent state hooks (hesperd)") {
		t.Fatalf("%s", out)
	}
	mine := []byte(`{"description": "mine", "hooks": {}}`)
	if out, _ := MergeCodexHooks(mine, bin); !strings.Contains(string(out), `"description": "mine"`) {
		t.Fatalf("a description of the user's was replaced: %s", out)
	}
	toml, warn := MergeCodexConfig("notify = [\"/Users/me/.local/bin/ghostyd\", \"hook\", \"codex\", \"notify\", \"--then\", \"/bin/other\"]\n", bin)
	if warn != "" || toml != `notify = ["/Users/me/.local/bin/hesperd", "hook", "codex", "notify", "--then", "/bin/other"]`+"\n" {
		t.Fatalf("%q %q", toml, warn)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestSubscriberCoalesces(t *testing.T) {
	s := &Subscriber{pending: map[string]*wire.Agent{}, wake: make(chan struct{}, 1)}
	for i := range 1000 {
		a := wire.Agent{ID: "L/a", PID: i}
		s.push(a.ID, &a)
	}
	s.push("L/b", nil)
	notes := s.Wait(make(chan struct{}))
	if len(notes) != 2 || notes[0].Agent.PID != 999 || notes[1].Removed != "L/b" {
		t.Fatalf("%+v", notes)
	}
}

func TestCodexSessionAdoption(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	started := time.Now().Add(-time.Minute)
	day := filepath.Join(home, "sessions", started.Format("2006/01/02"))
	os.MkdirAll(day, 0o700)
	write := func(at time.Time, id, cwd string) {
		name := "rollout-" + at.Format("2006-01-02T15-04-05") + "-" + id + ".jsonl"
		os.WriteFile(filepath.Join(day, name), []byte(`{"type":"session_meta","payload":{"id":"`+id+`","cwd":"`+cwd+`"}}`+"\n"), 0o600)
	}
	write(started.Add(-time.Hour), "old", dir)
	write(started.Add(time.Second), "elsewhere", "/other")
	write(started.Add(2*time.Second), "taken", dir)
	write(started.Add(3*time.Second), "mine", dir)
	if got := codexSessionSince(home, dir, started, map[string]bool{"taken": true}); got != "mine" {
		t.Fatalf("%q", got)
	}
}
