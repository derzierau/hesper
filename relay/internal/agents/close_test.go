package agents

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// subscribed is a control connection subscribed to agents.*.
func (h *harness) subscribed() *wire.Client {
	h.t.Helper()
	c := h.client()
	if err := c.Call(h.t.Context(), "agents.subscribe", nil, nil); err != nil {
		h.t.Fatal(err)
	}
	return c
}

// waitRemoved waits for agents.removed of id and returns its reason;
// changes of id seen on the way go to changed (may be nil).
func waitRemoved(t *testing.T, c *wire.Client, id string, changed func(wire.Agent)) string {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case n := <-c.Notifications():
			switch n.Method {
			case wire.NoteRemoved:
				var r wire.Removed
				json.Unmarshal(n.Params, &r)
				if r.ID == id {
					return r.Reason
				}
			case wire.NoteChanged:
				var ch wire.Changed
				json.Unmarshal(n.Params, &ch)
				if ch.Agent.ID == id && changed != nil {
					changed(ch.Agent)
				}
			}
		case <-deadline:
			t.Fatalf("no agents.removed for %s", id)
		}
	}
}

func TestCloseRunningAgentGracefully(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "approve it"})
	h.waitState(a.ID, wire.StateApproval)
	sub := h.subscribed()
	var res wire.CloseResult
	if err := h.call("agents.close", wire.IDParams{ID: a.ID}, &res); err != nil {
		t.Fatal(err)
	}
	if res.Session == "" || res.Session != a.SessionID {
		t.Fatalf("close result %+v, want session %s", res, a.SessionID)
	}
	// The tool's interrupt first (Esc declines the approval), then the
	// hangup: it leaves with reason "closed".
	h.waitKeys(a.ID, "\x1b")
	if reason := waitRemoved(t, sub, a.ID, nil); reason != wire.ReasonClosed {
		t.Fatalf("reason %q", reason)
	}
	if _, err := h.reg.Get(a.ID); err == nil {
		t.Fatal("still listed")
	}
	// A second close of a closed agent: not found.
	if err := h.call("agents.close", wire.IDParams{ID: a.ID}, nil); err == nil || err.(*wire.Error).Code != wire.CodeNotFound {
		t.Fatalf("second close: %v", err)
	}
	// Gone from agents.json too (not respawned by the next daemon).
	h.close()
	data, _ := os.ReadFile(filepath.Join(h.state, "agents.json"))
	if strings.Contains(string(data), a.ID) {
		t.Fatalf("agents.json still has it: %s", data)
	}
}

func TestCloseShell(t *testing.T) {
	// A shell (always idle to the daemon) gets ^C and is ended at once.
	h := newHarness(t)
	sh := h.spawn(wire.SpawnParams{Kind: "shell"})
	h.waitState(sh.ID, wire.StateIdle)
	sub := h.subscribed()
	var res wire.CloseResult
	if err := h.call("agents.close", wire.IDParams{ID: sh.ID}, &res); err != nil || res.Session != "" {
		t.Fatalf("close %+v %v", res, err)
	}
	if reason := waitRemoved(t, sub, sh.ID, nil); reason != wire.ReasonClosed {
		t.Fatalf("reason %q", reason)
	}
}

func TestCloseEndedAgent(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(a.ID, wire.StateDone)
	h.call("agents.stop", wire.IDParams{ID: a.ID}, nil)
	h.waitState(a.ID, wire.StateExited)
	sub := h.subscribed()
	var res wire.CloseResult
	if err := h.call("agents.close", wire.IDParams{ID: a.ID}, &res); err != nil || res.Session != a.SessionID {
		t.Fatalf("close %+v %v", res, err)
	}
	// Removed before the answer.
	if _, err := h.reg.Get(a.ID); err == nil {
		t.Fatal("still listed")
	}
	if reason := waitRemoved(t, sub, a.ID, nil); reason != wire.ReasonClosed {
		t.Fatalf("reason %q", reason)
	}
}

func TestKillKeepsAgentKilled(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "approve it"})
	h.waitState(a.ID, wire.StateApproval)
	if err := h.call("agents.kill", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	x := h.waitState(a.ID, wire.StateExited)
	if x.Ended != wire.EndedKilled || x.Exit == nil || x.Exit.Signal != "SIGTERM" || x.Attention != nil {
		t.Fatalf("killed %+v exit %+v", x, x.Exit)
	}
	// Listed as killed; a kill of an ended agent does nothing.
	var list []wire.Agent
	h.call("agents.list", nil, &list)
	if len(list) != 1 || list[0].Ended != wire.EndedKilled {
		t.Fatalf("list %+v", list)
	}
	if err := h.call("agents.kill", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	// Resumed, it is no longer "killed".
	var r wire.Agent
	if err := h.call("agents.resume", wire.IDParams{ID: a.ID}, &r); err != nil || r.Ended != "" {
		t.Fatalf("resume %+v %v", r, err)
	}
}

func TestBackgroundPersistsAcrossRestart(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(a.ID, wire.StateDone)
	sub := h.subscribed()
	if err := h.call("agents.background", wire.BackgroundParams{ID: a.ID, Background: true}, nil); err != nil {
		t.Fatal(err)
	}
	// agents.changed carries it.
	deadline := time.After(10 * time.Second)
	for seen := false; !seen; {
		select {
		case n := <-sub.Notifications():
			var ch wire.Changed
			if n.Method == wire.NoteChanged && json.Unmarshal(n.Params, &ch) == nil && ch.Agent.ID == a.ID && ch.Agent.Background {
				seen = true
			}
		case <-deadline:
			t.Fatal("no agents.changed with background")
		}
	}
	// Setting it never touches the process.
	got := h.waitState(a.ID, wire.StateDone)
	if got.PID != a.PID || got.Exit != nil {
		t.Fatalf("process touched: %+v", got)
	}
	h.close()
	data, _ := os.ReadFile(filepath.Join(h.state, "agents.json"))
	if !strings.Contains(string(data), `"background": true`) {
		t.Fatalf("agents.json: %s", data)
	}
	h.open()
	// Respawned with resume (starting → idle is no finished turn): still
	// there, still in the background.
	back := h.waitState(a.ID, wire.StateIdle)
	if !back.Background {
		t.Fatalf("after restart %+v", back)
	}
	var list []wire.Agent
	h.call("agents.list", nil, &list)
	if len(list) != 1 || !list[0].Background {
		t.Fatalf("list %+v", list)
	}
	// And back to the wall.
	if err := h.call("agents.background", wire.BackgroundParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.reg.Get(a.ID); got.Background {
		t.Fatal("still in the background")
	}
}

func TestBackgroundAgentFinishing(t *testing.T) {
	h := newHarness(t)
	sub := h.subscribed()
	// A turn that ends in the background closes it.
	a := h.spawn(wire.SpawnParams{Task: "approve it"})
	h.waitState(a.ID, wire.StateApproval)
	if err := h.call("agents.background", wire.BackgroundParams{ID: a.ID, Background: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.call("agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatal(err)
	}
	if reason := waitRemoved(t, sub, a.ID, nil); reason != wire.ReasonFinishedInBackground {
		t.Fatalf("reason %q", reason)
	}
	// One whose process ends on its own, too.
	b := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(b.ID, wire.StateDone)
	h.call("agents.background", wire.BackgroundParams{ID: b.ID, Background: true}, nil)
	h.call("agents.input", wire.InputParams{ID: b.ID, Text: "exit", Submit: true}, nil)
	if reason := waitRemoved(t, sub, b.ID, nil); reason != wire.ReasonFinishedInBackground {
		t.Fatalf("reason %q", reason)
	}
	// A killed background agent stays (killed); a failed one stays (an
	// error to look at).
	c := h.spawn(wire.SpawnParams{Task: "approve it"})
	h.waitState(c.ID, wire.StateApproval)
	h.call("agents.background", wire.BackgroundParams{ID: c.ID, Background: true}, nil)
	h.call("agents.kill", wire.IDParams{ID: c.ID}, nil)
	if x := h.waitState(c.ID, wire.StateExited); x.Ended != wire.EndedKilled || !x.Background {
		t.Fatalf("killed in the background %+v", x)
	}
	d := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(d.ID, wire.StateDone)
	h.call("agents.background", wire.BackgroundParams{ID: d.ID, Background: true}, nil)
	h.call("agents.input", wire.InputParams{ID: d.ID, Text: "crash\r"}, nil)
	h.waitState(d.ID, wire.StateError)
}

// TestClosingAgentsErrors: unknown agents, bad params, and another
// machine that cannot be reached: -32010, data.code "offline".
func TestClosingAgentsErrors(t *testing.T) {
	h := newHarness(t)
	for _, method := range []string{"agents.close", "agents.kill", "agents.background"} {
		err := h.call(method, wire.BackgroundParams{ID: "L/nope00"}, nil)
		if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeNotFound {
			t.Fatalf("%s unknown: %v", method, err)
		}
		err = h.call(method, wire.BackgroundParams{}, nil)
		if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeInvalid {
			t.Fatalf("%s without id: %v", method, err)
		}
		// The raw answer: its JSON-RPC number.
		conn, err := net.Dial("unix", h.sock)
		if err != nil {
			t.Fatal(err)
		}
		conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{"id":"M/abcdef","background":true}}` + "\n"))
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		var resp wire.Response
		json.Unmarshal(line, &resp)
		if resp.Error == nil || resp.Error.Code != -32010 || resp.Error.Data == nil || resp.Error.Data.Code != wire.CodeOffline {
			t.Fatalf("%s offline: %s", method, line)
		}
	}
	// agents.stop keeps its "unavailable".
	err := h.call("agents.stop", wire.IDParams{ID: "M/abcdef"}, nil)
	if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeUnavailable {
		t.Fatalf("stop remote: %v", err)
	}
}

func TestRemoveReason(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(a.ID, wire.StateDone)
	h.call("agents.stop", wire.IDParams{ID: a.ID}, nil)
	h.waitState(a.ID, wire.StateExited)
	sub := h.subscribed()
	if err := h.call("agents.remove", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if reason := waitRemoved(t, sub, a.ID, nil); reason != wire.ReasonRemoved {
		t.Fatalf("reason %q", reason)
	}
}

// TestClosingNotRestored: an agent being closed when the daemon stops is
// not respawned.
func TestClosingNotRestored(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Task: "small job"})
	h.waitState(a.ID, wire.StateDone)
	h.reg.mu.Lock()
	h.reg.agents[strings.TrimPrefix(a.ID, "L/")].closeReason = wire.ReasonClosed
	h.reg.mu.Unlock()
	h.close()
	h.open()
	if _, err := h.reg.Get(a.ID); err == nil {
		t.Fatal("restored")
	}
}
