package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// `hesperctl mcp`: Hesper as an MCP server on stdio, so Claude Code and
// Codex (inside Hesper or not) drive agents as tools.
//
// Hand-rolled (no SDK): newline-delimited JSON-RPC 2.0 with the
// initialize handshake (protocol revisions 2024-11-05 … 2025-11-25; a
// dual-era client's server/discover probe gets "method not found" and
// falls back to initialize). Tools are generated from the command
// registry: one per command not marked NoMCP, its input schema from the
// command's flags and the positional arguments in its Usage. A call runs
// the command as a child process of this server (hesperctl itself, with
// --json), so calls run concurrently, a cancelled call is interrupted,
// and the caller's environment (HESPER_AGENT_ID, HESPER_SOCKET) and
// process ancestry (hesperd's verified caller) pass through unchanged.

// mcpVersions are the protocol revisions served, newest first.
var mcpVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const (
	// mcpMaxWait caps (and is the default of) a tool's timeout parameter.
	mcpMaxWait = 10 * time.Minute
	// mcpCallLimit bounds any one call (the child is interrupted).
	mcpCallLimit = mcpMaxWait + time.Minute
	// mcpOutputLimit bounds what a call returns.
	mcpOutputLimit = 4 << 20
)

// mcpExecutable is the hesperctl a tool call runs (tests replace it).
var mcpExecutable = os.Executable

const mcpInstructions = `Hesper runs Claude Code, Codex and shell agents on the owner's Macs (hesperd). These tools are hesperctl's commands: each parameter is the command's flag or argument of that name, the result is the command's --json output (an action with no output returns {"ok":true}). Errors come back as isError with {"error":{"code","message"},"exitCode":N}; exit codes: 1 error, 2 usage, 3 not found, 4 hesperd or machine unavailable, 5 forbidden, 6 timeout, 7 exists. Agent ids are machine/local (mini/a7f3k2); the local id or a unique name works too. Inside a Hesper agent, agents you start are your children: you may steer them, answer them only with let-parent-answer, and you cannot answer your own approvals. Waiting tools take timeout (default and at most 10m). Resources: hesper://agents, hesper://needs-you, hesper://reference (the full CLI reference).`

func init() {
	register(Command{
		Name: "mcp", Group: groupHelp, NoMCP: true,
		Summary: "Serve Hesper as MCP tools on stdio (for Claude Code, Codex)",
		Usage:   "mcp [--daemon-socket S]",
		Help: "A Model Context Protocol server on stdin/stdout (newline-delimited JSON-RPC; logs on stderr). Every hesperctl command except the interactive and streaming ones is a tool (hesper_agents_new, hesper_history_search, …) whose parameters are its flags and arguments; a call runs the command with --json. Resources: hesper://agents (ls), hesper://needs-you (agents in approval or question), hesper://reference (this reference); agents and needs-you can be subscribed to.\n\n" +
			"Register it with install.sh --mcp, or: claude mcp add --scope user hesper -- hesperctl mcp; for Codex [mcp_servers.hesper] command = \"hesperctl\", args = [\"mcp\"] in ~/.codex/config.toml. Inside a Hesper agent the agent's HESPER_AGENT_ID and process tree pass through, so the agent tree's rules apply to its calls.",
		Examples: []string{"claude mcp add --scope user hesper -- hesperctl mcp"},
		Run:      mcpCommand,
	})
}

func mcpCommand(ctx context.Context, f *flag.FlagSet, args []string) error {
	socket := f.String("daemon-socket", "", "hesperd socket for the tools ($HESPER_SOCKET)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() > 0 {
		return usagef("usage: hesperctl mcp [--daemon-socket S]")
	}
	exe, err := mcpExecutable()
	if err != nil {
		return err
	}
	s := newMCPServer(os.Stdout, os.Stderr, exe, nil)
	if *socket != "" {
		s.env = append(os.Environ(), "HESPER_SOCKET="+*socket)
	}
	return s.serve(ctx, os.Stdin)
}

// --- tools from the registry -------------------------------------------

// mcpParam is one parameter of a tool: a flag or a positional argument.
type mcpParam struct {
	Name string // the property (flag name, or the argument's lower-case name)
	// Kind: boolean, integer, number, duration, string, list (repeatable
	// flag or variadic argument).
	Kind     string
	Flag     bool
	Required bool
	Usage    string
	Default  string
}

type mcpTool struct {
	Name    string
	Command *Command
	Params  []mcpParam
	// Args: the positional arguments could not be derived from Usage;
	// the tool takes them as an args array.
	Args bool
}

// mcpHiddenFlags are flags tools do not offer: --json is forced, the
// socket is the server's.
var mcpHiddenFlags = map[string]bool{"json": true, "daemon-socket": true}

// mcpTools are the tools, sorted by name.
func mcpTools() []*mcpTool {
	var list []*mcpTool
	for _, c := range commands {
		if c.NoMCP {
			continue
		}
		list = append(list, newMCPTool(c))
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// mcpToolName: hesper_ + the name in snake case; the agent commands
// (one word, group Agents) get agents_: hesper_agents_new.
func mcpToolName(c *Command) string {
	name := c.Name
	if c.Group == groupAgents && !strings.Contains(name, " ") {
		name = "agents " + name
	}
	return "hesper_" + strings.NewReplacer(" ", "_", "-", "_").Replace(name)
}

func newMCPTool(c *Command) *mcpTool {
	t := &mcpTool{Name: mcpToolName(c), Command: c}
	f := commandFlagSet(c)
	if f == nil {
		f = newFlagSet(c.Name)
	}
	flags := map[string]mcpParam{}
	f.VisitAll(func(fl *flag.Flag) {
		if mcpHiddenFlags[fl.Name] {
			return
		}
		_, usage := flag.UnquoteUsage(fl)
		p := mcpParam{Name: fl.Name, Kind: flagKind(fl), Flag: true, Usage: usage}
		if fl.DefValue != "" && fl.DefValue != "false" && fl.DefValue != "0" && fl.DefValue != "0s" && p.Kind != "list" {
			p.Default = portableDefault(fl.DefValue)
		}
		flags[fl.Name] = p
		t.Params = append(t.Params, p)
	})
	positional, ok := usagePositionals(c, flags)
	if !ok {
		t.Args = true
		return t
	}
	t.Params = append(positional, t.Params...)
	return t
}

// flagKind is a flag's parameter kind, from its value's type.
func flagKind(fl *flag.Flag) string {
	if b, ok := fl.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
		return "boolean"
	}
	if g, ok := fl.Value.(flag.Getter); ok {
		switch g.Get().(type) {
		case int, int64, uint, uint64:
			return "integer"
		case float64:
			return "number"
		case time.Duration:
			return "duration"
		case string:
			return "string"
		}
	}
	// Our own repeatable flags (listFlag, keyFlag): a list.
	return "list"
}

var (
	usageBrackets = regexp.MustCompile(`\[[^\[\]]*\]`)
	usageArgName  = regexp.MustCompile(`^[A-Z][A-Z0-9_-]*(\|[A-Z][A-Z0-9_-]*)*$`)
)

// usagePositionals derives the positional arguments from c.Usage ("send
// ID TEXT… [--no-submit]"): upper-case words, optional in brackets,
// variadic with …; flags and their values are skipped. ok is false when
// a word is neither.
func usagePositionals(c *Command, flags map[string]mcpParam) ([]mcpParam, bool) {
	usage := strings.TrimSpace(strings.TrimPrefix(c.Usage, c.Name))
	// Drop the flag groups ([--x V], [--a|--b], [flags], nested ones from
	// the inside out); argument groups ([ID], [TASK…|-]) are kept, marked
	// optional with ⟦…⟧.
	for {
		next := usageBrackets.ReplaceAllStringFunc(usage, func(g string) string {
			inner := strings.TrimSpace(g[1 : len(g)-1])
			if strings.HasPrefix(inner, "-") || inner == "flags" || inner == "" {
				return ""
			}
			return "⟦" + inner + "⟧"
		})
		if next == usage {
			break
		}
		usage = next
	}
	var list []mcpParam
	words := strings.Fields(usage)
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "…" || w == "|" {
			continue
		}
		if strings.HasPrefix(w, "-") {
			name, _, hasValue := strings.Cut(strings.TrimLeft(w, "-"), "=")
			if p, ok := flags[name]; ok && p.Kind != "boolean" && !hasValue && i+1 < len(words) {
				i++ // the flag's value: --until COND
			}
			continue
		}
		optional := strings.HasPrefix(w, "⟦")
		w = strings.TrimSuffix(strings.TrimPrefix(w, "⟦"), "⟧")
		w = strings.TrimSuffix(w, "|-") // TASK…|-: - (stdin) is not for tools
		variadic := strings.Contains(w, "…")
		w = strings.ReplaceAll(w, "…", "")
		if !usageArgName.MatchString(w) {
			return nil, false
		}
		name := strings.ToLower(strings.ReplaceAll(w, "|", "_or_"))
		if _, taken := flags[name]; taken {
			name += "_arg"
		}
		p := mcpParam{Name: name, Kind: "string", Required: !optional && !variadic, Usage: w}
		if variadic {
			p.Kind = "list"
		}
		list = append(list, p)
	}
	return list, true
}

// schema is the tool's inputSchema (JSON Schema, draft 2020-12 subset).
func (t *mcpTool) schema() map[string]any {
	props := map[string]any{}
	var required []string
	for _, p := range t.Params {
		s := map[string]any{}
		desc := p.Usage
		switch p.Kind {
		case "boolean", "integer", "number", "string":
			s["type"] = p.Kind
		case "duration":
			s["type"] = "string"
			desc += " (a duration: 90s, 5m, 2h)"
			if p.Name == "timeout" {
				desc += fmt.Sprintf("; at most %s, which is also the default here", mcpDuration(mcpMaxWait))
			}
		case "list":
			s["type"] = "array"
			s["items"] = map[string]any{"type": "string"}
			if p.Flag {
				desc += " (repeatable: one item each)"
			}
		}
		if !p.Flag {
			desc = "Argument " + p.Usage
			if p.Kind == "list" {
				desc += " (one or more; joined with spaces where the command takes text)"
			}
			if h := argHint[p.Usage]; h != "" {
				desc += ": " + h
			}
		}
		if p.Default != "" {
			desc += " (default " + p.Default + ")"
		}
		s["description"] = desc
		props[p.Name] = s
		if p.Required {
			required = append(required, p.Name)
		}
	}
	if t.Args {
		props["args"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"},
			"description": "The command's positional arguments, in order (see the usage in the description)"}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// argHint explains the common argument names.
var argHint = map[string]string{
	"ID":      "an agent's full id (mini/a7f3k2), local id (a7f3k2) or unique name; for history commands a session id machine:kind:sessionId",
	"TASK":    "the new agent's first prompt",
	"TEXT":    "the text to type",
	"MACHINE": "a machine's short name",
	"PROJECT": "a project id, unique name or folder",
	"GROUP":   "a group id or unique name",
}

func mcpDuration(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

func (t *mcpTool) description() string {
	c := t.Command
	var b strings.Builder
	fmt.Fprintf(&b, "%s.", c.Summary)
	if h := strings.TrimSpace(c.Help); h != "" {
		fmt.Fprintf(&b, "\n\n%s", h)
	}
	fmt.Fprintf(&b, "\n\nCLI: hesperctl %s", c.Usage)
	if c.Output != "" {
		fmt.Fprintf(&b, "\nReturns (JSON): %s", c.Output)
	} else {
		b.WriteString("\nReturns {\"ok\":true} when done.")
	}
	if len(c.Examples) > 0 {
		b.WriteString("\nCLI examples (parameters are these flags and arguments):")
		for _, e := range c.Examples {
			fmt.Fprintf(&b, "\n  %s", e)
		}
	}
	return b.String()
}

func (t *mcpTool) describe() map[string]any {
	c := t.Command
	annotations := map[string]any{"title": "hesperctl " + c.Name, "readOnlyHint": c.ReadOnly, "openWorldHint": false}
	if !c.ReadOnly {
		annotations["destructiveHint"] = c.Destructive
	}
	return map[string]any{"name": t.Name, "title": "hesperctl " + c.Name, "description": t.description(),
		"inputSchema": t.schema(), "annotations": annotations}
}

// argv is the command line for a call with these arguments: the
// command's name, --json, the flags, then the positional arguments.
func (t *mcpTool) argv(arguments map[string]json.RawMessage) ([]string, error) {
	argv := append(strings.Fields(t.Command.Name), "--json")
	byName := map[string]mcpParam{}
	for _, p := range t.Params {
		byName[p.Name] = p
	}
	for name := range arguments {
		if _, ok := byName[name]; !ok && !(t.Args && name == "args") {
			return nil, usagef("unknown parameter %q", name)
		}
	}
	var positional []string
	timeoutSet := false
	for _, p := range t.Params {
		raw, given := arguments[p.Name]
		if given && string(raw) == "null" {
			given = false
		}
		if !given {
			if p.Required {
				return nil, usagef("parameter %q is required", p.Name)
			}
			continue
		}
		values, err := mcpValues(p, raw)
		if err != nil {
			return nil, err
		}
		if !p.Flag {
			positional = append(positional, values...)
			continue
		}
		if p.Name == "timeout" && p.Kind == "duration" {
			timeoutSet = true
		}
		for _, v := range values {
			argv = append(argv, "--"+p.Name+"="+v)
		}
	}
	if t.Args {
		if raw, ok := arguments["args"]; ok && string(raw) != "null" {
			values, err := mcpValues(mcpParam{Name: "args", Kind: "list"}, raw)
			if err != nil {
				return nil, err
			}
			positional = append(positional, values...)
		}
	}
	if !timeoutSet && t.waits(arguments) {
		argv = append(argv, "--timeout="+mcpDuration(mcpMaxWait))
	}
	for _, v := range positional {
		if strings.HasPrefix(v, "-") {
			argv = append(argv, "--") // a value, not a flag
		}
		argv = append(argv, v)
	}
	return argv, nil
}

// waits tells whether a call waits with a timeout flag of its command: a
// command with --timeout and no --wait (wait), or with --wait set (new).
func (t *mcpTool) waits(arguments map[string]json.RawMessage) bool {
	var hasTimeout, hasWait bool
	for _, p := range t.Params {
		hasTimeout = hasTimeout || (p.Flag && p.Name == "timeout" && p.Kind == "duration")
		hasWait = hasWait || (p.Flag && p.Name == "wait" && p.Kind == "boolean")
	}
	if !hasTimeout {
		return false
	}
	if !hasWait {
		return true
	}
	var wait bool
	json.Unmarshal(arguments["wait"], &wait)
	return wait
}

// mcpValues converts a parameter's JSON value to command-line values.
func mcpValues(p mcpParam, raw json.RawMessage) ([]string, error) {
	bad := func() error { return usagef("parameter %q: want %s, got %s", p.Name, kindName(p.Kind), raw) }
	switch p.Kind {
	case "boolean":
		var v bool
		if json.Unmarshal(raw, &v) != nil {
			return nil, bad()
		}
		return []string{strconv.FormatBool(v)}, nil
	case "integer":
		var v int64
		if json.Unmarshal(raw, &v) != nil {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return nil, bad()
			}
			if _, err := strconv.ParseInt(s, 10, 64); err != nil {
				return nil, bad()
			}
			return []string{s}, nil
		}
		return []string{strconv.FormatInt(v, 10)}, nil
	case "number":
		var v float64
		if json.Unmarshal(raw, &v) != nil {
			return nil, bad()
		}
		return []string{strconv.FormatFloat(v, 'g', -1, 64)}, nil
	case "duration":
		var d time.Duration
		var s string
		var n float64
		switch {
		case json.Unmarshal(raw, &s) == nil:
			var err error
			if d, err = time.ParseDuration(strings.TrimSpace(s)); err != nil {
				return nil, bad()
			}
		case json.Unmarshal(raw, &n) == nil: // seconds
			d = time.Duration(n * float64(time.Second))
		default:
			return nil, bad()
		}
		if p.Name == "timeout" && (d <= 0 || d > mcpMaxWait) {
			d = mcpMaxWait
		}
		return []string{d.String()}, nil
	case "list":
		var list []string
		if json.Unmarshal(raw, &list) == nil {
			return list, nil
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return []string{s}, nil
		}
		return nil, bad()
	default:
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return []string{s}, nil
		}
		var n json.Number
		if json.Unmarshal(raw, &n) == nil {
			return []string{n.String()}, nil
		}
		return nil, bad()
	}
}

func kindName(kind string) string {
	switch kind {
	case "list":
		return "an array of strings"
	case "duration":
		return "a duration string"
	case "integer":
		return "an integer"
	}
	return "a " + kind
}

// --- the server -----------------------------------------------------------

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC error codes.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternal       = -32603
	mcpNoResource     = -32002
)

type mcpServer struct {
	exe string
	env []string // the calls' environment (nil: this process's)
	log io.Writer

	outMu sync.Mutex
	out   io.Writer

	tools  []*mcpTool
	byName map[string]*mcpTool

	mu       sync.Mutex
	inflight map[string]*mcpCall
	subs     map[string]bool
	watching bool
	calls    sync.WaitGroup
	ctx      context.Context // the server's (subscriptions)
}

type mcpCall struct {
	cancel    context.CancelFunc
	cancelled bool
}

func newMCPServer(out, log io.Writer, exe string, env []string) *mcpServer {
	s := &mcpServer{exe: exe, env: env, out: out, log: log, inflight: map[string]*mcpCall{}, subs: map[string]bool{},
		byName: map[string]*mcpTool{}}
	s.tools = mcpTools()
	for _, t := range s.tools {
		s.byName[t.Name] = t
	}
	return s
}

func (s *mcpServer) logf(format string, args ...any) {
	fmt.Fprintf(s.log, "hesperctl mcp: "+format+"\n", args...)
}

// serve reads requests until in ends (or ctx), then waits for the calls
// under way.
func (s *mcpServer) serve(ctx context.Context, in io.Reader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.ctx = ctx
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		r := bufio.NewReaderSize(in, 64*1024)
		for {
			line, err := r.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					readErr <- err
				}
				close(lines)
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			s.cancelAll()
			s.calls.Wait()
			return nil
		case err := <-readErr:
			s.logf("reading stdin: %v", err)
		case line, ok := <-lines:
			if !ok {
				s.calls.Wait()
				return nil
			}
			s.handle(ctx, line)
		}
	}
}

func (s *mcpServer) cancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.inflight {
		c.cancelled = true
		c.cancel()
	}
}

func (s *mcpServer) send(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		s.logf("encoding a message: %v", err)
		return
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.out.Write(append(data, '\n'))
}

func (s *mcpServer) reply(id json.RawMessage, result any) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *mcpServer) fail(id json.RawMessage, code int, message string, data any) {
	if id == nil {
		id = json.RawMessage("null")
	}
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": mcpError{Code: code, Message: message, Data: data}})
}

func (s *mcpServer) notify(method string, params any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *mcpServer) handle(ctx context.Context, line []byte) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		s.fail(nil, rpcInvalidRequest, "batches are not supported", nil)
		return
	}
	var req mcpRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		s.fail(nil, rpcParseError, "parse error: "+err.Error(), nil)
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		if req.Method == "" && req.ID != nil {
			return // a response (we send no requests)
		}
		s.fail(req.ID, rpcInvalidRequest, "invalid request", nil)
		return
	}
	if req.ID == nil { // a notification
		s.handleNotification(req)
		return
	}
	switch req.Method {
	case "initialize":
		s.initialize(req)
	case "ping":
		s.reply(req.ID, map[string]any{})
	case "tools/list":
		list := make([]any, 0, len(s.tools))
		for _, t := range s.tools {
			list = append(list, t.describe())
		}
		s.reply(req.ID, map[string]any{"tools": list})
	case "tools/call":
		s.async(ctx, req, s.callTool)
	case "resources/list":
		s.reply(req.ID, map[string]any{"resources": mcpResources})
	case "resources/templates/list":
		s.reply(req.ID, map[string]any{"resourceTemplates": []any{}})
	case "resources/read":
		s.async(ctx, req, s.readResource)
	case "resources/subscribe", "resources/unsubscribe":
		s.subscription(ctx, req)
	case "prompts/list":
		s.reply(req.ID, map[string]any{"prompts": []any{}})
	default:
		s.fail(req.ID, rpcMethodNotFound, "method not found: "+req.Method, nil)
	}
}

func (s *mcpServer) handleNotification(req mcpRequest) {
	switch req.Method {
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
			Reason    string          `json:"reason"`
		}
		json.Unmarshal(req.Params, &p)
		s.mu.Lock()
		if c := s.inflight[string(p.RequestID)]; c != nil {
			c.cancelled = true
			c.cancel()
			s.logf("cancelled %s %s", p.RequestID, p.Reason)
		}
		s.mu.Unlock()
	}
	// notifications/initialized and others need nothing.
}

func (s *mcpServer) initialize(req mcpRequest) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	json.Unmarshal(req.Params, &p)
	version := mcpVersions[0]
	if contains(mcpVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	s.logf("%s %s, protocol %s (asked %s), %d tools", p.ClientInfo.Name, p.ClientInfo.Version, version, p.ProtocolVersion, len(s.tools))
	s.reply(req.ID, map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools":     map[string]any{"listChanged": false},
			"resources": map[string]any{"subscribe": true, "listChanged": false},
		},
		"serverInfo":   map[string]any{"name": "hesper", "title": "Hesper", "version": buildVersion()},
		"instructions": mcpInstructions,
	})
}

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// async runs a request in its own goroutine, cancellable by
// notifications/cancelled (a cancelled request gets no response).
func (s *mcpServer) async(ctx context.Context, req mcpRequest, do func(context.Context, mcpRequest) (any, *mcpError)) {
	ctx, cancel := context.WithCancel(ctx)
	call := &mcpCall{cancel: cancel}
	key := string(req.ID)
	s.mu.Lock()
	s.inflight[key] = call
	s.mu.Unlock()
	s.calls.Add(1)
	go func() {
		defer s.calls.Done()
		defer cancel()
		result, rpcErr := do(ctx, req)
		s.mu.Lock()
		delete(s.inflight, key)
		cancelled := call.cancelled
		s.mu.Unlock()
		switch {
		case cancelled:
		case rpcErr != nil:
			s.fail(req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
		default:
			s.reply(req.ID, result)
		}
	}()
}

func textContent(text string) []any {
	return []any{map[string]any{"type": "text", "text": text}}
}

func (s *mcpServer) callTool(ctx context.Context, req mcpRequest) (any, *mcpError) {
	var p struct {
		Name      string                     `json:"name"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, &mcpError{Code: rpcInvalidParams, Message: "invalid params: " + err.Error()}
	}
	t := s.byName[p.Name]
	if t == nil {
		return nil, &mcpError{Code: rpcInvalidParams, Message: "unknown tool: " + p.Name}
	}
	argv, err := t.argv(p.Arguments)
	if err != nil {
		return toolError(err, exitCode(err)), nil
	}
	out, code, err := s.runCommand(ctx, argv)
	if err != nil {
		return toolError(err, exitCode(err)), nil
	}
	if code != 0 {
		return map[string]any{"content": textContent(errorJSON(out.stderr, code)), "isError": true}, nil
	}
	text := strings.TrimSpace(out.stdout)
	if text == "" {
		text = `{"ok":true}`
	}
	return map[string]any{"content": textContent(text)}, nil
}

// toolError is a tool result for an error found before the command ran.
func toolError(err error, code int) map[string]any {
	var b bytes.Buffer
	printError(&b, err, true)
	return map[string]any{"content": textContent(errorJSON(b.String(), code)), "isError": true}
}

// errorJSON is the command's --json error (the last line of stderr that
// is one) with exitCode added; other stderr becomes the message.
func errorJSON(stderr string, code int) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var v map[string]any
		if json.Unmarshal([]byte(lines[i]), &v) == nil && v["error"] != nil {
			v["exitCode"] = code
			data, _ := json.Marshal(v)
			return string(data)
		}
	}
	data, _ := json.Marshal(map[string]any{"error": map[string]string{"code": codeError, "message": strings.TrimSpace(stderr)}, "exitCode": code})
	return string(data)
}

type mcpOutput struct{ stdout, stderr string }

// limitedBuffer keeps the first n bytes written to it.
type limitedBuffer struct {
	bytes.Buffer
	n         int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.n - b.Len(); room < len(p) {
		b.truncated = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// runCommand runs hesperctl argv as a child (stdin empty); cancelling
// ctx interrupts it (SIGINT, then SIGKILL after 3 s).
func (s *mcpServer) runCommand(ctx context.Context, argv []string) (mcpOutput, int, error) {
	ctx, cancel := context.WithTimeout(ctx, mcpCallLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.exe, argv...)
	cmd.Env = s.env
	stdout := &limitedBuffer{n: mcpOutputLimit}
	stderr := &limitedBuffer{n: 64 * 1024}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 3 * time.Second
	err := cmd.Run()
	out := mcpOutput{stdout: stdout.String(), stderr: stderr.String()}
	if stdout.truncated {
		out.stdout += fmt.Sprintf("\n[output truncated at %d bytes]", mcpOutputLimit)
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return out, 0, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() > 0:
		return out, exitErr.ExitCode(), nil
	case ctx.Err() != nil:
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return out, 0, failf(codeTimeout, "hesperctl %s took longer than %s", strings.Join(argv, " "), mcpCallLimit)
		}
		return out, 0, ctx.Err()
	}
	return out, 0, fmt.Errorf("running hesperctl: %w", err)
}

// --- resources ------------------------------------------------------------

var mcpResources = []map[string]any{
	{"uri": "hesper://agents", "name": "agents", "title": "Agents", "mimeType": "application/json",
		"description": "Every agent of every machine (hesperctl ls --json): [Agent]"},
	{"uri": "hesper://needs-you", "name": "needs-you", "title": "Agents that need you", "mimeType": "application/json",
		"description": "The agents waiting for an answer: state approval or question, with their attention"},
	{"uri": "hesper://reference", "name": "reference", "title": "hesperctl reference", "mimeType": "text/markdown",
		"description": "The whole CLI as Markdown: concepts, agent object, exit codes, every command"},
}

func (s *mcpServer) readResource(ctx context.Context, req mcpRequest) (any, *mcpError) {
	var p struct {
		URI string `json:"uri"`
	}
	json.Unmarshal(req.Params, &p)
	content := func(mime, text string) any {
		return map[string]any{"contents": []any{map[string]any{"uri": p.URI, "mimeType": mime, "text": text}}}
	}
	switch p.URI {
	case "hesper://reference":
		var b bytes.Buffer
		writeReference(&b)
		return content("text/markdown", b.String()), nil
	case "hesper://agents", "hesper://needs-you":
		out, code, err := s.runCommand(ctx, []string{"ls", "--json"})
		if err != nil || code != 0 {
			msg := errorJSON(out.stderr, code)
			if err != nil {
				msg = err.Error()
			}
			return nil, &mcpError{Code: rpcInternal, Message: "hesperctl ls: " + msg}
		}
		if p.URI == "hesper://agents" {
			return content("application/json", strings.TrimSpace(out.stdout)), nil
		}
		var list []wire.Agent
		if err := json.Unmarshal([]byte(out.stdout), &list); err != nil {
			return nil, &mcpError{Code: rpcInternal, Message: "hesperctl ls: " + err.Error()}
		}
		waiting := []wire.Agent{}
		for _, a := range list {
			if a.State == wire.StateApproval || a.State == wire.StateQuestion {
				waiting = append(waiting, a)
			}
		}
		data, _ := json.MarshalIndent(waiting, "", "  ")
		return content("application/json", string(data)), nil
	}
	return nil, &mcpError{Code: mcpNoResource, Message: "resource not found", Data: map[string]string{"uri": p.URI}}
}

// subscription: resources/subscribe and unsubscribe. The first
// subscription to agents or needs-you subscribes to hesperd's agent
// changes; each one sends notifications/resources/updated for the
// subscribed resources (at most every 250 ms).
func (s *mcpServer) subscription(ctx context.Context, req mcpRequest) {
	var p struct {
		URI string `json:"uri"`
	}
	json.Unmarshal(req.Params, &p)
	if p.URI != "hesper://agents" && p.URI != "hesper://needs-you" {
		if p.URI == "hesper://reference" {
			s.reply(req.ID, map[string]any{}) // never changes while we run
			return
		}
		s.fail(req.ID, mcpNoResource, "resource not found", map[string]string{"uri": p.URI})
		return
	}
	s.mu.Lock()
	if req.Method == "resources/unsubscribe" {
		delete(s.subs, p.URI)
		s.mu.Unlock()
		s.reply(req.ID, map[string]any{})
		return
	}
	s.subs[p.URI] = true
	start := !s.watching
	s.watching = true
	s.mu.Unlock()
	if start {
		c, err := subscribe(ctx, s.socket())
		if err != nil {
			s.mu.Lock()
			s.watching = false
			delete(s.subs, p.URI)
			s.mu.Unlock()
			s.fail(req.ID, rpcInternal, "subscribing to hesperd: "+err.Error(), nil)
			return
		}
		go s.watch(ctx, c)
	}
	s.reply(req.ID, map[string]any{})
}

// socket is the hesperd socket of the calls' environment.
func (s *mcpServer) socket() string {
	for i := len(s.env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(s.env[i], "HESPER_SOCKET="); ok && v != "" {
			return v
		}
	}
	return wire.SocketPath()
}

func (s *mcpServer) watch(ctx context.Context, c *wire.Client) {
	defer c.Close()
	defer func() {
		s.mu.Lock()
		s.watching = false
		s.mu.Unlock()
	}()
	var timer <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-c.Notifications():
			if !ok {
				s.logf("hesperd closed the subscription")
				return
			}
			if strings.HasPrefix(n.Method, "agents.") && timer == nil {
				timer = time.After(250 * time.Millisecond)
			}
		case <-timer:
			timer = nil
			s.mu.Lock()
			var uris []string
			for uri := range s.subs {
				uris = append(uris, uri)
			}
			s.mu.Unlock()
			sort.Strings(uris)
			for _, uri := range uris {
				s.notify("notifications/resources/updated", map[string]string{"uri": uri})
			}
		}
	}
}
