package transport_test

import (
	"strings"
	"testing"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// agents.screen of another Mac's agent through L: M's own screen text.
func TestRemoteScreen(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	var a wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Project: M.project, Task: "please approve the push"}, &a); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, a.ID, inState(wire.StateApproval))
	var remote, local wire.ScreenResult
	if err := L.call(t, "agents.screen", wire.ScreenParams{ID: a.ID, Scrollback: 50}, &remote); err != nil {
		t.Fatal(err)
	}
	_, id, _ := strings.Cut(a.ID, "/")
	if err := M.call(t, "agents.screen", wire.ScreenParams{ID: "M/" + id, Scrollback: 50}, &local); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(remote.Text) == "" || remote.Text != local.Text || remote.Cols != local.Cols || remote.Rows != local.Rows || strings.Contains(remote.Text, "\x1b") {
		t.Fatalf("remote %+v\nlocal %+v", remote, local)
	}
	if err := L.call(t, "agents.screen", wire.ScreenParams{ID: "M/nope00"}, nil); err == nil || !strings.Contains(err.Error(), "nope00") {
		t.Fatalf("unknown remote agent: %v", err)
	}
}
