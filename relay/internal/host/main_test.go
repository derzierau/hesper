package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/fakeagent"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGENTS_FAKE") == "1" && len(os.Args) > 1 {
		fakeagent.Run(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// testRegistry is a registry named "mini" with fake agent profiles
// (fake-claude, fake-shell) and a trusted project; it returns the project.
func testRegistry(t *testing.T) (*agents.Registry, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	config, project := filepath.Join(dir, "c"), filepath.Join(dir, "proj")
	os.MkdirAll(config, 0o755)
	os.MkdirAll(project, 0o755)
	exe, _ := os.Executable()
	write := func(path string, v any) {
		data, _ := json.Marshal(v)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(config, "profiles.json"), map[string]wire.Profile{
		"fake-claude": {Kind: wire.KindClaude, Argv: []string{exe, "claude", "--remote-control", "{name}"}},
		"fake-shell":  {Kind: wire.KindShell, Argv: []string{exe, "shell"}},
	})
	write(filepath.Join(config, "settings.json"), map[string]any{"defaults": map[string]any{"kind": "claude", "kinds": map[string]string{"claude": "fake-claude", "shell": "fake-shell"}}})
	write(filepath.Join(dir, "claude.json"), map[string]any{"projects": map[string]any{project: map[string]any{"hasTrustDialogAccepted": true}}})
	state := filepath.Join(dir, "s")
	reg, err := agents.Open(agents.Options{StateDir: state, ConfigDir: config, Socket: filepath.Join(state, "hesperd.sock"), Machine: "mini",
		Env: append(os.Environ(), "AGENTS_FAKE=1"), LoginShell: "/bin/sh", WorktreeRoot: filepath.Join(dir, "wt"), ProjectsRoot: dir,
		ClaudeConfig: filepath.Join(dir, "claude.json"), CodexHome: filepath.Join(dir, "codex"), Home: dir, ClaudeHome: filepath.Join(dir, "claude"),
		StopGrace: time.Second, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.Close)
	return reg, project
}
