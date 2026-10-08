package agents

import (
	"context"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Processes an agent started that would not move with it (agents.move,
// error "processes"): what runs under the shells its tool started for
// commands (Claude Code's Bash tool, Codex's exec): dev servers, watchers.
// The tool's other children (MCP servers and the like) belong to the tool
// and are not listed, nor are hesperd's own hook calls.

// shells are the command interpreters an agent runs commands in.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true, "ksh": true}

type proc struct {
	pid, ppid int
	command   string
}

// listProcs is every process (ps); a test seam.
var listProcs = func(env []string) ([]proc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,command=")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var list []proc
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		list = append(list, proc{pid: pid, ppid: ppid, command: strings.Join(f[2:], " ")})
	}
	return list, nil
}

// Processes lists the processes agent id started that run now (see
// above), oldest pid first.
func (r *Registry) Processes(id string) ([]wire.Process, error) {
	r.mu.Lock()
	a, err := r.find(id)
	pid, running := 0, false
	if err == nil {
		pid, running = a.PID, a.term != nil && a.Exit == nil
	}
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !running || pid <= 0 {
		return nil, nil
	}
	list, err := listProcs(r.env)
	if err != nil {
		return nil, err
	}
	return agentProcesses(list, pid, r.opt.HookBin), nil
}

// agentProcesses picks, below the agent's process root, what runs in the
// command shells its tool started.
func agentProcesses(list []proc, root int, hookBin string) []wire.Process {
	children := map[int][]proc{}
	for _, p := range list {
		children[p.ppid] = append(children[p.ppid], p)
	}
	isHook := func(p proc) bool {
		return strings.Contains(p.command, " hook") && (hookBin != "" && strings.Contains(p.command, hookBin) || strings.Contains(p.command, "hesperd hook"))
	}
	var out []wire.Process
	var walk func(p proc)
	walk = func(p proc) {
		if isHook(p) {
			return
		}
		kids := children[p.pid]
		if len(kids) == 0 {
			out = append(out, wire.Process{PID: p.pid, Command: p.command})
			return
		}
		for _, k := range kids {
			walk(k)
		}
	}
	// The tool may run under a shell (a profile's login shell): then the
	// tool is that shell's one child.
	tool := root
	for i := 0; i < 4 && shells[commandName(procCommand(list, tool))] && len(children[tool]) == 1; i++ {
		tool = children[tool][0].pid
	}
	for _, k := range children[tool] {
		if shells[commandName(k.command)] && !isHook(k) {
			walk(k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

func procCommand(list []proc, pid int) string {
	for _, p := range list {
		if p.pid == pid {
			return p.command
		}
	}
	return ""
}

// commandName is a command line's program name ("-zsh" → "zsh").
func commandName(command string) string {
	name, _, _ := strings.Cut(command, " ")
	return strings.TrimPrefix(filepath.Base(name), "-")
}
