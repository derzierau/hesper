package host

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/session"
)

type snapshotProvider struct {
	mu    sync.Mutex
	state session.Snapshot
}

func (p *snapshotProvider) Snapshot(context.Context) (session.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state, nil
}

func (p *snapshotProvider) set(terminals ...session.Terminal) {
	p.mu.Lock()
	p.state = session.Snapshot{Terminals: terminals}
	p.mu.Unlock()
}

func TestNeedsAwake(t *testing.T) {
	cases := []struct {
		terminal session.Terminal
		want     bool
	}{
		{session.Terminal{State: "working"}, true},
		{session.Terminal{State: "approval", Attention: "approval"}, true},
		{session.Terminal{Attention: "question"}, true},
		{session.Terminal{State: "idle"}, false},
		{session.Terminal{Attention: "ready"}, false},
		{session.Terminal{Attention: "error"}, false},
		{session.Terminal{State: "working", Exited: true}, false},
	}
	for _, c := range cases {
		if got := NeedsAwake(session.Snapshot{Terminals: []session.Terminal{c.terminal}}); got != c.want {
			t.Errorf("%+v: got %v, want %v", c.terminal, got, c.want)
		}
	}
}

func TestKeepAwakeHoldsAndReleases(t *testing.T) {
	provider := &snapshotProvider{}
	provider.set(session.Terminal{State: "working"})
	var mu sync.Mutex
	held, battery := 0, 80
	k := &KeepAwake{
		Snapshot: provider.Snapshot, Interval: 5 * time.Millisecond, MinBattery: 20,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hold: func() (func(), error) {
			mu.Lock()
			held++
			mu.Unlock()
			return func() { mu.Lock(); held--; mu.Unlock() }, nil
		},
		Battery: func(context.Context) (int, bool, bool) { mu.Lock(); defer mu.Unlock(); return battery, true, true },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { k.Run(ctx); close(done) }()
	waitHeld := func(want int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			mu.Lock()
			got := held
			mu.Unlock()
			if got == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("held = %d, want %d", got, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitHeld(1)
	provider.set(session.Terminal{State: "idle"})
	waitHeld(0)
	provider.set(session.Terminal{Attention: "approval"})
	waitHeld(1)
	mu.Lock()
	battery = 10
	mu.Unlock()
	waitHeld(0)
	mu.Lock()
	battery = 90
	mu.Unlock()
	waitHeld(1)
	cancel()
	<-done
	waitHeld(0)
}

func TestParseBattery(t *testing.T) {
	out := "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t54%; discharging; 3:10 remaining present: true\n"
	if p, on, ok := parseBattery(out); !ok || !on || p != 54 {
		t.Fatal(p, on, ok)
	}
	if p, on, ok := parseBattery("Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t100%; charged;\n"); !ok || on || p != 100 {
		t.Fatal(p, on, ok)
	}
	if _, _, ok := parseBattery("Now drawing from 'AC Power'\n"); ok {
		t.Fatal("desktop Mac reported a battery")
	}
}
