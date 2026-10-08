package agents

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The agent tree (docs/rebuild-contract.md, "As built — agent tree"):
// agents start agents (hesperctl new inside an agent), and an agent may
// steer only what it started.
//
// Who calls: the agent whose process tree the caller's process is in
// (the socket peer's PID, then its parents: verified), else the params'
// "caller" (HESPER_AGENT_ID, which hesperctl sends: advisory), else a
// person. A person's own shell agent (kind shell, no parent) is the
// person's terminal: its callers are the person. The boundary stops
// accidents and prompt-injection chains, not a hostile process of the
// same user: such a process can leave the agent's process tree (a
// double fork) and drop HESPER_AGENT_ID.
//
// Policy, when an agent calls (a person may do everything):
//   - read-only methods: anything;
//   - agents.spawn: the new agent's parent is the caller, its depth the
//     caller's + 1, at most MaxAgentDepth, with at most MaxAgentChildren
//     live children per agent;
//   - agents.answer, and agents.input to an agent in approval or
//     question: only the caller's own children started with
//     letParentAnswer — never itself, never another agent's;
//   - every other method that changes an agent (treeMutating), and rw
//     attaches (verified callers only): the caller's descendants only.

// Agent tree limits (settings.json maxAgentDepth, maxAgentChildren).
const (
	DefaultMaxAgentDepth    = 3
	DefaultMaxAgentChildren = 8
)

func (s Settings) maxAgentDepth() int {
	if s.MaxAgentDepth == nil {
		return DefaultMaxAgentDepth
	}
	return max(*s.MaxAgentDepth, 0)
}

func (s Settings) maxAgentChildren() int {
	if s.MaxAgentChildren == nil {
		return DefaultMaxAgentChildren
	}
	return max(*s.MaxAgentChildren, 0)
}

// treeMutating are the methods that change one agent (params {id, …}):
// an agent may call them only on its descendants. A new such method
// belongs here.
var treeMutating = map[string]bool{
	"agents.input": true, "agents.answer": true, "agents.stop": true, "agents.resume": true,
	"agents.remove": true, "agents.rename": true, "agents.move": true,
	"agents.close": true, "agents.kill": true, "agents.background": true,
}

// maxAncestry bounds walks up process and agent trees.
const maxAncestry = 64

// processAncestors is pid and its parents, nearest first (without
// launchd/init).
func processAncestors(pid int) []int {
	var out []int
	for i := 0; pid > 1 && i < maxAncestry; i++ {
		out = append(out, pid)
		ppid, err := parentPID(pid)
		if err != nil || ppid == pid {
			break
		}
		pid = ppid
	}
	return out
}

// agentOfPIDs is the running agent of this machine whose process is one
// of pids (the nearest).
func (r *Registry) agentOfPIDs(pids []int) (wire.Agent, bool) {
	if len(pids) == 0 {
		return wire.Agent{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	byPID := map[int]*agent{}
	for _, a := range r.agents {
		if a.PID > 0 && a.term != nil && a.Exit == nil {
			byPID[a.PID] = a
		}
	}
	for _, pid := range pids {
		if a := byPID[pid]; a != nil {
			return a.Agent, true
		}
	}
	return wire.Agent{}, false
}

// peerAgent is the agent a socket peer runs in (verified), if any.
func (s *Server) peerAgent(conn net.Conn) (wire.Agent, bool) {
	pid, err := peerPID(conn)
	if err != nil || pid <= 0 {
		return wire.Agent{}, false
	}
	return s.reg.agentOfPIDs(processAncestors(pid))
}

// treeView is every agent this daemon knows (this machine's and the
// remote ones), by full id.
type treeView map[string]wire.Agent

func (s *Server) tree() treeView {
	t := treeView{}
	for _, a := range s.reg.List() {
		t[a.ID] = a
	}
	if remote := s.reg.opt.Remote; remote != nil {
		for _, a := range remote.Agents() {
			t[a.ID] = a
		}
	}
	return t
}

// descends reports whether id is a strict descendant of ancestor.
func (t treeView) descends(id, ancestor string) bool {
	a, ok := t[id]
	for i := 0; ok && i < maxAncestry; i++ {
		if a.Parent == "" || a.Parent == id {
			return false
		}
		if a.Parent == ancestor {
			return true
		}
		a, ok = t[a.Parent]
	}
	return false
}

// liveChildren counts parent's children that run (or are starting).
func (t treeView) liveChildren(parent string) int {
	n := 0
	for _, a := range t {
		if a.Parent == parent && a.Exit == nil && a.State != wire.StateExited {
			n++
		}
	}
	return n
}

// fullID completes a local id with this machine.
func (s *Server) fullID(id string) string {
	if id == "" || strings.Contains(id, "/") {
		return id
	}
	return s.reg.id(id)
}

// callerOf is the agent a control call acts as ("" for a person): the
// agent the caller's process runs in (c's peer), else the params'
// caller. An agent this daemon does not know is refused. t is the tree
// when the caller is an agent.
func (s *Server) callerOf(c *ctrl, method string, params json.RawMessage) (string, treeView, error) {
	var probe struct {
		Caller string `json:"caller"`
	}
	json.Unmarshal(params, &probe)
	claimed := s.fullID(strings.TrimSpace(probe.Caller))
	caller := claimed
	if c != nil {
		if a, ok := c.peerAgent(); ok {
			if claimed != "" && claimed != a.ID {
				s.audit("agent.caller-mismatch", auditFields{Agent: a.ID, Method: method,
					Detail: "claimed caller " + claimed + "; the caller runs in " + a.ID})
			}
			caller = a.ID
		}
	}
	if caller == "" {
		return "", nil, nil
	}
	t := s.tree()
	a, ok := t[caller]
	if !ok {
		err := wire.Errorf(wire.CodeForbidden, "caller agent %s is unknown to hesperd (unset HESPER_AGENT_ID to act as a person)", caller)
		s.auditCall(caller, method, "", err, nil)
		return "", nil, err
	}
	if a.Kind == wire.KindShell && a.Parent == "" {
		return "", nil, nil // a person's own terminal
	}
	return caller, t, nil
}

// authorize checks whether caller (an agent; "" a person) may run method
// on target (a full id). Unknown targets pass: the method says not_found.
func (s *Server) authorize(caller, method, target string, t treeView) error {
	if caller == "" || !treeMutating[method] {
		return nil
	}
	a, ok := t[target]
	if !ok {
		return nil
	}
	answering := method == "agents.answer" ||
		(method == "agents.input" && (a.State == wire.StateApproval || a.State == wire.StateQuestion))
	if answering {
		return answerAllowed(caller, a)
	}
	if !t.descends(target, caller) {
		if target == caller {
			return wire.Errorf(wire.CodeForbidden, "agent %s may not %s itself: only agents it started (a person may)", caller, verb(method))
		}
		return wire.Errorf(wire.CodeForbidden, "agent %s may only %s agents it started; %s is not one of them", caller, verb(method), target)
	}
	return nil
}

// answerAllowed: an agent answers only its own children's approvals and
// questions, and only those started with letParentAnswer.
func answerAllowed(caller string, a wire.Agent) error {
	switch {
	case a.ID == caller:
		return wire.Errorf(wire.CodeForbidden, "agent %s may not answer its own approvals or questions: a person does", caller)
	case a.Parent != caller:
		return wire.Errorf(wire.CodeForbidden, "agent %s may only answer approvals of its own children; %s is not its child", caller, a.ID)
	case !a.LetParentAnswer:
		return wire.Errorf(wire.CodeForbidden, "%s was not started with --let-parent-answer: a person answers its approvals and questions", a.ID)
	}
	return nil
}

func verb(method string) string {
	switch method {
	case "agents.input":
		return "type into"
	case "agents.move":
		return "move"
	}
	return strings.TrimPrefix(method, "agents.")
}

// spawnTree decides a spawn's place in the tree: parent caller, depth
// the caller's + 1, within the limits. release ends the reservation of
// a child slot (call it once the spawn returned).
func (s *Server) spawnTree(caller string, t treeView) (depth int, release func(), err error) {
	parent := t[caller]
	s.reg.mu.Lock()
	settings := s.reg.settings
	s.reg.mu.Unlock()
	depth = parent.Depth + 1
	if limit := settings.maxAgentDepth(); depth > limit {
		return 0, nil, wire.Errorf(wire.CodeForbidden, "agent %s is at depth %d: agents may start agents at most %d deep (settings.json maxAgentDepth)", caller, parent.Depth, limit)
	}
	s.treeMu.Lock()
	defer s.treeMu.Unlock()
	if s.spawning == nil {
		s.spawning = map[string]int{}
	}
	live := t.liveChildren(caller) + s.spawning[caller]
	if limit := settings.maxAgentChildren(); live >= limit {
		return 0, nil, wire.Errorf(wire.CodeForbidden, "agent %s has %d live children: at most %d (settings.json maxAgentChildren); close one first", caller, live, limit)
	}
	s.spawning[caller]++
	var once sync.Once
	return depth, func() {
		once.Do(func() {
			s.treeMu.Lock()
			if s.spawning[caller]--; s.spawning[caller] <= 0 {
				delete(s.spawning, caller)
			}
			s.treeMu.Unlock()
		})
	}, nil
}

// withoutCaller drops "caller" from params forwarded to another machine
// (this daemon enforced the policy; hosts decode strictly).
func withoutCaller(params json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil || m == nil {
		return params
	}
	if _, ok := m["caller"]; !ok {
		return params
	}
	delete(m, "caller")
	out, err := json.Marshal(m)
	if err != nil {
		return params
	}
	return out
}

// Reparent points the children of an agent that moved (old id) at its new
// id.
func (r *Registry) Reparent(oldID, newID string) {
	if oldID == newID || oldID == "" || newID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.agents {
		if a.Parent == oldID {
			a.Parent = newID
			r.changed(a)
		}
	}
}

// maxLastMessage bounds the stored final message.
const maxLastMessage = 64 << 10

// setLastMessage keeps the final message of an agent's turn (the lock is
// held).
func (r *Registry) setLastMessage(a *agent, message string) {
	if len(message) > maxLastMessage {
		message = strings.ToValidUTF8(message[len(message)-maxLastMessage:], "")
	}
	a.lastMessage, a.lastMessageAt = message, time.Now().UTC()
	r.scheduleSave()
}

// Result is agents.result: an agent's last turn's final message, else its
// summary.
func (r *Registry) Result(id string) (wire.AgentResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, err := r.find(id)
	if err != nil {
		return wire.AgentResult{}, err
	}
	return wire.AgentResult{ID: a.ID, State: a.State, Message: a.lastMessage, Summary: a.Summary, At: a.lastMessageAt}, nil
}

// Audit: agents' calls go to audit.log in the state directory (the same
// file and line format as the host's device requests, plus agent and
// target).

type auditFields struct {
	Agent, Target, Method, Detail string
	OK                            bool
}

const maxAuditBytes = 10 << 20

var auditMu sync.Mutex

func (s *Server) audit(event string, f auditFields) {
	entry := struct {
		At     string `json:"at"`
		Event  string `json:"event"`
		Agent  string `json:"agent,omitempty"`
		Target string `json:"target,omitempty"`
		Method string `json:"method,omitempty"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail,omitempty"`
		Route  string `json:"route"`
	}{At: time.Now().UTC().Format(time.RFC3339Nano), Event: event, Agent: f.Agent, Target: f.Target, Method: f.Method, OK: f.OK, Detail: firstLine(f.Detail, 512), Route: "local"}
	line, _ := json.Marshal(entry)
	path := filepath.Join(s.reg.opt.StateDir, "audit.log")
	auditMu.Lock()
	defer auditMu.Unlock()
	if st, err := os.Stat(path); err == nil && st.Size() > maxAuditBytes {
		os.Rename(path, path+".1")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		s.reg.opt.Logf("hesperd: audit: %v", err)
		return
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		s.reg.opt.Logf("hesperd: audit: %v", err)
	}
}

// auditCall records an agent's call (a person's are not audited here):
// "agent.refused" when the tree policy refused it, else "agent.request"
// with ok and the method's error.
func (s *Server) auditCall(caller, method, target string, refused, err error) {
	if caller == "" {
		return
	}
	if refused != nil {
		s.audit("agent.refused", auditFields{Agent: caller, Target: target, Method: method, Detail: refused.Error()})
		return
	}
	f := auditFields{Agent: caller, Target: target, Method: method, OK: err == nil}
	if err != nil {
		f.Detail = err.Error()
	}
	s.audit("agent.request", f)
}

// peerAgent is the agent this connection's peer runs in (its process
// ancestry, looked up once per connection).
func (c *ctrl) peerAgent() (wire.Agent, bool) {
	c.peerOnce.Do(func() {
		if pid, err := peerPID(c.conn); err == nil && pid > 0 {
			c.peerPIDs = processAncestors(pid)
		}
	})
	return c.s.reg.agentOfPIDs(c.peerPIDs)
}
