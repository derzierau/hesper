package transport_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Closing agents on another Mac: agents.background, agents.kill and
// agents.close through L reach M; M's removal reaches L's subscription
// with its reason; with M gone, they fail with "offline" (-32010).
func TestRemoteCloseKillBackground(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	sub := L.client(t)
	if err := sub.Call(w.ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	removed := func(id string) string {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for {
			select {
			case n := <-sub.Notifications():
				var r wire.Removed
				if n.Method == wire.NoteRemoved && json.Unmarshal(n.Params, &r) == nil && r.ID == id {
					return r.Reason
				}
			case <-deadline:
				t.Fatalf("no agents.removed for %s on L", id)
			}
		}
	}
	spawn := func(task string) wire.Agent {
		t.Helper()
		var a wire.Agent
		if err := L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Project: M.project, Task: task}, &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	// Background: the flag shows on L; a turn that ends there closes it.
	a := spawn("please approve the push")
	L.waitAgent(t, a.ID, inState(wire.StateApproval))
	if err := L.call(t, "agents.background", wire.BackgroundParams{ID: a.ID, Background: true}, nil); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, a.ID, func(x wire.Agent) bool { return x.Background })
	if err := L.call(t, "agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatal(err)
	}
	if reason := removed(a.ID); reason != wire.ReasonFinishedInBackground {
		t.Fatalf("background reason %q", reason)
	}
	// Kill: stays on L, exited, killed.
	b := spawn("please approve it")
	L.waitAgent(t, b.ID, inState(wire.StateApproval))
	if err := L.call(t, "agents.kill", wire.IDParams{ID: b.ID}, nil); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, b.ID, func(x wire.Agent) bool { return x.State == wire.StateExited && x.Ended == wire.EndedKilled })
	// Close: the session in the answer, removed with reason "closed".
	c := spawn("please approve this one")
	L.waitAgent(t, c.ID, inState(wire.StateApproval))
	var res wire.CloseResult
	if err := L.call(t, "agents.close", wire.IDParams{ID: c.ID}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Session == "" || res.Session != c.SessionID {
		t.Fatalf("close result %+v (session %s)", res, c.SessionID)
	}
	if reason := removed(c.ID); reason != wire.ReasonClosed {
		t.Fatalf("close reason %q", reason)
	}
	// Closing the killed (ended) one.
	if err := L.call(t, "agents.close", wire.IDParams{ID: b.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if reason := removed(b.ID); reason != wire.ReasonClosed {
		t.Fatalf("close of ended reason %q", reason)
	}
	// An unknown agent there: not found.
	if err := L.call(t, "agents.close", wire.IDParams{ID: "M/nope00"}, nil); err == nil || err.(*wire.Error).Code != wire.CodeNotFound {
		t.Fatalf("unknown: %v", err)
	}
	// M goes away: offline.
	d := spawn("small job")
	L.waitAgent(t, d.ID, inState(wire.StateDone))
	M.stop()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var hello wire.HelloResult
		L.call(t, "hello", wire.HelloParams{Client: "test"}, &hello)
		if len(hello.Machines) == 2 && !hello.Machines[1].Online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("M still online: %+v", hello)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, method := range []string{"agents.close", "agents.kill", "agents.background"} {
		err := L.call(t, method, wire.BackgroundParams{ID: d.ID, Background: true}, nil)
		if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeOffline || !strings.Contains(we.Message, "offline") {
			t.Fatalf("%s with M offline: %v", method, err)
		}
	}
}
