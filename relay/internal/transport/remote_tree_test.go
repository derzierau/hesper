package transport_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The agent tree across machines: an agent on L starts a child on M; L
// records the parent on M, enforces the policy before forwarding, and
// reads the child's result from M.
func TestRemoteAgentTree(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	as := func(caller, method string, params, result any) error {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		c := L.client(t)
		c.Caller = caller
		return c.Call(ctx, method, params, result)
	}
	var parent wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Project: L.project, Task: "small job"}, &parent); err != nil {
		t.Fatal(err)
	}
	var child, other wire.Agent
	if err := as(parent.ID, "agents.spawn", wire.SpawnParams{Machine: "M", Project: M.project, Task: "please approve the push", LetParentAnswer: true}, &child); err != nil {
		t.Fatal(err)
	}
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Project: M.project, Task: "please approve it"}, &other); err != nil {
		t.Fatal(err)
	}
	got := L.waitAgent(t, child.ID, inState(wire.StateApproval))
	if got.Parent != parent.ID || got.Depth != 1 || !got.LetParentAnswer {
		t.Fatalf("remote child on L %+v", got)
	}
	L.waitAgent(t, other.ID, inState(wire.StateApproval))
	err := as(parent.ID, "agents.answer", wire.AnswerParams{ID: other.ID, Decision: wire.Allow}, nil)
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.CodeForbidden {
		t.Fatalf("answering another agent's on M: %v", err)
	}
	if err := as(parent.ID, "agents.answer", wire.AnswerParams{ID: child.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatalf("answering its child on M: %v", err)
	}
	L.waitAgent(t, child.ID, inState(wire.StateDone))
	var res wire.AgentResult
	if err := L.call(t, "agents.result", wire.IDParams{ID: child.ID}, &res); err != nil {
		t.Fatal(err)
	}
	if res.ID != child.ID || res.Message != "Pushed.\n\nOpened PR #482 (draft)" {
		t.Fatalf("result %+v", res)
	}
	// Steering stays with descendants on M too.
	err = as(parent.ID, "agents.rename", wire.RenameParams{ID: other.ID, Name: "x"}, nil)
	if !errors.As(err, &we) || we.Code != wire.CodeForbidden {
		t.Fatalf("renaming another agent on M: %v", err)
	}
	if err := as(parent.ID, "agents.rename", wire.RenameParams{ID: child.ID, Name: "kid"}, nil); err != nil {
		t.Fatalf("renaming its child on M: %v", err)
	}
}
