package host

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/derzierau/hesper/relay/pkg/agentlink"
	"github.com/derzierau/hesper/relay/pkg/devicekey"
	"github.com/derzierau/hesper/relay/pkg/protocol"
	"github.com/derzierau/hesper/relay/pkg/wire"
)

// A phone that attaches as the size owner (Take over) is published in the
// agent's info, sizeOwner and sizeOwnerName, with an agent event on each
// change; its detach clears both. The snapshot says the host does it.
func TestAttachPublishesSizeOwner(t *testing.T) {
	reg, project := testRegistry(t)
	a, err := reg.Spawn(wire.SpawnParams{Project: project, Task: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Agents: reg}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	snap, err := s.Snapshot(ctx)
	if err != nil || !snap.Capabilities.SizeOwner {
		t.Fatalf("snapshot capabilities %+v %v", snap.Capabilities, err)
	}

	in, out := make(chan []byte, 64), make(chan []byte, 64)
	lctx, lcancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer workers.Wait()
	defer lcancel()
	link := &hostLink{id: "link-1", device: "phone-1", conn: agentlink.New(memStream{in: in, out: out}), ctx: lctx, cancel: lcancel,
		opened: time.Now(), chans: map[uint32]*hostChan{}}
	s.links.add(link)
	workers.Add(1)
	go func() {
		// The phone reads what the agent sends.
		defer workers.Done()
		for {
			select {
			case <-out:
			case <-lctx.Done():
				return
			}
		}
	}()

	sub := reg.Subscribe()
	defer reg.Unsubscribe(sub)
	waitNote := func(what string, ok func(*wire.Agent) bool) {
		t.Helper()
		done := make(chan struct{})
		timer := time.AfterFunc(5*time.Second, func() { close(done) })
		defer timer.Stop()
		for {
			notes := sub.Wait(done)
			if notes == nil {
				t.Fatalf("no agent event: %s", what)
			}
			for _, n := range notes {
				if n.Agent != nil && n.Agent.ID == a.ID && ok(n.Agent) {
					return
				}
			}
		}
	}

	phone := Caller{Device: "phone-1", Name: "iPhone", Rights: []string{devicekey.Observe, devicekey.Type}, Verified: true}
	req, _ := json.Marshal(wire.AttachRequest{Attach: a.ID, Mode: wire.ModeRW, Owner: true, Cols: 46, Rows: 19})
	if _, err := s.Execute(WithCaller(ctx, phone), protocol.Message{Method: "agents.attach", ControllerID: "phone-1",
		Params: protocol.JSON(map[string]any{"link": "link-1", "ch": 3, "mode": wire.ModeRW, "request": json.RawMessage(req)}), Deadline: time.Now().Add(10 * time.Second).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	waitNote("the phone owns the size", func(g *wire.Agent) bool {
		return g.SizeOwner == "phone-1" && g.SizeOwnerName == "iPhone" && g.Size == wire.Size{Cols: 46, Rows: 19}
	})
	got, _ := reg.Get(a.ID)
	data, _ := json.Marshal(got)
	if !strings.Contains(string(data), `"sizeOwner":"phone-1","sizeOwnerName":"iPhone"`) {
		t.Fatalf("agent info %s", data)
	}

	// The phone lets go (X): both clear, and are omitted.
	link.closeChan(3, false)
	waitNote("the owner cleared", func(g *wire.Agent) bool { return g.SizeOwner == "" && g.SizeOwnerName == "" })
	got, _ = reg.Get(a.ID)
	data, _ = json.Marshal(got)
	if strings.Contains(string(data), "sizeOwner") {
		t.Fatalf("agent info after detach %s", data)
	}

	// A lost link closes every channel, then a fresh link can take the
	// size again. Neither the old owner nor its name survives the loss.
	if _, err := s.Execute(WithCaller(ctx, phone), protocol.Message{Method: "agents.attach", ControllerID: "phone-1",
		Params: protocol.JSON(map[string]any{"link": "link-1", "ch": 4, "mode": wire.ModeRW, "request": json.RawMessage(req)}), Deadline: time.Now().Add(10 * time.Second).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	waitNote("the phone reattached", func(g *wire.Agent) bool { return g.SizeOwner == "phone-1" })
	link.closeAll()
	waitNote("the lost link released the size", func(g *wire.Agent) bool { return g.SizeOwner == "" && g.SizeOwnerName == "" })
	lcancel()
	s.links.drop(link)

	reconnectIn, reconnectOut := make(chan []byte, 64), make(chan []byte, 64)
	reconnectCtx, reconnectCancel := context.WithCancel(ctx)
	defer reconnectCancel()
	reconnected := &hostLink{id: "link-2", device: "phone-1", conn: agentlink.New(memStream{in: reconnectIn, out: reconnectOut}),
		ctx: reconnectCtx, cancel: reconnectCancel, opened: time.Now(), chans: map[uint32]*hostChan{}}
	s.links.add(reconnected)
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-reconnectOut:
			case <-reconnectCtx.Done():
				return
			}
		}
	}()
	if _, err := s.Execute(WithCaller(ctx, phone), protocol.Message{Method: "agents.attach", ControllerID: "phone-1",
		Params: protocol.JSON(map[string]any{"link": "link-2", "ch": 3, "mode": wire.ModeRW, "request": json.RawMessage(req)}), Deadline: time.Now().Add(10 * time.Second).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	waitNote("the reconnect owns the size", func(g *wire.Agent) bool {
		return g.SizeOwner == "phone-1" && g.SizeOwnerName == "iPhone" && g.Size == wire.Size{Cols: 46, Rows: 19}
	})
	reconnected.closeAll()
	waitNote("the reconnect released the size", func(g *wire.Agent) bool { return g.SizeOwner == "" && g.SizeOwnerName == "" })
}
