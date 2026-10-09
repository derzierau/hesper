package review

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

func TestClassify(t *testing.T) {
	for cmd, want := range map[string]string{
		"go test ./...": KindTest, "cd relay && go build ./... && go test -race ./internal/x": KindTest,
		"make build test check": KindTest, "make": KindBuild, "make -C app build": KindBuild, "npm test": KindTest,
		"npm run build": KindBuild, "pnpm lint": KindLint, "yarn dev": KindRun, "CI=1 npx vitest run": KindTest,
		"python -m pytest -q": KindTest, "pytest": KindTest, "swift build": KindBuild, "xcodebuild -scheme X test": KindTest,
		"cargo clippy": KindLint, "go vet ./...": KindLint, "golangci-lint run": KindLint, "go run ./cmd/x": KindRun,
		"git status": KindOther, "ls -la | grep x": KindOther, "./gradlew check": KindTest, "docker build .": KindBuild,
		"docker ps": KindOther, "just test-remote": KindTest,
	} {
		if got := Classify(cmd); got != want {
			t.Errorf("%q: %s, want %s", cmd, got, want)
		}
	}
}

func payload(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEvidenceFreshness(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	var l Log
	if ev := l.Evidence(false); ev.Freshness != wire.EvidenceNone || ev.Commands == nil || ev.Attachments == nil {
		t.Fatalf("no changes: %+v", ev)
	}
	if ev := l.Evidence(true); ev.Freshness != wire.EvidenceMissing {
		t.Fatalf("nothing ran: %+v", ev)
	}
	edit := payload(t, `{"session_id":"s1","tool_name":"Edit","tool_input":{"file_path":"/x/a.go"},"tool_response":{"structuredPatch":[{"newStart":10,"newLines":3}]}}`)
	l.Record("UserPromptSubmit", payload(t, `{"session_id":"s1","prompt":"fix the bug"}`), at(0))
	l.Record("PostToolUse", edit, at(1))
	// A lint is no evidence.
	lint := payload(t, `{"session_id":"s1","tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"go vet ./..."}}`)
	l.Record("PreToolUse", lint, at(2))
	l.Record("PostToolUse", lint, at(3))
	if ev := l.Evidence(true); ev.Freshness != wire.EvidenceMissing || len(ev.Commands) != 1 || ev.Commands[0].Kind != KindLint {
		t.Fatalf("lint: %+v", ev)
	}
	// A test that fails (Codex: its output says the exit code; ended
	// before it started, as hooks may arrive).
	failing := payload(t, `{"session_id":"s1","tool_name":"Bash","call_id":"c2","tool_input":{"command":["bash","-lc","go test ./..."]},"tool_response":"Exit code: 1\nWall time: 2s"}`)
	l.Record("PostToolUse", failing, at(5))
	l.Record("PreToolUse", failing, at(4))
	ev := l.Evidence(true)
	if ev.Freshness != wire.EvidenceStale || len(ev.Commands) != 2 || ev.Commands[1].Command != "go test ./..." || *ev.Commands[1].ExitCode != 1 {
		t.Fatalf("failed test: %+v", ev)
	}
	// It passes: fresh.
	passing := payload(t, `{"session_id":"s1","tool_name":"Bash","tool_use_id":"t3","tool_input":{"command":"go test ./..."},"tool_response":{"stdout":"ok"}}`)
	l.Record("PreToolUse", passing, at(6))
	l.Record("PostToolUse", passing, at(8))
	ev = l.Evidence(true)
	if ev.Freshness != wire.EvidenceFresh || !ev.LastEditAt.Equal(at(1)) || *ev.Commands[2].ExitCode != 0 || !ev.Commands[2].EndedAt.Equal(at(8)) {
		t.Fatalf("passing test: %+v", ev)
	}
	// An edit after it: stale; a failure hook ends a build with 1.
	l.Record("PostToolUse", edit, at(9))
	if ev := l.Evidence(true); ev.Freshness != wire.EvidenceStale {
		t.Fatalf("edit after the test: %+v", ev)
	}
	build := payload(t, `{"session_id":"s1","tool_name":"Bash","tool_use_id":"t4","tool_input":{"command":"make build"},"error":"Exit code 2"}`)
	l.Record("PreToolUse", build, at(10))
	l.Record("PostToolUseFailure", build, at(11))
	if ev := l.Evidence(true); ev.Freshness != wire.EvidenceStale || *ev.Commands[3].ExitCode != 2 {
		t.Fatalf("failed build: %+v", ev)
	}
}

func TestProvenance(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	var l Log
	t0 := time.Now().UTC()
	l.Record("UserPromptSubmit", payload(t, `{"session_id":"s1","prompt":"first prompt"}`), t0)
	l.Record("PostToolUse", payload(t, `{"session_id":"s1","tool_name":"Write","tool_input":{"file_path":"`+dir+`/a.go"}}`), t0)
	l.Record("UserPromptSubmit", payload(t, `{"session_id":"s1","prompt":"second prompt"}`), t0)
	l.Record("PostToolUse", payload(t, `{"session_id":"s1","tool_name":"Edit","tool_input":{"file_path":"`+dir+`/a.go"},"tool_response":{"structuredPatch":[{"newStart":10,"newLines":3}]}}`), t0)
	// Codex: apply_patch with a path relative to its cwd.
	l.Record("PostToolUse", payload(t, `{"session_id":"s2","cwd":"`+dir+`","tool_name":"apply_patch","tool_input":{"input":"*** Begin Patch\n*** Update File: b.go\n@@\n-x\n+y\n*** End Patch"}}`), t0)
	for _, c := range []struct {
		path       string
		line, turn int
		tool       string
	}{{"a.go", 11, 2, "Edit"}, {"a.go", 3, 1, "Write"}, {"a.go", 0, 2, "Edit"}, {"b.go", 1, 0, "apply_patch"}} {
		e, ok := l.Provenance(filepath.Join(dir, c.path), c.line)
		if !ok || e.Turn != c.turn || e.Tool != c.tool {
			t.Errorf("%s:%d: %+v %v", c.path, c.line, e, ok)
		}
	}
	if p := l.PromptOf("s1", 2); p != "second prompt" {
		t.Errorf("prompt %q", p)
	}
	if _, ok := l.Provenance(filepath.Join(dir, "c.go"), 1); ok {
		t.Error("an unknown file has provenance")
	}
	// Bounded.
	for range MaxEvents + 10 {
		l.Record("UserPromptSubmit", payload(t, `{"session_id":"s1","prompt":"p"}`), t0)
	}
	if len(l.Events) != MaxEvents {
		t.Errorf("%d events", len(l.Events))
	}
}
