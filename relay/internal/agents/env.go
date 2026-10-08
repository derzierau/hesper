package agents

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// LoginShell is $SHELL, else /bin/zsh.
func LoginShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/zsh"
}

const envMarker = "\x00__HESPERD_ENV__\x00"

// LoginEnv is the environment a login shell of the user sets up (PATH from
// the shell's profile files and so on): a LaunchAgent starts the daemon
// with almost none. The shell starts from a clean environment (loginSeed:
// who the user is, locale, PATH, the agents' configuration homes), so what
// the daemon itself inherited (a terminal's, tmux's, or the markers of a
// Claude Code or Codex session it was started from) never reaches the
// agents. It falls back to the daemon's own environment, which agentEnv
// filters the same way at spawn. Its PATH is always completed
// (completePath).
func LoginEnv(shell string, timeout time.Duration) []string {
	env, _ := ResolveEnv(shell, timeout, 2)
	return env
}

// ResolveEnv is LoginEnv with how it was found (for the log): the login
// shell is asked up to attempts times, each within timeout (a slow
// profile, a busy Mac at login); then the daemon's own environment. The
// PATH is the login shell's, then the daemon's own entries, then the
// standard directories (completePath): a profile that sets a partial PATH,
// or a probe that failed, never leaves the agents without /opt/homebrew/bin
// or ~/.local/bin.
func ResolveEnv(shell string, timeout time.Duration, attempts int) (env []string, source string) {
	if attempts < 1 {
		attempts = 1
	}
	var errs []string
	for i := 0; i < attempts; i++ {
		start := time.Now()
		env, err := probeLogin(shell, timeout)
		if err == nil {
			return completePath(env, os.Getenv("PATH")), fmt.Sprintf("login shell %s (%.2fs)", shell, time.Since(start).Seconds())
		}
		errs = append(errs, err.Error())
	}
	return completePath(os.Environ(), ""), fmt.Sprintf("the daemon's own environment (login shell: %s)", strings.Join(errs, "; "))
}

// probeLogin runs the login shell once: its environment after the marker
// (what the profile prints before it is skipped), NUL-separated, so values
// with spaces, newlines or any other character pass unchanged. The shell
// runs in its own process group, killed whole at the timeout; a
// background process of the profile that keeps the output open does not
// hold the daemon up (WaitDelay).
func probeLogin(shell string, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-c", `printf '\0__HESPERD_ENV__\0'; /usr/bin/env -0`)
	cmd.Stdin = nil
	cmd.Stderr = nil
	if home := os.Getenv("HOME"); home != "" {
		if st, err := os.Stat(home); err == nil && st.IsDir() {
			cmd.Dir = home
		}
	}
	cmd.Env = loginSeed(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s -l timed out after %v", shell, timeout)
	}
	env := parseEnv(out)
	switch {
	case env == nil && err != nil:
		return nil, fmt.Errorf("%s -l: %v", shell, err)
	case env == nil:
		return nil, fmt.Errorf("%s -l printed no environment", shell)
	case envValue(env, "PATH") == "":
		return nil, fmt.Errorf("%s -l printed no PATH", shell)
	}
	// A non-zero status with the whole environment printed (a profile's
	// last command failing) is still the environment.
	return env, nil
}

// parseEnv is the environment printed after the marker; nil without it.
func parseEnv(out []byte) []string {
	i := bytes.Index(out, []byte(envMarker))
	if i < 0 {
		return nil
	}
	var env []string
	for _, kv := range bytes.Split(out[i+len(envMarker):], []byte{0}) {
		if s := string(kv); strings.Contains(s, "=") {
			env = append(env, s)
		}
	}
	return env
}

// standardPath are the directories an agent's PATH always has: where
// Homebrew, npm's global packages, the installers of Claude Code and Codex
// and the system put commands.
func standardPath(home string) []string {
	var dirs []string
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return append(dirs, "/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
}

// completePath sets env's PATH to its own entries, then those of extra
// (the daemon's PATH), then standardPath: each directory once, in that
// order. Only ':' separates entries; empty and relative entries are
// dropped (they would depend on the agent's directory).
func completePath(env []string, extra string) []string {
	home := envValue(env, "HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		if dir == "" || !strings.HasPrefix(dir, "/") {
			return
		}
		if clean := filepath.Clean(dir); !seen[clean] {
			seen[clean] = true
			dirs = append(dirs, dir)
		}
	}
	for _, list := range []string{envValue(env, "PATH"), extra} {
		for _, dir := range strings.Split(list, ":") {
			add(dir)
		}
	}
	for _, dir := range standardPath(home) {
		add(dir)
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			out = append(out, kv)
		}
	}
	return append(out, "PATH="+strings.Join(dirs, ":"))
}

// seedKeys are the daemon's variables a login shell starts from: the
// user, the locale, the ssh agent launchd provides, PATH, and where the
// agents and hesperd keep their configuration. LC_* and XDG_* pass too.
var seedKeys = map[string]bool{
	"HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "TMPDIR": true, "PATH": true, "LANG": true,
	"SSH_AUTH_SOCK": true, "__CF_USER_TEXT_ENCODING": true, "CLAUDE_CONFIG_DIR": true, "CODEX_HOME": true,
	"HESPER_STATE_DIR": true, "HESPER_CONFIG_DIR": true, "HESPER_PROJECT_ROOT": true, "HESPER_WORKTREE_ROOT": true,
	"HESPER_MACHINE": true,
}

func loginSeed(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if seedKeys[k] || (strings.HasPrefix(k, "LC_") && !dropped[k]) || strings.HasPrefix(k, "XDG_") {
			out = append(out, kv)
		}
	}
	return out
}

// dropped are variables an agent must not inherit from the base
// environment: another terminal's (hesperd is the agent's terminal), or
// the runtime markers of another agent (a Claude Code or Codex session the
// daemon was started from). Claude Code that sees CLAUDE_CODE_CHILD_SESSION
// turns its transcripts off, which breaks resume and moves.
var dropped = map[string]bool{
	// Terminals and multiplexers.
	"TMUX": true, "TMUX_PANE": true, "STY": true, "TERM": true, "TERM_PROGRAM": true, "TERM_PROGRAM_VERSION": true,
	"TERM_SESSION_ID": true, "ITERM_SESSION_ID": true, "ITERM_PROFILE": true, "LC_TERMINAL": true,
	"LC_TERMINAL_VERSION": true, "WINDOWID": true, "COLUMNS": true, "LINES": true,
	// The shell's own bookkeeping.
	"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true,
	// hesperd's own, set per agent.
	"HESPER_AGENT_ID": true, "HESPER_SOCKET": true,
	// What Claude Code sets for the processes it starts.
	"CLAUDECODE": true, "CLAUDE_PID": true, "CLAUDE_EFFORT": true, "CLAUDE_PROJECT_DIR": true, "CLAUDE_ENV_FILE": true,
	"CLAUDE_CODE_SESSION_ID": true, "CLAUDE_CODE_CHILD_SESSION": true, "CLAUDE_CODE_SESSION_ATTENDED": true,
	"CLAUDE_CODE_ENTRYPOINT": true, "CLAUDE_CODE_SSE_PORT": true, "CLAUDE_CODE_CHROME_MCP_ORG_DENIED": true,
	"AI_AGENT": true, "TRACEPARENT": true, "TRACESTATE": true,
}

// droppedPrefixes are whole families: other terminals' (Ghostty's surface
// and resources, iTerm, kitty, WezTerm, Alacritty, VS Code, tmux) and
// Codex's runtime (sandbox, thread, session, snapshot and network proxy
// markers). keptCodex is the user's own Codex configuration.
var droppedPrefixes = []string{"GHOSTTY_", "ITERM_", "KITTY_", "WEZTERM_", "ALACRITTY_", "VSCODE_", "TMUX_", "CODEX_"}

var keptCodex = map[string]bool{
	"CODEX_HOME": true, "CODEX_API_KEY": true, "CODEX_ACCESS_TOKEN": true, "CODEX_CA_CERTIFICATE": true,
	"CODEX_SQLITE_HOME": true, "CODEX_GITHUB_PERSONAL_ACCESS_TOKEN": true, "CODEX_CONNECTORS_TOKEN": true,
}

// inherited reports whether kv may pass from the base environment to an
// agent.
func inherited(kv string) bool {
	k, v, _ := strings.Cut(kv, "=")
	if dropped[k] {
		return false
	}
	if keptCodex[k] {
		return true
	}
	if k == "GIT_EDITOR" && v == "true" {
		return false // Claude Code's, for its own git calls
	}
	for _, p := range droppedPrefixes {
		if strings.HasPrefix(k, p) {
			return false
		}
	}
	return true
}

// agentEnv is base (the login environment) without what agents must not
// inherit, with the terminal's and hesperd's variables set.
func agentEnv(base []string, set map[string]string) []string {
	env := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !inherited(kv) {
			continue
		}
		if _, ok := set[k]; ok {
			continue
		}
		env = append(env, kv)
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+set[k])
	}
	return env
}

func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}
