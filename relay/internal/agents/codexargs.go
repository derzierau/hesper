package agents

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Per-agent Codex hooks (Codex 0.160): instead of changing the user's
// ~/.codex (hooks.json, config.toml), every Codex agent gets hesperd's
// hooks and notify as -c overrides on its own command line:
//
//	--no-daemon -c features.hooks=true
//	-c hooks.<Event>=[{matcher=…, hooks=[{type="command", command=…, timeout=5 (SessionEnd 3), async=…}]}]
//	-c hooks.state={"/<session-flags>/config.toml:<event>:0:0"={trusted_hash="sha256:…"}, …}
//	-c notify=["<hesperd>", "hook", "codex", "notify"(, "--then", <the user's notify…>)]
//
// Codex loads -c hooks as their own layer ("sessionFlags", next to the
// user's and the project's hooks, which keep running), and runs only hooks
// whose hash is trusted: hooks.state carries that trust for exactly these
// commands (the hash is SHA-256 of the canonical JSON of
// {"event_name": <snake_case event>, "hooks": [handler], "matcher"?},
// verified against `codex app-server` hooks/list). So the agent needs no
// /hooks review and `--dangerously-bypass-hook-trust` (which would also
// run every untrusted hook of the user's) is not used. The notify override
// replaces the user's notify for this agent only and calls it after
// hesperd's (--then).
//
// Skipped (settings.json "codexSessionHooks": false, or when the Codex
// home's hooks.json already has hesperd's hooks from `hesperd hooks
// install`; notify when config.toml's notify is hesperd's already).

// codexEventKey is Codex's snake_case name of an event in hook keys.
var codexEventKey = map[string]string{
	"SessionStart": "session_start", "UserPromptSubmit": "user_prompt_submit", "PreToolUse": "pre_tool_use",
	"PostToolUse": "post_tool_use", "PermissionRequest": "permission_request", "Stop": "stop", "SessionEnd": "session_end",
}

// CodexSessionArgs are the -c options that give one Codex invocation
// hesperd's hooks (trusted) and notify; userNotify is the notify program
// the user's config.toml has (called after hesperd's), nil for none.
func CodexSessionArgs(bin string, hooks, notify bool, userNotify []string) []string {
	var args []string
	if hooks {
		args = append(args, "-c", "features.hooks=true")
		var state []string
		for _, e := range CodexEvents {
			handler := codexHandler(bin, e.Event)
			group := map[string]any{"hooks": []any{handler}}
			if e.Matcher != "" {
				group["matcher"] = e.Matcher
			}
			args = append(args, "-c", "hooks."+e.Event+"=["+tomlInline(group)+"]")
			key := "/<session-flags>/config.toml:" + codexEventKey[e.Event] + ":0:0"
			state = append(state, tomlString(key)+"={trusted_hash="+tomlString(codexHookHash(codexEventKey[e.Event], e.Matcher, handler))+"}")
		}
		args = append(args, "-c", "hooks.state={"+strings.Join(state, ",")+"}")
	}
	if notify {
		args = append(args, "-c", "notify="+tomlArray(chainNotify(bin, userNotify)))
	}
	if len(args) > 0 {
		// Its own in-process server: the -c layer is certainly this
		// session's, hooks run in the agent's environment (HESPER_AGENT_ID
		// names it), and the user's shared Codex server is left alone.
		args = append([]string{"--no-daemon"}, args...)
	}
	return args
}

// codexHookHash is the hash Codex records trust against for one hook.
func codexHookHash(event, matcher string, handler map[string]any) string {
	v := map[string]any{"event_name": event, "hooks": []any{handler}}
	if matcher != "" {
		v["matcher"] = matcher
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // serde_json escapes nothing but what JSON must
	enc.Encode(v)            // maps encode with sorted keys
	sum := sha256.Sum256(bytes.TrimRight(b.Bytes(), "\n"))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// chainNotify is hesperd's notify, calling prev after it.
func chainNotify(bin string, prev []string) []string {
	out := CodexNotify(bin)
	if len(prev) > 0 {
		out = append(append(out, "--then"), prev...)
	}
	return out
}

func tomlInline(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+tomlValue(m[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func tomlValue(v any) string {
	switch x := v.(type) {
	case string:
		return tomlString(x)
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, tomlValue(e))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		return tomlInline(x)
	}
	return `""`
}

func tomlArray(list []string) string {
	parts := make([]string, 0, len(list))
	for _, s := range list {
		parts = append(parts, tomlString(s))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// codexSessionArgs are a Codex agent's extra options (the lock is held):
// hesperd's hooks and notify, and with settings.json codexUpdatePrompt
// "skip" no update check at start (Codex's check_for_update_on_startup,
// verified with 0.160.1: the "Update available" screen stays away).
func (r *Registry) codexSessionArgs() []string {
	args := r.codexHookArgs()
	if r.settings.codexUpdatePrompt() == CodexUpdateSkip {
		args = append(args, "-c", "check_for_update_on_startup=false")
	}
	return args
}

func (r *Registry) codexHookArgs() []string {
	if !r.settings.codexSessionHooks() || r.opt.HookBin == "" {
		return nil
	}
	installed, _ := os.ReadFile(filepath.Join(r.opt.CodexHome, "hooks.json"))
	hooks := !bytes.Contains(installed, []byte(" hook codex "))
	config, _ := os.ReadFile(filepath.Join(r.opt.CodexHome, "config.toml"))
	userNotify, ok := codexNotifyOf(string(config))
	notify := true
	switch {
	case !ok:
		userNotify = nil
	case isOurNotify(userNotify):
		notify, userNotify = false, nil
	}
	return CodexSessionArgs(r.opt.HookBin, hooks, notify, userNotify)
}

func isOurNotify(argv []string) bool {
	return len(argv) >= 4 && argv[1] == "hook" && argv[2] == "codex" && argv[3] == "notify"
}

// codexNotifyOf is config.toml's top-level notify program (ok false when
// there is none or it is not a one-line array of strings).
func codexNotifyOf(text string) ([]string, bool) {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			return nil, false // tables begin: no top-level notify
		}
		if m := notifyAssign.FindStringSubmatch(t); m != nil {
			return parseTOMLStrings(m[1])
		}
	}
	return nil, false
}

var notifyAssign = regexp.MustCompile(`^notify\s*=\s*(\[.*\])\s*(?:#.*)?$`)

// parseTOMLStrings parses a one-line TOML array of strings.
func parseTOMLStrings(s string) ([]string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, false
	}
	s = strings.TrimSpace(s[1 : len(s)-1])
	var out []string
	for s != "" {
		var v string
		switch s[0] {
		case '\'':
			end := strings.IndexByte(s[1:], '\'')
			if end < 0 {
				return nil, false
			}
			v, s = s[1:1+end], s[2+end:]
		case '"':
			i := 1
			for ; i < len(s); i++ {
				if s[i] == '\\' {
					i++
					continue
				}
				if s[i] == '"' {
					break
				}
			}
			if i >= len(s) {
				return nil, false
			}
			u, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return nil, false
			}
			v, s = u, s[i+1:]
		default:
			return nil, false
		}
		out = append(out, v)
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, ",") {
			s = strings.TrimSpace(s[1:])
		} else if s != "" {
			return nil, false
		}
	}
	return out, true
}
