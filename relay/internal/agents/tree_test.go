package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The agent tree (tree.go): parents, depth, limits, who may answer and
// steer whom, the audit, agents.result.

func forbidden(t *testing.T, what string, err error) {
	t.Helper()
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.CodeForbidden {
		t.Fatalf("%s: %v, want forbidden", what, err)
	}
}

func allowed(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// spawnAs spawns as agent caller ("" a person).
func (h *harness) spawnAs(caller string, p wire.SpawnParams) (wire.Agent, error) {
	if p.Project == "" {
		p.Project = h.project
	}
	p.Caller = caller
	var a wire.Agent
	err := h.call("agents.spawn", p, &a)
	return a, err
}

// callAs calls method with "caller" set, the way hesperctl does.
func (h *harness) callAs(caller, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := h.client()
	c.Caller = caller
	return c.Call(ctx, method, params, result)
}

func auditLines(t *testing.T, h *harness) []map[string]any {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(h.state, "audit.log"))
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func TestAgentTreeAnswerPolicy(t *testing.T) {
	h := newHarness(t)
	parent := h.spawn(wire.SpawnParams{Task: "small job"})
	if parent.Parent != "" || parent.Depth != 0 {
		t.Fatalf("a person's agent %+v", parent)
	}
	// The parent's children: one it may answer, one it may not.
	mayAnswer, err := h.spawnAs(parent.ID, wire.SpawnParams{Task: "approve this", LetParentAnswer: true})
	allowed(t, "spawn child", err)
	if mayAnswer.Parent != parent.ID || mayAnswer.Depth != 1 || !mayAnswer.LetParentAnswer {
		t.Fatalf("child %+v", mayAnswer)
	}
	personOnly, err := h.spawnAs(parent.ID, wire.SpawnParams{Task: "approve this"})
	allowed(t, "spawn child", err)
	if personOnly.LetParentAnswer || personOnly.Parent != parent.ID {
		t.Fatalf("child %+v", personOnly)
	}
	// Someone else's agent, and a person's with the flag (meaningless).
	other, err := h.spawnAs("", wire.SpawnParams{Task: "approve this", LetParentAnswer: true})
	allowed(t, "person spawn", err)
	if other.Parent != "" || other.LetParentAnswer {
		t.Fatalf("person's agent %+v", other)
	}
	// A grandchild the child lets answer.
	grandchild, err := h.spawnAs(mayAnswer.ID, wire.SpawnParams{Task: "approve this", LetParentAnswer: true})
	allowed(t, "spawn grandchild", err)
	if grandchild.Parent != mayAnswer.ID || grandchild.Depth != 2 {
		t.Fatalf("grandchild %+v", grandchild)
	}
	for _, a := range []wire.Agent{mayAnswer, personOnly, other, grandchild} {
		h.waitState(a.ID, wire.StateApproval)
	}
	h.waitState(parent.ID, wire.StateDone)
	allow := func(id string) wire.AnswerParams { return wire.AnswerParams{ID: id, Decision: wire.Allow} }

	// Never another agent's, never a grandchild's, never without the flag.
	forbidden(t, "parent answers another agent's", h.callAs(parent.ID, "agents.answer", allow(other.ID), nil))
	forbidden(t, "parent answers its grandchild", h.callAs(parent.ID, "agents.answer", allow(grandchild.ID), nil))
	forbidden(t, "parent answers a child without --let-parent-answer", h.callAs(parent.ID, "agents.answer", allow(personOnly.ID), nil))
	forbidden(t, "child answers itself", h.callAs(personOnly.ID, "agents.answer", allow(personOnly.ID), nil))
	forbidden(t, "child answers its parent's sibling", h.callAs(mayAnswer.ID, "agents.answer", allow(personOnly.ID), nil))
	forbidden(t, "child answers the person's", h.callAs(grandchild.ID, "agents.answer", allow(other.ID), nil))
	// Input into an agent that waits is answering: the same rule.
	forbidden(t, "parent types into a waiting child without the flag",
		h.callAs(parent.ID, "agents.input", wire.InputParams{ID: personOnly.ID, Text: "1"}, nil))
	forbidden(t, "local id", h.callAs(parent.ID, "agents.answer", allow(strings.TrimPrefix(personOnly.ID, "L/")), nil))
	for _, a := range []wire.Agent{personOnly, other, grandchild} {
		if g, _ := h.reg.Get(a.ID); g.State != wire.StateApproval {
			t.Fatalf("%s answered: %s", a.ID, g.State)
		}
	}
	// The parent answers its own child with the flag; the child its own.
	allowed(t, "parent answers its child", h.callAs(parent.ID, "agents.answer", allow(mayAnswer.ID), nil))
	h.waitKeys(mayAnswer.ID, "1")
	allowed(t, "child answers its child", h.callAs(mayAnswer.ID, "agents.answer", allow(grandchild.ID), nil))
	h.waitKeys(grandchild.ID, "1")
	// A person answers everything.
	allowed(t, "person answers", h.callAs("", "agents.answer", allow(personOnly.ID), nil))
	allowed(t, "person answers", h.call("agents.answer", allow(other.ID), nil))

	// Steering: descendants only.
	done := h.waitState(mayAnswer.ID, wire.StateDone)
	allowed(t, "parent types into its child", h.callAs(parent.ID, "agents.input", wire.InputParams{ID: done.ID, Text: "more", Submit: true}, nil))
	forbidden(t, "parent types into another agent", h.callAs(parent.ID, "agents.input", wire.InputParams{ID: other.ID, Text: "x"}, nil))
	forbidden(t, "child renames its parent", h.callAs(mayAnswer.ID, "agents.rename", wire.RenameParams{ID: parent.ID, Name: "boss"}, nil))
	forbidden(t, "agent stops itself", h.callAs(parent.ID, "agents.stop", wire.IDParams{ID: parent.ID}, nil))
	forbidden(t, "agent kills another", h.callAs(parent.ID, "agents.kill", wire.IDParams{ID: other.ID}, nil))
	forbidden(t, "agent closes another", h.callAs(parent.ID, "agents.close", wire.IDParams{ID: other.ID}, nil))
	allowed(t, "parent renames its grandchild", h.callAs(parent.ID, "agents.rename", wire.RenameParams{ID: grandchild.ID, Name: "gc"}, nil))
	allowed(t, "parent stops its grandchild", h.callAs(parent.ID, "agents.stop", wire.IDParams{ID: grandchild.ID}, nil))
	// Read-only methods are open.
	var list []wire.Agent
	allowed(t, "list", h.callAs(personOnly.ID, "agents.list", nil, &list))
	var res wire.AgentResult
	allowed(t, "result", h.callAs(personOnly.ID, "agents.result", wire.IDParams{ID: parent.ID}, &res))

	// An agent hesperd does not know is refused, not taken for a person.
	forbidden(t, "unknown caller", h.callAs("L/nobody", "agents.stop", wire.IDParams{ID: other.ID}, nil))
	_, err = h.spawnAs("zz/nobody", wire.SpawnParams{Kind: "shell"})
	forbidden(t, "unknown caller spawns", err)

	// A person's own shell agent is the person.
	shell := h.spawn(wire.SpawnParams{Kind: "shell"})
	allowed(t, "a person's shell renames", h.callAs(shell.ID, "agents.rename", wire.RenameParams{ID: other.ID, Name: "renamed"}, nil))
	// A shell an agent started is that agent's child, an agent itself.
	childShell, err := h.spawnAs(parent.ID, wire.SpawnParams{Kind: "shell"})
	allowed(t, "spawn shell child", err)
	forbidden(t, "a child shell renames another", h.callAs(childShell.ID, "agents.rename", wire.RenameParams{ID: other.ID, Name: "x"}, nil))

	// The audit names the acting agent.
	var refused, requests int
	for _, l := range auditLines(t, h) {
		if l["agent"] == parent.ID && l["event"] == "agent.refused" && l["method"] == "agents.answer" && l["target"] == other.ID {
			refused++
		}
		if l["agent"] == parent.ID && l["event"] == "agent.request" && l["method"] == "agents.spawn" && l["ok"] == true {
			requests++
		}
	}
	if refused != 1 || requests < 3 {
		t.Fatalf("audit: %d refusals, %d spawns: %v", refused, requests, auditLines(t, h))
	}
}

func TestAgentTreeLimits(t *testing.T) {
	h := newHarness(t)
	allowed(t, "hello", h.call("hello", nil, nil)) // serving: close stops it
	h.close()
	writeJSON(t, filepath.Join(h.config, "settings.json"), map[string]any{
		"defaults":      map[string]any{"kind": "claude", "kinds": map[string]string{"claude": "fake-claude", "codex": "fake-codex", "shell": "fake-shell"}},
		"maxAgentDepth": 2, "maxAgentChildren": 2,
	})
	h.open()
	root := h.spawn(wire.SpawnParams{Task: "small job"})
	c1, err := h.spawnAs(root.ID, wire.SpawnParams{Task: "small job"})
	allowed(t, "child 1", err)
	g1, err := h.spawnAs(c1.ID, wire.SpawnParams{Task: "small job"})
	allowed(t, "grandchild (depth 2)", err)
	_, err = h.spawnAs(g1.ID, wire.SpawnParams{Task: "small job"})
	forbidden(t, "depth 3", err)
	if !strings.Contains(err.Error(), "maxAgentDepth") {
		t.Fatalf("message %q", err)
	}
	c2, err := h.spawnAs(root.ID, wire.SpawnParams{Task: "small job"})
	allowed(t, "child 2", err)
	_, err = h.spawnAs(root.ID, wire.SpawnParams{Task: "small job"})
	forbidden(t, "child 3", err)
	if !strings.Contains(err.Error(), "maxAgentChildren") {
		t.Fatalf("message %q", err)
	}
	// An exited child frees its slot.
	allowed(t, "stop child", h.callAs(root.ID, "agents.stop", wire.IDParams{ID: c2.ID}, nil))
	h.waitState(c2.ID, wire.StateExited)
	_, err = h.spawnAs(root.ID, wire.SpawnParams{Task: "small job"})
	allowed(t, "child 3 after one exited", err)
	// A person has no limits.
	for range 3 {
		if _, err := h.spawnAs("", wire.SpawnParams{Kind: "shell"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentTreePersistsAndResult(t *testing.T) {
	h := newHarness(t)
	parent := h.spawn(wire.SpawnParams{Task: "small job"})
	child, err := h.spawnAs(parent.ID, wire.SpawnParams{Task: "approve this", LetParentAnswer: true})
	allowed(t, "spawn", err)
	h.waitState(child.ID, wire.StateApproval)
	var res wire.AgentResult
	allowed(t, "result before", h.call("agents.result", wire.IDParams{ID: child.ID}, &res))
	if res.Message != "" || res.State != wire.StateApproval {
		t.Fatalf("result before the turn ended %+v", res)
	}
	allowed(t, "answer", h.callAs(parent.ID, "agents.answer", wire.AnswerParams{ID: child.ID, Decision: wire.Allow}, nil))
	h.waitState(child.ID, wire.StateDone)
	allowed(t, "result", h.call("agents.result", wire.IDParams{ID: child.ID}, &res))
	if res.Message != "Pushed.\n\nOpened PR #482 (draft)" || res.Summary != "Opened PR #482 (draft)" || res.At.IsZero() || res.ID != child.ID {
		t.Fatalf("result %+v", res)
	}
	// A daemon restart keeps the tree and the result.
	h.close()
	h.open()
	got, err := h.reg.Get(child.ID)
	if err != nil || got.Parent != parent.ID || got.Depth != 1 || !got.LetParentAnswer {
		t.Fatalf("after restart %+v %v", got, err)
	}
	allowed(t, "result after restart", h.call("agents.result", wire.IDParams{ID: child.ID}, &res))
	if res.Message != "Pushed.\n\nOpened PR #482 (draft)" {
		t.Fatalf("result after restart %+v", res)
	}
	// The parent still steers it.
	allowed(t, "rename after restart", h.callAs(parent.ID, "agents.rename", wire.RenameParams{ID: child.ID, Name: "kid"}, nil))
	// Moving the parent re-points the children.
	h.reg.Reparent(parent.ID, "M/"+strings.TrimPrefix(parent.ID, "L/"))
	if g, _ := h.reg.Get(child.ID); g.Parent != "M/"+strings.TrimPrefix(parent.ID, "L/") {
		t.Fatalf("reparented %+v", g)
	}
	err = h.call("agents.result", wire.IDParams{ID: "L/nope00"}, &res)
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.CodeNotFound {
		t.Fatalf("result of nothing: %v", err)
	}
}

// The caller hesperd finds by the peer's process wins over what it claims.
func TestAgentTreeVerifiedCaller(t *testing.T) {
	if _, err := parentPID(os.Getpid()); err != nil {
		t.Skipf("no process parents here: %v", err)
	}
	// The walk stops before init: run as a container's PID 1, `go test`
	// leaves the test binary with no ancestor but itself.
	if got := processAncestors(os.Getpid()); len(got) < 1 || got[0] != os.Getpid() ||
		os.Getppid() > 1 && (len(got) < 2 || got[1] != os.Getppid()) {
		t.Fatalf("ancestors %v (ppid %d)", got, os.Getppid())
	}
	h := newHarness(t)
	parent := h.spawn(wire.SpawnParams{Task: "small job"})
	other := h.spawn(wire.SpawnParams{Task: "approve this"})
	h.waitState(other.ID, wire.StateApproval)
	h.waitState(parent.ID, wire.StateDone)
	pid := parent.PID
	// Connections now come from inside the parent's process tree.
	f := func(net.Conn) (int, error) { return pid, nil }
	old := peerPIDCheck.Load()
	peerPIDCheck.Store(&f)
	t.Cleanup(func() { peerPIDCheck.Store(old) })

	// No claim (an agent using the socket directly): still the parent.
	forbidden(t, "unclaimed", h.call("agents.answer", wire.AnswerParams{ID: other.ID, Decision: wire.Allow}, nil))
	// Claiming to be someone else does not help.
	forbidden(t, "lying", h.callAs(other.ID, "agents.stop", wire.IDParams{ID: other.ID}, nil))
	mismatch := false
	for _, l := range auditLines(t, h) {
		if l["event"] == "agent.caller-mismatch" && l["agent"] == parent.ID {
			mismatch = true
		}
	}
	if !mismatch {
		t.Fatalf("no mismatch in the audit: %v", auditLines(t, h))
	}
	// A spawn from there is the parent's child.
	child, err := h.spawnAs("", wire.SpawnParams{Kind: "shell"})
	allowed(t, "spawn", err)
	if child.Parent != parent.ID || child.Depth != 1 {
		t.Fatalf("child %+v", child)
	}
	// Read-write attaches only to descendants; read-only to anyone.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = wire.Attach(ctx, h.sock, wire.AttachRequest{Attach: other.ID, Mode: wire.ModeRW, Cols: 80, Rows: 24})
	forbidden(t, "rw attach to another", err)
	ro, err := wire.Attach(ctx, h.sock, wire.AttachRequest{Attach: other.ID, Mode: wire.ModeRO, Cols: 80, Rows: 24})
	allowed(t, "ro attach", err)
	ro.Close()
	rw, err := wire.Attach(ctx, h.sock, wire.AttachRequest{Attach: child.ID, Mode: wire.ModeRW, Cols: 80, Rows: 24})
	allowed(t, "rw attach to its child", err)
	rw.Close()
}

// The policy as a table over a tree (no daemon).
func TestAgentTreePolicyTable(t *testing.T) {
	s := &Server{reg: &Registry{machine: "L"}}
	tree := treeView{
		"L/p":  {ID: "L/p", Kind: wire.KindClaude},
		"L/c":  {ID: "L/c", Kind: wire.KindClaude, Parent: "L/p", Depth: 1, LetParentAnswer: true, State: wire.StateApproval},
		"L/n":  {ID: "L/n", Kind: wire.KindClaude, Parent: "L/p", Depth: 1, State: wire.StateApproval},
		"L/g":  {ID: "L/g", Kind: wire.KindClaude, Parent: "L/c", Depth: 2, LetParentAnswer: true, State: wire.StateWorking},
		"M/r":  {ID: "M/r", Kind: wire.KindClaude, Parent: "L/p", Depth: 1, LetParentAnswer: true, State: wire.StateQuestion},
		"L/x":  {ID: "L/x", Kind: wire.KindClaude, State: wire.StateApproval},
		"L/xc": {ID: "L/xc", Kind: wire.KindClaude, Parent: "L/x", Depth: 1, LetParentAnswer: true, State: wire.StateApproval},
	}
	cases := []struct {
		caller, method, target string
		ok                     bool
	}{
		{"", "agents.answer", "L/x", true}, // a person
		{"", "agents.kill", "L/p", true},
		{"L/p", "agents.answer", "L/c", true},   // own child, flag
		{"L/p", "agents.answer", "M/r", true},   // own child on another machine
		{"L/p", "agents.input", "M/r", true},    // input while it asks: same as answer
		{"L/p", "agents.answer", "L/n", false},  // own child, no flag
		{"L/p", "agents.input", "L/n", false},   // input while it waits, no flag
		{"L/p", "agents.answer", "L/g", false},  // grandchild
		{"L/p", "agents.answer", "L/x", false},  // someone else's
		{"L/p", "agents.answer", "L/xc", false}, // someone else's child
		{"L/c", "agents.answer", "L/c", false},  // itself
		{"L/c", "agents.answer", "L/p", false},  // its parent
		{"L/p", "agents.input", "L/g", true},    // a working descendant
		{"L/p", "agents.stop", "L/g", true},
		{"L/p", "agents.close", "M/r", true},
		{"L/p", "agents.stop", "L/p", false}, // itself
		{"L/c", "agents.stop", "L/p", false}, // its parent
		{"L/c", "agents.rename", "L/n", false},
		{"L/p", "agents.kill", "L/x", false},
		{"L/p", "agents.move", "L/xc", false},
		{"L/p", "agents.list", "L/x", true},   // read only
		{"L/p", "agents.result", "L/x", true}, // read only
		{"L/p", "agents.stop", "L/gone", true},
	}
	for _, c := range cases {
		err := s.authorize(c.caller, c.method, c.target, tree)
		if (err == nil) != c.ok {
			t.Errorf("%s %s %s: %v, want ok=%v", c.caller, c.method, c.target, err, c.ok)
		}
	}
	if !tree.descends("L/g", "L/p") || tree.descends("L/p", "L/g") || tree.descends("L/x", "L/p") {
		t.Error("descends")
	}
	if n := tree.liveChildren("L/p"); n != 3 {
		t.Errorf("live children %d", n)
	}
	if got := withoutCaller(json.RawMessage(`{"id":"L/x","caller":"L/p"}`)); string(got) != `{"id":"L/x"}` {
		t.Errorf("withoutCaller %s", got)
	}
	// A treeView with a loop never hangs.
	loop := treeView{"L/a": {ID: "L/a", Parent: "L/b"}, "L/b": {ID: "L/b", Parent: "L/a"}}
	if loop.descends("L/a", "L/z") {
		t.Error("loop")
	}
}
