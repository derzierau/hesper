package projects

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Scratch projects (scratch.go). Every test uses temporary folders: never
// the user's ~/scratch or ~/projects.

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const day = 24 * time.Hour

// scratchEnv is a store with a scratch root, a projects root, a config
// folder and a fake clock, and the agents it is told about.
type scratchEnv struct {
	dir, root, projects, config string
	clock                       *testClock
	s                           *Store
	mu                          sync.Mutex
	agents                      []wire.Agent
}

func testGitEnv() []string {
	return append(gitEnv(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
}

func newScratchEnv(t *testing.T, machine string) *scratchEnv {
	t.Helper()
	dir := tempDir(t)
	e := &scratchEnv{dir: dir, root: filepath.Join(dir, "home", "scratch"), projects: filepath.Join(dir, "home", "projects"),
		config: filepath.Join(dir, "config"), clock: &testClock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local)}}
	e.s = e.open(t, machine)
	return e
}

func (e *scratchEnv) open(t *testing.T, machine string) *Store {
	t.Helper()
	os.MkdirAll(filepath.Join(e.dir, "state-"+machine), 0o700)
	s := Open(Options{StateDir: filepath.Join(e.dir, "state-"+machine), Machine: machine, Home: "/nonexistent-home", Logf: t.Logf,
		Now: e.clock.now, ScratchRoot: e.root, ProjectsRoot: e.projects, ConfigDir: e.config, GH: filepath.Join(e.dir, "no-gh"),
		GitEnv: testGitEnv()})
	t.Cleanup(s.Close)
	s.SetAgents(func() []wire.Agent {
		e.mu.Lock()
		defer e.mu.Unlock()
		return append([]wire.Agent(nil), e.agents...)
	})
	return s
}

func (e *scratchEnv) setAgents(list ...wire.Agent) {
	e.mu.Lock()
	e.agents = list
	e.mu.Unlock()
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = testGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func codeOf(err error) string {
	var we *wire.Error
	if errors.As(err, &we) {
		return we.Code
	}
	return ""
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestScratchName(t *testing.T) {
	for _, c := range []struct{ name, task, display, slug string }{
		{"", "CSV cleanup", "csv cleanup", "csv-cleanup"},
		{"", "Clean up the CSV export, then the JSON one\nand more", "clean up the csv export then the json", "clean-up-the-csv-export-then-the-json"},
		{"My Spike!", "ignored task", "My Spike!", "my-spike"},
		{"日本", "", "日本", "scratch"},
	} {
		display, slug, err := scratchName(c.name, c.task)
		if err != nil || display != c.display || slug != c.slug {
			t.Errorf("%q/%q: %q %q %v", c.name, c.task, display, slug, err)
		}
	}
	if _, slug, _ := scratchName("", strings.Repeat("word ", 30)); len(slug) > 40 {
		t.Errorf("slug %d characters", len(slug))
	}
	if _, _, err := scratchName("", "  ...  "); codeOf(err) != wire.CodeInvalid {
		t.Errorf("no words: %v", err)
	}
}

func TestScratchCreate(t *testing.T) {
	e := newScratchEnv(t, "L")
	id, path, err := e.s.CreateScratch("", "CSV cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(e.root, "2026-10-08-csv-cleanup") {
		t.Fatalf("path %s", path)
	}
	if n := gitOut(t, path, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("commits: %s", n)
	}
	if remotes := gitOut(t, path, "remote"); remotes != "" {
		t.Fatalf("remotes: %q", remotes)
	}
	if branch := gitOut(t, path, "branch", "--show-current"); branch != "main" {
		t.Fatalf("branch %s", branch)
	}
	p, err := e.s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != wire.ProjectScratch || p.Name != "csv cleanup" || p.Paths["L"] != path || p.Scratch == nil ||
		*p.Scratch != (wire.ScratchInfo{State: wire.ScratchResting, Home: "L", Git: true}) || !p.Created.Equal(e.clock.now()) {
		t.Fatalf("project %+v %+v", p, p.Scratch)
	}
	// An agent's folder in it is the scratch project; it is active.
	if got := e.s.Resolve(filepath.Join(path, "sub")); got != id {
		t.Fatalf("resolve %s, want %s", got, id)
	}
	e.setAgents(wire.Agent{ID: "L/a1", Machine: "L", ProjectID: id, Project: path, State: wire.StateWorking})
	var found bool
	for _, q := range e.s.List() {
		if q.ID == id {
			found = q.Scratch.State == wire.ScratchActive
		}
	}
	if !found {
		t.Fatal("not active with an agent")
	}

	// Unique by date-slug: -2, -3; an archived one's name is taken too.
	_, second, err := e.s.CreateScratch("csv cleanup", "")
	if err != nil || second != path+"-2" {
		t.Fatalf("second %s %v", second, err)
	}
	os.MkdirAll(filepath.Join(e.root, archiveDir, "2026-10-08-csv-cleanup-3"), 0o755)
	if _, third, err := e.s.CreateScratch("", "csv cleanup"); err != nil || third != path+"-4" {
		t.Fatalf("third %s %v", third, err)
	}
	if _, _, err := e.s.CreateScratch("", ""); codeOf(err) != wire.CodeInvalid {
		t.Fatalf("no name: %v", err)
	}

	// projects.scratch over Call; persisted.
	raw, _ := json.Marshal(wire.ScratchParams{Name: "spike"})
	res, err, ok := e.s.Call("projects.scratch", raw)
	if !ok || err != nil {
		t.Fatal(err)
	}
	made := res.(wire.ScratchResult)
	if made.Path != filepath.Join(e.root, "2026-10-08-spike") || made.Project.Scratch == nil {
		t.Fatalf("projects.scratch %+v", made)
	}
	e.s.Close()
	s2 := e.open(t, "L")
	if p, err := s2.Get(made.Project.ID); err != nil || p.Scratch == nil || p.Scratch.Home != "L" || p.Created.IsZero() {
		t.Fatalf("reloaded %+v %v", p, err)
	}

	// Off without a scratch root.
	off := Open(Options{Machine: "L", Home: "/nonexistent-home", Logf: t.Logf})
	defer off.Close()
	if _, _, err := off.CreateScratch("x", ""); codeOf(err) != wire.CodeUnavailable {
		t.Fatalf("off: %v", err)
	}
}

func TestScratchAdoption(t *testing.T) {
	e := newScratchEnv(t, "L")
	notes := filepath.Join(e.root, "2025-01-02-old-notes")
	write(t, notes, map[string]string{"notes.md": "hello"})
	repo := filepath.Join(e.root, "2025-03-04-tool")
	newRepo(t, repo, "", map[string]string{"main.go": "package main"})
	empty := filepath.Join(e.root, "2025-05-06")
	os.MkdirAll(empty, 0o755)
	undated := filepath.Join(e.root, "misc")
	os.MkdirAll(undated, 0o755)
	cloned := filepath.Join(e.root, "2025-06-07-clone")
	newRepo(t, cloned, "git@github.com:o/clone.git", nil)
	// The repository was a project already (an agent ran there): it stays
	// that project, now of kind scratch.
	before := e.s.Resolve(repo)
	removed := filepath.Join(e.root, "2025-07-08-removed")
	newRepo(t, removed, "", nil)
	e.s.Remove(e.s.Resolve(removed))

	e.s.Sweep(e.clock.now())
	byPath := map[string]wire.ProjectInfo{}
	for _, p := range e.s.ListArchived(true) {
		byPath[p.Paths["L"]] = p
	}
	n := byPath[notes]
	if n.Kind != wire.ProjectScratch || n.Name != "old notes" || n.Scratch == nil || n.Scratch.Git || n.Scratch.State != wire.ScratchResting ||
		n.Created.Local().Format("2006-01-02") != "2025-01-02" {
		t.Fatalf("notes %+v %+v", n, n.Scratch)
	}
	if exists(filepath.Join(notes, ".git")) {
		t.Fatal("a folder with content got a repository")
	}
	if r := byPath[repo]; r.ID != before || r.Kind != wire.ProjectScratch || r.Name != "tool" || !r.Scratch.Git {
		t.Fatalf("repo %+v (was %s)", r, before)
	}
	if r := byPath[empty]; r.Kind != wire.ProjectScratch || r.Name != "2025-05-06" || !r.Scratch.Git || !exists(filepath.Join(empty, ".git")) {
		t.Fatalf("empty %+v", r)
	}
	for _, p := range []string{undated, cloned, removed} {
		if q, ok := byPath[p]; ok && q.Kind == wire.ProjectScratch {
			t.Errorf("%s adopted: %+v", p, q)
		}
	}
	// Adopted: resting from now, whatever their date, so a first start
	// archives nothing; then the lifecycle applies.
	e.s.Sweep(e.clock.now())
	if !exists(notes) {
		t.Fatal("archived at once")
	}
	e.clock.advance(15 * day)
	e.s.Sweep(e.clock.now())
	if exists(notes) || !exists(filepath.Join(e.root, archiveDir, "2025-01-02-old-notes", "notes.md")) {
		t.Fatal("not archived after 15 days")
	}
}

func TestScratchLifecycle(t *testing.T) {
	e := newScratchEnv(t, "L")
	id, path, err := e.s.CreateScratch("", "lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	kept, keptPath, _ := e.s.CreateScratch("", "kept one")
	if _, err := e.s.KeepScratch(kept, true); err != nil {
		t.Fatal(err)
	}
	state := func() *wire.ScratchInfo {
		p, err := e.s.Get(id)
		if err != nil {
			return nil
		}
		return p.Scratch
	}

	// Active: never archived; its lastUsed follows.
	e.setAgents(wire.Agent{ID: "L/a1", Machine: "L", Project: filepath.Join(path, "x"), State: wire.StateIdle})
	e.clock.advance(20 * day)
	e.s.Sweep(e.clock.now())
	if st := state(); st.State != wire.ScratchActive || !exists(path) {
		t.Fatalf("active: %+v", st)
	}
	// Resting: archived after 14 days (an exited agent does not count).
	e.setAgents(wire.Agent{ID: "L/a1", Machine: "L", ProjectID: id, Project: path, State: wire.StateExited, Exit: &wire.Exit{}})
	e.clock.advance(13 * day)
	e.s.Sweep(e.clock.now())
	if st := state(); st.State != wire.ScratchResting || !exists(path) {
		t.Fatalf("13 days: %+v", st)
	}
	e.clock.advance(day + time.Minute)
	e.s.Sweep(e.clock.now())
	archived := filepath.Join(e.root, archiveDir, filepath.Base(path))
	st := state()
	if st.State != wire.ScratchArchived || st.ArchivedAt == nil || exists(path) || !exists(filepath.Join(archived, ".git")) {
		t.Fatalf("14 days: %+v", st)
	}
	for _, p := range e.s.List() {
		if p.ID == id {
			t.Fatal("archived in projects.list")
		}
	}
	// Its sessions' folders still belong to it.
	if got := e.s.Resolve(filepath.Join(path, "sub")); got != id {
		t.Fatalf("archived resolve %s", got)
	}

	// Restore: back, resting, its days start again; archive by hand.
	if p, err := e.s.RestoreScratch(id); err != nil || p.Scratch.State != wire.ScratchResting || p.Paths["L"] != path || !exists(path) {
		t.Fatalf("restore %+v %v", p, err)
	}
	e.s.Sweep(e.clock.now())
	if !exists(path) {
		t.Fatal("archived right after the restore")
	}
	if _, err := e.s.ArchiveScratch(id); err != nil || !exists(archived) {
		t.Fatalf("archive: %v", err)
	}

	// Deleted 30 days after it was archived: folder and catalog entry.
	e.clock.advance(29 * day)
	e.s.Sweep(e.clock.now())
	if !exists(archived) {
		t.Fatal("deleted after 29 days")
	}
	e.clock.advance(day + time.Minute)
	e.s.Sweep(e.clock.now())
	if exists(archived) {
		t.Fatal("not deleted after 30 days")
	}
	if _, err := e.s.Get(id); codeOf(err) != wire.CodeNotFound {
		t.Fatalf("get: %v", err)
	}
	// History: "folder removed", by id and (re-resolved) by folder.
	if !e.s.FolderRemoved(id, path) || !e.s.FolderRemoved(wire.ScratchPrefix+path+"/sub", path+"/sub") || e.s.FolderRemoved(kept, keptPath) {
		t.Fatal("folder removed")
	}

	// Kept: neither archived nor deleted.
	if p, err := e.s.Get(kept); err != nil || p.Scratch.State != wire.ScratchResting || !p.Scratch.Keep || !exists(keptPath) {
		t.Fatalf("kept %+v %v", p, err)
	}
}

func TestScratchSettings(t *testing.T) {
	e := newScratchEnv(t, "L")
	if got := e.s.ScratchSettings(); got != (wire.ScratchSettings{ArchiveAfterDays: 14, DeleteAfterDays: 30}) {
		t.Fatalf("defaults %+v", got)
	}
	os.MkdirAll(e.config, 0o700)
	os.WriteFile(filepath.Join(e.config, "settings.json"), []byte(`{"machine":"L","trustProjects":false}`), 0o600)
	raw, _ := json.Marshal(wire.ScratchSettings{ArchiveAfterDays: 3})
	res, err, _ := e.s.Call("projects.scratchSettings", raw)
	if err != nil || res.(wire.ScratchSettings) != (wire.ScratchSettings{ArchiveAfterDays: 3, DeleteAfterDays: 30}) {
		t.Fatalf("set %+v %v", res, err)
	}
	data, _ := os.ReadFile(filepath.Join(e.config, "settings.json"))
	var f map[string]any
	json.Unmarshal(data, &f)
	if f["machine"] != "L" || f["trustProjects"] != false || f["scratch"].(map[string]any)["archiveAfterDays"] != 3.0 {
		t.Fatalf("settings.json %s", data)
	}
	if _, err := e.s.SetScratchSettings(wire.ScratchSettings{DeleteAfterDays: -1}); codeOf(err) != wire.CodeInvalid {
		t.Fatalf("bad days: %v", err)
	}
	// settings.get / settings.set (the app's names).
	raw, _ = json.Marshal(wire.SettingsValues{Values: map[string]int{wire.SettingScratchDeleteDays: 45}})
	if res, err, ok := e.s.Call("settings.set", raw); !ok || err != nil || res.(wire.SettingsValues).Values[wire.SettingScratchDeleteDays] != 45 {
		t.Fatalf("settings.set %+v %v", res, err)
	}
	raw, _ = json.Marshal(wire.SettingsGetParams{Keys: []string{wire.SettingScratchArchiveDays, wire.SettingScratchDeleteDays, "other"}})
	res, err, _ = e.s.Call("settings.get", raw)
	if got := res.(wire.SettingsValues).Values; err != nil || len(got) != 2 || got[wire.SettingScratchArchiveDays] != 3 || got[wire.SettingScratchDeleteDays] != 45 {
		t.Fatalf("settings.get %+v %v", res, err)
	}
	for _, bad := range []map[string]int{{"other": 3}, {wire.SettingScratchArchiveDays: 0}} {
		raw, _ = json.Marshal(wire.SettingsValues{Values: bad})
		if _, err, _ := e.s.Call("settings.set", raw); codeOf(err) != wire.CodeInvalid {
			t.Fatalf("settings.set %v: %v", bad, err)
		}
	}
	// projects.scratchKeep of an unknown id fails (the app probes with it).
	raw, _ = json.Marshal(wire.ScratchKeepParams{ID: "p-0000000000000000", Keep: true})
	if _, err, _ := e.s.Call("projects.scratchKeep", raw); codeOf(err) != wire.CodeNotFound {
		t.Fatalf("keep unknown: %v", err)
	}
	e.s.Close()
	s2 := e.open(t, "L")
	if got := s2.ScratchSettings(); got.ArchiveAfterDays != 3 {
		t.Fatalf("reloaded %+v", got)
	}
	// 3 days now.
	id, path, _ := s2.CreateScratch("", "short")
	e.clock.advance(3*day + time.Minute)
	s2.Sweep(e.clock.now())
	if p, _ := s2.Get(id); p.Scratch.State != wire.ScratchArchived || exists(path) {
		t.Fatalf("not archived after 3 days: %+v", p.Scratch)
	}
}

func TestScratchPromote(t *testing.T) {
	e := newScratchEnv(t, "L")
	id, path, _ := e.s.CreateScratch("", "csv cleanup")
	write(t, path, map[string]string{"notes.md": "x"})
	gitRun(t, path, "add", "-A")
	gitRun(t, path, "commit", "-q", "--no-gpg-sign", "-m", "work")

	// Busy while an agent runs in it.
	e.setAgents(wire.Agent{ID: "L/a1", Machine: "L", ProjectID: id, Project: path, State: wire.StateIdle})
	if _, err := e.s.Promote(wire.ProjectPromoteParams{ID: id}); codeOf(err) != wire.CodeBusy || !exists(path) {
		t.Fatalf("busy: %v", err)
	}
	e.setAgents()
	// No gh: refused before anything moves.
	if _, err := e.s.Promote(wire.ProjectPromoteParams{ID: id, CreateRepo: wire.CreateRepoGitHub}); codeOf(err) != wire.CodeUnavailable || !exists(path) {
		t.Fatalf("no gh: %v", err)
	}
	if _, err := e.s.Promote(wire.ProjectPromoteParams{ID: id, CreateRepo: "gitlab"}); codeOf(err) != wire.CodeInvalid {
		t.Fatalf("createRepo: %v", err)
	}
	os.MkdirAll(filepath.Join(e.projects, "taken"), 0o755)
	if _, err := e.s.Promote(wire.ProjectPromoteParams{ID: id, Name: "taken"}); codeOf(err) != wire.CodeExists || !exists(path) {
		t.Fatalf("taken: %v", err)
	}

	p, err := e.s.Promote(wire.ProjectPromoteParams{ID: id, Name: "CSV Tool"})
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(e.projects, "csv-tool")
	if p.ID != id || p.Kind != wire.ProjectRepo || p.Name != "CSV Tool" || p.Paths["L"] != dst || p.Scratch != nil || exists(path) {
		t.Fatalf("promoted %+v", p)
	}
	if n := gitOut(t, dst, "rev-list", "--count", "HEAD"); n != "2" {
		t.Fatalf("history: %s commits", n)
	}
	if got := e.s.Resolve(dst); got != id {
		t.Fatalf("resolve %s", got)
	}
	// Not a scratch any more: not swept, not promoted by id again.
	e.clock.advance(100 * day)
	e.s.Sweep(e.clock.now())
	if !exists(dst) {
		t.Fatal("swept")
	}
	if _, err := e.s.Promote(wire.ProjectPromoteParams{ID: id}); codeOf(err) != wire.CodeInvalid {
		t.Fatalf("again: %v", err)
	}
}

func TestScratchPromoteGitHub(t *testing.T) {
	e := newScratchEnv(t, "L")
	// A fake gh: records its arguments and adds the remote it would have.
	log := filepath.Join(e.dir, "gh.log")
	gh := filepath.Join(e.dir, "gh")
	os.WriteFile(gh, []byte("#!/bin/sh\necho \"$PWD $*\" > "+log+"\ngit remote add origin git@github.com:me/$3.git\n"), 0o755)
	s := Open(Options{StateDir: filepath.Join(e.dir, "state-gh"), Machine: "L", Home: "/nonexistent-home", Logf: t.Logf, Now: e.clock.now,
		ScratchRoot: e.root, ProjectsRoot: e.projects, GH: gh, GitEnv: testGitEnv()})
	defer s.Close()
	id, _, _ := s.CreateScratch("", "spike")
	p, err := s.Promote(wire.ProjectPromoteParams{ID: id, CreateRepo: wire.CreateRepoGitHub})
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(e.projects, "spike")
	data, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(data)); got != dst+" repo create spike --private --source . --push" {
		t.Fatalf("gh %q", got)
	}
	// It has a remote now and keeps its id.
	if got := s.Resolve(dst); got != id || p.ID != id {
		t.Fatalf("resolve %s, want %s", got, id)
	}
}

// The home Mac owns the folder: another Mac sees the scratch (home L),
// never sweeps it, forwards archive to L; keep replicates both ways.
func TestScratchReplication(t *testing.T) {
	e := newScratchEnv(t, "L")
	m := e.open(t, "M")
	id, path, _ := e.s.CreateScratch("", "shared")
	syncStores(e.s, m, "L", "M")
	p, err := m.Get(id)
	if err != nil || p.Scratch == nil || p.Scratch.Home != "L" || p.Kind != wire.ProjectScratch || p.Paths["L"] != path {
		t.Fatalf("on M: %+v %v", p, err)
	}
	e.clock.advance(40 * day)
	m.Sweep(e.clock.now())
	if !exists(path) {
		t.Fatal("M archived L's scratch")
	}
	if _, err := m.KeepScratch(id, true); err != nil {
		t.Fatal(err)
	}
	syncStores(m, e.s, "M", "L")
	e.s.Sweep(e.clock.now())
	if p, _ := e.s.Get(id); !p.Scratch.Keep || p.Scratch.State != wire.ScratchResting || !exists(path) {
		t.Fatalf("keep from M: %+v", p.Scratch)
	}
	m.KeepScratch(id, false)

	// M forwards archive and delete to L (host methods).
	var calls []string
	m.SetForward(func(ctx context.Context, machine, method string, params any) (json.RawMessage, error) {
		calls = append(calls, machine+" "+method)
		raw, _ := json.Marshal(params)
		res, err := e.s.ScratchForPeer(method, raw)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	})
	syncStores(m, e.s, "M", "L")
	got, err := m.ArchiveScratch(id)
	if err != nil || got.Scratch.State != wire.ScratchArchived || exists(path) {
		t.Fatalf("archive from M: %+v %v", got.Scratch, err)
	}
	if err := m.DeleteScratch(id); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(id); codeOf(err) != wire.CodeNotFound {
		t.Fatalf("deleted on M: %v", err)
	}
	if strings.Join(calls, ",") != "L projects.scratchArchive,L projects.scratchDelete" {
		t.Fatalf("forwarded %v", calls)
	}
	// projects.scratch on M for L.
	res, err := m.NewScratch(wire.ScratchParams{Machine: "L", Task: "made from M"})
	if err != nil || res.Path != filepath.Join(e.root, "2026-11-17-made-from-m") || res.Project.Scratch.Home != "L" {
		t.Fatalf("new on L from M: %+v %v", res, err)
	}
}

// A move: the target records the folder; a move (not a fork) makes it
// the home.
func TestScratchArrived(t *testing.T) {
	e := newScratchEnv(t, "L")
	m := e.open(t, "M")
	id, _, _ := e.s.CreateScratch("", "moving")
	local, name, created, ok := e.s.ScratchOf(id)
	if !ok || name != "moving" || created.IsZero() {
		t.Fatalf("scratch of: %q %q %v", local, name, ok)
	}
	if _, _, _, ok := e.s.ScratchOf("p-0000000000000000"); ok {
		t.Fatal("not a scratch")
	}
	target := filepath.Join(e.dir, "m-home", "scratch", "2026-10-08-moving")
	os.MkdirAll(target, 0o755)
	// Before any sync (the manifest carries what it needs), a fork:
	if got := m.ScratchArrived(local, name, created, target, false); got != id {
		t.Fatalf("arrived %s, want %s", got, id)
	}
	syncStores(e.s, m, "L", "M")
	if p, _ := m.Get(id); p.Scratch.Home != "L" || p.Paths["M"] != target {
		t.Fatalf("fork: %+v %v", p.Scratch, p.Paths)
	}
	// A move:
	m.ScratchArrived(local, name, created, target, true)
	syncStores(m, e.s, "M", "L")
	if p, _ := e.s.Get(id); p.Scratch.Home != "M" {
		t.Fatalf("move: home %s", p.Scratch.Home)
	}
	if m.ScratchArrived("../bad", "x", created, target, true) != "" {
		t.Fatal("a bad identity was taken")
	}
}
