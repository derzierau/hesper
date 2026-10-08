// Command hesperd is Hesper's per-Mac daemon: it owns the agents in their
// PTYs, their state, hooks, approvals, worktrees and persistence, and
// serves the local socket (docs/rebuild-contract.md). It is also the
// Mac's only gateway to the other machines: as a relay host it serves its
// agents to approved devices, as a relay controller it shows the other
// machines' agents through its socket (part R, internal/gateway).
//
//	hesperd serve                          the daemon (LaunchAgent)
//	hesperd attach <id> [--ro] [--owner] [--view] [--view-rows R] [--fit]
//	                                       bridge this terminal to an agent
//	hesperd hook <claude|codex> [event]    deliver a hook event (stdin JSON)
//	hesperd hooks print|install [--dry-run]  the hook configuration
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/derzierau/hesper/relay/internal/agents"
	"github.com/derzierau/hesper/relay/internal/attachtty"
	"github.com/derzierau/hesper/relay/internal/gateway"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

var version = "dev"

const usage = `usage: hesperd serve [--state-dir D] [--config-dir D] [--allow-shell] [--direct auto|on|off]
       hesperd attach <id> [--ro] [--owner] [--view] [--view-rows R] [--fit]
       hesperd hook <claude|codex> [event] [payload]
       hesperd hooks print [--bin PATH]
       hesperd hooks install [--dry-run] [--bin PATH] [--claude-settings P] [--codex-home D]
       hesperd hooks codex-args [--bin PATH]
       hesperd version`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "attach":
		return attach(args[1:])
	case "hook":
		hook(args[1:])
		return 0 // never fail the agent
	case "hooks":
		return hooks(args[1:])
	case "version", "--version":
		fmt.Println("hesperd", version)
		return 0
	}
	fmt.Fprintln(os.Stderr, usage)
	return 2
}

func serve(args []string) int {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	stateDir := f.String("state-dir", wire.StateDir(), "State directory (agents.json, the socket, relay credentials, device keys)")
	configDir := f.String("config-dir", wire.ConfigDir(), "Config directory (profiles.json, settings.json, machines.json)")
	socket := f.String("socket", "", "Socket path (default: hesperd.sock in the state directory)")
	hostCreds := f.String("host-credentials", "", "Host enrollment: serve this Mac's agents to approved devices (default: host.credentials.json in the state directory; missing: the role is off)")
	controllerCreds := f.String("controller-credentials", "", "Controller enrollment: reach the other machines' agents (default: controller.credentials.json in the state directory; missing: the role is off)")
	controlSocket := f.String("control-socket", "", "Socket through which hesperctl's relay commands use the controller connection (default: controller.sock in the state directory; \"off\" disables it)")
	allowShell := f.Bool("allow-shell", false, "Offer shells on this Mac to devices approved with the shell right (Touch ID on theirs)")
	requireKeys := f.Bool("require-device-keys", false, "Refuse every request not signed by an approved device, even before any device is approved")
	directMode := f.String("direct", "auto", "Direct path for machines on the same network: on, off, or auto (on unless the macOS firewall would ask about or block incoming connections)")
	directPort := f.Int("direct-port", 0, "TCP port of the direct path on this Mac's private addresses (0: random)")
	keepAwake := f.Bool("keep-awake", runtime.GOOS == "darwin", "Prevent idle sleep while an agent works or waits for an answer (macOS)")
	minBattery := f.Int("keep-awake-min-battery", 20, "Allow sleep below this battery percentage on battery power")
	if err := f.Parse(args); err != nil {
		return 2
	}
	if *socket == "" {
		*socket = filepath.Join(*stateDir, "hesperd.sock")
	}
	if *hostCreds == "" {
		*hostCreds = filepath.Join(*stateDir, "host.credentials.json")
	}
	if *controllerCreds == "" {
		*controllerCreds = filepath.Join(*stateDir, "controller.credentials.json")
	}
	switch *controlSocket {
	case "":
		*controlSocket = filepath.Join(*stateDir, "controller.sock")
	case "off":
		*controlSocket = ""
	}
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("hesperd: ")
	agents.Version = version
	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := gateway.Start(ctx, gateway.Config{
		Registry:        agents.Options{StateDir: *stateDir, ConfigDir: *configDir, Socket: *socket},
		HostCredentials: *hostCreds, ControllerCredentials: *controllerCreds, KeysDir: *stateDir,
		AllowShell: *allowShell, RequireDeviceKeys: *requireKeys, Direct: *directMode, DirectPort: *directPort,
		ControlSocket: *controlSocket, KeepAwake: *keepAwake, MinBattery: *minBattery,
	})
	if err != nil {
		log.Print(err)
		return 1
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	log.Printf("serving %s as %s (%d agents)", *socket, d.Registry.Machine(), len(d.Registry.List()))
	for {
		select {
		case sig := <-stop:
			log.Printf("%v: stopping", sig)
			d.Close()
			return 0
		case err := <-d.Err:
			// A role needs a new sign-in (the error names the command).
			// The agents keep running; restart hesperd after signing in.
			log.Print(err)
		}
	}
}

func attach(args []string) int {
	f := flag.NewFlagSet("attach", flag.ContinueOnError)
	ro := f.Bool("ro", false, "Read only: input is not sent")
	owner := f.Bool("owner", false, "This view sets the agent's PTY size")
	view := f.Bool("view", false, "Read only, the agent's last rows that fit this terminal (follows its size)")
	viewRows := f.Int("view-rows", 0, "With --view: show this many rows (implies --view)")
	fit := f.Bool("fit", false, "With --view: ask that the PTY take this terminal's size while no owner holds it (implies --view)")
	socket := f.String("socket", wire.SocketPath(), "hesperd socket")
	id, err := parse(f, args)
	if err != nil || id == "" {
		fmt.Fprintln(os.Stderr, "usage: hesperd attach <id> [--ro] [--owner] [--view] [--view-rows R] [--fit]")
		return 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := attachtty.Run(ctx, attachtty.Options{Socket: *socket, ID: id, RO: *ro || *view || *viewRows > 0 || *fit, Owner: *owner,
		View: *view || *viewRows > 0 || *fit, ViewRows: *viewRows, Fit: *fit}); err != nil {
		fmt.Fprintln(os.Stderr, "hesperd attach:", err)
		return 1
	}
	return 0
}

// parse takes flags before and after one positional argument.
func parse(f *flag.FlagSet, args []string) (string, error) {
	var positional []string
	for {
		if err := f.Parse(args); err != nil {
			return "", err
		}
		args = f.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	if len(positional) != 1 {
		return "", errors.New("one argument")
	}
	return positional[0], nil
}

// Hook delivery limits: the daemon answers in about a millisecond; a down
// daemon refuses the connection at once.
const (
	hookTimeout   = 250 * time.Millisecond
	stdinTimeout  = 2 * time.Second
	maxHookInput  = 8 << 20
	maxHookToSend = 1 << 20
)

// hook reads the hook's JSON (stdin, or the argument Codex's notify passes)
// and hands it to the daemon. It never blocks or fails the agent.
func hook(args []string) {
	if len(args) < 1 {
		return
	}
	source, event := args[0], ""
	if len(args) > 1 {
		event = args[1]
	}
	var payload []byte
	if len(args) > 3 && args[2] == "--then" {
		// codex notify chained: hesperd's, then the program that was
		// there before (with the same JSON argument), not waited for.
		payload = []byte(args[len(args)-1])
		then := append([]string{}, args[3:]...)
		defer chainNotify(then)
	} else if len(args) > 2 {
		payload = []byte(args[2]) // codex notify: the JSON is the last argument
	} else if !term.IsTerminal(int(os.Stdin.Fd())) {
		payload = readStdin()
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if !json.Valid(payload) {
		return
	}
	payload = trimPayload(payload)
	wire.SendHook(wire.SocketPath(), wire.HookParams{Agent: os.Getenv("HESPER_AGENT_ID"), Source: source, Event: event, Payload: payload}, hookTimeout)
}

// chainNotify starts the notify program hesperd's came before (argv, the
// payload last) and leaves it running.
func chainNotify(argv []string) {
	if len(argv) < 2 {
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		cmd.Process.Release()
	}
}

func readStdin() []byte {
	out := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(os.Stdin, maxHookInput))
		out <- data
	}()
	select {
	case data := <-out:
		return data
	case <-time.After(stdinTimeout):
		return nil
	}
}

// trimPayload drops tool output from payloads too large to send: the
// daemon never needs it.
func trimPayload(p []byte) []byte {
	if len(p) <= maxHookToSend {
		return p
	}
	var data map[string]any
	if json.Unmarshal(p, &data) != nil {
		return []byte("{}")
	}
	for _, k := range []string{"tool_response", "tool_output", "output", "input-messages"} {
		delete(data, k)
	}
	out, _ := json.Marshal(data)
	if len(out) > maxHookToSend {
		for _, k := range []string{"tool_input", "last_assistant_message", "last-assistant-message", "message"} {
			delete(data, k)
		}
		out, _ = json.Marshal(data)
	}
	return out
}

func hooks(args []string) int {
	if len(args) == 0 || (args[0] != "print" && args[0] != "install" && args[0] != "codex-args") {
		fmt.Fprintln(os.Stderr, "usage: hesperd hooks print|install|codex-args [--dry-run] [--bin PATH] [--claude-settings P] [--codex-home D]")
		return 2
	}
	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	f := flag.NewFlagSet("hooks", flag.ContinueOnError)
	self, _ := os.Executable()
	if real, err := filepath.EvalSymlinks(self); err == nil {
		self = real
	}
	bin := f.String("bin", self, "The hesperd the hooks call (the installed one)")
	dry := f.Bool("dry-run", false, "Show the files an install would write; write nothing")
	claudeSettings := f.String("claude-settings", filepath.Join(home, ".claude/settings.json"), "Claude Code settings.json")
	codexDir := f.String("codex-home", codexHome, "Codex home (hooks.json, config.toml)")
	if err := f.Parse(args[1:]); err != nil {
		return 2
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if args[0] == "codex-args" {
		// The -c options a Codex agent gets (one per line): hesperd's
		// hooks, trusted, and notify.
		for _, a := range agents.CodexSessionArgs(*bin, true, true, nil) {
			fmt.Println(a)
		}
		return 0
	}
	if args[0] == "print" {
		enc.Encode(map[string]any{
			"claude": map[string]any{"settings.json": map[string]any{"hooks": agents.ClaudeHooks(*bin)}},
			"codex":  map[string]any{"hooks.json": map[string]any{"hooks": agents.CodexHooks(*bin)}, "config.toml": map[string]any{"notify": agents.CodexNotify(*bin)}},
		})
		return 0
	}
	files, err := agents.PlanHookInstall(*bin, *claudeSettings, *codexDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hesperd hooks:", err)
		return 1
	}
	if *dry {
		enc.Encode(files)
		return 0
	}
	if err := agents.InstallHooks(files); err != nil {
		fmt.Fprintln(os.Stderr, "hesperd hooks:", err)
		return 1
	}
	for _, f := range files {
		state := "unchanged"
		if f.Changed {
			state = "written"
		}
		fmt.Printf("%s: %s\n", f.Path, state)
		if f.Warning != "" {
			fmt.Fprintf(os.Stderr, "%s: %s\n", f.Path, f.Warning)
		}
	}
	fmt.Println("Codex runs hooks only after they are trusted: open Codex and run /hooks once.")
	return 0
}
