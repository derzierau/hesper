package agents

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// fakeSessions starts every session start as a claude agent in the
// project (internal/sessions does the real thing), with the tree place
// the server put in the params.
type fakeSessions struct {
	h      *harness
	params []map[string]any
}

func (f *fakeSessions) Call(method string, params json.RawMessage) (any, error, bool) {
	var p struct {
		ID     string `json:"id"`
		Parent string `json:"parent"`
		Depth  int    `json:"depth"`
	}
	json.Unmarshal(params, &p)
	var m map[string]any
	json.Unmarshal(params, &m)
	f.params = append(f.params, m)
	switch method {
	case "sessions.resume", "sessions.fork", "sessions.continueAs":
		a, err := f.h.reg.SpawnSession(SessionSpawn{Kind: wire.KindClaude, SessionID: newUUID(), Dir: f.h.project, Name: p.ID,
			Fork: method == "sessions.fork", Parent: p.Parent, Depth: p.Depth})
		return a, err, true
	}
	return nil, nil, false
}

func (f *fakeSessions) Watch(<-chan struct{}, func(string, any) error) {}

// An agent's session starts (resume, fork, continue-as) make its
// children, within the limits; a person's have no parent, whatever the
// params say.
func TestAgentTreeSessionStarts(t *testing.T) {
	h := newHarness(t)
	allowed(t, "hello", h.call("hello", nil, nil))
	h.close()
	writeJSON(t, filepath.Join(h.config, "settings.json"), map[string]any{
		"defaults":         map[string]any{"kind": "claude", "kinds": map[string]string{"claude": "fake-claude", "codex": "fake-codex", "shell": "fake-shell"}},
		"maxAgentChildren": 2,
	})
	fake := &fakeSessions{h: h}
	h.opt.Sessions = fake
	h.open()
	root := h.spawn(wire.SpawnParams{Task: "small job"})
	var res wire.Agent
	allowed(t, "resume as an agent", h.callAs(root.ID, "sessions.resume", wire.SessionResumeParams{ID: "L:claude:a"}, &res))
	if got, _ := h.reg.Get(res.ID); got.Parent != root.ID || got.Depth != 1 || got.LetParentAnswer {
		t.Fatalf("resumed by an agent: %+v", got)
	}
	// The child is the agent's to steer.
	allowed(t, "rename the resumed child", h.callAs(root.ID, "agents.rename", wire.RenameParams{ID: res.ID, Name: "kid"}, nil))
	allowed(t, "continue as an agent", h.callAs(root.ID, "sessions.continueAs", wire.SessionContinueParams{ID: "L:claude:b", Kind: "codex"}, &res))
	err := h.callAs(root.ID, "sessions.fork", wire.SessionResumeParams{ID: "L:claude:c"}, &res)
	forbidden(t, "a third live child by fork", err)
	// A person: no parent, even when the params claim one.
	var person wire.Agent
	allowed(t, "fork as a person", h.call("sessions.fork", map[string]any{"id": "L:claude:d", "parent": root.ID, "depth": 1}, &person))
	if got, _ := h.reg.Get(person.ID); got.Parent != "" || got.Depth != 0 {
		t.Fatalf("a person's fork: %+v", got)
	}
	if last := fake.params[len(fake.params)-1]; last["parent"] != nil || last["depth"] != nil {
		t.Fatalf("a person's params reached the history with %v", last)
	}
	// Refusals and starts are audited.
	var refused, requested bool
	for _, l := range auditLines(t, h) {
		refused = refused || l["event"] == "agent.refused" && l["method"] == "sessions.fork"
		requested = requested || l["event"] == "agent.request" && l["method"] == "sessions.resume" && l["target"] != ""
	}
	if !refused || !requested {
		t.Fatalf("audit: refused %v, requested %v: %v", refused, requested, auditLines(t, h))
	}
}

// An agent gives files only to its descendants (files.put, and the
// chunks of an upload for another agent).
func TestAgentTreeFiles(t *testing.T) {
	h := newHarness(t)
	root := h.spawn(wire.SpawnParams{Task: "small job"})
	other := h.spawn(wire.SpawnParams{Task: "small job"})
	child, err := h.spawnAs(root.ID, wire.SpawnParams{Task: "small job"})
	allowed(t, "spawn", err)
	data := []byte("hello")
	var started FilePutResult
	allowed(t, "put for the child", h.callAs(root.ID, "files.put", FilePut{Agent: child.ID, Name: "a.txt", Size: 5, SHA256: sum(data)}, &started))
	allowed(t, "chunk for the child", h.callAs(root.ID, "files.chunk", FileChunkParams{Upload: started.Upload, Data: data, Last: true}, nil))
	forbidden(t, "put for another agent", h.callAs(root.ID, "files.put", FilePut{Agent: other.ID, Name: "a.txt", Size: 5, SHA256: sum(data)}, &started))
	forbidden(t, "put for itself", h.callAs(root.ID, "files.put", FilePut{Agent: root.ID, Name: "a.txt", Size: 5, SHA256: sum(data)}, &started))
	// A person's upload for another agent: its chunks are not the agent's to send.
	allowed(t, "a person's put", h.call("files.put", FilePut{Agent: other.ID, Name: "a.txt", Size: 5, SHA256: sum(data)}, &started))
	forbidden(t, "chunk of a person's upload", h.callAs(root.ID, "files.chunk", FileChunkParams{Upload: started.Upload, Data: data, Last: true}, nil))
	allowed(t, "the person's chunk", h.call("files.chunk", FileChunkParams{Upload: started.Upload, Data: data, Last: true}, nil))
}

// Closing an agent gives its children to its parent: the grandparent
// still steers them; depths follow; letParentAnswer goes.
func TestAgentTreeCloseAdoptsChildren(t *testing.T) {
	h := newHarness(t)
	root := h.spawn(wire.SpawnParams{Task: "small job"})
	mid, err := h.spawnAs(root.ID, wire.SpawnParams{Task: "small job"})
	allowed(t, "mid", err)
	leaf, err := h.spawnAs(mid.ID, wire.SpawnParams{Task: "small job", LetParentAnswer: true})
	allowed(t, "leaf", err)
	deep, err := h.spawnAs(leaf.ID, wire.SpawnParams{Task: "small job"})
	allowed(t, "deep", err)
	c := h.subscribed()
	allowed(t, "close mid", h.callAs(root.ID, "agents.close", wire.IDParams{ID: mid.ID}, nil))
	waitRemoved(t, c, mid.ID, nil)
	got, _ := h.reg.Get(leaf.ID)
	if got.Parent != root.ID || got.Depth != 1 || got.LetParentAnswer {
		t.Fatalf("leaf after its parent closed: %+v", got)
	}
	if d, _ := h.reg.Get(deep.ID); d.Parent != leaf.ID || d.Depth != 2 {
		t.Fatalf("deep after mid closed: %+v", d)
	}
	allowed(t, "the grandparent steers the leaf", h.callAs(root.ID, "agents.rename", wire.RenameParams{ID: leaf.ID, Name: "leaf"}, nil))
	allowed(t, "and its child", h.callAs(root.ID, "agents.rename", wire.RenameParams{ID: deep.ID, Name: "deep"}, nil))
	// Closing a root: its children become roots.
	allowed(t, "close root", h.call("agents.close", wire.IDParams{ID: root.ID}, nil))
	waitRemoved(t, c, root.ID, nil)
	if got, _ := h.reg.Get(leaf.ID); got.Parent != "" || got.Depth != 0 {
		t.Fatalf("leaf after the root closed: %+v", got)
	}
	if d, _ := h.reg.Get(deep.ID); d.Depth != 1 {
		t.Fatalf("deep after the root closed: %+v", d)
	}
	// It persists.
	h.close()
	h.open()
	if d, _ := h.reg.Get(deep.ID); d.Parent != leaf.ID || d.Depth != 1 {
		t.Fatalf("deep after a restart: %+v", d)
	}
}
