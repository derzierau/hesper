package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Hook configuration: the entries in Claude Code's settings.json and
// Codex's hooks.json and config.toml that call `hesperd hook`. Part C
// installs them; these functions only compute (and, for install, merge)
// the files.

// ClaudeEvents are the Claude Code hook events and their matchers (nil:
// none); AsyncEvents never block the agent.
var ClaudeEvents = []struct {
	Event    string
	Matchers []string
}{
	{"SessionStart", nil}, {"UserPromptSubmit", nil}, {"PreToolUse", []string{"*"}}, {"PostToolUse", []string{"*"}},
	{"PermissionRequest", []string{"*"}}, {"Notification", []string{"*"}}, {"PreCompact", []string{"auto", "manual"}},
	{"Stop", nil}, {"StopFailure", nil}, {"SubagentStop", nil}, {"SessionEnd", nil},
}

// CodexEvents are Codex's hook events (Claude's names); all but SessionEnd
// (always synchronous in Codex) run in the background.
var CodexEvents = []struct {
	Event   string
	Matcher string
}{
	{"SessionStart", ""}, {"UserPromptSubmit", ""}, {"PreToolUse", "*"}, {"PostToolUse", "*"},
	{"PermissionRequest", "*"}, {"Stop", ""}, {"SessionEnd", ""},
}

var asyncClaude = map[string]bool{"PreToolUse": true, "PostToolUse": true}

func hookCommand(bin, source, event string) string {
	return strconv.Quote(bin) + " hook " + source + " " + event
}

// ClaudeHooks is the "hooks" object of Claude Code's settings.json.
func ClaudeHooks(bin string) map[string]any {
	hooks := map[string]any{}
	for _, e := range ClaudeEvents {
		var groups []any
		matchers := e.Matchers
		if matchers == nil {
			matchers = []string{""}
		}
		for _, m := range matchers {
			hook := map[string]any{"type": "command", "command": hookCommand(bin, "claude", e.Event)}
			if asyncClaude[e.Event] {
				hook["async"] = true
			}
			group := map[string]any{"hooks": []any{hook}}
			if m != "" {
				group["matcher"] = m
			}
			groups = append(groups, group)
		}
		hooks[e.Event] = groups
	}
	return hooks
}

// codexHandler is one Codex hook handler of hesperd's. Codex caps
// SessionEnd (always synchronous) at 3 s.
func codexHandler(bin, event string) map[string]any {
	timeout := 5
	if event == "SessionEnd" {
		timeout = 3
	}
	return map[string]any{"type": "command", "command": hookCommand(bin, "codex", event), "timeout": timeout, "async": event != "SessionEnd"}
}

// CodexHooks is the "hooks" object of Codex's hooks.json.
func CodexHooks(bin string) map[string]any {
	hooks := map[string]any{}
	for _, e := range CodexEvents {
		hook := codexHandler(bin, e.Event)
		group := map[string]any{"hooks": []any{hook}}
		if e.Matcher != "" {
			group["matcher"] = e.Matcher
		}
		hooks[e.Event] = []any{group}
	}
	return hooks
}

// CodexNotify is config.toml's notify program.
func CodexNotify(bin string) []string { return []string{bin, "hook", "codex", "notify"} }

// ours reports whether a hook command is hesperd's or ghostyd's (Hesper's
// former name; any `"<bin>" hook claude|codex …` matches), so an install
// replaces them instead of adding a second set.
func ours(command string) bool {
	return strings.Contains(command, " hook claude ") ||
		strings.Contains(command, " hook codex ") || strings.HasSuffix(command, "hesperd\" hook") ||
		strings.HasSuffix(command, "ghostyd\" hook")
}

// mergeHooks puts want's groups into the hooks object of a settings file,
// dropping the groups whose commands are all ours.
func mergeHooks(existing []byte, want map[string]any, extra map[string]any) ([]byte, error) {
	settings := map[string]any{}
	if len(strings.TrimSpace(string(existing))) > 0 {
		if err := json.Unmarshal(existing, &settings); err != nil {
			return nil, err
		}
	}
	for k, v := range extra {
		if old, ok := settings[k]; !ok || (legacyExtra[k] != nil && old == legacyExtra[k]) {
			settings[k] = v
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, groups := range hooks {
		list, _ := groups.([]any)
		var kept []any
		for _, g := range list {
			if !onlyOurs(g) {
				kept = append(kept, g)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	for event, groups := range want {
		list, _ := hooks[event].([]any)
		hooks[event] = append(list, groups.([]any)...)
	}
	settings["hooks"] = hooks
	out, err := json.MarshalIndent(settings, "", "  ")
	return append(out, '\n'), err
}

func onlyOurs(group any) bool {
	g, _ := group.(map[string]any)
	list, _ := g["hooks"].([]any)
	if len(list) == 0 {
		return false
	}
	for _, h := range list {
		hook, _ := h.(map[string]any)
		command, _ := hook["command"].(string)
		if !ours(command) {
			return false
		}
	}
	return true
}

// MergeClaudeSettings is settings.json with hesperd's hooks.
func MergeClaudeSettings(existing []byte, bin string) ([]byte, error) {
	return mergeHooks(existing, ClaudeHooks(bin), nil)
}

// legacyExtra are the values ghostyd (Hesper's former name) wrote; an
// install replaces them.
var legacyExtra = map[string]any{"description": "Ghosty agent state hooks (ghostyd)"}

// MergeCodexHooks is hooks.json with hesperd's hooks.
func MergeCodexHooks(existing []byte, bin string) ([]byte, error) {
	return mergeHooks(existing, CodexHooks(bin), map[string]any{"description": "Hesper agent state hooks (hesperd)"})
}

var notifyLine = regexp.MustCompile(`(?m)^notify\s*=.*$`)

// MergeCodexConfig is config.toml with hesperd's notify. Codex has one
// notify program: another one there is chained (hesperd's notify calls it
// after itself: `… "notify", "--then", <its argv…>`).
func MergeCodexConfig(existing, bin string) (string, string) {
	line := "notify = " + tomlArray(CodexNotify(bin))
	if m := notifyLine.FindString(existing); m != "" {
		if m == line {
			return existing, ""
		}
		prev, ok := codexNotifyOf(strings.TrimSpace(m))
		switch {
		case !ok && strings.Contains(m, "\"hook\", \"codex\""):
		case !ok:
			return existing, "kept the existing Codex notify program (not a one-line array of strings); chain `hesperd hook codex notify` by hand"
		case isOurNotify(prev):
			// Ours (perhaps another bin): keep what it chains.
			chained := []string(nil)
			if len(prev) > 5 && prev[4] == "--then" {
				chained = prev[5:]
			}
			line = "notify = " + tomlArray(chainNotify(bin, chained))
		default:
			line = "notify = " + tomlArray(chainNotify(bin, prev))
		}
		if m == line {
			return existing, ""
		}
		return strings.Replace(existing, m, line, 1), ""
	}
	// Top-level keys must come before the first table.
	return line + "\n" + existing, ""
}

// HookFile is one file an install writes.
type HookFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Changed bool   `json:"changed"`
	Warning string `json:"warning,omitempty"`
}

// PlanHookInstall computes the files an install writes: Claude's
// settings.json, Codex's hooks.json and config.toml.
func PlanHookInstall(bin, claudeSettings, codexHome string) ([]HookFile, error) {
	var files []HookFile
	read := func(p string) []byte {
		data, _ := os.ReadFile(p)
		return data
	}
	old := read(claudeSettings)
	merged, err := MergeClaudeSettings(old, bin)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", claudeSettings, err)
	}
	files = append(files, HookFile{Path: claudeSettings, Content: string(merged), Changed: !sameJSON(old, merged)})
	hooksPath := filepath.Join(codexHome, "hooks.json")
	old = read(hooksPath)
	merged, err = MergeCodexHooks(old, bin)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", hooksPath, err)
	}
	files = append(files, HookFile{Path: hooksPath, Content: string(merged), Changed: !sameJSON(old, merged)})
	configPath := filepath.Join(codexHome, "config.toml")
	oldText := string(read(configPath))
	text, warning := MergeCodexConfig(oldText, bin)
	files = append(files, HookFile{Path: configPath, Content: text, Changed: text != oldText, Warning: warning})
	return files, nil
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}

// InstallHooks writes the planned files that changed (each backed up
// first as <file>.hesperd-backup).
func InstallHooks(files []HookFile) error {
	for _, f := range files {
		if !f.Changed {
			continue
		}
		if real, err := filepath.EvalSymlinks(f.Path); err == nil {
			f.Path = real // a symlinked config is written where it points
		}
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if st, err := os.Stat(f.Path); err == nil {
			mode = st.Mode().Perm()
			data, err := os.ReadFile(f.Path)
			if err != nil {
				return err
			}
			if err := atomicWrite(f.Path+".hesperd-backup", data, mode); err != nil {
				return err
			}
		}
		if err := atomicWrite(f.Path, []byte(f.Content), mode); err != nil {
			return err
		}
	}
	return nil
}
