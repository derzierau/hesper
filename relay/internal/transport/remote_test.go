package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// Spawned on M through L's socket, the agent is listed by L with M's
// prefix, its states arrive on L's subscription, an answer through L
// reaches it, and the relay never sees its task, name, attention text or
// screen.
func TestRemoteSpawnStatesAnswerDone(t *testing.T) {
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	L.waitLinked(t, "M")
	var hello wire.HelloResult
	if err := L.call(t, "hello", wire.HelloParams{Client: "test"}, &hello); err != nil {
		t.Fatal(err)
	}
	if len(hello.Machines) != 2 || hello.Machines[0].Short != "L" || hello.Machines[1].Short != "M" || !hello.Machines[1].Online || hello.Machines[1].Route != "relay" {
		t.Fatalf("hello %+v", hello)
	}
	sub := L.client(t)
	if err := sub.Call(w.ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	var a wire.Agent
	task := "SECRET-TASK-7731 please approve the push"
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Project: M.project, Task: task}, &a); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.ID, "M/") || a.Machine != "M" || a.Task != task {
		t.Fatalf("spawned %+v", a)
	}
	// The approval, with its detail, through L.
	got := L.waitAgent(t, a.ID, inState(wire.StateApproval))
	if got.Attention == nil || got.Attention.Detail != "git push origin main" {
		t.Fatalf("attention %+v", got.Attention)
	}
	// L's subscription saw the agent's states, with M's prefix.
	seen := map[string]bool{}
	deadline := time.After(10 * time.Second)
	for !seen[wire.StateApproval] {
		select {
		case n := <-sub.Notifications():
			if n.Method == wire.NoteChanged {
				var c wire.Changed
				decode(t, n.Params, &c)
				if c.Agent.ID == a.ID {
					seen[c.Agent.State] = true
				}
			}
		case <-deadline:
			t.Fatalf("subscription saw %v", seen)
		}
	}
	if err := L.call(t, "agents.answer", wire.AnswerParams{ID: a.ID, Decision: wire.Allow}, nil); err != nil {
		t.Fatal(err)
	}
	done := L.waitAgent(t, a.ID, inState(wire.StateDone))
	if done.Summary != "Opened PR #482 (draft)" {
		t.Fatalf("summary %q", done.Summary)
	}
	// Typing a prompt, renaming, stopping and resuming through L.
	if err := L.call(t, "agents.input", wire.InputParams{ID: a.ID, Text: "SECRET-PROMPT-1234", Submit: true}, nil); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, a.ID, func(x wire.Agent) bool { return x.State == wire.StateDone && x.Summary == "Done: SECRET-PROMPT-1234" })
	var renamed wire.Agent
	if err := L.call(t, "agents.rename", wire.RenameParams{ID: a.ID, Name: "SECRET-NAME-55"}, &renamed); err != nil || renamed.ID != a.ID {
		t.Fatalf("rename %+v %v", renamed, err)
	}
	if err := L.call(t, "agents.stop", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, a.ID, inState(wire.StateExited))
	var resumed wire.Agent
	if err := L.call(t, "agents.resume", wire.IDParams{ID: a.ID}, &resumed); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, a.ID, func(x wire.Agent) bool { return x.Exit == nil && x.Name == "SECRET-NAME-55" })
	for end := time.Now().Add(5 * time.Second); !strings.Contains(M.argvs(a.ID), "--resume") && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(M.argvs(a.ID), "--resume") {
		t.Fatalf("not resumed: %s", M.argvs(a.ID))
	}
	// Errors keep their codes.
	err := L.call(t, "agents.stop", wire.IDParams{ID: "M/nope00"}, nil)
	if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeNotFound {
		t.Fatalf("unknown remote agent: %v", err)
	}
	err = L.call(t, "agents.stop", wire.IDParams{ID: "Q/abc123"}, nil)
	if we, ok := err.(*wire.Error); !ok || we.Code != wire.CodeNotFound {
		t.Fatalf("unknown machine: %v", err)
	}
	// The relay saw none of it.
	if leaked := w.tap.sawAny("SECRET-TASK-7731", "SECRET-PROMPT-1234", "SECRET-NAME-55", "git push origin main", "fake claude", "please-approve"); leaked != "" {
		t.Fatalf("the relay saw %q", leaked)
	}
	if w.tap.count(`"method":"agents.`) != 0 {
		t.Fatal("an agents method went in plaintext")
	}
}

func decode(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// readUntil reads attach frames until DATA shows want; it returns the
// frame types seen.
func readUntil(t *testing.T, a *wire.AttachConn, want string) []byte {
	t.Helper()
	var screen bytes.Buffer
	var types []byte
	done := make(chan struct{})
	timer := time.AfterFunc(10*time.Second, func() {
		select {
		case <-done:
		default:
			a.Close()
		}
	})
	defer timer.Stop()
	defer close(done)
	for !strings.Contains(screen.String(), want) {
		typ, p, err := a.ReadFrame()
		if err != nil {
			t.Fatalf("waiting for %q: %v (saw %q)", want, err, tail(screen.String()))
		}
		types = append(types, typ)
		if typ == wire.FrameData {
			screen.Write(p)
		}
	}
	return types
}

func tail(s string) string {
	if len(s) > 300 {
		return s[len(s)-300:]
	}
	return s
}

func attach(t *testing.T, n *node, req wire.AttachRequest) *wire.AttachConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a, err := wire.Attach(ctx, n.socket, req)
	if err != nil {
		t.Fatalf("attach %+v: %v", req, err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}
