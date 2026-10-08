package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Version is hesperd's version (hello).
var Version = "dev"

// Listen opens the daemon's socket: its directory 0700 (created, or made
// so), the socket 0600. A socket a live daemon answers on is an error; a
// stale one is replaced.
func Listen(path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Getuid() {
		return nil, fmt.Errorf("%s belongs to another user", dir)
	}
	if st.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			c.Close()
			return nil, fmt.Errorf("hesperd is already running on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Server serves the socket: control connections (JSON-RPC lines) and
// attach connections (a JSON line, then frames).
type Server struct {
	reg   *Registry
	mu    sync.Mutex
	conns map[net.Conn]struct{}
	done  chan struct{}
	once  sync.Once
	ln    net.Listener

	drafts *DraftStore // drafts.go

	// agent tree (tree.go): child slots taken by spawns under way, by
	// parent.
	treeMu   sync.Mutex
	spawning map[string]int
	// uploads: the agent each files.put upload is for (tree.go).
	uploads map[string]uploadTarget
	app     *appBridge // app control (appbridge.go)
}

// NewServer serves reg.
func NewServer(reg *Registry) *Server {
	return &Server{reg: reg, drafts: OpenDrafts(reg.opt.StateDir, reg.opt.Logf), app: newAppBridge(), conns: map[net.Conn]struct{}{}, done: make(chan struct{})}
}

// Serve accepts connections until Close.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		go func() {
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				conn.Close()
			}()
			s.handle(conn)
		}()
	}
}

// Close stops serving and closes every connection.
func (s *Server) Close() {
	s.once.Do(func() {
		close(s.done)
		s.mu.Lock()
		if s.ln != nil {
			s.ln.Close()
		}
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
	})
}

func (s *Server) handle(conn net.Conn) {
	// Same user only: the socket's mode keeps others out, the kernel's
	// peer credentials make sure.
	if uid, err := peerUID(conn); err != nil || uid != os.Getuid() {
		return
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := wire.ReadLine(r)
	if err != nil {
		return
	}
	var probe struct {
		Attach *string `json:"attach"`
	}
	json.Unmarshal(line, &probe)
	if probe.Attach != nil {
		s.attach(conn, r, line)
		return
	}
	s.control(conn, r, line)
}

func (s *Server) attach(conn net.Conn, r *bufio.Reader, line []byte) {
	var req wire.AttachRequest
	if err := json.Unmarshal(line, &req); err != nil || req.Attach == "" {
		refuse(conn, wire.Errorf(wire.CodeInvalid, "bad attach request"))
		return
	}
	if req.Mode == wire.ModeRW {
		// agent tree: an agent types only into agents it started.
		if a, ok := s.peerAgent(conn); ok && (a.Kind != wire.KindShell || a.Parent != "") {
			target := s.fullID(req.Attach)
			if !s.tree().descends(target, a.ID) {
				err := wire.Errorf(wire.CodeForbidden, "agent %s may only attach read-write to agents it started; %s is not one of them (attach read-only)", a.ID, target)
				s.auditCall(a.ID, "attach", target, err, nil)
				refuse(conn, err)
				return
			}
		}
	}
	if s.reg.IsRemote(req.Attach) {
		if s.reg.opt.Remote != nil {
			s.reg.opt.Remote.Attach(conn, r, req)
			return
		}
		refuse(conn, wire.Errorf(wire.CodeUnavailable, "%s is on another machine: remote agents are not connected yet", req.Attach))
		return
	}
	term, err := s.reg.Term(req.Attach)
	if err != nil {
		refuse(conn, asWireError(err))
		return
	}
	term.Attach(conn, r, req)
}

func refuse(conn net.Conn, err *wire.Error) {
	line, _ := json.Marshal(wire.AttachReply{Error: err})
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn.Write(append(line, '\n'))
}

func asWireError(err error) *wire.Error {
	var we *wire.Error
	if errors.As(err, &we) {
		return we
	}
	return wire.Errorf(wire.CodeInvalid, "%v", err)
}

// ctrl is one control connection.
type ctrl struct {
	s      *Server
	conn   net.Conn
	wmu    sync.Mutex
	done   chan struct{}
	subbed bool
	mu     sync.Mutex
	// agent tree (tree.go): the peer process and its parents.
	peerOnce sync.Once
	peerPIDs []int
}

func (s *Server) control(conn net.Conn, r *bufio.Reader, first []byte) {
	c := &ctrl{s: s, conn: conn, done: make(chan struct{})}
	var wg sync.WaitGroup
	// done closes first: the watchers (agents, drafts, projects,
	// sessions) end on it, and only then can their wait finish.
	defer func() {
		close(c.done)
		wg.Wait()
	}()
	line := first
	for {
		var req wire.Request
		if err := json.Unmarshal(line, &req); err != nil {
			c.reply(nil, nil, &wire.RPCError{Code: wire.RPCParse, Message: "parse error", Data: &wire.ErrorData{Code: wire.CodeInvalid}})
		} else if s.app.intercept(c, &req, line) {
			// app control (appbridge.go): app.register, the app's responses
		} else if req.Method == "agents.subscribe" {
			c.subscribe(req.ID, &wg)
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, err := s.dispatch(c, req.Method, req.Params)
				if len(req.ID) == 0 {
					return // a notification: no answer
				}
				if err != nil {
					c.reply(req.ID, nil, rpcError(err))
					return
				}
				c.reply(req.ID, result, nil)
			}()
		}
		var err error
		if line, err = wire.ReadLine(r); err != nil {
			c.conn.Close()
			return
		}
	}
}

func rpcError(err error) *wire.RPCError {
	var bad *badParams
	if errors.As(err, &bad) {
		return &wire.RPCError{Code: wire.RPCInvalidParams, Message: bad.Error(), Data: &wire.ErrorData{Code: wire.CodeInvalid}}
	}
	var none *noMethod
	if errors.As(err, &none) {
		return &wire.RPCError{Code: wire.RPCNoMethod, Message: none.Error(), Data: &wire.ErrorData{Code: wire.CodeNotFound}}
	}
	we := asWireError(err)
	code := wire.RPCServer
	if we.Code == wire.CodeOffline {
		code = wire.RPCOffline // closing agents: the app queues the call
	}
	return &wire.RPCError{Code: code, Message: we.Message, Data: &wire.ErrorData{Code: we.Code, AgentID: we.AgentID, Processes: we.Processes}}
}

type badParams struct{ err error }

func (e *badParams) Error() string { return "invalid params: " + e.err.Error() }

type noMethod struct{ method string }

func (e *noMethod) Error() string { return "no method " + e.method }

func (c *ctrl) write(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err = c.conn.Write(append(line, '\n'))
	if err != nil {
		c.conn.Close()
	}
	return err
}

func (c *ctrl) reply(id json.RawMessage, result any, rerr *wire.RPCError) {
	if id == nil {
		id = json.RawMessage("null")
	}
	res := wire.Response{JSONRPC: "2.0", ID: id, Error: rerr}
	if rerr == nil {
		data, err := json.Marshal(result)
		if err != nil {
			res.Error = &wire.RPCError{Code: wire.RPCServer, Message: err.Error()}
		} else {
			res.Result = data
		}
	}
	c.write(res)
}

func (c *ctrl) subscribe(id json.RawMessage, wg *sync.WaitGroup) {
	c.mu.Lock()
	already := c.subbed
	c.subbed = true
	c.mu.Unlock()
	if len(id) > 0 {
		c.reply(id, struct{}{}, nil)
	}
	if already {
		return
	}
	sub := c.s.reg.SubscribeMoves()
	c.subscribeDrafts(wg)
	wg.Add(1)
	go func() { defer wg.Done(); c.watchProjects() }() // projects step 1
	wg.Add(1)
	go func() { defer wg.Done(); c.watchSessions() }() // shared history
	ctx, cancel := context.WithCancel(context.Background())
	if remote := c.s.reg.opt.Remote; remote != nil {
		go remote.Watch(ctx, func(a wire.Agent) { sub.push(a.ID, &a) }, func(rid, reason, to string) { sub.pushRemovedTo(rid, reason, to) })
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		defer c.s.reg.Unsubscribe(sub)
		for {
			notes := sub.Wait(c.done)
			if notes == nil {
				return
			}
			for _, n := range notes {
				var note wire.Notification
				note.JSONRPC = "2.0"
				switch {
				case n.Agent != nil:
					note.Method = wire.NoteChanged
					note.Params, _ = json.Marshal(wire.Changed{Agent: *n.Agent})
				case n.Moving != nil:
					note.Method = wire.NoteMoving // move work
					note.Params, _ = json.Marshal(n.Moving)
				default:
					note.Method = wire.NoteRemoved
					rm := wire.Removed{ID: n.Removed, Reason: n.Reason}
					if n.To != "" {
						rm.Data = &wire.RemovedData{To: n.To}
					}
					note.Params, _ = json.Marshal(rm)
				}
				if c.write(note) != nil {
					return
				}
			}
		}
	}()
}

func decode(params json.RawMessage, v any) error {
	if len(params) == 0 || string(params) == "null" {
		params = []byte("{}")
	}
	if err := json.Unmarshal(params, v); err != nil {
		return &badParams{err}
	}
	return nil
}

// dispatch runs one method of control connection c: the agent tree's
// policy (tree.go) first for the methods that start or change agents.
func (s *Server) dispatch(c *ctrl, method string, params json.RawMessage) (any, error) {
	if method != "agents.spawn" && !treeMutating[method] && !treeStarting[method] && !treeFiles[method] {
		return s.call(method, params)
	}
	caller, t, err := s.callerOf(c, method, params)
	if err != nil {
		return nil, err
	}
	params = withoutCaller(params)
	switch {
	case method == "agents.spawn":
		return s.spawn(caller, params, t)
	case treeStarting[method]:
		return s.startSession(caller, method, params, t) // tree.go
	case treeFiles[method]:
		return s.files(caller, method, params, t) // tree.go
	}
	if caller == "" {
		return s.call(method, params)
	}
	var head wire.IDParams
	if err := decode(params, &head); err != nil {
		return nil, err
	}
	target := s.fullID(head.ID)
	if refused := s.authorize(caller, method, target, t); refused != nil {
		s.auditCall(caller, method, target, refused, nil)
		return nil, refused
	}
	res, err := s.call(method, params)
	s.auditCall(caller, method, target, nil, err)
	return res, err
}

// spawn is agents.spawn: an agent's spawn gets its place in the tree
// (parent, depth) within the limits; a person's has none.
func (s *Server) spawn(caller string, params json.RawMessage, t treeView) (any, error) {
	var p wire.SpawnParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	p.Caller, p.Parent, p.Depth = "", "", 0
	if caller != "" {
		depth, release, err := s.spawnTree(caller, t)
		if err != nil {
			s.auditCall(caller, "agents.spawn", "", err, nil)
			return nil, err
		}
		defer release()
		p.Parent, p.Depth = caller, depth
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	res, err := s.call("agents.spawn", raw)
	if caller != "" {
		var child struct {
			ID string `json:"id"`
		}
		switch v := res.(type) {
		case wire.Agent:
			child.ID = v.ID
		case json.RawMessage:
			json.Unmarshal(v, &child)
		}
		s.auditCall(caller, "agents.spawn", child.ID, nil, err)
	}
	return res, err
}

// call runs one method.
func (s *Server) call(method string, params json.RawMessage) (any, error) {
	reg := s.reg
	if strings.HasPrefix(method, "agents.") {
		params = withoutCaller(params) // hosts decode strictly
	}
	remote := reg.opt.Remote
	// Methods about one agent go to its machine.
	forward := func(id string) (bool, any, error) {
		if !reg.IsRemote(id) {
			return false, nil, nil
		}
		if remote == nil {
			m, _ := reg.split(id)
			return true, nil, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", m)
		}
		m, _ := reg.split(id)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		res, err := remote.Call(ctx, m, method, params)
		return true, res, err
	}
	switch method {
	case "hello":
		var p wire.HelloParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		machines := []wire.Machine{{Short: reg.machine, Name: hostName(), Online: true, Route: "local"}}
		if remote != nil {
			machines = append(machines, remote.Machines()...)
		}
		return wire.HelloResult{Daemon: "hesperd", Version: Version, Machine: reg.machine, Machines: machines}, nil
	case "agents.list":
		list := reg.List()
		if remote != nil {
			list = append(list, remote.Agents()...)
		}
		return list, nil
	case "agents.spawn":
		var p wire.SpawnParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.Machine != "" && p.Machine != reg.machine {
			if remote == nil {
				return nil, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", p.Machine)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			return remote.Call(ctx, p.Machine, method, params)
		}
		return reg.Spawn(p)
	case "agents.close", "agents.kill", "agents.background":
		// closing agents (close.go)
		var p wire.BackgroundParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, &badParams{errors.New("id is required")}
		}
		if ok, res, err := forward(p.ID); ok {
			return res, offline(err)
		}
		switch method {
		case "agents.close":
			return reg.CloseAgent(p.ID)
		case "agents.kill":
			return struct{}{}, reg.Kill(p.ID)
		}
		return struct{}{}, reg.SetBackground(p.ID, p.Background)
	case "agents.screen":
		return s.screen(params, forward) // screen.go
	case "agents.checkpoint":
		// move work (checkpoint.go)
		var p wire.IDParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, &badParams{errors.New("id is required")}
		}
		if ok, res, err := forward(p.ID); ok {
			return res, err
		}
		cp, err := reg.Checkpoint(p.ID)
		if err != nil {
			return nil, err
		}
		return wire.CheckpointResult{Checkpoint: cp}, nil
	case "agents.input", "agents.answer", "agents.stop", "agents.resume", "agents.remove", "agents.rename", "agents.move":
		var head wire.IDParams
		if err := decode(params, &head); err != nil {
			return nil, err
		}
		if head.ID == "" {
			return nil, &badParams{errors.New("id is required")}
		}
		if method == "agents.move" {
			var p wire.MoveParams
			if err := decode(params, &p); err != nil {
				return nil, err
			}
			if remote == nil {
				return nil, wire.Errorf(wire.CodeUnavailable, "moving agents between machines needs the remote connection (part R)")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			res, err := remote.Move(ctx, p)
			if err == nil && !p.Fork {
				// The moved agent's children (this Mac's) follow it.
				m, local := reg.split(p.ID)
				reg.Reparent(m+"/"+local, res.Agent)
			}
			return res, err
		}
		if ok, res, err := forward(head.ID); ok {
			return res, err
		}
		switch method {
		case "agents.input":
			var p wire.InputParams
			if err := decode(params, &p); err != nil {
				return nil, err
			}
			return struct{}{}, reg.Input(p)
		case "agents.answer":
			var p wire.AnswerParams
			if err := decode(params, &p); err != nil {
				return nil, err
			}
			return struct{}{}, reg.Answer(p)
		case "agents.stop":
			return struct{}{}, reg.Stop(head.ID)
		case "agents.resume":
			return reg.Resume(head.ID)
		case "agents.remove":
			return struct{}{}, reg.Remove(head.ID)
		case "agents.rename":
			var p wire.RenameParams
			if err := decode(params, &p); err != nil {
				return nil, err
			}
			return reg.Rename(p.ID, p.Name)
		}
	case "agents.result":
		// agent tree: the last turn's final message
		var p wire.IDParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			return nil, &badParams{errors.New("id is required")}
		}
		if ok, res, err := forward(p.ID); ok {
			if err != nil && remote != nil {
				// A host without agents.result: its summary.
				for _, a := range remote.Agents() {
					if a.ID == p.ID {
						return wire.AgentResult{ID: a.ID, State: a.State, Summary: a.Summary}, nil
					}
				}
			}
			return res, err
		}
		return reg.Result(p.ID)
	case "projects.recent":
		return reg.Recent(), nil
	case "projects.clone":
		var p CloneParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.Machine != "" && p.Machine != reg.machine {
			if remote == nil {
				return nil, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", p.Machine)
			}
			ctx, cancel := context.WithTimeout(context.Background(), CloneTimeout)
			defer cancel()
			return remote.Call(ctx, p.Machine, method, params)
		}
		ctx, cancel := context.WithTimeout(context.Background(), CloneTimeout)
		defer cancel()
		return reg.Clone(ctx, p)
	case "profiles.list":
		return reg.Profiles(), nil
	case "fs.stat":
		var p wire.FSStatParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.Machine != "" && p.Machine != reg.machine {
			if remote == nil {
				return nil, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", p.Machine)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return remote.Call(ctx, p.Machine, method, params)
		}
		return Stat(p.Path)
	case "files.put":
		var p FilePut
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if p.Upload != "" || p.EPK != "" {
			return nil, &badParams{errors.New("upload and epk are set by hesperd")}
		}
		machine := p.Machine
		if p.Agent != "" {
			machine, _ = reg.split(p.Agent)
		}
		if machine != "" && machine != reg.machine {
			if remote == nil {
				return nil, wire.Errorf(wire.CodeUnavailable, "machine %s is not connected", machine)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			return remote.FilePut(ctx, machine, p)
		}
		if p.Agent != "" {
			_, local := reg.split(p.Agent)
			p.Agent = reg.id(local)
		}
		return reg.Files().Begin(p, UploadHooks{})
	case "files.chunk":
		var p FileChunkParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		if remote != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if res, found, err := remote.FileChunk(ctx, p); found {
				return res, err
			}
		}
		return reg.Files().Chunk(p, false)
	case "drafts.list", "drafts.save", "drafts.remove":
		return s.drafts.call(method, params)
	case "projects.list", "projects.update", "projects.promote", "projects.remove", "groups.list", "groups.save", "groups.remove",
		"projects.scratch", "projects.scratchKeep", "projects.scratchArchive", "projects.scratchRestore", "projects.scratchDelete",
		"projects.scratchSettings", "settings.get", "settings.set": // scratch projects
		// projects step 1 (projecthook.go)
		if reg.opt.Projects != nil {
			res, err, _ := reg.opt.Projects.Call(method, params)
			return res, err
		}
		return nil, wire.Errorf(wire.CodeUnavailable, "projects are not available")
	case "sessions.search", "sessions.show", "sessions.resume", "sessions.fork", "sessions.brief", "sessions.continueAs",
		"sessions.archive", "sessions.delete", "sessions.stats", "checkpoints.restore":
		// shared history (sessionhook.go)
		if reg.opt.Sessions != nil {
			res, err, _ := reg.opt.Sessions.Call(method, params)
			return res, err
		}
		return nil, wire.Errorf(wire.CodeUnavailable, "the shared history is not available")
	case "app.state", "app.open", "app.wall.set", "app.desk":
		return s.app.forward(method, withoutCaller(params)) // app control (appbridge.go): any caller, it touches no agent
	case "hook":
		var p wire.HookParams
		if err := decode(params, &p); err != nil {
			return nil, err
		}
		return struct{}{}, reg.Hook(p)
	}
	return nil, &noMethod{method}
}

// offline: closing agents' calls for another machine that cannot reach it
// fail with "offline" (-32010), which the app queues.
func offline(err error) error {
	var we *wire.Error
	if errors.As(err, &we) && we.Code == wire.CodeUnavailable {
		return &wire.Error{Code: wire.CodeOffline, Message: "machine offline: " + we.Message}
	}
	return err
}

func hostName() string {
	h, _ := os.Hostname()
	return h
}
