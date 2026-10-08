package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// claudeLikeWriter rewrites ~/.claude.json the way Claude Code does:
// read-modify-write under proper-lockfile's "<file>.lock" directory.
func claudeLikeWriter(t *testing.T, path string, n int) {
	for i := 0; i < n; i++ {
		unlock, err := lockDir(path + ".lock")
		if err != nil {
			t.Error(err)
			return
		}
		var c map[string]any
		data, _ := os.ReadFile(path)
		if err := json.Unmarshal(data, &c); err != nil {
			unlock()
			t.Errorf("a torn read: %v (%q)", err, data)
			return
		}
		c["numStartups"] = c["numStartups"].(float64) + 1
		out, _ := json.MarshalIndent(c, "", "  ")
		atomicWrite(path, append(out, '\n'), 0o600)
		unlock()
		time.Sleep(time.Millisecond)
	}
}

func TestClaudeTrustWithConcurrentWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	os.WriteFile(path, []byte(`{"numStartups": 0, "projects": {}}`+"\n"), 0o600)
	const writes = 200
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		claudeLikeWriter(t, path, writes)
	}()
	var keys []string
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("/work/p%02d", i)
		keys = append(keys, key)
		if res, err := trustClaude(path, key); err != nil || res != trustMarked {
			t.Fatalf("%s: %s %v", key, res, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	wg.Wait()
	var c struct {
		NumStartups float64                   `json:"numStartups"`
		Projects    map[string]map[string]any `json:"projects"`
	}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if c.NumStartups != writes {
		t.Fatalf("the other writer's changes were lost: %v of %d", c.NumStartups, writes)
	}
	for _, k := range keys {
		if c.Projects[k]["hasTrustDialogAccepted"] != true {
			t.Fatalf("trust of %s lost", k)
		}
	}
	if res, _ := trustClaude(path, keys[0]); res != trustAlready {
		t.Fatalf("again: %s", res)
	}
}

// A writer that ignores the lock and writes between our read and our
// rename: we start over on its content instead of clobbering it.
func TestUpdateFileStartsOverWhenChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	os.WriteFile(path, []byte(`{"a":1}`), 0o600)
	calls := 0
	err := updateFile(path, true, func(old []byte) ([]byte, error) {
		calls++
		if calls == 1 {
			os.WriteFile(path, []byte(`{"a":2}`), 0o600) // someone else, meanwhile
		}
		return []byte(strings.Replace(string(old), "}", `,"b":true}`, 1)), nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("%v after %d edits", err, calls)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"a":2,"b":true}` {
		t.Fatalf("%s", data)
	}
}

func TestLockDirWaitsAndBreaksStaleLocks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := lockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		unlock()
	}()
	start := time.Now()
	unlock2, err := lockDir(dir)
	if err != nil || time.Since(start) < 90*time.Millisecond {
		t.Fatalf("did not wait: %v", err)
	}
	unlock2()
	// A lock nobody refreshed for longer than lockStale is abandoned.
	os.Mkdir(dir, 0o700)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(dir, old, old)
	unlock3, err := lockDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	unlock3()
}

func TestClaudeTrustEdit(t *testing.T) {
	// Missing file: nothing (Claude Code has not run here).
	dir := t.TempDir()
	if res, err := trustClaude(filepath.Join(dir, "none.json"), "/p"); res != trustMissing || err != nil {
		t.Fatalf("%s %v", res, err)
	}
	// An existing entry keeps its fields; numbers and escapes stay.
	in := `{"projects":{"/p":{"allowedTools":["Bash(ls)"],"hasTrustDialogAccepted":false,"lastCost":0.1234567890123}},"x":" <&>"}`
	out, already, err := claudeTrustEdit([]byte(in), "/p")
	if err != nil || already {
		t.Fatal(err)
	}
	want := `{
  "projects": {
    "/p": {
      "allowedTools": [
        "Bash(ls)"
      ],
      "hasTrustDialogAccepted": true,
      "lastCost": 0.1234567890123
    }
  },
  "x": " <&>"
}`
	if string(out) != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
	if _, already, _ := claudeTrustEdit(out, "/p"); !already {
		t.Fatal("not already")
	}
	if _, _, err := claudeTrustEdit([]byte(`{"projects": [1]}`), "/p"); err == nil {
		t.Fatal("edited projects that are not an object")
	}
	if _, _, err := claudeTrustEdit([]byte(`not json`), "/p"); err == nil {
		t.Fatal("edited a broken file")
	}
}

func TestCodexTrustEdit(t *testing.T) {
	cases := []struct {
		name, in, out, res string
	}{
		{"empty", "", "[projects.\"/p\"]\ntrust_level = \"trusted\"\n", trustMarked},
		{"append", "model = \"o3\"\n[tui]\nx = 1", "model = \"o3\"\n[tui]\nx = 1\n\n[projects.\"/p\"]\ntrust_level = \"trusted\"\n", trustMarked},
		{"table without level", "[projects.\"/p\"]\nfoo = 1\n[other]\n", "[projects.\"/p\"]\ntrust_level = \"trusted\"\nfoo = 1\n[other]\n", trustMarked},
		{"literal key", "[projects.'/p']\n", "[projects.'/p']\ntrust_level = \"trusted\"\n", trustMarked},
		{"already", "[projects.\"/p\"]\ntrust_level = \"trusted\"\n", "", trustAlready},
		{"untrusted stays", "[projects.\"/p\"]\ntrust_level = \"untrusted\"\n", "", trustKept},
		{"other project", "[projects.\"/q\"]\ntrust_level = \"untrusted\"\n", "[projects.\"/q\"]\ntrust_level = \"untrusted\"\n\n[projects.\"/p\"]\ntrust_level = \"trusted\"\n", trustMarked},
	}
	for _, c := range cases {
		out, res, err := codexTrustEdit(c.in, "/p")
		if err != nil || res != c.res || (res == trustMarked && out != c.out) {
			t.Errorf("%s: %q %s %v", c.name, out, res, err)
		}
	}
	if _, _, err := codexTrustEdit("projects = { \"/q\" = { trust_level = \"trusted\" } }\n", "/p"); err == nil {
		t.Error("appended a table to inline projects")
	}
	if got := tomlString(`/a "b"\c`); got != `"/a \"b\"\\c"` {
		t.Errorf("tomlString %s", got)
	}
	// Through the file: written, then already.
	path := filepath.Join(t.TempDir(), "codex", "config.toml")
	if res, err := trustCodex(path, "/p"); res != trustMarked || err != nil {
		t.Fatalf("%s %v", res, err)
	}
	if !codexTrustedText(path, "/p") {
		t.Fatal("not trusted")
	}
	if res, _ := trustCodex(path, "/p"); res != trustAlready {
		t.Fatalf("again %s", res)
	}
}

func codexTrustedText(path, key string) bool {
	data, _ := os.ReadFile(path)
	level, _ := codexTrustLevel(string(data), key)
	return level == "trusted"
}

func TestPretrustRefusesHome(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	if !homeOrAbove(home, home) || !homeOrAbove(filepath.Dir(home), home) || homeOrAbove(filepath.Join(home, "p"), home) {
		t.Fatal("homeOrAbove")
	}
	r := &Registry{opt: Options{Home: home, ClaudeConfig: filepath.Join(home, ".claude.json"), CodexHome: filepath.Join(home, ".codex")}}
	os.WriteFile(r.opt.ClaudeConfig, []byte("{}"), 0o600)
	for _, kind := range []string{"claude", "codex"} {
		if res, err := r.pretrust(kind, home); res != trustRefused || err != nil {
			t.Fatalf("%s: %s %v", kind, res, err)
		}
	}
	if data, _ := os.ReadFile(r.opt.ClaudeConfig); string(data) != "{}" {
		t.Fatal("home trusted")
	}
	if _, err := os.Stat(r.opt.CodexHome); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("codex home touched")
	}
}
