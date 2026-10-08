package agents

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// states records the states (and activities) one agent goes through.
type states struct {
	mu   sync.Mutex
	seen []string
	acts []string
}

func (h *harness) recordStates(id string) *states {
	c := h.subscribed()
	s := &states{}
	go func() {
		for n := range c.Notifications() {
			if n.Method != wire.NoteChanged {
				continue
			}
			var ch wire.Changed
			if json.Unmarshal(n.Params, &ch) != nil || ch.Agent.ID != id {
				continue
			}
			s.mu.Lock()
			if len(s.seen) == 0 || s.seen[len(s.seen)-1] != ch.Agent.State {
				s.seen = append(s.seen, ch.Agent.State)
			}
			if ch.Agent.Activity != "" {
				s.acts = append(s.acts, ch.Agent.Activity)
			}
			s.mu.Unlock()
		}
	}()
	return s
}

func (s *states) get() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...), append([]string(nil), s.acts...)
}

// A shell started with a task: starting until its prompt is ready, then
// the task is typed after the prompt; working while the command runs
// (activity: the job), idle when it is done.
func TestShellTask(t *testing.T) {
	h := newHarness(t, "PS1=P> ", "ENV=")
	a := h.spawn(wire.SpawnParams{Profile: "real-shell", Task: "sleep 1; echo hesper-$((6*7))"})
	if a.State != wire.StateStarting {
		t.Fatalf("a shell with a task starts %q, want starting", a.State)
	}
	rec := h.recordStates(a.ID)
	h.waitState(a.ID, wire.StateWorking)
	h.waitScreen(a.ID, "hesper-42")
	h.waitState(a.ID, wire.StateIdle)
	seen, acts := rec.get()
	for deadline := time.Now().Add(5 * time.Second); (len(seen) == 0 || seen[len(seen)-1] != wire.StateIdle) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		seen, acts = rec.get()
	}
	if !contains(seen, wire.StateWorking) || seen[len(seen)-1] != wire.StateIdle {
		t.Fatalf("states %v", seen)
	}
	if !contains(acts, "sleep") {
		t.Errorf("activities %v, want the job (sleep)", acts)
	}
	if screen := h.screen(a.ID); !strings.Contains(screen, "P> sleep 1; echo") {
		t.Fatalf("the task is not typed after the prompt: %s", screen)
	}
	if got, _ := h.reg.Get(a.ID); got.Activity != "" {
		t.Errorf("idle shell keeps activity %q", got.Activity)
	}
}

// Without a task a shell is idle; a command sent with Enter makes it
// working until the shell is back at its prompt.
func TestShellSendWorking(t *testing.T) {
	h := newHarness(t, "PS1=P> ", "ENV=")
	a := h.spawn(wire.SpawnParams{Profile: "real-shell"})
	if a.State != wire.StateIdle {
		t.Fatalf("a shell without a task starts %q, want idle", a.State)
	}
	h.waitScreen(a.ID, "P>")
	if err := h.call("agents.input", wire.InputParams{ID: a.ID, Text: "sleep 1", Paste: true, Submit: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.reg.Get(a.ID); got.State != wire.StateWorking {
		t.Fatalf("after send: %q, want working", got.State)
	}
	start := time.Now()
	h.waitState(a.ID, wire.StateIdle)
	if d := time.Since(start); d < 800*time.Millisecond {
		t.Fatalf("idle after %v, while sleep 1 ran", d)
	}
	// A builtin with no job: done once the shell is quiet.
	h.call("agents.input", wire.InputParams{ID: a.ID, Text: "cd /", Submit: true}, nil)
	h.waitState(a.ID, wire.StateIdle)
}

// Text sent right after a shell starts comes after its prompt.
func TestShellInputWaitsForPrompt(t *testing.T) {
	h := newHarness(t, "PS1=P> ", "ENV=")
	a := h.spawn(wire.SpawnParams{Profile: "slow-shell"})
	if err := h.call("agents.input", wire.InputParams{ID: a.ID, Text: "echo early-$((1+1))", Submit: true}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitScreen(a.ID, "early-2")
	// The prompt (P> or the shell's own, sh-3.2$) first, then the echo.
	if screen := h.screen(a.ID); strings.HasPrefix(screen, "echo") || !strings.Contains(strings.SplitN(screen, " | ", 2)[0], " echo early") {
		t.Fatalf("input before the prompt: %s", screen)
	}
}

// A respawned shell does not run its task again.
func TestShellTaskNotRetyped(t *testing.T) {
	h := newHarness(t)
	a := h.spawn(wire.SpawnParams{Kind: "shell", Task: "first"})
	h.waitScreen(a.ID, "ran first")
	h.waitState(a.ID, wire.StateIdle)
	if err := h.call("agents.stop", wire.IDParams{ID: a.ID}, nil); err != nil {
		t.Fatal(err)
	}
	h.waitState(a.ID, wire.StateExited)
	var b wire.Agent
	if err := h.call("agents.resume", wire.IDParams{ID: a.ID}, &b); err != nil {
		t.Fatal(err)
	}
	if b.State != wire.StateIdle {
		t.Fatalf("resumed shell %q, want idle", b.State)
	}
	time.Sleep(shellQuiet + 3*shellPoll)
	if strings.Count(h.screen(a.ID), "ran first") > 0 {
		t.Fatalf("the task ran again: %s", h.screen(a.ID))
	}
}
