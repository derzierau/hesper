package agents

import (
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Loops in the registry (loopwork.go).

func TestSpawnServiceStaysInBackground(t *testing.T) {
	h := newHarness(t)
	if _, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project}, "nope"); err == nil {
		t.Fatal("an unknown service started")
	}
	a, err := h.reg.SpawnService(wire.SpawnParams{Project: h.project, Task: "approve it"}, wire.ServiceSteward)
	if err != nil {
		t.Fatal(err)
	}
	if a.Service != wire.ServiceSteward {
		t.Fatalf("service %q", a.Service)
	}
	h.waitState(a.ID, wire.StateApproval)
	if err := h.call("agents.background", wire.BackgroundParams{ID: a.ID, Background: true}, nil); err != nil {
		t.Fatal(err)
	}
	// Its turn ends in the background: it stays (finishedInBackground).
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitState(a.ID, wire.StateDone)
	// Its process ends on its own: it stays, exited (restarted by the
	// Steward).
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "exit", Submit: true}, nil)
	if got := h.waitState(a.ID, wire.StateExited); got.Service != wire.ServiceSteward {
		t.Fatalf("after exit %+v", got)
	}
	// Persisted.
	h.close()
	h.open()
	if got, err := h.reg.Get(a.ID); err != nil || got.Service != wire.ServiceSteward {
		t.Fatalf("after restart %+v %v", got, err)
	}
}
