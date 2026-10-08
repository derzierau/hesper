package transport_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/internal/handoff"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

func wireCode(err error) string {
	if we, ok := err.(*wire.Error); ok {
		return we.Code
	}
	return ""
}

// A state change on M reaches L's subscription in about one network leg
// (the link pushes it), well inside 100 ms + a round trip.
func TestRemoteEventsArrivePromptly(t *testing.T) {
	leg := 20 * time.Millisecond // round trip of each client ↔ relay leg
	w := newWorld(t, worldOptions{delay: leg / 2})
	w.L.waitLinked(t, "M")
	var a wire.Agent
	if err := w.M.call(t, "agents.spawn", wire.SpawnParams{Project: w.M.project, Task: "idle along"}, &a); err != nil {
		t.Fatal(err)
	}
	remoteID := localOn(a.ID, "M")
	w.L.waitAgent(t, remoteID, inState(wire.StateDone))
	sub := w.L.client(t)
	if err := sub.Call(w.ctx, "agents.subscribe", nil, nil); err != nil {
		t.Fatal(err)
	}
	var took []time.Duration
	m := w.M.client(t)
	for i := 0; i < 15; i++ {
		name := "name-" + string(rune('a'+i))
		started := time.Now()
		if err := m.Call(w.ctx, "agents.rename", wire.RenameParams{ID: a.ID, Name: name}, nil); err != nil {
			t.Fatal(err)
		}
		for arrived := false; !arrived; {
			select {
			case n := <-sub.Notifications():
				var c wire.Changed
				json.Unmarshal(n.Params, &c)
				arrived = c.Agent.ID == remoteID && c.Agent.Name == name
			case <-time.After(5 * time.Second):
				t.Fatalf("change %d never arrived", i)
			}
		}
		took = append(took, time.Since(started))
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	t.Logf("state change on M → L's subscription, relay legs %s: p50 %s, max %s", leg, took[len(took)/2], took[len(took)-1])
	if p50 := took[len(took)/2]; p50 > 100*time.Millisecond+2*leg {
		t.Fatalf("p50 %s exceeds 100 ms + RTT", p50)
	}
}

// Device approval gates the host's agents: an unapproved controller gets
// nothing; an observing one may look but not type, start or answer; a
// shell needs the shell right (and Touch ID, which the software keys
// stand in for).
func TestRemoteDeviceApprovalGates(t *testing.T) {
	t.Run("unapproved", func(t *testing.T) {
		w := newWorld(t, worldOptions{stranger: true})
		var a wire.Agent
		if err := w.M.call(t, "agents.spawn", wire.SpawnParams{Project: w.M.project, Task: "SECRET-UNSEEN"}, &a); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if route := w.L.d.Fleet.Route("M"); route != "" {
			t.Fatalf("an unapproved device has a link (%s)", route)
		}
		var list []wire.Agent
		w.L.call(t, "agents.list", nil, &list)
		if len(list) != 0 {
			t.Fatalf("an unapproved device sees %+v", list)
		}
		err := w.L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Project: w.M.project, Task: "x"}, nil)
		if code := wireCode(err); code != wire.CodeForbidden {
			t.Fatalf("spawn from an unapproved device: %v", err)
		}
		if w.tap.sawAny("SECRET-UNSEEN") != "" {
			t.Fatal("the relay saw the task")
		}
	})
	t.Run("observer", func(t *testing.T) {
		w := newWorld(t, worldOptions{shell: true, rights: []string{"observe"}})
		w.L.waitLinked(t, "M")
		var a wire.Agent
		if err := w.M.call(t, "agents.spawn", wire.SpawnParams{Project: w.M.project, Task: "please approve"}, &a); err != nil {
			t.Fatal(err)
		}
		id := localOn(a.ID, "M")
		w.L.waitAgent(t, id, inState(wire.StateApproval))
		ro := attach(t, w.L, wire.AttachRequest{Attach: id, Mode: wire.ModeRO})
		readUntil(t, ro, "git push")
		for method, params := range map[string]any{
			"agents.answer": wire.AnswerParams{ID: id, Decision: wire.Allow},
			"agents.input":  wire.InputParams{ID: id, Text: "x"},
			"agents.spawn":  wire.SpawnParams{Machine: "M", Project: w.M.project, Task: "x"},
			"agents.stop":   wire.IDParams{ID: id},
		} {
			if err := w.L.call(t, method, params, nil); wireCode(err) != wire.CodeForbidden {
				t.Errorf("%s by an observer: %v", method, err)
			}
		}
		if _, err := wire.Attach(w.ctx, w.L.socket, wire.AttachRequest{Attach: id, Mode: wire.ModeRW}); wireCode(err) != wire.CodeForbidden {
			t.Fatalf("rw attach by an observer: %v", err)
		}
	})
	t.Run("shell right", func(t *testing.T) {
		w := newWorld(t, worldOptions{shell: true, rights: []string{"observe", "answer", "type", "transfer"}})
		w.L.waitLinked(t, "M")
		err := w.L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Kind: "shell", Project: w.M.project}, nil)
		if wireCode(err) != wire.CodeForbidden {
			t.Fatalf("shell without the shell right: %v", err)
		}
		// A shell started on M itself stays invisible to L.
		var a wire.Agent
		if err := w.M.call(t, "agents.spawn", wire.SpawnParams{Project: w.M.project, Kind: "shell"}, &a); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		var list []wire.Agent
		w.L.call(t, "agents.list", nil, &list)
		for _, x := range list {
			if x.Kind == wire.KindShell {
				t.Fatalf("L sees a shell it may not use: %+v", x)
			}
		}
	})
	t.Run("shells off", func(t *testing.T) {
		w := newWorld(t, worldOptions{})
		w.L.waitLinked(t, "M")
		err := w.L.call(t, "agents.spawn", wire.SpawnParams{Machine: "M", Kind: "shell", Project: w.M.project}, nil)
		if wireCode(err) != wire.CodeForbidden || !strings.Contains(err.Error(), "allow-shell") {
			t.Fatalf("shell on a machine without --allow-shell: %v", err)
		}
	})
}

// On one network the link runs on the direct path; when it breaks, the
// relay carries on, attaches included.
func TestRemoteDirectPathAndFallback(t *testing.T) {
	w := newWorld(t, worldOptions{direct: true, shell: true})
	a := spawnShell(t, w)
	deadline := time.Now().Add(15 * time.Second)
	for w.L.d.Fleet.Route("M") != "direct" {
		if time.Now().After(deadline) {
			t.Fatalf("the link stayed on %q", w.L.d.Fleet.Route("M"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	var hello wire.HelloResult
	w.L.call(t, "hello", nil, &hello)
	if hello.Machines[1].Route != "direct" {
		t.Fatalf("hello %+v", hello.Machines)
	}
	streams := w.tap.streams.Load()
	rw := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRW})
	rw.Input([]byte("OVER-DIRECT\r"))
	readUntil(t, rw, "ran OVER-DIRECT")
	if w.tap.sawAny("OVER-DIRECT") != "" || w.tap.streams.Load() != streams {
		t.Fatal("the direct path's traffic went through the relay")
	}
	// The direct path breaks (the listener goes away).
	w.M.d.Runner.Direct.Stop()
	readUntil(t, rw, "$") // the reattach's redraw
	rw.Input([]byte("OVER-RELAY\r"))
	readUntil(t, rw, "ran OVER-RELAY")
	if route := w.L.d.Fleet.Route("M"); route != "relay" {
		t.Fatalf("route %q after the direct path broke", route)
	}
	if w.tap.sawAny("OVER-RELAY") != "" {
		t.Fatal("the relay saw the typing")
	}
}

// After the relay restarts (every socket dropped), both machines come
// back, the link too, and an attach carries on with a fresh screen.
func TestRemoteReconnectAfterRelayRestart(t *testing.T) {
	w := newWorld(t, worldOptions{shell: true})
	a := spawnShell(t, w)
	rw := attach(t, w.L, wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRW})
	readUntil(t, rw, "$")
	w.tap.dropAll()
	time.Sleep(200 * time.Millisecond)
	w.L.waitLinked(t, "M")
	readUntil(t, rw, "$")
	ok := false
	for i := 0; i < 50 && !ok; i++ {
		rw.Input([]byte("AFTER-RESTART\r"))
		done := make(chan bool, 1)
		go func() {
			defer func() { recover() }()
			readUntil(t, rw, "ran AFTER-RESTART")
			done <- true
		}()
		select {
		case ok = <-done:
		case <-time.After(500 * time.Millisecond):
		}
	}
	if !ok {
		t.Fatal("typing after the relay restart never arrived")
	}
	w.L.waitAgent(t, a.ID, inState(wire.StateIdle))
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// agents.move: L's agent goes to M with its conversation and its
// uncommitted work and resumes there; then back to L (a remote source).
func TestRemoteMoveWithConversation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	w := newWorld(t, worldOptions{})
	L, M := w.L, w.M
	git(t, L.project, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(L.project, "a.txt"), []byte("one\n"), 0o644)
	git(t, L.project, "add", ".")
	git(t, L.project, "commit", "-q", "-m", "first")
	L.waitLinked(t, "M")
	var a wire.Agent
	if err := L.call(t, "agents.spawn", wire.SpawnParams{Project: L.project, Task: "SECRET-MOVE-TASK"}, &a); err != nil {
		t.Fatal(err)
	}
	L.waitAgent(t, a.ID, inState(wire.StateDone))
	os.WriteFile(filepath.Join(L.project, "a.txt"), []byte("one\nUNCOMMITTED\n"), 0o644)
	transcript := filepath.Join(L.home, ".claude", "projects", handoff.ClaudeSlug(L.project), a.SessionID+".jsonl")
	os.MkdirAll(filepath.Dir(transcript), 0o700)
	os.WriteFile(transcript, []byte(`{"cwd":"`+L.project+`","message":"SECRET-CONVERSATION"}`+"\n"), 0o600)

	var moved wire.Agent
	if err := L.call(t, "agents.move", wire.MoveParams{ID: a.ID, To: "M"}, &moved); err != nil {
		t.Fatal(err)
	}
	if moved.ID != localOn(a.ID, "M") || moved.Project != M.project || moved.SessionID != a.SessionID {
		t.Fatalf("moved %+v", moved)
	}
	L.waitAgent(t, moved.ID, func(x wire.Agent) bool { return x.Exit == nil })
	var list []wire.Agent
	L.call(t, "agents.list", nil, &list)
	for _, x := range list {
		if x.ID == a.ID {
			t.Fatal("the source still lists the moved agent")
		}
	}
	placed := filepath.Join(M.home, ".claude", "projects", handoff.ClaudeSlug(M.project), a.SessionID+".jsonl")
	data, err := os.ReadFile(placed)
	if err != nil || !strings.Contains(string(data), `"cwd":"`+M.project+`"`) {
		t.Fatalf("transcript on M: %s %v", data, err)
	}
	if got, _ := os.ReadFile(filepath.Join(M.project, "a.txt")); string(got) != "one\nUNCOMMITTED\n" {
		t.Fatalf("work on M: %q", got)
	}
	for end := time.Now().Add(5 * time.Second); !strings.Contains(M.argvs(moved.ID), "--resume") && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(M.argvs(moved.ID), `"--resume","`+a.SessionID+`"`) {
		t.Fatalf("not resumed on M: %s", M.argvs(moved.ID))
	}
	// And back, the source now remote.
	os.WriteFile(filepath.Join(M.project, "b.txt"), []byte("from M\n"), 0o644)
	git(t, L.project, "checkout", "-q", "--", ".")
	var back wire.Agent
	if err := L.call(t, "agents.move", wire.MoveParams{ID: moved.ID, To: "L"}, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != a.ID {
		t.Fatalf("back %+v", back)
	}
	if got, _ := os.ReadFile(filepath.Join(L.project, "b.txt")); string(got) != "from M\n" {
		t.Fatalf("work back on L: %q", got)
	}
	L.waitAgent(t, a.ID, func(x wire.Agent) bool { return x.Exit == nil })
	if leaked := w.tap.sawAny("SECRET-CONVERSATION", "SECRET-MOVE-TASK", "UNCOMMITTED"); leaked != "" {
		t.Fatalf("the relay saw %q", leaked)
	}
}
