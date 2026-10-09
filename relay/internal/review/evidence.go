package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Evidence and provenance (phase 2): what an agent ran and edited, from
// its hook events (Claude Code's and Codex's PreToolUse / PostToolUse,
// UserPromptSubmit), kept in its Log.

// MaxEvents bounds a log (the oldest go first).
const MaxEvents = 2000

// Event kinds.
const (
	EventPrompt  = "prompt"
	EventCommand = "command"
	EventEdit    = "edit"
)

// Event is one thing an agent did: a prompt it got (a turn starts), a
// command it ran (At its start, EndedAt and ExitCode once it ended), a
// file it edited (Paths absolute; Ranges the new lines [start, end) of
// the first path the edit wrote, when the tool said).
type Event struct {
	Kind        string    `json:"kind"`
	At          time.Time `json:"at"`
	Session     string    `json:"session,omitempty"`
	Turn        int       `json:"turn,omitempty"`
	Tool        string    `json:"tool,omitempty"`
	Key         string    `json:"key,omitempty"`
	Command     string    `json:"command,omitempty"`
	CommandKind string    `json:"commandKind,omitempty"`
	ExitCode    *int      `json:"exitCode,omitempty"`
	EndedAt     time.Time `json:"endedAt,omitzero"`
	Paths       []string  `json:"paths,omitempty"`
	Ranges      [][2]int  `json:"ranges,omitempty"`
	Prompt      string    `json:"prompt,omitempty"`
}

// RecordsEvent: the hook events Record looks at.
func RecordsEvent(event string) bool {
	switch event {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure":
		return true
	}
	return false
}

var (
	commandTools = map[string]bool{"bash": true, "shell": true, "exec_command": true, "local_shell": true, "unified_exec": true, "container.exec": true}
	editTools    = map[string]bool{"edit": true, "write": true, "multiedit": true, "notebookedit": true, "apply_patch": true, "applypatch": true}
	patchPath    = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+)$|^\*\*\* Move to: (.+)$`)
	codexExit    = regexp.MustCompile(`^Exit code: (-?\d+)`)
	failureExit  = regexp.MustCompile(`(?i)exit code:? (-?\d+)`)
)

func str(data map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := data[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// Record adds what one hook event says to the log; false when it says
// nothing for review. at is when it arrived.
func (l *Log) Record(event string, data map[string]any, at time.Time) bool {
	session := str(data, "session_id", "thread-id", "thread_id")
	tool := str(data, "tool_name")
	input, _ := data["tool_input"].(map[string]any)
	switch event {
	case "UserPromptSubmit":
		if l.Turns == nil {
			l.Turns = map[string]int{}
		}
		l.Turns[session]++
		l.add(Event{Kind: EventPrompt, At: at, Session: session, Turn: l.Turns[session], Prompt: excerpt(str(data, "prompt"), 200)})
		return true
	case "PreToolUse":
		if !commandTools[strings.ToLower(tool)] {
			return false
		}
		cmd := commandOf(input)
		if cmd == "" {
			return false
		}
		key := callKey(data)
		for i := len(l.Events) - 1; i >= 0; i-- {
			if e := &l.Events[i]; e.Kind == EventCommand && e.Key == key {
				return false // its end came first (hooks run concurrently)
			}
		}
		l.add(Event{Kind: EventCommand, At: at, Session: session, Turn: l.Turns[session], Tool: tool, Key: key, Command: cmd, CommandKind: Classify(cmd)})
		return true
	case "PostToolUse", "PostToolUseFailure":
		switch {
		case commandTools[strings.ToLower(tool)]:
			cmd := commandOf(input)
			if cmd == "" {
				return false
			}
			code := exitCode(event, data)
			key := callKey(data)
			for i := len(l.Events) - 1; i >= 0; i-- {
				if e := &l.Events[i]; e.Kind == EventCommand && e.Key == key && e.EndedAt.IsZero() {
					e.EndedAt, e.ExitCode = at, code
					return true
				}
			}
			l.add(Event{Kind: EventCommand, At: at, EndedAt: at, ExitCode: code, Session: session, Turn: l.Turns[session], Tool: tool, Key: key,
				Command: cmd, CommandKind: Classify(cmd)})
			return true
		case editTools[strings.ToLower(tool)] && event == "PostToolUse":
			paths := editPaths(input, str(data, "cwd"))
			if len(paths) == 0 {
				return false
			}
			l.add(Event{Kind: EventEdit, At: at, Session: session, Turn: l.Turns[session], Tool: tool, Paths: paths, Ranges: editRanges(data["tool_response"])})
			return true
		}
	}
	return false
}

func (l *Log) add(e Event) {
	l.Events = append(l.Events, e)
	if n := len(l.Events) - MaxEvents; n > 0 {
		l.Events = append([]Event(nil), l.Events[n:]...)
	}
}

func excerpt(s string, limit int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit])
	}
	return s
}

// callKey identifies a tool call: its id, else its name and input.
func callKey(data map[string]any) string {
	if id := str(data, "tool_use_id", "call_id"); id != "" {
		return id
	}
	raw, _ := json.Marshal(data["tool_input"])
	sum := sha256.Sum256(append([]byte(str(data, "tool_name")+"\x00"), raw...))
	return hex.EncodeToString(sum[:12])
}

// commandOf is a shell tool's command line (Codex's ["bash", "-lc",
// "…"] unwrapped).
func commandOf(input map[string]any) string {
	command := input["command"]
	if command == nil {
		command = input["cmd"]
	}
	if list, ok := command.([]any); ok {
		parts := make([]string, 0, len(list))
		for _, p := range list {
			if s, ok := p.(string); ok {
				parts = append(parts, s)
			}
		}
		if len(parts) == 3 && (parts[0] == "bash" || parts[0] == "zsh" || parts[0] == "sh") && strings.HasPrefix(parts[1], "-") {
			parts = parts[2:]
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	s, _ := command.(string)
	return strings.TrimSpace(s)
}

// exitCode of a finished command: what the tool reported (Codex's
// "Exit code: N", an exit_code field), else 0 for PostToolUse (the tool
// succeeded) and 1 for PostToolUseFailure; nil when it was interrupted.
func exitCode(event string, data map[string]any) *int {
	n := func(v int) *int { return &v }
	switch resp := data["tool_response"].(type) {
	case map[string]any:
		if b, _ := resp["interrupted"].(bool); b {
			return nil
		}
		for _, k := range []string{"exit_code", "exitCode", "returncode", "return_code"} {
			if f, ok := resp[k].(float64); ok {
				return n(int(f))
			}
		}
	case string:
		if m := codexExit.FindStringSubmatch(resp); m != nil {
			v, _ := strconv.Atoi(m[1])
			return n(v)
		}
	}
	if event == "PostToolUseFailure" {
		if m := failureExit.FindStringSubmatch(str(data, "error")); m != nil {
			if v, _ := strconv.Atoi(m[1]); v != 0 {
				return n(v)
			}
		}
		return n(1)
	}
	return n(0)
}

// editPaths are the files an edit tool wrote, absolute (relative ones
// against cwd), their folder's links resolved.
func editPaths(input map[string]any, cwd string) []string {
	var paths []string
	if p := str(input, "file_path", "notebook_path", "path"); p != "" {
		paths = append(paths, p)
	}
	for _, field := range []string{"input", "patch", "command"} {
		patch, _ := input[field].(string)
		for _, m := range patchPath.FindAllStringSubmatch(patch, -1) {
			paths = append(paths, strings.TrimSpace(m[1]+m[2]))
		}
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			if cwd == "" {
				continue
			}
			p = filepath.Join(cwd, p)
		}
		out = append(out, realPath(p))
	}
	return out
}

// realPath is p with its folder's links resolved (macOS's /tmp is
// /private/tmp: Git names the latter).
func realPath(p string) string {
	p = filepath.Clean(p)
	if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(dir, filepath.Base(p))
	}
	return p
}

// editRanges are the new lines an edit wrote (Claude Code's
// structuredPatch), [start, end).
func editRanges(response any) [][2]int {
	resp, _ := response.(map[string]any)
	hunks, _ := resp["structuredPatch"].([]any)
	var out [][2]int
	for _, h := range hunks {
		m, _ := h.(map[string]any)
		start, ok1 := m["newStart"].(float64)
		lines, ok2 := m["newLines"].(float64)
		if ok1 && ok2 {
			out = append(out, [2]int{int(start), int(start + lines)})
		}
	}
	return out
}

// Command kinds.
const (
	KindTest  = "test"
	KindBuild = "build"
	KindLint  = "lint"
	KindRun   = "run"
	KindOther = "other"
)

var (
	splitCommand = regexp.MustCompile(`&&|\|\||;|\||\n`)
	envAssign    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	kindRank     = map[string]int{KindOther: 0, KindRun: 1, KindLint: 2, KindBuild: 3, KindTest: 4}
	testScript   = regexp.MustCompile(`^(test|tests|check|test:.*|test-.*|e2e|spec)$`)
	buildScript  = regexp.MustCompile(`^(build|build:.*|compile|typecheck|tsc)$`)
	lintScript   = regexp.MustCompile(`^(lint|lint:.*|fmt|format|vet)$`)
	runScript    = regexp.MustCompile(`^(start|dev|serve|run|preview)$`)
	makeTest     = regexp.MustCompile(`^(test|tests|check|test-.*|.*-test|ci)$`)
)

// Classify sorts a command line into test, build, lint, run or other by
// its programs (the strongest part of a chain: test over build over lint
// over run).
func Classify(command string) string {
	best := KindOther
	for _, part := range splitCommand.Split(command, -1) {
		if k := classifyOne(strings.Fields(part)); kindRank[k] > kindRank[best] {
			best = k
		}
	}
	return best
}

func classifyOne(words []string) string {
	for len(words) > 0 && (envAssign.MatchString(words[0]) || words[0] == "sudo" || words[0] == "time" || words[0] == "env" || words[0] == "exec" || words[0] == "(") {
		words = words[1:]
	}
	if len(words) == 0 {
		return KindOther
	}
	prog := path.Base(words[0])
	arg := func(i int) string {
		if i < len(words) {
			return words[i]
		}
		return ""
	}
	// The first argument that is not an option.
	sub := func(from int) string {
		for i := from; i < len(words); i++ {
			if !strings.HasPrefix(words[i], "-") {
				return words[i]
			}
		}
		return ""
	}
	switch prog {
	case "go":
		switch sub(1) {
		case "test":
			return KindTest
		case "build", "install":
			return KindBuild
		case "vet", "fmt":
			return KindLint
		case "run":
			return KindRun
		}
	case "cargo":
		switch sub(1) {
		case "test", "nextest":
			return KindTest
		case "build", "check":
			return KindBuild
		case "clippy", "fmt":
			return KindLint
		case "run":
			return KindRun
		}
	case "swift":
		switch sub(1) {
		case "test":
			return KindTest
		case "build":
			return KindBuild
		case "run":
			return KindRun
		}
	case "xcodebuild":
		for _, w := range words[1:] {
			if w == "test" || w == "test-without-building" {
				return KindTest
			}
		}
		return KindBuild
	case "npm", "pnpm", "yarn", "bun":
		s := sub(1)
		if s == "run" || s == "run-script" {
			s = sub(indexOf(words, s) + 1)
		}
		switch {
		case s == "t" || testScript.MatchString(s):
			return KindTest
		case buildScript.MatchString(s):
			return KindBuild
		case lintScript.MatchString(s):
			return KindLint
		case runScript.MatchString(s):
			return KindRun
		}
	case "npx", "bunx":
		return classifyOne(words[1:])
	case "make", "gmake", "just":
		targets := 0
		kind := KindBuild
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "-") || strings.Contains(w, "=") {
				continue
			}
			targets++
			if makeTest.MatchString(w) {
				return KindTest
			}
			if lintScript.MatchString(w) {
				kind = KindLint
			}
		}
		if targets == 0 || kind == KindBuild {
			return KindBuild
		}
		return kind
	case "pytest", "py.test", "jest", "vitest", "mocha", "rspec", "phpunit", "ctest", "tox", "nox", "ava", "karma":
		return KindTest
	case "python", "python3":
		if arg(1) == "-m" {
			switch arg(2) {
			case "pytest", "unittest", "tox", "nox":
				return KindTest
			case "mypy", "ruff", "flake8", "pylint", "black":
				return KindLint
			}
		}
		return KindRun
	case "node", "deno", "ruby", "php", "java":
		if prog == "deno" && sub(1) == "test" {
			return KindTest
		}
		return KindRun
	case "mvn", "gradle", "gradlew", "./gradlew":
		for _, w := range words[1:] {
			if w == "test" || w == "check" || w == "verify" {
				return KindTest
			}
		}
		return KindBuild
	case "bundle", "rake", "mix", "dotnet":
		for _, w := range words[1:] {
			if w == "test" || w == "rspec" || w == "spec" {
				return KindTest
			}
		}
		if prog == "dotnet" && sub(1) == "build" {
			return KindBuild
		}
	case "tsc", "bazel", "ninja", "cmake", "docker", "swiftc", "gcc", "clang", "javac":
		if prog == "bazel" && sub(1) == "test" {
			return KindTest
		}
		if prog == "docker" && sub(1) != "build" {
			return KindOther
		}
		return KindBuild
	case "eslint", "prettier", "ruff", "flake8", "mypy", "pylint", "black", "swiftlint", "swiftformat", "golangci-lint", "staticcheck",
		"shellcheck", "rubocop", "gofmt", "goimports", "stylelint", "biome", "clang-format", "ktlint":
		return KindLint
	}
	if strings.HasPrefix(words[0], "./") && strings.Contains(prog, "test") {
		return KindTest
	}
	return KindOther
}

func indexOf(words []string, w string) int {
	for i, x := range words {
		if x == w {
			return i
		}
	}
	return 0
}

// Evidence is review.evidence's freshness and commands from the log;
// changed: the folder has changes against the base.
func (l *Log) Evidence(changed bool) wire.ReviewEvidence {
	ev := wire.ReviewEvidence{Freshness: wire.EvidenceNone, Commands: []wire.ReviewCommand{}, Attachments: []wire.ReviewAttachment{}}
	var lastEdit time.Time
	ran, fresh := false, false
	for _, e := range l.Events {
		if e.Kind == EventEdit && e.At.After(lastEdit) {
			lastEdit = e.At
		}
	}
	for _, e := range l.Events {
		if e.Kind != EventCommand {
			continue
		}
		ev.Commands = append(ev.Commands, wire.ReviewCommand{Command: e.Command, Kind: e.CommandKind, ExitCode: e.ExitCode, StartedAt: e.At, EndedAt: e.EndedAt})
		if e.CommandKind != KindTest && e.CommandKind != KindBuild {
			continue
		}
		ran = true
		if !e.EndedAt.IsZero() && e.ExitCode != nil && *e.ExitCode == 0 && !e.At.Before(lastEdit) {
			fresh = true
		}
	}
	if n := len(ev.Commands) - MaxEvidenceCommands; n > 0 {
		ev.Commands = ev.Commands[n:]
	}
	ev.LastEditAt = lastEdit
	switch {
	case !changed:
	case fresh:
		ev.Freshness = wire.EvidenceFresh
	case ran:
		ev.Freshness = wire.EvidenceStale
	default:
		ev.Freshness = wire.EvidenceMissing
	}
	return ev
}

// MaxEvidenceCommands bounds the commands review.evidence returns (the
// latest).
const MaxEvidenceCommands = 200

// Attachments are the changed files that are images, videos or logs
// (absolute paths), for review.evidence.
func Attachments(top string, changes []Change) []wire.ReviewAttachment {
	out := []wire.ReviewAttachment{}
	for _, c := range changes {
		if c.Status == wire.ReviewDeleted {
			continue
		}
		kind := ""
		switch strings.ToLower(path.Ext(c.Path)) {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".heic", ".tiff", ".bmp":
			kind = "image"
		case ".mp4", ".mov", ".webm", ".m4v":
			kind = "video"
		case ".log":
			kind = "log"
		}
		if kind != "" {
			out = append(out, wire.ReviewAttachment{Path: filepath.Join(top, filepath.FromSlash(c.Path)), Kind: kind})
		}
	}
	return out
}

// Provenance is the last edit of file (absolute) covering line (new side;
// 0: any): one whose lines cover it, else the last edit of the file. ok
// false when no edit of it is known.
func (l *Log) Provenance(file string, line int) (Event, bool) {
	file = realPath(file)
	var any *Event
	for i := len(l.Events) - 1; i >= 0; i-- {
		e := &l.Events[i]
		if e.Kind != EventEdit || !contains(e.Paths, file) {
			continue
		}
		if any == nil {
			any = e
		}
		if line <= 0 || len(e.Ranges) == 0 || e.Paths[0] != file {
			return *e, true // the whole file, as far as is known
		}
		for _, r := range e.Ranges {
			if line >= r[0] && line < r[1] {
				return *e, true
			}
		}
	}
	if any != nil {
		return *any, true
	}
	return Event{}, false
}

// PromptOf is the prompt of a session's turn ("" when not in the log).
func (l *Log) PromptOf(session string, turn int) string {
	for _, e := range l.Events {
		if e.Kind == EventPrompt && e.Session == session && e.Turn == turn {
			return e.Prompt
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
