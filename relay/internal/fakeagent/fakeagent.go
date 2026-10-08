// Package fakeagent is the fake agent programs tests run in place of
// claude, codex and a login shell (never the real ones). A test binary
// dispatches to Run from its TestMain when AGENTS_FAKE=1.
package fakeagent

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Fake agents: the test binary run as "claude", "codex" or "shell" (with
// AGENTS_FAKE=1). They draw like a TUI (alternate screen, colors, cursor),
// send Claude- and Codex-shaped hook payloads to the daemon (as `hesperd
// hook` does), ask for approval and act on the key they read. Never a real
// claude or codex.

type fake struct {
	kind    string
	args    []string
	session string
	resumed bool
	task    string
	in      *bufio.Reader
	turn    int
	cwd     string
	// codexServer: hooks without HESPER_AGENT_ID (Codex runs them in its
	// own server), found by session and directory.
	codexServer bool
	noHooks     bool     // Codex without trusted hooks: approval by bell + screen only
	overrides   []string // codex -c options
}

// Screens recorded from the real programs (testdata/README).
var (
	//go:embed testdata/claude-trust.txt
	claudeTrustScreen string
	//go:embed testdata/claude-theme.txt
	claudeThemeScreen string
	//go:embed testdata/claude-permission.txt
	claudePermissionScreen string
	//go:embed testdata/claude-permission-noalways.txt
	claudePermissionNoAlwaysScreen string
	//go:embed testdata/codex-update.txt
	codexUpdateScreen string
	//go:embed testdata/codex-composer.txt
	codexComposerScreen string
)

// Run is the fake program: args[0] is claude, codex or shell.
func Run(args []string) {
	if (args[0] == "resume" || args[0] == "fork") && len(args) > 1 {
		// `codex resume` of the profile [<test binary>, "codex", …]:
		// <test binary> resume codex …
		args = append([]string{args[1], args[0]}, args[2:]...)
	}
	kind := args[0]
	f := &fake{kind: kind, args: args[1:], in: bufio.NewReader(os.Stdin)}
	f.cwd, _ = os.Getwd()
	logArgs(args)
	if os.Getenv("FAKE_PRINT_ENV") == "1" {
		logEnv()
	}
	switch kind {
	case "shell":
		old, _ := term.MakeRaw(0)
		defer term.Restore(0, old)
		os.Stdout.WriteString("$ ")
		for {
			line := f.readLine()
			if line == "exit" {
				return
			}
			fmt.Printf("\r\nran %s\r\n$ ", line)
		}
	case "claude", "codex":
		f.parse()
		old, _ := term.MakeRaw(0)
		if os.Getenv("FAKE_HUP_EXIT0") == "1" {
			// As Claude Code: a hangup ends it cleanly, status 0.
			hup := make(chan os.Signal, 1)
			signal.Notify(hup, syscall.SIGHUP)
			go func() {
				<-hup
				term.Restore(0, old)
				os.Exit(0)
			}()
		}
		code := f.run()
		term.Restore(0, old)
		os.Exit(code)
	}
}

func logArgs(args []string) {
	dir := os.Getenv("FAKE_LOG")
	if dir == "" {
		return
	}
	id := strings.ReplaceAll(os.Getenv("HESPER_AGENT_ID"), "/", "_")
	data, _ := json.Marshal(args)
	fh, err := os.OpenFile(filepath.Join(dir, id+".argv"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		fh.Write(append(data, '\n'))
		fh.Close()
	}
}

// logEnv records the environment the agent got (<id>.env, one per line).
func logEnv() {
	id := strings.ReplaceAll(os.Getenv("HESPER_AGENT_ID"), "/", "_")
	os.WriteFile(filepath.Join(os.Getenv("FAKE_LOG"), id+".env"), []byte(strings.Join(os.Environ(), "\n")+"\n"), 0o600)
}

// logKey records the key that answered an approval.
func logKey(key byte) {
	id := strings.ReplaceAll(os.Getenv("HESPER_AGENT_ID"), "/", "_")
	fh, err := os.OpenFile(filepath.Join(os.Getenv("FAKE_LOG"), id+".keys"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		fh.Write([]byte{key})
		fh.Close()
	}
}

func (f *fake) parse() {
	args := f.args
	if f.kind == "codex" {
		f.codexServer = os.Getenv("FAKE_CODEX_SERVER") == "1"
		f.noHooks = os.Getenv("FAKE_CODEX_NOHOOKS") == "1"
		if len(args) > 0 && args[0] == "resume" {
			f.resumed, f.session = true, args[len(args)-1]
			for i := 1; i < len(args)-1; i++ {
				if args[i] == "-c" {
					i++
					f.overrides = append(f.overrides, args[i])
				}
			}
			return
		}
		f.session = fmt.Sprintf("codex-%d", time.Now().UnixNano())
		if len(args) > 0 && args[0] == "fork" { // codex fork [options] ID: a new session
			f.resumed = true
			return
		}
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "--enable", "--disable":
			i++
			f.overrides = append(f.overrides, args[i])
		case "--no-daemon":
		case "--session-id":
			i++
			f.session = args[i]
		case "--resume":
			i++
			f.session, f.resumed = args[i], true
		case "--remote-control", "--permission-mode":
			i++
		case "--dangerously-bypass-approvals-and-sandbox", "--", "--fork-session":
		default:
			f.task = args[i]
		}
	}
}

func (f *fake) hook(event string, payload map[string]any) {
	if f.noHooks {
		return
	}
	payload["hook_event_name"] = event
	payload["session_id"] = f.session
	payload["cwd"] = f.cwd
	if f.kind == "codex" {
		payload["turn_id"] = fmt.Sprintf("turn-%d", f.turn)
	}
	data, _ := json.Marshal(payload)
	agent := os.Getenv("HESPER_AGENT_ID")
	if f.codexServer {
		agent = ""
	}
	wire.SendHook(os.Getenv("HESPER_SOCKET"), wire.HookParams{Agent: agent, Source: f.kind, Event: event, Payload: data}, time.Second)
}

func (f *fake) draw(status string) {
	if f.kind == "codex" {
		// Codex's composer, as the real one draws it when idle.
		screen := strings.NewReplacer("{dir}", f.cwd, "{status}", strings.ReplaceAll(status, "\r\n", "\n")).Replace(codexComposerScreen)
		os.Stdout.WriteString("\x1b[H\x1b[2J" + strings.ReplaceAll(screen, "\n", "\r\n"))
		return
	}
	fmt.Printf("\x1b[H\x1b[2J\x1b[1;35m✳ fake %s\x1b[0m  \x1b[2msession %s\x1b[0m\r\n\r\n%s\r\n\r\n\x1b[38;2;120;200;255m>\x1b[0m ", f.kind, f.session, status)
}

func (f *fake) readKey() byte {
	b, err := f.in.ReadByte()
	if err != nil {
		os.Exit(0)
	}
	return b
}

// readLine reads typed or pasted text up to \r (bracketed paste markers
// dropped).
func (f *fake) readLine() string {
	var line []byte
	for {
		b := f.readKey()
		if b == '\r' {
			s := strings.ReplaceAll(strings.ReplaceAll(string(line), "\x1b[200~", ""), "\x1b[201~", "")
			return s
		}
		line = append(line, b)
		os.Stdout.Write([]byte{b})
	}
}

// claudeTrusted: FAKE_CLAUDE_JSON (Claude Code's ~/.claude.json) trusts
// the directory or a parent, as the real one decides.
func (f *fake) claudeTrusted() bool {
	data, err := os.ReadFile(os.Getenv("FAKE_CLAUDE_JSON"))
	if err != nil {
		return false
	}
	var c struct {
		Projects map[string]struct {
			Accepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	json.Unmarshal(data, &c)
	dir, _ := filepath.EvalSymlinks(f.cwd)
	for {
		if c.Projects[dir].Accepted {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// askTrust is Claude Code 2.1's trust question, its cursor on "No, exit"
// as in the trial: 1 or Enter on "Yes" trusts, 2, Enter on "No" or Esc
// exits (status 1), arrows move the cursor.
func (f *fake) askTrust() bool {
	cursor := 1
	for {
		yes, no := "   1. Yes, I trust this folder", "   2. No, exit"
		if cursor == 0 {
			yes = " ❯ 1. Yes, I trust this folder"
		} else {
			no = " ❯ 2. No, exit"
		}
		screen := strings.NewReplacer("{dir}", f.cwd, "{choices}", yes+"\n"+no).Replace(claudeTrustScreen)
		os.Stdout.WriteString("\x1b[H\x1b[2J" + strings.ReplaceAll(screen, "\n", "\r\n"))
		switch b := f.readKey(); b {
		case '1':
			return true
		case '2':
			return false
		case '\r':
			return cursor == 0
		case 0x1b:
			if f.loneEsc() {
				return false // Esc
			}
			if f.readKey() == '[' {
				switch f.readKey() {
				case 'A':
					cursor = 0
				case 'B':
					cursor = 1
				}
			}
		}
	}
}

// loneEsc: the Esc just read is the key, not the start of a sequence.
func (f *fake) loneEsc() bool {
	if f.in.Buffered() == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	return f.in.Buffered() == 0
}

// askUpdate is Codex 0.160's update screen: Esc skips; Enter takes the
// cursor's choice (1, the default: update; 2, 3: skip); a digit moves
// the cursor. Updating ends Codex (it asks to be restarted).
func (f *fake) askUpdate() bool {
	cursor := byte('1')
	for {
		screen := strings.Replace(codexUpdateScreen, "› 1.", "  1.", 1)
		screen = strings.Replace(screen, "  "+string(cursor)+".", "› "+string(cursor)+".", 1)
		os.Stdout.WriteString("\x1b[H\x1b[2J" + strings.ReplaceAll(screen, "\n", "\r\n"))
		b := f.readKey()
		switch {
		case b == 0x1b && f.loneEsc():
			logKey(b)
			return false
		case b >= '1' && b <= '3':
			logKey(b)
			cursor = b
		case b == '\r':
			logKey(b)
			return cursor == '1'
		}
	}
}

// skipsUpdateCheck: -c check_for_update_on_startup=false.
func (f *fake) skipsUpdateCheck() bool {
	for _, o := range f.overrides {
		if o == "check_for_update_on_startup=false" {
			return true
		}
	}
	return false
}

// transcript is where Claude Code keeps a session (FAKE_CLAUDE_HOME).
func (f *fake) transcript() string {
	home := os.Getenv("FAKE_CLAUDE_HOME")
	if home == "" || f.kind != "claude" {
		return ""
	}
	slug := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, f.cwd)
	return filepath.Join(home, "projects", slug, f.session+".jsonl")
}

func (f *fake) run() int {
	os.Stdout.WriteString("\x1b[?1049h\x1b[?2004h")
	if once := os.Getenv("FAKE_RESUME_EXIT0_ONCE"); once != "" && f.resumed && os.Remove(once) == nil {
		// A resume that ends at once with status 0 (as Claude did on
		// the Mac mini after a daemon restart): once per file.
		os.Stdout.WriteString("Remote control session ended\r\n")
		return 0
	}
	if t := f.transcript(); t != "" && f.resumed {
		if _, err := os.Stat(t); err != nil {
			// As claude --resume of a session it never saved.
			fmt.Printf("No conversation found with session ID: %s\r\n", f.session)
			return 1
		}
	}
	if f.kind == "claude" && os.Getenv("FAKE_CLAUDE_JSON") != "" && !f.resumed && !f.claudeTrusted() {
		if !f.askTrust() {
			return 1
		}
	}
	if os.Getenv("FAKE_FIRST_RUN") == "theme" && !f.resumed {
		os.Stdout.WriteString("\x1b[H\x1b[2J" + strings.ReplaceAll(claudeThemeScreen, "\n", "\r\n"))
		for f.readKey() != '\r' {
		}
	}
	if os.Getenv("FAKE_TRUST_ASK") == "1" && !f.resumed {
		f.draw("Do you trust the files in this folder?\r\n\x1b[7m❯ 1. Yes, proceed\x1b[0m\r\n  2. No, exit")
		if f.readKey() != '1' {
			return 1
		}
	}
	if f.kind == "codex" && os.Getenv("FAKE_CODEX_UPDATE") == "1" && !f.skipsUpdateCheck() {
		if f.askUpdate() {
			os.Stdout.WriteString("\x1b[H\x1b[2JUpdating Codex via `brew upgrade --cask codex`...\r\n")
			return 0
		}
	}
	source := "startup"
	if f.resumed {
		source = "resume"
	}
	// Codex 0.160 sends no SessionStart before the first prompt (seen
	// after `codex resume`): FAKE_CODEX_NO_SESSIONSTART.
	if f.kind != "codex" || os.Getenv("FAKE_CODEX_NO_SESSIONSTART") != "1" {
		f.hook("SessionStart", map[string]any{"source": source})
	}
	f.draw("ready")
	task := f.task
	if task == "" && os.Getenv("FAKE_TRUST_ASK") == "1" && !f.resumed {
		task = f.readLine() // typed in by the daemon
	}
	if task != "" {
		f.work(task)
	}
	for {
		line := f.readLine()
		switch line {
		case "exit":
			f.hook("SessionEnd", map[string]any{})
			return 0
		case "crash":
			return 2
		}
		f.work(line)
	}
}

// work runs one turn: a tool that needs approval, then the end of the turn.
func (f *fake) work(prompt string) {
	f.turn++
	if t := f.transcript(); t != "" {
		// Claude Code saves the conversation with its first message.
		os.MkdirAll(filepath.Dir(t), 0o700)
		if fh, err := os.OpenFile(t, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			data, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": prompt}})
			fh.Write(append(data, '\n'))
			fh.Close()
		}
	}
	f.hook("UserPromptSubmit", map[string]any{"prompt": prompt})
	f.draw("working on: " + prompt)
	tool := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "git push origin main"}}
	if f.kind == "codex" {
		tool = map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": []any{"bash", "-lc", "git push origin main"}}}
	}
	f.hook("PreToolUse", tool)
	if strings.Contains(prompt, "dev server") {
		// A command left running, as a Bash tool's background job: a
		// shell with a long command under it (its pid in <id>.child).
		cmd := exec.Command("/bin/sh", "-c", "sleep 600; true")
		if cmd.Start() == nil {
			id := strings.ReplaceAll(os.Getenv("HESPER_AGENT_ID"), "/", "_")
			os.WriteFile(filepath.Join(os.Getenv("FAKE_LOG"), id+".child"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		}
	}
	if strings.Contains(prompt, "keep working") {
		// Working until a key (Esc interrupts the turn).
		f.readKey()
		f.loneEsc()
		f.finish("Interrupted")
		return
	}
	if !strings.Contains(prompt, "approve") {
		f.hook("PostToolUse", tool)
		f.finish("Done: " + prompt)
		return
	}
	f.hook("PermissionRequest", tool)
	noAlways := os.Getenv("FAKE_PERMISSION_NO_ALWAYS") == "1"
	if f.kind == "codex" {
		f.draw("\x1b[1mWould you like to run the following command?\x1b[0m\r\n\r\n  $ git push origin main\r\n\r\n› 1. Yes, proceed (y)\r\n  2. Yes, and don't ask again for this command (a)\r\n  3. No, and tell Codex what to do differently (esc)\x07")
	} else {
		screen := claudePermissionScreen
		if noAlways {
			screen = claudePermissionNoAlwaysScreen
		}
		screen = strings.NewReplacer("{command}", "git push origin main", "{description}", "Push the branch",
			"{prefix}", "git push", "{dir}", f.cwd).Replace(screen)
		os.Stdout.WriteString("\x1b[H\x1b[2J" + strings.ReplaceAll(screen, "\n", "\r\n"))
	}
	key := f.readKey()
	logKey(key)
	f.draw(fmt.Sprintf("answer: %q", key))
	if key == '2' && noAlways {
		key = 0x1b // 2 is "No" there
	}
	switch key {
	case '1', '2', 'y', 'a':
		f.hook("PostToolUse", tool)
		f.finish("Pushed.\n\nOpened PR #482 (draft)")
	default:
		// Declined: the turn ends; what to do instead may follow.
		f.draw("declined")
	}
}

func (f *fake) finish(message string) {
	f.hook("Stop", map[string]any{"last_assistant_message": message})
	f.draw("turn done")
}
