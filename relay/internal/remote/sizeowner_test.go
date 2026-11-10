package remote

import (
	"context"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// The owner fields as this Mac's clients see them, through the link's
// events (list, changed) and a watcher: another device's ownership with
// its name, this Mac's own as absent, the host's as sizeOwnerHost. An
// older host sends none of the fields and no capability: all absent.
func TestFleetSizeOwnerEvents(t *testing.T) {
	f := New(Options{})
	f.device = "mac-1"
	m := &machine{id: "host-M", short: "M", agents: map[string]wire.Agent{}}
	l := &link{f: f, m: m}
	m.link = l
	f.mu.Lock()
	f.machines[m.id] = m
	f.mu.Unlock()

	seen := make(chan wire.Agent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Watch(ctx, func(a wire.Agent) { seen <- a }, func(string, string, string) {})
	}()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(5 * time.Second); ; {
		f.mu.Lock()
		n := len(f.watchers)
		f.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the watcher never registered")
		}
		time.Sleep(time.Millisecond)
	}
	next := func(what string) wire.Agent {
		t.Helper()
		select {
		case a := <-seen:
			return a
		case <-time.After(5 * time.Second):
			t.Fatalf("no changed event: %s", what)
		}
		return wire.Agent{}
	}

	// An older host: no capability, none of the fields.
	f.event(l, agentlink.Event{Full: true, Machine: "M", List: []wire.Agent{{ID: "a1"}}})
	if a := next("old host list"); a.ID != "M/a1" || a.SizeOwner != "" || a.SizeOwnerName != "" || a.SizeOwnerHost {
		t.Fatalf("old host %+v", a)
	}
	if m.state.Capabilities.SizeOwner {
		t.Fatal("old host has the capability")
	}

	for _, tc := range []struct {
		what string
		in   wire.Agent
		want wire.Agent
	}{
		{"another device owns", wire.Agent{ID: "a1", SizeOwner: "phone-1", SizeOwnerName: "iPhone"},
			wire.Agent{SizeOwner: "phone-1", SizeOwnerName: "iPhone"}},
		{"this Mac owns", wire.Agent{ID: "a1", SizeOwner: "mac-1", SizeOwnerName: "Laptop"}, wire.Agent{}},
		{"the host owns", wire.Agent{ID: "a1", SizeOwnerHost: true}, wire.Agent{SizeOwnerHost: true}},
		{"nobody owns", wire.Agent{ID: "a1"}, wire.Agent{}},
	} {
		in := tc.in
		f.event(l, agentlink.Event{Changed: &in})
		a := next(tc.what)
		if a.ID != "M/a1" || a.SizeOwner != tc.want.SizeOwner || a.SizeOwnerName != tc.want.SizeOwnerName || a.SizeOwnerHost != tc.want.SizeOwnerHost {
			t.Fatalf("%s: the client sees %+v", tc.what, a)
		}
		if got := f.Agents(); len(got) != 1 || got[0].SizeOwner != tc.want.SizeOwner || got[0].SizeOwnerHost != tc.want.SizeOwnerHost {
			t.Fatalf("%s: agents %+v", tc.what, got)
		}
	}
}

// A host publishes the controller that owns a terminal's size. When that
// controller is this Mac (its own tile owns a remote agent's size), the
// owner is local in this Mac's naming, so its tiles never turn into fit
// views of their own attach; another device's ownership passes through.
func TestNamedSizeOwner(t *testing.T) {
	f, m := &Fleet{device: "mac-1"}, &machine{short: "M"}
	own := f.named(m, wire.Agent{ID: "a7f3k2", SizeOwner: "mac-1", SizeOwnerName: "Laptop"})
	if own.ID != "M/a7f3k2" || own.SizeOwner != "" || own.SizeOwnerName != "" {
		t.Fatalf("own attach %+v", own)
	}
	phone := f.named(m, wire.Agent{ID: "a7f3k2", SizeOwner: "phone-1", SizeOwnerName: "iPhone"})
	if phone.SizeOwner != "phone-1" || phone.SizeOwnerName != "iPhone" {
		t.Fatalf("phone %+v", phone)
	}
	// The host owning it passes through: there, the agent's machine.
	if host := f.named(m, wire.Agent{ID: "x", SizeOwnerHost: true}); !host.SizeOwnerHost || host.SizeOwner != "" {
		t.Fatalf("host %+v", host)
	}
	if none := (&Fleet{}).named(m, wire.Agent{ID: "x"}); none.SizeOwner != "" || none.SizeOwnerHost {
		t.Fatalf("none %+v", none)
	}
}
