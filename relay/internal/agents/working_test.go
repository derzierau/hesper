package agents

import (
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Working: whether an agent works (the history's indexer slows down).
func TestWorking(t *testing.T) {
	h := newHarness(t)
	if h.reg.Working() {
		t.Fatal("working without agents")
	}
	a := h.spawn(wire.SpawnParams{Task: "keep working"})
	h.waitState(a.ID, wire.StateWorking)
	if !h.reg.Working() {
		t.Fatal("an agent works")
	}
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "x"}, nil)
	h.waitState(a.ID, wire.StateDone)
	if h.reg.Working() {
		t.Fatal("working after the turn")
	}
}
