package agents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/wire"
)

// A subscribed connection that goes away ends its watchers at once
// (agents, drafts, projects, sessions), without waiting for a change to
// make a write fail: control closes done before it waits for them.
func TestSubscriberWatchersEndWithTheConnection(t *testing.T) {
	h := newHarness(t)
	counts := func() (agents, drafts int) {
		h.reg.mu.Lock()
		agents = len(h.reg.subs)
		h.reg.mu.Unlock()
		h.srv.drafts.mu.Lock()
		drafts = len(h.srv.drafts.subs)
		h.srv.drafts.mu.Unlock()
		return
	}
	for round := 0; round < 3; round++ {
		c, err := wire.Dial(context.Background(), h.sock)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := c.Call(ctx, "agents.subscribe", nil, nil); err != nil {
			t.Fatal(err)
		}
		cancel()
		waitFor(t, func() bool { a, d := counts(); return a == 1 && d == 1 })
		c.Close()
		deadline := time.Now().Add(2 * time.Second)
		for {
			a, d := counts()
			if a == 0 && d == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("round %d: %d agent and %d draft watchers still running after the connection closed", round, a, d)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// Serve after Close (a server closed before its Serve goroutine ran)
// returns at once and closes the listener, so the socket is free for the
// next hesperd instead of answering as if one were still running.
func TestServeAfterCloseStops(t *testing.T) {
	h := newHarness(t)
	dir, err := os.MkdirTemp("/tmp", "sv") // short: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s", "hesperd.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Open(h.opt)
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	defer reg.Close()
	srv := NewServer(reg)
	srv.Close()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		ln.Close()
		t.Fatal("Serve kept accepting after Close")
	}
	if ln, err := Listen(sock); err != nil {
		t.Fatalf("the socket is still taken: %v", err)
	} else {
		ln.Close()
	}
}
