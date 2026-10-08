package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/fakeagent"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The test binary is also the fake agent programs (never a real claude or
// codex).
func TestMain(m *testing.M) {
	if os.Getenv("AGENTS_FAKE") == "1" && len(os.Args) > 1 {
		fakeagent.Run(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const (
	claudeSID = "5b0c1d2e-0000-4000-8000-00000000c1a0"
	codexSID  = "019a0000-0000-7000-8000-00000000c0de"
)

type env struct {
	t                   *testing.T
	dir, state, config  string
	claude, codex, home string
	work                string
	s                   *Service
	reg                 *agents.Registry
	logs                string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	e := &env{t: t, dir: dir, state: filepath.Join(dir, "s"), config: filepath.Join(dir, "c"), claude: filepath.Join(dir, "home", ".claude"),
		codex: filepath.Join(dir, "home", ".codex"), home: filepath.Join(dir, "home"), work: filepath.Join(dir, "home", "work"), logs: filepath.Join(dir, "logs")}
	for _, d := range []string{e.state, e.config, e.claude, e.codex, e.work, e.logs} {
		os.MkdirAll(d, 0o700)
	}
	return e
}

func (e *env) open(opt Options) *Service {
	e.t.Helper()
	opt.StateDir, opt.ConfigDir, opt.ClaudeHome, opt.CodexHome, opt.UserHome = e.state, e.config, e.claude, e.codex, e.home
	if opt.Machine == "" {
		opt.Machine = "L"
	}
	opt.Foreground = true
	if opt.ScanEvery == 0 {
		opt.ScanEvery = 30 * time.Millisecond
	}
	if opt.FullScanEvery == 0 {
		opt.FullScanEvery = 100 * time.Millisecond
	}
	opt.Logf = e.t.Logf
	s, err := Open(opt)
	if err != nil {
		e.t.Fatal(err)
	}
	e.s = s
	e.t.Cleanup(s.Close)
	return s
}

// registry: a real agents registry with fake claude / codex programs.
func (e *env) registry() *agents.Registry {
	e.t.Helper()
	exe, _ := os.Executable()
	profiles := map[string]wire.Profile{
		"fake-claude": {Kind: wire.KindClaude, Argv: []string{exe, "claude", "--permission-mode", "auto"}},
		"fake-codex":  {Kind: wire.KindCodex, Argv: []string{exe, "codex", "--dangerously-bypass-approvals-and-sandbox"}},
	}
	writeJSON(e.t, filepath.Join(e.config, "profiles.json"), profiles)
	writeJSON(e.t, filepath.Join(e.config, "settings.json"), map[string]any{"codexSessionHooks": false, "trustProjects": false,
		"defaults": map[string]any{"kind": "claude", "kinds": map[string]string{"claude": "fake-claude", "codex": "fake-codex"}}})
	writeJSON(e.t, filepath.Join(e.dir, "claude.json"), map[string]any{"projects": map[string]any{e.work: map[string]any{"hasTrustDialogAccepted": true}}})
	reg, err := agents.Open(agents.Options{StateDir: filepath.Join(e.dir, "as"), ConfigDir: e.config, Socket: filepath.Join(e.dir, "as", "g.sock"),
		Machine: "L", Env: append(os.Environ(), "AGENTS_FAKE=1", "FAKE_LOG="+e.logs), LoginShell: "/bin/sh", ClaudeConfig: filepath.Join(e.dir, "claude.json"),
		ClaudeHome: e.claude, CodexHome: e.codex, Home: e.home, StopGrace: time.Second, Logf: e.t.Logf})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(reg.Close)
	e.reg = reg
	return reg
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// install places a fixture where its CLI writes it, cwd replaced.
func (e *env) install(name, cwd string) string {
	e.t.Helper()
	data := fixture(e.t, name)
	var path string
	switch name {
	case "claude.jsonl":
		data = bytes.ReplaceAll(data, []byte("/work/rail"), []byte(cwd))
		path = filepath.Join(e.claude, "projects", strings.ReplaceAll(cwd, "/", "-"), claudeSID+".jsonl")
	case "codex.jsonl":
		data = bytes.ReplaceAll(data, []byte("/work/push"), []byte(cwd))
		path = filepath.Join(e.codex, "sessions", "2026", "10", "02", "rollout-2026-10-02T09-00-00-"+codexSID+".jsonl")
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		e.t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	return path
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	waitForWithin(t, what, 15*time.Second, ok)
}

func waitForWithin(t *testing.T, what string, limit time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *env) search(p wire.SessionSearchParams) []wire.Session {
	e.t.Helper()
	res, err := e.s.Search(p)
	if err != nil {
		e.t.Fatal(err)
	}
	return res.Items
}

func (e *env) session(id string) (wire.Session, bool) {
	rw, err := e.s.find(id)
	if err != nil || rw == nil {
		return wire.Session{}, false
	}
	return e.s.toWire(&rw.rec), true
}

func parseFixture(t *testing.T, name, kind string) *Accum {
	acc := &Accum{Kind: kind}
	line := acc.claudeLineIn
	if kind == wire.KindCodex {
		line = acc.codexLineIn
	}
	if _, err := readLines(bytes.NewReader(fixture(t, name)), line, nil); err != nil {
		t.Fatal(err)
	}
	return acc
}

func TestParseClaudeTranscript(t *testing.T) {
	acc := parseFixture(t, "claude.jsonl", wire.KindClaude)
	if got := acc.Title(); got != "Badge flake hunt" {
		t.Errorf("title %q (custom title wins)", got)
	}
	if !strings.HasPrefix(acc.FirstPrompt, "Fix the flaky badge test on the rail") || acc.LastUser != "make it pass 20 times in a row, then stop" {
		t.Errorf("prompts %q / %q", acc.FirstPrompt, acc.LastUser)
	}
	if !strings.HasPrefix(acc.LastAssistant, "20 runs, all green") {
		t.Errorf("last answer %q", acc.LastAssistant)
	}
	if acc.Turns != 2 {
		t.Errorf("turns %d, want 2 (meta, tool results, commands and the sidechain are no prompts)", acc.Turns)
	}
	if acc.Tokens != 195+230 {
		t.Errorf("tokens %d, want %d (each message once, no cache reads)", acc.Tokens, 195+230)
	}
	todos := acc.todos()
	if len(todos) != 2 || !todos[0].Done || todos[1].Done || todos[1].Text != "Open a PR" {
		t.Errorf("todos %+v", todos)
	}
	if acc.Cwd != "/work/rail" || acc.Branch != "fix/flaky-badge" || acc.Origin != "cli" {
		t.Errorf("cwd %q branch %q origin %q", acc.Cwd, acc.Branch, acc.Origin)
	}
	text := strings.Join(acc.Prompts, " ") + strings.Join(acc.Answers, " ")
	for _, hidden := range []string{"wombat", "platypus", "quokkas", "narwhal", "Caveat", "command-name"} {
		if strings.Contains(text, hidden) {
			t.Errorf("searchable text has %q (tool output, hooks, thinking, sidechains, commands stay out)", hidden)
		}
	}
	if !strings.Contains(text, "debounce") {
		t.Error("the answer's text is not searchable")
	}
	if acc.StartedAt != time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC).UnixMilli() || acc.LastActivity != time.Date(2026, 10, 1, 10, 5, 1, 0, time.UTC).UnixMilli() {
		t.Errorf("times %v %v", acc.StartedAt, acc.LastActivity)
	}
}

func TestParseClaudeTaskTools(t *testing.T) {
	acc := &Accum{Kind: wire.KindClaude}
	acc.claudeTool("TaskCreate", json.RawMessage(`{"subject":"Write tests","description":"x","activeForm":"Writing"}`))
	acc.claudeTool("TaskCreate", json.RawMessage(`{"subject":"Ship it"}`))
	acc.claudeTool("TaskUpdate", json.RawMessage(`{"taskId":"1","status":"completed"}`))
	todos := acc.todos()
	if len(todos) != 2 || !todos[0].Done || todos[1].Done || todos[1].Text != "Ship it" {
		t.Errorf("todos %+v", todos)
	}
}

func TestParseCodexRollout(t *testing.T) {
	acc := parseFixture(t, "codex.jsonl", wire.KindCodex)
	if acc.SessionID != codexSID || acc.Cwd != "/work/push" || acc.Branch != "feature/push-fcm" || acc.Origin != "codex-tui" {
		t.Errorf("meta %+v", acc)
	}
	if acc.Title() != "Port the push provider to FCM" || acc.Turns != 2 || acc.LastUser != "run the tests and open a draft PR" {
		t.Errorf("title %q turns %d last %q", acc.Title(), acc.Turns, acc.LastUser)
	}
	if acc.LastAssistant != "Tests pass. Opened draft PR #482." {
		t.Errorf("last answer %q", acc.LastAssistant)
	}
	if acc.Tokens != 9000-6000+900 {
		t.Errorf("tokens %d", acc.Tokens)
	}
	if len(acc.Todos) != 3 || !acc.Todos[0].Done || acc.Todos[1].Done || acc.Todos[2].Text != "Run the tests" {
		t.Errorf("plan %+v", acc.Todos)
	}
	text := strings.Join(acc.Prompts, " ") + strings.Join(acc.Answers, " ")
	for _, hidden := range []string{"capybara", "yak", "ibex", "gazelle", "okapi", "environment_context"} {
		if strings.Contains(text, hidden) {
			t.Errorf("searchable text has %q", hidden)
		}
	}
	acc.ThreadName = "FCM push port"
	if acc.Title() != "FCM push port" {
		t.Errorf("thread name should win: %q", acc.Title())
	}
}

func TestIndexBothFormatsWithZstAndThreadNames(t *testing.T) {
	e := newEnv(t)
	e.install("claude.jsonl", e.work)
	// The Codex rollout as .jsonl.zst, plus Codex's thread name.
	var zbuf bytes.Buffer
	w, _ := zstd.NewWriter(&zbuf)
	w.Write(fixture(t, "codex.jsonl"))
	w.Close()
	zpath := filepath.Join(e.codex, "sessions", "2026", "10", "02", "rollout-2026-10-02T09-00-00-"+codexSID+".jsonl.zst")
	os.MkdirAll(filepath.Dir(zpath), 0o700)
	os.WriteFile(zpath, zbuf.Bytes(), 0o600)
	os.WriteFile(filepath.Join(e.codex, "session_index.jsonl"), []byte(`{"id":"`+codexSID+`","thread_name":"Old name","updated_at":"2026-10-02T09:01:00Z"}`+"\n"+
		`{"id":"`+codexSID+`","thread_name":"FCM push port","updated_at":"2026-10-02T09:05:00Z"}`+"\n"), 0o600)
	e.open(Options{})
	waitFor(t, "both indexed", func() bool { return len(e.search(wire.SessionSearchParams{})) == 2 })
	cx, ok := e.session("L:codex:" + codexSID)
	if !ok || cx.Title != "FCM push port" || cx.Turns != 2 || cx.Cwd != "/work/push" || !cx.External || cx.Origin != "codex-tui" {
		t.Fatalf("codex session %+v", cx)
	}
	cl, _ := e.session("L:claude:" + claudeSID)
	if cl.Title != "Badge flake hunt" || cl.Machine != "L" || cl.ID != "L:claude:"+claudeSID || len(cl.Todos) != 2 || cl.Bytes == 0 {
		t.Fatalf("claude session %+v", cl)
	}
	st, _ := e.s.Stats()
	if st.Count != 2 || st.ByKind["codex"] != 1 || st.ByMachine["L"] != 2 || st.IndexBytes == 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestIncrementalReadsOnlyNewBytes(t *testing.T) {
	e := newEnv(t)
	data := fixture(t, "claude.jsonl")
	lines := bytes.SplitAfter(data, []byte("\n"))
	head := bytes.Join(lines[:5], nil)
	half := lines[5][:len(lines[5])/2]
	path := filepath.Join(e.claude, "projects", "-w", claudeSID+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, append(append([]byte{}, head...), half...), 0o600)
	s := e.open(Options{})
	id := "L:claude:" + claudeSID
	waitFor(t, "first part", func() bool { got, ok := e.session(id); return ok && got.Turns == 1 })
	before := s.scan.readTotal.Load()
	if before != int64(len(head)) {
		t.Fatalf("read %d bytes, want %d (the partial last line waits)", before, len(head))
	}
	// Append the rest.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	rest := data[len(head)+len(half):]
	f.Write(rest)
	f.Close()
	start := time.Now()
	waitFor(t, "the append", func() bool { got, _ := e.session(id); return got.Turns == 2 && got.Title == "Badge flake hunt" })
	t.Logf("append seen and indexed after %v (scan every 30 ms)", time.Since(start))
	if got := s.scan.readTotal.Load() - before; got != int64(len(data)-len(head)) {
		t.Fatalf("the append read %d bytes, want %d", got, len(data)-len(head))
	}
	// The text of both parts is searchable (pieces kept across the checkpoint).
	for _, q := range []string{"flaky", "debounce"} {
		if len(e.search(wire.SessionSearchParams{Query: q})) != 1 {
			t.Errorf("search %q", q)
		}
	}
	// A restart reads nothing again.
	s.Close()
	s2 := e.open(Options{})
	waitFor(t, "first pass", func() bool { return s2.scan.firstPassed.Load() })
	if n := s2.scan.readTotal.Load(); n != 0 {
		t.Fatalf("restart read %d bytes", n)
	}
	// A rewritten (shorter) file is read again from the start.
	os.WriteFile(path, head, 0o600)
	waitFor(t, "the rewrite", func() bool { got, _ := e.session(id); return got.Turns == 1 })
}

func TestSearchFiltersAndPages(t *testing.T) {
	e := newEnv(t)
	e.install("claude.jsonl", e.work)
	e.install("codex.jsonl", filepath.Join(e.home, "push"))
	s := e.open(Options{})
	waitFor(t, "indexed", func() bool { return len(e.search(wire.SessionSearchParams{})) == 2 })
	res := e.search(wire.SessionSearchParams{Query: "debounce"})
	if len(res) != 1 || res[0].Kind != "claude" || !strings.Contains(res[0].Snippet, "[debounce]") {
		t.Fatalf("debounce: %+v", res)
	}
	if len(e.search(wire.SessionSearchParams{Query: "capybara"})) != 0 {
		t.Fatal("tool output is searchable")
	}
	if res := e.search(wire.SessionSearchParams{Query: "flaky bad"}); len(res) != 1 {
		t.Fatal("prefix search")
	}
	if res := e.search(wire.SessionSearchParams{Query: "FCM"}); len(res) != 1 || res[0].Kind != "codex" {
		t.Fatal("title search")
	}
	if res := e.search(wire.SessionSearchParams{Kinds: []string{"codex"}}); len(res) != 1 {
		t.Fatal("kinds")
	}
	if res := e.search(wire.SessionSearchParams{Machines: []string{"M"}}); len(res) != 0 {
		t.Fatal("machines M")
	}
	if res := e.search(wire.SessionSearchParams{Machines: []string{"L"}}); len(res) != 2 {
		t.Fatal("machines L")
	}
	if res := e.search(wire.SessionSearchParams{Since: "2026-10-02T00:00:00Z"}); len(res) != 1 || res[0].Kind != "codex" {
		t.Fatal("since")
	}
	yes := true
	if res := e.search(wire.SessionSearchParams{External: &yes}); len(res) != 2 {
		t.Fatal("external")
	}
	if res := e.search(wire.SessionSearchParams{Live: &yes}); len(res) != 0 {
		t.Fatal("live")
	}
	// Newest first, one per page.
	page, _ := s.Search(wire.SessionSearchParams{Limit: 1})
	if len(page.Items) != 1 || page.Items[0].Kind != "codex" || page.Cursor == "" {
		t.Fatalf("page 1 %+v", page)
	}
	page2, _ := s.Search(wire.SessionSearchParams{Limit: 1, Cursor: page.Cursor})
	if len(page2.Items) != 1 || page2.Items[0].Kind != "claude" || page2.Cursor != "" {
		t.Fatalf("page 2 %+v", page2)
	}
	// Words that are no words, quotes and FTS syntax are just text.
	for _, q := range []string{`"`, `AND OR NOT`, `debounce*`, `rail.py`, `***`, `title:x`} {
		if _, err := s.Search(wire.SessionSearchParams{Query: q}); err != nil {
			t.Errorf("query %q: %v", q, err)
		}
	}
}

type fakeProjects struct{ ch chan struct{} }

func (f *fakeProjects) Resolve(dir string) string {
	if strings.HasSuffix(dir, "/work") {
		return "p-0123456789abcdef"
	}
	return wire.ScratchPrefix + dir
}
func (f *fakeProjects) PathOn(id string) string  { return "" }
func (f *fakeProjects) Changes() <-chan struct{} { return f.ch }

func TestProjectMapping(t *testing.T) {
	e := newEnv(t)
	e.install("claude.jsonl", e.work)
	e.install("codex.jsonl", filepath.Join(e.home, "push"))
	s := e.open(Options{})
	s.SetProjects(&fakeProjects{ch: make(chan struct{})})
	// Projects connect after open in the daemon too: mapped on first sight.
	waitFor(t, "indexed", func() bool { return len(e.search(wire.SessionSearchParams{})) == 2 })
	waitFor(t, "mapped", func() bool {
		return len(e.search(wire.SessionSearchParams{ProjectID: "p-0123456789abcdef"})) == 1
	})
	cx, _ := e.session("L:codex:" + codexSID)
	if cx.ProjectID != wire.ScratchPrefix+filepath.Join(e.home, "push") {
		t.Fatalf("codex project %q", cx.ProjectID)
	}
}

// removedProjects: the scratch project at gone was deleted.
type removedProjects struct {
	fakeProjects
	gone string
}

func (p *removedProjects) FolderRemoved(id, cwd string) bool { return cwd == p.gone }

// Scratch projects: a session whose scratch folder was deleted is marked
// folder removed.
func TestFolderRemoved(t *testing.T) {
	e := newEnv(t)
	e.install("claude.jsonl", e.work)
	e.install("codex.jsonl", filepath.Join(e.home, "push"))
	s := e.open(Options{})
	s.SetProjects(&removedProjects{fakeProjects: fakeProjects{ch: make(chan struct{})}, gone: filepath.Join(e.home, "push")})
	waitFor(t, "indexed", func() bool { return len(e.search(wire.SessionSearchParams{})) == 2 })
	cx, _ := e.session("L:codex:" + codexSID)
	cl, _ := e.session("L:claude:" + claudeSID)
	if !cx.FolderRemoved || cl.FolderRemoved {
		t.Fatalf("folder removed: codex %v, claude %v", cx.FolderRemoved, cl.FolderRemoved)
	}
}

func TestLiveDetection(t *testing.T) {
	e := newEnv(t)
	cpath := e.install("claude.jsonl", e.work)
	e.install("codex.jsonl", filepath.Join(e.home, "push"))
	s := e.open(Options{})
	waitFor(t, "indexed", func() bool { return len(e.search(wire.SessionSearchParams{})) == 2 })
	if l := s.liveOf("claude", claudeSID, time.Now().Add(-time.Hour).UnixNano()); l != nil {
		t.Fatalf("quiet session live: %+v", l)
	}
	// Claude's own session file names it, the process (this one) lives.
	os.MkdirAll(filepath.Join(e.claude, "sessions"), 0o700)
	writeJSON(t, filepath.Join(e.claude, "sessions", "1.json"), map[string]any{"pid": os.Getpid(), "sessionId": claudeSID})
	claudePids.Lock()
	claudePids.at = time.Time{}
	claudePids.Unlock()
	if l := s.liveOf("claude", claudeSID, 0); l == nil || !l.External {
		t.Fatalf("claude process: %+v", l)
	}
	// Codex's writer lock, held.
	lock := filepath.Join(e.codex, "thread-writer-locks", codexSID+".lock")
	os.MkdirAll(filepath.Dir(lock), 0o700)
	f, _ := os.Create(lock)
	if l := s.liveOf("codex", codexSID, 0); l != nil {
		t.Fatal("an unheld lock is not live")
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	// flock is per open file: another open (as Codex's own process
	// would be) sees it held.
	if l := s.liveOf("codex", codexSID, 0); l == nil || !l.External {
		t.Fatalf("codex lock: %+v", l)
	}
	f.Close()
	// A recent write by another process.
	os.Remove(filepath.Join(e.claude, "sessions", "1.json"))
	claudePids.Lock()
	claudePids.at = time.Time{}
	claudePids.Unlock()
	fh, _ := os.OpenFile(cpath, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"type":"mode","mode":"x","sessionId":"` + claudeSID + `"}` + "\n")
	fh.Close()
	waitFor(t, "recent write → live", func() bool { got, _ := e.session("L:claude:" + claudeSID); return got.Live != nil && got.Live.External })
	yes := true
	if res := e.search(wire.SessionSearchParams{Live: &yes}); len(res) != 1 {
		t.Fatal("live filter")
	}
}

func TestResumeForkBriefNeverLive(t *testing.T) {
	e := newEnv(t)
	e.install("claude.jsonl", e.work)
	e.install("codex.jsonl", e.work)
	s := e.open(Options{})
	reg := e.registry()
	s.SetRegistry(reg)
	waitFor(t, "indexed", func() bool { return len(e.search(wire.SessionSearchParams{})) == 2 })
	id := "L:claude:" + claudeSID
	res, err := s.Resume(id, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != claudeSID || res.Project != e.work || res.Name != "Badge flake hunt" {
		t.Fatalf("agent %+v", res.Agent)
	}
	argv := e.argv(res.ID)
	if !strings.Contains(argv, `"--resume","`+claudeSID+`"`) || strings.Contains(argv, "Fix the flaky") {
		t.Fatalf("argv %s (resume, no prompt)", argv)
	}
	// Live in that agent now: resume answers "live" with the agent.
	waitFor(t, "live", func() bool { got, _ := e.session(id); return got.Live != nil && got.Live.AgentID == res.ID })
	var we *wire.Error
	if _, err := s.Resume(id, "", false); !errors.As(err, &we) || we.Code != wire.CodeLive || we.AgentID != res.ID {
		t.Fatalf("second resume: %v", err)
	}
	if err := s.Delete(id, false); !errors.As(err, &we) || we.Code != wire.CodeLive {
		t.Fatalf("delete of a live session: %v", err)
	}
	// It is hesperd's now: not external.
	waitFor(t, "owned", func() bool { got, _ := e.session(id); return !got.External })
	// A fork is fine while it runs.
	fork, err := s.Resume(id, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if fa := e.argv(fork.ID); !strings.Contains(fa, `"--resume","`+claudeSID+`","--fork-session","--session-id"`) || fork.SessionID == claudeSID {
		t.Fatalf("fork argv %s session %s", fa, fork.SessionID)
	}
	cfork, err := s.Resume("L:codex:"+codexSID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if ca := e.argv(cfork.ID); !strings.Contains(ca, `["fork"`) && !strings.Contains(ca, `"fork"`) || !strings.HasSuffix(strings.TrimSpace(ca), `"`+codexSID+`"]`) {
		t.Fatalf("codex fork argv %s", ca)
	}
	// Brief and continue in the other kind.
	brief := s.brief(mustRow(t, s, id))
	for _, want := range []string{"Fix the flaky badge test", "20 runs, all green", "[ ] Open a PR", "[x] Reproduce the flake", "fix/flaky-badge"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief lacks %q:\n%s", want, brief)
		}
	}
	cont, err := s.ContinueAs(id, "codex", "")
	if err != nil {
		t.Fatal(err)
	}
	if cont.Kind != "codex" || !strings.Contains(e.argv(cont.ID), "Pick up where it left off") {
		t.Fatalf("continue %+v %s", cont, e.argv(cont.ID))
	}
	// Removing the agent leaves the session with removedAt.
	reg.StopWait(res.ID, 5*time.Second)
	if err := reg.Remove(res.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "removedAt", func() bool { got, _ := e.session(id); return got.RemovedAt != "" && got.Live == nil })
}

// TestResumeAfterClose: closing agents' undo. agents.close of a running
// agent names its session; the session stays in the history (removedAt,
// not live) and sessions.resume brings it back in a new agent.
func TestResumeAfterClose(t *testing.T) {
	e := newEnv(t)
	e.install("claude.jsonl", e.work)
	s := e.open(Options{})
	reg := e.registry()
	s.SetRegistry(reg)
	waitFor(t, "indexed", func() bool { return len(e.search(wire.SessionSearchParams{})) == 1 })
	id := "L:claude:" + claudeSID
	first, err := s.Resume(id, "", false)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "live", func() bool { got, _ := e.session(id); return got.Live != nil && got.Live.AgentID == first.ID })
	closed, err := reg.CloseAgent(first.ID)
	if err != nil || closed.Session != claudeSID {
		t.Fatalf("close %+v %v", closed, err)
	}
	waitFor(t, "closed", func() bool { _, err := reg.Get(first.ID); return err != nil })
	waitFor(t, "kept, not live", func() bool { got, ok := e.session(id); return ok && got.RemovedAt != "" && got.Live == nil })
	again, err := s.Resume(id, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == first.ID || again.SessionID != claudeSID || again.Project != e.work {
		t.Fatalf("resumed %+v", again.Agent)
	}
	if argv := e.argv(again.ID); !strings.Contains(argv, `"--resume","`+claudeSID+`"`) {
		t.Fatalf("argv %s", argv)
	}
}

func mustRow(t *testing.T, s *Service, id string) *Record {
	rw, err := s.find(id)
	if err != nil {
		t.Fatal(err)
	}
	return &rw.rec
}

// argv is the command line the fake agent id was started with (waiting
// for the process to write it).
func (e *env) argv(id string) string {
	path := filepath.Join(e.logs, strings.ReplaceAll(id, "/", "_")+".argv")
	var data []byte
	waitFor(e.t, "argv of "+id, func() bool { data, _ = os.ReadFile(path); return len(data) > 0 })
	return string(data)
}

func TestResumeRecreatesMissingWorktree(t *testing.T) {
	e := newEnv(t)
	repo := filepath.Join(e.home, "repo")
	run := func(dir string, args ...string) {
		cmd := execGit(dir, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	os.MkdirAll(repo, 0o755)
	run(repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	run(repo, "add", ".")
	run(repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "a")
	run(repo, "branch", "fix/flaky-badge")
	wt := filepath.Join(e.home, "worktrees", "repo", "flaky")
	e.install("claude.jsonl", wt) // the worktree is gone
	s := e.open(Options{})
	s.SetProjects(&pathProjects{repo: repo})
	reg := e.registry()
	s.SetRegistry(reg)
	waitFor(t, "indexed", func() bool { return len(e.search(wire.SessionSearchParams{})) == 1 })
	res, err := s.Resume("L:claude:"+claudeSID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Project != wt || !isDir(wt) {
		t.Fatalf("resumed in %s", res.Project)
	}
	head, _ := execGit(wt, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if strings.TrimSpace(string(head)) != "fix/flaky-badge" {
		t.Fatalf("worktree on %s", head)
	}
	// sessions.show: the changes of the folder (cached).
	d, err := s.Show("L:claude:" + claudeSID)
	if err != nil || d.Changes == nil || !d.Changes.WorktreeExists {
		t.Fatalf("show %+v %v", d.Changes, err)
	}
}

type pathProjects struct{ repo string }

func (p *pathProjects) Resolve(dir string) string { return "p-00000000000000aa" }
func (p *pathProjects) PathOn(id string) string   { return p.repo }
func (p *pathProjects) Changes() <-chan struct{}  { return nil }

func TestArchiveDeleteUndo(t *testing.T) {
	e := newEnv(t)
	cpath := e.install("claude.jsonl", e.work)
	xpath := e.install("codex.jsonl", e.work)
	old := UndoWindow
	UndoWindow = 200 * time.Millisecond
	t.Cleanup(func() { UndoWindow = old })
	s := e.open(Options{})
	waitFor(t, "indexed", func() bool { return len(e.search(wire.SessionSearchParams{})) == 2 })
	cid, xid := "L:claude:"+claudeSID, "L:codex:"+codexSID
	if _, err := s.Archive(cid, true); err != nil {
		t.Fatal(err)
	}
	yes := true
	if len(e.search(wire.SessionSearchParams{})) != 1 || len(e.search(wire.SessionSearchParams{Archived: &yes})) != 1 {
		t.Fatal("archive filter")
	}
	s.Archive(cid, false)
	// Delete and undo within the window: the file stays.
	if err := s.Delete(cid, false); err != nil {
		t.Fatal(err)
	}
	if len(e.search(wire.SessionSearchParams{})) != 1 {
		t.Fatal("a deleted session is listed")
	}
	if err := s.Delete(cid, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	s.deletes.sweep()
	if !fileExists(cpath) || len(e.search(wire.SessionSearchParams{})) != 2 {
		t.Fatal("undo did not keep it")
	}
	// Delete for good: Claude's file goes, Codex's moves to its archive.
	s.Delete(cid, false)
	s.Delete(xid, false)
	waitFor(t, "removed", func() bool { s.deletes.sweep(); return !fileExists(cpath) && !fileExists(xpath) })
	if !fileExists(filepath.Join(e.codex, "archived_sessions", filepath.Base(xpath))) {
		t.Fatal("codex rollout not in archived_sessions")
	}
	time.Sleep(300 * time.Millisecond) // full passes see both gone / moved
	if n := len(e.search(wire.SessionSearchParams{})); n != 0 {
		t.Fatalf("%d listed after delete", n)
	}
	if err := s.Delete(cid, true); err == nil {
		t.Fatal("undo after the window")
	}
}

func TestMergeConvergesAndTombstones(t *testing.T) {
	base := Record{Node: "n-a", Home: "L", Kind: "claude", SID: "s1", Meta: Meta{Title: "one"}, MetaS: Stamp{T: 10, N: "n-a"}}
	a, b := base, base
	a.Archived, a.ArchivedS = true, Stamp{T: 20, N: "n-a"}
	b.Meta, b.MetaS = Meta{Title: "two"}, Stamp{T: 30, N: "n-a"}
	b.Deleted, b.DeletedS = true, Stamp{T: 25, N: "n-b"}
	x, y := a, b
	x.merge(&b)
	y.merge(&a)
	xj, _ := json.Marshal(x)
	yj, _ := json.Marshal(y)
	if !bytes.Equal(xj, yj) || x.Meta.Title != "two" || !x.Archived || !x.Deleted {
		t.Fatalf("diverged:\n%s\n%s", xj, yj)
	}
	// An older copy never revives a tombstone; a newer undo does.
	stale := base
	if x.merge(&stale) || !x.Deleted {
		t.Fatal("stale copy changed it")
	}
	undo := x
	undo.Deleted, undo.DeletedS = false, Stamp{T: 40, N: "n-b"}
	if !x.merge(&undo) || x.Deleted {
		t.Fatal("undo lost")
	}
	// Equal stamps: the larger value wins on both sides.
	p, q := base, base
	p.MovedTo, p.MovedS = "n-x", Stamp{T: 5, N: "n-c"}
	q.MovedTo, q.MovedS = "n-y", Stamp{T: 5, N: "n-c"}
	p2, q2 := p, q
	p2.merge(&q)
	q2.merge(&p)
	if p2.MovedTo != q2.MovedTo {
		t.Fatal("equal stamps diverge")
	}
}

func TestBadIDs(t *testing.T) {
	e := newEnv(t)
	s := e.open(Options{})
	for _, id := range []string{"", "L", "L:shell:x", "L:claude:../../etc", "L:claude:"} {
		if _, err := s.Show(id); err == nil {
			t.Errorf("show %q", id)
		}
	}
	if _, err := s.Show("L:claude:" + claudeSID); err == nil {
		t.Error("unknown session shown")
	}
	if _, err := s.Search(wire.SessionSearchParams{Cursor: "x"}); err == nil {
		t.Error("bad cursor")
	}
}

func TestScannerKeepsOwnershipClaimedAfterMetadataRead(t *testing.T) {
	e := newEnv(t)
	s := e.open(Options{})
	f := fileInfo{path: filepath.Join(e.codex, "stale.jsonl"), kind: wire.KindCodex, sid: codexSID, size: 1}
	rec := Record{Node: s.db.node, Kind: f.kind, SID: f.sid}
	// The scanner captured this before the resume path claimed the session.
	stale := Meta{External: true, Cwd: e.work, Version: "1"}
	s.own(f.kind, f.sid)
	if err := s.scan.store(rec.Key(), f, stale, 1, nil); err != nil {
		t.Fatal(err)
	}
	got, ok := e.session("L:codex:" + codexSID)
	if !ok || got.External {
		t.Fatalf("owned session indexed as external: %+v", got)
	}
}
