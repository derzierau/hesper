package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Agent mirrors the contract's agent model.
type Agent struct {
	ID         string         `json:"id"`
	Machine    string         `json:"machine"`
	Kind       string         `json:"kind"`
	Profile    string         `json:"profile"`
	Name       string         `json:"name"`
	Task       string         `json:"task"`
	Project    string         `json:"project"`
	ProjectID  string         `json:"projectId,omitempty"`
	Worktree   string         `json:"worktree,omitempty"`
	Branch     string         `json:"branch,omitempty"`
	State      string         `json:"state"`
	StateSince string         `json:"stateSince"`
	Attention  *Attention     `json:"attention,omitempty"`
	Summary    string         `json:"summary,omitempty"`
	Activity   string         `json:"activity,omitempty"`
	SessionID  string         `json:"sessionId,omitempty"`
	Size       map[string]int `json:"size"`
	Created    string         `json:"created"`
	PID        int            `json:"pid"`
	Exit       *ExitInfo      `json:"exit"`
}

// ExitInfo is how part D reports an ended process.
type ExitInfo struct {
	Code   *int   `json:"code"`
	Signal string `json:"signal,omitempty"`
}

type Attention struct {
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Detail  string   `json:"detail"`
	Options []string `json:"options,omitempty"`
}

type viewer struct {
	conn  net.Conn
	out   chan frame
	rw    bool
	owner bool
	done  chan struct{}
	view  *viewFilter // a view attach (view.go)
	fit   [2]int      // a view's wished PTY size (cols, rows); 0: none
}

type frame struct {
	typ byte
	p   []byte
}

type agentProc struct {
	a        *Agent
	master   *os.File
	tty      *os.File // kept open for TIOCSWINSZ
	cmd      *exec.Cmd
	viewers  map[*viewer]bool
	owner    *viewer
	stopping bool
}

type daemon struct {
	mu      sync.Mutex
	agents  map[string]*agentProc
	order   []string
	subs    map[chan []byte]bool
	self    string
	tuiMode string
	fps     int
	tuiCols int
	tuiRows int
	sock    string
	drafts  map[string]Draft
	// projects.go
	projects map[string]*Project
	groups   map[string]*Group
	// sessions.go (shared history)
	sess *sessionStore
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	sock := fs.String("socket", "", "socket path")
	n := fs.Int("agents", 0, "seed this many agents")
	mode := fs.String("tui", "agent", "fake TUI mode for agents: agent | flood")
	fps := fs.Int("fps", 120, "flood fps")
	scenario := fs.Bool("scenario", false, "cycle agent states for demos")
	demo := fs.Bool("demo-states", false, "seeded agents start in a mix of states (screenshots)")
	cols := fs.Int("cols", 120, "initial PTY cols")
	rows := fs.Int("rows", 40, "initial PTY rows")
	sizes := fs.String("sizes", "", "seeded agents' PTY sizes, cycled: 120x40,200x60,...")
	projectsSeed := fs.String("projects", "", "seed projects and groups: demo (seeded agents spread over them)")
	nSessions := fs.Int("sessions", 40, "seed this many history sessions (sessions.*)")
	noSessions := fs.Bool("no-sessions", false, "answer sessions.* with -32601 (an older hesperd)")
	indexingMs := fs.Int("indexing-ms", 0, "imitate the first index for this long (sessions.indexing)")
	_ = fs.Parse(args)
	var seedSizes [][2]int
	for _, s := range strings.Split(*sizes, ",") {
		var c, r int
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%dx%d", &c, &r); err == nil && c > 0 && r > 0 {
			seedSizes = append(seedSizes, [2]int{c, r})
		}
	}

	path, err := socketPath(*sock)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	defer os.Remove(path)

	d := &daemon{agents: map[string]*agentProc{}, subs: map[chan []byte]bool{}, self: "L", tuiMode: *mode, fps: *fps, tuiCols: *cols, tuiRows: *rows, sock: path,
		projects: map[string]*Project{}, groups: map[string]*Group{}}
	d.loadDrafts()
	d.sess = &sessionStore{byID: map[string]*Session{}, disabled: *noSessions}
	demoProjects := *projectsSeed == "demo"
	if demoProjects {
		d.seedDemoProjects()
	}
	names := []string{"push-provider-fcm", "relay latency", "edition picker", "draft PR copy", "flaky badges test", "ios widgets", "auth renewal", "vt screen copy"}
	for i := 0; i < *n; i++ {
		kind := "claude"
		if i%3 == 1 {
			kind = "codex"
		}
		name := names[i%len(names)]
		if i >= len(names) {
			name = fmt.Sprintf("%s %d", name, i/len(names)+1)
		}
		sp := spawnParams{Kind: kind, Project: "/tmp/fake/project", Task: name + "\n(fake seeded agent)", Name: name}
		if demoProjects {
			sp.Machine, sp.Project, sp.Branch = demoSeat(i)
		}
		if len(seedSizes) > 0 {
			sz := seedSizes[i%len(seedSizes)]
			sp.cols, sp.rows = sz[0], sz[1]
		}
		if _, err := d.spawn(sp); err != nil {
			return err
		}
	}
	if !*noSessions {
		d.seedSessions(*nSessions, demoProjects)
		if *indexingMs > 0 {
			d.sess.indexing = [2]int{0, *nSessions}
			go d.runIndexing(*nSessions, time.Duration(*indexingMs)*time.Millisecond)
		}
	}
	if *demo {
		go func() { time.Sleep(time.Second); d.demoStates() }()
	}
	if *scenario {
		go d.scenario()
	}
	fmt.Fprintf(os.Stderr, "fake-hesperd: listening on %s with %d agents\n", path, *n)
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go d.handle(c)
	}
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func newID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = alphabet[rand.Intn(len(alphabet))]
	}
	return string(b)
}

type spawnParams struct {
	Machine  string `json:"machine"`
	Profile  string `json:"profile"`
	Kind     string `json:"kind"`
	Project  string `json:"project"`
	Task     string `json:"task"`
	Name     string `json:"name"`
	Worktree any    `json:"worktree"`
	Branch   string `json:"branch"`
	// Seeded agents only: the PTY's size (else --cols x --rows).
	cols, rows int
	// sessions.resume: the session the agent continues (sessions.go).
	sessionID string
}

var profiles = map[string]map[string]any{
	"claude":            {"kind": "claude", "argv": []string{"claude"}},
	"claude-auto-rc":    {"kind": "claude", "argv": []string{"claude", "--permission-mode", "auto", "--remote-control", "{name}"}},
	"claude-unattended": {"kind": "claude", "argv": []string{"claude", "--dangerously-skip-permissions", "--remote-control", "{name}"}},
	"codex":             {"kind": "codex", "argv": []string{"codex"}},
	"codex-unattended":  {"kind": "codex", "argv": []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}},
	"shell":             {"kind": "shell", "argv": []string{"{loginShell}", "-l"}},
}
var defaultProfile = map[string]string{"claude": "claude", "codex": "codex", "shell": "shell"}

// miniLacks: the fake mini has every folder but those named "laptop-only".
func miniLacks(path string) bool { return strings.Contains(path, "laptop-only") }

func (d *daemon) spawn(p spawnParams) (*Agent, error) {
	if strings.TrimSpace(p.Project) == "" {
		return nil, errCode("invalid", "project is required")
	}
	machine := d.self
	if p.Machine == "M" {
		machine = "M" // the fake "mini": online, its agents run here too
		if miniLacks(p.Project) {
			return nil, errCode("not_found", "no directory "+p.Project) // as hesperd says it
		}
	} else if p.Machine != "" && p.Machine != d.self {
		return nil, errCode("unavailable", fmt.Sprintf("machine %s is offline", p.Machine))
	}
	if p.Profile != "" {
		prof, ok := profiles[p.Profile]
		if !ok {
			return nil, errCode("invalid", "unknown profile "+p.Profile)
		}
		p.Kind = prof["kind"].(string)
	}
	if p.Kind == "" {
		p.Kind = "claude"
	}
	if p.Profile == "" {
		p.Profile = defaultProfile[p.Kind]
	}
	name := p.Name
	if name == "" {
		name = strings.TrimSpace(strings.SplitN(p.Task, "\n", 2)[0])
		if len(name) > 40 {
			name = name[:40]
		}
		if name == "" {
			name = p.Kind
		}
	}
	a := &Agent{
		ID: machine + "/" + newID(), Machine: machine, Kind: p.Kind, Profile: p.Profile, Name: name, Task: p.Task,
		Project: p.Project, Branch: p.Branch, State: "starting", StateSince: now(), Created: now(),
		Size: map[string]int{"cols": d.tuiCols, "rows": d.tuiRows}, SessionID: "fake-" + newID(),
	}
	if p.sessionID != "" {
		a.SessionID = p.sessionID
	}
	if p.cols > 0 && p.rows > 0 {
		a.Size = map[string]int{"cols": p.cols, "rows": p.rows}
	}
	d.mu.Lock()
	a.ProjectID = d.projectFor(machine, p.Project)
	d.mu.Unlock()
	if wt, ok := p.Worktree.(string); ok && wt != "" {
		a.Worktree = wt
	} else if b, ok := p.Worktree.(bool); ok && b {
		a.Worktree = filepath.Join("/tmp/fake/worktrees", strings.ReplaceAll(name, " ", "-"))
	}
	ap := &agentProc{a: a, viewers: map[*viewer]bool{}}
	if err := d.start(ap); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.agents[a.ID] = ap
	d.order = append(d.order, a.ID)
	d.mu.Unlock()
	d.changed(ap)
	d.agentAddedSession(a) // shared history (sessions.go)
	go func() {
		time.Sleep(600 * time.Millisecond)
		d.setState(a.ID, "working", nil, "")
	}()
	return a, nil
}

func (d *daemon) start(ap *agentProc) error {
	master, slave, err := openPTY()
	if err != nil {
		return err
	}
	tty, err := os.OpenFile(slave, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return err
	}
	// macOS applies the window size through the slave side.
	_ = setWinsize(int(tty.Fd()), ap.a.Size["cols"], ap.a.Size["rows"])
	self, _ := os.Executable()
	cmd := exec.Command(self, "tui", "--mode", d.tuiMode, "--name", ap.a.Name, "--fps", fmt.Sprint(d.fps))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "HESPER_AGENT_ID="+ap.a.ID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		tty.Close()
		master.Close()
		return err
	}
	if ap.tty != nil {
		ap.tty.Close()
	}
	ap.master, ap.cmd, ap.tty = master, cmd, tty
	ap.a.PID = cmd.Process.Pid
	ap.a.Exit = nil
	go d.pump(ap, master, cmd)
	return nil
}

func (d *daemon) pump(ap *agentProc, master *os.File, cmd *exec.Cmd) {
	buf := make([]byte, 64<<10)
	for {
		n, err := master.Read(buf)
		if n > 0 {
			p := make([]byte, n)
			copy(p, buf[:n])
			d.mu.Lock()
			for v := range ap.viewers {
				q := p
				if v.view != nil {
					if q = v.view.filter(p, ap.a.Size["rows"]); len(q) == 0 {
						continue
					}
				}
				select {
				case v.out <- frame{frameData, q}:
				default: // slow viewer: drop it, it will reattach
					delete(ap.viewers, v)
					close(v.done)
				}
			}
			d.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	_ = cmd.Wait()
	code := cmd.ProcessState.ExitCode()
	ex := &ExitInfo{}
	if ap.stopping {
		ex.Signal = "SIGHUP"
	} else if code >= 0 {
		ex.Code = &code
	} else {
		ex.Signal = "SIGKILL"
	}
	d.mu.Lock()
	if ap.cmd != cmd { // replaced by resume
		d.mu.Unlock()
		return
	}
	payload, _ := json.Marshal(ex)
	for v := range ap.viewers {
		select {
		case v.out <- frame{frameExit, payload}:
		default:
		}
	}
	ap.a.Exit = ex
	ap.a.PID = 0
	ap.a.StateSince = now()
	ap.a.Attention = nil
	// Part D: ended by stop or with 0 → exited; on its own otherwise → error.
	if ap.stopping || code == 0 {
		ap.a.State = "exited"
	} else {
		ap.a.State = "error"
		ap.a.Attention = &Attention{Kind: "error", Title: "Exited", Detail: fmt.Sprintf("exit status %d", code)}
	}
	ap.stopping = false
	d.mu.Unlock()
	d.changed(ap)
}

func (d *daemon) setState(id, state string, att *Attention, summary string) error {
	d.mu.Lock()
	ap, ok := d.agents[id]
	if !ok {
		d.mu.Unlock()
		return errCode("not_found", "no agent "+id)
	}
	if ap.a.Exit != nil && state != "exited" {
		d.mu.Unlock()
		return nil
	}
	ap.a.State = state
	ap.a.StateSince = now()
	ap.a.Attention = att
	if summary != "" {
		ap.a.Summary = summary
	}
	d.mu.Unlock()
	d.changed(ap)
	return nil
}

func (d *daemon) changed(ap *agentProc) {
	d.mu.Lock()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "agents.changed", "params": map[string]any{"agent": ap.a}})
	for s := range d.subs {
		select {
		case s <- b:
		default:
		}
	}
	d.mu.Unlock()
}

func (d *daemon) removed(id string) {
	d.mu.Lock()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "agents.removed", "params": map[string]any{"id": id}})
	for s := range d.subs {
		select {
		case s <- b:
		default:
		}
	}
	d.mu.Unlock()
}

// demoStates gives seeded agents a mix of states (screenshots, layout
// checks): by position i%8, 1 approval, 2 done, 4 question, 6 idle, 7 error;
// the rest keep working.
func (d *daemon) demoStates() {
	d.mu.Lock()
	ids := append([]string(nil), d.order...)
	d.mu.Unlock()
	for i, id := range ids {
		switch i % 8 {
		case 0, 3:
			d.mu.Lock()
			if ap := d.agents[id]; ap != nil {
				ap.a.Activity = "Bash: ./gradlew :push:test"
			}
			d.mu.Unlock()
		case 1:
			d.setState(id, "approval", &Attention{Kind: "approval", Title: "Bash", Detail: "git push origin feature/push-provider-fcm", Options: []string{"allow", "always", "deny"}}, "")
		case 2:
			d.setState(id, "done", nil, "Opened PR #482 (draft)")
		case 4:
			d.setState(id, "question", &Attention{Kind: "question", Title: "Question", Detail: "Should the edition picker stay on the home screen or move into settings?"}, "")
		case 5:
			if i >= 8 {
				d.setState(id, "question", &Attention{Kind: "question", Title: "Trust folder", Detail: "Trust ~/projects/acme-apps?", Options: []string{"trust", "exit"}}, "")
			}
		case 6:
			d.setState(id, "idle", nil, "Waiting for review on #479")
		case 7:
			d.setState(id, "error", &Attention{Kind: "error", Title: "Agent error", Detail: "API rate limit reached, retry in 60 s"}, "")
		}
	}
}

func (d *daemon) scenario() {
	approvals := []Attention{
		{Kind: "approval", Title: "Bash", Detail: "git push origin feature/push-provider-fcm", Options: []string{"allow", "always", "deny"}},
		{Kind: "approval", Title: "Edit", Detail: "relay/internal/vt/screen.go", Options: []string{"allow", "always", "deny"}},
	}
	for {
		time.Sleep(time.Duration(4+rand.Intn(5)) * time.Second)
		d.mu.Lock()
		ids := append([]string(nil), d.order...)
		d.mu.Unlock()
		if len(ids) == 0 {
			continue
		}
		id := ids[rand.Intn(len(ids))]
		switch rand.Intn(5) {
		case 0:
			a := approvals[rand.Intn(len(approvals))]
			d.setState(id, "approval", &a, "")
		case 1:
			d.setState(id, "question", &Attention{Kind: "question", Title: "Question", Detail: "Should the edition picker stay on the home screen or move into settings?"}, "")
		case 2:
			d.setState(id, "done", nil, "Opened PR #482 (draft)")
		default:
			d.setState(id, "working", nil, "")
		}
	}
}

func (d *daemon) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReaderSize(c, 1<<16)
	first, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(first, &probe) != nil {
		return
	}
	if _, ok := probe["attach"]; ok {
		d.attach(c, br, first)
		return
	}
	d.control(c, br, first)
}

func (d *daemon) control(c net.Conn, br *bufio.Reader, first []byte) {
	out := make(chan []byte, 1024)
	done := make(chan struct{})
	go func() {
		w := bufio.NewWriter(c)
		for {
			select {
			case b := <-out:
				w.Write(b)
				w.WriteByte('\n')
				for len(out) > 0 {
					w.Write(<-out)
					w.WriteByte('\n')
				}
				if w.Flush() != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
	defer close(done)
	var sub chan []byte
	defer func() {
		if sub != nil {
			d.mu.Lock()
			delete(d.subs, sub)
			d.mu.Unlock()
		}
	}()
	line := first
	for {
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err == nil {
			res, rerr := d.call(req)
			if len(req.ID) > 0 {
				msg := map[string]any{"jsonrpc": "2.0", "id": req.ID}
				if rerr != nil {
					msg["error"] = rerr
				} else {
					msg["result"] = res
				}
				b, _ := json.Marshal(msg)
				out <- b
			}
			if req.Method == "agents.subscribe" && rerr == nil && sub == nil {
				sub = make(chan []byte, 4096)
				d.mu.Lock()
				snapshot := d.projectSnapshot()
				for _, id := range d.order {
					b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "agents.changed", "params": map[string]any{"agent": d.agents[id].a}})
					snapshot = append(snapshot, b)
				}
				for _, x := range d.draftList() {
					b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "drafts.changed", "params": map[string]any{"draft": x}})
					snapshot = append(snapshot, b)
				}
				d.subs[sub] = true
				d.mu.Unlock()
				for _, b := range snapshot {
					out <- b
				}
				go func(s chan []byte) {
					for {
						select {
						case b := <-s:
							out <- b
						case <-done:
							return
						}
					}
				}(sub)
			}
		}
		var err error
		line, err = br.ReadBytes('\n')
		if err != nil {
			return
		}
	}
}

func (d *daemon) call(req rpcRequest) (any, *rpcError) {
	var p map[string]any
	_ = json.Unmarshal(req.Params, &p)
	str := func(k string) string { s, _ := p[k].(string); return s }
	get := func() (*agentProc, *rpcError) {
		d.mu.Lock()
		defer d.mu.Unlock()
		ap, ok := d.agents[str("id")]
		if !ok {
			return nil, errCode("not_found", "no agent "+str("id"))
		}
		return ap, nil
	}
	switch req.Method {
	case "hello":
		return map[string]any{"daemon": "fake-hesperd", "version": "0.0.0-fake", "machine": d.self, "machines": []map[string]any{
			{"short": "L", "name": "laptop", "online": true, "rttMs": 0, "route": "local"},
			{"short": "M", "name": "mini", "online": true, "rttMs": 44, "route": "relay"},
			{"short": "S", "name": "studio", "online": false, "rttMs": nil, "route": ""},
		}}, nil
	case "agents.list":
		d.mu.Lock()
		defer d.mu.Unlock()
		list := []*Agent{}
		for _, id := range d.order {
			list = append(list, d.agents[id].a)
		}
		return list, nil
	case "agents.subscribe":
		return map[string]any{}, nil
	case "agents.spawn":
		var sp spawnParams
		_ = json.Unmarshal(req.Params, &sp)
		a, err := d.spawn(sp)
		if err != nil {
			if re, ok := err.(*rpcError); ok {
				return nil, re
			}
			return nil, errCode("invalid", err.Error())
		}
		return a, nil
	case "agents.input":
		ap, e := get()
		if e != nil {
			return nil, e
		}
		if ap.master != nil {
			text := str("text")
			if b, _ := p["paste"].(bool); b {
				text = "\x1b[200~" + text + "\x1b[201~"
			}
			if b, _ := p["submit"].(bool); b {
				text += "\r"
			}
			ap.master.Write([]byte(text))
		}
		return map[string]any{}, nil
	case "agents.answer":
		ap, e := get()
		if e != nil {
			return nil, e
		}
		if dec := str("decision"); dec == "trust" || dec == "exit" || dec == "skip" || dec == "update" {
			// The trust question of a first run (real hesperd: firstrun.go).
			if ap.a.State != "question" || ap.a.Attention == nil || len(ap.a.Attention.Options) == 0 {
				return nil, errCode("invalid", "the agent is not asking to trust its folder")
			}
			if dec == "exit" && ap.cmd != nil && ap.cmd.Process != nil {
				d.mu.Lock()
				ap.stopping = true
				d.mu.Unlock()
				_ = ap.cmd.Process.Signal(syscall.SIGHUP)
				return map[string]any{}, nil
			}
			d.setState(ap.a.ID, "working", nil, "")
			return map[string]any{}, nil
		}
		if ap.a.State != "approval" {
			return nil, errCode("invalid", "the agent is not waiting for an approval")
		}
		keys := map[string]string{"allow": "1", "always": "2", "deny": "\x1b"}
		if ap.a.Kind == "codex" {
			keys = map[string]string{"allow": "y", "always": "a", "deny": "\x1b"}
		}
		k, ok := keys[str("decision")]
		if !ok {
			return nil, errCode("invalid", "decision must be allow, always or deny")
		}
		ap.master.Write([]byte(k))
		next := "working"
		if str("decision") == "deny" && str("message") == "" {
			next = "idle"
		}
		d.setState(ap.a.ID, next, nil, "")
		return map[string]any{}, nil
	case "agents.stop":
		ap, e := get()
		if e != nil {
			return nil, e
		}
		if ap.cmd != nil && ap.cmd.Process != nil {
			d.mu.Lock()
			ap.stopping = true
			d.mu.Unlock()
			_ = ap.cmd.Process.Signal(syscall.SIGHUP)
		}
		return map[string]any{}, nil
	case "agents.resume":
		ap, e := get()
		if e != nil {
			return nil, e
		}
		if ap.a.Exit == nil {
			return nil, errCode("invalid", "agent is still running")
		}
		d.mu.Lock()
		ap.a.State, ap.a.StateSince, ap.a.Exit = "starting", now(), nil
		err := d.start(ap)
		d.mu.Unlock()
		if err != nil {
			return nil, errCode("invalid", err.Error())
		}
		d.changed(ap)
		go func() { time.Sleep(500 * time.Millisecond); d.setState(ap.a.ID, "working", nil, "") }()
		return ap.a, nil
	case "agents.remove":
		ap, e := get()
		if e != nil {
			return nil, e
		}
		if ap.a.Exit == nil {
			return nil, errCode("invalid", "stop the agent first")
		}
		d.mu.Lock()
		delete(d.agents, ap.a.ID)
		for i, id := range d.order {
			if id == ap.a.ID {
				d.order = append(d.order[:i], d.order[i+1:]...)
				break
			}
		}
		d.mu.Unlock()
		d.removed(ap.a.ID)
		d.agentRemovedSession(ap.a) // shared history: a ghost card (sessions.go)
		return map[string]any{}, nil
	case "agents.rename":
		ap, e := get()
		if e != nil {
			return nil, e
		}
		if strings.TrimSpace(str("name")) == "" {
			return nil, errCode("invalid", "name is empty")
		}
		d.mu.Lock()
		ap.a.Name = str("name")
		d.mu.Unlock()
		d.changed(ap)
		return ap.a, nil
	case "agents.move":
		ap, e := get()
		if e != nil {
			return nil, e
		}
		to := str("to")
		if to != "L" && to != "M" {
			return nil, errCode("unavailable", "machine "+to+" is offline")
		}
		if to == ap.a.Machine {
			return nil, errCode("invalid", "the agent is already on "+to)
		}
		// A move takes a moment (stop, pack, resume on the target); the
		// fake keeps the process and only renames the agent.
		time.Sleep(1200 * time.Millisecond)
		d.mu.Lock()
		old := ap.a.ID
		local := old[strings.Index(old, "/")+1:]
		moved := *ap.a
		moved.ID, moved.Machine = to+"/"+local, to
		ap.a = &moved
		delete(d.agents, old)
		d.agents[moved.ID] = ap
		for i, id := range d.order {
			if id == old {
				d.order[i] = moved.ID
			}
		}
		d.mu.Unlock()
		d.removed(old)
		d.changed(ap)
		return ap.a, nil
	case "projects.recent":
		home, _ := os.UserHomeDir()
		return []map[string]any{
			{"path": filepath.Join(home, "projects/acme-apps"), "name": "acme-apps", "lastUsed": now()},
			{"path": filepath.Join(home, "projects/hesper"), "name": "hesper", "lastUsed": now()},
		}, nil
	case "fs.stat":
		// This machine: the file system. The fake mini has every folder
		// but those named "laptop-only" (the composer's "This folder is on
		// laptop, not on mini").
		path := str("path")
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errCode("invalid", "path must be a clean absolute path")
		}
		switch m := str("machine"); {
		case m == "M":
			there := !miniLacks(path)
			return map[string]any{"exists": there, "isDir": there}, nil
		case m != "" && m != d.self:
			return nil, errCode("unavailable", "machine "+m+" is not connected")
		}
		st, err := os.Stat(path)
		return map[string]any{"exists": err == nil, "isDir": err == nil && st.IsDir()}, nil
	case "profiles.list":
		return map[string]any{"profiles": profiles, "defaults": map[string]any{"kind": "claude", "kinds": defaultProfile, "projects": map[string]string{}}}, nil
	case "hook":
		return map[string]any{}, nil
	case "drafts.list", "drafts.save", "drafts.remove":
		return d.draftCall(req.Method, req.Params)
	case "files.put", "files.chunk":
		return d.filesCall(req.Method, req.Params)
	case "sessions.search", "sessions.show", "sessions.stats", "sessions.resume", "sessions.fork", "sessions.brief", "sessions.continueAs",
		"sessions.archive", "sessions.delete":
		return d.sessionsCall(req.Method, req.Params)
	case "fake.sessionCalls": // test-only: the sessions.* calls so far (clear: true empties the log)
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.sess == nil {
			return []any{}, nil
		}
		calls := append([]map[string]any{}, d.sess.calls...)
		if c, _ := p["clear"].(bool); c {
			d.sess.calls = nil
		}
		return calls, nil
	case "fake.sessionsFlood": // test-only: bursts of sessions.changed / sessions.indexing
		secs, _ := p["seconds"].(float64)
		rate, _ := p["rate"].(float64)
		go d.sessionsFlood(secs, int(rate))
		return map[string]any{}, nil
	case "projects.list", "groups.list", "projects.update", "projects.promote", "projects.remove", "groups.save", "groups.remove":
		return d.projectCall(req.Method, req.Params)
	// ---- test-only, outside the contract ----
	case "fake.setState":
		var att *Attention
		if raw, ok := p["attention"]; ok && raw != nil {
			b, _ := json.Marshal(raw)
			att = &Attention{}
			_ = json.Unmarshal(b, att)
		}
		if e := d.setState(str("id"), str("state"), att, str("summary")); e != nil {
			return nil, e.(*rpcError)
		}
		if act, ok := p["activity"]; ok {
			d.mu.Lock()
			if ap := d.agents[str("id")]; ap != nil {
				ap.a.Activity, _ = act.(string)
			}
			ap := d.agents[str("id")]
			d.mu.Unlock()
			if ap != nil {
				d.changed(ap)
			}
		}
		return map[string]any{}, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method, Data: map[string]any{"code": "not_found"}}
}

func (d *daemon) attach(c net.Conn, br *bufio.Reader, first []byte) {
	var req struct {
		Attach string `json:"attach"`
		Mode   string `json:"mode"`
		Cols   int    `json:"cols"`
		Rows   int    `json:"rows"`
		Owner  bool   `json:"owner"`
		View   *struct {
			Rows   int    `json:"rows"`
			Cols   int    `json:"cols"`
			Anchor string `json:"anchor"`
		} `json:"view"`
		Fit *struct {
			Cols int `json:"cols"`
			Rows int `json:"rows"`
		} `json:"fit"`
	}
	_ = json.Unmarshal(first, &req)
	if req.View != nil && req.Mode != "ro" {
		b, _ := json.Marshal(map[string]any{"ok": false, "error": map[string]any{"code": "invalid", "message": "a view attach must be read-only"}})
		c.Write(append(b, '\n'))
		return
	}
	d.mu.Lock()
	ap, ok := d.agents[req.Attach]
	if !ok || ap.a.Exit != nil {
		d.mu.Unlock()
		code, msg := "not_found", "no agent "+req.Attach
		if ok {
			code, msg = "invalid", "agent has exited"
		}
		b, _ := json.Marshal(map[string]any{"ok": false, "error": map[string]any{"code": code, "message": msg}})
		c.Write(append(b, '\n'))
		return
	}
	v := &viewer{conn: c, out: make(chan frame, 4096), rw: req.Mode == "rw", owner: req.Owner && req.Mode == "rw", done: make(chan struct{})}
	if req.View != nil {
		vf := &viewFilter{rows: req.View.Rows, cols: req.View.Cols}
		if vf.rows <= 0 {
			vf.rows = req.Rows
		}
		if vf.cols <= 0 {
			vf.cols = req.Cols
		}
		vf.rows = max(vf.rows, 1)
		v.view = vf
		if req.Fit != nil {
			v.fit = [2]int{req.Fit.Cols, req.Fit.Rows}
		}
	}
	resized := false
	if v.owner {
		ap.owner = v
		if req.Cols > 0 && req.Rows > 0 && (req.Cols != ap.a.Size["cols"] || req.Rows != ap.a.Size["rows"]) {
			ap.a.Size = map[string]int{"cols": req.Cols, "rows": req.Rows}
			ap.resize(req.Cols, req.Rows)
			resized = true
		}
	}
	cols, rows := ap.a.Size["cols"], ap.a.Size["rows"]
	ap.viewers[v] = true
	if resized {
		for o := range ap.viewers {
			if o != v && o.view == nil {
				select {
				case o.out <- frame{frameSize, sizePayload(cols, rows)}:
				default:
				}
			}
		}
	}
	pid := ap.a.PID
	d.mu.Unlock()
	if resized {
		d.changed(ap)
	}
	if v.fit[0] > 0 {
		go d.applyFitLater(ap)
	}

	rep := map[string]any{"ok": true, "cols": cols, "rows": rows}
	if v.view != nil {
		rep["view"] = map[string]any{"rows": v.view.rows, "cols": v.view.cols, "anchor": "bottom"}
	}
	b, _ := json.Marshal(rep)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return
	}
	// The fake's "screen copy": clear, then make the TUI redraw everything.
	if v.view != nil {
		_ = writeFrame(c, frameData, []byte("\x1b[?1049h\x1b[?7l\x1b[H\x1b[2J"))
	} else {
		_ = writeFrame(c, frameData, []byte("\x1b[?1049h\x1b[H\x1b[2J"))
	}
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGWINCH)
	}

	go func() {
		defer c.Close()
		for {
			select {
			case f := <-v.out:
				if writeFrame(c, f.typ, f.p) != nil {
					return
				}
				if f.typ == frameExit {
					return
				}
			case <-v.done:
				return
			}
		}
	}()
	for {
		typ, p, err := readFrame(br)
		if err != nil {
			break
		}
		switch typ {
		case frameData:
			if v.rw && ap.master != nil {
				ap.master.Write(p)
			}
		case frameResize:
			c2, r2, ok := parseSize(p)
			d.mu.Lock()
			if v.view != nil {
				// A view's own window size: clear and let the TUI redraw.
				if ok && r2 > 0 {
					v.view.rows, v.view.cols = r2, c2
					if v.fit[0] > 0 {
						v.fit = [2]int{c2, r2}
						go d.applyFitLater(ap)
					}
					select {
					case v.out <- frame{frameData, []byte("\x1b[H\x1b[2J")}:
					default:
					}
					if ap.cmd != nil && ap.cmd.Process != nil {
						_ = ap.cmd.Process.Signal(syscall.SIGWINCH)
					}
				}
				d.mu.Unlock()
				continue
			}
			isOwner := ap.owner == v
			if ok && isOwner && c2 > 0 && r2 > 0 && (c2 != ap.a.Size["cols"] || r2 != ap.a.Size["rows"]) {
				ap.a.Size = map[string]int{"cols": c2, "rows": r2}
				ap.resize(c2, r2)
				for o := range ap.viewers {
					if o.view != nil {
						continue
					}
					select {
					case o.out <- frame{frameSize, sizePayload(c2, r2)}:
					default:
					}
				}
				d.mu.Unlock()
				d.changed(ap)
				continue
			}
			d.mu.Unlock()
		}
	}
	d.mu.Lock()
	if ap.viewers[v] {
		delete(ap.viewers, v)
		close(v.done)
	}
	wasOwner := ap.owner == v
	if wasOwner {
		ap.owner = nil
	}
	d.mu.Unlock()
	if wasOwner || v.fit[0] > 0 {
		go d.applyFitLater(ap)
	}
}

// applyFitLater imitates hesperd's fit rule: with no owner, the PTY takes
// the largest fit of the view attaches (≥ 80x24), 300 ms after a change,
// when a side changes by ≥ 2.
func (d *daemon) applyFitLater(ap *agentProc) {
	time.Sleep(300 * time.Millisecond)
	d.mu.Lock()
	if ap.owner != nil || ap.a.Exit != nil {
		d.mu.Unlock()
		return
	}
	cols, rows := 0, 0
	for v := range ap.viewers {
		cols, rows = max(cols, v.fit[0]), max(rows, v.fit[1])
	}
	if cols == 0 {
		d.mu.Unlock()
		return
	}
	cols, rows = min(max(cols, 80), 1000), min(max(rows, 24), 1000)
	dc, dr := cols-ap.a.Size["cols"], rows-ap.a.Size["rows"]
	if dc > -2 && dc < 2 && dr > -2 && dr < 2 {
		d.mu.Unlock()
		return
	}
	ap.a.Size = map[string]int{"cols": cols, "rows": rows}
	ap.resize(cols, rows)
	d.mu.Unlock()
	d.changed(ap)
}

// sortedIDs is used by tests of the fake itself.
func (d *daemon) sortedIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := append([]string(nil), d.order...)
	sort.Strings(ids)
	return ids
}

func (ap *agentProc) resize(cols, rows int) {
	if ap.tty != nil {
		_ = setWinsize(int(ap.tty.Fd()), cols, rows)
	}
	if ap.cmd != nil && ap.cmd.Process != nil {
		_ = ap.cmd.Process.Signal(syscall.SIGWINCH)
	}
}
