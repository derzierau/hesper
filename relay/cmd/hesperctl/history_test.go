package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/projects"
	"github.com/derzierau/hesper/relay/internal/sessions"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

const (
	fixtureClaude = "5b0c1d2e-0000-4000-8000-00000000c1a0"
	fixtureCodex  = "019a0000-0000-7000-8000-00000000c0de"
)

// fullEnv is hesperd as gateway wires it, minus other machines: the
// registry with the project store and the shared history. Its claude and
// codex profiles are cat; the history holds internal/sessions' two
// synthetic transcripts, in project.
type fullEnv struct {
	reg     *agents.Registry
	sock    string
	dir     string
	project string
	root    string // the projects root (clone)
}

func fullDaemon(t *testing.T) *fullEnv {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	e := &fullEnv{dir: dir, project: filepath.Join(dir, "home", "app"), root: filepath.Join(dir, "home", "projects")}
	state, config := filepath.Join(dir, "state"), filepath.Join(dir, "config")
	home, claude, codex := filepath.Join(dir, "home"), filepath.Join(dir, "home", ".claude"), filepath.Join(dir, "home", ".codex")
	for _, d := range []string{state, config, e.project, claude, codex, e.root} {
		os.MkdirAll(d, 0o700)
	}
	cat := []string{"/bin/sh", "-c", "exec cat"}
	profiles, _ := json.Marshal(map[string]wire.Profile{"cat": {Kind: wire.KindClaude, Argv: cat}, "catx": {Kind: wire.KindCodex, Argv: cat}})
	os.WriteFile(filepath.Join(config, "profiles.json"), profiles, 0o600)
	os.WriteFile(filepath.Join(config, "settings.json"), []byte(`{"codexSessionHooks":false,"trustProjects":false,"defaults":{"kind":"claude","kinds":{"claude":"cat","codex":"catx"}}}`), 0o600)
	trust, _ := json.Marshal(map[string]any{"projects": map[string]any{e.project: map[string]any{"hasTrustDialogAccepted": true}}})
	os.WriteFile(filepath.Join(dir, "claude.json"), trust, 0o600)
	install := func(name, from, path string) {
		data, err := os.ReadFile(filepath.Join("..", "..", "internal", "sessions", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.ReplaceAll(data, []byte(from), []byte(e.project))
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, data, 0o600)
		old := time.Now().Add(-time.Hour)
		os.Chtimes(path, old, old)
	}
	install("claude.jsonl", "/work/rail", filepath.Join(claude, "projects", strings.ReplaceAll(e.project, "/", "-"), fixtureClaude+".jsonl"))
	install("codex.jsonl", "/work/push", filepath.Join(codex, "sessions", "2026", "10", "02", "rollout-2026-10-02T09-00-00-"+fixtureCodex+".jsonl"))

	store := projects.Open(projects.Options{StateDir: state, Machine: "L", Home: home, Logf: t.Logf})
	hist, err := sessions.Open(sessions.Options{StateDir: state, ConfigDir: config, ClaudeHome: claude, CodexHome: codex, UserHome: home, Machine: "L",
		Foreground: true, ScanEvery: 30 * time.Millisecond, FullScanEvery: 100 * time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	e.sock = filepath.Join(state, "hesperd.sock")
	reg, err := agents.Open(agents.Options{StateDir: state, ConfigDir: config, Socket: e.sock, Machine: "L",
		Env: os.Environ(), LoginShell: "/bin/sh", ClaudeConfig: filepath.Join(dir, "claude.json"), ClaudeHome: claude, CodexHome: codex, Home: home,
		WorktreeRoot: filepath.Join(dir, "wt"), ProjectsRoot: e.root, StopGrace: time.Second, Logf: t.Logf, Projects: store, Sessions: hist})
	if err != nil {
		t.Fatal(err)
	}
	store.SetMachine(reg.Machine())
	store.SetReproject(reg.Reproject)
	store.SetAgents(reg.List)
	hist.SetRegistry(reg)
	hist.SetProjects(store)
	ln, err := agents.Listen(e.sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := agents.NewServer(reg)
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); reg.Close(); hist.Close(); store.Close() })
	e.reg = reg
	return e
}

// ctl runs a command on the daemon and returns its stdout and exit code;
// errors go to the test log.
func (e *fullEnv) ctl(t *testing.T, args ...string) (string, int) {
	t.Helper()
	args = append(args, "--daemon-socket", e.sock)
	var code int
	var stderr bytes.Buffer
	out := capture(t, func() { code = execute(context.Background(), args, &stderr) })
	if stderr.Len() > 0 {
		t.Logf("%v: %s", args, stderr.String())
	}
	return out, code
}

// ok runs a command that must succeed.
func (e *fullEnv) ok(t *testing.T, args ...string) string {
	t.Helper()
	out, code := e.ctl(t, args...)
	if code != exitOK {
		t.Fatalf("%v: exit %d", args, code)
	}
	return out
}

// okJSON runs a command with --json into v (zeroed first: omitted fields
// must not keep an earlier value).
func (e *fullEnv) okJSON(t *testing.T, v any, args ...string) {
	t.Helper()
	out := e.ok(t, append(args, "--json")...)
	rv := reflect.ValueOf(v).Elem()
	rv.Set(reflect.Zero(rv.Type()))
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

// fails runs a command with --json that must fail with exit and code.
func (e *fullEnv) fails(t *testing.T, exit int, code string, args ...string) string {
	t.Helper()
	got, gotCode, message := jsonError(t, append(args, "--json", "--daemon-socket", e.sock)...)
	if got != exit || gotCode != code {
		t.Fatalf("%v: exit %d code %q (%s), want %d %q", args, got, gotCode, message, exit, code)
	}
	return message
}

func TestHistoryCommands(t *testing.T) {
	e := fullDaemon(t)
	claudeID, codexID := "L:claude:"+fixtureClaude, "L:codex:"+fixtureCodex
	var page wire.SessionSearchResult
	eventually(t, "indexed", func() bool {
		e.okJSON(t, &page, "history", "search")
		return len(page.Items) == 2
	})
	if out := e.ok(t, "history", "search"); !strings.Contains(out, claudeID) || !strings.Contains(out, "Badge flake hunt") || !strings.Contains(out, "FOLDER") {
		t.Errorf("table:\n%s", out)
	}
	// Query, kinds, folder (no project here: scratch), since, flags.
	e.okJSON(t, &page, "history", "search", "flaky", "badge")
	if len(page.Items) != 1 || page.Items[0].ID != claudeID || !strings.Contains(page.Items[0].Snippet, "[") {
		t.Fatalf("query: %+v", page.Items)
	}
	e.okJSON(t, &page, "history", "search", "--kind", "codex")
	if len(page.Items) != 1 || page.Items[0].ID != codexID {
		t.Fatalf("--kind codex: %+v", page.Items)
	}
	e.okJSON(t, &page, "history", "search", "--project", e.project, "--machine", "L", "--since", "3650d")
	if len(page.Items) != 2 {
		t.Fatalf("--project folder: %+v", page.Items)
	}
	e.okJSON(t, &page, "history", "search", "--since", "1m")
	if len(page.Items) != 0 {
		t.Fatalf("--since 1m: %+v", page.Items)
	}
	e.okJSON(t, &page, "history", "search", "--live")
	if len(page.Items) != 0 {
		t.Fatalf("--live: %+v", page.Items)
	}
	e.okJSON(t, &page, "history", "search", "--limit", "1")
	if len(page.Items) != 1 || page.Cursor == "" {
		t.Fatalf("--limit 1: %+v", page)
	}
	e.okJSON(t, &page, "history", "search", "--limit", "1", "--cursor", page.Cursor)
	if len(page.Items) != 1 {
		t.Fatalf("second page: %+v", page)
	}
	e.fails(t, exitUsage, codeUsage, "history", "search", "--since", "yesterday")
	e.fails(t, exitUsage, codeUsage, "history", "search", "--kind", "shell")

	// show, brief, stats.
	var d wire.SessionDetail
	e.okJSON(t, &d, "history", "show", claudeID)
	if d.Title != "Badge flake hunt" || d.Turns == 0 {
		t.Fatalf("show: %+v", d.Session)
	}
	if out := e.ok(t, "history", "show", claudeID); !strings.Contains(out, "Badge flake hunt") || !strings.Contains(out, "[x] Reproduce the flake") {
		t.Errorf("show:\n%s", out)
	}
	if out := e.ok(t, "history", "brief", claudeID); !strings.Contains(out, "Fix the flaky badge test") {
		t.Errorf("brief:\n%s", out)
	}
	var stats wire.SessionStats
	e.okJSON(t, &stats, "history", "stats")
	if stats.Count != 2 || stats.ByKind["codex"] != 1 {
		t.Fatalf("stats %+v", stats)
	}
	if out := e.ok(t, "history", "stats"); !strings.Contains(out, "sessions") {
		t.Errorf("stats:\n%s", out)
	}
	e.fails(t, exitNotFound, wire.CodeNotFound, "history", "show", "L:claude:nope")
	e.fails(t, exitUsage, codeUsage, "history", "show")

	// archive, delete, undelete.
	e.ok(t, "history", "archive", codexID)
	e.okJSON(t, &page, "history", "search")
	if len(page.Items) != 1 {
		t.Fatalf("archived still listed: %+v", page.Items)
	}
	e.okJSON(t, &page, "history", "search", "--archived")
	if len(page.Items) != 1 || !page.Items[0].Archived {
		t.Fatalf("--archived: %+v", page.Items)
	}
	e.ok(t, "history", "archive", codexID, "--undo")
	e.ok(t, "history", "delete", codexID)
	e.okJSON(t, &page, "history", "search")
	if len(page.Items) != 1 {
		t.Fatalf("deleted still listed: %+v", page.Items)
	}
	e.ok(t, "history", "undelete", codexID)
	e.okJSON(t, &page, "history", "search")
	if len(page.Items) != 2 {
		t.Fatalf("undeleted not listed: %+v", page.Items)
	}

	// resume (then live: exit 7 with the agent), fork, continue-as.
	var a startedAgent
	e.okJSON(t, &a, "history", "resume", claudeID)
	if a.SessionID != fixtureClaude || a.Project != e.project {
		t.Fatalf("resume: %+v", a)
	}
	eventually(t, "live", func() bool {
		var s wire.SessionDetail
		e.okJSON(t, &s, "history", "show", claudeID)
		return s.Live != nil && s.Live.AgentID == a.ID
	})
	var stderr bytes.Buffer
	if code := execute(context.Background(), []string{"history", "resume", claudeID, "--json", "--daemon-socket", e.sock}, &stderr); code != exitExists ||
		!strings.Contains(stderr.String(), `"code":"live"`) || !strings.Contains(stderr.String(), `"agentId":"`+a.ID+`"`) {
		t.Fatalf("resume of a live session: exit %d %s", code, stderr.String())
	}
	out := e.ok(t, "history", "fork", claudeID)
	if g, err := e.reg.Get(strings.TrimSpace(out)); err != nil || g.SessionID == fixtureClaude || !strings.HasPrefix(g.Name, "fork: ") {
		t.Fatalf("fork %q: %+v", out, g)
	}
	e.okJSON(t, &a, "history", "continue-as", claudeID, "--kind", "codex")
	if a.Kind != wire.KindCodex {
		t.Fatalf("continue-as: %+v", a)
	}
	e.fails(t, exitUsage, codeUsage, "history", "continue-as", claudeID)
	if len(e.reg.List()) != 3 {
		t.Fatalf("agents %d", len(e.reg.List()))
	}
}

func TestSubcommandHelp(t *testing.T) {
	ctx := context.Background()
	for _, args := range [][]string{{"history"}, {"help", "history"}, {"history", "--help"}} {
		out := capture(t, func() {
			if code := execute(ctx, args, io.Discard); code != exitOK {
				t.Errorf("%v: exit %d", args, code)
			}
		})
		if !strings.Contains(out, "history search") || !strings.Contains(out, "history continue-as") || strings.Contains(out, "projects ls") {
			t.Errorf("%v:\n%s", args, out)
		}
	}
	for _, args := range [][]string{{"help", "history", "search"}, {"history", "search", "--help"}, {"history", "search", "x", "-h"}} {
		out := capture(t, func() {
			if code := execute(ctx, args, io.Discard); code != exitOK {
				t.Errorf("%v: exit %d", args, code)
			}
		})
		if !strings.Contains(out, "Usage: hesperctl history search") || !strings.Contains(out, "--cursor") || !strings.Contains(out, "--daemon-socket") {
			t.Errorf("%v:\n%s", args, out)
		}
	}
	out := capture(t, func() { execute(ctx, []string{"help", "projects", "--json"}, io.Discard) })
	var docs []commandDoc
	if err := json.Unmarshal([]byte(out), &docs); err != nil || len(docs) != 6 || docs[0].Name != "projects ls" {
		t.Errorf("help projects --json: %v %s", err, out)
	}
	var stderr bytes.Buffer
	for _, args := range [][]string{{"history", "nope"}, {"help", "history", "nope"}, {"help", "groups", "ls", "extra"}} {
		stderr.Reset()
		if code := execute(ctx, args, &stderr); code != exitUsage {
			t.Errorf("%v: exit %d %s", args, code, stderr.String())
		}
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("message %q", stderr.String())
	}
	// A command that is not a parent still takes positional arguments.
	if c, n := lookup([]string{"send", "history", "search"}); c == nil || c.Name != "send" || n != 1 {
		t.Errorf("lookup send: %v %d", c, n)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{
		"24h":                  "2026-10-07T12:00:00Z",
		"90m":                  "2026-10-08T10:30:00Z",
		"7d":                   "2026-10-01T12:00:00Z",
		"2w":                   "2026-09-24T12:00:00Z",
		"2026-10-01T08:00:00Z": "2026-10-01T08:00:00Z",
	} {
		if got, err := parseSince(in, now); err != nil || got != want {
			t.Errorf("%s: %s %v, want %s", in, got, err, want)
		}
	}
	if got, err := parseSince("2026-10-01", now); err != nil || !strings.HasPrefix(got, "2026-") {
		t.Errorf("date: %s %v", got, err)
	}
	for _, bad := range []string{"", "soon", "-3d", "0s"} {
		if _, err := parseSince(bad, now); exitCode(err) != exitUsage {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
