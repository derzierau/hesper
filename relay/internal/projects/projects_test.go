package projects

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestNormalizeRemote(t *testing.T) {
	want := "github.com/owner/repo"
	for _, in := range []string{
		"git@github.com:owner/repo.git",
		"git@GitHub.com:Owner/Repo.git",
		"https://github.com/owner/repo",
		"https://github.com/owner/repo.git",
		"https://user:token@github.com/owner/repo/",
		"http://GITHUB.COM/owner/repo.git/",
		"ssh://git@github.com/owner/repo.git",
		"ssh://git@github.com:22/owner/repo",
		"git+ssh://git@github.com/owner/repo.git",
		"git://github.com/owner/repo.git",
		"  github.com:owner/repo  ",
		"ssh://git@ssh.github.com:443/owner/repo.git",
	} {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"git@git.example.org:Team/Tool.git":          "git.example.org/Team/Tool", // case kept off the big hosts
		"https://GIT.example.org:8443/Team/Tool.git": "git.example.org/Team/Tool",
		"git@gitlab.com:Group/Sub/Proj.git":          "gitlab.com/group/sub/proj",
		"file:///srv/git/repo.git":                   "file:/srv/git/repo.git",
		"/srv/git/repo":                              "file:/srv/git/repo",
		"../relative/repo":                           "file:../relative/repo",
		"":                                           "",
	} {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	a := ProjectID(wire.ProjectIdentity{Remote: NormalizeRemote("git@github.com:o/r.git")})
	b := ProjectID(wire.ProjectIdentity{Remote: NormalizeRemote("https://github.com/O/R")})
	if a != b || !strings.HasPrefix(a, "p-") || len(a) != 18 {
		t.Fatalf("ids %q %q", a, b)
	}
	if ProjectID(wire.ProjectIdentity{Remote: "github.com/o/r", Package: "packages/x"}) == a {
		t.Fatal("a package has its repository's id")
	}
}

// --- fixtures ---

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(gitEnv(os.Environ()), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newRepo makes a repository (one commit) with origin remote.
func newRepo(t *testing.T, dir, remote string, files map[string]string) string {
	t.Helper()
	write(t, dir, files)
	gitRun(t, dir, "init", "-q", "-b", "main")
	if remote != "" {
		gitRun(t, dir, "remote", "add", "origin", remote)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return canonical(dir)
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func tempDir(t *testing.T) string {
	return canonical(t.TempDir())
}

func openStore(t *testing.T, state, machine string) *Store {
	t.Helper()
	s := Open(Options{StateDir: state, Machine: machine, Home: "/nonexistent-home", Logf: t.Logf})
	t.Cleanup(s.Close)
	return s
}

// --- detection ---

func TestWorktreeMapsToItsRepository(t *testing.T) {
	root := tempDir(t)
	repo := newRepo(t, filepath.Join(root, "app"), "git@github.com:acme/app.git", map[string]string{"README": "x", "src/main.go": "package main"})
	wt := filepath.Join(root, "worktrees", "app-feature")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	s := openStore(t, "", "L")
	want := ProjectID(wire.ProjectIdentity{Remote: "github.com/acme/app"})
	for _, dir := range []string{repo, filepath.Join(repo, "src"), wt, filepath.Join(wt, "src")} {
		if got := s.Resolve(dir); got != want {
			t.Errorf("%s → %s, want %s", dir, got, want)
		}
	}
	p, err := s.Get(want)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != wire.ProjectRepo || p.Name != "app" || p.Paths["L"] != repo || p.Identity.Remote != "github.com/acme/app" {
		t.Fatalf("project %+v", p)
	}
	// Cached: asking again runs no git.
	calls := s.det.calls
	for i := 0; i < 20; i++ {
		s.Resolve(filepath.Join(wt, "src"))
		s.Touch(wt, time.Now())
		s.List()
	}
	if s.det.calls != calls {
		t.Fatalf("%d git calls for cached folders", s.det.calls-calls)
	}
	// A repository without a remote: a generated identity, the same on
	// every resolve.
	plain := newRepo(t, filepath.Join(root, "plain"), "", map[string]string{"a": "b"})
	id := s.Resolve(plain)
	if !strings.HasPrefix(id, "p-") || s.Resolve(filepath.Join(plain)) != id {
		t.Fatalf("plain repo %s", id)
	}
	if p, _ := s.Get(id); p.Identity.Local == "" || p.Identity.Remote != "" {
		t.Fatalf("plain identity %+v", p.Identity)
	}
}

func TestPackagesPerEcosystem(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []wire.DetectedPackage
	}{
		{"pnpm", map[string]string{
			"package.json":                   `{"name":"root"}`,
			"pnpm-workspace.yaml":            "packages:\n  - 'packages/*'\n  - \"apps/**\" # all apps\n  - '!**/fixtures/**'\n",
			"packages/ui/package.json":       `{"name":"@acme/ui"}`,
			"packages/nopkg/README":          "no manifest",
			"apps/web/package.json":          `{"name":"web"}`,
			"apps/web/fixtures/package.json": `{"name":"fixture"}`,
		}, []wire.DetectedPackage{{Path: "apps/web", Name: "web", Tool: "pnpm"}, {Path: "packages/ui", Name: "@acme/ui", Tool: "pnpm"}}},
		{"npm", map[string]string{
			"package.json":                `{"name":"root","workspaces":["packages/*"]}`,
			"packages/a/package.json":     `{"name":"a"}`,
			"packages/b/package.json":     `{}`,
			"node_modules/x/package.json": `{"name":"x"}`,
		}, []wire.DetectedPackage{{Path: "packages/a", Name: "a", Tool: "npm"}, {Path: "packages/b", Name: "b", Tool: "npm"}}},
		{"yarn", map[string]string{
			"package.json":           `{"workspaces":{"packages":["libs/*"]}}`,
			"libs/core/package.json": `{"name":"core"}`,
		}, []wire.DetectedPackage{{Path: "libs/core", Name: "core", Tool: "npm"}}},
		{"turbo", map[string]string{
			"package.json":           `{"workspaces":["apps/*"]}`,
			"turbo.json":             `{}`,
			"apps/docs/package.json": `{"name":"docs"}`,
		}, []wire.DetectedPackage{{Path: "apps/docs", Name: "docs", Tool: "turbo"}}},
		{"lerna", map[string]string{
			"lerna.json":                 `{"version":"1.0.0"}`,
			"packages/tool/package.json": `{"name":"tool"}`,
		}, []wire.DetectedPackage{{Path: "packages/tool", Name: "tool", Tool: "lerna"}}},
		{"nx", map[string]string{
			"nx.json":                     `{}`,
			"apps/shop/project.json":      `{"name":"shop"}`,
			"libs/data/project.json":      `{}`,
			"libs/data/package.json":      `{"name":"@acme/data"}`,
			"dist/apps/shop/project.json": `{"name":"built"}`,
		}, []wire.DetectedPackage{{Path: "apps/shop", Name: "shop", Tool: "nx"}, {Path: "libs/data", Name: "@acme/data", Tool: "nx"}}},
		{"go.work", map[string]string{
			"go.work":          "go 1.22\n\nuse (\n\t./api\n\t./tools/cli // the CLI\n)\nuse ./web\n",
			"api/go.mod":       "module example.com/acme/api\n",
			"tools/cli/go.mod": "module example.com/acme/cli\n",
			"web/go.mod":       "module \"example.com/acme/web\"\n",
		}, []wire.DetectedPackage{{Path: "api", Name: "api", Tool: "go"}, {Path: "tools/cli", Name: "cli", Tool: "go"}, {Path: "web", Name: "web", Tool: "go"}}},
		{"cargo", map[string]string{
			"Cargo.toml":             "[workspace]\nmembers = [\n  \"crates/*\", # all\n  \"bin\",\n]\nexclude = [\"crates/old\"]\n\n[workspace.package]\nversion = \"1.0.0\"\n",
			"crates/core/Cargo.toml": "[package]\nname = \"acme-core\"\nversion = \"0.1.0\"\n",
			"crates/old/Cargo.toml":  "[package]\nname = \"old\"\n",
			"bin/Cargo.toml":         "[package]\nname = 'acme-bin'\n",
		}, []wire.DetectedPackage{{Path: "bin", Name: "acme-bin", Tool: "cargo"}, {Path: "crates/core", Name: "acme-core", Tool: "cargo"}}},
		{"none", map[string]string{"package.json": `{"name":"single"}`, "go.mod": "module x\n"}, []wire.DetectedPackage{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := tempDir(t)
			write(t, dir, c.files)
			got := DetectPackages(dir)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

// An agent inside a package joins the package (deepest); packages are
// only made projects when an agent works in one; the repository lists
// them as detected.
func TestAgentInPackageJoinsThePackage(t *testing.T) {
	root := tempDir(t)
	repo := newRepo(t, filepath.Join(root, "mono"), "https://github.com/acme/mono.git", map[string]string{
		"pnpm-workspace.yaml":       "packages:\n  - packages/*\n",
		"packages/ui/package.json":  `{"name":"@acme/ui"}`,
		"packages/api/package.json": `{"name":"@acme/api"}`,
	})
	s := openStore(t, "", "L")
	repoID := s.Resolve(repo)
	if len(s.List()) != 1 {
		t.Fatalf("packages listed before an agent works in one: %+v", s.List())
	}
	pkgID := s.Resolve(filepath.Join(repo, "packages", "ui", "src"))
	if pkgID == repoID || !strings.HasPrefix(pkgID, "p-") {
		t.Fatalf("package %s repo %s", pkgID, repoID)
	}
	p, _ := s.Get(pkgID)
	if p.Kind != wire.ProjectPackage || p.Name != "@acme/ui" || p.ParentID != repoID || p.Identity.Package != "packages/ui" ||
		p.Identity.Remote != "github.com/acme/mono" || p.Paths["L"] != filepath.Join(repo, "packages/ui") {
		t.Fatalf("package %+v", p)
	}
	var repoView wire.ProjectInfo
	for _, x := range s.List() {
		if x.ID == repoID {
			repoView = x
		}
	}
	if len(repoView.DetectedPackages) != 2 || repoView.DetectedPackages[0].Path != "packages/api" {
		t.Fatalf("detected %+v", repoView.DetectedPackages)
	}
	// The same package in a worktree is the same project.
	wt := filepath.Join(root, "wt")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "x", wt)
	if got := s.Resolve(filepath.Join(wt, "packages", "ui")); got != pkgID {
		t.Fatalf("worktree package %s, want %s", got, pkgID)
	}
}

func TestDeepestProjectScratchPromoteRemove(t *testing.T) {
	root := tempDir(t)
	repo := newRepo(t, filepath.Join(root, "app"), "git@github.com:acme/app.git", map[string]string{
		"package.json":            `{"workspaces":["packages/*"]}`,
		"packages/a/package.json": `{"name":"a"}`,
		"docs/guide/x.md":         "x",
	})
	s := openStore(t, "", "L")
	repoID := s.Resolve(repo)
	// A plain folder: scratch, by folder.
	plain := filepath.Join(root, "notes", "today")
	os.MkdirAll(plain, 0o755)
	if got := s.Resolve(plain); got != wire.ScratchPrefix+plain {
		t.Fatalf("scratch %s", got)
	}
	// Promote the folder: agents there join it.
	notes, err := s.Promote(wire.ProjectPromoteParams{Path: filepath.Join(root, "notes"), Name: "Notes", Kind: "reference"})
	if err != nil {
		t.Fatal(err)
	}
	if notes.Kind != wire.ProjectReference || notes.Name != "Notes" || notes.Identity.Local == "" || s.Resolve(plain) != notes.ID {
		t.Fatalf("promoted %+v → %s", notes, s.Resolve(plain))
	}
	// Promoting again finds the same project.
	again, _ := s.Promote(wire.ProjectPromoteParams{Path: filepath.Join(root, "notes")})
	if again.ID != notes.ID {
		t.Fatalf("promoted twice: %s %s", again.ID, notes.ID)
	}
	// A folder inside the repository, promoted: deeper than the repo.
	docs, err := s.Promote(wire.ProjectPromoteParams{Path: filepath.Join(repo, "docs")})
	if err != nil {
		t.Fatal(err)
	}
	if docs.Kind != wire.ProjectFolder || docs.Identity.Package != "docs" || docs.ParentID != repoID {
		t.Fatalf("docs %+v", docs)
	}
	if got := s.Resolve(filepath.Join(repo, "docs", "guide")); got != docs.ID {
		t.Fatalf("docs/guide → %s, want %s", got, docs.ID)
	}
	pkgID := s.Resolve(filepath.Join(repo, "packages", "a"))
	if s.Resolve(filepath.Join(repo, "src")) != repoID {
		t.Fatal("repo root folder")
	}
	// Removing the deeper one falls back to the repository.
	if err := s.Remove(docs.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.Resolve(filepath.Join(repo, "docs", "guide")); got != repoID {
		t.Fatalf("after removing docs: %s", got)
	}
	// Removing the repository: its package stays (deeper); the rest is
	// scratch, and agents starting there do not bring it back.
	if err := s.Remove(repoID); err != nil {
		t.Fatal(err)
	}
	if got := s.Resolve(filepath.Join(repo, "packages", "a")); got != pkgID {
		t.Fatalf("package after removing the repo: %s", got)
	}
	if got := s.Resolve(filepath.Join(repo, "src")); got != wire.ScratchPrefix+filepath.Join(repo, "src") {
		t.Fatalf("after removing the repo: %s", got)
	}
	if _, err := s.Get(repoID); err == nil {
		t.Fatal("removed repo still there")
	}
	// Promote brings it back.
	back, err := s.Promote(wire.ProjectPromoteParams{Path: repo})
	if err != nil || back.ID != repoID || s.Resolve(filepath.Join(repo, "src")) != repoID {
		t.Fatalf("revived %+v %v", back, err)
	}
	// Scratch is not a project for update/remove.
	if err := s.Remove(wire.ScratchPrefix + plain); err == nil {
		t.Fatal("removed scratch")
	}
	if _, err := s.Promote(wire.ProjectPromoteParams{Path: "relative"}); err == nil {
		t.Fatal("relative path promoted")
	}
	if _, err := s.Promote(wire.ProjectPromoteParams{Path: filepath.Join(root, "missing")}); err == nil {
		t.Fatal("missing folder promoted")
	}
	// Scratch projects in projects.list come from the agents.
	s.SetAgents(func() []wire.Agent {
		return []wire.Agent{{ID: "L/a", Machine: "L", ProjectID: wire.ScratchPrefix + "/tmp/x"}, {ID: "M/b", Machine: "M", ProjectID: wire.ScratchPrefix + "/tmp/x"}}
	})
	list := s.List()
	last := list[len(list)-1]
	if last.Kind != wire.ProjectScratch || last.Name != "x" || last.Paths["L"] != "/tmp/x" || last.Paths["M"] != "/tmp/x" {
		t.Fatalf("scratch view %+v", last)
	}
}

func TestHomeRepositoryIsNotAProjectByItself(t *testing.T) {
	home := tempDir(t)
	newRepo(t, home, "git@github.com:me/dotfiles.git", map[string]string{".zshrc": "x"})
	s := Open(Options{Home: home, Logf: t.Logf})
	defer s.Close()
	dir := filepath.Join(home, "scratch")
	os.MkdirAll(dir, 0o755)
	if got := s.Resolve(dir); got != wire.ScratchPrefix+dir {
		t.Fatalf("home repo: %s", got)
	}
}

func TestUpdateGroupsPersistence(t *testing.T) {
	state := tempDir(t)
	root := tempDir(t)
	repo := newRepo(t, filepath.Join(root, "app"), "git@github.com:acme/app.git", nil)
	os.WriteFile(filepath.Join(state, "projects.json"), []byte(`[{"path":"/old/folder","name":"folder","lastUsed":"2026-01-01T00:00:00Z"}]`), 0o600)
	s := Open(Options{StateDir: state, Machine: "L", Home: "/nonexistent", Logf: t.Logf})
	id := s.Resolve(repo)
	s.Touch(repo, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	name, color, kind := "App", "#123ABC", "reference"
	p, err := s.Update(wire.ProjectUpdateParams{ID: id, Name: &name, Color: &color, Kind: &kind, Defaults: &wire.ProjectDefaults{Profile: "codex-full", Machine: "M"}})
	if err != nil || p.Name != "App" || p.Color != "#123abc" || !p.ColorSet || p.Kind != "reference" || p.Defaults.Profile != "codex-full" {
		t.Fatalf("update %+v %v", p, err)
	}
	bad := "red"
	if _, err := s.Update(wire.ProjectUpdateParams{ID: id, Color: &bad}); err == nil {
		t.Fatal("bad color")
	}
	g, err := s.SaveGroup(wire.Group{Name: "tools", ProjectIDs: []string{id, id}, Order: 2})
	if err != nil || !strings.HasPrefix(g.ID, "g-") || len(g.ProjectIDs) != 1 {
		t.Fatalf("group %+v %v", g, err)
	}
	if _, err := s.SaveGroup(wire.Group{Name: "x", ProjectIDs: []string{"p-nope"}}); err == nil {
		t.Fatal("group with an unknown project")
	}
	recent := s.Recent()
	if len(recent) != 2 || recent[0].ProjectID != id || recent[0].Path != repo || recent[1].Path != "/old/folder" {
		t.Fatalf("recent %+v", recent)
	}
	node := s.Node()
	s.Close()
	// Back from the file.
	s2 := Open(Options{StateDir: state, Machine: "L", Home: "/nonexistent", Logf: t.Logf})
	defer s2.Close()
	if s2.Node() != node {
		t.Fatal("node id changed")
	}
	p2, err := s2.Get(id)
	if err != nil || p2.Name != "App" || p2.Color != "#123abc" || p2.Paths["L"] != repo || !reflect.DeepEqual(p2.Groups, []string{g.ID}) {
		t.Fatalf("reloaded %+v %v", p2, err)
	}
	if gs := s2.Groups(); len(gs) != 1 || gs[0].Name != "tools" || gs[0].Order != 2 {
		t.Fatalf("groups %+v", gs)
	}
	if r := s2.Recent(); len(r) != 2 || r[1].Path != "/old/folder" {
		t.Fatalf("recent %+v", r)
	}
	st, _ := os.Stat(filepath.Join(state, "projects.json"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if err := s2.RemoveGroup(g.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := s2.Get(id); len(p.Groups) != 0 {
		t.Fatalf("groups after removal %+v", p.Groups)
	}
}

// --- replication ---

// sync exchanges states both ways, as a link and projects.sync do.
func syncStores(a, b *Store, aName, bName string) {
	b.Merge(a.Export(), aName)
	a.Merge(b.Export(), bName)
}

func TestMergeIdentityPathsLWWTombstones(t *testing.T) {
	root := tempDir(t)
	repoL := newRepo(t, filepath.Join(root, "L", "projects", "app"), "git@github.com:acme/app.git", nil)
	repoM := newRepo(t, filepath.Join(root, "M", "src", "the-app"), "https://github.com/Acme/App", nil)
	clock := time.Now()
	var cmu sync.Mutex
	now := func() time.Time { cmu.Lock(); defer cmu.Unlock(); return clock }
	advance := func() { cmu.Lock(); clock = clock.Add(time.Second); cmu.Unlock() }
	L := Open(Options{Machine: "L", Home: "/nonexistent", Now: now, Logf: t.Logf})
	M := Open(Options{Machine: "M", Home: "/nonexistent", Now: now, Logf: t.Logf})
	defer L.Close()
	defer M.Close()
	idL, idM := L.Resolve(repoL), M.Resolve(filepath.Join(repoM, "sub"))
	if idL != idM {
		t.Fatalf("same repository, ids %s %s", idL, idM)
	}
	syncStores(L, M, "L", "M")
	for _, s := range []*Store{L, M} {
		p, _ := s.Get(idL)
		if p.Paths["L"] != repoL || p.Paths["M"] != repoM || len(s.List()) != 1 {
			t.Fatalf("union of paths %+v", p.Paths)
		}
	}
	// Concurrent renames: the later one wins everywhere.
	advance()
	n1 := "from L"
	L.Update(wire.ProjectUpdateParams{ID: idL, Name: &n1})
	advance()
	n2 := "from M"
	M.Update(wire.ProjectUpdateParams{ID: idL, Name: &n2})
	syncStores(L, M, "L", "M")
	for _, s := range []*Store{L, M} {
		if p, _ := s.Get(idL); p.Name != "from M" {
			t.Fatalf("lww name %q", p.Name)
		}
	}
	// Different fields both survive.
	advance()
	c := "#aabbcc"
	L.Update(wire.ProjectUpdateParams{ID: idL, Color: &c})
	M.Update(wire.ProjectUpdateParams{ID: idL, Defaults: &wire.ProjectDefaults{Profile: "shell"}})
	syncStores(M, L, "M", "L")
	for _, s := range []*Store{L, M} {
		if p, _ := s.Get(idL); p.Color != c || p.Defaults.Profile != "shell" {
			t.Fatalf("fields %+v", p)
		}
	}
	// A group made on L arrives on M.
	g, _ := L.SaveGroup(wire.Group{Name: "acme", ProjectIDs: []string{idL}})
	syncStores(L, M, "L", "M")
	if gs := M.Groups(); len(gs) != 1 || gs[0].ID != g.ID || gs[0].ProjectIDs[0] != idL {
		t.Fatalf("groups on M %+v", gs)
	}
	// Removal on L beats an older rename on M that arrives later.
	advance()
	old := M.Export() // M's state before the removal
	advance()
	L.Remove(idL)
	L.Merge(old, "M")
	if _, err := L.Get(idL); err == nil {
		t.Fatal("an older copy revived the removed project")
	}
	syncStores(L, M, "L", "M")
	if _, err := M.Get(idL); err == nil {
		t.Fatal("tombstone did not travel")
	}
	// M's agents in the repository fall to scratch and do not bring it
	// back; a later promote on M does, everywhere.
	if got := M.Resolve(repoM); got != wire.ScratchPrefix+repoM {
		t.Fatalf("after remote removal: %s", got)
	}
	advance()
	if _, err := M.Promote(wire.ProjectPromoteParams{Path: repoM}); err != nil {
		t.Fatal(err)
	}
	syncStores(M, L, "M", "L")
	if p, err := L.Get(idL); err != nil || p.Paths["M"] != repoM {
		t.Fatalf("revived on L %+v %v", p, err)
	}
	// Converged: the same shared state in any order.
	a, _ := json.Marshal(L.Export().Projects)
	b, _ := json.Marshal(M.Export().Projects)
	if string(a) != string(b) {
		t.Fatalf("diverged\n%s\n%s", a, b)
	}
	// A record whose id is not its identity's is refused.
	bad := L.Export()
	bad.Node = "n-evil"
	bad.Projects[0].ID = "p-0000000000000000"
	if M.Merge(bad, "") && len(M.List()) != 1 {
		t.Fatal("forged id accepted")
	}
}

// Equal stamps (two machines registering the same repository with its
// default name) converge whatever the order.
func TestMergeOrderIndependent(t *testing.T) {
	mk := func(node, name string) *State {
		r := &Record{ID: ProjectID(wire.ProjectIdentity{Remote: "github.com/x/y"}), Identity: wire.ProjectIdentity{Remote: "github.com/x/y"},
			Name: Field[string]{V: name}, Kind: Field[string]{V: "repo"}, Paths: map[string]Field[string]{node: {V: "/p/" + node, S: Stamp{T: 5, N: node}}}}
		return &State{Node: node, Projects: []*Record{r}}
	}
	a, b := Open(Options{Home: "/x"}), Open(Options{Home: "/x"})
	defer a.Close()
	defer b.Close()
	s1, s2 := mk("n-1", "y"), mk("n-2", "Y")
	a.Merge(s1, "")
	a.Merge(s2, "")
	b.Merge(s2, "")
	b.Merge(s1, "")
	x, _ := json.Marshal(a.Export().Projects)
	y, _ := json.Marshal(b.Export().Projects)
	if string(x) != string(y) {
		t.Fatalf("order matters\n%s\n%s", x, y)
	}
}

func TestEvents(t *testing.T) {
	root := tempDir(t)
	repo := newRepo(t, filepath.Join(root, "app"), "git@github.com:acme/app.git", nil)
	s := openStore(t, "", "L")
	first := s.Resolve(repo)
	type note struct {
		method string
		params string
	}
	notes := make(chan note, 64)
	done := make(chan struct{})
	defer close(done)
	go s.Watch(done, func(method string, params any) error {
		data, _ := json.Marshal(params)
		notes <- note{method, string(data)}
		return nil
	})
	next := func(method string) string {
		t.Helper()
		select {
		case n := <-notes:
			if n.method != method {
				t.Fatalf("got %s %s, want %s", n.method, n.params, method)
			}
			return n.params
		case <-time.After(5 * time.Second):
			t.Fatalf("no %s", method)
		}
		return ""
	}
	// First every project.
	if p := next(wire.NoteProjectChanged); !strings.Contains(p, first) {
		t.Fatalf("initial %s", p)
	}
	name := "Renamed"
	s.Update(wire.ProjectUpdateParams{ID: first, Name: &name})
	if p := next(wire.NoteProjectChanged); !strings.Contains(p, `"name":"Renamed"`) {
		t.Fatalf("changed %s", p)
	}
	g, _ := s.SaveGroup(wire.Group{Name: "g", ProjectIDs: []string{first}})
	// The group, and the project whose groups changed (coalesced, any order).
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case n := <-notes:
			got[n.method] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("saw %v", got)
		}
	}
	if !got[wire.NoteGroupChanged] || !got[wire.NoteProjectChanged] {
		t.Fatalf("saw %v", got)
	}
	s.RemoveGroup(g.ID)
	got = map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case n := <-notes:
			got[n.method] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("saw %v", got)
		}
	}
	if !got[wire.NoteGroupRemoved] {
		t.Fatalf("saw %v", got)
	}
	s.Remove(first)
	if p := next(wire.NoteProjectRemoved); !strings.Contains(p, first) {
		t.Fatalf("removed %s", p)
	}
	// Re-resolution is requested when projects change.
	called := make(chan struct{}, 4)
	s.SetReproject(func() { called <- struct{}{} })
	s.Promote(wire.ProjectPromoteParams{Path: repo})
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("no reproject")
	}
}
