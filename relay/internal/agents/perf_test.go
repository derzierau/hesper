package agents

import (
	"syscall"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// 30 idle agents cost the daemon (next to) no CPU: no timers, no polling.
func TestIdleAgentsCostNoCPU(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	h := newHarness(t)
	var ids []string
	for range 30 {
		ids = append(ids, h.spawn(wire.SpawnParams{Kind: "shell"}).ID)
	}
	for _, id := range ids {
		h.waitScreen(id, "$")
	}
	time.Sleep(300 * time.Millisecond)
	var before, after syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	time.Sleep(2 * time.Second)
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	used := time.Duration(after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano())
	t.Logf("30 idle agents: %v CPU in 2 s", used)
	if used > 100*time.Millisecond {
		t.Fatalf("idle daemon used %v CPU in 2 s", used)
	}
}

// A hook call: what `hesperd hook` costs an agent, the process start aside.
func BenchmarkHookRoundTrip(b *testing.B) {
	h := newHarness(&testing.T{})
	defer h.close()
	payload := mustJSON(map[string]any{"session_id": "x", "tool_name": "Bash"})
	for b.Loop() {
		if err := wire.SendHook(h.sock, wire.HookParams{Agent: "L/zzzzzz", Source: "claude", Event: "PreToolUse", Payload: payload}, time.Second); err != nil {
			b.Fatal(err)
		}
	}
}
